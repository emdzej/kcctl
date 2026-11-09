package clientcmd

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/emdzej/kcctl/internal/app"
	"github.com/emdzej/kcctl/internal/apptest"
)

var build = []func(*app.App) *cobra.Command{New}

const (
	webID  = "0a1b2c3d-1111-4222-8333-944455556666"
	userID = "6b9f3c2e-1111-4222-8333-944455556666"
	base   = "/admin/realms/test/clients/" + webID
)

// webLookup answers the clientId lookup the way ClientsResource does.
var webLookup = apptest.Reply{Body: []any{map[string]any{"id": webID, "clientId": "web"}}}

func ok(t *testing.T, r apptest.Result) {
	t.Helper()
	if r.Code != 0 {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
}

func TestGetByClientID(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{
		"GET /admin/realms/test/clients": webLookup,
		"GET " + base:                    {Body: map[string]any{"id": webID, "clientId": "web"}},
	}, apptest.Options{}, "client", "get", "web", "-o", "json")
	ok(t, r)
	if q := r.Sent[0].Query; q != "clientId=web" {
		t.Errorf("lookup query = %q", q)
	}
	if got := r.JSON(t).(map[string]any)["clientId"]; got != "web" {
		t.Errorf("clientId = %v", got)
	}
}

func TestListSearch(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{
		"GET /admin/realms/test/clients": webLookup,
	}, apptest.Options{}, "client", "list", "--search", "we", "--max", "5")
	ok(t, r)
	q := r.Last(t).Query
	for _, want := range []string{"clientId=we", "search=true", "max=5"} {
		if !strings.Contains(q, want) {
			t.Errorf("query %q lacks %s", q, want)
		}
	}
}

func TestCreateBody(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{
		"POST /admin/realms/test/clients": {Status: 201, Header: map[string]string{"Location": "{server}" + base}},
	}, apptest.Options{}, "client", "create", "web", "--public", "--redirect-uri", "https://a/*,https://b/*",
		"--direct-access=false", "--attr", "pkce.code.challenge.method=S256")
	ok(t, r)
	b := r.Last(t).Body.(map[string]any)
	if b["clientId"] != "web" || b["publicClient"] != true || b["directAccessGrantsEnabled"] != false {
		t.Errorf("body = %v", b)
	}
	if u := b["redirectUris"].([]any); len(u) != 2 || u[1] != "https://b/*" {
		t.Errorf("redirectUris = %v", u)
	}
	if b["attributes"].(map[string]any)["pkce.code.challenge.method"] != "S256" {
		t.Errorf("attributes = %v", b["attributes"])
	}
	if !strings.Contains(r.Stderr, webID) {
		t.Errorf("stderr = %q", r.Stderr)
	}
}

func TestDisableOverlaysCurrent(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{
		"GET " + base: {Body: map[string]any{"id": webID, "clientId": "web", "enabled": true, "redirectUris": []any{"x"}}},
		"PUT " + base: {},
	}, apptest.Options{}, "client", "disable", webID)
	ok(t, r)
	b := r.Last(t).Body.(map[string]any)
	if b["enabled"] != false || b["redirectUris"] == nil {
		t.Errorf("body = %v", b)
	}
}

func TestServiceAccountOld(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{
		"GET " + base + "/service-account-user": {Body: map[string]any{"id": userID, "username": "service-account-web"}},
	}, apptest.Options{Version: "26.6.3"}, "client", "service-account", webID, "-o", "json")
	ok(t, r)
	if got := r.JSON(t).(map[string]any)["username"]; got != "service-account-web" {
		t.Errorf("username = %v", got)
	}
}

func TestServiceAccountNew(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{
		"GET " + base:                            {Body: map[string]any{"id": webID, "serviceAccountUserId": userID}},
		"GET /admin/realms/test/users/" + userID: {Body: map[string]any{"id": userID, "username": "service-account-web"}},
	}, apptest.Options{Version: "27.0.0"}, "client", "service-account", webID, "-o", "json")
	ok(t, r)
	if got := r.JSON(t).(map[string]any)["username"]; got != "service-account-web" {
		t.Errorf("username = %v", got)
	}
}

func TestServiceAccountNewDisabled(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{
		"GET " + base: {Body: map[string]any{"id": webID}},
	}, apptest.Options{Version: "27.0.0"}, "client", "service-account", webID)
	if r.Code != 1 || !strings.Contains(r.Stderr, "no service account") {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
}

func TestClientRoleCreatePath(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{
		"GET /admin/realms/test/clients": webLookup,
		"POST " + base + "/roles":        {Status: 201, Header: map[string]string{"Location": "{server}" + base + "/roles/reader"}},
	}, apptest.Options{}, "client", "role", "create", "--client", "web", "reader", "--description", "Read")
	ok(t, r)
	b := r.Last(t).Body.(map[string]any)
	if b["name"] != "reader" || b["description"] != "Read" {
		t.Errorf("body = %v", b)
	}
}

