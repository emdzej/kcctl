// Package groupcmd manages groups: the tree, members and role mappings.
//
// Groups are hand-written rather than built on app.Resource because a group
// ref may be an id, a path or a name anywhere in the tree (app.FindGroup),
// while Resource resolves names only among top-level list results.
package groupcmd

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/emdzej/keycloak-cli/internal/app"
	"github.com/emdzej/keycloak-cli/internal/errs"
	"github.com/emdzej/keycloak-cli/internal/kc"
	"github.com/emdzej/keycloak-cli/internal/output"
)

var columns = []output.Column{
	output.Col("name", ".name"), output.Col("path", ".path"),
	output.Col("subgroups", ".subGroupCount"), output.Col("id", ".id"),
}

var memberColumns = []output.Column{
	output.Col("username", ".username"), output.Col("email", ".email"),
	output.Col("enabled", ".enabled"), output.Col("id", ".id"),
}

var fields = app.Fields{
	{Flag: "name", Usage: "group name (update renames the group)"},
	{Flag: "description", Usage: "description"},
	{Flag: "attr", Path: "attributes", Kind: app.KV, Usage: "set a group attribute; empty value removes it"},
}

func New(a *app.App) *cobra.Command {
	cmd := app.Group("group", "Manage groups", "groups")
	comp := a.GroupCompleter()
	cmd.AddCommand(
		list(a), get(a, comp), create(a, comp), update(a, comp), del(a, comp),
		count(a), children(a, comp), members(a, comp),
		a.RoleMappingCmds("group", comp, func(ctx context.Context, c *kc.Client, ref string) (string, error) {
			id, err := app.FindGroup(ctx, c, ref)
			if err != nil {
				return "", err
			}
			return c.RealmPath("groups", id, "role-mappings"), nil
		}),
	)
	return cmd
}

const refUse = "<id|path|name>"

func groupPath(ctx context.Context, a *app.App, ref string, segs ...string) (*kc.Client, string, error) {
	c, err := a.Client()
	if err != nil {
		return nil, "", err
	}
	id, err := app.FindGroup(ctx, c, ref)
	if err != nil {
		return nil, "", err
	}
	return c, c.RealmPath(append([]string{"groups", id}, segs...)...), nil
}

// pageFlags binds first/max/all for endpoints that page.
type pageFlags struct {
	first, max int
	all        bool
}

func (pf *pageFlags) bind(cmd *cobra.Command) {
	cmd.Flags().IntVar(&pf.first, "first", 0, "skip this many results")
	cmd.Flags().IntVar(&pf.max, "max", 0, "return at most this many results (server default if unset)")
	cmd.Flags().BoolVar(&pf.all, "all", false, "fetch every page")
}

func (pf *pageFlags) get(ctx context.Context, cmd *cobra.Command, c *kc.Client, p string, q url.Values) (any, error) {
	if pf.all {
		return app.Paginate(ctx, c, p, q, pf.first)
	}
	if cmd.Flags().Changed("first") {
		q.Set("first", strconv.Itoa(pf.first))
	}
	if cmd.Flags().Changed("max") {
		q.Set("max", strconv.Itoa(pf.max))
	}
	return c.Get(ctx, p, q)
}

func list(a *app.App) *cobra.Command {
	var pf pageFlags
	var search, query string
	var exact, full, tree bool
	cmd := &cobra.Command{
		Use: "list", Aliases: []string{"ls"}, Short: "List groups",
		Long: `List top-level groups, or groups matching --search/--q anywhere in the tree
(returned under their ancestors). Keycloak does not include subgroups in the
top-level listing; --tree fetches them level by level.

The table shows every group returned, nested ones included; -o json/yaml keep
the nesting in subGroups.`,
		Example: "  kcctl group list --tree\n  kcctl group list --search eng\n  kcctl group list --q dept:sales -o json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			c, err := a.Client()
			if err != nil {
				return err
			}
			q := url.Values{"briefRepresentation": {strconv.FormatBool(!full)}}
			if search != "" {
				q.Set("search", search)
			}
			if query != "" {
				q.Set("q", query)
			}
			if exact {
				q.Set("exact", "true")
			}
			v, err := pf.get(ctx, cmd, c, c.RealmPath("groups"), q)
			if err != nil {
				return err
			}
			if tree {
				if err := expand(ctx, c, v, full); err != nil {
					return err
				}
			}
			return printGroups(a, v)
		},
	}
	pf.bind(cmd)
	cmd.Flags().StringVar(&search, "search", "", "match group names (substring; --exact for exact)")
	cmd.Flags().StringVar(&query, "q", "", "match attributes, key:value (space-separated for several)")
	cmd.Flags().BoolVar(&exact, "exact", false, "make --search match exactly")
	cmd.Flags().BoolVar(&full, "full", false, "return full representations (attributes, roles)")
	cmd.Flags().BoolVar(&tree, "tree", false, "fetch every subgroup recursively")
	return cmd
}

