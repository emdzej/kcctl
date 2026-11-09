// Package clientcmd manages clients and what hangs off a client: secrets,
// service account, client roles, protocol mappers, assigned client scopes,
// role scope mappings, adapter installation files, sessions and token
// evaluation.
package clientcmd

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
	output.Col("client id", ".clientId"), output.Col("name", ".name"),
	output.Col("enabled", ".enabled"), output.Col("public", ".publicClient"),
	output.Col("protocol", ".protocol"), output.Col("id", ".id"),
}

var clients = &app.Resource{
	Noun: "client", Plural: "clients", Lookup: app.Clients, Columns: columns, Paged: true,
	ListFlags: func(cmd *cobra.Command) func(url.Values) {
		f := cmd.Flags()
		// --client-id is a global flag (the sign-in client), hence --search.
		search := f.String("search", "", "only clients whose clientId contains this (case-insensitive)")
		attr := f.StringArray("attr", nil, "filter by attribute key:value (repeatable)")
		return func(q url.Values) {
			if *search != "" {
				q.Set("clientId", *search)
				q.Set("search", "true")
			}
			if len(*attr) > 0 {
				q.Set("q", strings.Join(*attr, " "))
			}
		}
	},
	Fields: app.Fields{
		{Flag: "name", Usage: "display name"},
		{Flag: "description", Usage: "description"},
		{Flag: "enabled", Kind: app.Bool, Usage: "whether the client can be used"},
		{Flag: "public", Path: "publicClient", Kind: app.Bool, Usage: "public client (no secret)"},
		{Flag: "protocol", Usage: "openid-connect|saml"},
		{Flag: "root-url", Path: "rootUrl", Usage: "root URL"},
		{Flag: "base-url", Path: "baseUrl", Usage: "base URL"},
		{Flag: "redirect-uri", Path: "redirectUris", Kind: app.Strings, Usage: "valid redirect URIs (replaces the list)"},
		{Flag: "web-origin", Path: "webOrigins", Kind: app.Strings, Usage: "allowed web origins (replaces the list)"},
		{Flag: "service-accounts", Path: "serviceAccountsEnabled", Kind: app.Bool, Usage: "enable the client-credentials grant"},
		{Flag: "standard-flow", Path: "standardFlowEnabled", Kind: app.Bool, Usage: "enable the authorization-code flow"},
		{Flag: "direct-access", Path: "directAccessGrantsEnabled", Kind: app.Bool, Usage: "enable the password grant"},
		{Flag: "implicit-flow", Path: "implicitFlowEnabled", Kind: app.Bool, Usage: "enable the implicit flow"},
		{Flag: "attr", Path: "attributes", Kind: app.KVString, Usage: "set a client attribute"},
	},
	CreateArg: "clientId",
	Required:  []string{"clientId"},
	Example: map[string]string{
		"list":   "  kcctl client list --search web\n  kcctl client list --all -o json",
		"create": "  kcctl client create web --redirect-uri 'https://app.example.com/*' --web-origin +\n  kcctl client create api --service-accounts --standard-flow=false",
		"update": "  kcctl client update web --redirect-uri https://a/*,https://b/*\n  kcctl client update web --attr pkce.code.challenge.method=S256",
		"get":    "  kcctl client get web -o yaml",
	},
}

func New(a *app.App) *cobra.Command {
	cmd := app.Group("client", "Manage clients", "clients")
	cmd.AddCommand(clients.CRUD(a)...)
	comp := clients.RefCompletion(a)
	one := app.NoMoreArgs(1, comp)

	mappers := a.ProtocolMapperCmds("client", comp, func(ctx context.Context, c *kc.Client, ref string) (string, error) {
		id, err := app.ClientUUID(ctx, c, ref)
		if err != nil {
			return "", err
		}
		return c.RealmPath("clients", id, "protocol-mappers"), nil
	})
	scopeMappings := a.RoleMappingCmds("client", comp, func(ctx context.Context, c *kc.Client, ref string) (string, error) {
		id, err := app.ClientUUID(ctx, c, ref)
		if err != nil {
			return "", err
		}
		return c.RealmPath("clients", id, "scope-mappings"), nil
	})
	scopeMappings.Use = "scope-mapping"
	scopeMappings.Aliases = []string{"scope-mappings"}
	scopeMappings.Short = "Manage which roles the client's tokens may carry (role scope mappings)"

	cmd.AddCommand(
		enable(a, comp, true), enable(a, comp, false),
		secret(a, one),
		serviceAccount(a, one),
		roles(a),
		mappers,
		scopes(a, comp, one),
		scopeMappings,
		installation(a, comp),
		sessions(a, one),
		evaluate(a, one),
	)
	return cmd
}

