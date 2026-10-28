// Package kube connects to the Kubernetes API, from inside a pod or from a
// kubeconfig on a laptop.
package kube

import (
	"fmt"
	"os"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	metrics "k8s.io/metrics/pkg/client/clientset/versioned"
)

// Mode says how clusterview reached the cluster.
type Mode string

const (
	// InCluster means clusterview runs as a pod and uses its service account.
	InCluster Mode = "in-cluster"
	// Kubeconfig means clusterview runs outside the cluster and uses a kubeconfig.
	Kubeconfig Mode = "kubeconfig"
)

// Client is a connection to one cluster.
type Client struct {
	Clientset kubernetes.Interface
	// Metrics reads metrics-server. Calls fail when it isn't installed.
	Metrics metrics.Interface
	Mode    Mode
	// Context is the kubeconfig context in use; empty in-cluster.
	Context string
}

// Connect uses the pod's service account when running inside Kubernetes, and
// otherwise the kubeconfig at kubeconfigPath (or the default location when
// empty), in kubeContext (or the current context when empty).
func Connect(kubeconfigPath, kubeContext string) (*Client, error) {
	if kubeconfigPath == "" && kubeContext == "" && os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
		cfg, err := rest.InClusterConfig()
		if err != nil {
			return nil, fmt.Errorf("in-cluster config: %w", err)
		}
		return newClient(cfg, InCluster, "")
	}

	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfigPath != "" {
		rules.ExplicitPath = kubeconfigPath
	}
	loader := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{CurrentContext: kubeContext})
	cfg, err := loader.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("kubeconfig: %w", err)
	}
	raw, err := loader.RawConfig()
	if err != nil {
		return nil, fmt.Errorf("kubeconfig: %w", err)
	}
	return newClient(cfg, Kubeconfig, cmpOr(kubeContext, raw.CurrentContext))
}

func newClient(cfg *rest.Config, mode Mode, context string) (*Client, error) {
	cfg.UserAgent = "clusterview"
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("kubernetes client: %w", err)
	}
	mc, err := metrics.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("metrics client: %w", err)
	}
	return &Client{Clientset: cs, Metrics: mc, Mode: mode, Context: context}, nil
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
