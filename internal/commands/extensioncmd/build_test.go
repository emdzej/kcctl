package extensioncmd

import (
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/emdzej/kcctl/internal/app"
	"github.com/emdzej/kcctl/internal/apptest"
	"github.com/emdzej/kcctl/internal/extension"
)

func apiKeys(t *testing.T) []func(*app.App) *cobra.Command {
	t.Helper()
	data, err := os.ReadFile("../../../docs/examples/api-keys.yaml")
	if err != nil {
		t.Fatal(err)
	}
	m, err := extension.Parse(data, "api-keys.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return []func(*app.App) *cobra.Command{func(a *app.App) *cobra.Command {
		cmd := Build(a, m)[0]
		cmd.GroupID = "" // the test root has no help groups
		return cmd
	}}
}

var alice = apptest.Reply{Body: []any{map[string]any{"id": "u-1", "username": "alice"}}}

func TestListResolvesUserAndPassesFilters(t *testing.T) {
	r := apptest.Run(t, apiKeys(t), apptest.Replies{
		"GET /admin/realms/test/users":    alice,
		"GET /admin/realms/test/api-keys": {Body: []any{map[string]any{"id": "k-1", "name": "ci", "keyPrefix": "myapi_"}}},
	}, apptest.Options{}, "api-key", "list", "--user", "alice", "--status", "active", "--max", "5")
	if r.Code != 0 {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
	q := r.Last(t).Query
	for _, want := range []string{"userId=u-1", "status=active", "max=5"} {
		if !strings.Contains(q, want) {
			t.Errorf("query %q lacks %s", q, want)
		}
	}
	if !strings.Contains(r.Stdout, "myapi_") || !strings.Contains(r.Stdout, "PREFIX") && !strings.Contains(r.Stdout, "prefix") {
		t.Errorf("table: %s", r.Stdout)
	}
}

func TestCreatePostsBodyAndWarnsSecret(t *testing.T) {
	r := apptest.Run(t, apiKeys(t), apptest.Replies{
		"GET /admin/realms/test/users":                        alice,
		"POST /admin/realms/test/api-keys/users/u-1/api-keys": {Status: 201, Body: map[string]any{"id": "k-1", "key": "myapi_secret", "name": "ci"}},
	}, apptest.Options{}, "api-key", "create", "alice", "--name", "ci", "--client", "my-api", "--role", "a,b", "-o", "json")
	if r.Code != 0 {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
	body := r.Last(t).Body.(map[string]any)
	if body["name"] != "ci" || body["clientId"] != "my-api" || len(body["roles"].([]any)) != 2 {
		t.Errorf("body: %v", body)
	}
	data := r.JSON(t).(map[string]any)
	if data["key"] != "myapi_secret" {
		t.Errorf("stdout: %s", r.Stdout)
	}
	if !strings.Contains(r.Stderr, "shown only this once") {
		t.Errorf("no secret warning: %s", r.Stderr)
	}
}

func TestCreateRequiresFields(t *testing.T) {
	r := apptest.Run(t, apiKeys(t), apptest.Replies{"GET /admin/realms/test/users": alice},
		apptest.Options{}, "api-key", "create", "alice", "--name", "ci")
	if r.Code != 1 || !strings.Contains(r.Stderr, "clientId") {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
}

func TestRevokeEscapesAndReports(t *testing.T) {
	r := apptest.Run(t, apiKeys(t), apptest.Replies{
		"DELETE /admin/realms/test/api-keys/a%2Fb": {Status: 204},
	}, apptest.Options{}, "api-key", "revoke", "a/b")
	if r.Code != 0 {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
	if !strings.Contains(r.Stderr, "Revoked API key a/b.") {
		t.Errorf("message: %s", r.Stderr)
	}
}

func TestRequiresMissingProvider(t *testing.T) {
	r := apptest.Run(t, apiKeys(t), apptest.Replies{
		"GET /admin/serverinfo": {Body: map[string]any{
			"systemInfo": map[string]any{"version": "26.6.3"},
			"providers":  map[string]any{"admin-realm-restapi-extension": map[string]any{"providers": map[string]any{"other": map[string]any{}}}},
		}},
	}, apptest.Options{}, "api-key", "health")
	if r.Code != 1 || !strings.Contains(r.Stderr, "admin-realm-restapi-extension/api-keys") {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
}

func TestSelfCompletion(t *testing.T) {
	r := apptest.Run(t, apiKeys(t), apptest.Replies{
		"GET /admin/realms/test/api-keys": {Body: []any{map[string]any{"id": "k-1", "name": "ci"}}},
	}, apptest.Options{}, "__complete", "api-key", "stats", "")
	if !strings.Contains(r.Stdout, "k-1\tci") {
		t.Fatalf("completion: %s %s", r.Stdout, r.Stderr)
	}
}
