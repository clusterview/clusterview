package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

const (
	sessionCookie = "clusterview_session"
	loginCookie   = "clusterview_login"
	loginTTL      = 10 * time.Minute
	// maxGroups keeps the session cookie under browsers' 4 KB limit.
	maxGroups = 50
)

// session is what a signed cookie holds.
type session struct {
	Name    string   `json:"n,omitempty"`
	Groups  []string `json:"g,omitempty"`
	Expires int64    `json:"e"`
	// Login-in-progress fields.
	State    string `json:"s,omitempty"`
	Nonce    string `json:"o,omitempty"`
	Verifier string `json:"v,omitempty"`
	Return   string `json:"r,omitempty"`
}

// sessions signs and reads cookies with HMAC-SHA256. The content isn't
// secret (who you are, your groups), only tamper-proof.
type sessions struct {
	key    []byte
	secure bool
}

func (s sessions) write(w http.ResponseWriter, name string, v session, ttl time.Duration) {
	v.Expires = time.Now().Add(ttl).Unix()
	body, _ := json.Marshal(v)
	payload := base64.RawURLEncoding.EncodeToString(body)
	// Secure is on whenever the dashboard is served over https (its redirect
	// URL); over a plain-http port-forward the browser wouldn't keep it.
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // G124: Secure follows the scheme, see above
		Name: name, Value: payload + "." + s.sign(payload), Path: "/",
		MaxAge: int(ttl.Seconds()), HttpOnly: true, Secure: s.secure, SameSite: http.SameSiteLaxMode,
	})
}

func (s sessions) read(r *http.Request, name string) (session, bool) {
	c, err := r.Cookie(name)
	if err != nil {
		return session{}, false
	}
	payload, sig, ok := strings.Cut(c.Value, ".")
	if !ok || !hmac.Equal([]byte(sig), []byte(s.sign(payload))) {
		return session{}, false
	}
	body, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return session{}, false
	}
	var v session
	if json.Unmarshal(body, &v) != nil || time.Now().Unix() > v.Expires {
		return session{}, false
	}
	return v, true
}

func (s sessions) clear(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: s.secure, SameSite: http.SameSiteLaxMode}) //nolint:gosec // G124: as in write
}

func (s sessions) sign(payload string) string {
	m := hmac.New(sha256.New, s.key)
	m.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// Routes adds the sign-in routes for OIDC mode.
func (a *Authenticator) Routes(mux *http.ServeMux) {
	if a.cfg.Mode != ModeOIDC {
		return
	}
	mux.HandleFunc("GET /auth/login", a.login)
	mux.HandleFunc("GET /auth/callback", a.callback)
	mux.HandleFunc("GET /auth/logout", a.logout)
	mux.HandleFunc("GET /auth/signed-out", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!doctype html><meta charset="utf-8"><title>Signed out</title>` +
			`<body style="font-family:system-ui;display:grid;place-items:center;min-height:90vh">` +
			`<p>You're signed out of clusterview. <a href="/auth/login">Sign in again</a></p>`))
	})
}

func (a *Authenticator) login(w http.ResponseWriter, r *http.Request) {
	st := session{State: random(), Nonce: random(), Verifier: oauth2.GenerateVerifier(), Return: safeReturn(r.URL.Query().Get("return"))}
	a.sessions.write(w, loginCookie, st, loginTTL)
	http.Redirect(w, r, a.oauth.AuthCodeURL(st.State, oidc.Nonce(st.Nonce), oauth2.S256ChallengeOption(st.Verifier)), http.StatusFound)
}

func (a *Authenticator) callback(w http.ResponseWriter, r *http.Request) {
	st, ok := a.sessions.read(r, loginCookie)
	q := r.URL.Query()
	if !ok || q.Get("state") == "" || q.Get("state") != st.State {
		http.Error(w, "The sign-in expired or didn't start here. Try again.", http.StatusBadRequest)
		return
	}
	a.sessions.clear(w, loginCookie)
	if e := q.Get("error"); e != "" {
		http.Error(w, "Sign-in failed: "+e+" "+q.Get("error_description"), http.StatusUnauthorized)
		return
	}
	token, err := a.oauth.Exchange(r.Context(), q.Get("code"), oauth2.VerifierOption(st.Verifier))
	if err != nil {
		a.log.Warn("oidc code exchange failed", "err", err)
		http.Error(w, "Sign-in failed while talking to the identity provider.", http.StatusBadGateway)
		return
	}
	raw, _ := token.Extra("id_token").(string)
	idToken, err := a.verifier.Verify(r.Context(), raw)
	if err != nil || idToken.Nonce != st.Nonce {
		a.log.Warn("oidc token rejected", "err", err)
		http.Error(w, "The identity provider's answer didn't check out.", http.StatusUnauthorized)
		return
	}
	var claims map[string]any
	if err := idToken.Claims(&claims); err != nil {
		http.Error(w, "Couldn't read the sign-in token.", http.StatusUnauthorized)
		return
	}
	name := firstString(claims, "email", "preferred_username", "name")
	if name == "" {
		name = idToken.Subject
	}
	groups := stringList(claims[a.cfg.GroupsClaim])
	if len(groups) > maxGroups {
		groups = groups[:maxGroups]
	}
	a.sessions.write(w, sessionCookie, session{Name: name, Groups: groups}, a.cfg.SessionTTL)
	a.log.Info("signed in", "user", name)
	http.Redirect(w, r, st.Return, http.StatusFound)
}

func (a *Authenticator) logout(w http.ResponseWriter, r *http.Request) {
	a.sessions.clear(w, sessionCookie)
	http.Redirect(w, r, "/auth/signed-out", http.StatusFound)
}

// safeReturn only allows a path on this site, so sign-in can't be used to
// send people elsewhere.
func safeReturn(p string) string {
	if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.HasPrefix(p, "/\\") || strings.HasPrefix(p, "/auth/") {
		return "/"
	}
	return p
}

func random() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func firstString(claims map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := claims[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

func stringList(v any) []string {
	out := []string{}
	switch t := v.(type) {
	case []any:
		for _, x := range t {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
	case string:
		out = splitGroups(t)
	}
	return out
}

func urlQueryEscape(s string) string { return url.QueryEscape(s) }
