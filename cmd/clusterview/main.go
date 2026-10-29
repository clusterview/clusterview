// Command clusterview serves the clusterview dashboard.
//
// Inside Kubernetes it uses the pod's service account and listens on :8080.
// Anywhere else it uses your kubeconfig, listens on localhost only and opens
// the dashboard in your browser, so the same binary runs on a laptop, also as
// "kubectl clusterview".
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/clusterview/clusterview/internal/actions"
	"github.com/clusterview/clusterview/internal/ai"
	"github.com/clusterview/clusterview/internal/alerts"
	"github.com/clusterview/clusterview/internal/api"
	"github.com/clusterview/clusterview/internal/assistant"
	"github.com/clusterview/clusterview/internal/auth"
	"github.com/clusterview/clusterview/internal/cluster"
	"github.com/clusterview/clusterview/internal/diagnose"
	"github.com/clusterview/clusterview/internal/kube"
	"github.com/clusterview/clusterview/internal/version"
	"github.com/clusterview/clusterview/web"
)

type config struct {
	addr        string
	kubeconfig  string
	kubeContext string
	open        bool
	scope       cluster.Options
	actions     actions.Config
	auth        auth.Config
	ai          ai.Config
	alerts      alerts.Config
	basePath    string
}

func main() {
	inCluster := os.Getenv("KUBERNETES_SERVICE_HOST") != ""
	asPlugin := strings.HasPrefix(filepath.Base(os.Args[0]), "kubectl-")
	var cfg config

	flag.StringVar(&cfg.addr, "addr", os.Getenv("CLUSTERVIEW_ADDR"), "address to listen on (default :8080 in a cluster, 127.0.0.1:8080 elsewhere)")
	flag.StringVar(&cfg.kubeconfig, "kubeconfig", os.Getenv("KUBECONFIG"), "path to a kubeconfig (outside the cluster)")
	flag.StringVar(&cfg.kubeContext, "context", "", "kubeconfig context to use (default: the current one)")
	flag.BoolVar(&cfg.open, "open", asPlugin, "open the dashboard in a browser")
	showVersion := flag.Bool("version", false, "print the version and exit")

	namespaces := flag.String("namespaces", os.Getenv("CLUSTERVIEW_NAMESPACES"), "comma-separated namespaces to watch (default: the whole cluster)")
	flag.BoolVar(&cfg.scope.Nodes, "nodes", envOr("CLUSTERVIEW_NODES", "true") == "true", "show the nodes view (needs cluster-wide read access to nodes)")

	flag.BoolVar(&cfg.actions.Enabled, "actions", envOr("CLUSTERVIEW_ACTIONS", "false") == "true", "allow restarting, scaling and running workloads")
	actionNamespaces := flag.String("actions-namespaces", os.Getenv("CLUSTERVIEW_ACTIONS_NAMESPACES"), "comma-separated namespaces actions may touch (default: those watched)")
	flag.IntVar(&cfg.actions.MaxReplicas, "actions-max-replicas", envInt("CLUSTERVIEW_ACTIONS_MAX_REPLICAS", 20), "the most replicas scaling may set")

	authMode := flag.String("auth", envOr("CLUSTERVIEW_AUTH", "none"), "how people sign in: none, proxy, oidc or password")
	viewers := flag.String("auth-viewers", os.Getenv("CLUSTERVIEW_AUTH_VIEWERS"), "who may look: users, group:<name>, *@<domain>; empty means anyone signed in")
	operators := flag.String("auth-operators", os.Getenv("CLUSTERVIEW_AUTH_OPERATORS"), "who may also act; empty means nobody unless --auth=none")
	flag.StringVar(&cfg.auth.UserHeader, "auth-user-header", os.Getenv("CLUSTERVIEW_AUTH_USER_HEADER"), "proxy mode: header with the user (default X-Forwarded-Email)")
	flag.StringVar(&cfg.auth.GroupsHeader, "auth-groups-header", os.Getenv("CLUSTERVIEW_AUTH_GROUPS_HEADER"), "proxy mode: header with comma-separated groups (default X-Forwarded-Groups)")
	flag.StringVar(&cfg.auth.IssuerURL, "oidc-issuer", os.Getenv("CLUSTERVIEW_OIDC_ISSUER"), "oidc mode: issuer URL")
	flag.StringVar(&cfg.auth.ClientID, "oidc-client-id", os.Getenv("CLUSTERVIEW_OIDC_CLIENT_ID"), "oidc mode: client ID")
	flag.StringVar(&cfg.auth.RedirectURL, "oidc-redirect-url", os.Getenv("CLUSTERVIEW_OIDC_REDIRECT_URL"), "oidc mode: https://<host>/auth/callback")
	scopes := flag.String("oidc-scopes", os.Getenv("CLUSTERVIEW_OIDC_SCOPES"), "oidc mode: scopes (default openid,email,profile)")
	flag.StringVar(&cfg.auth.GroupsClaim, "oidc-groups-claim", os.Getenv("CLUSTERVIEW_OIDC_GROUPS_CLAIM"), "oidc mode: claim with the user's groups (default groups)")

	basePath := flag.String("base-path", os.Getenv("CLUSTERVIEW_BASE_PATH"), "serve the dashboard under this path, such as /clusterview, when it shares a host with other apps")
	flag.StringVar(&cfg.ai.Provider, "ai-provider", os.Getenv("CLUSTERVIEW_AI_PROVIDER"), "turns on cluster chat and Find error: anthropic, gemini, or openai (also for local OpenAI-compatible servers)")
	flag.StringVar(&cfg.ai.Model, "ai-model", os.Getenv("CLUSTERVIEW_AI_MODEL"), "model to ask (default "+ai.DefaultAnthropicModel+" for anthropic; required for openai and gemini)")
	flag.StringVar(&cfg.ai.BaseURL, "ai-base-url", os.Getenv("CLUSTERVIEW_AI_BASE_URL"), "another server for the openai provider, such as http://localhost:11434/v1 for Ollama")
	flag.BoolVar(&cfg.ai.Describe, "ai-describe", envOr("CLUSTERVIEW_AI_DESCRIBE", "true") == "true", "describe services the catalogue doesn't know, from their image names, marked as AI guesses")
	alertFor := flag.String("alerts-for", envOr("CLUSTERVIEW_ALERTS_FOR", "5m"), "how long a service must stay broken before notifying the alert webhook")
	flag.StringVar(&cfg.alerts.Format, "alerts-format", envOr("CLUSTERVIEW_ALERTS_FORMAT", "webhook"), "alert payload: webhook, slack or teams")
	flag.StringVar(&cfg.alerts.Cluster, "alerts-cluster", os.Getenv("CLUSTERVIEW_ALERTS_CLUSTER"), "cluster name in notifications (default: kubeconfig context, or cluster)")
	flag.StringVar(&cfg.alerts.DashboardURL, "alerts-dashboard-url", os.Getenv("CLUSTERVIEW_ALERTS_DASHBOARD_URL"), "dashboard URL to include in notifications")
	flag.Parse()

	if *showVersion {
		fmt.Printf("clusterview %s (%s)\n", version.Version, version.Commit)
		return
	}
	var err error
	cfg.alerts.For, err = time.ParseDuration(*alertFor)
	if err != nil {
		fmt.Fprintln(os.Stderr, "alerts-for must be a duration, such as 5m")
		os.Exit(2)
	}

	cfg.scope.Namespaces = splitList(*namespaces)
	// Actions never reach beyond what's watched.
	cfg.actions.Namespaces = splitList(*actionNamespaces)
	if len(cfg.actions.Namespaces) == 0 {
		cfg.actions.Namespaces = cfg.scope.Namespaces
	}
	cfg.auth.Mode = auth.Mode(*authMode)
	cfg.auth.Viewers, cfg.auth.Operators, cfg.auth.Scopes = splitList(*viewers), splitList(*operators), splitList(*scopes)
	// Secrets come from the environment only, never flags (which ps shows).
	cfg.auth.ClientSecret = os.Getenv("CLUSTERVIEW_OIDC_CLIENT_SECRET")
	cfg.auth.SessionKey = []byte(os.Getenv("CLUSTERVIEW_SESSION_KEY"))
	cfg.auth.Password = os.Getenv("CLUSTERVIEW_AUTH_PASSWORD")
	cfg.ai.APIKey = aiKey(cfg.ai.Provider)
	bp, err := cleanBasePath(*basePath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	cfg.basePath, cfg.auth.BasePath = bp, bp
	cfg.alerts.WebhookURL = os.Getenv("CLUSTERVIEW_ALERTS_WEBHOOK_URL")
	if cfg.addr == "" {
		cfg.addr = "127.0.0.1:8080" // on a laptop, never listen beyond it
		if inCluster {
			cfg.addr = ":8080"
		}
	}

	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(cfg, log); err != nil {
		log.Error("exiting", "err", err)
		os.Exit(1)
	}
}

func run(cfg config, log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	authn, err := auth.New(ctx, cfg.auth, log)
	if err != nil {
		return fmt.Errorf("sign-in setup: %w", err)
	}
	log.Info("sign-in", "mode", authn.Mode(), "viewers", cfg.auth.Viewers, "operators", cfg.auth.Operators)
	if authn.Mode() == auth.ModeNone && cfg.actions.Enabled {
		log.Warn("actions are on without sign-in: anyone who can reach the dashboard can use them")
	}

	model, err := optionalModel(cfg.ai, log)
	if err != nil {
		return fmt.Errorf("AI setup: %w", err)
	}

	var c *cluster.Cache
	var runner *actions.Runner
	k, err := kube.Connect(cfg.kubeconfig, cfg.kubeContext)
	if err != nil {
		// Still serve the UI, which shows the cluster as unreachable.
		log.Warn("no cluster connection", "err", err)
		k = nil
	} else {
		log.Info("cluster client ready", "mode", k.Mode, "context", k.Context, "namespaces", cfg.scope.Namespaces, "nodes", cfg.scope.Nodes)
		c = cluster.NewCache(k, cfg.scope, log)
		if model != nil && cfg.ai.Describe {
			describer := ai.NewDescriber(model, c.Refresh, log)
			c.SetGuesser(describer)
			go describer.Run(ctx)
		}
		go c.Start(ctx)
		runner = actions.NewRunner(cfg.actions, k.Clientset, nil, log)
		log.Info("actions", "enabled", cfg.actions.Enabled, "namespaces", cfg.actions.Namespaces, "maxReplicas", runner.Config().MaxReplicas)
	}
	if cfg.alerts.Cluster == "" && k != nil {
		cfg.alerts.Cluster = k.Context
	}
	alerter, err := alerts.New(cfg.alerts, c, log)
	if err != nil {
		return fmt.Errorf("alerts setup: %w", err)
	}
	if alerter != nil && c != nil {
		go alerter.Run(ctx)
		log.Info("alerts enabled", "format", cfg.alerts.Format, "for", cfg.alerts.For)
	}

	ln, err := listen(cfg.addr)
	if err != nil {
		return err
	}
	server := api.New(k, c, runner, authn, web.UI(), log)
	server.SetBasePath(cfg.basePath)
	if model != nil && c != nil {
		server.SetAI(diagnose.New(model, c), api.AIInfo{Enabled: true, Model: model.Name(), Describe: cfg.ai.Describe})
		server.SetAssistant(assistant.New(model, c))
		log.Info("AI", "model", model.Name(), "describe", cfg.ai.Describe)
	}
	srv := &http.Server{
		Handler:           server.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	url := "http://" + browsable(ln.Addr().String()) + cfg.basePath + "/"
	log.Info("listening", "url", url, "version", version.Version)
	if cfg.open {
		fmt.Fprintf(os.Stderr, "clusterview is at %s (Ctrl+C to stop)\n", url)
		openBrowser(url, log)
	}

	errc := make(chan error, 1)
	go func() {
		if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
		close(errc)
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	log.Info("shutting down")
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdown)
}

// listen binds addr. On the default laptop address it falls back to any free
// port, so a second copy (or something else on 8080) doesn't stop it.
func listen(addr string) (net.Listener, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil && addr == "127.0.0.1:8080" {
		return net.Listen("tcp", "127.0.0.1:0")
	}
	return ln, err
}

// browsable turns a listen address into one a browser can open.
func browsable(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || host == "" || host == "::" || host == "0.0.0.0" {
		return "localhost:" + port
	}
	return net.JoinHostPort(host, port)
}

// openBrowser opens url, which is always http://<our listener address>.
func openBrowser(url string, log *slog.Logger) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url) //nolint:gosec // G204: url is our own listener address
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url) //nolint:gosec // G204: url is our own listener address
	default:
		cmd = exec.Command("xdg-open", url) //nolint:gosec // G204: url is our own listener address
	}
	if err := cmd.Start(); err != nil {
		log.Info("couldn't open a browser", "err", err)
	}
}

