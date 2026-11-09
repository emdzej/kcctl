package policycmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"

	"github.com/emdzej/kcctl/internal/app"
	"github.com/emdzej/kcctl/internal/apptest"
)

var build = []func(*app.App) *cobra.Command{New}

func TestProfilesGetIncludeGlobal(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{
		"GET /admin/realms/test/client-policies/profiles": {Body: map[string]any{
			"profiles": []any{}, "globalProfiles": []any{map[string]any{"name": "fapi-1-baseline"}},
		}},
	}, apptest.Options{}, "client-policy", "profiles", "get", "--include-global")
	if r.Code != 0 || r.Last(t).Query != "include-global-profiles=true" {
		t.Fatalf("exit %d: %s query=%q", r.Code, r.Stderr, r.Last(t).Query)
	}
}

func TestPoliciesUpdateFromFileDropsGlobals(t *testing.T) {
	f := filepath.Join(t.TempDir(), "p.yaml")
	os.WriteFile(f, []byte("policies:\n- name: p1\n  enabled: true\n  profiles: [fapi-1-baseline]\nglobalPolicies: []\n"), 0o600)
	r := apptest.Run(t, build, apptest.Replies{
		"GET /admin/realms/test/client-policies/policies": {Body: map[string]any{"policies": []any{}}},
		"PUT /admin/realms/test/client-policies/policies": {},
	}, apptest.Options{}, "client-policy", "policies", "update", "-f", f)
	if r.Code != 0 {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
	b := r.Last(t).Body.(map[string]any)
	if _, ok := b["globalPolicies"]; ok || len(b["policies"].([]any)) != 1 {
		t.Errorf("body = %v", b)
	}
}
