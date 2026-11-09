// Package componentcmd manages realm components (keys, user storage, client
// registration policies, ...) and the user-storage (LDAP/Kerberos) actions.
package componentcmd

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/emdzej/keycloak-cli/internal/app"
	"github.com/emdzej/keycloak-cli/internal/errs"
	"github.com/emdzej/keycloak-cli/internal/kc"
	"github.com/emdzej/keycloak-cli/internal/output"
)

const userStorageType = "org.keycloak.storage.UserStorageProvider"

var columns = []output.Column{
	output.Col("name", ".name"), output.Col("provider", ".providerId"),
	output.Col("type", ".providerType"), output.Col("parent", ".parentId"),
	output.Col("id", ".id"),
}

// Components filter by exact name server-side; completion lists them all
// and filters by prefix locally.
var components = app.Lookup{
	What: "component", Path: app.RealmPath("components"), NameField: "name",
	Query:  func(n string) url.Values { return url.Values{"name": {n}} },
	Search: func(string) url.Values { return nil },
}

var storages = app.Lookup{
	What: "user storage provider", Path: app.RealmPath("components"), NameField: "name",
	Query:  func(n string) url.Values { return url.Values{"name": {n}, "type": {userStorageType}} },
	Search: func(string) url.Values { return url.Values{"type": {userStorageType}} },
}

func New(a *app.App) *cobra.Command {
	cmd := app.Group("component", "Manage realm components (keys, user storage, policies)", "components")
	r, bind := resource(a, components, "component", "components")
	cmd.AddCommand(r.ListCmd(a))
	cmd.AddCommand(writeCmds(a, r, bind)...)
	cmd.AddCommand(subTypes(a, r.RefCompletion(a)), UserStorage(a))
	return cmd
}

// resource builds the component Resource and the function that binds
// --bind-credential-file to its BeforeWrite on a create/update command.
func resource(a *app.App, l app.Lookup, noun, plural string) (*app.Resource, func(*cobra.Command)) {
	var bindFile string
	r := &app.Resource{
		Noun: noun, Plural: plural, Lookup: l, Columns: columns,
		ListFlags: func(cmd *cobra.Command) func(url.Values) {
			f := cmd.Flags()
			typ := f.String("type", "", "provider type, e.g. org.keycloak.keys.KeyProvider")
			parent := f.String("parent", "", "parent id (realm id or component id)")
			name := f.String("name", "", "exact component name")
			pid := f.String("provider-id", "", "provider id, e.g. ldap, rsa-generated")
			cmd.RegisterFlagCompletionFunc("type", componentTypes(a))
			return func(q url.Values) {
				for k, v := range map[string]string{"type": *typ, "parent": *parent, "name": *name, "providerId": *pid} {
					if v != "" {
						q.Set(k, v)
					}
				}
			}
		},
		Fields: app.Fields{
			{Flag: "name", Usage: "component name"},
			{Flag: "provider-id", Path: "providerId", Usage: "provider id, e.g. ldap, rsa-generated"},
			{Flag: "provider-type", Path: "providerType", Usage: "provider type, e.g. " + userStorageType},
			{Flag: "parent-id", Path: "parentId", Usage: "parent id (default: the realm)"},
			{Flag: "sub-type", Path: "subType", Usage: "sub type, e.g. authenticated|anonymous for client registration policies"},
			{Flag: "config", Path: "config", Kind: app.KV, Usage: "config entry; empty value removes it"},
		},
		CreateArg: "name",
		Required:  []string{"name", "providerId", "providerType"},
		BeforeWrite: func(_ context.Context, a *app.App, b map[string]any, _ bool) error {
			if bindFile == "" {
				return nil
			}
			s, err := a.ReadSecret(bindFile, "Bind credential")
			if err != nil {
				return err
			}
			if s == "" {
				return errs.Usage("the bind credential is empty; nothing was changed")
			}
			cfg, _ := b["config"].(map[string]any)
			if cfg == nil {
				cfg = map[string]any{}
				b["config"] = cfg
			}
			cfg["bindCredential"] = []any{s}
			return nil
		},
		Example: map[string]string{
			"list": "  kcctl component list --type org.keycloak.keys.KeyProvider",
			"create": `  kcctl component create corp-ldap --provider-id ldap --provider-type ` + userStorageType + ` \
    --config connectionUrl=ldaps://ldap.corp --config bindDn=cn=kc,dc=corp --config editMode=READ_ONLY \
    --bind-credential-file bind.txt`,
			"update": "  kcctl component update corp-ldap --config fullSyncPeriod=86400",
		},
	}
	bind := func(c *cobra.Command) {
		c.Flags().StringVar(&bindFile, "bind-credential-file", "", "read the LDAP bind credential from this file ('-' for stdin)")
	}
	return r, bind
}

