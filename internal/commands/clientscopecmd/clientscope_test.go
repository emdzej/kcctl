package clientscopecmd

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/emdzej/kcctl/internal/app"
	"github.com/emdzej/kcctl/internal/apptest"
)

var build = []func(*app.App) *cobra.Command{New}

const scopeID = "5c0be111-1111-4222-8333-944455556666"

var scopeList = apptest.Reply{Body: []any{
	map[string]any{"id": scopeID, "name": "dept", "protocol": "openid-connect"},
	map[string]any{"id": "other", "name": "profile", "protocol": "openid-connect"},
}}

func TestGetByName(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{
		"GET /admin/realms/test/client-scopes":            scopeList,
		"GET /admin/realms/test/client-scopes/" + scopeID: {Body: map[string]any{"id": scopeID, "name": "dept"}},
	}, apptest.Options{}, "client-scope", "get", "dept", "-o", "json")
	if r.Code != 0 {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
	if r.JSON(t).(map[string]any)["id"] != scopeID {
		t.Errorf("stdout = %s", r.Stdout)
	}
}

func TestCreateDefaultsProtocolAndStringAttributes(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{
		"POST /admin/realms/test/client-scopes": {Status: 201, Header: map[string]string{"Location": "{server}/admin/realms/test/client-scopes/" + scopeID}},
	}, apptest.Options{}, "scope", "create", "dept", "--attr", "include.in.token.scope=true")
	if r.Code != 0 {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
	b := r.Last(t).Body.(map[string]any)
	if b["name"] != "dept" || b["protocol"] != "openid-connect" {
		t.Errorf("body = %v", b)
	}
	// ClientScopeRepresentation.attributes is Map<String,String>, not lists.
	if b["attributes"].(map[string]any)["include.in.token.scope"] != "true" {
		t.Errorf("attributes = %v", b["attributes"])
	}
}

func TestMapperCreateUnderScope(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{
		"GET /admin/realms/test/client-scopes":                                          scopeList,
		"POST /admin/realms/test/client-scopes/" + scopeID + "/protocol-mappers/models": {Status: 201, Header: map[string]string{"Location": "{server}/x/m1"}},
	}, apptest.Options{}, "client-scope", "mapper", "create", "dept", "--name", "dept", "--mapper-type", "oidc-usermodel-attribute-mapper", "--config", "claim.name=dept")
	if r.Code != 0 {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
	if b := r.Last(t).Body.(map[string]any); b["protocolMapper"] != "oidc-usermodel-attribute-mapper" {
		t.Errorf("body = %v", b)
	}
}

func TestScopeMappingAdd(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{
		"GET /admin/realms/test/roles/admin":                                         {Body: map[string]any{"id": "r1", "name": "admin"}},
		"POST /admin/realms/test/client-scopes/" + scopeID + "/scope-mappings/realm": {},
	}, apptest.Options{}, "client-scope", "scope-mapping", "add", scopeID, "admin")
	if r.Code != 0 {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
	if !strings.Contains(r.Stderr, "client-scope '"+scopeID+"'") {
		t.Errorf("stderr = %q", r.Stderr)
	}
}
