package helm

import (
	"bytes"
	"context"
	"fmt"
	"io/ioutil"

	"github.com/containerd/containerd/remotes"
	"github.com/containerd/containerd/remotes/docker"
	"github.com/deislabs/oras/pkg/content"
	"github.com/deislabs/oras/pkg/oras"
	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/cli"

	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/getter"
	"helm.sh/helm/v3/pkg/helmpath"
	"helm.sh/helm/v3/pkg/kube"
	"helm.sh/helm/v3/pkg/storage"
	"helm.sh/helm/v3/pkg/storage/driver"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/kubectl/pkg/cmd/util"
)

const VERSION = "v3"

var (
	repositoryConfig = helmpath.ConfigPath("repositories.yaml")
	repositoryCache  = helmpath.CachePath("repository")
	pluginsDir       = helmpath.DataPath("plugins")

	caFile   = "/tmp/ca.crt"
	certFile = "/tmp/cert.crt"
	keyFile  = "/tmp/cert.key"
)

type HelmOptions struct {
	Driver    string
	Namespace string
}

const (
	// HelmChartConfigMediaType is the reserved media type for the Helm chart manifest config
	HelmChartConfigMediaType = "application/vnd.cncf.helm.config.v1+json"

	// HelmChartContentLayerMediaType is the reserved media type for Helm chart package content
	HelmChartContentLayerMediaType = "application/tar+gzip"
)

type Client struct {
	actionConfig *action.Configuration
	resolver     remotes.Resolver
	logger       logr.Logger
	namespace    string
}

type HelmParams struct {
	Config            *rest.Config
	Username          string
	Password          string
	ContainerRegistry string
	Namespace         string
	Logger            logr.Logger
	Driver            string
}

func New(params HelmParams) (*Client, error) {

	cli := &Client{
		logger: params.Logger,
	}

	actionConfig, err := newActionConfig(params.Config, cli.helmlog, params.Namespace, params.Driver)
	if err != nil {
		return nil, err
	}

	resolver := docker.NewResolver(docker.ResolverOptions{
		Credentials: func(hostName string) (string, string, error) {
			return params.Username, params.Password, nil
		},
	})

	cli.actionConfig = actionConfig
	cli.resolver = resolver
	cli.namespace = params.Namespace

	return cli, nil
}

type infoLogFunc func(string, ...interface{})

func (c *Client) helmlog(format string, v ...interface{}) {
	format = fmt.Sprintf(" %s\n", format)
	c.logger.Info(fmt.Sprintf(format, v...))
}

func (c *Client) PullChart(ref string) (*chart.Chart, error) {

	ctx := context.Background()
	memStore := content.NewMemoryStore()
	allowedMediaTypes := []string{
		HelmChartConfigMediaType,
		HelmChartContentLayerMediaType,
	}
	_, content, err := oras.Pull(ctx, c.resolver, ref, memStore, oras.WithPullEmptyNameAllowed(), oras.WithAllowedMediaTypes(allowedMediaTypes))
	if err != nil {
		return nil, errors.Wrapf(err, "oras pull failed %s", ref)
	}

	if len(content) != 2 {
		return nil, errors.Errorf("%s has invalid configuration", ref)
	}

	for _, chartBytes := range content {

		if chartBytes.MediaType == HelmChartConfigMediaType {
			continue
		}

		_, byt, ok := memStore.Get(chartBytes)
		if !ok {
			return nil, err
		}

		ch, err := loader.LoadArchive(bytes.NewBuffer(byt))
		if err != nil {
			return nil, err
		}
		return ch, nil
	}

	return nil, errors.Errorf("%s has invalid configuration", ref)
}

func fixReleaseName(releaseName string) string {
	if len(releaseName) > 50 {
		return releaseName[0:50]
	}
	return releaseName
}

func (c *Client) InstallChart(ch *chart.Chart, releaseName string, vals map[string]interface{}) error {

	fixedReleaseName := fixReleaseName(releaseName)

	_, err := c.GetChart(fixedReleaseName)
	if err == nil {
		// release already found
		return nil
	}

	inst := action.NewInstall(c.actionConfig)

	inst.ReleaseName = fixedReleaseName
	inst.Namespace = c.namespace
	inst.DisableOpenAPIValidation = true

	_, err = inst.Run(ch, vals)
	if err != nil {
		return err
	}

	return nil
}

func (c *Client) UninstallChart(releaseName string) error {

	fixedReleaseName := fixReleaseName(releaseName)

	uninst := action.NewUninstall(c.actionConfig)
	_, err := uninst.Run(fixedReleaseName)
	if err != nil {
		return err
	}

	return nil
}

func (c *Client) UpgradeChart(ch *chart.Chart, releaseName string) error {

	fixedReleaseName := fixReleaseName(releaseName)

	upgrade := action.NewUpgrade(c.actionConfig)

	vals := make(map[string]interface{})
	upgrade.Namespace = c.namespace

	_, err := upgrade.Run(fixedReleaseName, ch, vals)
	if err != nil {
		return err
	}

	return nil
}

func (c *Client) GetChart(releaseName string) (*chart.Chart, error) {

	fixedReleaseName := fixReleaseName(releaseName)

	get := action.NewGet(c.actionConfig)
	release, err := get.Run(fixedReleaseName)
	if err != nil {
		return nil, err
	}

	return release.Chart, nil
}

func newActionConfig(config *rest.Config, logFunc infoLogFunc, namespace, driver string) (*action.Configuration, error) {

	restClientGetter := newConfigFlags(config, namespace)
	kubeClient := &kube.Client{
		Factory: util.NewFactory(restClientGetter),
		Log:     logFunc,
	}
	client, err := kubeClient.Factory.KubernetesClientSet()
	if err != nil {
		return nil, err
	}

	store, err := newStorageDriver(client, logFunc, namespace, driver)
	if err != nil {
		return nil, err
	}

	return &action.Configuration{
		RESTClientGetter: restClientGetter,
		Releases:         store,
		KubeClient:       kubeClient,
		Log:              logFunc,
	}, nil
}

func newConfigFlags(config *rest.Config, namespace string) *genericclioptions.ConfigFlags {

	// Write the config certificates to file so that
	// helm client can read them.
	ioutil.WriteFile(caFile, config.CAData, 0644)
	ioutil.WriteFile(certFile, config.CertData, 0644)
	ioutil.WriteFile(keyFile, config.KeyData, 0644)

	return &genericclioptions.ConfigFlags{
		Namespace:   &namespace,
		APIServer:   &config.Host,
		CAFile:      &caFile,
		CertFile:    &certFile,
		KeyFile:     &keyFile,
		BearerToken: &config.BearerToken,
	}
}

func newStorageDriver(client *kubernetes.Clientset, logFunc infoLogFunc, namespace, d string) (*storage.Storage, error) {
	switch d {
	case "secret", "secrets", "":
		s := driver.NewSecrets(client.CoreV1().Secrets(namespace))
		s.Log = logFunc
		return storage.Init(s), nil
	case "configmap", "configmaps":
		c := driver.NewConfigMaps(client.CoreV1().ConfigMaps(namespace))
		c.Log = logFunc
		return storage.Init(c), nil
	case "memory":
		m := driver.NewMemory()
		return storage.Init(m), nil
	default:
		return nil, fmt.Errorf("unsupported storage driver '%s'", d)
	}
}

func getterProviders() getter.Providers {
	return getter.All(&cli.EnvSettings{
		RepositoryConfig: repositoryConfig,
		RepositoryCache:  repositoryCache,
		PluginsDirectory: pluginsDir,
	})
}
