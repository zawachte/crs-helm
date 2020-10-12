module github.com/zawachte-msft/csr-helm

go 1.13

require (
	github.com/containerd/containerd v1.4.1
	github.com/deislabs/oras v0.8.1
	github.com/go-logr/logr v0.2.1
	github.com/onsi/ginkgo v1.14.1
	github.com/onsi/gomega v1.10.2
	github.com/pkg/errors v0.9.1
	helm.sh/helm v2.16.12+incompatible
	helm.sh/helm/v3 v3.1.0-rc.1.0.20201005141519-fc9b46067f8f
	k8s.io/apiextensions-apiserver v0.19.2
	k8s.io/apimachinery v0.19.2
	k8s.io/cli-runtime v0.19.2
	k8s.io/client-go v0.19.2
	k8s.io/kubectl v0.19.2
	rsc.io/letsencrypt v0.0.3 // indirect
	sigs.k8s.io/cluster-api v0.3.11-0.20201009213249-3dd38537ad8e
	sigs.k8s.io/controller-runtime v0.7.0-alpha.2.0.20201009200249-bd97e08c43d5
)
