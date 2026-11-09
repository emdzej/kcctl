package usercmd

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/emdzej/keycloak-cli/internal/app"
	"github.com/emdzej/keycloak-cli/internal/apptest"
)

var build = []func(*app.App) *cobra.Command{New}

const aliceID = "6b9f3c2e-1111-4222-8333-944455556666"

func TestGetByNameResolvesThenFetches(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{
		"GET /admin/realms/test/users":            {Body: []any{map[string]any{"id": aliceID, "username": "alice"}}},
		"GET /admin/realms/test/users/" + aliceID: {Body: map[string]any{"id": aliceID, "username": "alice", "enabled": true}},
	}, apptest.Options{}, "user", "get", "alice", "-o", "json")
	if r.Code != 0 {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
	if !strings.Contains(r.Sent[0].Query, "exact=true") || !strings.Contains(r.Sent[0].Query, "username=alice") {
		t.Errorf("lookup query = %q", r.Sent[0].Query)
	}
	if got := r.JSON(t).(map[string]any)["username"]; got != "alice" {
		t.Errorf("username = %v", got)
	}
}

func TestUUIDSkipsLookup(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{
		"GET /admin/realms/test/users/" + aliceID: {Body: map[string]any{"id": aliceID}},
	}, apptest.Options{}, "user", "get", aliceID)
	if r.Code != 0 || len(r.Sent) != 1 {
		t.Fatalf("exit %d, sent %d: %s", r.Code, len(r.Sent), r.Stderr)
	}
}

func TestCreateSendsFieldsAndReportsID(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{
		"POST /admin/realms/test/users": {Status: 201, Header: map[string]string{"Location": "{server}/admin/realms/test/users/" + aliceID}},
	}, apptest.Options{}, "user", "create", "alice", "--email", "a@x.io", "--attr", "dept=sales")
	if r.Code != 0 {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
	b := r.Last(t).Body.(map[string]any)
	if b["username"] != "alice" || b["email"] != "a@x.io" || b["enabled"] != true {
		t.Errorf("body = %v", b)
	}
	if attrs := b["attributes"].(map[string]any); attrs["dept"].([]any)[0] != "sales" {
		t.Errorf("attributes = %v", attrs)
	}
	if !strings.Contains(r.Stderr, aliceID) {
		t.Errorf("stderr = %q", r.Stderr)
	}
}

func TestSetRejectsSecrets(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{}, apptest.Options{}, "user", "create", "bob", "-s", "credentials.password=x")
	if r.Code != 1 || !strings.Contains(r.Stderr, "cannot carry a secret") {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
}

func TestUpdateOverlaysOnCurrent(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{
		"GET /admin/realms/test/users/" + aliceID: {Body: map[string]any{"id": aliceID, "username": "alice", "email": "old@x.io", "enabled": true}},
		"PUT /admin/realms/test/users/" + aliceID: {},
	}, apptest.Options{}, "user", "update", aliceID, "--enabled=false")
	if r.Code != 0 {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
	b := r.Last(t).Body.(map[string]any)
	if b["enabled"] != false || b["email"] != "old@x.io" {
		t.Errorf("body = %v", b)
	}
}

func TestSetPasswordFromStdinNeverOnLine(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{
		"PUT /admin/realms/test/users/" + aliceID + "/reset-password": {},
	}, apptest.Options{Stdin: "s3cret\n"}, "user", "set-password", aliceID, "--temporary")
	if r.Code != 0 {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
	b := r.Last(t).Body.(map[string]any)
	if b["value"] != "s3cret" || b["temporary"] != true {
		t.Errorf("body = %v", b)
	}
}

func TestBulkDeleteNeedsYesWithoutTTY(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{}, apptest.Options{}, "user", "delete", aliceID, aliceID)
	if r.Code != 1 || !strings.Contains(r.Stderr, "--yes") {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
}

func TestNotFoundExitCode(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{
		"GET /admin/realms/test/users": {Body: []any{}},
	}, apptest.Options{}, "user", "get", "ghost")
	if r.Code != 4 || !strings.Contains(r.Stderr, "no user named 'ghost'") {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
}

func TestVCRequiresNewerServer(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{}, apptest.Options{Version: "26.6.3"}, "user", "vc", aliceID)
	if r.Code != 1 || !strings.Contains(r.Stderr, "26.8.0 or newer") {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
}

func TestRoleAddResolvesRoles(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{
		"GET /admin/realms/test/roles/admin":                                {Body: map[string]any{"id": "r1", "name": "admin"}},
		"POST /admin/realms/test/users/" + aliceID + "/role-mappings/realm": {},
	}, apptest.Options{}, "user", "role", "add", aliceID, "admin")
	if r.Code != 0 {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
	if l := r.Last(t).Body.([]any); l[0].(map[string]any)["id"] != "r1" {
		t.Errorf("body = %v", l)
	}
}
