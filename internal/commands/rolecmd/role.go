// Package rolecmd manages realm roles, their composites, and who holds them.
// Client roles live under `client role`.
package rolecmd

import (
	"context"
	"net/url"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/emdzej/keycloak-cli/internal/app"
	"github.com/emdzej/keycloak-cli/internal/errs"
	"github.com/emdzej/keycloak-cli/internal/kc"
	"github.com/emdzej/keycloak-cli/internal/output"
)

var roleLookup = app.Lookup{What: "role", Path: app.RealmPath("roles"), NameField: "name"}

// Roles are addressed by name (roles/{role-name}); the id-based roles-by-id
// endpoints are not needed when the name is unique within the realm.
var roles = &app.Resource{
	Noun: "role", Plural: "roles", Lookup: roleLookup, KeyByName: true,
	Columns: app.RoleColumns, Paged: true,
	ListFlags: func(cmd *cobra.Command) func(url.Values) {
		search := cmd.Flags().String("search", "", "only roles whose name contains this")
		full := cmd.Flags().Bool("full", false, "return full representations (with attributes)")
		return func(q url.Values) {
			if *search != "" {
				q.Set("search", *search)
			}
			// The server defaults to brief; say so explicitly either way.
			q.Set("briefRepresentation", strconv.FormatBool(!*full))
		}
	},
	Fields: app.Fields{
		{Flag: "name", Usage: "role name (update renames the role)"},
		{Flag: "description", Usage: "description"},
		{Flag: "attr", Path: "attributes", Kind: app.KV, Usage: "set a role attribute; empty value removes it"},
	},
	CreateArg: "name",
	Required:  []string{"name"},
	Example: map[string]string{
		"list":   "  kcctl role list --search admin\n  kcctl role list --all -o json",
		"create": "  kcctl role create auditor --description 'Read-only access' --attr team=sec",
		"update": "  kcctl role update auditor --description 'Audit access'",
	},
}

func New(a *app.App) *cobra.Command {
	cmd := app.Group("role", "Manage realm roles", "roles")
	cmd.AddCommand(roles.CRUD(a)...)
	comp := a.RealmRoleCompleter()
	// The Resource completer would show ids; realm roles are keyed by name.
	for _, c := range cmd.Commands() {
		switch c.Name() {
		case "get", "update":
			c.ValidArgsFunction = app.NoMoreArgs(1, comp)
		case "delete":
			c.ValidArgsFunction = comp
		}
	}
	cmd.AddCommand(composites(a, comp), users(a, comp), groups(a, comp))
	return cmd
}

func rolePath(c *kc.Client, name string, segs ...string) string {
	return c.RealmPath(append([]string{"roles", name}, segs...)...)
}

func composites(a *app.App, comp cobra.CompletionFunc) *cobra.Command {
	cmd := app.Group("composite", "Manage the roles a composite role includes", "composites")
	var client string
	cmd.PersistentFlags().StringVar(&client, "client", "", "work with this client's roles instead of realm roles")
	cmd.RegisterFlagCompletionFunc("client", a.ClientCompleter())

	var realmOnly bool
	list := &cobra.Command{
		Use: "list <role>", Aliases: []string{"ls"}, Short: "List the roles a composite includes",
		Long: `List the roles a composite includes. Without --client both realm and client
roles are shown; --client narrows to that client's roles, --realm-only to
realm roles.`,
		Args: app.ExactArgs(1, "<role>"), ValidArgsFunction: app.NoMoreArgs(1, comp),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if realmOnly && client != "" {
				return errs.Usage("--realm-only and --client cannot be combined")
			}
			c, err := a.Client()
			if err != nil {
				return err
			}
			p := rolePath(c, args[0], "composites")
			switch {
			case realmOnly:
				p = rolePath(c, args[0], "composites", "realm")
			case client != "":
				id, err := app.ClientUUID(ctx, c, client)
				if err != nil {
					return err
				}
				p = rolePath(c, args[0], "composites", "clients", id)
			}
			v, err := c.Get(ctx, p, nil)
			if err != nil {
				return err
			}
			return a.Printer.Print(v, compositeColumns)
		},
	}
	list.Flags().BoolVar(&realmOnly, "realm-only", false, "only realm roles")

	roleArgs := func(cmd *cobra.Command, args []string, tc string) ([]string, cobra.ShellCompDirective) {
		if len(args) == 0 || client == "" {
			return comp(cmd, args, tc)
		}
		return a.ClientRoleCompleter(&client)(cmd, args, tc)
	}
	change := func(verb string, del bool) *cobra.Command {
		return &cobra.Command{
			Use:               verb + " <role> <role>...",
			Short:             map[bool]string{false: "Add roles to a composite", true: "Remove roles from a composite"}[del],
			Example:           "  kcctl role composite " + verb + " admin auditor\n  kcctl role composite " + verb + " admin view-users --client realm-management",
			Args:              app.MinArgs(2, "<role> <role>..."),
			ValidArgsFunction: roleArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				ctx := cmd.Context()
				c, err := a.Client()
				if err != nil {
					return err
				}
				reps, err := app.ResolveRoles(ctx, c, client, args[1:])
				if err != nil {
					return err
				}
				p := rolePath(c, args[0], "composites")
				if del {
					_, err = c.Delete(ctx, p, reps)
				} else {
					_, err = c.Post(ctx, p, reps)
				}
				if err != nil {
					return err
				}
				a.Printer.Success("%s %d role(s) %s composite '%s'.", map[bool]string{false: "Added", true: "Removed"}[del],
					len(reps), map[bool]string{false: "to", true: "from"}[del], args[0])
				return nil
			},
		}
	}
	cmd.AddCommand(list, change("add", false), change("remove", true))
	return cmd
}