// clientPath resolves a client ref to .../clients/{uuid}/segs...
func clientPath(ctx context.Context, a *app.App, ref string, segs ...string) (*kc.Client, string, error) {
	c, err := a.Client()
	if err != nil {
		return nil, "", err
	}
	id, err := app.ClientUUID(ctx, c, ref)
	if err != nil {
		return nil, "", err
	}
	return c, c.RealmPath(append([]string{"clients", id}, segs...)...), nil
}

func enable(a *app.App, comp cobra.CompletionFunc, on bool) *cobra.Command {
	verb := map[bool]string{true: "enable", false: "disable"}[on]
	return &cobra.Command{
		Use:               verb + " <client>...",
		Short:             strings.ToUpper(verb[:1]) + verb[1:] + " clients",
		Args:              app.MinArgs(1, "<client>..."),
		ValidArgsFunction: comp,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			for _, ref := range args {
				c, p, err := clientPath(ctx, a, ref)
				if err != nil {
					return err
				}
				// The client PUT validates the whole representation, so send
				// the current one back with only the flag changed.
				cur, err := c.Get(ctx, p, nil)
				if err != nil {
					return err
				}
				b, _ := cur.(map[string]any)
				if b == nil {
					b = map[string]any{}
				}
				b["enabled"] = on
				if _, err := c.Put(ctx, p, b); err != nil {
					return err
				}
				a.Printer.Success("%sd client '%s'.", strings.ToUpper(verb[:1])+verb[1:], ref)
			}
			return nil
		},
	}
}

// printSecret writes the secret value alone to stdout so it can be captured;
// structured output gets the whole CredentialRepresentation.
func printSecret(a *app.App, v any) error {
	if a.Printer.IsStructured() {
		return a.Printer.Print(v, nil)
	}
	m, _ := v.(map[string]any)
	s, _ := m["value"].(string)
	return a.Printer.Print(s, nil)
}

func secret(a *app.App, one cobra.CompletionFunc) *cobra.Command {
	cmd := app.Group("secret", "Show or regenerate a confidential client's secret", "client-secret")
	get := &cobra.Command{
		Use: "get <client>", Short: "Print the client secret",
		Args: app.ExactArgs(1, "<client>"), ValidArgsFunction: one,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, p, err := clientPath(cmd.Context(), a, args[0], "client-secret")
			if err != nil {
				return err
			}
			v, err := c.Get(cmd.Context(), p, nil)
			if err != nil {
				return err
			}
			return printSecret(a, v)
		},
	}
	regen := &cobra.Command{
		Use:   "regenerate <client>",
		Short: "Replace the client secret with a new one and print it",
		Long: `Replace the client secret with a new random one and print it on stdout.
The old secret stops working at once unless client-secret rotation is
configured by a client policy.`,
		Example: "  NEW=$(kcctl client secret regenerate api)",
		Args:    app.ExactArgs(1, "<client>"), ValidArgsFunction: one,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, p, err := clientPath(cmd.Context(), a, args[0], "client-secret")
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
			if err := printSecret(a, v); err != nil {
				return err
			}
			a.Printer.Success("Regenerated the secret of client '%s'.", args[0])
			return nil
		},
	}
	rotated := app.Group("rotated", "Show or drop the previous secret kept by secret rotation")
	rotated.AddCommand(
		&cobra.Command{
			Use: "get <client>", Short: "Print the rotated (previous) secret",
			Args: app.ExactArgs(1, "<client>"), ValidArgsFunction: one,
			RunE: func(cmd *cobra.Command, args []string) error {
				c, p, err := clientPath(cmd.Context(), a, args[0], "client-secret", "rotated")
				if err != nil {
					return err
				}
				v, err := c.Get(cmd.Context(), p, nil)
				if err != nil {
					return err
				}
				return printSecret(a, v)
			},
		},
		&cobra.Command{
			Use: "delete <client>", Aliases: []string{"rm"}, Short: "Invalidate the rotated secret now",
			Args: app.ExactArgs(1, "<client>"), ValidArgsFunction: one,
			RunE: func(cmd *cobra.Command, args []string) error {
				c, p, err := clientPath(cmd.Context(), a, args[0], "client-secret", "rotated")
				if err != nil {
					return err
				}
				if _, err := c.Delete(cmd.Context(), p, nil); err != nil {
					return err
				}
				a.Printer.Success("Invalidated the rotated secret of client '%s'.", args[0])
				return nil
			},
		},
	)
	cmd.AddCommand(get, regen, rotated)
	return cmd
}

