package cluster

// catalogEntry describes a well-known service in plain words.
type catalogEntry struct {
	title       string
	description string
	category    Category
	// keepName keeps the Kubernetes name as the title, for owners that run
	// an app rather than being one (an Argo Rollout is still "checkout").
	keepName bool
}

// operatorCatalog knows custom resources by "<group>/<Kind>".
var operatorCatalog = map[string]catalogEntry{
	"rabbitmq.com/RabbitmqCluster":                      {"RabbitMQ", "Message broker run by the RabbitMQ Cluster Operator. Holds work queues between services.", CategoryQueue, false},
	"postgresql.cnpg.io/Cluster":                        {"PostgreSQL", "PostgreSQL database run by CloudNativePG, with failover between its instances.", CategoryDatabase, false},
	"postgres-operator.crunchydata.com/PostgresCluster": {"PostgreSQL", "PostgreSQL database run by Crunchy Postgres for Kubernetes.", CategoryDatabase, false},
	"acid.zalan.do/postgresql":                          {"PostgreSQL", "PostgreSQL database run by the Zalando Postgres Operator.", CategoryDatabase, false},
	"mysql.oracle.com/InnoDBCluster":                    {"MySQL", "MySQL InnoDB Cluster run by the MySQL Operator.", CategoryDatabase, false},
	"pxc.percona.com/PerconaXtraDBCluster":              {"MySQL", "Percona XtraDB Cluster (MySQL) run by the Percona Operator.", CategoryDatabase, false},
	"psmdb.percona.com/PerconaServerMongoDB":            {"MongoDB", "Percona Server for MongoDB run by the Percona Operator.", CategoryDatabase, false},
	"mongodbcommunity.mongodb.com/MongoDBCommunity":     {"MongoDB", "MongoDB replica set run by the MongoDB Community Operator.", CategoryDatabase, false},
	"kafka.strimzi.io/Kafka":                            {"Kafka", "Kafka event streaming run by Strimzi.", CategoryQueue, false},
	"core.strimzi.io/StrimziPodSet":                     {"Kafka", "Kafka brokers or controllers run by Strimzi.", CategoryQueue, false},
	"redis.redis.opstreelabs.in/Redis":                  {"Redis", "Redis run by the OT Redis Operator.", CategoryCache, false},
	"redis.redis.opstreelabs.in/RedisCluster":           {"Redis", "Redis cluster run by the OT Redis Operator.", CategoryCache, false},
	"databases.spotahome.com/RedisFailover":             {"Redis", "Redis with Sentinel failover, run by the Spotahome operator.", CategoryCache, false},
	"elasticsearch.k8s.elastic.co/Elasticsearch":        {"Elasticsearch", "Search and log storage run by Elastic Cloud on Kubernetes.", CategorySearch, false},
	"kibana.k8s.elastic.co/Kibana":                      {"Kibana", "Dashboards for Elasticsearch, run by Elastic Cloud on Kubernetes.", CategoryMonitoring, false},
	"monitoring.coreos.com/Prometheus":                  {"Prometheus", "Collects and stores metrics, run by the Prometheus Operator.", CategoryMonitoring, false},
	"monitoring.coreos.com/Alertmanager":                {"Alertmanager", "Sends alerts from Prometheus, run by the Prometheus Operator.", CategoryMonitoring, false},
	"clickhouse.altinity.com/ClickHouseInstallation":    {"ClickHouse", "Analytics database run by the Altinity ClickHouse Operator.", CategoryDatabase, false},
	"minio.min.io/Tenant":                               {"MinIO", "S3-compatible object storage run by the MinIO Operator.", CategoryStorage, false},
	"argoproj.io/Rollout":                               {"", "Released step by step by Argo Rollouts.", CategoryApp, true},
	"serving.knative.dev/Revision":                      {"", "A Knative service; scales with traffic, down to zero.", CategoryApp, true},
	"keda.sh/ScaledJob":                                 {"", "Jobs KEDA starts when there is work waiting.", CategoryJob, true},
}