// expand fills subGroups of every group that has children but came back
// without them, which is how both 26.6 and newer servers return top-level
// groups and search matches.
func expand(ctx context.Context, c *kc.Client, v any, full bool) error {
	l, _ := v.([]any)
	for _, x := range l {
		m, _ := x.(map[string]any)
		if m == nil {
			continue
		}
		sub, _ := m["subGroups"].([]any)
		if len(sub) == 0 && subCount(m) > 0 {
			id, _ := m["id"].(string)
			kids, err := app.Paginate(ctx, c, c.RealmPath("groups", id, "children"),
				url.Values{"briefRepresentation": {strconv.FormatBool(!full)}}, 0)
			if err != nil {
				return err
			}
			sub = kids
			m["subGroups"] = kids
		}
		if err := expand(ctx, c, sub, full); err != nil {
			return err
		}
	}
	return nil
}

func subCount(m map[string]any) int64 {
	switch n := m["subGroupCount"].(type) {
	case float64:
		return int64(n)
	case interface{ Int64() (int64, error) }:
		i, _ := n.Int64()
		return i
	}
	return 0
}

// printGroups flattens the tree for tables so nested groups get rows;
// structured output keeps the server's shape.
func printGroups(a *app.App, v any) error {
	if a.Printer.IsStructured() {
		return a.Printer.Print(v, columns)
	}
	var rows []any
	var walk func(any)
	walk = func(x any) {
		l, _ := x.([]any)
		for _, g := range l {
			rows = append(rows, g)
			if m, ok := g.(map[string]any); ok {
				walk(m["subGroups"])
			}
		}
	}
	walk(v)
	if rows == nil {
		rows = []any{}
	}
	return a.Printer.Print(rows, columns)
}

func get(a *app.App, comp cobra.CompletionFunc) *cobra.Command {
	return &cobra.Command{
		Use: "get " + refUse, Short: "Show one group",
		Example: "  kcctl group get /staff/eng\n  kcctl group get eng -o yaml",
		Args:    app.ExactArgs(1, refUse), ValidArgsFunction: app.NoMoreArgs(1, comp),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, p, err := groupPath(cmd.Context(), a, args[0])
			if err != nil {
				return err
			}
			v, err := c.Get(cmd.Context(), p, nil)
			if err != nil {
				return err
			}
			return a.Printer.Print(v, columns)
		},
	}
}

func create(a *app.App, comp cobra.CompletionFunc) *cobra.Command {
	var body app.BodyFlags
	var parent string
	cmd := &cobra.Command{
		Use:     "create [name]",
		Short:   "Create a group, top-level or under --parent",
		Example: "  kcctl group create staff\n  kcctl group create eng --parent /staff --attr cost-center=42",
		Args:    cobra.MaximumNArgs(1),
	}
	fv := fields.Bind(cmd)
	body.Bind(cmd)
	cmd.Flags().StringVar(&parent, "parent", "", "create as a subgroup of this group (id, path or name)")
	cmd.RegisterFlagCompletionFunc("parent", comp)
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		b, err := body.Build(a, nil)
		if err != nil {
			return err
		}
		if len(args) == 1 {
			b["name"] = args[0]
		}
		if err := fv.Apply(cmd, b); err != nil {
			return err
		}
		if err := app.RequireFields(b, "name"); err != nil {
			return err
		}
		c, err := a.Client()
		if err != nil {
			return err
		}
		p := c.RealmPath("groups")
		if parent != "" {
			if _, p, err = groupPath(ctx, a, parent, "children"); err != nil {
				return err
			}
		}
		resp, err := c.Post(ctx, p, b)
		if err != nil {
			return err
		}
		name, _ := b["name"].(string)
		// POST .../children with an existing id moves the group (204, no Location).
		return a.ReportCreated("group", name, resp.CreatedID())
	}
	return cmd
}

