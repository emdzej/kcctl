// Package policycmd manages client policies and client profiles, the realm's
// rules for what clients must look like (e.g. FAPI).
package policycmd

import (
	"net/url"

	"github.com/spf13/cobra"

	"github.com/emdzej/keycloak-cli/internal/app"
	"github.com/emdzej/keycloak-cli/internal/errs"
	"github.com/emdzej/keycloak-cli/internal/output"
)

func New(a *app.App) *cobra.Command {
	cmd := app.Group("client-policy", "Manage client policies and client profiles", "client-policies")
	cmd.AddCommand(
		doc(a, docSpec{use: "policies", short: "client policies", seg: "policies", key: "policies", global: "globalPolicies", param: "include-global-policies",
			cols: []output.Column{
				output.Col("name", ".name"), output.Col("enabled", ".enabled"), output.Col("profiles", ".profiles"),
				output.Col("global", ".global"), output.Col("description", ".description"),
			}}),
		doc(a, docSpec{use: "profiles", short: "client profiles", seg: "profiles", key: "profiles", global: "globalProfiles", param: "include-global-profiles",
			cols: []output.Column{
				output.Col("name", ".name"), output.Col("executors", ".executors[*].executor"),
				output.Col("global", ".global"), output.Col("description", ".description"),
			}}),
	)
	return cmd
}

type docSpec struct {
	use, short, seg, key, global, param string
	cols                                []output.Column
}

// doc builds get/update for one of the two realm documents. Both are a single
// object holding the realm's list plus the read-only built-in (global) list.
func doc(a *app.App, s docSpec) *cobra.Command {
	cmd := app.Group(s.use, "Show or replace the realm's "+s.short, s.use[:len(s.use)-1])
	var global bool
	get := &cobra.Command{
		Use: "get", Short: "Show the realm's " + s.short, Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.Client()
			if err != nil {
				return err
			}
			var q url.Values
			if global {
				q = url.Values{s.param: {"true"}}
			}
			v, err := c.Get(cmd.Context(), c.RealmPath("client-policies", s.seg), q)
			if err != nil {
				return err
			}
			if a.Printer.IsStructured() {
				return a.Printer.Print(v, nil)
			}
			m, _ := v.(map[string]any)
			rows := []any{}
			add := func(l any, isGlobal bool) {
				items, _ := l.([]any)
				for _, x := range items {
					if r, ok := x.(map[string]any); ok {
						r["global"] = isGlobal
						rows = append(rows, r)
					}
				}
			}
			add(m[s.key], false)
			add(m[s.global], true)
			return a.Printer.Print(rows, s.cols)
		},
	}
	get.Flags().BoolVar(&global, "include-global", false, "also show the built-in "+s.short)

	var body app.BodyFlags
	update := &cobra.Command{
		Use:   "update",
		Short: "Change the realm's " + s.short + " (reads them, applies -f/-s, writes them back)",
		Long: `Change the realm's ` + s.short + `. The current document is read, -f and -s are
applied on top, and the result is written back. A file with a top-level
"` + s.key + `" list replaces the whole list; built-in (global) entries cannot be
changed.`,
		Example: "  kcctl client-policy " + s.use + " get -o yaml > " + s.use + ".yaml\n  kcctl client-policy " + s.use + " update -f " + s.use + ".yaml",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			if !body.Given() {
				return errs.Usage("nothing to change").WithHint("Pass -f file or -s path=value.")
			}
			c, err := a.Client()
			if err != nil {
				return err
			}
			p := c.RealmPath("client-policies", s.seg)
			cur, err := c.Get(ctx, p, nil)
			if err != nil {
				return err
			}
			b, _ := cur.(map[string]any)
			if b, err = body.Build(a, b); err != nil {
				return err
			}
			// A document read with --include-global and fed back would
			// otherwise carry the built-ins along.
			delete(b, s.global)
			if _, err := c.Put(ctx, p, b); err != nil {
				return err
			}
			a.Printer.Success("Updated %s of realm '%s'.", s.short, c.Realm)
			return nil
		},
	}
	body.Bind(update)
	cmd.AddCommand(get, update)
	return cmd
}
