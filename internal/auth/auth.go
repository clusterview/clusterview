// Package auth decides who is looking at the dashboard and what they may do.
//
// Four modes:
//   - none: no login. Fine behind kubectl port-forward, where only people
//     with cluster access can reach the dashboard. Everyone is an operator
//     (actions still need actions.enabled).
//   - proxy: an auth proxy in front (oauth2-proxy, Pomerium, an ingress's
//     auth-url) has signed the person in and passes who they are in headers.
//   - oidc: clusterview signs people in itself with any OpenID Connect
//     provider (Okta, Entra ID, Google, Keycloak, Dex…).
//   - password: one shared password, like Grafana's built-in login. Simple to
//     set up; nobody is told apart.
//
// Viewers may look; operators may also act. Lists match an exact user,
// "group:<name>", "*@<domain>" or "*".
package auth

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// Mode is how people sign in.
type Mode string

// Modes.
const (
	ModeNone  Mode = "none"
	ModeProxy Mode = "proxy"
	ModeOIDC  Mode = "oidc"
	// ModePassword signs everyone in with one shared password.
	ModePassword Mode = "password"
)

// Role is what someone may do.
type Role string

// Roles.
const (
	RoleNone     Role = ""         // signed in, but not allowed in
	RoleViewer   Role = "viewer"   // may look
	RoleOperator Role = "operator" // may look and act
)

// Config sets how people sign in and who may do what.
type Config struct {
	Mode Mode
	// Viewers may look. Empty means anyone who signed in.
	Viewers []string
	// Operators may also act. Empty means nobody, except in mode none.
	Operators []string

	// Proxy mode: the headers the proxy sets.
	UserHeader   string // default X-Forwarded-Email
	GroupsHeader string // default X-Forwarded-Groups

	// OIDC mode.
	IssuerURL    string
	ClientID     string
	ClientSecret string
	RedirectURL  string // https://<host>/auth/callback
	Scopes       []string
	GroupsClaim  string // default "groups"

	// Password mode: the shared password.
	Password string

	// SessionKey signs session cookies. A random one is made when empty,
	// which signs everyone out when the pod restarts.
	SessionKey []byte
	SessionTTL time.Duration

	// BasePath is where the dashboard is served, such as "/clusterview", or
	// empty at the root. Requests reach this package with it already removed;
	// it's added back to redirects and cookie paths.
	BasePath string
}

// User is whoever made a request.
type User struct {
	Name   string   `json:"name"`
	Groups []string `json:"groups"`
	Role   Role     `json:"role"`
	Mode   Mode     `json:"mode"`
}

// CanAct reports whether the user may take actions.
func (u User) CanAct() bool { return u.Role == RoleOperator }

// Authenticator identifies users and guards the routes.
type Authenticator struct {
	cfg      Config
	log      *slog.Logger
	sessions sessions
	oauth    *oauth2.Config
	verifier *oidc.IDTokenVerifier
}

// New checks the config and, in OIDC mode, discovers the provider.
func New(ctx context.Context, cfg Config, log *slog.Logger) (*Authenticator, error) {
	if cfg.Mode == "" {
		cfg.Mode = ModeNone
	}
	if cfg.UserHeader == "" {
		cfg.UserHeader = "X-Forwarded-Email"
	}
	if cfg.GroupsHeader == "" {
		cfg.GroupsHeader = "X-Forwarded-Groups"
	}
	if cfg.GroupsClaim == "" {
		cfg.GroupsClaim = "groups"
	}
	if len(cfg.Scopes) == 0 {
		cfg.Scopes = []string{oidc.ScopeOpenID, "email", "profile"}
	}
	if cfg.SessionTTL <= 0 {
		cfg.SessionTTL = 12 * time.Hour
	}
	if len(cfg.SessionKey) == 0 {
		cfg.SessionKey = make([]byte, 32)
		if _, err := rand.Read(cfg.SessionKey); err != nil {
			return nil, err
		}
	}
	a := &Authenticator{cfg: cfg, log: log, sessions: sessions{key: cfg.SessionKey, path: cfg.BasePath + "/"}}

	switch cfg.Mode {
	case ModeNone, ModeProxy:
	case ModeOIDC:
		if cfg.IssuerURL == "" || cfg.ClientID == "" || cfg.RedirectURL == "" {
			return nil, errors.New("oidc mode needs an issuer URL, a client ID and a redirect URL")
		}
		provider, err := oidc.NewProvider(ctx, cfg.IssuerURL)
		if err != nil {
			return nil, fmt.Errorf("reaching the OIDC provider: %w", err)
		}
		a.verifier = provider.Verifier(&oidc.Config{ClientID: cfg.ClientID})
		a.oauth = &oauth2.Config{
			ClientID: cfg.ClientID, ClientSecret: cfg.ClientSecret, RedirectURL: cfg.RedirectURL,
			Endpoint: provider.Endpoint(), Scopes: cfg.Scopes,
		}
		a.sessions.secure = strings.HasPrefix(cfg.RedirectURL, "https://")
	case ModePassword:
		if len(cfg.Password) < minPasswordLength {
			return nil, fmt.Errorf("password mode needs a password of at least %d characters", minPasswordLength)
		}
	default:
		return nil, fmt.Errorf("unknown auth mode %q (use none, proxy, oidc or password)", cfg.Mode)
	}
	return a, nil
}

