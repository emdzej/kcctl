// Package configcmd manages contexts: named server + realm + sign-in settings.
package configcmd

import (
	"strings"

	"github.com/spf13/cobra"

	"github.com/emdzej/keycloak-cli/internal/app"
	"github.com/emdzej/keycloak-cli/internal/config"
	"github.com/emdzej/keycloak-cli/internal/errs"
	"github.com/emdzej/keycloak-cli/internal/output"
)

func New(a *app.App) *cobra.Command {
	cmd := app.Group("config", "Manage contexts (servers, realms, sign-in settings)")
	cmd.GroupID = "session"
	cmd.AddCommand(view(a), getContexts(a), currentContext(a), useContext(a), setContext(a), deleteContext(a), renameContext(a))
	return cmd
}

func contextNames(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	f, err := config.Load()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var out []string
	for _, c := range f.Contexts {
		out = append(out, c.Name+"\t"+c.Server)
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}

func view(a *app.App) *cobra.Command {
	return &cobra.Command{
		Use: "view", Short: "Show the config file", Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			f, err := a.Config()
			if err != nil {
				return err
			}
			a.Printer.Note("# %s", config.ConfigFile())
			if a.Printer.Format == "" {
				a.Printer.Format = "yaml"
			}
			return a.Printer.Print(toMap(f), nil)
		},
	}
}

func toMap(f *config.File) map[string]any {
	ctxs := make([]any, 0, len(f.Contexts))
	for _, c := range f.Contexts {
		ctxs = append(ctxs, contextMap(c, f.CurrentContext))
	}
	return map[string]any{"currentContext": f.CurrentContext, "contexts": ctxs}
}

func contextMap(c config.Context, current string) map[string]any {
	m := map[string]any{"name": c.Name, "server": c.Server}
	set := func(k, v string) {
		if v != "" {
			m[k] = v
		}
	}
	set("authRealm", c.AuthRealm)
	set("realm", c.Realm)
	set("clientId", c.ClientID)
	set("grant", string(c.Grant))
	set("username", c.Username)
	set("caFile", c.CAFile)
	if c.Insecure {
		m["insecureSkipTLSVerify"] = true
	}
	if current != "" {
		m["current"] = c.Name == current
	}
	return m
}

func getContexts(a *app.App) *cobra.Command {
	return &cobra.Command{
		Use: "get-contexts", Aliases: []string{"contexts"}, Short: "List contexts", Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			f, err := a.Config()
			if err != nil {
				return err
			}
			rows := make([]any, 0, len(f.Contexts))
			for _, c := range f.Contexts {
				m := contextMap(c, f.CurrentContext)
				if c.Name == f.CurrentContext {
					m["marker"] = "*"
				}
				rows = append(rows, m)
			}
			return a.Printer.Print(rows, []output.Column{
				output.Col("current", ".marker"), output.Col("name", ".name"), output.Col("server", ".server"),
				output.Col("auth realm", ".authRealm"), output.Col("realm", ".realm"),
				output.Col("grant", ".grant"), output.Col("user", ".username"),
			})
		},
	}
}

func currentContext(a *app.App) *cobra.Command {
	return &cobra.Command{
		Use: "current-context", Short: "Print the current context name", Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			f, err := a.Config()
			if err != nil {
				return err
			}
			if f.CurrentContext == "" {
				return errs.Usage("no current context").WithHint("Set one with 'kcctl config use-context <name>'.")
			}
			return a.Printer.Print(f.CurrentContext, nil)
		},
	}
}

func useContext(a *app.App) *cobra.Command {
	return &cobra.Command{
		Use: "use-context <name>", Aliases: []string{"use"}, Short: "Switch the current context",
		Args: app.ExactArgs(1, "<name>"), ValidArgsFunction: app.NoMoreArgs(1, contextNames),
		RunE: func(_ *cobra.Command, args []string) error {
			f, err := a.Config()
			if err != nil {
				return err
			}
			if _, ok := f.Get(args[0]); !ok {
				return errs.NotFound("context %q does not exist", args[0])
			}
			f.CurrentContext = args[0]
			if err := f.Save(); err != nil {
				return err
			}
			a.Printer.Success("Switched to context '%s'.", args[0])
			return nil
		},
	}
}

