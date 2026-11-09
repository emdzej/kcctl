package app

import (
	"context"
	"net/url"
	"regexp"
	"strings"

	"github.com/emdzej/kcctl/internal/errs"
	"github.com/emdzej/kcctl/internal/kc"
)

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func IsUUID(s string) bool { return uuidRe.MatchString(s) }

type PathFunc func(ctx context.Context, c *kc.Client) (string, error)

// RealmPath is a PathFunc for a fixed collection under the target realm.
func RealmPath(segs ...string) PathFunc {
	return func(_ context.Context, c *kc.Client) (string, error) { return c.RealmPath(segs...), nil }
}

// Lookup describes how to find a resource's id from its human name.
type Lookup struct {
	What string // "user", for messages
	// Path returns the collection path; it takes a context because nested
	// collections (client roles) must first resolve their parent's id.
	Path      PathFunc
	NameField string
	IDField   string // default "id"
	// Query narrows the server-side list; nil lists everything and filters.
	Query func(name string) url.Values
	// Search is the prefix query used while completing; defaults to Query
	// with exact=false.
	Search func(prefix string) url.Values
}

// Find resolves an id-or-name. A UUID goes straight through (no request);
// anything else must match exactly one item by NameField.
func (l Lookup) Find(ctx context.Context, c *kc.Client, ref string) (string, error) {
	if ref == "" {
		return "", errs.Usage("%s id or name is required", l.What)
	}
	if IsUUID(ref) {
		return ref, nil
	}
	m, err := l.FindItem(ctx, c, ref)
	if err != nil {
		return "", err
	}
	id, _ := m[l.idField()].(string)
	return id, nil
}

func (l Lookup) idField() string {
	if l.IDField == "" {
		return "id"
	}
	return l.IDField
}

// FindItem returns the matching record itself, which saves a GET when the
// caller needs more than the id.
func (l Lookup) FindItem(ctx context.Context, c *kc.Client, name string) (map[string]any, error) {
	var q url.Values
	if l.Query != nil {
		q = l.Query(name)
	}
	p, err := l.Path(ctx, c)
	if err != nil {
		return nil, err
	}
	v, err := c.Get(ctx, p, q)
	if err != nil {
		return nil, err
	}
	list, _ := v.([]any)
	var matches []map[string]any
	for _, x := range list {
		m, _ := x.(map[string]any)
		if s, _ := m[l.NameField].(string); s == name {
			matches = append(matches, m)
		}
	}
	switch len(matches) {
	case 0:
		return nil, errs.NotFound("no %s named '%s' in realm '%s'", l.What, name, c.Realm)
	case 1:
		return matches[0], nil
	}
	ids := make([]string, len(matches))
	for i, m := range matches {
		ids[i], _ = m[l.idField()].(string)
	}
	return nil, errs.Usage("%d %ss are named '%s'; pass the id instead (%s)", len(matches), l.What, name, strings.Join(ids, ", "))
}

// Completer lists items for completion as name (id).
func (l Lookup) Completer() Fetcher {
	return func(ctx context.Context, c *kc.Client, _ []string, toComplete string) ([]Item, error) {
		p, err := l.Path(ctx, c)
		if err != nil {
			return nil, err
		}
		v, err := c.Get(ctx, p, l.completionQuery(toComplete))
		if err != nil {
			return nil, err
		}
		return ItemsFrom(v, l.NameField, l.idField()), nil
	}
}

func (l Lookup) completionQuery(prefix string) url.Values {
	if l.Search != nil {
		return l.Search(prefix)
	}
	if l.Query == nil {
		return nil
	}
	q := l.Query(prefix)
	// Exact-match queries become prefix searches while typing.
	if q.Has("exact") {
		q.Set("exact", "false")
	}
	if !q.Has("max") {
		q.Set("max", "100")
	}
	if prefix == "" {
		for k := range q {
			if q.Get(k) == "" {
				q.Del(k)
			}
		}
	}
	return q
}
