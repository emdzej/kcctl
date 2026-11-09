package workflowcmd

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/emdzej/kcctl/internal/app"
	"github.com/emdzej/kcctl/internal/apptest"
)

var build = []func(*app.App) *cobra.Command{New}

const (
	wfID    = "1f2e3d4c-1111-4222-8333-944455556666"
	aliceID = "6b9f3c2e-1111-4222-8333-944455556666"
)

var enabled = apptest.Options{Features: map[string]bool{"WORKFLOWS": true}}

func TestFeatureDisabled(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{}, apptest.Options{Features: map[string]bool{"WORKFLOWS": false}}, "workflow", "list")
	if r.Code != 1 || !strings.Contains(r.Stderr, "WORKFLOWS feature") {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
}

func TestGetByName(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{
		"GET /admin/realms/test/workflows":         {Body: []any{map[string]any{"id": wfID, "name": "disable-inactive"}}},
		"GET /admin/realms/test/workflows/" + wfID: {Body: map[string]any{"id": wfID, "name": "disable-inactive"}},
	}, enabled, "workflow", "get", "disable-inactive", "--no-ids")
	if r.Code != 0 {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
	if q := r.Sent[0].Query; !strings.Contains(q, "exact=true") || !strings.Contains(q, "search=disable-inactive") {
		t.Errorf("lookup query = %q", q)
	}
	if q := r.Last(t).Query; q != "includeId=false" {
		t.Errorf("get query = %q", q)
	}
}

func TestActivateUser(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{
		"GET /admin/realms/test/users":                                             {Body: []any{map[string]any{"id": aliceID, "username": "alice"}}},
		"POST /admin/realms/test/workflows/" + wfID + "/activate/USERS/" + aliceID: {},
	}, enabled, "workflow", "activate", wfID, "user", "alice", "--not-before", "3600")
	if r.Code != 0 || r.Last(t).Query != "notBefore=3600" {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
}

func TestUpdateOverlays(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{
		"GET /admin/realms/test/workflows/" + wfID: {Body: map[string]any{"id": wfID, "name": "w", "enabled": true, "steps": []any{map[string]any{"uses": "disable-user"}}}},
		"PUT /admin/realms/test/workflows/" + wfID: {},
	}, enabled, "workflow", "update", wfID, "--enabled=false")
	if r.Code != 0 {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
	b := r.Last(t).Body.(map[string]any)
	if b["enabled"] != false || len(b["steps"].([]any)) != 1 {
		t.Errorf("body = %v", b)
	}
}