func TestClientRoleNeedsClient(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{}, apptest.Options{}, "client", "role", "get", "reader")
	if r.Code != 1 || !strings.Contains(r.Stderr, "--client is required") {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
}

func TestCompositeAddRealmRole(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{
		"GET /admin/realms/test/roles/viewer":      {Body: map[string]any{"id": "r1", "name": "viewer"}},
		"POST " + base + "/roles/admin/composites": {},
	}, apptest.Options{}, "client", "role", "composite", "add", "--client", webID, "admin", "viewer")
	ok(t, r)
	if l := r.Last(t).Body.([]any); l[0].(map[string]any)["id"] != "r1" {
		t.Errorf("body = %v", l)
	}
}

func TestScopeAddOptional(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{
		"GET /admin/realms/test/client-scopes":       {Body: []any{map[string]any{"id": "s1", "name": "email"}, map[string]any{"id": "s2", "name": "phone"}}},
		"PUT " + base + "/optional-client-scopes/s2": {},
	}, apptest.Options{}, "client", "scope", "add", webID, "phone", "--optional")
	ok(t, r)
	if l := r.Last(t); l.Method != "PUT" || !strings.HasSuffix(l.Path, "/optional-client-scopes/s2") {
		t.Errorf("last = %+v", l)
	}
}

func availableReplies(src string) apptest.Replies {
	return apptest.Replies{
		"GET " + base:                             {Body: map[string]any{"id": webID, "protocol": "openid-connect"}},
		"GET " + base + "/default-client-scopes":  {Body: []any{map[string]any{"id": "s1", "name": "email", "protocol": "openid-connect"}}},
		"GET " + base + "/optional-client-scopes": {Body: []any{}},
		"GET " + src: {Body: []any{
			map[string]any{"id": "s1", "name": "email", "protocol": "openid-connect"},
			map[string]any{"id": "s2", "name": "phone", "protocol": "openid-connect"},
			map[string]any{"id": "s3", "name": "role_list", "protocol": "saml"},
		}},
	}
}

func checkAvailable(t *testing.T, r apptest.Result) {
	t.Helper()
	ok(t, r)
	l := r.JSON(t).([]any)
	if len(l) != 1 || l[0].(map[string]any)["name"] != "phone" {
		t.Errorf("available = %v", l)
	}
}

func TestAvailableScopesFallback(t *testing.T) {
	r := apptest.Run(t, build, availableReplies("/admin/realms/test/client-scopes"),
		apptest.Options{Version: "26.6.3"}, "client", "scope", "available", webID, "-o", "json")
	checkAvailable(t, r)
}

func TestAvailableScopesEndpoint(t *testing.T) {
	r := apptest.Run(t, build, availableReplies(base+"/available-client-scopes"),
		apptest.Options{Version: "27.0.0"}, "client", "scope", "available", webID, "-o", "json")
	checkAvailable(t, r)
}

func TestSecretRegenerate(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{
		"POST " + base + "/client-secret": {Body: map[string]any{"type": "secret", "value": "n3w"}},
	}, apptest.Options{}, "client", "secret", "regenerate", webID)
	ok(t, r)
	if r.Stdout != "n3w\n" {
		t.Errorf("stdout = %q", r.Stdout)
	}
	if !strings.Contains(r.Stderr, "Regenerated") || strings.Contains(r.Stderr, "n3w") {
		t.Errorf("stderr = %q", r.Stderr)
	}
}

func TestScopeMappingRealm(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{
		"GET /admin/realms/test/roles/admin":     {Body: map[string]any{"id": "r1", "name": "admin"}},
		"POST " + base + "/scope-mappings/realm": {},
	}, apptest.Options{}, "client", "scope-mappings", "add", webID, "admin")
	ok(t, r)
}

func TestInstallationRaw(t *testing.T) {
	xml := "<EntityDescriptor/>"
	r := apptest.Run(t, build, apptest.Replies{
		"GET " + base + "/installation/providers/saml-sp-descriptor": {Body: xml},
	}, apptest.Options{}, "client", "installation", webID, "--provider", "saml-sp-descriptor")
	ok(t, r)
	if r.Stdout != xml+"\n" {
		t.Errorf("stdout = %q", r.Stdout)
	}
}

func TestOfflineSessions(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{
		"GET " + base + "/offline-sessions": {Body: []any{}},
	}, apptest.Options{}, "client", "sessions", webID, "--offline", "--max", "10")
	ok(t, r)
	if r.Last(t).Query != "max=10" {
		t.Errorf("query = %q", r.Last(t).Query)
	}
}

func TestEvaluateToken(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{
		"GET " + base + "/evaluate-scopes/generate-example-access-token": {Body: map[string]any{"sub": userID}},
	}, apptest.Options{}, "client", "evaluate", "token", webID, "--user", userID, "--scope", "openid,email")
	ok(t, r)
	q := r.Last(t).Query
	if !strings.Contains(q, "userId="+userID) || !strings.Contains(q, "scope=openid+email") {
		t.Errorf("query = %q", q)
	}
}
