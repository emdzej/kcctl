package groupcmd

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/emdzej/kcctl/internal/app"
	"github.com/emdzej/kcctl/internal/apptest"
)

var build = []func(*app.App) *cobra.Command{New}

const (
	staffID = "aaaaaaaa-1111-4222-8333-944455556666"
	engID   = "bbbbbbbb-1111-4222-8333-944455556666"
	aliceID = "cccccccc-1111-4222-8333-944455556666"
)

func TestGetByPathUsesGroupByPath(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{
		"GET /admin/realms/test/group-by-path/staff/eng": {Body: map[string]any{"id": engID, "name": "eng", "path": "/staff/eng"}},
		"GET /admin/realms/test/groups/" + engID:         {Body: map[string]any{"id": engID, "name": "eng", "path": "/staff/eng"}},
	}, apptest.Options{}, "group", "get", "/staff/eng", "-o", "json")
	if r.Code != 0 {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
	if r.JSON(t).(map[string]any)["path"] != "/staff/eng" {
		t.Errorf("stdout = %s", r.Stdout)
	}
}

func TestGetByNestedName(t *testing.T) {
	// A search returns matches nested under their ancestors.
	r := apptest.Run(t, build, apptest.Replies{
		"GET /admin/realms/test/groups": {Body: []any{map[string]any{"id": staffID, "name": "staff", "path": "/staff",
			"subGroups": []any{map[string]any{"id": engID, "name": "eng", "path": "/staff/eng"}}}}},
		"GET /admin/realms/test/groups/" + engID: {Body: map[string]any{"id": engID, "name": "eng"}},
	}, apptest.Options{}, "group", "get", "eng")
	if r.Code != 0 {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
	if q := r.Sent[0].Query; !strings.Contains(q, "search=eng") || !strings.Contains(q, "exact=true") {
		t.Errorf("lookup query = %q", q)
	}
}

func TestCreateTopLevel(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{
		"POST /admin/realms/test/groups": {Status: 201, Header: map[string]string{"Location": "{server}/admin/realms/test/groups/" + staffID}},
	}, apptest.Options{}, "group", "create", "staff", "--attr", "cc=42")
	if r.Code != 0 {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
	b := r.Last(t).Body.(map[string]any)
	if b["name"] != "staff" || b["attributes"].(map[string]any)["cc"].([]any)[0] != "42" {
		t.Errorf("body = %v", b)
	}
}

func TestCreateUnderParent(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{
		"GET /admin/realms/test/group-by-path/staff":              {Body: map[string]any{"id": staffID, "path": "/staff"}},
		"POST /admin/realms/test/groups/" + staffID + "/children": {Status: 201, Header: map[string]string{"Location": "{server}/admin/realms/test/groups/" + engID}},
	}, apptest.Options{}, "group", "create", "eng", "--parent", "/staff")
	if r.Code != 0 {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
	if b := r.Last(t).Body.(map[string]any); b["name"] != "eng" {
		t.Errorf("body = %v", b)
	}
	if !strings.Contains(r.Stderr, engID) {
		t.Errorf("stderr = %q", r.Stderr)
	}
}

func TestListTreeFetchesChildren(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{
		"GET /admin/realms/test/groups":                          {Body: []any{map[string]any{"id": staffID, "name": "staff", "path": "/staff", "subGroupCount": 1, "subGroups": []any{}}}},
		"GET /admin/realms/test/groups/" + staffID + "/children": {Body: []any{map[string]any{"id": engID, "name": "eng", "path": "/staff/eng", "subGroupCount": 0}}},
	}, apptest.Options{}, "group", "list", "--tree")
	if r.Code != 0 {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
	if !strings.Contains(r.Stdout, "/staff/eng") {
		t.Errorf("stdout = %s", r.Stdout)
	}
	if q := r.Last(t).Query; !strings.Contains(q, "briefRepresentation=true") || !strings.Contains(q, "max=100") {
		t.Errorf("children query = %q", q)
	}
}

func TestDeleteWithSubgroupsNeedsYes(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{
		"GET /admin/realms/test/groups/" + staffID: {Body: map[string]any{"id": staffID, "subGroupCount": 2}},
	}, apptest.Options{}, "group", "delete", staffID)
	if r.Code != 1 || !strings.Contains(r.Stderr, "2 subgroups") {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
}

func TestDeleteLeafDoesNotAsk(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{
		"GET /admin/realms/test/groups/" + engID:    {Body: map[string]any{"id": engID, "subGroupCount": 0}},
		"DELETE /admin/realms/test/groups/" + engID: {},
	}, apptest.Options{}, "group", "delete", engID)
	if r.Code != 0 {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
}

func TestMemberAddGoesThroughUser(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{
		"GET /admin/realms/test/group-by-path/staff":                     {Body: map[string]any{"id": staffID}},
		"GET /admin/realms/test/users":                                   {Body: []any{map[string]any{"id": aliceID, "username": "alice"}}},
		"PUT /admin/realms/test/users/" + aliceID + "/groups/" + staffID: {},
	}, apptest.Options{}, "group", "member", "add", "/staff", "alice")
	if r.Code != 0 {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
}

func TestMemberListBrief(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{
		"GET /admin/realms/test/groups/" + staffID + "/members": {Body: []any{map[string]any{"id": aliceID, "username": "alice"}}},
	}, apptest.Options{}, "group", "member", "list", staffID)
	if r.Code != 0 || !strings.Contains(r.Last(t).Query, "briefRepresentation=true") {
		t.Fatalf("exit %d: %s %+v", r.Code, r.Stderr, r.Sent)
	}
}

func TestRoleMappingOnGroup(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{
		"GET /admin/realms/test/group-by-path/staff":                         {Body: map[string]any{"id": staffID}},
		"GET /admin/realms/test/roles/admin":                                 {Body: map[string]any{"id": "r1", "name": "admin"}},
		"POST /admin/realms/test/groups/" + staffID + "/role-mappings/realm": {},
	}, apptest.Options{}, "group", "role", "add", "/staff", "admin")
	if r.Code != 0 {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
	if l := r.Last(t).Body.([]any); l[0].(map[string]any)["id"] != "r1" {
		t.Errorf("body = %v", l)
	}
}

func TestCountPrintsNumber(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{
		"GET /admin/realms/test/groups/count": {Body: map[string]any{"count": 7}},
	}, apptest.Options{}, "group", "count", "--top")
	if r.Code != 0 || strings.TrimSpace(r.Stdout) != "7" || r.Last(t).Query != "top=true" {
		t.Fatalf("exit %d: %q %s", r.Code, r.Stdout, r.Stderr)
	}
}