func writeCmds(a *app.App, r *app.Resource, bind func(*cobra.Command)) []*cobra.Command {
	create, update := r.CreateCmd(a), r.UpdateCmd(a)
	// Rotating only the bind credential is a change by itself; the generic
	// update would call it "nothing to change". The placeholder is replaced
	// by BeforeWrite. Wrapped before guardSecrets, which then sees only what
	// the user typed.
	run := update.RunE
	update.RunE = func(cmd *cobra.Command, args []string) error {
		if cmd.Flags().Changed("bind-credential-file") && !cmd.Flags().Changed("set") && !cmd.Flags().Changed("file") {
			if err := cmd.Flags().Set("set", `config.bindCredential=[""]`); err != nil {
				return err
			}
		}
		return run(cmd, args)
	}
	for _, c := range []*cobra.Command{create, update} {
		bind(c)
		guardSecrets(c)
		c.RegisterFlagCompletionFunc("provider-type", componentTypes(a))
	}
	update.Long = `Change a component. Secret config values come back masked ("**********") and
Keycloak keeps the stored value when the masked value is sent back, so they
survive an update. Keycloak asks for the bind credential again when the LDAP
connection URL or bind DN changes; pass --bind-credential-file then.`
	return []*cobra.Command{r.GetCmd(a), create, update, r.DeleteCmd(a)}
}

// guardSecrets keeps credentials off the command line: --config and --set
// may not name a secret-looking key.
func guardSecrets(cmd *cobra.Command) {
	run := cmd.RunE
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		kvs, _ := cmd.Flags().GetStringArray("config")
		sets, _ := cmd.Flags().GetStringArray("set")
		check := func(key string) error {
			k := strings.ToLower(key)
			for _, s := range []string{"credential", "secret", "password", "privatekey"} {
				if strings.Contains(k, s) {
					return errs.Usage("%s looks like a secret and cannot be given on the command line", key).
						WithHint("Use --bind-credential-file for the LDAP bind credential, or put it in the -f file.")
				}
			}
			return nil
		}
		for _, kv := range kvs {
			k, _, _ := strings.Cut(kv, "=")
			if err := check(k); err != nil {
				return err
			}
		}
		for _, s := range sets {
			p, _, _ := strings.Cut(s, "=")
			segs := strings.Split(p, ".")
			if err := check(segs[len(segs)-1]); err != nil {
				return err
			}
		}
		return run(cmd, args)
	}
}

func componentTypes(a *app.App) cobra.CompletionFunc {
	return a.Fetched("component types", func(ctx context.Context, c *kc.Client, _ []string, _ string) ([]app.Item, error) {
		info, err := c.ServerInfo(ctx)
		if err != nil {
			return nil, err
		}
		m, _ := info["componentTypes"].(map[string]any)
		items := make([]app.Item, 0, len(m))
		for k := range m {
			items = append(items, app.Item{Value: k})
		}
		sort.Slice(items, func(i, j int) bool { return items[i].Value < items[j].Value })
		return items, nil
	})
}

