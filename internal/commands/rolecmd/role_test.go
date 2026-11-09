package rolecmd

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/emdzej/kcctl/internal/app"
	"github.com/emdzej/kcctl/internal/apptest"
)

var build = []func(*app.App) *cobra.Command{New}

const clientID = "0c1d2e3f-1111-4222-8333-944455556666"

func TestGetUsesNameInPath(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{
		"GET /admin/realms/test/roles/team%20lead": {Body: map[string]any{"id": "r1", "name": "team lead"}},
	}, apptest.Options{}, "role", "get", "team lead", "-o", "json")
	if r.Code != 0 || len(r.Sent) != 1 {
		t.Fatalf("exit %d, sent %d: %s", r.Code, len(r.Sent), r.Stderr)
	}
	if r.JSON(t).(map[string]any)["name"] != "team lead" {
		t.Errorf("stdout = %s", r.Stdout)
	}
}

func TestListSendsFilters(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{
		"GET /admin/realms/test/roles": {Body: []any{}},
	}, apptest.Options{}, "role", "list", "--search", "adm", "--max", "5")
	if r.Code != 0 {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
	q := r.Last(t).Query
	for _, want := range []string{"search=adm", "max=5", "briefRepresentation=true"} {
		if !strings.Contains(q, want) {
			t.Errorf("query %q lacks %s", q, want)
		}
	}
}

func TestCreateBody(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{
		"POST /admin/realms/test/roles": {Status: 201, Header: map[string]string{"Location": "{server}/admin/realms/test/roles/auditor"}},
	}, apptest.Options{}, "role", "create", "auditor", "--description", "Reads", "--attr", "team=sec")
	if r.Code != 0 {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
	b := r.Last(t).Body.(map[string]any)
	if b["name"] != "auditor" || b["description"] != "Reads" {
		t.Errorf("body = %v", b)
	}
	if b["attributes"].(map[string]any)["team"].([]any)[0] != "sec" {
		t.Errorf("attributes = %v", b["attributes"])
	}
	if !strings.Contains(r.Stderr, "Created role 'auditor'.") {
		t.Errorf("stderr = %q", r.Stderr)
	}
}

func TestCompositeAddRealmRoles(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{
		"GET /admin/realms/test/roles/auditor":           {Body: map[string]any{"id": "r2", "name": "auditor"}},
		"POST /admin/realms/test/roles/admin/composites": {},
	}, apptest.Options{}, "role", "composite", "add", "admin", "auditor")
	if r.Code != 0 {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
	if l := r.Last(t).Body.([]any); len(l) != 1 || l[0].(map[string]any)["id"] != "r2" {
		t.Errorf("body = %v", l)
	}
}

func TestCompositeRemoveClientRoles(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{
		"GET /admin/realms/test/clients":                                   {Body: []any{map[string]any{"id": clientID, "clientId": "realm-management"}}},
		"GET /admin/realms/test/clients/" + clientID + "/roles/view-users": {Body: map[string]any{"id": "c1", "name": "view-users"}},
		"DELETE /admin/realms/test/roles/admin/composites":                 {},
	}, apptest.Options{}, "role", "composite", "remove", "admin", "view-users", "--client", "realm-management")
	if r.Code != 0 {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
	last := r.Last(t)
	if last.Method != "DELETE" || last.Body.([]any)[0].(map[string]any)["id"] != "c1" {
		t.Errorf("last = %+v", last)
	}
}

func TestCompositeListByClient(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{
		"GET /admin/realms/test/clients":                                    {Body: []any{map[string]any{"id": clientID, "clientId": "web"}}},
		"GET /admin/realms/test/roles/admin/composites/clients/" + clientID: {Body: []any{map[string]any{"name": "x"}}},
	}, apptest.Options{}, "role", "composite", "list", "admin", "--client", "web")
	if r.Code != 0 {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
}

func TestUsersPaged(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{
		"GET /admin/realms/test/roles/admin/users": {Body: []any{map[string]any{"username": "alice"}}},
	}, apptest.Options{}, "role", "users", "admin", "--all")
	if r.Code != 0 || !strings.Contains(r.Stdout, "alice") {
		t.Fatalf("exit %d: %s %s", r.Code, r.Stdout, r.Stderr)
	}
	if q := r.Last(t).Query; !strings.Contains(q, "first=0") || !strings.Contains(q, "max=100") {
		t.Errorf("query = %q", q)
	}
}

func TestMissingRoleIsNotFound(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{
		"GET /admin/realms/test/roles/ghost": {Status: 404, Body: map[string]any{"error": "Could not find role"}},
	}, apptest.Options{}, "role", "composite", "add", "admin", "ghost")
	if r.Code != 4 || !strings.Contains(r.Stderr, "no role named 'ghost'") {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
}
