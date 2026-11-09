package app

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	"github.com/emdzej/kcctl/internal/errs"
	"github.com/emdzej/kcctl/internal/kc"
)

// Shared lookups: several command packages reference the same resources
// (a user's groups, a group's client roles), so they resolve them one way.

var Users = Lookup{
	What: "user", Path: RealmPath("users"), NameField: "username",
	Query: func(n string) url.Values {
		return url.Values{"username": {n}, "exact": {"true"}, "briefRepresentation": {"true"}}
	},
	Search: func(p string) url.Values {
		return url.Values{"search": {p}, "max": {"100"}, "briefRepresentation": {"true"}}
	},
}

var Clients = Lookup{
	What: "client", Path: RealmPath("clients"), NameField: "clientId",
	Query: func(n string) url.Values { return url.Values{"clientId": {n}} },
	Search: func(p string) url.Values {
		q := url.Values{"max": {"100"}}
		if p != "" {
			q.Set("clientId", p)
			q.Set("search", "true")
		}
		return q
	},
}

var ClientScopes = Lookup{What: "client scope", Path: RealmPath("client-scopes"), NameField: "name"}

var Organizations = Lookup{
	What: "organization", Path: RealmPath("organizations"), NameField: "name",
	Query: func(n string) url.Values {
		return url.Values{"search": {n}, "exact": {"true"}, "briefRepresentation": {"true"}}
	},
}

// Groups resolve by id, by path ("/parent/child"), or by unique name anywhere
// in the tree.
var Groups = Lookup{
	What: "group", Path: RealmPath("groups"), NameField: "name",
	Query: func(n string) url.Values {
		return url.Values{"search": {n}, "exact": {"true"}, "briefRepresentation": {"true"}}
	},
	Search: func(p string) url.Values {
		q := url.Values{"max": {"100"}, "briefRepresentation": {"true"}}
		if p != "" && !strings.HasPrefix(p, "/") {
			q.Set("search", p)
		}
		return q
	},
}

func FindGroup(ctx context.Context, c *kc.Client, ref string) (string, error) {
	if IsUUID(ref) {
		return ref, nil
	}
	if strings.HasPrefix(ref, "/") {
		// group-by-path takes the path as raw segments, not one escaped value.
		segs := strings.Split(strings.Trim(ref, "/"), "/")
		v, err := c.Get(ctx, c.RealmPath(append([]string{"group-by-path"}, segs...)...), nil)
		if err != nil {
			if errs.As(err).Code == errs.CodeNotFound {
				return "", errs.NotFound("no group at path '%s' in realm '%s'", ref, c.Realm)
			}
			return "", err
		}
		m, _ := v.(map[string]any)
		id, _ := m["id"].(string)
		return id, nil
	}
	v, err := c.Get(ctx, c.RealmPath("groups"), Groups.Query(ref))
	if err != nil {
		return "", err
	}
	var found []map[string]any
	var walk func(any)
	walk = func(x any) {
		l, _ := x.([]any)
		for _, g := range l {
			m, _ := g.(map[string]any)
			if m["name"] == ref {
				found = append(found, m)
			}
			walk(m["subGroups"])
		}
	}
	walk(v)
	switch len(found) {
	case 0:
		return "", errs.NotFound("no group named '%s' in realm '%s'", ref, c.Realm)
	case 1:
		id, _ := found[0]["id"].(string)
		return id, nil
	}
	paths := make([]string, len(found))
	for i, m := range found {
		paths[i], _ = m["path"].(string)
	}
	return "", errs.Usage("%d groups are named '%s'; pass a path instead (%s)", len(found), ref, strings.Join(paths, ", "))
}

