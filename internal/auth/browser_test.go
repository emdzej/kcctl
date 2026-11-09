package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/emdzej/kcctl/internal/config"
	"github.com/emdzej/kcctl/internal/errs"
)

// fakeKeycloak authorizes instantly (as if the user signed in) and checks
// the PKCE verifier against the challenge, like the real token endpoint.
func fakeKeycloak(t *testing.T, authError string) *httptest.Server {
	var challenge string
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/realms/master/protocol/openid-connect/auth":
			q := r.URL.Query()
			if q.Get("code_challenge_method") != "S256" || q.Get("response_type") != "code" || q.Get("client_id") != "kcctl" {
				t.Errorf("auth query = %v", q)
			}
			if !strings.HasPrefix(q.Get("redirect_uri"), "http://127.0.0.1:") {
				t.Errorf("redirect_uri = %s", q.Get("redirect_uri"))
			}
			challenge = q.Get("code_challenge")
			back := url.Values{"state": {q.Get("state")}, "code": {"the-code"}}
			if authError != "" {
				back = url.Values{"state": {q.Get("state")}, "error": {authError}, "error_description": {"nope"}}
			}
			http.Redirect(w, r, q.Get("redirect_uri")+"?"+back.Encode(), http.StatusFound)
		case "/realms/master/protocol/openid-connect/token":
			_ = r.ParseForm()
			sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			if r.Form.Get("grant_type") != "authorization_code" || r.Form.Get("code") != "the-code" ||
				base64.RawURLEncoding.EncodeToString(sum[:]) != challenge {
				w.WriteHeader(400)
				json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
				return
			}
			idTok := "x." + base64.RawURLEncoding.EncodeToString([]byte(`{"preferred_username":"jane"}`)) + ".y"
			json.NewEncoder(w).Encode(map[string]any{"access_token": "at", "refresh_token": "rt", "id_token": idTok, "expires_in": 300, "refresh_expires_in": 1800})
		default:
			t.Errorf("unexpected %s", r.URL.Path)
		}
	}))
}

// visit plays the browser: follow the auth URL through the redirect to the
// CLI's loopback listener.
func visit(t *testing.T) func(string) {
	return func(u string) {
		go func() {
			resp, err := http.Get(u)
			if err != nil {
				t.Errorf("browser: %v", err)
				return
			}
			resp.Body.Close()
		}()
	}
}

func TestBrowserLogin(t *testing.T) {
	srv := fakeKeycloak(t, "")
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), Server: srv.URL, AuthRealm: "master", ClientID: "kcctl"}
	s, err := c.Browser(context.Background(), "", visit(t))
	if err != nil {
		t.Fatal(err)
	}
	if s.AccessToken != "at" || s.RefreshToken != "rt" || s.Grant != config.GrantBrowser || s.Username != "jane" {
		t.Errorf("session = %+v", s)
	}
}

func TestBrowserLoginReportsServerError(t *testing.T) {
	srv := fakeKeycloak(t, "unauthorized_client")
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), Server: srv.URL, AuthRealm: "master", ClientID: "kcctl"}
	_, err := c.Browser(context.Background(), "", visit(t))
	e := errs.As(err)
	if e.Code != errs.CodeAuth || !strings.Contains(e.Hint, "Standard flow") {
		t.Errorf("err = %v (hint %q)", e, e.Hint)
	}
}

func TestBrowserLoginRejectsForeignState(t *testing.T) {
	srv := fakeKeycloak(t, "")
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), Server: srv.URL, AuthRealm: "master", ClientID: "kcctl"}
	ctx, cancel := context.WithCancel(context.Background())
	_, err := c.Browser(ctx, "", func(u string) {
		go func() {
			au, _ := url.Parse(u)
			redirect := au.Query().Get("redirect_uri")
			resp, err := http.Get(redirect + "?state=forged&code=evil")
			if err == nil {
				if resp.StatusCode != 400 {
					t.Errorf("forged callback status = %d", resp.StatusCode)
				}
				resp.Body.Close()
			}
			cancel()
		}()
	})
	if err == nil {
		t.Fatal("a forged callback must not sign in")
	}
}
