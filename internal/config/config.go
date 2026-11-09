// Package config owns the on-disk contexts file and the precedence rules
// (flags > env > context) that turn it into a Resolved connection. It knows
// nothing about cobra so it can be used headless.
package config

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/emdzej/kcctl/internal/errs"
)

// Grant is how a context obtains tokens.
type Grant string

const (
	GrantPassword          Grant = "password"
	GrantClientCredentials Grant = "client_credentials"
	GrantDevice            Grant = "device"
	// GrantBrowser is authorization code + PKCE with a loopback redirect: the
	// way to sign in when the account has no password (IdP-brokered users).
	GrantBrowser Grant = "browser"
)

// Grants lists every grant, for validation, help and completion.
var Grants = []Grant{GrantBrowser, GrantPassword, GrantClientCredentials, GrantDevice}

func ValidGrant(g Grant) bool {
	for _, x := range Grants {
		if x == g {
			return true
		}
	}
	return false
}

func GrantNames() []string {
	out := make([]string, len(Grants))
	for i, g := range Grants {
		out[i] = string(g)
	}
	return out
}

type Context struct {
	Name string `json:"name"`
	// Server is the Keycloak base URL including any relative path
	// (e.g. https://sso.example.com or http://localhost:8080/auth).
	Server string `json:"server"`
	// AuthRealm is where the CLI signs in; Realm is the realm commands act on.
	// They differ in the common "master admin manages tenant realm" setup.
	AuthRealm string `json:"authRealm,omitempty"`
	Realm     string `json:"realm,omitempty"`
	ClientID  string `json:"clientId,omitempty"`
	Grant     Grant  `json:"grant,omitempty"`
	Username  string `json:"username,omitempty"`
	CAFile    string `json:"caFile,omitempty"`
	Insecure  bool   `json:"insecureSkipTLSVerify,omitempty"`
}

type File struct {
	CurrentContext string    `json:"currentContext,omitempty"`
	Contexts       []Context `json:"contexts"`
}

func Load() (*File, error) {
	path := ConfigFile()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &File{}, nil
	}
	if err != nil {
		return nil, errs.Wrap(errs.CodeUsage, err, "cannot read config %s: %v", path, err)
	}
	var f File
	if err := yaml.UnmarshalStrict(data, &f); err != nil {
		return nil, errs.Wrap(errs.CodeUsage, err, "invalid config %s: %v", path, err)
	}
	return &f, nil
}

func (f *File) Save() error {
	sort.SliceStable(f.Contexts, func(i, j int) bool { return f.Contexts[i].Name < f.Contexts[j].Name })
	data, err := yaml.Marshal(f)
	if err != nil {
		return err
	}
	return WriteFilePrivate(ConfigFile(), data)
}

func (f *File) Get(name string) (*Context, bool) {
	for i := range f.Contexts {
		if f.Contexts[i].Name == name {
			return &f.Contexts[i], true
		}
	}
	return nil, false
}

func (f *File) Upsert(c Context) {
	if existing, ok := f.Get(c.Name); ok {
		*existing = c
		return
	}
	f.Contexts = append(f.Contexts, c)
}

func (f *File) Delete(name string) bool {
	for i := range f.Contexts {
		if f.Contexts[i].Name == name {
			f.Contexts = append(f.Contexts[:i], f.Contexts[i+1:]...)
			if f.CurrentContext == name {
				f.CurrentContext = ""
			}
			return true
		}
	}
	return false
}

func (f *File) Names() []string {
	out := make([]string, 0, len(f.Contexts))
	for _, c := range f.Contexts {
		out = append(out, c.Name)
	}
	return out
}

// Overrides are values from flags or env; empty means "not given".
type Overrides struct {
	Context   string
	Server    string
	AuthRealm string
	Realm     string
	ClientID  string
	Token     string
	Insecure  bool
}

// Resolved is everything needed to talk to one server.
type Resolved struct {
	Context
	// Key identifies the token slot. Ad-hoc --server runs without a context
	// still get a stable slot so tokens survive between invocations.
	Key   string
	Token string
}

func EnvOverrides() Overrides {
	return Overrides{
		Context:   os.Getenv("KCCTL_CONTEXT"),
		Server:    os.Getenv("KCCTL_SERVER"),
		AuthRealm: os.Getenv("KCCTL_AUTH_REALM"),
		Realm:     os.Getenv("KCCTL_REALM"),
		ClientID:  os.Getenv("KCCTL_CLIENT_ID"),
		Token:     os.Getenv("KCCTL_TOKEN"),
	}
}

// Resolve applies flags > env > context > defaults, one field at a time.
func Resolve(f *File, flags, env Overrides) (*Resolved, error) {
	name := first(flags.Context, env.Context, f.CurrentContext)
	var base Context
	if name != "" {
		c, ok := f.Get(name)
		if !ok {
			// An explicit --server makes a missing context harmless only when
			// the context came from the file, not from the user.
			if flags.Context != "" || env.Context != "" || first(flags.Server, env.Server) == "" {
				return nil, errs.Usage("context %q does not exist", name).
					WithHint(contextHint(f))
			}
		} else {
			base = *c
		}
	}
	r := &Resolved{Context: base}
	r.Server = strings.TrimRight(first(flags.Server, env.Server, base.Server), "/")
	r.AuthRealm = first(flags.AuthRealm, env.AuthRealm, base.AuthRealm, "master")
	r.Realm = first(flags.Realm, env.Realm, base.Realm, r.AuthRealm)
	r.ClientID = first(flags.ClientID, env.ClientID, base.ClientID, "admin-cli")
	r.Token = first(flags.Token, env.Token)
	r.Insecure = flags.Insecure || base.Insecure
	if r.Grant == "" {
		r.Grant = GrantPassword
	}
	// A server given ad hoc gets its own token slot, even if a context is active.
	if r.Name != "" && r.Server == strings.TrimRight(base.Server, "/") {
		r.Key = r.Name
	} else {
		r.Key = fmt.Sprintf("%s#%s", r.Server, r.AuthRealm)
	}
	if r.Server == "" {
		return r, errs.Usage("no server configured").
			WithHint("Create a context with 'kcctl config set-context <name> --server <url>' or pass --server.")
	}
	return r, nil
}

func contextHint(f *File) string {
	if len(f.Contexts) == 0 {
		return "Create one with 'kcctl config set-context <name> --server <url>'."
	}
	return "Known contexts: " + strings.Join(f.Names(), ", ") + "."
}

func first(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
