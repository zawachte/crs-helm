/*


Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controllers

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	"helm.sh/helm/pkg/strvals"
	"helm.sh/helm/v3/pkg/chart"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	kerrors "k8s.io/apimachinery/pkg/util/errors"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1alpha3"
	"sigs.k8s.io/cluster-api/controllers/remote"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/cluster-api/util/conditions"
	"sigs.k8s.io/cluster-api/util/patch"
	"sigs.k8s.io/cluster-api/util/predicates"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/source"

	addonsv1 "github.com/zawachte-msft/csr-helm/api/v1"

	"github.com/zawachte-msft/csr-helm/pkg/helm"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

// HelmClusterResourceSetReconciler reconciles a ClusterResourceSet object
type HelmClusterResourceSetReconciler struct {
	Client  client.Client
	Tracker *remote.ClusterCacheTracker
	Scheme  *runtime.Scheme
	Log     logr.Logger
}

// +kubebuilder:rbac:groups=addons.x-k8s.io,resources=helmaddonsresourcesets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=addons.x-k8s.io,resources=helmaddonsresourcesets/status,verbs=get;update;patch

func (r *HelmClusterResourceSetReconciler) Reconcile(ctx context.Context, req ctrl.Request) (_ ctrl.Result, reterr error) {
	log := r.Log.WithValues("helmaddonsresourceset", req.NamespacedName)

	// Fetch the HelmClusterResourceSet instance.
	clusterResourceSet := &addonsv1.HelmClusterResourceSet{}
	if err := r.Client.Get(ctx, req.NamespacedName, clusterResourceSet); err != nil {
		if apierrors.IsNotFound(err) {
			// Object not found, return.  Created objects are automatically garbage collected.
			// For additional cleanup logic use finalizers.
			return ctrl.Result{}, nil
		}
		// Error reading the object - requeue the request.
		return ctrl.Result{}, err
	}

	// Initialize the patch helper.
	patchHelper, err := patch.NewHelper(clusterResourceSet, r.Client)
	if err != nil {
		return ctrl.Result{}, err
	}

	defer func() {
		// Always attempt to Patch the HelmClusterResourceSet object and status after each reconciliation.
		if err := patchHelper.Patch(ctx, clusterResourceSet, patch.WithStatusObservedGeneration{}); err != nil {
			reterr = kerrors.NewAggregate([]error{reterr, err})
		}
	}()

	clusters, err := r.getClustersByHelmClusterResourceSetSelector(ctx, clusterResourceSet)
	if err != nil {
		log.Error(err, "Failed fetching clusters that matches HelmClusterResourceSet labels", "HelmClusterResourceSet", clusterResourceSet.Name)
		conditions.MarkFalse(clusterResourceSet, addonsv1.ResourcesAppliedCondition, addonsv1.ClusterMatchFailedReason, clusterv1.ConditionSeverityWarning, err.Error())
		return ctrl.Result{}, err
	}

	// Add finalizer first if not exist to avoid the race condition between init and delete
	if !controllerutil.ContainsFinalizer(clusterResourceSet, addonsv1.HelmClusterResourceSetFinalizer) {
		controllerutil.AddFinalizer(clusterResourceSet, addonsv1.HelmClusterResourceSetFinalizer)
		return ctrl.Result{}, nil
	}

	// Handle deletion reconciliation loop.
	if !clusterResourceSet.ObjectMeta.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, clusters, clusterResourceSet)
	}

	for _, cluster := range clusters {
		if err := r.ApplyHelmClusterResourceSet(ctx, cluster, clusterResourceSet); err != nil {
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, nil
}

func (r *HelmClusterResourceSetReconciler) SetupWithManager(mgr ctrl.Manager, options controller.Options) error {
	_, err := ctrl.NewControllerManagedBy(mgr).
		For(&addonsv1.HelmClusterResourceSet{}).
		Watches(
			&source.Kind{Type: &clusterv1.Cluster{}},
			handler.EnqueueRequestsFromMapFunc(r.clusterToHelmClusterResourceSet),
		).
		WithOptions(options).
		WithEventFilter(predicates.ResourceNotPaused(r.Log)).
		Build(r)
	if err != nil {
		return errors.Wrap(err, "failed setting up with a controller manager")
	}

	return nil
}

// clusterToHelmClusterResourceSet is mapper function that maps clusters to HelmClusterResourceSet
func (r *HelmClusterResourceSetReconciler) clusterToHelmClusterResourceSet(o client.Object) []ctrl.Request {
	result := []ctrl.Request{}

	cluster, ok := o.(*clusterv1.Cluster)
	if !ok {
		panic(fmt.Sprintf("Expected a Cluster but got a %T", o))
	}

	resourceList := &addonsv1.HelmClusterResourceSetList{}
	if err := r.Client.List(context.Background(), resourceList, client.InNamespace(cluster.Namespace)); err != nil {
		r.Log.Error(err, "failed to list ClusterResourceSet")
		return nil
	}

	labels := labels.Set(cluster.GetLabels())
	for i := range resourceList.Items {
		rs := &resourceList.Items[i]

		selector, err := metav1.LabelSelectorAsSelector(&rs.Spec.ClusterSelector)
		if err != nil {
			r.Log.Error(err, "unable to convert ClusterSelector to selector")
			return nil
		}

		// If a ClusterResourceSet has a nil or empty selector, it should match nothing, not everything.
		if selector.Empty() {
			return nil
		}

		if !selector.Matches(labels) {
			continue
		}

		name := client.ObjectKey{Namespace: rs.Namespace, Name: rs.Name}
		result = append(result, ctrl.Request{NamespacedName: name})
	}
	return result
}

// ApplyClusterResourceSet applies resources in a ClusterResourceSet to a Cluster. Once applied, a record will be added to the
// cluster's ClusterResourceSetBinding.
// In ApplyOnce strategy, resources are applied only once to a particular cluster. ClusterResourceSetBinding is used to check if a resource is applied before.
// It applies resources best effort and continue on scenarios like: unsupported resource types, failure during creation, missing resources.
// TODO: If a resource already exists in the cluster but not applied by ClusterResourceSet, the resource will be updated ?
func (r *HelmClusterResourceSetReconciler) ApplyHelmClusterResourceSet(ctx context.Context, cluster *clusterv1.Cluster, clusterResourceSet *addonsv1.HelmClusterResourceSet) error {
	r.Log.Info("Applying ClusterResourceSet to cluster")

	restConfig, err := remote.RESTConfig(ctx, r.Client, util.ObjectKey(cluster))
	if err != nil {
		conditions.MarkFalse(clusterResourceSet, addonsv1.ResourcesAppliedCondition, addonsv1.RemoteClusterClientFailedReason, clusterv1.ConditionSeverityError, err.Error())
		return err
	}

	reference := clusterResourceSet.Spec.RegistryReference

	helmParams := helm.HelmParams{
		Config:    restConfig,
		Namespace: clusterResourceSet.Namespace,
		Logger:    r.Log,
	}

	helmClient, err := helm.New(helmParams)
	if err != nil {
		return errors.Wrapf(err, "Failed to get helm client for %s", clusterResourceSet.Name)
	}

	ch, err := helmClient.PullChart(reference)
	if err != nil {
		return errors.Wrapf(err, "Failed to pull chart for addon %s", clusterResourceSet.Name)
	}

	ok := r.chartNeedsUpgrade(helmClient, ch, clusterResourceSet)
	if ok {
		// upgrade
		r.Log.Info("Detected Helm Chart Upgrade")
		err = helmClient.UpgradeChart(ch, clusterResourceSet.Name)
		if err != nil {
			return errors.Wrapf(err, "Failed to install chart")
		}

		conditions.MarkTrue(clusterResourceSet, addonsv1.ResourcesAppliedCondition)
		return nil
	}

	vals, err := getHelmValuesFromProviderVariables(clusterResourceSet.Spec.HelmValues)
	if err != nil {
		return errors.Wrapf(err, "Failed to parse helm variables")
	}

	err = helmClient.InstallChart(ch, clusterResourceSet.Name, vals)
	if err != nil {
		return errors.Wrapf(err, "Failed to install chart")
	}

	conditions.MarkTrue(clusterResourceSet, addonsv1.ResourcesAppliedCondition)

	return nil
}

func (r *HelmClusterResourceSetReconciler) chartNeedsUpgrade(helmClient *helm.Client, requestedChart *chart.Chart, clusterResourceSet *addonsv1.HelmClusterResourceSet) bool {
	currentChart, err := helmClient.GetChart(clusterResourceSet.Name)
	if err != nil {
		r.Log.Info("Helm Release not found, going to install", "release name", clusterResourceSet.Name)
		return false
	}

	if currentChart.Metadata.Version != requestedChart.Metadata.Version {
		return true
	}

	return false
}

// getClustersByClusterResourceSetSelector fetches Clusters matched by the ClusterResourceSet's label selector that are in the same namespace as the ClusterResourceSet object.
func (r *HelmClusterResourceSetReconciler) getClustersByHelmClusterResourceSetSelector(ctx context.Context, clusterResourceSet *addonsv1.HelmClusterResourceSet) ([]*clusterv1.Cluster, error) {

	clusterList := &clusterv1.ClusterList{}
	selector, err := metav1.LabelSelectorAsSelector(&clusterResourceSet.Spec.ClusterSelector)
	if err != nil {
		return nil, errors.Wrap(err, "unable to convert selector")
	}

	// If a ClusterResourceSet has a nil or empty selector, it should match nothing, not everything.
	if selector.Empty() {
		r.Log.Info("Empty ClusterResourceSet selector: No clusters are selected.")
		return nil, nil
	}

	if err := r.Client.List(ctx, clusterList, client.InNamespace(clusterResourceSet.Namespace), client.MatchingLabelsSelector{Selector: selector}); err != nil {
		return nil, errors.Wrap(err, "failed to list clusters")
	}

	clusters := []*clusterv1.Cluster{}
	for i := range clusterList.Items {
		c := &clusterList.Items[i]
		if c.DeletionTimestamp.IsZero() {
			clusters = append(clusters, c)
		}
	}
	return clusters, nil
}

// reconcileDelete removes the deleted ClusterResourceSet from all the ClusterResourceSetBindings it is added to.
func (r *HelmClusterResourceSetReconciler) reconcileDelete(ctx context.Context, clusters []*clusterv1.Cluster, crs *addonsv1.HelmClusterResourceSet) (ctrl.Result, error) {

	controllerutil.RemoveFinalizer(crs, addonsv1.HelmClusterResourceSetFinalizer)
	return ctrl.Result{}, nil
}

func getHelmValuesFromProviderVariables(helmValues []addonsv1.HelmValue) (map[string]interface{}, error) {
	vals := make(map[string]interface{})

	helmString := ""
	for _, helmValue := range helmValues {
		b, err := strconv.ParseBool(helmValue.Value)
		if err != nil {
			helmString += fmt.Sprintf("%s=%s,", helmValue.Key, helmValue.Value)
		} else {
			if b {
				helmString += fmt.Sprintf("%s=%s,", helmValue.Key, "true")
			} else {
				helmString += fmt.Sprintf("%s=%s,", helmValue.Key, "false")
			}
		}
	}

	helmString = strings.TrimSuffix(helmString, ",")

	if err := strvals.ParseInto(helmString, vals); err != nil {
		return vals, errors.Wrapf(err, "Failed parsing input variables")
	}

	return vals, nil
}