func update(a *app.App, comp cobra.CompletionFunc) *cobra.Command {
	var body app.BodyFlags
	cmd := &cobra.Command{
		Use: "update " + refUse, Short: "Change a group (reads it, applies your changes, writes it back)",
		Example: "  kcctl group update /staff/eng --description Engineering --attr legacy=",
		Args:    app.ExactArgs(1, refUse), ValidArgsFunction: app.NoMoreArgs(1, comp),
	}
	fv := fields.Bind(cmd)
	body.Bind(cmd)
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		if !body.Given() && !anyChanged(cmd) {
			return errs.Usage("nothing to change").WithHint("Pass field flags, -s path=value, or -f file.")
		}
		c, p, err := groupPath(ctx, a, args[0])
		if err != nil {
			return err
		}
		cur, err := c.Get(ctx, p, nil)
		if err != nil {
			return err
		}
		b, _ := cur.(map[string]any)
		if b, err = body.Build(a, b); err != nil {
			return err
		}
		if err := fv.Apply(cmd, b); err != nil {
			return err
		}
		if _, err := c.Put(ctx, p, b); err != nil {
			return err
		}
		a.Printer.Success("Updated group '%s'.", args[0])
		return nil
	}
	return cmd
}

func anyChanged(cmd *cobra.Command) bool {
	for _, f := range fields {
		if cmd.Flags().Changed(f.Flag) {
			return true
		}
	}
	return false
}

func del(a *app.App, comp cobra.CompletionFunc) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use: "delete " + refUse + "...", Aliases: []string{"rm"}, Short: "Delete groups (and their subgroups)",
		Args: app.MinArgs(1, refUse), ValidArgsFunction: comp,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			c, err := a.Client()
			if err != nil {
				return err
			}
			// Resolve everything first so nothing is deleted when a later ref
			// is wrong, and so the confirmation can count subgroups.
			paths := make([]string, len(args))
			var withSubs []string
			for i, ref := range args {
				_, p, err := groupPath(ctx, a, ref)
				if err != nil {
					return err
				}
				paths[i] = p
				if len(args) == 1 {
					v, err := c.Get(ctx, p, nil)
					if err != nil {
						return err
					}
					m, _ := v.(map[string]any)
					if n := subCount(m); n > 0 {
						withSubs = append(withSubs, fmt.Sprintf("'%s' (%d subgroups)", ref, n))
					}
				}
			}
			switch {
			case len(args) > 1:
				if err := a.Confirm(yes, fmt.Sprintf("This deletes %d groups and all their subgroups in realm '%s'.", len(args), c.Realm)); err != nil {
					return err
				}
			case len(withSubs) > 0:
				if err := a.Confirm(yes, "This deletes group "+strings.Join(withSubs, ", ")+" and all subgroups below it."); err != nil {
					return err
				}
			}
			for i, p := range paths {
				if _, err := c.Delete(ctx, p, nil); err != nil {
					return err
				}
				a.Printer.Success("Deleted group '%s'.", args[i])
			}
			return nil
		},
	}
	app.YesFlag(cmd, &yes)
	return cmd
}

func count(a *app.App) *cobra.Command {
	var search string
	var top bool
	cmd := &cobra.Command{
		Use: "count", Short: "Count groups", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.Client()
			if err != nil {
				return err
			}
			q := url.Values{}
			if search != "" {
				q.Set("search", search)
			}
			if top {
				q.Set("top", "true")
			}
			v, err := c.Get(cmd.Context(), c.RealmPath("groups", "count"), q)
			if err != nil {
				return err
			}
			if m, ok := v.(map[string]any); ok && !a.Printer.IsStructured() {
				return a.Printer.Print(m["count"], nil)
			}
			return a.Printer.Print(v, nil)
		},
	}
	cmd.Flags().StringVar(&search, "search", "", "count only groups whose name contains this")
	cmd.Flags().BoolVar(&top, "top", false, "count only top-level groups (ignored with --search)")
	return cmd
}

