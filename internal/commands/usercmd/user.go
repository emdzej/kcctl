// Package usercmd manages users and everything hanging off a user: password,
// required actions, sessions, credentials, groups, role mappings and
// identity-provider links.
package usercmd

import (
	"context"
	"net/url"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/emdzej/kcctl/internal/app"
	"github.com/emdzej/kcctl/internal/compat"
	"github.com/emdzej/kcctl/internal/errs"
	"github.com/emdzej/kcctl/internal/kc"
	"github.com/emdzej/kcctl/internal/output"
)

var columns = []output.Column{
	output.Col("username", ".username"), output.Col("email", ".email"),
	output.Col("first name", ".firstName"), output.Col("last name", ".lastName"),
	output.Col("enabled", ".enabled"), output.Col("id", ".id"),
}

var users = &app.Resource{
	Noun: "user", Plural: "users", Lookup: app.Users, Columns: columns, Paged: true,
	ListFlags: func(cmd *cobra.Command) func(url.Values) {
		f := cmd.Flags()
		search := f.String("search", "", "match username, email, first or last name (prefix; *x* for infix, \"x\" for exact)")
		username := f.String("username", "", "filter by username")
		email := f.String("email", "", "filter by email")
		exact := f.Bool("exact", false, "make --username/--email/--first-name/--last-name match exactly")
		firstName := f.String("first-name", "", "filter by first name")
		lastName := f.String("last-name", "", "filter by last name")
		enabled := f.String("enabled", "", "filter by enabled state (true|false)")
		idp := f.String("idp", "", "only users linked to this identity provider alias")
		attr := f.StringArray("attr", nil, "filter by attribute key:value (repeatable)")
		cmd.RegisterFlagCompletionFunc("enabled", app.Values("true", "false"))
		return func(q url.Values) {
			set := func(k, v string) {
				if v != "" {
					q.Set(k, v)
				}
			}
			set("search", *search)
			set("username", *username)
			set("email", *email)
			set("firstName", *firstName)
			set("lastName", *lastName)
			set("enabled", *enabled)
			set("idpAlias", *idp)
			if *exact {
				q.Set("exact", "true")
			}
			if len(*attr) > 0 {
				s := ""
				for i, x := range *attr {
					if i > 0 {
						s += " "
					}
					s += x
				}
				q.Set("q", s)
			}
		}
	},
	Fields: app.Fields{
		{Flag: "username", Usage: "username"},
		{Flag: "email", Usage: "email address"},
		{Flag: "first-name", Path: "firstName", Usage: "first name"},
		{Flag: "last-name", Path: "lastName", Usage: "last name"},
		{Flag: "enabled", Kind: app.Bool, Usage: "whether the user can sign in"},
		{Flag: "email-verified", Path: "emailVerified", Kind: app.Bool, Usage: "mark the email as verified"},
		{Flag: "attr", Path: "attributes", Kind: app.KV, Usage: "set a user attribute; empty value removes it"},
		{Flag: "required-action", Path: "requiredActions", Kind: app.Strings, Usage: "required actions, e.g. UPDATE_PASSWORD,VERIFY_EMAIL"},
		{Flag: "group", Path: "groups", Kind: app.Strings, Usage: "group paths to join on create, e.g. /staff"},
	},
	CreateArg: "username",
	Required:  []string{"username"},
	Defaults:  map[string]any{"enabled": true},
	Example: map[string]string{
		"list":   "  kcctl user list --search alice\n  kcctl user list --attr dept:sales --all -o json",
		"create": "  kcctl user create alice --email alice@example.com --first-name Alice --group /staff\n  kcctl user create bob --required-action UPDATE_PASSWORD",
		"update": "  kcctl user update alice --enabled=false\n  kcctl user update alice --attr dept=sales --attr legacy=",
		"get":    "  kcctl user get alice\n  kcctl user get 6b9f...-... -o yaml",
	},
}

func New(a *app.App) *cobra.Command {
	cmd := app.Group("user", "Manage users", "users")
	cmd.AddCommand(users.CRUD(a)...)
	comp := users.RefCompletion(a)
	cmd.AddCommand(
		count(a),
		enable(a, comp, true), enable(a, comp, false),
		setPassword(a, comp),
		executeActions(a, comp),
		sendVerifyEmail(a, comp),
		logout(a, comp),
		sessions(a, comp),
		credentials(a, comp),
		groups(a, comp),
		a.RoleMappingCmds("user", comp, func(ctx context.Context, c *kc.Client, ref string) (string, error) {
			id, err := app.Users.Find(ctx, c, ref)
			if err != nil {
				return "", err
			}
			return c.RealmPath("users", id, "role-mappings"), nil
		}),
		federated(a, comp),
		impersonate(a, comp),
		verifiableCredentials(a, comp),
	)
	return cmd
}

