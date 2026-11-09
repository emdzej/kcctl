package kc

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/emdzej/kcctl/internal/compat"
	"github.com/emdzej/kcctl/internal/config"
	"github.com/emdzej/kcctl/internal/errs"
)

// profileTTL bounds how long a cached version is trusted; upgrades are rare
// but a stale version would pick the wrong endpoint shape.
const profileTTL = 6 * time.Hour

type cachedProfile struct {
	Version   string              `json:"version"`
	Features  map[string]bool     `json:"features,omitempty"`
	Providers map[string][]string `json:"providers,omitempty"`
	FetchedAt time.Time           `json:"fetchedAt"`
}

func cachePath() string { return filepath.Join(config.StateDir(), "serverinfo.json") }

func readCache() map[string]cachedProfile {
	m := map[string]cachedProfile{}
	data, err := os.ReadFile(cachePath())
	if err == nil {
		_ = json.Unmarshal(data, &m)
	}
	return m
}

// Profile returns the server's version and features, cached per server URL.
func (c *Client) Profile(ctx context.Context, refresh bool) (*compat.Profile, error) {
	cache := readCache()
	if cp, ok := cache[c.Server]; ok && !refresh && time.Since(cp.FetchedAt) < profileTTL {
		if v, err := compat.Parse(cp.Version); err == nil {
			return &compat.Profile{Version: v, Features: cp.Features, Providers: cp.Providers}, nil
		}
	}
	info, err := c.ServerInfo(ctx)
	if err != nil {
		return nil, err
	}
	p, err := ProfileFrom(info)
	if err != nil {
		return nil, err
	}
	cache[c.Server] = cachedProfile{Version: p.Version.Raw, Features: p.Features, Providers: p.Providers, FetchedAt: time.Now()}
	if data, err := json.MarshalIndent(cache, "", "  "); err == nil {
		_ = config.WriteFilePrivate(cachePath(), data)
	}
	return p, nil
}

func (c *Client) ServerInfo(ctx context.Context) (map[string]any, error) {
	v, err := c.Get(ctx, "/admin/serverinfo", nil)
	if err != nil {
		return nil, err
	}
	m, _ := v.(map[string]any)
	if m == nil {
		return nil, errs.New(errs.CodeServer, "unexpected /admin/serverinfo response")
	}
	return m, nil
}

func ProfileFrom(info map[string]any) (*compat.Profile, error) {
	sys, _ := info["systemInfo"].(map[string]any)
	ver, _ := sys["version"].(string)
	if ver == "" {
		return nil, errs.New(errs.CodeServer, "server did not report its version")
	}
	v, err := compat.Parse(ver)
	if err != nil {
		return nil, errs.New(errs.CodeServer, "%v", err)
	}
	p := &compat.Profile{Version: v}
	if feats, ok := info["features"].([]any); ok && len(feats) > 0 {
		p.Features = map[string]bool{}
		for _, f := range feats {
			fm, _ := f.(map[string]any)
			name, _ := fm["name"].(string)
			enabled, _ := fm["enabled"].(bool)
			if name != "" {
				p.Features[name] = enabled
			}
		}
	}
	if provs, ok := info["providers"].(map[string]any); ok {
		p.Providers = map[string][]string{}
		for spi, x := range provs {
			m, _ := x.(map[string]any)
			list, _ := m["providers"].(map[string]any)
			ids := make([]string, 0, len(list))
			for id := range list {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			p.Providers[spi] = ids
		}
	}
	return p, nil
}

// CachedVersion returns the last known version for server without any
// network call; the interactive status line uses it on every prompt.
func CachedVersion(server string) string {
	return readCache()[strings.TrimRight(server, "/")].Version
}