func children(a *app.App, comp cobra.CompletionFunc) *cobra.Command {
	var pf pageFlags
	var search string
	var exact, full bool
	cmd := &cobra.Command{
		Use: "children <group>", Aliases: []string{"subgroups"}, Short: "List a group's direct subgroups",
		Long: "List a group's direct subgroups. The server returns 10 per page unless --max or --all is given.",
		Args: app.ExactArgs(1, "<group>"), ValidArgsFunction: app.NoMoreArgs(1, comp),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			c, p, err := groupPath(ctx, a, args[0], "children")
			if err != nil {
				return err
			}
			// The children endpoint defaults to full representations, unlike
			// the group list; ask for the same shape list returns.
			q := url.Values{"briefRepresentation": {strconv.FormatBool(!full)}}
			if search != "" {
				q.Set("search", search)
			}
			if exact {
				q.Set("exact", "true")
			}
			v, err := pf.get(ctx, cmd, c, p, q)
			if err != nil {
				return err
			}
			return a.Printer.Print(v, columns)
		},
	}
	pf.bind(cmd)
	cmd.Flags().StringVar(&search, "search", "", "only subgroups whose name matches (prefix; --exact for exact)")
	cmd.Flags().BoolVar(&exact, "exact", false, "make --search match exactly")
	cmd.Flags().BoolVar(&full, "full", false, "return full representations")
	return cmd
}

func members(a *app.App, comp cobra.CompletionFunc) *cobra.Command {
	cmd := app.Group("member", "Manage group members", "members")

	var pf pageFlags
	var full bool
	ls := &cobra.Command{
		Use: "list <group>", Aliases: []string{"ls"}, Short: "List direct members of a group",
		Args: app.ExactArgs(1, "<group>"), ValidArgsFunction: app.NoMoreArgs(1, comp),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			c, p, err := groupPath(ctx, a, args[0], "members")
			if err != nil {
				return err
			}
			v, err := pf.get(ctx, cmd, c, p, url.Values{"briefRepresentation": {strconv.FormatBool(!full)}})
			if err != nil {
				return err
			}
			return a.Printer.Print(v, memberColumns)
		},
	}
	pf.bind(ls)
	ls.Flags().BoolVar(&full, "full", false, "return full user representations (attributes, required actions)")

	groupThenUsers := func(cmd *cobra.Command, args []string, tc string) ([]string, cobra.ShellCompDirective) {
		if len(args) == 0 {
			return comp(cmd, args, tc)
		}
		return a.UserCompleter()(cmd, args, tc)
	}
	// Membership is written through the user: PUT/DELETE users/{id}/groups/{gid}.
	change := func(verb string, del bool) *cobra.Command {
		return &cobra.Command{
			Use:               verb + " <group> <user>...",
			Short:             map[bool]string{false: "Add users to a group", true: "Remove users from a group"}[del],
			Example:           "  kcctl group member " + verb + " /staff/eng alice bob",
			Args:              app.MinArgs(2, "<group> <user>..."),
			ValidArgsFunction: groupThenUsers,
			RunE: func(cmd *cobra.Command, args []string) error {
				ctx := cmd.Context()
				c, err := a.Client()
				if err != nil {
					return err
				}
				gid, err := app.FindGroup(ctx, c, args[0])
				if err != nil {
					return err
				}
				for _, u := range args[1:] {
					uid, err := app.Users.Find(ctx, c, u)
					if err != nil {
						return err
					}
					p := c.RealmPath("users", uid, "groups", gid)
					if del {
						_, err = c.Delete(ctx, p, nil)
					} else {
						_, err = c.Put(ctx, p, nil)
					}
					if err != nil {
						return err
					}
				}
				a.Printer.Success("%s %d user(s) %s group '%s'.", map[bool]string{false: "Added", true: "Removed"}[del],
					len(args)-1, map[bool]string{false: "to", true: "from"}[del], args[0])
				return nil
			},
		}
	}
	cmd.AddCommand(ls, change("add", false), change("remove", true))
	return cmd
}
