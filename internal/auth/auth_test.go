package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
)

var quiet = slog.New(slog.DiscardHandler)

func TestRoles(t *testing.T) {
	a, _ := New(context.Background(), Config{
		Mode:      ModeProxy,
		Viewers:   []string{"*@example.com", "group:contractors"},
		Operators: []string{"ops@example.com", "group:platform"},
	}, quiet)
	cases := []struct {
		name   string
		groups []string
		want   Role
	}{
		{"ops@example.com", nil, RoleOperator},
		{"OPS@Example.com", nil, RoleOperator},
		{"dev@example.com", []string{"platform"}, RoleOperator},
		{"dev@example.com", nil, RoleViewer},
		{"temp@agency.io", []string{"contractors"}, RoleViewer},
		{"eve@evil-example.com", nil, RoleNone},
		{"eve@example.com.evil.io", nil, RoleNone},
	}
	for _, c := range cases {
		if got := a.withRole(User{Name: c.name, Groups: c.groups}).Role; got != c.want {
			t.Errorf("%s %v: got %q, want %q", c.name, c.groups, got, c.want)
		}
	}

	open, _ := New(context.Background(), Config{Mode: ModeProxy}, quiet)
	if got := open.withRole(User{Name: "anyone@anywhere"}).Role; got != RoleViewer {
		t.Errorf("empty viewer list lets anyone signed in look; empty operators lets nobody act: %q", got)
	}
}

func serve(a *Authenticator) *httptest.Server {
	mux := http.NewServeMux()
	a.Routes(mux)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(UserFrom(r.Context()))
	})
	return httptest.NewServer(a.Middleware(mux))
}

func TestProxyMode(t *testing.T) {
	a, _ := New(context.Background(), Config{Mode: ModeProxy, Viewers: []string{"*@example.com"}, Operators: []string{"group:sre"}}, quiet)
	srv := serve(a)
	defer srv.Close()
	call := func(path string, headers map[string]string) (int, string) {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if code, _ := call("/api/v1/snapshot", nil); code != http.StatusUnauthorized {
		t.Errorf("no header: %d", code)
	}
	if code, _ := call("/healthz", nil); code != 200 {
		t.Errorf("health checks stay open: %d", code)
	}
	if code, body := call("/api/v1/snapshot", map[string]string{"X-Forwarded-Email": "eve@elsewhere.io"}); code != http.StatusForbidden || !strings.Contains(body, "auth.viewers") {
		t.Errorf("not a viewer: %d %s", code, body)
	}
	code, body := call("/api/v1/me", map[string]string{"X-Forwarded-Email": "sam@example.com", "X-Forwarded-Groups": "dev, sre"})
	var u User
	_ = json.Unmarshal([]byte(body), &u)
	if code != 200 || u.Name != "sam@example.com" || u.Role != RoleOperator || len(u.Groups) != 2 || u.Mode != ModeProxy {
		t.Errorf("operator: %d %+v", code, u)
	}
}

func TestNoneMode(t *testing.T) {
	a, _ := New(context.Background(), Config{}, quiet)
	srv := serve(a)
	defer srv.Close()
	resp, _ := http.Get(srv.URL + "/api/v1/me")
	var u User
	_ = json.NewDecoder(resp.Body).Decode(&u)
	_ = resp.Body.Close()
	if u.Name != "anonymous" || !u.CanAct() || u.Mode != ModeNone {
		t.Errorf("none mode: %+v", u)
	}
}

func TestSessionsResistTampering(t *testing.T) {
	s := sessions{key: []byte("k")}
	rec := httptest.NewRecorder()
	s.write(rec, sessionCookie, session{Name: "ann", Groups: []string{"dev"}}, time.Hour)
	c := rec.Result().Cookies()[0]
	if !c.HttpOnly || c.SameSite != http.SameSiteLaxMode {
		t.Errorf("cookie flags: %+v", c)
	}
	read := func(value string) bool {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: value})
		_, ok := s.read(r, sessionCookie)
		return ok
	}
	if !read(c.Value) {
		t.Fatal("a valid cookie should read")
	}
	payload, sig, _ := strings.Cut(c.Value, ".")
	forged, _ := json.Marshal(session{Name: "admin", Groups: []string{"platform"}, Expires: time.Now().Add(time.Hour).Unix()})
	if read(base64.RawURLEncoding.EncodeToString(forged) + "." + sig) {
		t.Error("a changed payload must not read")
	}
	if (sessions{key: []byte("other")}).sign(payload) == sig {
		t.Error("another key must sign differently")
	}
	rec = httptest.NewRecorder()
	s.write(rec, sessionCookie, session{Name: "ann"}, -time.Minute)
	if read(rec.Result().Cookies()[0].Value) {
		t.Error("an expired cookie must not read")
	}
}

func TestSafeReturn(t *testing.T) {
	cases := map[string]string{
		"/services?x=1":        "/services?x=1",
		"":                     "/",
		"https://evil.example": "/",
		"//evil.example":       "/",
		"/\\evil.example":      "/",
		"/auth/logout":         "/",
	}
	for in, want := range cases {
		if got := safeReturn(in); got != want {
			t.Errorf("safeReturn(%q) = %q, want %q", in, got, want)
		}
	}
}

// fakeProvider is just enough of an OpenID Connect provider: discovery, keys
// and a token endpoint that checks PKCE and returns a signed ID token.
type fakeProvider struct {
	*httptest.Server
	key       *rsa.PrivateKey
	challenge string
	nonce     string
	groups    []string
}