var userColumns = []output.Column{
	output.Col("username", ".username"), output.Col("enabled", ".enabled"), output.Col("id", ".id"),
}

func serviceAccount(a *app.App, one cobra.CompletionFunc) *cobra.Command {
	return &cobra.Command{
		Use:               "service-account <client>",
		Short:             "Show the client's service-account user",
		Args:              app.ExactArgs(1, "<client>"),
		ValidArgsFunction: one,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			byField, err := a.Has(ctx, compat.ServiceAccountUserIDField)
			if err != nil {
				return err
			}
			c, p, err := clientPath(ctx, a, args[0])
			if err != nil {
				return err
			}
			if !byField {
				v, err := c.Get(ctx, p+"/service-account-user", nil)
				if err != nil {
					return err
				}
				return a.Printer.Print(v, userColumns)
			}
			cur, err := c.Get(ctx, p, nil)
			if err != nil {
				return err
			}
			m, _ := cur.(map[string]any)
			uid, _ := m["serviceAccountUserId"].(string)
			if uid == "" {
				return errs.Usage("client '%s' has no service account", args[0]).
					WithHint("Enable it with 'kcctl client update " + args[0] + " --service-accounts'.")
			}
			v, err := c.Get(ctx, c.RealmPath("users", uid), nil)
			if err != nil {
				return err
			}
			return a.Printer.Print(v, userColumns)
		},
	}
}

func roles(a *app.App) *cobra.Command {
	var client string
	r := &app.Resource{
		Noun: "client role", Plural: "client roles",
		Lookup: app.Lookup{
			What: "client role", Path: app.ClientPath(&client, "roles"), NameField: "name",
			Search: func(p string) url.Values {
				return url.Values{"search": {p}, "max": {"100"}, "briefRepresentation": {"true"}}
			},
		},
		KeyByName: true, Columns: app.RoleColumns, Paged: true,
		ListFlags: func(cmd *cobra.Command) func(url.Values) {
			search := cmd.Flags().String("search", "", "only roles whose name contains this")
			full := cmd.Flags().Bool("full", false, "return full representations (with attributes)")
			return func(q url.Values) {
				if *search != "" {
					q.Set("search", *search)
				}
				if *full {
					q.Set("briefRepresentation", "false")
				}
			}
		},
		Fields: app.Fields{
			{Flag: "description", Usage: "description"},
			{Flag: "attr", Path: "attributes", Kind: app.KV, Usage: "set a role attribute; empty value removes it"},
		},
		CreateArg: "name",
		Required:  []string{"name"},
		Example: map[string]string{
			"create": "  kcctl client role create --client api reader --description 'Read access'",
			"list":   "  kcctl client role list --client api",
		},
	}
	g := app.Group("role", "Manage a client's roles (needs --client)", "roles")
	g.PersistentFlags().StringVar(&client, "client", "", "client whose roles to manage (clientId or id)")
	g.RegisterFlagCompletionFunc("client", a.ClientCompleter())
	g.AddCommand(r.CRUD(a)...)
	g.AddCommand(composites(a, &client))
	return g
}

