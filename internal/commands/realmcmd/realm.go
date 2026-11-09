// Package realmcmd manages realms, realm export/import and the realm-wide
// default scopes and groups.
package realmcmd

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/emdzej/kcctl/internal/app"
	"github.com/emdzej/kcctl/internal/errs"
	"github.com/emdzej/kcctl/internal/kc"
	"github.com/emdzej/kcctl/internal/output"
)

var realms = &app.Resource{
	Noun: "realm", Plural: "realms",
	Lookup: app.Lookup{
		What: "realm", NameField: "realm", IDField: "realm",
		Path: func(context.Context, *kc.Client) (string, error) { return "/admin/realms", nil },
	},
	KeyByName: true,
	Columns: []output.Column{
		output.Col("realm", ".realm"), output.Col("display name", ".displayName"),
		output.Col("enabled", ".enabled"), output.Col("id", ".id"),
	},
	ListFlags: func(cmd *cobra.Command) func(url.Values) {
		full := cmd.Flags().Bool("full", false, "return full representations")
		return func(q url.Values) {
			if !*full {
				q.Set("briefRepresentation", "true")
			}
		}
	},
	Fields: app.Fields{
		{Flag: "display-name", Path: "displayName", Usage: "display name"},
		{Flag: "enabled", Kind: app.Bool, Usage: "whether the realm is enabled"},
		{Flag: "login-theme", Path: "loginTheme", Usage: "login theme"},
		{Flag: "registration-allowed", Path: "registrationAllowed", Kind: app.Bool, Usage: "allow self-registration"},
		{Flag: "verify-email", Path: "verifyEmail", Kind: app.Bool, Usage: "require email verification"},
		{Flag: "reset-password-allowed", Path: "resetPasswordAllowed", Kind: app.Bool, Usage: "allow forgot-password"},
		{Flag: "login-with-email", Path: "loginWithEmailAllowed", Kind: app.Bool, Usage: "allow login with email"},
		{Flag: "organizations", Path: "organizationsEnabled", Kind: app.Bool, Usage: "enable organizations"},
		{Flag: "ssl-required", Path: "sslRequired", Usage: "all|external|none"},
	},
	CreateArg:     "realm",
	Required:      []string{"realm"},
	Defaults:      map[string]any{"enabled": true},
	ConfirmDelete: true,
	Example: map[string]string{
		"create": "  kcctl realm create acme --display-name 'ACME Corp'\n  kcctl realm create -f realm.json",
		"update": "  kcctl realm update acme --registration-allowed=true -s attributes.frontendUrl=https://id.acme.com",
	},
}

func New(a *app.App) *cobra.Command {
	cmd := app.Group("realm", "Manage realms", "realms")
	cmd.AddCommand(realms.CRUD(a)...)
	for _, c := range cmd.Commands() {
		if c.ValidArgsFunction == nil {
			continue
		}
		c.ValidArgsFunction = realmArg(a, c.Name() != "delete")
	}
	cmd.AddCommand(use(a), export(a), importCmd(a), defaultScopes(a), defaultGroups(a))
	return cmd
}

func realmArg(a *app.App, single bool) cobra.CompletionFunc {
	if single {
		return app.NoMoreArgs(1, a.CompleteRealms)
	}
	return a.CompleteRealms
}

func use(a *app.App) *cobra.Command {
	return &cobra.Command{
		Use:               "use <realm>",
		Short:             "Make <realm> the default target realm of the current context",
		Args:              app.ExactArgs(1, "<realm>"),
		ValidArgsFunction: app.NoMoreArgs(1, a.CompleteRealms),
		RunE: func(_ *cobra.Command, args []string) error {
			f, err := a.Config()
			if err != nil {
				return err
			}
			r, err := a.Resolve()
			if err != nil {
				return err
			}
			ctx, ok := f.Get(r.Name)
			if !ok {
				return errs.Usage("no current context to change").WithHint("Pass --realm per command, or create a context with 'kcctl config set-context'.")
			}
			ctx.Realm = args[0]
			if err := f.Save(); err != nil {
				return err
			}
			a.Printer.Success("Context '%s' now targets realm '%s'.", ctx.Name, args[0])
			return nil
		},
	}
}