// userPath resolves a user ref to .../users/{id}/segs...
func userPath(ctx context.Context, a *app.App, ref string, segs ...string) (*kc.Client, string, error) {
	c, err := a.Client()
	if err != nil {
		return nil, "", err
	}
	id, err := app.Users.Find(ctx, c, ref)
	if err != nil {
		return nil, "", err
	}
	return c, c.RealmPath(append([]string{"users", id}, segs...)...), nil
}

func count(a *app.App) *cobra.Command {
	var search string
	cmd := &cobra.Command{
		Use: "count", Short: "Count users", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.Client()
			if err != nil {
				return err
			}
			q := url.Values{}
			if search != "" {
				q.Set("search", search)
			}
			v, err := c.Get(cmd.Context(), c.RealmPath("users", "count"), q)
			if err != nil {
				return err
			}
			return a.Printer.Print(v, nil)
		},
	}
	cmd.Flags().StringVar(&search, "search", "", "count only users matching this search")
	return cmd
}

func enable(a *app.App, comp cobra.CompletionFunc, on bool) *cobra.Command {
	verb := map[bool]string{true: "enable", false: "disable"}[on]
	return &cobra.Command{
		Use:               verb + " <user>...",
		Short:             map[bool]string{true: "Allow", false: "Block"}[on] + " sign-in for users",
		Args:              app.MinArgs(1, "<user>..."),
		ValidArgsFunction: comp,
		RunE: func(cmd *cobra.Command, args []string) error {
			for _, ref := range args {
				c, p, err := userPath(cmd.Context(), a, ref)
				if err != nil {
					return err
				}
				// Users PUT is a partial update, so only the flag is sent.
				if _, err := c.Put(cmd.Context(), p, map[string]any{"enabled": on}); err != nil {
					return err
				}
				a.Printer.Success("%sd user '%s'.", capitalize(verb), ref)
			}
			return nil
		},
	}
}

func capitalize(s string) string { return strings.ToUpper(s[:1]) + s[1:] }

func setPassword(a *app.App, comp cobra.CompletionFunc) *cobra.Command {
	var file string
	var temporary bool
	cmd := &cobra.Command{
		Use:   "set-password <user>",
		Short: "Set a user's password",
		Long: `Set a user's password. The password is read from --password-file, from
piped stdin, or from a hidden prompt; never from a flag value.`,
		Example: `  kcctl user set-password alice --temporary
  printf '%s' "$PW" | kcctl user set-password alice`,
		Args:              app.ExactArgs(1, "<user>"),
		ValidArgsFunction: app.NoMoreArgs(1, comp),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, p, err := userPath(cmd.Context(), a, args[0], "reset-password")
			if err != nil {
				return err
			}
			pw, err := a.ReadSecret(file, "New password for "+args[0])
			if err != nil {
				return err
			}
			if pw == "" {
				return errs.Usage("the password is empty; nothing was changed")
			}
			if _, err := c.Put(cmd.Context(), p, map[string]any{"type": "password", "value": pw, "temporary": temporary}); err != nil {
				return err
			}
			msg := "Set password for '%s'."
			if temporary {
				msg = "Set a temporary password for '%s'; they must change it at next sign-in."
			}
			a.Printer.Success(msg, args[0])
			return nil
		},
	}
	cmd.Flags().StringVar(&file, "password-file", "", "read the password from this file ('-' for stdin)")
	cmd.Flags().BoolVar(&temporary, "temporary", false, "require a change at next sign-in")
	return cmd
}

var requiredActions = []string{"UPDATE_PASSWORD", "VERIFY_EMAIL", "UPDATE_PROFILE", "CONFIGURE_TOTP", "TERMS_AND_CONDITIONS", "webauthn-register", "webauthn-register-passwordless", "delete_account", "update_user_locale", "UPDATE_EMAIL", "CONFIGURE_RECOVERY_AUTHN_CODES", "delete_credential", "idp_link"}