// composites manages roles/{name}/composites. Children are realm roles, or
// roles of --from-client.
func composites(a *app.App, client *string) *cobra.Command {
	g := app.Group("composite", "Manage the roles a client role includes", "composites")
	var from string
	g.PersistentFlags().StringVar(&from, "from-client", "", "child roles belong to this client instead of the realm")
	g.RegisterFlagCompletionFunc("from-client", a.ClientCompleter())

	rolePath := func(ctx context.Context, c *kc.Client, name string) (string, error) {
		base, err := app.ClientPath(client, "roles")(ctx, c)
		if err != nil {
			return "", err
		}
		return base + "/" + url.PathEscape(name) + "/composites", nil
	}
	parentComp := a.ClientRoleCompleter(client)
	childArgs := func(cmd *cobra.Command, args []string, tc string) ([]string, cobra.ShellCompDirective) {
		if len(args) == 0 {
			return parentComp(cmd, args, tc)
		}
		if from != "" {
			return a.ClientRoleCompleter(&from)(cmd, args, tc)
		}
		return a.RealmRoleCompleter()(cmd, args, tc)
	}

	list := &cobra.Command{
		Use: "list <role>", Aliases: []string{"ls"}, Short: "List the roles a client role includes",
		Args: app.ExactArgs(1, "<role>"), ValidArgsFunction: app.NoMoreArgs(1, parentComp),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.Client()
			if err != nil {
				return err
			}
			p, err := rolePath(cmd.Context(), c, args[0])
			if err != nil {
				return err
			}
			v, err := c.Get(cmd.Context(), p, nil)
			if err != nil {
				return err
			}
			return a.Printer.Print(v, app.RoleColumns)
		},
	}
	change := func(verb string, del bool) *cobra.Command {
		return &cobra.Command{
			Use:               verb + " <role> <child-role>...",
			Short:             map[bool]string{false: "Make a client role include other roles", true: "Stop a client role including other roles"}[del],
			Example:           "  kcctl client role composite " + verb + " --client api admin reader writer\n  kcctl client role composite " + verb + " --client api admin --from-client other viewer",
			Args:              app.MinArgs(2, "<role> <child-role>..."),
			ValidArgsFunction: childArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				ctx := cmd.Context()
				c, err := a.Client()
				if err != nil {
					return err
				}
				p, err := rolePath(ctx, c, args[0])
				if err != nil {
					return err
				}
				children, err := app.ResolveRoles(ctx, c, from, args[1:])
				if err != nil {
					return err
				}
				if del {
					_, err = c.Delete(ctx, p, children)
				} else {
					_, err = c.Post(ctx, p, children)
				}
				if err != nil {
					return err
				}
				a.Printer.Success("%s %d role(s) %s composite '%s'.", map[bool]string{false: "Added", true: "Removed"}[del],
					len(children), map[bool]string{false: "to", true: "from"}[del], args[0])
				return nil
			},
		}
	}
	g.AddCommand(list, change("add", false), change("remove", true))
	return g
}

var scopeColumns = []output.Column{
	output.Col("name", ".name"), output.Col("type", ".type"),
	output.Col("protocol", ".protocol"), output.Col("id", ".id"),
}

func scopeKind(optional bool) string {
	if optional {
		return "optional-client-scopes"
	}
	return "default-client-scopes"
}

// assignedScopes returns default and optional scopes, each tagged with its
// type, so one table shows both.
func assignedScopes(ctx context.Context, c *kc.Client, p string) ([]any, error) {
	var out []any
	for _, kind := range []string{"default", "optional"} {
		v, err := c.Get(ctx, p+"/"+kind+"-client-scopes", nil)
		if err != nil {
			return nil, err
		}
		l, _ := v.([]any)
		for _, x := range l {
			if m, ok := x.(map[string]any); ok {
				m["type"] = kind
				out = append(out, m)
			}
		}
	}
	if out == nil {
		out = []any{}
	}
	return out, nil
}

func scopes(a *app.App, comp, one cobra.CompletionFunc) *cobra.Command {
	g := app.Group("scope", "Manage the client scopes assigned to a client", "scopes", "client-scope")
	scopeArgs := func(cmd *cobra.Command, args []string, tc string) ([]string, cobra.ShellCompDirective) {
		if len(args) == 0 {
			return comp(cmd, args, tc)
		}
		return a.ClientScopeCompleter()(cmd, args, tc)
	}
	list := &cobra.Command{
		Use: "list <client>", Aliases: []string{"ls"}, Short: "List default and optional client scopes",
		Args: app.ExactArgs(1, "<client>"), ValidArgsFunction: one,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, p, err := clientPath(cmd.Context(), a, args[0])
			if err != nil {
				return err
			}
			v, err := assignedScopes(cmd.Context(), c, p)
			if err != nil {
				return err
			}
			return a.Printer.Print(v, scopeColumns)
		},
	}
	available := &cobra.Command{
		Use: "available <client>", Short: "List client scopes that could still be assigned",
		Args: app.ExactArgs(1, "<client>"), ValidArgsFunction: one,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			c, p, err := clientPath(ctx, a, args[0])
			if err != nil {
				return err
			}
			v, err := availableScopes(ctx, a, c, p)
			if err != nil {
				return err
			}
			return a.Printer.Print(v, []output.Column{
				output.Col("name", ".name"), output.Col("protocol", ".protocol"),
				output.Col("description", ".description"), output.Col("id", ".id"),
			})
		},
	}
	change := func(verb string, del bool) *cobra.Command {
		var optional bool
		cmd := &cobra.Command{
			Use:               verb + " <client> <scope>...",
			Short:             map[bool]string{false: "Assign client scopes (default unless --optional)", true: "Unassign client scopes"}[del],
			Example:           "  kcctl client scope " + verb + " web profile email\n  kcctl client scope " + verb + " web offline_access --optional",
			Args:              app.MinArgs(2, "<client> <scope>..."),
			ValidArgsFunction: scopeArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				ctx := cmd.Context()
				c, p, err := clientPath(ctx, a, args[0], scopeKind(optional))
				if err != nil {
					return err
				}
				for _, ref := range args[1:] {
					id, err := app.ClientScopes.Find(ctx, c, ref)
					if err != nil {
						return err
					}
					ip := p + "/" + url.PathEscape(id)
					if del {
						_, err = c.Delete(ctx, ip, nil)
					} else {
						_, err = c.Put(ctx, ip, nil)
					}
					if err != nil {
						return err
					}
				}
				kind := map[bool]string{false: "default", true: "optional"}[optional]
				if del {
					a.Printer.Success("Removed %d client scope(s) from client '%s'.", len(args)-1, args[0])
				} else {
					a.Printer.Success("Added %d %s client scope(s) to client '%s'.", len(args)-1, kind, args[0])
				}
				return nil
			},
		}
		usage := "assign as optional (requested with the scope parameter) instead of default"
		if del {
			// Keycloak removes the scope whichever list it is in; the flag
			// only picks the endpoint.
			usage = "use the optional-client-scopes endpoint (either removes the scope)"
		}
		cmd.Flags().BoolVar(&optional, "optional", false, usage)
		return cmd
	}
	g.AddCommand(list, available, change("add", false), change("remove", true))
	return g
}