// Mode returns the sign-in mode.
func (a *Authenticator) Mode() Mode { return a.cfg.Mode }

// identify works out who made the request, without deciding access.
func (a *Authenticator) identify(r *http.Request) (User, bool) {
	switch a.cfg.Mode {
	case ModeNone:
		return User{Name: "anonymous", Groups: []string{}, Role: RoleOperator, Mode: ModeNone}, true
	case ModeProxy:
		name := strings.TrimSpace(r.Header.Get(a.cfg.UserHeader))
		if name == "" {
			return User{}, false
		}
		return a.withRole(User{Name: name, Groups: splitGroups(r.Header.Get(a.cfg.GroupsHeader))}), true
	case ModeOIDC, ModePassword:
		s, ok := a.sessions.read(r, sessionCookie)
		if !ok {
			return User{}, false
		}
		return a.withRole(User{Name: s.Name, Groups: s.Groups}), true
	}
	return User{}, false
}

func (a *Authenticator) withRole(u User) User {
	u.Mode = a.cfg.Mode
	if u.Groups == nil {
		u.Groups = []string{}
	}
	switch {
	case matches(a.cfg.Operators, u):
		u.Role = RoleOperator
	case len(a.cfg.Viewers) == 0 || matches(a.cfg.Viewers, u):
		u.Role = RoleViewer
	default:
		u.Role = RoleNone
	}
	return u
}

// matches reports whether any entry in list names the user.
func matches(list []string, u User) bool {
	name := strings.ToLower(u.Name)
	return slices.ContainsFunc(list, func(entry string) bool {
		entry = strings.TrimSpace(entry)
		switch {
		case entry == "*":
			return true
		case strings.HasPrefix(entry, "group:"):
			return slices.Contains(u.Groups, strings.TrimPrefix(entry, "group:"))
		case strings.HasPrefix(entry, "*@"):
			return strings.HasSuffix(name, strings.ToLower(entry[1:]))
		}
		return strings.EqualFold(entry, u.Name)
	})
}

func splitGroups(s string) []string {
	out := []string{}
	for _, g := range strings.Split(s, ",") {
		if g = strings.TrimSpace(g); g != "" {
			out = append(out, g)
		}
	}
	return out
}

type userKey struct{}

// UserFrom returns the user the middleware put on the request.
func UserFrom(ctx context.Context) User {
	u, _ := ctx.Value(userKey{}).(User)
	return u
}

// Middleware lets requests through only for people allowed to look. Health
// checks and the sign-in routes are always open.
func (a *Authenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if p == "/healthz" || p == "/readyz" || strings.HasPrefix(p, "/auth/") {
			next.ServeHTTP(w, r)
			return
		}
		api := strings.HasPrefix(p, "/api/")
		u, ok := a.identify(r)
		switch {
		case !ok && (a.cfg.Mode == ModeOIDC || a.cfg.Mode == ModePassword) && !api:
			// Always our own sign-in path; the return target is escaped here and
			// checked by safeReturn when used.
			http.Redirect(w, r, a.cfg.BasePath+"/auth/login?return="+urlQueryEscape(r.URL.RequestURI()), http.StatusFound) //nolint:gosec // G710: fixed local path
			return
		case !ok:
			deny(w, api, http.StatusUnauthorized, "Sign in first.")
			return
		case u.Role == RoleNone:
			deny(w, api, http.StatusForbidden, fmt.Sprintf("%s isn't allowed to view this dashboard. Ask to be added to auth.viewers.", u.Name))
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey{}, u)))
	})
}

func deny(w http.ResponseWriter, api bool, code int, msg string) {
	if api {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_, _ = fmt.Fprintf(w, `{"error":%q}`, msg)
		return
	}
	http.Error(w, msg, code)
}