func executeActions(a *app.App, comp cobra.CompletionFunc) *cobra.Command {
	var lifespan int
	var clientID, redirect string
	cmd := &cobra.Command{
		Use:     "send-actions-email <user> <action>...",
		Aliases: []string{"execute-actions-email"},
		Short:   "Email the user a link to perform required actions",
		Example: "  kcctl user send-actions-email alice UPDATE_PASSWORD VERIFY_EMAIL --lifespan 3600",
		Args:    app.MinArgs(2, "<user> <action>..."),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, tc string) ([]string, cobra.ShellCompDirective) {
			if len(args) == 0 {
				return comp(cmd, args, tc)
			}
			return requiredActions, cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			c, p, err := userPath(cmd.Context(), a, args[0], "execute-actions-email")
			if err != nil {
				return err
			}
			q := url.Values{}
			if lifespan > 0 {
				q.Set("lifespan", strconv.Itoa(lifespan))
			}
			if clientID != "" {
				q.Set("client_id", clientID)
			}
			if redirect != "" {
				q.Set("redirect_uri", redirect)
			}
			actions := make([]any, 0, len(args)-1)
			for _, x := range args[1:] {
				actions = append(actions, x)
			}
			if _, err := c.Do(cmd.Context(), kc.Request{Method: "PUT", Path: p, Query: q, Body: actions}); err != nil {
				return err
			}
			a.Printer.Success("Sent an actions email to '%s'.", args[0])
			return nil
		},
	}
	cmd.Flags().IntVar(&lifespan, "lifespan", 0, "link validity in seconds (server default: 12h)")
	cmd.Flags().StringVar(&clientID, "link-client", "", "client the link returns to (with --redirect-uri)")
	cmd.Flags().StringVar(&redirect, "redirect-uri", "", "where to send the user afterwards")
	cmd.RegisterFlagCompletionFunc("link-client", a.ClientCompleter())
	return cmd
}

func sendVerifyEmail(a *app.App, comp cobra.CompletionFunc) *cobra.Command {
	return &cobra.Command{
		Use: "send-verify-email <user>", Short: "Email the user an address-verification link",
		Args: app.ExactArgs(1, "<user>"), ValidArgsFunction: app.NoMoreArgs(1, comp),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, p, err := userPath(cmd.Context(), a, args[0], "send-verify-email")
			if err != nil {
				return err
			}
			if _, err := c.Put(cmd.Context(), p, nil); err != nil {
				return err
			}
			a.Printer.Success("Sent a verification email to '%s'.", args[0])
			return nil
		},
	}
}

func logout(a *app.App, comp cobra.CompletionFunc) *cobra.Command {
	return &cobra.Command{
		Use: "logout <user>", Short: "End all of the user's sessions",
		Args: app.ExactArgs(1, "<user>"), ValidArgsFunction: app.NoMoreArgs(1, comp),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, p, err := userPath(cmd.Context(), a, args[0], "logout")
			if err != nil {
				return err
			}
			if _, err := c.Post(cmd.Context(), p, nil); err != nil {
				return err
			}
			a.Printer.Success("Ended all sessions of '%s'.", args[0])
			return nil
		},
	}
}

func sessions(a *app.App, comp cobra.CompletionFunc) *cobra.Command {
	var offline string
	cmd := &cobra.Command{
		Use: "sessions <user>", Short: "List the user's active sessions",
		Args: app.ExactArgs(1, "<user>"), ValidArgsFunction: app.NoMoreArgs(1, comp),
		RunE: func(cmd *cobra.Command, args []string) error {
			segs := []string{"sessions"}
			if offline != "" {
				segs = []string{"offline-sessions", offline}
			}
			ctx := cmd.Context()
			c, err := a.Client()
			if err != nil {
				return err
			}
			id, err := app.Users.Find(ctx, c, args[0])
			if err != nil {
				return err
			}
			if offline != "" {
				cid, err := app.ClientUUID(ctx, c, offline)
				if err != nil {
					return err
				}
				segs[1] = cid
			}
			v, err := c.Get(ctx, c.RealmPath(append([]string{"users", id}, segs...)...), nil)
			if err != nil {
				return err
			}
			return a.Printer.Print(v, []output.Column{
				output.Col("id", ".id"), output.Col("ip", ".ipAddress"),
				output.Col("started", ".start"), output.Col("last access", ".lastAccess"),
				output.Col("clients", ".clients.*"),
			})
		},
	}
	cmd.Flags().StringVar(&offline, "offline", "", "list offline sessions for this client instead")
	cmd.RegisterFlagCompletionFunc("offline", a.ClientCompleter())
	return cmd
}

