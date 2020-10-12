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

package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1alpha3"
)

const (
	// HelmClusterResourceSetFinalizer is added to the HelmClusterResourceSet object for additional cleanup logic on deletion.
	HelmClusterResourceSetFinalizer = "addons.cluster.x-k8s.io"
)

// HelmClusterResourceSetSpec defines the desired state of HelmClusterResourceSet
type HelmClusterResourceSetSpec struct {
	// It must match the Cluster labels. This field is immutable.
	ClusterSelector metav1.LabelSelector `json:"clusterSelector"`

	// Strategy is the strategy to be used during applying resources. Defaults to ApplyOnce. This field is immutable.
	// +kubebuilder:validation:Enum=ApplyOnce
	// +optional
	Strategy          string      `json:"strategy,omitempty"`
	TargetNamespace   string      `json:"targetNamespace"`
	HelmValues        []HelmValue `json:"helmValues,omitempty"`
	RegistryReference string      `json:"registryReference,omitempty"`
}

// HelmValue
type HelmValue struct {
	Key   string `json:"key,omitempty"`
	Value string `json:"value,omitempty"`
}

// HelmClusterResourceSetStatus defines the observed state of HelmClusterResourceSet
type HelmClusterResourceSetStatus struct {
	// ObservedGeneration reflects the generation of the most recently observed HelmClusterResourceSet.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions defines current state of the HelmClusterResourceSet.
	// +optional
	Conditions clusterv1.Conditions `json:"conditions,omitempty"`
}

func (m *HelmClusterResourceSet) GetConditions() clusterv1.Conditions {
	return m.Status.Conditions
}

func (m *HelmClusterResourceSet) SetConditions(conditions clusterv1.Conditions) {
	m.Status.Conditions = conditions
}

// +kubebuilder:object:root=true

// HelmClusterResourceSet is the Schema for the helmaddonsresourcesets API
type HelmClusterResourceSet struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   HelmClusterResourceSetSpec   `json:"spec,omitempty"`
	Status HelmClusterResourceSetStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// HelmClusterResourceSetList contains a list of HelmClusterResourceSet
type HelmClusterResourceSetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []HelmClusterResourceSet `json:"items"`
}

func init() {
	SchemeBuilder.Register(&HelmClusterResourceSet{}, &HelmClusterResourceSetList{})
}
