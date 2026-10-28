package cluster

import (
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func findService(t *testing.T, s Snapshot, id string) Service {
	t.Helper()
	for _, svc := range s.Services {
		if svc.ID == id {
			return svc
		}
	}
	ids := []string{}
	for _, svc := range s.Services {
		ids = append(ids, svc.ID)
	}
	t.Fatalf("service %s not found; have %v", id, ids)
	return Service{}
}

func crOwner(apiVersion, kind, name string) []metav1.OwnerReference {
	return []metav1.OwnerReference{{APIVersion: apiVersion, Kind: kind, Name: name, UID: types.UID(name), Controller: ptr(true)}}
}

func TestOperatorManagedServices(t *testing.T) {
	rmq := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: "data", Name: "events-server", OwnerReferences: crOwner("rabbitmq.com/v1beta1", "RabbitmqCluster", "events")},
		Spec: appsv1.StatefulSetSpec{Replicas: ptr(int32(3)), Template: corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Image: "rabbitmq:4.1-management"}}},
		}},
		Status: appsv1.StatefulSetStatus{ReadyReplicas: 3},
	}
	cnpg := crOwner("postgresql.cnpg.io/v1", "Cluster", "orders-db")
	pg := func(name string, opts ...podOpt) *corev1.Pod {
		p := pod("data", name, cnpg, opts...)
		p.Spec.Containers[0].Image = "ghcr.io/cloudnative-pg/postgresql:17.2"
		return p
	}
	s := BuildSnapshot(Objects{
		StatefulSets: []*appsv1.StatefulSet{rmq},
		Pods:         []*corev1.Pod{pg("orders-db-1"), pg("orders-db-2"), pg("orders-db-3", waiting("CrashLoopBackOff", 4))},
	}, 1, now)

	queue := findService(t, s, "rabbitmq.com/RabbitmqCluster/data/events")
	if queue.Title != "RabbitMQ" || queue.Category != CategoryQueue || queue.DescriptionSource != "operator" || queue.Name != "events" {
		t.Errorf("rabbitmq: %+v", queue)
	}
	if queue.ManagedBy == nil || queue.ManagedBy.Kind != "RabbitmqCluster" || queue.Status != StatusHealthy || queue.Ready != 3 {
		t.Errorf("rabbitmq owner/health: %+v", queue)
	}

	db := findService(t, s, "postgresql.cnpg.io/Cluster/data/orders-db")
	if db.Title != "PostgreSQL" || db.Category != CategoryDatabase || len(db.Workloads) != 3 {
		t.Errorf("postgres: %+v", db)
	}
	// One crash-looping instance makes the whole database need attention.
	if db.Status != StatusDown || db.Ready != 2 || db.Desired != 3 {
		t.Errorf("postgres health: status %s ready %d/%d", db.Status, db.Ready, db.Desired)
	}
	if len(s.Services) != 2 || s.Summary.NeedAttention != 1 {
		t.Errorf("want 2 services, 1 needing attention; got %d, %d", len(s.Services), s.Summary.NeedAttention)
	}
}

func TestChartHookJobsJoinTheirService(t *testing.T) {
	labels := func(component string) map[string]string {
		return map[string]string{labelInstance: "ingress-nginx", labelName: "ingress-nginx", "app.kubernetes.io/component": component}
	}
	controller := deployment("ingress-nginx", "ingress-nginx-controller", 1, 1)
	controller.Labels = labels("controller")
	controller.Spec.Template.Spec.Containers[0].Image = "registry.k8s.io/ingress-nginx/controller:v1.12.0@sha256:abc"
	create := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ingress-nginx", Name: "ingress-nginx-admission-create", UID: "j1", Labels: labels("admission-webhook")},
		Status:     batchv1.JobStatus{Succeeded: 1},
	}
	s := BuildSnapshot(Objects{Deployments: []*appsv1.Deployment{controller}, Jobs: []*batchv1.Job{create}}, 1, now)

	if len(s.Services) != 1 {
		t.Fatalf("want one service, got %+v", s.Services)
	}
	svc := s.Services[0]
	if svc.ID != "Deployment/ingress-nginx/ingress-nginx-controller" || svc.Name != "ingress-nginx-controller" || svc.App != "ingress-nginx" {
		t.Errorf("identity: %+v", svc)
	}
	if svc.Title != "NGINX Ingress" || svc.Category != CategoryIngress || len(svc.Workloads) != 2 || svc.Workloads[1] != "Job/ingress-nginx/ingress-nginx-admission-create" {
		t.Errorf("service: %+v", svc)
	}
	// The hook Job doesn't count towards replicas.
	if svc.Ready != 1 || svc.Desired != 1 {
		t.Errorf("replicas %d/%d", svc.Ready, svc.Desired)
	}
}