func credentials(a *app.App, comp cobra.CompletionFunc) *cobra.Command {
	cmd := app.Group("credential", "Inspect and remove the user's credentials", "credentials", "cred")
	cmd.AddCommand(
		&cobra.Command{
			Use: "list <user>", Aliases: []string{"ls"}, Short: "List credentials (passwords, OTP, passkeys)",
			Args: app.ExactArgs(1, "<user>"), ValidArgsFunction: app.NoMoreArgs(1, comp),
			RunE: func(cmd *cobra.Command, args []string) error {
				c, p, err := userPath(cmd.Context(), a, args[0], "credentials")
				if err != nil {
					return err
				}
				v, err := c.Get(cmd.Context(), p, nil)
				if err != nil {
					return err
				}
				return a.Printer.Print(v, []output.Column{
					output.Col("id", ".id"), output.Col("type", ".type"),
					output.Col("label", ".userLabel"), output.Col("created", ".createdDate"),
				})
			},
		},
		&cobra.Command{
			Use: "delete <user> <credential-id>...", Aliases: []string{"rm"}, Short: "Remove credentials",
			Args: app.MinArgs(2, "<user> <credential-id>..."),
			ValidArgsFunction: func(cmd *cobra.Command, args []string, tc string) ([]string, cobra.ShellCompDirective) {
				if len(args) == 0 {
					return comp(cmd, args, tc)
				}
				return a.Fetched("credentials", func(ctx context.Context, c *kc.Client, args []string, _ string) ([]app.Item, error) {
					id, err := app.Users.Find(ctx, c, args[0])
					if err != nil {
						return nil, err
					}
					v, err := c.Get(ctx, c.RealmPath("users", id, "credentials"), nil)
					if err != nil {
						return nil, err
					}
					return app.ItemsFrom(v, "id", "type"), nil
				})(cmd, args, tc)
			},
			RunE: func(cmd *cobra.Command, args []string) error {
				c, p, err := userPath(cmd.Context(), a, args[0], "credentials")
				if err != nil {
					return err
				}
				for _, id := range args[1:] {
					if _, err := c.Delete(cmd.Context(), p+"/"+url.PathEscape(id), nil); err != nil {
						return err
					}
				}
				a.Printer.Success("Removed %d credential(s) from '%s'.", len(args)-1, args[0])
				return nil
			},
		},
	)
	return cmd
}

func groups(a *app.App, comp cobra.CompletionFunc) *cobra.Command {
	cmd := app.Group("group", "Manage the user's group memberships", "groups")
	userThenGroups := func(cmd *cobra.Command, args []string, tc string) ([]string, cobra.ShellCompDirective) {
		if len(args) == 0 {
			return comp(cmd, args, tc)
		}
		return a.GroupCompleter()(cmd, args, tc)
	}
	change := func(verb string, del bool) *cobra.Command {
		return &cobra.Command{
			Use:               verb + " <user> <group>...",
			Short:             map[bool]string{false: "Add the user to groups", true: "Remove the user from groups"}[del],
			Args:              app.MinArgs(2, "<user> <group>..."),
			ValidArgsFunction: userThenGroups,
			RunE: func(cmd *cobra.Command, args []string) error {
				ctx := cmd.Context()
				c, p, err := userPath(ctx, a, args[0], "groups")
				if err != nil {
					return err
				}
				for _, g := range args[1:] {
					gid, err := app.FindGroup(ctx, c, g)
					if err != nil {
						return err
					}
					if del {
						_, err = c.Delete(ctx, p+"/"+gid, nil)
					} else {
						_, err = c.Put(ctx, p+"/"+gid, nil)
					}
					if err != nil {
						return err
					}
				}
				a.Printer.Success("%s '%s' %s %d group(s).", map[bool]string{false: "Added", true: "Removed"}[del],
					args[0], map[bool]string{false: "to", true: "from"}[del], len(args)-1)
				return nil
			},
		}
	}
	cmd.AddCommand(
		&cobra.Command{
			Use: "list <user>", Aliases: []string{"ls"}, Short: "List the user's groups",
			Args: app.ExactArgs(1, "<user>"), ValidArgsFunction: app.NoMoreArgs(1, comp),
			RunE: func(cmd *cobra.Command, args []string) error {
				c, p, err := userPath(cmd.Context(), a, args[0], "groups")
				if err != nil {
					return err
				}
				v, err := c.Get(cmd.Context(), p, nil)
				if err != nil {
					return err
				}
				return a.Printer.Print(v, []output.Column{output.Col("path", ".path"), output.Col("id", ".id")})
			},
		},
		change("add", false), change("remove", true),
	)
	return cmd
}