func export(a *app.App) *cobra.Command {
	var clients, groupsRoles bool
	var out string
	cmd := &cobra.Command{
		Use:   "export [realm]",
		Short: "Export the realm configuration (secrets are masked by the server)",
		Example: `  kcctl realm export acme --clients --groups-and-roles --out acme.json
  kcctl realm export -o yaml > acme.yaml`,
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: app.NoMoreArgs(1, a.CompleteRealms),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.Client()
			if err != nil {
				return err
			}
			realm := c.Realm
			if len(args) == 1 {
				realm = args[0]
			}
			resp, err := c.Do(cmd.Context(), kc.Request{
				Method: "POST", Path: kc.RealmPathFor(realm, "partial-export"),
				Query: url.Values{"exportClients": {strconv.FormatBool(clients)}, "exportGroupsAndRoles": {strconv.FormatBool(groupsRoles)}},
			})
			if err != nil {
				return err
			}
			v, err := resp.JSON()
			if err != nil {
				return err
			}
			if out != "" {
				return writeDoc(a, out, v)
			}
			if a.Printer.Format == "" {
				a.Printer.Format = "json"
			}
			return a.Printer.Print(v, nil)
		},
	}
	cmd.Flags().BoolVar(&clients, "clients", false, "include clients")
	cmd.Flags().BoolVar(&groupsRoles, "groups-and-roles", false, "include groups and roles")
	cmd.Flags().StringVar(&out, "out", "", "write to this file (.json or .yaml)")
	return cmd
}

func importCmd(a *app.App) *cobra.Command {
	var file, policy string
	var yes bool
	cmd := &cobra.Command{
		Use:   "import [realm]",
		Short: "Import users, clients, groups, roles and IdPs into an existing realm",
		Long: `Partial import into an existing realm. --if-exists decides what happens to
resources that already exist: FAIL (default, nothing is changed), SKIP, or
OVERWRITE (asks for confirmation, since it replaces existing resources).

To create a new realm from a file, use 'kcctl realm create -f realm.json'.`,
		Example:           `  kcctl realm import acme -f export.json --if-exists SKIP`,
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: app.NoMoreArgs(1, a.CompleteRealms),
		RunE: func(cmd *cobra.Command, args []string) error {
			if file == "" {
				return errs.Usage("-f/--file is required")
			}
			switch policy {
			case "FAIL", "SKIP", "OVERWRITE":
			default:
				return errs.Usage("--if-exists must be FAIL, SKIP or OVERWRITE")
			}
			c, err := a.Client()
			if err != nil {
				return err
			}
			realm := c.Realm
			if len(args) == 1 {
				realm = args[0]
			}
			doc, err := app.ReadDocument(a, file)
			if err != nil {
				return err
			}
			body, ok := doc.(map[string]any)
			if !ok {
				return errs.Usage("%s must contain a JSON/YAML object", file)
			}
			body["ifResourceExists"] = policy
			if policy == "OVERWRITE" {
				if err := a.Confirm(yes, fmt.Sprintf("This overwrites existing resources in realm '%s' with the ones in %s.", realm, file)); err != nil {
					return err
				}
			}
			resp, err := c.Post(cmd.Context(), kc.RealmPathFor(realm, "partialImport"), body)
			if err != nil {
				return err
			}
			v, err := resp.JSON()
			if err != nil {
				return err
			}
			m, _ := v.(map[string]any)
			if a.Printer.IsStructured() {
				return a.Printer.Print(v, nil)
			}
			a.Printer.Success("Imported into '%s': %v added, %v overwritten, %v skipped.", realm, m["added"], m["overwritten"], m["skipped"])
			if res, ok := m["results"].([]any); ok && len(res) > 0 && !a.Flags.Quiet {
				return a.Printer.Print(res, []output.Column{
					output.Col("action", ".action"), output.Col("type", ".resourceType"),
					output.Col("name", ".resourceName"), output.Col("id", ".id"),
				})
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&file, "file", "f", "", "export file (JSON or YAML, '-' for stdin)")
	cmd.Flags().StringVar(&policy, "if-exists", "FAIL", "FAIL|SKIP|OVERWRITE")
	cmd.RegisterFlagCompletionFunc("if-exists", app.Values("FAIL", "SKIP", "OVERWRITE"))
	app.YesFlag(cmd, &yes)
	return cmd
}

func writeDoc(a *app.App, path string, v any) error {
	ext := strings.ToLower(filepath.Ext(path))
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return errs.Usage("cannot write %s: %v", path, err)
	}
	p := *a.Printer
	p.Out = f
	// Export files hold the bare realm, not the {data} envelope, so they can
	// be fed straight back to 'realm import' or 'realm create -f'.
	if err := p.Raw(v, ext == ".yaml" || ext == ".yml"); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	a.Printer.Success("Wrote %s.", path)
	return nil
}

// defaultScopes manages the realm's default client scopes, which every new
// client receives.
func defaultScopes(a *app.App) *cobra.Command {
	cmd := app.Group("default-scope", "Manage the client scopes new clients get by default", "default-scopes")
	var optional bool
	cmd.PersistentFlags().BoolVar(&optional, "optional", false, "work with optional instead of default scopes")
	seg := func() string {
		if optional {
			return "default-optional-client-scopes"
		}
		return "default-default-client-scopes"
	}
	cmd.AddCommand(
		&cobra.Command{
			Use: "list", Aliases: []string{"ls"}, Short: "List realm default client scopes", Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				c, err := a.Client()
				if err != nil {
					return err
				}
				v, err := c.Get(cmd.Context(), c.RealmPath(seg()), nil)
				if err != nil {
					return err
				}
				return a.Printer.Print(v, []output.Column{output.Col("name", ".name"), output.Col("id", ".id")})
			},
		},
		scopeChange(a, "add", seg, false),
		scopeChange(a, "remove", seg, true),
	)
	return cmd
}