func TestAnnotationsWin(t *testing.T) {
	d := deployment("shop", "checkout", 2, 2)
	d.Spec.Template.Spec.Containers[0].Image = "nginx:1.27"
	d.Spec.Template.Annotations = map[string]string{
		AnnotationTitle:       "Checkout",
		AnnotationDescription: "Takes payment and creates the order.",
	}
	svc := BuildSnapshot(Objects{Deployments: []*appsv1.Deployment{d}}, 1, now).Services[0]
	if svc.Title != "Checkout" || svc.Description != "Takes payment and creates the order." || svc.DescriptionSource != "annotation" {
		t.Errorf("annotation: %+v", svc)
	}
	// The category still comes from the image when the annotation omits it.
	if svc.Category != CategoryApp {
		t.Errorf("category %s", svc.Category)
	}
}

func TestUnknownAndKeepNameServices(t *testing.T) {
	plain := deployment("shop", "orders", 1, 1)
	rollout := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "checkout-5f7d", UID: "rs-co", OwnerReferences: crOwner("argoproj.io/v1alpha1", "Rollout", "checkout")},
		Spec:       appsv1.ReplicaSetSpec{Replicas: ptr(int32(2))},
		Status:     appsv1.ReplicaSetStatus{ReadyReplicas: 2},
	}
	old := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "checkout-1a2b", UID: "rs-old", OwnerReferences: crOwner("argoproj.io/v1alpha1", "Rollout", "checkout")},
		Spec:       appsv1.ReplicaSetSpec{Replicas: ptr(int32(0))},
	}
	s := BuildSnapshot(Objects{Deployments: []*appsv1.Deployment{plain}, ReplicaSets: []*appsv1.ReplicaSet{rollout, old}}, 1, now)

	orders := findService(t, s, "Deployment/shop/orders")
	if orders.Title != "orders" || orders.Description != "" || orders.Category != CategoryApp {
		t.Errorf("an unknown image keeps its name and no description: %+v", orders)
	}
	co := findService(t, s, "argoproj.io/Rollout/shop/checkout")
	if co.Title != "checkout" || co.DescriptionSource != "operator" || len(co.Workloads) != 1 || co.Ready != 2 {
		t.Errorf("rollout (old ReplicaSet at zero left out): %+v", co)
	}
}

