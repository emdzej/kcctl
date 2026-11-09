package config

import (
	"testing"

	"github.com/emdzej/keycloak-cli/internal/errs"
)

func TestResolvePrecedence(t *testing.T) {
	f := &File{CurrentContext: "dev", Contexts: []Context{
		{Name: "dev", Server: "http://dev/", Realm: "acme"},
		{Name: "prod", Server: "https://prod", AuthRealm: "admin"},
	}}
	r, err := Resolve(f, Overrides{}, Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if r.Server != "http://dev" || r.Realm != "acme" || r.AuthRealm != "master" || r.ClientID != "admin-cli" || r.Key != "dev" {
		t.Errorf("defaults: %+v", r)
	}
	r, _ = Resolve(f, Overrides{Realm: "flag"}, Overrides{Context: "prod", Realm: "env"})
	if r.Name != "prod" || r.Realm != "flag" || r.AuthRealm != "admin" {
		t.Errorf("flags > env > context: %+v", r)
	}
	// Target realm falls back to the auth realm, not to master.
	if r, _ = Resolve(f, Overrides{Context: "prod"}, Overrides{}); r.Realm != "admin" {
		t.Errorf("realm fallback = %q", r.Realm)
	}
	// An ad-hoc server gets its own token slot.
	if r, _ = Resolve(f, Overrides{Server: "http://other"}, Overrides{}); r.Key == "dev" {
		t.Errorf("ad-hoc server reused context slot")
	}
}

func TestResolveErrors(t *testing.T) {
	f := &File{}
	if _, err := Resolve(f, Overrides{Context: "nope"}, Overrides{}); errs.As(err).Code != errs.CodeUsage {
		t.Errorf("unknown context: %v", err)
	}
	if _, err := Resolve(f, Overrides{}, Overrides{}); err == nil || errs.As(err).Hint == "" {
		t.Errorf("no server should fail with a hint: %v", err)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("KCCTL_CONFIG", "")
	f := &File{CurrentContext: "a"}
	f.Upsert(Context{Name: "a", Server: "http://a", Grant: GrantDevice})
	if err := f.Save(); err != nil {
		t.Fatal(err)
	}
	g, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c, ok := g.Get("a"); !ok || c.Grant != GrantDevice || g.CurrentContext != "a" {
		t.Errorf("loaded %+v", g)
	}
}
