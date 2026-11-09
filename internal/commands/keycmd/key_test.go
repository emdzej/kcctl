package keycmd

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/emdzej/keycloak-cli/internal/app"
	"github.com/emdzej/keycloak-cli/internal/apptest"
)

var build = []func(*app.App) *cobra.Command{New}

// Shape of KeysMetadataRepresentation.
var keysDoc = map[string]any{
	"active": map[string]any{"RS256": "kid-1"},
	"keys": []any{
		map[string]any{"kid": "kid-1", "type": "RSA", "algorithm": "RS256", "use": "SIG", "status": "ACTIVE", "providerId": "p1", "providerPriority": 100, "publicKey": "MIIB", "validTo": 1900000000000},
		map[string]any{"kid": "kid-2", "type": "OCT", "algorithm": "HS512", "use": "SIG", "status": "PASSIVE", "providerId": "p2"},
	},
}

func TestListFlattensKeysForTable(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{"GET /admin/realms/test/keys": {Body: keysDoc}}, apptest.Options{}, "key", "list")
	if r.Code != 0 {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
	lines := strings.Split(strings.TrimSpace(r.Stdout), "\n")
	if len(lines) != 3 || !strings.Contains(lines[0], "KID") || !strings.Contains(lines[1], "kid-1") || !strings.Contains(lines[2], "PASSIVE") {
		t.Errorf("stdout =\n%s", r.Stdout)
	}
	if strings.Contains(r.Stdout, "MIIB") {
		t.Errorf("table should not include public keys:\n%s", r.Stdout)
	}
}

func TestListJSONKeepsDocument(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{"GET /admin/realms/test/keys": {Body: keysDoc}}, apptest.Options{}, "key", "list", "-o", "json")
	if r.Code != 0 {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
	d := r.JSON(t).(map[string]any)
	if d["active"].(map[string]any)["RS256"] != "kid-1" || len(d["keys"].([]any)) != 2 {
		t.Errorf("data = %v", d)
	}
}

func TestProvidersQueryType(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{"GET /admin/realms/test/components": {Body: []any{}}}, apptest.Options{}, "key", "providers")
	if r.Code != 0 || r.Last(t).Query != "type=org.keycloak.keys.KeyProvider" {
		t.Fatalf("exit %d: %s query=%q", r.Code, r.Stderr, r.Last(t).Query)
	}
}