func setContext(a *app.App) *cobra.Command {
	var c config.Context
	var grant string
	var use bool
	cmd := &cobra.Command{
		Use:   "set-context <name>",
		Short: "Create or change a context",
		Long: `Create or change a context. Only the flags you pass are changed on an
existing context. Global --server/--realm/--auth-realm/--client-id/--insecure
set the corresponding context fields.`,
		Example: `  kcctl config set-context local --server http://localhost:8080 --username admin
  kcctl config set-context prod --server https://sso.example.com --auth-realm master --realm acme
  kcctl config set-context sso --server https://sso.example.com --grant browser --client-id kcctl
  kcctl config set-context ci --server https://sso.example.com --grant client_credentials --client-id kcctl-ci`,
		Args:              app.ExactArgs(1, "<name>"),
		ValidArgsFunction: app.NoMoreArgs(1, contextNames),
		RunE: func(cmd *cobra.Command, args []string) error {
			f, err := a.Config()
			if err != nil {
				return err
			}
			cur, exists := f.Get(args[0])
			next := config.Context{Name: args[0]}
			if exists {
				next = *cur
			}
			gf := cmd.Flags()
			if gf.Changed("server") {
				next.Server = a.Flags.Server
			}
			if gf.Changed("auth-realm") {
				next.AuthRealm = a.Flags.AuthRealm
			}
			if gf.Changed("realm") {
				next.Realm = a.Flags.Realm
			}
			if gf.Changed("client-id") {
				next.ClientID = a.Flags.ClientID
			}
			if gf.Changed("insecure") {
				next.Insecure = a.Flags.Insecure
			}
			if gf.Changed("username") {
				next.Username = c.Username
			}
			if gf.Changed("ca-file") {
				next.CAFile = c.CAFile
			}
			if gf.Changed("grant") {
				if !config.ValidGrant(config.Grant(grant)) {
					return errs.Usage("--grant must be one of %s", strings.Join(config.GrantNames(), ", "))
				}
				next.Grant = config.Grant(grant)
			}
			if next.Server == "" {
				return errs.Usage("a new context needs --server")
			}
			f.Upsert(next)
			if use || f.CurrentContext == "" {
				f.CurrentContext = next.Name
			}
			if err := f.Save(); err != nil {
				return err
			}
			verb := map[bool]string{true: "Updated", false: "Created"}[exists]
			a.Printer.Success("%s context '%s'.", verb, next.Name)
			if f.CurrentContext == next.Name {
				a.Printer.Note("Current context is '%s'. Sign in with 'kcctl auth login'.", next.Name)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&c.Username, "username", "", "default username for password sign-in")
	cmd.Flags().StringVar(&c.CAFile, "ca-file", "", "PEM file with extra CA certificates")
	cmd.Flags().StringVar(&grant, "grant", "", "sign-in grant: browser|password|client_credentials|device")
	cmd.Flags().BoolVar(&use, "use", false, "also make it the current context")
	cmd.RegisterFlagCompletionFunc("grant", app.Values(config.GrantNames()...))
	return cmd
}

func deleteContext(a *app.App) *cobra.Command {
	return &cobra.Command{
		Use: "delete-context <name>", Short: "Delete a context and its stored session",
		Args: app.ExactArgs(1, "<name>"), ValidArgsFunction: app.NoMoreArgs(1, contextNames),
		RunE: func(_ *cobra.Command, args []string) error {
			f, err := a.Config()
			if err != nil {
				return err
			}
			if !f.Delete(args[0]) {
				return errs.NotFound("context %q does not exist", args[0])
			}
			if err := f.Save(); err != nil {
				return err
			}
			_ = a.Store.Delete(args[0])
			a.Printer.Success("Deleted context '%s'.", args[0])
			return nil
		},
	}
}

func renameContext(a *app.App) *cobra.Command {
	return &cobra.Command{
		Use: "rename-context <old> <new>", Short: "Rename a context",
		Args: app.ExactArgs(2, "<old> <new>"), ValidArgsFunction: app.NoMoreArgs(1, contextNames),
		RunE: func(_ *cobra.Command, args []string) error {
			f, err := a.Config()
			if err != nil {
				return err
			}
			c, ok := f.Get(args[0])
			if !ok {
				return errs.NotFound("context %q does not exist", args[0])
			}
			if _, taken := f.Get(args[1]); taken {
				return errs.Usage("context %q already exists", args[1])
			}
			// Move the stored session with it so the rename doesn't sign out.
			if s, err := a.Store.Load(args[0]); err == nil && s != nil {
				_ = a.Store.Save(args[1], s)
				_ = a.Store.Delete(args[0])
			}
			c.Name = args[1]
			if f.CurrentContext == args[0] {
				f.CurrentContext = args[1]
			}
			if err := f.Save(); err != nil {
				return err
			}
			a.Printer.Success("Renamed context '%s' to '%s'.", args[0], args[1])
			return nil
		},
	}
}
