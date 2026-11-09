package extension

import (
	"os"
	"strings"
	"testing"
)

func TestExampleManifestIsValid(t *testing.T) {
	data, err := os.ReadFile("../../docs/examples/api-keys.yaml")
	if err != nil {
		t.Fatal(err)
	}
	m, err := Parse(data, "api-keys.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if m.Name != "api-keys" || m.Commands[0].Verbs[0].Method != "GET" {
		t.Fatalf("unexpected manifest: %+v", m)
	}
}

func TestValidateRejects(t *testing.T) {
	cases := map[string]string{
		"url base":        `{apiVersion: kcctl/v1, name: x, commands: [{name: g, base: "https://evil", verbs: [{name: v}]}]}`,
		"scheme-relative": `{apiVersion: kcctl/v1, name: x, commands: [{name: g, base: "//evil/x", verbs: [{name: v}]}]}`,
		"non-admin abs":   `{apiVersion: kcctl/v1, name: x, commands: [{name: g, base: "/realms/x", verbs: [{name: v}]}]}`,
		"dotdot":          `{apiVersion: kcctl/v1, name: x, commands: [{name: g, verbs: [{name: v, path: "../users"}]}]}`,
		"query in path":   `{apiVersion: kcctl/v1, name: x, commands: [{name: g, verbs: [{name: v, path: "a?b=1"}]}]}`,
		"unbound place":   `{apiVersion: kcctl/v1, name: x, commands: [{name: g, verbs: [{name: v, path: "{id}"}]}]}`,
		"reserved flag":   `{apiVersion: kcctl/v1, name: x, commands: [{name: g, verbs: [{name: v, query: [{flag: realm}]}]}]}`,
		"secret file":     `{apiVersion: kcctl/v1, name: x, commands: [{name: g, verbs: [{name: v, query: [{flag: pw-file}], fields: [{flag: pw, kind: secret}]}]}]}`,
		"bad version":     `{apiVersion: kcctl/v2, name: x, commands: [{name: g, verbs: [{name: v}]}]}`,
		"unknown field":   `{apiVersion: kcctl/v1, name: x, bogus: 1, commands: [{name: g, verbs: [{name: v}]}]}`,
		"unknown lookup":  `{apiVersion: kcctl/v1, name: x, commands: [{name: g, verbs: [{name: v, args: [{name: a, lookup: role}]}]}]}`,
		"self w/o spec":   `{apiVersion: kcctl/v1, name: x, commands: [{name: g, verbs: [{name: v, args: [{name: a, complete: self}]}]}]}`,
		"min version":     `{apiVersion: kcctl/v1, name: x, requires: {minVersion: soon}, commands: [{name: g, verbs: [{name: v}]}]}`,
	}
	for name, doc := range cases {
		if _, err := Parse([]byte(doc), name); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestRealmPlaceholderAndAbsolutePaths(t *testing.T) {
	doc := `{apiVersion: kcctl/v1, name: x, commands: [{name: g, base: "/admin/realms/{realm}/x", verbs: [{name: v}]}]}`
	if _, err := Parse([]byte(doc), "t"); err != nil {
		t.Fatal(err)
	}
}

func TestFill(t *testing.T) {
	got := Fill("users/{user}/keys/{other}", func(n string) (string, bool) {
		if n == "user" {
			return "u1", true
		}
		return "", false
	})
	if got != "users/u1/keys/{other}" {
		t.Fatal(got)
	}
}

func TestInstallLoadRemove(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir+"/config")
	t.Setenv("XDG_STATE_HOME", dir+"/state")
	doc := []byte(`{apiVersion: kcctl/v1, name: demo, commands: [{name: g, verbs: [{name: v}]}]}`)
	if _, err := Install(doc, "t"); err != nil {
		t.Fatal(err)
	}
	ms, problems := Load("https://sso.example.com")
	if len(problems) > 0 || len(ms) != 1 || ms[0].Source != SourceInstalled {
		t.Fatalf("load: %v %v", ms, problems)
	}
	if err := Remove("demo"); err != nil {
		t.Fatal(err)
	}
	if err := Remove("demo"); err == nil || !strings.Contains(err.Error(), "no installed") {
		t.Fatalf("second remove: %v", err)
	}
}

func TestPluginName(t *testing.T) {
	if n, ok := pluginName("kcctl-foo"); !ok || n != "foo" {
		t.Fatal(n, ok)
	}
	if _, ok := pluginName("kcctl-Foo_bar"); ok {
		t.Fatal("accepted bad name")
	}
	if _, ok := pluginName("kubectl-foo"); ok {
		t.Fatal("accepted other prefix")
	}
}