func newFakeProvider(t *testing.T) *fakeProvider {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := &fakeProvider{key: key, groups: []string{"platform"}}
	mux := http.NewServeMux()
	p.Server = httptest.NewServer(mux)
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer": p.URL, "authorization_endpoint": p.URL + "/authorize", "token_endpoint": p.URL + "/token",
			"jwks_uri": p.URL + "/keys", "id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/keys", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig"}}})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		if r.Form.Get("code") != "good-code" || base64.RawURLEncoding.EncodeToString(sum[:]) != p.challenge {
			http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
			return
		}
		signer, _ := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: key, KeyID: "k1"}}, (&jose.SignerOptions{}).WithType("JWT"))
		claims, _ := json.Marshal(map[string]any{
			"iss": p.URL, "aud": "clusterview", "sub": "u-1", "email": "ann@example.com", "groups": p.groups,
			"nonce": p.nonce, "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(),
		})
		jws, _ := signer.Sign(claims)
		idToken, _ := jws.CompactSerialize()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "at", "token_type": "Bearer", "expires_in": 3600, "id_token": idToken})
	})
	return p
}

func TestOIDCSignIn(t *testing.T) {
	provider := newFakeProvider(t)
	defer provider.Close()

	var app *httptest.Server
	appMux := http.NewServeMux()
	app = httptest.NewServer(appMux)
	defer app.Close()
	a, err := New(context.Background(), Config{
		Mode: ModeOIDC, IssuerURL: provider.URL, ClientID: "clusterview", ClientSecret: "s",
		RedirectURL: app.URL + "/auth/callback", Operators: []string{"group:platform"},
	}, quiet)
	if err != nil {
		t.Fatal(err)
	}
	inner := http.NewServeMux()
	a.Routes(inner)
	inner.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { _ = json.NewEncoder(w).Encode(UserFrom(r.Context())) })
	appMux.Handle("/", a.Middleware(inner))

	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	get := func(u string) *http.Response {
		resp, err := client.Get(u)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp
	}

	// 1. A page needs sign-in; an API call gets 401 instead of a redirect.
	resp := get(app.URL + "/services")
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/auth/login?return=%2Fservices" {
		t.Fatalf("page without session: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	if resp := get(app.URL + "/api/v1/snapshot"); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("api without session: %d", resp.StatusCode)
	}

	// 2. Login sends the browser to the provider with state, nonce and PKCE.
	resp = get(app.URL + "/auth/login?return=%2Fservices")
	authURL, _ := url.Parse(resp.Header.Get("Location"))
	q := authURL.Query()
	if authURL.Path != "/authorize" || q.Get("code_challenge_method") != "S256" || q.Get("client_id") != "clusterview" {
		t.Fatalf("authorize redirect: %s", authURL)
	}
	provider.challenge, provider.nonce = q.Get("code_challenge"), q.Get("nonce")

	// 3. A callback with the wrong state is refused.
	if resp := get(app.URL + "/auth/callback?code=good-code&state=wrong"); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("wrong state: %d", resp.StatusCode)
	}

	// 4. The real callback signs in and returns to the page.
	resp = get(app.URL + "/auth/callback?code=good-code&state=" + url.QueryEscape(q.Get("state")))
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/services" {
		t.Fatalf("callback: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}

	// 5. Now requests carry the user, with the role from their groups.
	resp, _ = client.Get(app.URL + "/api/v1/me")
	var u User
	_ = json.NewDecoder(resp.Body).Decode(&u)
	_ = resp.Body.Close()
	if u.Name != "ann@example.com" || u.Role != RoleOperator || u.Mode != ModeOIDC {
		t.Fatalf("me: %+v", u)
	}

	// 6. Signing out ends it.
	get(app.URL + "/auth/logout")
	if resp := get(app.URL + "/api/v1/me"); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("after logout: %d", resp.StatusCode)
	}
}

func TestOIDCConfigErrors(t *testing.T) {
	if _, err := New(context.Background(), Config{Mode: ModeOIDC}, quiet); err == nil {
		t.Error("oidc without issuer should fail")
	}
	if _, err := New(context.Background(), Config{Mode: "magic"}, quiet); err == nil {
		t.Error("unknown mode should fail")
	}
}

func TestSignInUnderAPath(t *testing.T) {
	a := &Authenticator{
		cfg:      Config{Mode: ModeOIDC, BasePath: "/clusterview"},
		log:      slog.New(slog.DiscardHandler),
		sessions: sessions{key: []byte("k"), path: "/clusterview/"},
	}
	// Requests arrive with the path already removed.
	rec := httptest.NewRecorder()
	a.Middleware(http.NotFoundHandler()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/?select=x", nil))
	if loc := rec.Header().Get("Location"); loc != "/clusterview/auth/login?return=%2F%3Fselect%3Dx" {
		t.Errorf("sign-in redirect: %q", loc)
	}

	rec = httptest.NewRecorder()
	a.logout(rec, httptest.NewRequest(http.MethodGet, "/auth/logout", nil))
	if loc := rec.Header().Get("Location"); loc != "/clusterview/auth/signed-out" {
		t.Errorf("sign-out redirect: %q", loc)
	}
	if c := rec.Result().Cookies(); len(c) != 1 || c[0].Path != "/clusterview/" {
		t.Errorf("cookies must stay under the dashboard's path: %+v", c)
	}
}
