// Package extension loads kcctl extension manifests: declarative
// descriptions of commands that an SPI adds to the Keycloak admin API. A
// manifest is data, not code, so the commands it describes get the same
// behaviour as built-in ones (name lookups, completion, output formats,
// confirmation) without kcctl knowing the SPI.
package extension

import (
	"fmt"
	"regexp"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/emdzej/kcctl/internal/compat"
)

// APIVersion is the manifest format this kcctl understands.
const APIVersion = "kcctl/v1"

// Manifest is one extension. Field names are the wire format; keep them
// stable and add, don't rename.
type Manifest struct {
	APIVersion  string   `json:"apiVersion"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Requires    Requires `json:"requires,omitempty"`
	Commands    []Group  `json:"commands"`

	// Source and Origin are set by the loader, never read from the file.
	Source Source `json:"-"`
	Origin string `json:"-"`
}

type Source string

const (
	SourceInstalled  Source = "installed"
	SourceDiscovered Source = "discovered"
)

// Requires is checked when a command runs, against the cached server
// profile; commands stay registered so startup needs no network.
type Requires struct {
	// Providers are "spi/provider-id" pairs that must be installed, e.g.
	// "admin-realm-restapi-extension/api-keys".
	Providers []string `json:"providers,omitempty"`
	// Features are Keycloak feature flags that must be enabled.
	Features []string `json:"features,omitempty"`
	// MinVersion is the oldest Keycloak release the extension supports.
	MinVersion string `json:"minVersion,omitempty"`
}

// Group is a noun: `kcctl <group> <verb>`. Groups nest for sub-nouns.
type Group struct {
	Name    string   `json:"name"`
	Aliases []string `json:"aliases,omitempty"`
	Short   string   `json:"short,omitempty"`
	// Base is the path prefix for the group's verbs, relative to
	// /admin/realms/{realm}/, or absolute when it starts with "/admin/".
	Base     string    `json:"base,omitempty"`
	Columns  []Column  `json:"columns,omitempty"`
	Complete *Complete `json:"complete,omitempty"`
	Verbs    []Verb    `json:"verbs,omitempty"`
	Groups   []Group   `json:"groups,omitempty"`
}

type Column struct {
	Header string `json:"header"`
	Path   string `json:"path"`
}

// Complete says how to list the group's own items, for `complete: self`.
type Complete struct {
	Path  string            `json:"path,omitempty"` // relative to the group's base
	Query map[string]string `json:"query,omitempty"`
	Value string            `json:"value"`          // JSONPath to the value, e.g. ".id"
	Desc  string            `json:"desc,omitempty"` // JSONPath shown beside it, e.g. ".name"
}

// Verb is one command.
type Verb struct {
	Name    string   `json:"name"`
	Aliases []string `json:"aliases,omitempty"`
	Short   string   `json:"short,omitempty"`
	Long    string   `json:"long,omitempty"`
	Example string   `json:"example,omitempty"`
	Method  string   `json:"method,omitempty"` // default GET
	// Path is relative to the group's base; {name} placeholders are filled
	// from args (resolved and URL-escaped), and {realm} is the target realm.
	Path   string  `json:"path,omitempty"`
	Args   []Arg   `json:"args,omitempty"`
	Query  []Param `json:"query,omitempty"`
	Fields []Field `json:"fields,omitempty"`
	// Body enables -f/--file and -s/--set for a JSON request body.
	Body bool `json:"body,omitempty"`
	// Paged adds --first/--max/--all using Keycloak's first/max convention.
	Paged   bool     `json:"paged,omitempty"`
	Columns []Column `json:"columns,omitempty"`
	// Print controls whether the response body is shown. Default: true for
	// GET, false otherwise (a success message is printed instead).
	Print *bool `json:"print,omitempty"`
	// Secret names a response field the server returns only once (a
	// generated key). The response is printed and a warning says so.
	Secret string `json:"secret,omitempty"`
	// Message is the success line for non-GET verbs; {name} placeholders
	// come from args, then the request body, then the response.
	Message string `json:"message,omitempty"`
	// Confirm makes the verb ask first (or need --yes); use it when the
	// verb affects more than the one thing named on the line.
	Confirm string `json:"confirm,omitempty"`
}

// Arg is a positional argument.
type Arg struct {
	Name  string `json:"name"`
	Usage string `json:"usage,omitempty"`
	// Lookup resolves a human name to an id and completes it; see Lookups.
	Lookup string `json:"lookup,omitempty"`
	// Complete only completes (no resolution): a lookup kind, "self", or
	// nothing. Values completes from a fixed list.
	Complete string   `json:"complete,omitempty"`
	Values   []string `json:"values,omitempty"`
	Optional bool     `json:"optional,omitempty"`
}

// Param is a query parameter set from a flag.
type Param struct {
	Flag     string   `json:"flag"`
	Param    string   `json:"param,omitempty"` // default: the flag name
	Usage    string   `json:"usage,omitempty"`
	Kind     string   `json:"kind,omitempty"` // string (default), bool, int, strings
	Lookup   string   `json:"lookup,omitempty"`
	Complete string   `json:"complete,omitempty"`
	Values   []string `json:"values,omitempty"`
}

// Field is a request-body field set from a flag.
type Field struct {
	Flag  string `json:"flag"`
	Path  string `json:"path,omitempty"` // dotted body path; default: the flag name
	Usage string `json:"usage,omitempty"`
	// Kind: string (default), bool, int, strings, kv (map of string lists),
	// kvstring (map of strings), secret (read from --<flag>-file, stdin or a
	// hidden prompt; never a flag value).
	Kind     string   `json:"kind,omitempty"`
	Required bool     `json:"required,omitempty"`
	Lookup   string   `json:"lookup,omitempty"`
	Complete string   `json:"complete,omitempty"`
	Values   []string `json:"values,omitempty"`
}

// Lookups are the resolvers a manifest may name. Each resolves a name to the
// id the API expects and provides completion.
var Lookups = []string{"user", "client", "group", "client-scope", "organization"}

// Completers can complete but don't resolve (the API takes the name itself).
var Completers = []string{"user", "client", "group", "client-scope", "organization", "realm", "realm-role", "self"}

var (
	nameRe   = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	placeRe  = regexp.MustCompile(`\{([a-zA-Z0-9_-]+)\}`)
	methods  = map[string]bool{"GET": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true}
	kinds    = map[string]bool{"": true, "string": true, "bool": true, "int": true, "strings": true}
	fieldKnd = map[string]bool{"": true, "string": true, "bool": true, "int": true, "strings": true, "kv": true, "kvstring": true, "secret": true}
)

// Parse reads and validates a manifest (YAML or JSON).
func Parse(data []byte, origin string) (*Manifest, error) {
	var m Manifest
	if err := yaml.UnmarshalStrict(data, &m); err != nil {
		return nil, fmt.Errorf("%s: %v", origin, err)
	}
	m.Origin = origin
	if err := m.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %v", origin, err)
	}
	return &m, nil
}

// Validate rejects anything kcctl couldn't build safely. Path checks matter:
// a manifest may come from a server, and must not be able to send the
// user's token anywhere but that server's admin API.
func (m *Manifest) Validate() error {
	if m.APIVersion != APIVersion {
		return fmt.Errorf("apiVersion must be %q (got %q)", APIVersion, m.APIVersion)
	}
	if !nameRe.MatchString(m.Name) {
		return fmt.Errorf("name %q must be lowercase letters, digits and dashes", m.Name)
	}
	if len(m.Commands) == 0 {
		return fmt.Errorf("no commands")
	}
	if v := m.Requires.MinVersion; v != "" {
		if _, err := compat.Parse(v); err != nil {
			return fmt.Errorf("requires.minVersion: %v", err)
		}
	}
	for _, p := range m.Requires.Providers {
		if spi, id, ok := strings.Cut(p, "/"); !ok || spi == "" || id == "" {
			return fmt.Errorf("requires.providers entry %q must be spi/provider-id", p)
		}
	}
	for i := range m.Commands {
		if err := validateGroup(&m.Commands[i], m.Commands[i].Name); err != nil {
			return err
		}
	}
	return nil
}

func validateGroup(g *Group, where string) error {
	if !nameRe.MatchString(g.Name) {
		return fmt.Errorf("%s: name %q must be lowercase letters, digits and dashes", where, g.Name)
	}
	if err := checkPath(g.Base); err != nil {
		return fmt.Errorf("%s: base: %v", where, err)
	}
	if len(g.Verbs) == 0 && len(g.Groups) == 0 {
		return fmt.Errorf("%s: has no verbs or groups", where)
	}
	if g.Complete != nil && g.Complete.Value == "" {
		return fmt.Errorf("%s: complete.value is required", where)
	}
	seen := map[string]bool{}
	for i := range g.Verbs {
		v := &g.Verbs[i]
		at := where + " " + v.Name
		if !nameRe.MatchString(v.Name) {
			return fmt.Errorf("%s: verb name must be lowercase letters, digits and dashes", at)
		}
		if seen[v.Name] {
			return fmt.Errorf("%s: duplicate verb", at)
		}
		seen[v.Name] = true
		if v.Method == "" {
			v.Method = "GET"
		}
		v.Method = strings.ToUpper(v.Method)
		if !methods[v.Method] {
			return fmt.Errorf("%s: method %q not supported", at, v.Method)
		}
		if err := checkPath(v.Path); err != nil {
			return fmt.Errorf("%s: path: %v", at, err)
		}
		args := map[string]bool{}
		optional := false
		for _, a := range v.Args {
			if !nameRe.MatchString(a.Name) {
				return fmt.Errorf("%s: arg name %q must be lowercase letters, digits and dashes", at, a.Name)
			}
			if optional && !a.Optional {
				return fmt.Errorf("%s: required arg %q follows an optional one", at, a.Name)
			}
			optional = optional || a.Optional
			if err := checkRef(a.Lookup, a.Complete, g); err != nil {
				return fmt.Errorf("%s: arg %s: %v", at, a.Name, err)
			}
			args[a.Name] = true
		}
		for _, ph := range placeRe.FindAllStringSubmatch(g.Base+"/"+v.Path, -1) {
			// {realm} is always the target realm.
			if !args[ph[1]] && ph[1] != "realm" {
				return fmt.Errorf("%s: path placeholder {%s} has no matching arg", at, ph[1])
			}
		}
		flags := map[string]bool{}
		for _, q := range v.Query {
			if err := checkFlag(q.Flag, flags); err != nil {
				return fmt.Errorf("%s: %v", at, err)
			}
			if !kinds[q.Kind] {
				return fmt.Errorf("%s: query %s: kind %q not supported", at, q.Flag, q.Kind)
			}
			if err := checkRef(q.Lookup, q.Complete, g); err != nil {
				return fmt.Errorf("%s: query %s: %v", at, q.Flag, err)
			}
		}
		for _, f := range v.Fields {
			flag := f.Flag
			if f.Kind == "secret" {
				flag += "-file"
			}
			if err := checkFlag(flag, flags); err != nil {
				return fmt.Errorf("%s: %v", at, err)
			}
			if !fieldKnd[f.Kind] {
				return fmt.Errorf("%s: field %s: kind %q not supported", at, f.Flag, f.Kind)
			}
			if err := checkRef(f.Lookup, f.Complete, g); err != nil {
				return fmt.Errorf("%s: field %s: %v", at, f.Flag, err)
			}
		}
		if v.Paged && (flags["first"] || flags["max"] || flags["all"]) {
			return fmt.Errorf("%s: paged verbs own --first, --max and --all", at)
		}
	}
	for i := range g.Groups {
		if err := validateGroup(&g.Groups[i], where+" "+g.Groups[i].Name); err != nil {
			return err
		}
	}
	return nil
}

// reservedFlags are kcctl's global flags and the ones it adds itself.
var reservedFlags = map[string]bool{
	"context": true, "server": true, "realm": true, "auth-realm": true, "client-id": true,
	"token": true, "output": true, "color": true, "no-color": true, "quiet": true,
	"no-input": true, "verbose": true, "insecure": true, "help": true,
	"file": true, "set": true, "yes": true,
}

func checkFlag(name string, seen map[string]bool) error {
	if !nameRe.MatchString(name) {
		return fmt.Errorf("flag %q must be lowercase letters, digits and dashes", name)
	}
	if reservedFlags[name] {
		return fmt.Errorf("flag --%s is reserved by kcctl", name)
	}
	if seen[name] {
		return fmt.Errorf("duplicate flag --%s", name)
	}
	seen[name] = true
	return nil
}

func checkRef(lookup, complete string, g *Group) error {
	if lookup != "" && !contains(Lookups, lookup) {
		return fmt.Errorf("unknown lookup %q (known: %s)", lookup, strings.Join(Lookups, ", "))
	}
	if complete != "" && !contains(Completers, complete) {
		return fmt.Errorf("unknown completer %q (known: %s)", complete, strings.Join(Completers, ", "))
	}
	if complete == "self" && g.Complete == nil {
		return fmt.Errorf("complete: self needs the group's complete section")
	}
	return nil
}

// checkPath allows only relative paths or /admin/ paths on the same server.
func checkPath(p string) error {
	switch {
	case strings.Contains(p, "://") || strings.HasPrefix(p, "//"):
		return fmt.Errorf("%q must be a path on the Keycloak server, not a URL", p)
	case strings.HasPrefix(p, "/") && !strings.HasPrefix(p, "/admin/"):
		return fmt.Errorf("absolute path %q must start with /admin/", p)
	case strings.Contains(p, ".."):
		return fmt.Errorf("%q must not contain '..'", p)
	case strings.ContainsAny(p, "?#"):
		return fmt.Errorf("%q must not carry a query or fragment; use query params", p)
	}
	return nil
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// Placeholders returns the {name}s in s, in order.
func Placeholders(s string) []string {
	var out []string
	for _, m := range placeRe.FindAllStringSubmatch(s, -1) {
		out = append(out, m[1])
	}
	return out
}

// Fill replaces {name} in s using lookup; unknown names are left as-is.
func Fill(s string, lookup func(string) (string, bool)) string {
	return placeRe.ReplaceAllStringFunc(s, func(m string) string {
		if v, ok := lookup(m[1 : len(m)-1]); ok {
			return v
		}
		return m
	})
}