var compositeColumns = []output.Column{
	output.Col("name", ".name"), output.Col("client role", ".clientRole"),
	output.Col("container", ".containerId"), output.Col("description", ".description"), output.Col("id", ".id"),
}

// paged binds first/max/all and returns a fetcher for a list endpoint whose
// server-side default page is small (role users/groups default to 100).
func paged(cmd *cobra.Command) func(ctx context.Context, c *kc.Client, p string, q url.Values) (any, error) {
	var first, maxN int
	var all bool
	cmd.Flags().IntVar(&first, "first", 0, "skip this many results")
	cmd.Flags().IntVar(&maxN, "max", 0, "return at most this many results (server default if unset)")
	cmd.Flags().BoolVar(&all, "all", false, "fetch every page")
	return func(ctx context.Context, c *kc.Client, p string, q url.Values) (any, error) {
		if all {
			return app.Paginate(ctx, c, p, q, first)
		}
		if cmd.Flags().Changed("first") {
			q.Set("first", strconv.Itoa(first))
		}
		if cmd.Flags().Changed("max") {
			q.Set("max", strconv.Itoa(maxN))
		}
		return c.Get(ctx, p, q)
	}
}

func users(a *app.App, comp cobra.CompletionFunc) *cobra.Command {
	var full bool
	cmd := &cobra.Command{
		Use: "users <role>", Short: "List users that hold the role directly",
		Args: app.ExactArgs(1, "<role>"), ValidArgsFunction: app.NoMoreArgs(1, comp),
	}
	fetch := paged(cmd)
	cmd.Flags().BoolVar(&full, "full", false, "return full user representations")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		c, err := a.Client()
		if err != nil {
			return err
		}
		v, err := fetch(cmd.Context(), c, rolePath(c, args[0], "users"), url.Values{"briefRepresentation": {strconv.FormatBool(!full)}})
		if err != nil {
			return err
		}
		return a.Printer.Print(v, []output.Column{
			output.Col("username", ".username"), output.Col("email", ".email"),
			output.Col("enabled", ".enabled"), output.Col("id", ".id"),
		})
	}
	return cmd
}

func groups(a *app.App, comp cobra.CompletionFunc) *cobra.Command {
	var full bool
	cmd := &cobra.Command{
		Use: "groups <role>", Short: "List groups that hold the role directly",
		Args: app.ExactArgs(1, "<role>"), ValidArgsFunction: app.NoMoreArgs(1, comp),
	}
	fetch := paged(cmd)
	cmd.Flags().BoolVar(&full, "full", false, "return full group representations")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		c, err := a.Client()
		if err != nil {
			return err
		}
		v, err := fetch(cmd.Context(), c, rolePath(c, args[0], "groups"), url.Values{"briefRepresentation": {strconv.FormatBool(!full)}})
		if err != nil {
			return err
		}
		return a.Printer.Print(v, []output.Column{output.Col("path", ".path"), output.Col("id", ".id")})
	}
	return cmd
}
