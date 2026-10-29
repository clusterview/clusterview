package auth

import (
	"crypto/subtle"
	"html/template"
	"net/http"
	"strings"
	"time"
)

// passwordUser is the name everyone signed in with the shared password gets.
const passwordUser = "shared password"

// minPasswordLength keeps the shared password out of reach of guessing.
const minPasswordLength = 12

// failedSignInDelay slows down guessing. A variable for tests.
var failedSignInDelay = time.Second

func (a *Authenticator) passwordRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /auth/login", func(w http.ResponseWriter, r *http.Request) {
		a.loginPage(w, http.StatusOK, safeReturn(r.URL.Query().Get("return")), "")
	})
	mux.HandleFunc("POST /auth/login", a.passwordLogin)
	mux.HandleFunc("GET /auth/logout", a.logout)
	mux.HandleFunc("GET /auth/signed-out", signedOut)
}

func (a *Authenticator) passwordLogin(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Couldn't read the form.", http.StatusBadRequest)
		return
	}
	ret := safeReturn(r.PostForm.Get("return"))
	if subtle.ConstantTimeCompare([]byte(r.PostForm.Get("password")), []byte(a.cfg.Password)) != 1 {
		time.Sleep(failedSignInDelay)
		a.log.Info("sign-in with a wrong password")
		a.loginPage(w, http.StatusUnauthorized, ret, "That password isn't right.")
		return
	}
	s := a.sessions
	// No redirect URL says whether the dashboard is on https, so the request
	// does (directly, or through the load balancer).
	s.secure = s.secure || r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
	s.write(w, sessionCookie, session{Name: passwordUser}, a.cfg.SessionTTL)
	a.log.Info("signed in", "user", passwordUser)
	http.Redirect(w, r, a.cfg.BasePath+ret, http.StatusSeeOther) //nolint:gosec // G710: ret passed safeReturn, a path on this site
}

var loginTemplate = template.Must(template.New("login").Parse(`<!doctype html>
<html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Sign in · clusterview</title>
<style>
:root{color-scheme:light dark;--bg:#f6f7f9;--card:#fff;--text:#111827;--muted:#6b7280;--border:#e5e7eb;--accent:#4f46e5;--bad:#dc2626}
@media (prefers-color-scheme:dark){:root{--bg:#0b0d12;--card:#141821;--text:#e5e7eb;--muted:#9ca3af;--border:#262b36;--bad:#f87171}}
body{margin:0;min-height:100vh;display:grid;place-items:center;background:var(--bg);color:var(--text);font:15px/1.5 system-ui,-apple-system,sans-serif}
form{width:min(340px,calc(100vw - 32px));padding:28px;border-radius:16px;background:var(--card);border:1px solid var(--border);box-shadow:0 10px 30px #0000001a}
h1{margin:0 0 4px;font-size:19px}p{margin:0 0 18px;color:var(--muted);font-size:13.5px}
input{box-sizing:border-box;width:100%;height:40px;padding:0 12px;border-radius:9px;border:1px solid var(--border);background:transparent;color:inherit;font:inherit}
input:focus{outline:2px solid var(--accent);outline-offset:1px}
button{margin-top:12px;width:100%;height:40px;border:0;border-radius:9px;background:var(--accent);color:#fff;font:inherit;font-weight:600;cursor:pointer}
.error{margin:10px 0 0;color:var(--bad);font-size:13.5px}
</style>
<form method="post" action="login">
<h1>clusterview</h1>
<p>Enter the dashboard password.</p>
<input type="hidden" name="return" value="{{.Return}}">
<input type="password" name="password" aria-label="Password" placeholder="Password" autocomplete="current-password" autofocus required>
{{if .Error}}<p class="error" role="alert">{{.Error}}</p>{{end}}
<button type="submit">Sign in</button>
</form>
</html>`))

func (a *Authenticator) loginPage(w http.ResponseWriter, status int, ret, errMsg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = loginTemplate.Execute(w, struct{ Return, Error string }{ret, errMsg})
}
