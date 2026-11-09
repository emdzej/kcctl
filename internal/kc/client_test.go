package kc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/emdzej/keycloak-cli/internal/errs"
)

func TestErrorMapping(t *testing.T) {
	cases := []struct {
		status int
		body   string
		code   errs.Code
		exit   int
		msg    string
	}{
		{409, `{"errorMessage":"User exists with same username"}`, errs.CodeValidation, 3, "User exists with same username"},
		{400, `{"field":"email","errorMessage":"invalidEmailMessage"}`, errs.CodeValidation, 3, "field email: invalidEmailMessage"},
		{401, `{"error":"HTTP 401 Unauthorized"}`, errs.CodeAuth, 2, "HTTP 401 Unauthorized"},
		{403, `{"error":"unknown_error"}`, errs.CodePermission, 5, "unknown_error"},
		{404, `{"error":"User not found"}`, errs.CodeNotFound, 4, "User not found"},
		{500, `<html>boom</html>`, errs.CodeServer, 6, "server failed"},
	}
	for _, c := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("X-Request-Id", "rid")
			w.WriteHeader(c.status)
			w.Write([]byte(c.body))
		}))
		cl := &Client{HTTP: srv.Client(), Server: srv.URL, Realm: "r"}
		_, err := cl.Get(context.Background(), cl.RealmPath("users"), nil)
		srv.Close()
		e := errs.As(err)
		if e.Code != c.code || e.ExitCode() != c.exit || !strings.Contains(e.Message, c.msg) {
			t.Errorf("%d: code=%s exit=%d msg=%q", c.status, e.Code, e.ExitCode(), e.Message)
		}
		if e.Request == nil || e.Request.RequestID != "rid" {
			t.Errorf("%d: request info missing", c.status)
		}
		// 404s get no hint on purpose: the message already says it all.
		if c.status == 404 && e.Hint != "" {
			t.Errorf("404 hint = %q", e.Hint)
		}
	}
}

func TestRealmPathEscapes(t *testing.T) {
	c := &Client{Realm: "my realm"}
	if got := c.RealmPath("roles", "a/b c"); got != "/admin/realms/my%20realm/roles/a%2Fb%20c" {
		t.Errorf("got %s", got)
	}
}

func TestCreatedID(t *testing.T) {
	r := &Response{Header: http.Header{"Location": {"http://x/admin/realms/r/users/abc-123"}}}
	if r.CreatedID() != "abc-123" {
		t.Errorf("got %q", r.CreatedID())
	}
}

func TestMultipleFieldErrorsAreSeparated(t *testing.T) {
	body := `{"field":"email","errorMessage":"error-invalid-email","params":[],"errors":[{"field":"email","errorMessage":"error-invalid-email"},{"field":"username","errorMessage":"error-username-invalid-character"}]}`
	if got := serverMessage([]byte(body)); got != "field email: error-invalid-email; field username: error-username-invalid-character" {
		t.Errorf("got %q", got)
	}
}