// imageCatalog knows images by repository path, without registry or tag. The
// longest matching suffix wins, so "ingress-nginx/controller" beats "nginx".
var imageCatalog = map[string]catalogEntry{
	// Databases
	"postgres":                  {"PostgreSQL", "Relational database.", CategoryDatabase, false},
	"bitnami/postgresql":        {"PostgreSQL", "Relational database.", CategoryDatabase, false},
	"cloudnative-pg/postgresql": {"PostgreSQL", "Relational database.", CategoryDatabase, false},
	"pgvector/pgvector":         {"PostgreSQL", "Relational database with vector search (pgvector).", CategoryDatabase, false},
	"mysql":                     {"MySQL", "Relational database.", CategoryDatabase, false},
	"mariadb":                   {"MariaDB", "Relational database.", CategoryDatabase, false},
	"mongo":                     {"MongoDB", "Document database.", CategoryDatabase, false},
	"clickhouse-server":         {"ClickHouse", "Analytics database for large volumes of events.", CategoryDatabase, false},
	"qdrant":                    {"Qdrant", "Vector database for semantic search.", CategoryDatabase, false},
	"cassandra":                 {"Cassandra", "Wide-column database.", CategoryDatabase, false},
	"etcd":                      {"etcd", "Key-value store; in the control plane it holds the whole cluster's state.", CategoryPlatform, false},
	// Queues and streams
	"rabbitmq": {"RabbitMQ", "Message broker. Holds work queues between services.", CategoryQueue, false},
	"kafka":    {"Kafka", "Event streaming platform.", CategoryQueue, false},
	"cp-kafka": {"Kafka", "Event streaming platform.", CategoryQueue, false},
	"nats":     {"NATS", "Messaging system for services and streams.", CategoryQueue, false},
	// Caches
	"redis":     {"Redis", "In-memory store for caching, sessions and queues.", CategoryCache, false},
	"valkey":    {"Valkey", "In-memory store for caching, sessions and queues.", CategoryCache, false},
	"memcached": {"Memcached", "In-memory cache.", CategoryCache, false},
	// Search
	"elasticsearch": {"Elasticsearch", "Search engine and log store.", CategorySearch, false},
	"opensearch":    {"OpenSearch", "Search engine and log store.", CategorySearch, false},
	// Storage
	"minio":                     {"MinIO", "S3-compatible object storage.", CategoryStorage, false},
	"local-path-provisioner":    {"Local volumes", "Creates volumes on a node's own disk when a pod asks for storage.", CategoryStorage, false},
	"aws-ebs-csi-driver":        {"EBS volumes", "Attaches AWS EBS disks to pods that ask for storage.", CategoryStorage, false},
	"csi-node-driver-registrar": {"Storage driver", "Registers a storage driver with each node.", CategoryStorage, false},
	// Ingress and networking
	"nginx":                        {"NGINX", "Web server and reverse proxy.", CategoryApp, false},
	"ingress-nginx/controller":     {"NGINX Ingress", "Routes outside traffic into the cluster by host name and path.", CategoryIngress, false},
	"traefik":                      {"Traefik", "Routes outside traffic into the cluster.", CategoryIngress, false},
	"haproxy":                      {"HAProxy", "Load balancer and reverse proxy.", CategoryIngress, false},
	"aws-load-balancer-controller": {"AWS Load Balancer Controller", "Creates AWS load balancers for Services and Ingresses.", CategoryIngress, false},
	"envoy":                        {"Envoy", "Proxy that carries traffic between services.", CategoryNetwork, false},
	"istio/proxyv2":                {"Istio proxy", "Service-mesh sidecar or gateway.", CategoryNetwork, false},
	"istio/pilot":                  {"Istio", "Service-mesh control plane.", CategoryNetwork, false},
	"kube-proxy":                   {"kube-proxy", "Sends traffic for each Service to its pods, on every node.", CategoryNetwork, false},
	"kindnetd":                     {"kindnet", "The pod network of a kind cluster.", CategoryNetwork, false},
	"cilium":                       {"Cilium", "Pod network and network policies.", CategoryNetwork, false},
	"calico/node":                  {"Calico", "Pod network and network policies.", CategoryNetwork, false},
	"amazon-k8s-cni":               {"AWS VPC CNI", "Gives pods addresses in the VPC.", CategoryNetwork, false},
	"coredns":                      {"CoreDNS", "Cluster DNS: turns service names into addresses.", CategoryDNS, false},
	"external-dns":                 {"ExternalDNS", "Creates public DNS records for Services and Ingresses.", CategoryDNS, false},
	// Monitoring
	"metrics-server":                  {"Metrics Server", "Collects CPU and memory use for autoscaling and kubectl top.", CategoryMonitoring, false},
	"prometheus":                      {"Prometheus", "Collects and stores metrics.", CategoryMonitoring, false},
	"alertmanager":                    {"Alertmanager", "Sends alerts from Prometheus.", CategoryMonitoring, false},
	"grafana":                         {"Grafana", "Dashboards for metrics and logs.", CategoryMonitoring, false},
	"loki":                            {"Loki", "Stores and searches logs.", CategoryMonitoring, false},
	"promtail":                        {"Promtail", "Ships pod logs to Loki.", CategoryMonitoring, false},
	"tempo":                           {"Tempo", "Stores traces.", CategoryMonitoring, false},
	"node-exporter":                   {"Node Exporter", "Reports each node's CPU, memory and disk to Prometheus.", CategoryMonitoring, false},
	"kube-state-metrics":              {"kube-state-metrics", "Turns Kubernetes objects into Prometheus metrics.", CategoryMonitoring, false},
	"opentelemetry-collector":         {"OpenTelemetry Collector", "Receives and forwards traces, metrics and logs.", CategoryMonitoring, false},
	"opentelemetry-collector-contrib": {"OpenTelemetry Collector", "Receives and forwards traces, metrics and logs.", CategoryMonitoring, false},
	"fluent-bit":                      {"Fluent Bit", "Ships pod logs to a log store.", CategoryMonitoring, false},
	"vector":                          {"Vector", "Ships logs and metrics.", CategoryMonitoring, false},
	// Security
	"cert-manager-controller": {"cert-manager", "Issues and renews TLS certificates.", CategorySecurity, false},
	"cert-manager-webhook":    {"cert-manager webhook", "Checks certificate resources before they are saved.", CategorySecurity, false},
	"cert-manager-cainjector": {"cert-manager CA injector", "Puts CA bundles into webhooks and API services.", CategorySecurity, false},
	"vault":                   {"Vault", "Stores and hands out secrets.", CategorySecurity, false},
	"external-secrets":        {"External Secrets", "Copies secrets from a secret store into Kubernetes.", CategorySecurity, false},
	// Platform
	"kube-apiserver":                    {"API server", "The Kubernetes API every tool and controller talks to.", CategoryPlatform, false},
	"kube-scheduler":                    {"Scheduler", "Picks a node for every new pod.", CategoryPlatform, false},
	"kube-controller-manager":           {"Controller manager", "Runs Kubernetes' built-in controllers (Deployments, Jobs, nodes…).", CategoryPlatform, false},
	"cluster-autoscaler":                {"Cluster Autoscaler", "Adds and removes nodes as pods need room.", CategoryPlatform, false},
	"karpenter/controller":              {"Karpenter", "Adds and removes nodes as pods need room.", CategoryPlatform, false},
	"kedacore/keda":                     {"KEDA", "Scales workloads on queue length and other events.", CategoryPlatform, false},
	"kedacore/keda-metrics-apiserver":   {"KEDA metrics", "Serves KEDA's scaling figures to the autoscaler.", CategoryPlatform, false},
	"argoproj/argocd":                   {"Argo CD", "Deploys what's in Git to the cluster.", CategoryPlatform, false},
	"rabbitmqoperator/cluster-operator": {"RabbitMQ operator", "Creates and looks after RabbitMQ clusters.", CategoryPlatform, false},
	"rabbitmq/cluster-operator":         {"RabbitMQ operator", "Creates and looks after RabbitMQ clusters.", CategoryPlatform, false},
	"cloudnative-pg/cloudnative-pg":     {"CloudNativePG operator", "Creates and looks after PostgreSQL clusters.", CategoryPlatform, false},
	"strimzi/operator":                  {"Strimzi operator", "Creates and looks after Kafka clusters.", CategoryPlatform, false},
	"clusterview/clusterview":           {"clusterview", "This dashboard.", CategoryMonitoring, false},
}