// cleanBasePath turns "clusterview/" or "/clusterview/" into "/clusterview",
// and "/" into "" (the root).
func cleanBasePath(p string) (string, error) {
	p = strings.Trim(strings.TrimSpace(p), "/")
	if p == "" {
		return "", nil
	}
	if strings.ContainsAny(p, "?#%\\ ") || strings.Contains(p, "//") || strings.Contains(p, "..") {
		return "", fmt.Errorf("--base-path %q: use a plain path such as /clusterview", p)
	}
	return "/" + p, nil
}

// aiKey reads the model's key from the environment, never a flag. On a
// laptop the provider's usual variable works too.
func aiKey(provider string) string {
	if k := os.Getenv("CLUSTERVIEW_AI_API_KEY"); k != "" {
		return k
	}
	switch strings.ToLower(provider) {
	case ai.ProviderAnthropic:
		return os.Getenv("ANTHROPIC_API_KEY")
	case ai.ProviderGemini:
		return os.Getenv("GEMINI_API_KEY")
	case ai.ProviderOpenAI:
		return os.Getenv("OPENAI_API_KEY")
	}
	return ""
}

func envInt(key string, fallback int) int {
	if n, err := strconv.Atoi(os.Getenv(key)); err == nil {
		return n
	}
	return fallback
}

func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// optionalModel keeps the dashboard available while cloud AI credentials are absent.
func optionalModel(cfg ai.Config, log *slog.Logger) (ai.Model, error) {
	model, err := ai.New(cfg)
	if errors.Is(err, ai.ErrMissingKey) {
		log.Warn("AI disabled: provider API key is missing; configure it to enable the assistant and Find error", "provider", cfg.Provider)
		return nil, nil
	}
	return model, err
}