func subTypes(a *app.App, comp cobra.CompletionFunc) *cobra.Command {
	var typ string
	cmd := &cobra.Command{
		Use:     "sub-types <component> --type <provider-type>",
		Short:   "List the sub-component types a component accepts (e.g. LDAP mappers)",
		Example: "  kcctl component sub-types corp-ldap --type org.keycloak.storage.ldap.mappers.LDAPStorageMapper",
		Args:    app.ExactArgs(1, "<component>"), ValidArgsFunction: app.NoMoreArgs(1, comp),
		RunE: func(cmd *cobra.Command, args []string) error {
			if typ == "" {
				return errs.Usage("--type is required")
			}
			ctx := cmd.Context()
			c, err := a.Client()
			if err != nil {
				return err
			}
			id, err := components.Find(ctx, c, args[0])
			if err != nil {
				return err
			}
			v, err := c.Get(ctx, c.RealmPath("components", id, "sub-component-types"), url.Values{"type": {typ}})
			if err != nil {
				return err
			}
			return a.Printer.Print(v, []output.Column{output.Col("id", ".id"), output.Col("help", ".helpText")})
		},
	}
	cmd.Flags().StringVar(&typ, "type", "", "sub-component provider type")
	cmd.RegisterFlagCompletionFunc("type", componentTypes(a))
	return cmd
}

// UserStorage is `user-storage`: components of the user storage type plus
// the federation actions (sync, unlink, remove imported users, test).
func UserStorage(a *app.App) *cobra.Command {
	cmd := app.Group("user-storage", "Manage user federation providers (LDAP, Kerberos)", "ldap", "user-federation")
	r, bind := resource(a, storages, "user storage provider", "user storage providers")
	r.Defaults = map[string]any{"providerType": userStorageType}
	r.Example = map[string]string{
		"create": `  kcctl user-storage create corp-ldap --provider-id ldap \
    --config connectionUrl=ldaps://ldap.corp --config bindDn=cn=kc,dc=corp \
    --config usersDn=ou=people,dc=corp --config editMode=READ_ONLY --config vendor=other \
    --bind-credential-file bind.txt`,
	}
	list := &cobra.Command{
		Use: "list", Aliases: []string{"ls"}, Short: "List user storage providers", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.Client()
			if err != nil {
				return err
			}
			v, err := c.Get(cmd.Context(), c.RealmPath("components"), url.Values{"type": {userStorageType}})
			if err != nil {
				return err
			}
			return a.Printer.Print(v, []output.Column{
				output.Col("name", ".name"), output.Col("provider", ".providerId"),
				output.Col("enabled", ".config.enabled[0]"), output.Col("edit mode", ".config.editMode[0]"),
				output.Col("id", ".id"),
			})
		},
	}
	comp := r.RefCompletion(a)
	cmd.AddCommand(list)
	cmd.AddCommand(writeCmds(a, r, bind)...)
	cmd.AddCommand(sync(a, comp), bulkAction(a, comp, "remove-imported", "remove-imported-users",
		"Delete every user imported from the provider",
		"This deletes every user imported from %s '%s' in realm '%s'.", "Removed users imported from '%s'."),
		bulkAction(a, comp, "unlink", "unlink-users",
			"Turn users imported from the provider into local users",
			"This unlinks every user imported from %s '%s' in realm '%s'; they become local users.", "Unlinked users of '%s'."),
		testConnection(a, comp),
	)
	return cmd
}

func storagePath(ctx context.Context, a *app.App, ref string, segs ...string) (*kc.Client, string, error) {
	c, err := a.Client()
	if err != nil {
		return nil, "", err
	}
	id, err := storages.Find(ctx, c, ref)
	if err != nil {
		return nil, "", err
	}
	return c, c.RealmPath(append([]string{"user-storage", id}, segs...)...), nil
}

