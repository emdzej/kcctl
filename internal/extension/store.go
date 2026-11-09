package extension

import (
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"

	"github.com/emdzej/kcctl/internal/config"
	"github.com/emdzej/kcctl/internal/errs"
	"github.com/emdzej/kcctl/internal/kc"
)

// AdminExtensionSPI is the Keycloak SPI whose providers add admin REST
// endpoints under /admin/realms/{realm}/{provider-id}; it is where kcctl
// looks for discoverable extensions.
const AdminExtensionSPI = "admin-realm-restapi-extension"

// ManifestPath is the well-known sub-path an admin REST extension serves its
// kcctl manifest at: GET /admin/realms/{realm}/{provider-id}/kcctl-extension.
const ManifestPath = "kcctl-extension"

// InstalledDir holds manifests the user installed; they apply to every
// server and win over discovered ones with the same name.
func InstalledDir() string { return filepath.Join(config.ConfigDir(), "extensions") }

// DiscoveredDir holds manifests fetched from one server.
func DiscoveredDir(server string) string {
	return filepath.Join(config.StateDir(), "extensions", serverKey(server))
}

var unsafeRe = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

func serverKey(server string) string {
	u, err := url.Parse(server)
	if err != nil || u.Host == "" {
		return unsafeRe.ReplaceAllString(server, "_")
	}
	return unsafeRe.ReplaceAllString(u.Host+u.Path, "_")
}

// Load returns the manifests for server: installed ones plus those
// discovered on that server. Broken files are reported, not fatal, so one
// bad manifest never stops kcctl from starting.
func Load(server string) ([]*Manifest, []error) {
	byName := map[string]*Manifest{}
	var problems []error
	if server != "" {
		ms, errs := loadDir(DiscoveredDir(server), SourceDiscovered)
		problems = append(problems, errs...)
		for _, m := range ms {
			byName[m.Name] = m
		}
	}
	ms, errs := loadDir(InstalledDir(), SourceInstalled)
	problems = append(problems, errs...)
	for _, m := range ms {
		byName[m.Name] = m
	}
	out := make([]*Manifest, 0, len(byName))
	for _, m := range byName {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, problems
}

func loadDir(dir string, src Source) ([]*Manifest, []error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, []error{err}
	}
	var out []*Manifest
	var problems []error
	for _, e := range entries {
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if e.IsDir() || (ext != ".yaml" && ext != ".yml" && ext != ".json") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(p)
		if err != nil {
			problems = append(problems, err)
			continue
		}
		m, err := Parse(data, p)
		if err != nil {
			problems = append(problems, err)
			continue
		}
		m.Source = src
		out = append(out, m)
	}
	return out, problems
}

// Install validates data and stores it as <name>.yaml, replacing an
// installed manifest of the same name.
func Install(data []byte, origin string) (*Manifest, error) {
	m, err := Parse(data, origin)
	if err != nil {
		return nil, err
	}
	if err := config.WriteFilePrivate(filepath.Join(InstalledDir(), m.Name+".yaml"), data); err != nil {
		return nil, err
	}
	m.Source = SourceInstalled
	return m, nil
}

// Remove deletes an installed manifest; discovered ones come back on the
// next discovery, so they are removed per server with RemoveDiscovered.
func Remove(name string) error {
	for _, ext := range []string{".yaml", ".yml", ".json"} {
		p := filepath.Join(InstalledDir(), name+ext)
		if err := os.Remove(p); err == nil {
			return nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return errs.NotFound("no installed extension named %q", name)
}

// Discovered is what discovery found for one admin extension provider.
type Discovered struct {
	Provider string
	Manifest *Manifest
	// Err is set when the provider serves something that isn't a valid
	// manifest; a provider without one simply has neither.
	Err error
}

// Discover asks every admin REST extension on the server for its manifest
// and replaces the server's discovered set with what it finds.
func Discover(ctx context.Context, c *kc.Client, providers []string) ([]Discovered, error) {
	dir := DiscoveredDir(c.Server)
	var found []Discovered
	keep := map[string]bool{}
	for _, id := range providers {
		resp, err := c.Do(ctx, kc.Request{
			Method: "GET",
			Path:   c.RealmPath(id, ManifestPath),
			Accept: "application/yaml, application/json;q=0.9",
		})
		if err != nil {
			e := errs.As(err)
			// No manifest is the normal case for most extensions.
			if e.Code == errs.CodeNotFound || (e.Request != nil && (e.Request.Status == 405 || e.Request.Status == 406)) {
				continue
			}
			if e.Code == errs.CodeAuth || e.Code == errs.CodeNetwork {
				return found, err
			}
			found = append(found, Discovered{Provider: id, Err: err})
			continue
		}
		m, err := Parse(resp.Body, c.Server+c.RealmPath(id, ManifestPath))
		if err != nil {
			found = append(found, Discovered{Provider: id, Err: err})
			continue
		}
		m.Source = SourceDiscovered
		if err := config.WriteFilePrivate(filepath.Join(dir, m.Name+".yaml"), resp.Body); err != nil {
			return found, err
		}
		keep[m.Name+".yaml"] = true
		found = append(found, Discovered{Provider: id, Manifest: m})
	}
	// Forget manifests of providers the server no longer has.
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if !keep[e.Name()] {
				_ = os.Remove(filepath.Join(dir, e.Name()))
			}
		}
	}
	return found, nil
}

// Plugin is an executable named kcctl-<name> on PATH.
type Plugin struct {
	Name string
	Path string
}

var pluginNameRe = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// Plugins scans PATH like a shell would: the first match for a name wins.
func Plugins() []Plugin {
	seen := map[string]bool{}
	var out []Plugin
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" {
			continue
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			name, ok := pluginName(e.Name())
			if !ok || seen[name] || e.IsDir() {
				continue
			}
			p := filepath.Join(dir, e.Name())
			if !isExecutable(p) {
				continue
			}
			seen[name] = true
			out = append(out, Plugin{Name: name, Path: p})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func pluginName(file string) (string, bool) {
	if !strings.HasPrefix(file, "kcctl-") {
		return "", false
	}
	name := strings.TrimPrefix(file, "kcctl-")
	if runtime.GOOS == "windows" {
		ext := strings.ToLower(filepath.Ext(name))
		if ext != ".exe" && ext != ".bat" && ext != ".cmd" {
			return "", false
		}
		name = strings.TrimSuffix(name, filepath.Ext(name))
	}
	return name, pluginNameRe.MatchString(name)
}

func isExecutable(p string) bool {
	fi, err := os.Stat(p)
	if err != nil || fi.IsDir() {
		return false
	}
	if runtime.GOOS == "windows" {
		return true
	}
	return fi.Mode()&0o111 != 0
}
