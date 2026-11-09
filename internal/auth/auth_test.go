package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/emdzej/kcctl/internal/config"
	"github.com/emdzej/kcctl/internal/errs"
)

type mem map[string]*Session

func (m mem) Load(k string) (*Session, error) { return m[k], nil }
func (m mem) Save(k string, s *Session) error { m[k] = s; return nil }
func (m mem) Delete(k string) error           { delete(m, k); return nil }

func tokenServer(t *testing.T, grants *[]string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/realms/master/protocol/openid-connect/token" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		_ = r.ParseForm()
		*grants = append(*grants, r.Form.Get("grant_type"))
		if r.Form.Get("password") == "wrong" {
			w.WriteHeader(401)
			json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant", "error_description": "Invalid user credentials"})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"access_token": "new", "refresh_token": "r2", "expires_in": 60, "refresh_expires_in": 1800})
	}))
}

func manager(srv *httptest.Server, store Store) *Manager {
	r := &config.Resolved{Key: "k", Context: config.Context{Server: srv.URL, AuthRealm: "master", ClientID: "admin-cli"}}
	return &Manager{Resolved: r, Store: store, Client: &Client{HTTP: srv.Client(), Server: srv.URL, AuthRealm: "master", ClientID: "admin-cli"}}
}

func TestRefreshesExpiredToken(t *testing.T) {
	var grants []string
	srv := tokenServer(t, &grants)
	defer srv.Close()
	store := mem{"k": {AccessToken: "old", RefreshToken: "r1", ExpiresAt: time.Now().Add(-time.Minute), Grant: config.GrantPassword}}
	tok, err := manager(srv, store).Token(context.Background())
	if err != nil || tok != "new" {
		t.Fatalf("token=%q err=%v", tok, err)
	}
	if len(grants) != 1 || grants[0] != "refresh_token" || store["k"].RefreshToken != "r2" {
		t.Errorf("grants=%v stored=%+v", grants, store["k"])
	}
}

func TestValidTokenMakesNoRequest(t *testing.T) {
	var grants []string
	srv := tokenServer(t, &grants)
	defer srv.Close()
	store := mem{"k": {AccessToken: "ok", ExpiresAt: time.Now().Add(time.Hour)}}
	if tok, _ := manager(srv, store).Token(context.Background()); tok != "ok" || len(grants) != 0 {
		t.Errorf("tok=%q grants=%v", tok, grants)
	}
}

func TestClientCredentialsRenewWithoutRefreshToken(t *testing.T) {
	var grants []string
	srv := tokenServer(t, &grants)
	defer srv.Close()
	store := mem{"k": {AccessToken: "old", ExpiresAt: time.Now().Add(-time.Minute), Grant: config.GrantClientCredentials, ClientSecret: "s"}}
	if _, err := manager(srv, store).Token(context.Background()); err != nil || grants[0] != "client_credentials" {
		t.Errorf("err=%v grants=%v", err, grants)
	}
}

func TestNotSignedIn(t *testing.T) {
	var grants []string
	srv := tokenServer(t, &grants)
	defer srv.Close()
	_, err := manager(srv, mem{}).Token(context.Background())
	if e := errs.As(err); e.Code != errs.CodeAuth || e.ExitCode() != 2 || e.Hint == "" {
		t.Errorf("err = %+v", e)
	}
}

func TestPasswordRejected(t *testing.T) {
	var grants []string
	srv := tokenServer(t, &grants)
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), Server: srv.URL, AuthRealm: "master", ClientID: "admin-cli"}
	_, err := c.Password(context.Background(), "admin", "wrong", "")
	if e := errs.As(err); e.Code != errs.CodeAuth || e.Message != "sign-in rejected (401): invalid_grant: Invalid user credentials" {
		t.Errorf("err = %q", e.Message)
	}
}

func TestConfidentialClientHint(t *testing.T) {
	// Keycloak's actual response for a confidential client without a secret.
	e := errs.As(tokenError(401, &tokenResponse{Error: "unauthorized_client", ErrorDescription: "Invalid client or Invalid client credentials"}))
	if !strings.Contains(e.Hint, "Client authentication is on") {
		t.Errorf("hint = %q", e.Hint)
	}
	e = errs.As(tokenError(400, &tokenResponse{Error: "unauthorized_client", ErrorDescription: "Client not allowed for direct access grants"}))
	if !strings.Contains(e.Hint, "does not allow this grant") {
		t.Errorf("hint = %q", e.Hint)
	}
}
