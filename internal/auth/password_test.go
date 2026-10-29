package auth

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestPasswordMode(t *testing.T) {
	failedSignInDelay = 0
	if _, err := New(context.Background(), Config{Mode: ModePassword, Password: "short"}, slog.New(slog.DiscardHandler)); err == nil {
		t.Error("a short password should be refused")
	}
	a, err := New(context.Background(), Config{Mode: ModePassword, Password: "correct horse battery", BasePath: "/clusterview"}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	a.Routes(mux)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "hello "+UserFrom(r.Context()).Name)
	})
	// As underPath would: the base path is removed before the handler.
	srv := httptest.NewServer(http.StripPrefix("/clusterview", a.Middleware(mux)))
	defer srv.Close()
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	resp, _ := c.Get(srv.URL + "/clusterview/?select=x")
	if loc := resp.Header.Get("Location"); resp.StatusCode != http.StatusFound || loc != "/clusterview/auth/login?return=%2F%3Fselect%3Dx" {
		t.Fatalf("not signed in: %d %q", resp.StatusCode, loc)
	}
	resp, _ = c.Get(srv.URL + "/clusterview/api/v1/me")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("API without signing in: %d", resp.StatusCode)
	}

	resp, _ = c.Get(srv.URL + "/clusterview/auth/login?return=%2F%3Fselect%3Dx")
	page, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(page), `type="password"`) || !strings.Contains(string(page), `value="/?select=x"`) {
		t.Errorf("login page: %s", page)
	}

	resp, _ = c.PostForm(srv.URL+"/clusterview/auth/login", url.Values{"password": {"wrong"}, "return": {"/?select=x"}})
	page, _ = io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusUnauthorized || !strings.Contains(string(page), "That password") {
		t.Errorf("wrong password: %d", resp.StatusCode)
	}

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/clusterview/auth/login", strings.NewReader(url.Values{"password": {"correct horse battery"}, "return": {"/?select=x"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Forwarded-Proto", "https")
	resp, _ = c.Do(req)
	if loc := resp.Header.Get("Location"); resp.StatusCode != http.StatusSeeOther || loc != "/clusterview/?select=x" {
		t.Fatalf("right password: %d %q", resp.StatusCode, loc)
	}
	cookies := resp.Cookies()
	if len(cookies) != 1 || cookies[0].Path != "/clusterview/" || !cookies[0].Secure || !cookies[0].HttpOnly {
		t.Errorf("session cookie: %+v", cookies)
	}

	// httptest is plain http, so a Secure cookie wouldn't be sent back; send
	// it by hand.
	req, _ = http.NewRequest(http.MethodGet, srv.URL+"/clusterview/", nil)
	req.AddCookie(cookies[0])
	resp, _ = c.Do(req)
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "hello "+passwordUser {
		t.Errorf("signed in: %d %q", resp.StatusCode, body)
	}
}