// GroupCompleter offers group paths, which are unique, with the id beside.
// Keycloak 26 lists only top-level groups (subgroups are loaded lazily), so a
// path prefix like "/staff/" lists the children of /staff instead.
func (a *App) GroupCompleter() cobra.CompletionFunc {
	return a.Fetched("groups", func(ctx context.Context, c *kc.Client, _ []string, tc string) ([]Item, error) {
		var v any
		var err error
		if i := strings.LastIndex(tc, "/"); i > 0 {
			parent, perr := FindGroup(ctx, c, tc[:i])
			if perr != nil {
				return nil, perr
			}
			v, err = c.Get(ctx, c.RealmPath("groups", parent, "children"),
				url.Values{"max": {"100"}, "briefRepresentation": {"true"}})
		} else {
			v, err = c.Get(ctx, c.RealmPath("groups"), Groups.Search(strings.TrimPrefix(tc, "/")))
		}
		if err != nil {
			return nil, err
		}
		var items []Item
		var walk func(any)
		walk = func(x any) {
			l, _ := x.([]any)
			for _, g := range l {
				m, _ := g.(map[string]any)
				p, _ := m["path"].(string)
				id, _ := m["id"].(string)
				value, desc := p, id
				// A bare name typed without "/" completes as a name, which
				// FindGroup accepts when it is unique.
				if !strings.HasPrefix(tc, "/") {
					name, _ := m["name"].(string)
					value, desc = name, p+" · "+id
				}
				// Say there is more below, so the user knows to keep typing.
				if n, ok := m["subGroupCount"].(json.Number); ok && n.String() != "0" {
					desc = n.String() + " subgroups · " + desc
				}
				items = append(items, Item{Value: value, Desc: desc})
				walk(m["subGroups"])
			}
		}
		walk(v)
		return items, nil
	})
}

// ClientUUID resolves a clientId or UUID; most client sub-resources are
// addressed by the internal UUID, which users rarely know.
func ClientUUID(ctx context.Context, c *kc.Client, ref string) (string, error) {
	return Clients.Find(ctx, c, ref)
}

// ClientPath returns a PathFunc for a client sub-collection whose clientId
// comes from a flag variable read at run time.
func ClientPath(clientRef *string, segs ...string) PathFunc {
	return func(ctx context.Context, c *kc.Client) (string, error) {
		if *clientRef == "" {
			return "", errs.Usage("--client is required")
		}
		id, err := ClientUUID(ctx, c, *clientRef)
		if err != nil {
			return "", err
		}
		return c.RealmPath(append([]string{"clients", id}, segs...)...), nil
	}
}

func (a *App) ClientCompleter() cobra.CompletionFunc {
	return a.Fetched("clients", Clients.Completer())
}
func (a *App) UserCompleter() cobra.CompletionFunc { return a.Fetched("users", Users.Completer()) }
func (a *App) ClientScopeCompleter() cobra.CompletionFunc {
	return a.Fetched("client scopes", ClientScopes.Completer())
}

// RealmRoleCompleter lists realm role names.
func (a *App) RealmRoleCompleter() cobra.CompletionFunc {
	return a.Fetched("roles", func(ctx context.Context, c *kc.Client, _ []string, tc string) ([]Item, error) {
		v, err := c.Get(ctx, c.RealmPath("roles"), url.Values{"search": {tc}, "max": {"100"}, "briefRepresentation": {"true"}})
		if err != nil {
			return nil, err
		}
		return ItemsFrom(v, "name", "description"), nil
	})
}

// ClientRoleCompleter lists roles of the client named by *clientRef.
func (a *App) ClientRoleCompleter(clientRef *string) cobra.CompletionFunc {
	return a.Fetched("client roles", func(ctx context.Context, c *kc.Client, _ []string, tc string) ([]Item, error) {
		p, err := ClientPath(clientRef, "roles")(ctx, c)
		if err != nil {
			return nil, err
		}
		v, err := c.Get(ctx, p, url.Values{"search": {tc}, "max": {"100"}, "briefRepresentation": {"true"}})
		if err != nil {
			return nil, err
		}
		return ItemsFrom(v, "name", "description"), nil
	})
}