func scopeChange(a *app.App, verb string, seg func() string, del bool) *cobra.Command {
	return &cobra.Command{
		Use:               verb + " <scope>...",
		Short:             map[bool]string{false: "Add", true: "Remove"}[del] + " realm default client scopes",
		Args:              app.MinArgs(1, "<scope>..."),
		ValidArgsFunction: a.ClientScopeCompleter(),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			c, err := a.Client()
			if err != nil {
				return err
			}
			for _, ref := range args {
				id, err := app.ClientScopes.Find(ctx, c, ref)
				if err != nil {
					return err
				}
				p := c.RealmPath(seg(), id)
				if del {
					_, err = c.Delete(ctx, p, nil)
				} else {
					_, err = c.Put(ctx, p, nil)
				}
				if err != nil {
					return err
				}
			}
			a.Printer.Success("%s %d realm default scope(s).", map[bool]string{false: "Added", true: "Removed"}[del], len(args))
			return nil
		},
	}
}

func defaultGroups(a *app.App) *cobra.Command {
	cmd := app.Group("default-group", "Manage groups every new user joins", "default-groups")
	cmd.AddCommand(
		&cobra.Command{
			Use: "list", Aliases: []string{"ls"}, Short: "List default groups", Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				c, err := a.Client()
				if err != nil {
					return err
				}
				v, err := c.Get(cmd.Context(), c.RealmPath("default-groups"), nil)
				if err != nil {
					return err
				}
				return a.Printer.Print(v, []output.Column{output.Col("path", ".path"), output.Col("id", ".id")})
			},
		},
		groupChange(a, "add", false), groupChange(a, "remove", true),
	)
	return cmd
}

func groupChange(a *app.App, verb string, del bool) *cobra.Command {
	return &cobra.Command{
		Use:               verb + " <group>...",
		Short:             map[bool]string{false: "Add", true: "Remove"}[del] + " default groups",
		Args:              app.MinArgs(1, "<group>..."),
		ValidArgsFunction: a.GroupCompleter(),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			c, err := a.Client()
			if err != nil {
				return err
			}
			for _, ref := range args {
				id, err := app.FindGroup(ctx, c, ref)
				if err != nil {
					return err
				}
				p := c.RealmPath("default-groups", id)
				if del {
					_, err = c.Delete(ctx, p, nil)
				} else {
					_, err = c.Put(ctx, p, nil)
				}
				if err != nil {
					return err
				}
			}
			a.Printer.Success("%s %d default group(s).", map[bool]string{false: "Added", true: "Removed"}[del], len(args))
			return nil
		},
	}
}
