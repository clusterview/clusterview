package cluster

import (
	"slices"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func kubeService(ns, name string, selector map[string]string, ports ...int32) *corev1.Service {
	s := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec:       corev1.ServiceSpec{Selector: selector, Type: corev1.ServiceTypeClusterIP},
	}
	for _, p := range ports {
		s.Spec.Ports = append(s.Spec.Ports, corev1.ServicePort{Port: p})
	}
	return s
}

// app makes a Deployment with one pod labelled app=<name>.
func app(ns, name string, env ...corev1.EnvVar) (*appsv1.Deployment, *appsv1.ReplicaSet, *corev1.Pod) {
	d := deployment(ns, name, 1, 1)
	d.Spec.Template.Spec.Containers[0].Env = env
	uid := types.UID("rs-" + ns + "-" + name)
	rs := replicaSet(ns, name+"-5d8f7c9b6", name, uid)
	p := pod(ns, name+"-5d8f7c9b6-aaaaa", controller("ReplicaSet", name+"-5d8f7c9b6", uid))
	p.Labels = map[string]string{"app": name}
	return d, rs, p
}

func TestLinks(t *testing.T) {
	objs := Objects{}
	addApp := func(ns, name string, env ...corev1.EnvVar) {
		d, rs, p := app(ns, name, env...)
		objs.Deployments = append(objs.Deployments, d)
		objs.ReplicaSets = append(objs.ReplicaSets, rs)
		objs.Pods = append(objs.Pods, p)
	}
	addApp("shop", "api",
		corev1.EnvVar{Name: "DATABASE_URL", Value: "postgres://app@orders-db-rw.data.svc:5432/orders"},
		corev1.EnvVar{Name: "RABBITMQ_HOST", Value: "events.data"},
		corev1.EnvVar{Name: "CACHE_HOST", Value: "redis"},
		corev1.EnvVar{Name: "FEATURE_BACKEND", Value: "search-v2"},         // not a Service
		corev1.EnvVar{Name: "DEFAULT_THEME", Value: "worker"},              // a Service, but not an address
		corev1.EnvVar{Name: "PASSWORD", ValueFrom: &corev1.EnvVarSource{}}, // never read
	)
	addApp("shop", "redis")
	addApp("shop", "worker")
	objs.Deployments[0].Spec.Template.Spec.Containers[0].Args = []string{"--search", "search:9200"}
	addApp("shop", "search")

	// Postgres pods run straight from an operator's custom resource.
	pg := pod("data", "orders-db-1", crOwner("postgresql.cnpg.io/v1", "Cluster", "orders-db"))
	pg.Labels = map[string]string{"cnpg.io/cluster": "orders-db", "role": "primary"}
	rmq := pod("data", "events-server-0", controller("StatefulSet", "events-server", "ss"))
	rmq.Labels = map[string]string{"app.kubernetes.io/name": "events"}
	objs.Pods = append(objs.Pods, pg, rmq)
	objs.StatefulSets = []*appsv1.StatefulSet{{
		ObjectMeta: metav1.ObjectMeta{Namespace: "data", Name: "events-server", OwnerReferences: crOwner("rabbitmq.com/v1beta1", "RabbitmqCluster", "events")},
		Spec:       appsv1.StatefulSetSpec{Replicas: ptr(int32(1))},
		Status:     appsv1.StatefulSetStatus{ReadyReplicas: 1},
	}}

	lb := kubeService("shop", "api", map[string]string{"app": "api"}, 80)
	lb.Spec.Type = corev1.ServiceTypeLoadBalancer
	lb.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{Hostname: "abc.elb.amazonaws.com"}}
	objs.KubeServices = []*corev1.Service{
		lb,
		kubeService("shop", "redis", map[string]string{"app": "redis"}, 6379),
		kubeService("shop", "worker", map[string]string{"app": "worker"}, 8080),
		kubeService("shop", "search", map[string]string{"app": "search"}, 9200),
		kubeService("data", "orders-db-rw", map[string]string{"cnpg.io/cluster": "orders-db", "role": "primary"}, 5432),
		kubeService("data", "events", map[string]string{"app.kubernetes.io/name": "events"}, 5672, 15672),
		{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "external-db"}, Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeExternalName, ExternalName: "db.example.com"}},
	}
	class := "nginx"
	objs.Ingresses = []*networkingv1.Ingress{{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "storefront"},
		Spec: networkingv1.IngressSpec{
			IngressClassName: &class,
			TLS:              []networkingv1.IngressTLS{{Hosts: []string{"shop.example.com"}}},
			Rules: []networkingv1.IngressRule{{
				Host: "shop.example.com",
				IngressRuleValue: networkingv1.IngressRuleValue{HTTP: &networkingv1.HTTPIngressRuleValue{Paths: []networkingv1.HTTPIngressPath{{
					Path:    "/api",
					Backend: networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{Name: "api"}},
				}}}},
			}},
		},
	}}

	s := BuildSnapshot(objs, 1, now)

	got := map[string]string{}
	for _, l := range s.Links {
		if l.From != "Deployment/shop/api" {
			t.Errorf("unexpected link %+v", l)
		}
		got[l.To] = l.Via + " (" + l.Evidence + ")"
	}
	want := map[string]string{
		"postgresql.cnpg.io/Cluster/data/orders-db": "orders-db-rw.data (env DATABASE_URL)",
		"rabbitmq.com/RabbitmqCluster/data/events":  "events.data (env RABBITMQ_HOST)",
		"Deployment/shop/redis":                     "redis.shop (env CACHE_HOST)",
		"Deployment/shop/search":                    "search.shop (command-line arguments)",
	}
	if len(got) != len(want) {
		t.Errorf("links: got %v", got)
	}
	for to, w := range want {
		if got[to] != w {
			t.Errorf("link to %s: got %q, want %q", to, got[to], w)
		}
	}

	api := findService(t, s, "Deployment/shop/api")
	if len(api.Addresses) != 1 || api.Addresses[0].Type != "LoadBalancer" || !slices.Equal(api.Addresses[0].External, []string{"abc.elb.amazonaws.com"}) {
		t.Errorf("addresses: %+v", api.Addresses)
	}
	if len(api.Entrypoints) != 1 || api.Entrypoints[0] != (Entrypoint{Host: "shop.example.com", Path: "/api", TLS: true, Ingress: "shop/storefront", Class: "nginx"}) {
		t.Errorf("entrypoints: %+v", api.Entrypoints)
	}
	rabbit := findService(t, s, "rabbitmq.com/RabbitmqCluster/data/events")
	if len(rabbit.Addresses) != 1 || !slices.Equal(rabbit.Addresses[0].Ports, []string{"5672", "15672"}) {
		t.Errorf("rabbitmq addresses: %+v", rabbit.Addresses)
	}
}

func TestBareAddress(t *testing.T) {
	cases := []struct {
		name, value string
		want        bool
	}{
		{"REDIS_HOST", "redis", true},
		{"ANYTHING", "http://redis/health", true},
		{"ANYTHING", "user@redis", true},
		{"ANYTHING", "redis:6379", true},
		{"PEERS", "a:1,redis:6379", true},
		{"MODE", "redis", false},         // not an address-like variable
		{"REDIS_HOST", "redis-2", false}, // a different name
		{"ANYTHING", "myredis:6379", false},
	}
	for _, c := range cases {
		if got := bareAddress(envVar{c.name, c.value}, "redis"); got != c.want {
			t.Errorf("%s=%s: got %v", c.name, c.value, got)
		}
	}
}