func TestCronJobs(t *testing.T) {
	cron := func(name string, suspend bool) *batchv1.CronJob {
		return &batchv1.CronJob{
			ObjectMeta: metav1.ObjectMeta{Namespace: "ops", Name: name, UID: types.UID("cj-" + name)},
			Spec:       batchv1.CronJobSpec{Schedule: "0 * * * *", Suspend: ptr(suspend)},
		}
	}
	run := func(cj, name string, minutesAgo int, st batchv1.JobStatus) *batchv1.Job {
		return &batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "ops", Name: name, UID: types.UID(name),
				CreationTimestamp: metav1.NewTime(now.Add(-time.Duration(minutesAgo) * time.Minute)),
				OwnerReferences:   []metav1.OwnerReference{{Kind: "CronJob", Name: cj, UID: types.UID("cj-" + cj), Controller: ptr(true)}},
			},
			Status: st,
		}
	}
	jobs := []*batchv1.Job{
		run("backup", "backup-100", 120, batchv1.JobStatus{Succeeded: 1}),
		run("backup", "backup-200", 60, batchv1.JobStatus{Failed: 6}), // latest
		run("report", "report-100", 30, batchv1.JobStatus{Succeeded: 1}),
		run("paused", "paused-100", 30, batchv1.JobStatus{Failed: 1}),
		run("sync", "sync-100", 1, batchv1.JobStatus{Active: 1}),
		run("starting", "starting-100", 1, batchv1.JobStatus{Active: 1}), // pod not running yet
	}
	syncPod := pod("ops", "sync-100-abcde", []metav1.OwnerReference{{Kind: "Job", Name: "sync-100", UID: "sync-100", Controller: ptr(true)}})
	s := BuildSnapshot(Objects{
		CronJobs: []*batchv1.CronJob{cron("backup", false), cron("report", false), cron("paused", true), cron("new", false), cron("sync", false), cron("starting", false)},
		Jobs:     jobs,
		Pods:     []*corev1.Pod{syncPod},
	}, 1, now)

	if len(s.Workloads) != 6 {
		t.Fatalf("a CronJob's runs belong to it: %d workloads", len(s.Workloads))
	}
	backup := find(t, s, "CronJob/ops/backup")
	if backup.Status != StatusDown || backup.Message != "6 failed attempt(s)" || backup.Schedule != "0 * * * *" {
		t.Errorf("backup: %+v", backup)
	}
	cases := map[string]Status{
		"CronJob/ops/report":   StatusHealthy,
		"CronJob/ops/paused":   StatusDown, // suspended, but its last run failed
		"CronJob/ops/new":      StatusIdle,
		"CronJob/ops/sync":     StatusHealthy,
		"CronJob/ops/starting": StatusHealthy, // a run on its way up isn\'t a problem
	}
	for id, want := range cases {
		if got := find(t, s, id).Status; got != want {
			t.Errorf("%s: %s, want %s", id, got, want)
		}
	}
	if sync := find(t, s, "CronJob/ops/sync"); len(sync.Pods) != 1 {
		t.Errorf("sync's running pod should be listed: %+v", sync.Pods)
	}
	if svc := findService(t, s, "CronJob/ops/report"); svc.Category != CategoryJob {
		t.Errorf("category %s", svc.Category)
	}
}

func TestStaticPods(t *testing.T) {
	api := pod("kube-system", "kube-apiserver-node-1", []metav1.OwnerReference{{APIVersion: "v1", Kind: "Node", Name: "node-1", Controller: ptr(true)}})
	api.Spec.Containers[0].Image = "registry.k8s.io/kube-apiserver:v1.37.0"
	svc := BuildSnapshot(Objects{Pods: []*corev1.Pod{api}}, 1, now).Services[0]
	if svc.ManagedBy != nil || svc.Title != "API server" || svc.Category != CategoryPlatform || svc.Status != StatusHealthy {
		t.Errorf("static pod: %+v", svc)
	}
}

func TestImagePath(t *testing.T) {
	cases := map[string]string{
		"nginx":                         "nginx",
		"nginx:1.27":                    "nginx",
		"library/redis:7":               "redis",
		"docker.io/library/postgres:17": "postgres",
		"localhost:5000/team/app:v2":    "team/app",
		"registry.k8s.io/ingress-nginx/controller:v1.12.0@sha256:abc": "ingress-nginx/controller",
		"ghcr.io/cloudnative-pg/cloudnative-pg:1.25.0":                "cloudnative-pg/cloudnative-pg",
		"bitnami/kafka": "bitnami/kafka",
	}
	for in, want := range cases {
		if got := imagePath(in); got != want {
			t.Errorf("imagePath(%q) = %q, want %q", in, got, want)
		}
	}
	if e, ok := lookupImage("bitnami/kafka:3.9"); !ok || e.title != "Kafka" {
		t.Errorf("suffix match: %+v", e)
	}
	if e, _ := lookupImage("registry.k8s.io/ingress-nginx/controller:v1"); e.title != "NGINX Ingress" {
		t.Errorf("longest match should win: %+v", e)
	}
	if _, ok := lookupImage("ghcr.io/acme/orders:1"); ok {
		t.Error("unknown image matched")
	}
}
