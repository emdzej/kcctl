package idpcmd

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/emdzej/keycloak-cli/internal/app"
	"github.com/emdzej/keycloak-cli/internal/apptest"
)

var build = []func(*app.App) *cobra.Command{New}

const inst = "/admin/realms/test/identity-provider/instances"

func TestCreateReadsSecretFromStdin(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{
		"POST " + inst: {Status: 201, Header: map[string]string{"Location": "{server}" + inst + "/corp"}},
	}, apptest.Options{Stdin: "s3cret\n"}, "idp", "create", "corp", "--provider-id", "oidc",
		"--config", "clientId=kc", "--trust-email")
	if r.Code != 0 {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
	b := r.Last(t).Body.(map[string]any)
	cfg := b["config"].(map[string]any)
	if b["alias"] != "corp" || b["providerId"] != "oidc" || b["trustEmail"] != true {
		t.Errorf("body = %v", b)
	}
	if cfg["clientSecret"] != "s3cret" || cfg["clientId"] != "kc" {
		t.Errorf("config = %v", cfg)
	}
}

func TestCreateSAMLNeedsNoSecret(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{
		"POST " + inst: {Status: 201, Header: map[string]string{"Location": "{server}" + inst + "/s"}},
	}, apptest.Options{}, "idp", "create", "s", "--provider-id", "saml")
	if r.Code != 0 {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
	if _, ok := r.Last(t).Body.(map[string]any)["config"].(map[string]any)["clientSecret"]; ok {
		t.Error("saml body carries a clientSecret")
	}
}

func TestConfigRejectsSecret(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{}, apptest.Options{}, "idp", "create", "corp",
		"--provider-id", "oidc", "--config", "clientSecret=x")
	if r.Code != 1 || !strings.Contains(r.Stderr, "cannot carry the client secret") {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
}

// Keycloak swaps the masked value for the stored secret on PUT and replaces
// the whole config map, so the masked value must go back untouched.
func TestUpdateOn27KeepsMaskedSecret(t *testing.T) {
	cur := map[string]any{"alias": "corp", "providerId": "oidc", "internalId": "i1",
		"config": map[string]any{"clientId": "kc", "clientSecret": masked}}
	r := apptest.Run(t, build, apptest.Replies{
		"GET " + inst + "/corp": {Body: cur},
		"PUT " + inst + "/corp": {},
	}, apptest.Options{Version: "27.0.0"}, "idp", "update", "corp", "--display-name", "Corp")
	if r.Code != 0 {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
	b := r.Last(t).Body.(map[string]any)
	if b["displayName"] != "Corp" || b["config"].(map[string]any)["clientSecret"] != masked {
		t.Errorf("body = %v", b)
	}
}

func TestUpdateReplacesSecretFromFile(t *testing.T) {
	cur := map[string]any{"alias": "corp", "providerId": "oidc",
		"config": map[string]any{"clientSecret": masked}}
	r := apptest.Run(t, build, apptest.Replies{
		"GET " + inst + "/corp": {Body: cur},
		"PUT " + inst + "/corp": {},
	}, apptest.Options{Version: "27.0.0", Stdin: "new\n"}, "idp", "update", "corp", "--client-secret-file", "-")
	if r.Code != 0 {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
	if got := r.Last(t).Body.(map[string]any)["config"].(map[string]any)["clientSecret"]; got != "new" {
		t.Errorf("clientSecret = %v", got)
	}
}

func TestMapperCreateSetsAlias(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{
		"POST " + inst + "/corp/mappers": {Status: 201, Header: map[string]string{"Location": "{server}" + inst + "/corp/mappers/m1"}},
	}, apptest.Options{}, "idp", "mapper", "create", "corp", "--name", "dept",
		"--mapper-type", "oidc-user-attribute-idp-mapper", "--sync-mode", "FORCE", "--config", "claim=dept")
	if r.Code != 0 {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
	b := r.Last(t).Body.(map[string]any)
	cfg := b["config"].(map[string]any)
	if b["identityProviderAlias"] != "corp" || cfg["syncMode"] != "FORCE" || cfg["claim"] != "dept" {
		t.Errorf("body = %v", b)
	}
}

func TestImportConfigFromURL(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{
		"POST /admin/realms/test/identity-provider/import-config": {Body: map[string]any{"issuer": "https://corp"}},
	}, apptest.Options{}, "idp", "import-config", "--provider-id", "oidc", "--from-url", "https://corp/.well-known/openid-configuration", "-o", "json")
	if r.Code != 0 {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
	if b := r.Last(t).Body.(map[string]any); b["providerId"] != "oidc" || b["fromUrl"] == nil {
		t.Errorf("body = %v", b)
	}
	if r.JSON(t).(map[string]any)["issuer"] != "https://corp" {
		t.Errorf("stdout = %s", r.Stdout)
	}
}
