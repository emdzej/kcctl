package componentcmd

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/emdzej/kcctl/internal/app"
	"github.com/emdzej/kcctl/internal/apptest"
)

var build = []func(*app.App) *cobra.Command{New, UserStorage}

const ldapID = "0c7a1b2c-3d4e-4f50-8a6b-7c8d9e0f1a2b"

func TestCreateWithBindCredentialFromStdin(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{
		"POST /admin/realms/test/components": {Status: 201, Header: map[string]string{"Location": "{server}/admin/realms/test/components/" + ldapID}},
	}, apptest.Options{Stdin: "bindpw\n"}, "user-storage", "create", "corp", "--provider-id", "ldap",
		"--config", "connectionUrl=ldaps://ldap.corp", "--bind-credential-file", "-")
	if r.Code != 0 {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
	b := r.Last(t).Body.(map[string]any)
	cfg := b["config"].(map[string]any)
	if b["providerType"] != userStorageType || b["name"] != "corp" || b["providerId"] != "ldap" {
		t.Errorf("body = %v", b)
	}
	if cfg["bindCredential"].([]any)[0] != "bindpw" || cfg["connectionUrl"].([]any)[0] != "ldaps://ldap.corp" {
		t.Errorf("config = %v", cfg)
	}
	if !strings.Contains(r.Stderr, ldapID) {
		t.Errorf("stderr = %q", r.Stderr)
	}
}

func TestConfigRejectsBindCredential(t *testing.T) {
	for _, args := range [][]string{
		{"component", "create", "x", "--provider-id", "ldap", "--provider-type", userStorageType, "--config", "bindCredential=pw"},
		{"component", "create", "x", "--provider-id", "ldap", "--provider-type", userStorageType, "-s", "config.bindCredential=[\"pw\"]"},
	} {
		r := apptest.Run(t, build, apptest.Replies{}, apptest.Options{}, args...)
		if r.Code != 1 || !strings.Contains(r.Stderr, "looks like a secret") {
			t.Errorf("%v: exit %d: %s", args, r.Code, r.Stderr)
		}
	}
}

func TestSyncByNameUsesAction(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{
		"GET /admin/realms/test/components":                        {Body: []any{map[string]any{"id": ldapID, "name": "corp"}}},
		"POST /admin/realms/test/user-storage/" + ldapID + "/sync": {Body: map[string]any{"added": 3, "updated": 1, "removed": 0, "failed": 0}},
	}, apptest.Options{}, "user-storage", "sync", "corp", "--full")
	if r.Code != 0 {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
	if q := r.Sent[0].Query; !strings.Contains(q, "name=corp") || !strings.Contains(q, "type=org.keycloak.storage.UserStorageProvider") {
		t.Errorf("lookup query = %q", q)
	}
	if q := r.Last(t).Query; q != "action=triggerFullSync" {
		t.Errorf("sync query = %q", q)
	}
	if !strings.Contains(r.Stderr, "3 added") {
		t.Errorf("stderr = %q", r.Stderr)
	}
}

func TestRemoveImportedNeedsYes(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{}, apptest.Options{}, "user-storage", "remove-imported", ldapID)
	if r.Code != 1 || !strings.Contains(r.Stderr, "--yes") {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
	r = apptest.Run(t, build, apptest.Replies{
		"POST /admin/realms/test/user-storage/" + ldapID + "/remove-imported-users": {},
	}, apptest.Options{}, "user-storage", "remove-imported", ldapID, "--yes")
	if r.Code != 0 {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
}

func TestTestConnectionUsesStoredCredential(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{
		"GET /admin/realms/test/components/" + ldapID: {Body: map[string]any{"id": ldapID, "providerId": "ldap",
			"config": map[string]any{"connectionUrl": []any{"ldaps://ldap.corp"}, "bindDn": []any{"cn=kc"}, "bindCredential": []any{"**********"}}}},
		"POST /admin/realms/test/testLDAPConnection": {},
	}, apptest.Options{}, "user-storage", "test-connection", ldapID, "--auth")
	if r.Code != 0 {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
	b := r.Last(t).Body.(map[string]any)
	if b["action"] != "testAuthentication" || b["componentId"] != ldapID || b["bindCredential"] != "**********" || b["connectionUrl"] != "ldaps://ldap.corp" {
		t.Errorf("body = %v", b)
	}
}

func TestSubTypes(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{
		"GET /admin/realms/test/components/" + ldapID + "/sub-component-types": {Body: []any{map[string]any{"id": "user-attribute-ldap-mapper"}}},
	}, apptest.Options{}, "component", "sub-types", ldapID, "--type", "org.keycloak.storage.ldap.mappers.LDAPStorageMapper")
	if r.Code != 0 || !strings.Contains(r.Last(t).Query, "type=org.keycloak.storage.ldap.mappers.LDAPStorageMapper") {
		t.Fatalf("exit %d: %s %v", r.Code, r.Stderr, r.Sent)
	}
}

func TestUpdateOnlyBindCredential(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{
		"GET /admin/realms/test/components/" + ldapID: {Body: map[string]any{"id": ldapID, "name": "corp",
			"config": map[string]any{"bindCredential": []any{"**********"}, "bindDn": []any{"cn=kc"}}}},
		"PUT /admin/realms/test/components/" + ldapID: {},
	}, apptest.Options{Stdin: "newpw"}, "user-storage", "update", ldapID, "--bind-credential-file", "-")
	if r.Code != 0 {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
	cfg := r.Last(t).Body.(map[string]any)["config"].(map[string]any)
	if cfg["bindCredential"].([]any)[0] != "newpw" || cfg["bindDn"].([]any)[0] != "cn=kc" {
		t.Errorf("config = %v", cfg)
	}
}