// availableScopes lists realm client scopes of the client's protocol that are
// not yet assigned. 27+ has an endpoint that needs only manage-client (it
// returns every realm scope, so it is filtered the same way); older servers
// need view access to the realm's client-scopes.
func availableScopes(ctx context.Context, a *app.App, c *kc.Client, p string) ([]any, error) {
	has, err := a.Has(ctx, compat.AvailableClientScopes)
	if err != nil {
		return nil, err
	}
	src := c.RealmPath("client-scopes")
	if has {
		src = p + "/available-client-scopes"
	}
	cur, err := c.Get(ctx, p, nil)
	if err != nil {
		return nil, err
	}
	cm, _ := cur.(map[string]any)
	protocol, _ := cm["protocol"].(string)
	if protocol == "" {
		protocol = "openid-connect"
	}
	assigned, err := assignedScopes(ctx, c, p)
	if err != nil {
		return nil, err
	}
	taken := map[string]bool{}
	for _, x := range assigned {
		m, _ := x.(map[string]any)
		id, _ := m["id"].(string)
		taken[id] = true
	}
	v, err := c.Get(ctx, src, nil)
	if err != nil {
		return nil, err
	}
	all, _ := v.([]any)
	out := []any{}
	for _, x := range all {
		m, _ := x.(map[string]any)
		id, _ := m["id"].(string)
		sp, _ := m["protocol"].(string)
		if sp == "" {
			sp = "openid-connect"
		}
		if taken[id] || sp != protocol {
			continue
		}
		out = append(out, m)
	}
	return out, nil
}

// installationProviders are the ClientInstallationProvider ids shipped with
// Keycloak (oidc, saml and docker protocols).
var installationProviders = []string{
	"keycloak-oidc-keycloak-json", "keycloak-oidc-jboss-subsystem", "keycloak-oidc-jboss-subsystem-cli",
	"keycloak-saml", "keycloak-saml-subsystem", "keycloak-saml-subsystem-cli", "saml-sp-descriptor", "mod-auth-mellon",
	"docker-v2-registry-config-file", "docker-v2-variable-override", "docker-v2-compose-yaml",
}

func installation(a *app.App, comp cobra.CompletionFunc) *cobra.Command {
	var provider string
	cmd := &cobra.Command{
		Use:   "installation <client>",
		Short: "Print an adapter configuration file for the client",
		Long: `Print an adapter configuration file for the client exactly as the server
generates it (JSON, XML or other formats depending on the provider).`,
		Example: `  kcctl client installation web > keycloak.json
  kcctl client installation sp --provider saml-sp-descriptor > sp.xml`,
		Args:              app.ExactArgs(1, "<client>"),
		ValidArgsFunction: app.NoMoreArgs(1, comp),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, p, err := clientPath(cmd.Context(), a, args[0], "installation", "providers", provider)
			if err != nil {
				return err
			}
			// Providers choose their own media type (XML for SAML), so accept any.
			resp, err := c.Do(cmd.Context(), kc.Request{Method: "GET", Path: p, Accept: "*/*"})
			if err != nil {
				return err
			}
			body := resp.Body
			if _, err := a.Out.Write(body); err != nil {
				return err
			}
			if len(body) > 0 && body[len(body)-1] != '\n' {
				_, err = a.Out.Write([]byte("\n"))
			}
			return err
		},
	}
	cmd.Flags().StringVar(&provider, "provider", "keycloak-oidc-keycloak-json", "installation provider id")
	cmd.RegisterFlagCompletionFunc("provider", app.Values(installationProviders...))
	return cmd
}