func sync(a *app.App, comp cobra.CompletionFunc) *cobra.Command {
	var full, changed bool
	cmd := &cobra.Command{
		Use:               "sync <provider>",
		Short:             "Import users from the provider (changed users by default)",
		Example:           "  kcctl user-storage sync corp-ldap --full",
		Args:              app.ExactArgs(1, "<provider>"),
		ValidArgsFunction: app.NoMoreArgs(1, comp),
		RunE: func(cmd *cobra.Command, args []string) error {
			if full && changed {
				return errs.Usage("--full and --changed are mutually exclusive")
			}
			action := "triggerChangedUsersSync"
			if full {
				action = "triggerFullSync"
			}
			c, p, err := storagePath(cmd.Context(), a, args[0], "sync")
			if err != nil {
				return err
			}
			resp, err := c.Do(cmd.Context(), kc.Request{Method: "POST", Path: p, Query: url.Values{"action": {action}}})
			if err != nil {
				return err
			}
			v, err := resp.JSON()
			if err != nil {
				return err
			}
			if a.Printer.IsStructured() {
				return a.Printer.Print(v, nil)
			}
			m, _ := v.(map[string]any)
			a.Printer.Success("Synced '%s': %v added, %v updated, %v removed, %v failed.", args[0], m["added"], m["updated"], m["removed"], m["failed"])
			return nil
		},
	}
	cmd.Flags().BoolVar(&full, "full", false, "sync all users")
	cmd.Flags().BoolVar(&changed, "changed", false, "sync only users changed since the last sync (default)")
	return cmd
}

func bulkAction(a *app.App, comp cobra.CompletionFunc, verb, seg, short, warn, done string) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:               verb + " <provider>",
		Short:             short,
		Args:              app.ExactArgs(1, "<provider>"),
		ValidArgsFunction: app.NoMoreArgs(1, comp),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, p, err := storagePath(cmd.Context(), a, args[0], seg)
			if err != nil {
				return err
			}
			if err := a.Confirm(yes, fmt.Sprintf(warn, "user storage provider", args[0], c.Realm)); err != nil {
				return err
			}
			if _, err := c.Post(cmd.Context(), p, nil); err != nil {
				return err
			}
			a.Printer.Success(done, args[0])
			return nil
		},
	}
	app.YesFlag(cmd, &yes)
	return cmd
}

// testConnection runs the admin console's "Test connection"/"Test
// authentication" against an LDAP provider's stored settings. The masked
// bind credential plus componentId makes Keycloak use the stored one.
func testConnection(a *app.App, comp cobra.CompletionFunc) *cobra.Command {
	var auth bool
	var credFile string
	cmd := &cobra.Command{
		Use:               "test-connection <provider>",
		Short:             "Test an LDAP provider's connection (or bind with --auth)",
		Args:              app.ExactArgs(1, "<provider>"),
		ValidArgsFunction: app.NoMoreArgs(1, comp),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			c, err := a.Client()
			if err != nil {
				return err
			}
			id, err := storages.Find(ctx, c, args[0])
			if err != nil {
				return err
			}
			v, err := c.Get(ctx, c.RealmPath("components", id), nil)
			if err != nil {
				return err
			}
			m, _ := v.(map[string]any)
			if m["providerId"] != "ldap" {
				return errs.Usage("'%s' is a %v provider; only LDAP providers can be tested", args[0], m["providerId"])
			}
			cfg, _ := m["config"].(map[string]any)
			first := func(k string) any {
				if l, ok := cfg[k].([]any); ok && len(l) > 0 {
					return l[0]
				}
				return nil
			}
			action := "testConnection"
			if auth {
				action = "testAuthentication"
			}
			body := map[string]any{"action": action, "componentId": id}
			for _, k := range []string{"connectionUrl", "bindDn", "bindCredential", "useTruststoreSpi", "connectionTimeout", "startTls", "authType"} {
				if x := first(k); x != nil {
					body[k] = x
				}
			}
			if credFile != "" {
				s, err := a.ReadSecret(credFile, "Bind credential")
				if err != nil {
					return err
				}
				body["bindCredential"] = s
			}
			if _, err := c.Post(ctx, c.RealmPath("testLDAPConnection"), body); err != nil {
				return err
			}
			a.Printer.Success("%s to '%s' succeeded.", map[bool]string{false: "Connection", true: "Authentication"}[auth], args[0])
			return nil
		},
	}
	cmd.Flags().BoolVar(&auth, "auth", false, "also bind with the configured bind DN and credential")
	cmd.Flags().StringVar(&credFile, "bind-credential-file", "", "test this bind credential instead of the stored one ('-' for stdin)")
	return cmd
}