func federated(a *app.App, comp cobra.CompletionFunc) *cobra.Command {
	cmd := app.Group("idp-link", "Manage the user's identity-provider links", "federated-identity")
	idpComp := a.Fetched("identity providers", func(ctx context.Context, c *kc.Client, _ []string, _ string) ([]app.Item, error) {
		v, err := c.Get(ctx, c.RealmPath("identity-provider", "instances"), url.Values{"briefRepresentation": {"true"}})
		if err != nil {
			return nil, err
		}
		return app.ItemsFrom(v, "alias", "displayName"), nil
	})
	userThenIdP := func(cmd *cobra.Command, args []string, tc string) ([]string, cobra.ShellCompDirective) {
		if len(args) == 0 {
			return comp(cmd, args, tc)
		}
		if len(args) == 1 {
			return idpComp(cmd, args, tc)
		}
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var idpUserID, idpUsername string
	add := &cobra.Command{
		Use: "add <user> <idp-alias>", Short: "Link the user to an account at an identity provider",
		Args: app.ExactArgs(2, "<user> <idp-alias>"), ValidArgsFunction: userThenIdP,
		RunE: func(cmd *cobra.Command, args []string) error {
			if idpUserID == "" {
				return errs.Usage("--idp-user-id is required")
			}
			c, p, err := userPath(cmd.Context(), a, args[0], "federated-identity", args[1])
			if err != nil {
				return err
			}
			body := map[string]any{"identityProvider": args[1], "userId": idpUserID, "userName": idpUsername}
			if _, err := c.Post(cmd.Context(), p, body); err != nil {
				return err
			}
			a.Printer.Success("Linked '%s' to %s.", args[0], args[1])
			return nil
		},
	}
	add.Flags().StringVar(&idpUserID, "idp-user-id", "", "the user's id at the identity provider")
	add.Flags().StringVar(&idpUsername, "idp-username", "", "the user's name at the identity provider")
	cmd.AddCommand(
		&cobra.Command{
			Use: "list <user>", Aliases: []string{"ls"}, Short: "List the user's identity-provider links",
			Args: app.ExactArgs(1, "<user>"), ValidArgsFunction: app.NoMoreArgs(1, comp),
			RunE: func(cmd *cobra.Command, args []string) error {
				c, p, err := userPath(cmd.Context(), a, args[0], "federated-identity")
				if err != nil {
					return err
				}
				v, err := c.Get(cmd.Context(), p, nil)
				if err != nil {
					return err
				}
				return a.Printer.Print(v, []output.Column{
					output.Col("provider", ".identityProvider"), output.Col("user id", ".userId"), output.Col("username", ".userName"),
				})
			},
		},
		add,
		&cobra.Command{
			Use: "remove <user> <idp-alias>", Short: "Remove an identity-provider link",
			Args: app.ExactArgs(2, "<user> <idp-alias>"), ValidArgsFunction: userThenIdP,
			RunE: func(cmd *cobra.Command, args []string) error {
				c, p, err := userPath(cmd.Context(), a, args[0], "federated-identity", args[1])
				if err != nil {
					return err
				}
				if _, err := c.Delete(cmd.Context(), p, nil); err != nil {
					return err
				}
				a.Printer.Success("Unlinked '%s' from %s.", args[0], args[1])
				return nil
			},
		},
	)
	return cmd
}

func impersonate(a *app.App, comp cobra.CompletionFunc) *cobra.Command {
	return &cobra.Command{
		Use:               "impersonate <user>",
		Short:             "Start an impersonation session (needs the impersonation feature and role)",
		Args:              app.ExactArgs(1, "<user>"),
		ValidArgsFunction: app.NoMoreArgs(1, comp),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, p, err := userPath(cmd.Context(), a, args[0], "impersonation")
			if err != nil {
				return err
			}
			resp, err := c.Post(cmd.Context(), p, nil)
			if err != nil {
				return err
			}
			v, err := resp.JSON()
			if err != nil {
				return err
			}
			return a.Printer.Print(v, nil)
		},
	}
}

// verifiableCredentials exists from 26.8; on older servers it explains why
// it can't run instead of returning a bare 404.
func verifiableCredentials(a *app.App, comp cobra.CompletionFunc) *cobra.Command {
	return &cobra.Command{
		Use:               "vc <user>",
		Short:             "List the user's verifiable credentials (Keycloak 26.8+)",
		Args:              app.ExactArgs(1, "<user>"),
		ValidArgsFunction: app.NoMoreArgs(1, comp),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.Require(cmd.Context(), compat.UserVerifiableCredentials); err != nil {
				return err
			}
			c, p, err := userPath(cmd.Context(), a, args[0], "vc", "credentials")
			if err != nil {
				return err
			}
			v, err := c.Get(cmd.Context(), p, nil)
			if err != nil {
				return err
			}
			return a.Printer.Print(v, nil)
		},
	}
}