func sessions(a *app.App, one cobra.CompletionFunc) *cobra.Command {
	var offline, all bool
	var first, maxN int
	cmd := &cobra.Command{
		Use: "sessions <client>", Short: "List user sessions of the client",
		Args: app.ExactArgs(1, "<client>"), ValidArgsFunction: one,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			seg := "user-sessions"
			if offline {
				seg = "offline-sessions"
			}
			c, p, err := clientPath(ctx, a, args[0], seg)
			if err != nil {
				return err
			}
			cols := []output.Column{
				output.Col("username", ".username"), output.Col("ip", ".ipAddress"),
				output.Col("started", ".start"), output.Col("last access", ".lastAccess"),
				output.Col("id", ".id"),
			}
			q := url.Values{}
			if all {
				items, err := app.Paginate(ctx, c, p, q, first)
				if err != nil {
					return err
				}
				return a.Printer.Print(items, cols)
			}
			if cmd.Flags().Changed("first") {
				q.Set("first", strconv.Itoa(first))
			}
			if cmd.Flags().Changed("max") {
				q.Set("max", strconv.Itoa(maxN))
			}
			v, err := c.Get(ctx, p, q)
			if err != nil {
				return err
			}
			return a.Printer.Print(v, cols)
		},
	}
	cmd.Flags().BoolVar(&offline, "offline", false, "list offline sessions instead")
	cmd.Flags().IntVar(&first, "first", 0, "skip this many results")
	cmd.Flags().IntVar(&maxN, "max", 0, "return at most this many results (server default if unset)")
	cmd.Flags().BoolVar(&all, "all", false, "fetch every page")
	return cmd
}

// evaluate generates example tokens through evaluate-scopes, which shows what
// a user would get from this client without signing in as them.
func evaluate(a *app.App, one cobra.CompletionFunc) *cobra.Command {
	g := app.Group("evaluate", "Preview tokens the client would issue for a user")
	build := func(use, seg, short string, audience bool) *cobra.Command {
		var user, aud string
		var scope []string
		cmd := &cobra.Command{
			Use: use + " <client>", Short: short,
			Example: "  kcctl client evaluate " + use + " web --user alice --scope openid,email",
			Args:    app.ExactArgs(1, "<client>"), ValidArgsFunction: one,
			RunE: func(cmd *cobra.Command, args []string) error {
				if user == "" {
					return errs.Usage("--user is required")
				}
				ctx := cmd.Context()
				c, p, err := clientPath(ctx, a, args[0], "evaluate-scopes", seg)
				if err != nil {
					return err
				}
				uid, err := app.Users.Find(ctx, c, user)
				if err != nil {
					return err
				}
				q := url.Values{"userId": {uid}}
				if len(scope) > 0 {
					q.Set("scope", strings.Join(scope, " "))
				}
				if aud != "" {
					q.Set("audience", aud)
				}
				v, err := c.Get(ctx, p, q)
				if err != nil {
					return err
				}
				return a.Printer.Print(v, nil)
			},
		}
		cmd.Flags().StringVar(&user, "user", "", "user to issue the token for (username or id)")
		cmd.Flags().StringSliceVar(&scope, "scope", nil, "requested scopes (the OAuth scope parameter)")
		cmd.RegisterFlagCompletionFunc("user", a.UserCompleter())
		if audience {
			cmd.Flags().StringVar(&aud, "audience", "", "limit the token audience to this clientId")
			cmd.RegisterFlagCompletionFunc("audience", a.ClientCompleter())
		}
		return cmd
	}
	g.AddCommand(
		build("token", "generate-example-access-token", "Show an example access token", true),
		build("id-token", "generate-example-id-token", "Show an example ID token", true),
		build("userinfo", "generate-example-userinfo", "Show an example userinfo response", false),
	)
	return g
}
