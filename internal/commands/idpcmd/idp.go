// Package idpcmd manages identity providers (brokering) and their mappers.
package idpcmd

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/emdzej/keycloak-cli/internal/app"
	"github.com/emdzej/keycloak-cli/internal/errs"
	"github.com/emdzej/keycloak-cli/internal/kc"
	"github.com/emdzej/keycloak-cli/internal/output"
)

// masked is ComponentRepresentation.SECRET_VALUE: what Keycloak returns in
// place of config.clientSecret on every IdP read (26.6.3 and later).
const masked = "**********"

var columns = []output.Column{
	output.Col("alias", ".alias"), output.Col("provider", ".providerId"),
	output.Col("display name", ".displayName"), output.Col("enabled", ".enabled"),
	output.Col("internal id", ".internalId"),
}

var lookup = app.Lookup{
	What: "identity provider", Path: app.RealmPath("identity-provider", "instances"),
	NameField: "alias", IDField: "alias",
	Search: func(p string) url.Values {
		q := url.Values{"briefRepresentation": {"true"}, "max": {"100"}}
		if p != "" {
			q.Set("search", p) // prefix match by default
		}
		return q
	},
}

// secretProviders authenticate to the upstream with config.clientSecret
// unless clientAuthMethod is private_key_jwt.
var secretProviders = map[string]bool{
	"oidc": true, "keycloak-oidc": true, "oauth2": true, "google": true, "github": true,
	"facebook": true, "microsoft": true, "linkedin-openid-connect": true, "gitlab": true,
	"bitbucket": true, "instagram": true, "paypal": true, "stackoverflow": true,
	"twitter": true, "openshift-v3": true, "openshift-v4": true,
}

func New(a *app.App) *cobra.Command {
	cmd := app.Group("idp", "Manage identity providers", "idps", "identity-provider")
	var createSecret, updateSecret string
	r := &app.Resource{
		Noun: "identity provider", Plural: "identity providers", Lookup: lookup,
		KeyByName: true, Columns: columns, Paged: true,
		ListFlags: func(cmd *cobra.Command) func(url.Values) {
			f := cmd.Flags()
			search := f.String("search", "", "filter by alias: prefix (x), infix (*x*) or exact (\"x\")")
			realmOnly := f.Bool("realm-only", false, "only IdPs not linked to an organization")
			full := f.Bool("full", false, "return full representations (default is brief)")
			typ := f.String("type", "", "filter by IdP type")
			capability := f.String("capability", "", "filter by IdP capability")
			return func(q url.Values) {
				if *search != "" {
					q.Set("search", *search)
				}
				if *realmOnly {
					q.Set("realmOnly", "true")
				}
				if !*full {
					q.Set("briefRepresentation", "true")
				}
				if *typ != "" {
					q.Set("type", *typ)
				}
				if *capability != "" {
					q.Set("capability", *capability)
				}
			}
		},
		Fields: app.Fields{
			{Flag: "provider-id", Path: "providerId", Usage: "provider type, e.g. oidc, keycloak-oidc, saml, google, github"},
			{Flag: "display-name", Path: "displayName", Usage: "name shown on the login page"},
			{Flag: "enabled", Kind: app.Bool, Usage: "whether the IdP can be used"},
			{Flag: "trust-email", Path: "trustEmail", Kind: app.Bool, Usage: "treat emails from this IdP as verified"},
			{Flag: "store-token", Path: "storeToken", Kind: app.Bool, Usage: "store the upstream tokens"},
			{Flag: "link-only", Path: "linkOnly", Kind: app.Bool, Usage: "only allow linking existing accounts"},
			{Flag: "hide-on-login", Path: "hideOnLogin", Kind: app.Bool, Usage: "hide from the login page"},
			{Flag: "first-broker-login-flow", Path: "firstBrokerLoginFlowAlias", Usage: "flow run on first login"},
			{Flag: "post-broker-login-flow", Path: "postBrokerLoginFlowAlias", Usage: "flow run after every login"},
			{Flag: "config", Path: "config", Kind: app.KVString, Usage: "provider config entry, e.g. clientId=x, authorizationUrl=..."},
		},
		CreateArg: "alias",
		Required:  []string{"alias", "providerId"},
		BeforeWrite: func(ctx context.Context, a *app.App, b map[string]any, update bool) error {
			file := createSecret
			if update {
				file = updateSecret
			}
			return applySecret(a, b, file, update)
		},
		Example: map[string]string{
			"list": "  kcctl idp list\n  kcctl idp list --realm-only --full -o json",
			"create": `  kcctl idp create corp --provider-id oidc --display-name Corp \
    --config clientId=kc --config authorizationUrl=https://corp/auth --config tokenUrl=https://corp/token \
    --client-secret-file secret.txt
  printf '%s' "$SECRET" | kcctl idp create gh --provider-id github --config clientId=abc`,
			"update": "  kcctl idp update corp --display-name 'Corp SSO'\n  kcctl idp update corp --client-secret-file new-secret.txt",
		},
	}
	create, update := r.CreateCmd(a), r.UpdateCmd(a)
	create.Flags().StringVar(&createSecret, "client-secret-file", "", "read config.clientSecret from this file ('-' for stdin)")
	update.Flags().StringVar(&updateSecret, "client-secret-file", "", "replace config.clientSecret with the contents of this file ('-' for stdin)")
	// A secret rotation alone is a change; the generic update would call it
	// "nothing to change", so it gets an alias=<alias> overlay, which is a
	// no-op (the alias cannot change anyway).
	run := update.RunE
	update.RunE = func(cmd *cobra.Command, args []string) error {
		if updateSecret != "" && len(args) == 1 {
			q, _ := json.Marshal(args[0]) // quoted, so "123" stays a string
			if err := cmd.Flags().Set("set", "alias="+string(q)); err != nil {
				return err
			}
		}
		return run(cmd, args)
	}
	for _, c := range []*cobra.Command{create, update} {
		guardConfig(c)
		c.RegisterFlagCompletionFunc("provider-id", providerTypes(a))
		c.RegisterFlagCompletionFunc("first-broker-login-flow", flows(a))
		c.RegisterFlagCompletionFunc("post-broker-login-flow", flows(a))
	}
	update.Long = `Change an identity provider. Keycloak returns config.clientSecret masked
("**********") and keeps the stored secret when the masked value is sent back,
so an update leaves the secret alone unless --client-secret-file is given.
Keycloak 26.8+ refuses the masked value when the token URL, client ID, base
URL or client authentication method change; pass --client-secret-file then.`
	comp := r.RefCompletion(a)
	cmd.AddCommand(r.ListCmd(a), r.GetCmd(a), create, update, r.DeleteCmd(a),
		mappers(a, comp), importConfig(a))
	return cmd
}

// applySecret puts the client secret into config only from a file, stdin or
// a prompt. On update the masked value read from the server is sent back
// as-is: the server swaps it for the stored secret, while dropping it would
// erase the secret (the PUT replaces the whole config map).
func applySecret(a *app.App, b map[string]any, file string, update bool) error {
	cfg, _ := b["config"].(map[string]any)
	if cfg == nil {
		cfg = map[string]any{}
		b["config"] = cfg
	}
	need := file != ""
	if !update && !need {
		pid, _ := b["providerId"].(string)
		_, has := cfg["clientSecret"]
		need = secretProviders[pid] && !has && cfg["clientAuthMethod"] != "private_key_jwt"
	}
	if !need {
		return nil
	}
	alias, _ := b["alias"].(string)
	s, err := a.ReadSecret(file, "Client secret for '"+alias+"'")
	if err != nil {
		return err
	}
	if s == "" {
		return errs.Usage("the client secret is empty; nothing was changed")
	}
	cfg["clientSecret"] = s
	return nil
}

// guardConfig keeps secrets off the command line: BodyFlags already rejects
// -s ...clientSecret, this covers --config clientSecret=....
func guardConfig(cmd *cobra.Command) {
	run := cmd.RunE
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		kvs, _ := cmd.Flags().GetStringArray("config")
		for _, kv := range kvs {
			k, _, _ := strings.Cut(kv, "=")
			if strings.EqualFold(k, "clientSecret") {
				return errs.Usage("--config cannot carry the client secret").
					WithHint("Use --client-secret-file, pipe it on stdin, or put it in the -f file.")
			}
		}
		return run(cmd, args)
	}
}

func providerTypes(a *app.App) cobra.CompletionFunc {
	return a.Fetched("identity provider types", func(ctx context.Context, c *kc.Client, _ []string, _ string) ([]app.Item, error) {
		info, err := c.ServerInfo(ctx)
		if err != nil {
			return nil, err
		}
		var items []app.Item
		for _, k := range []string{"identityProviders", "socialProviders"} {
			items = append(items, app.ItemsFrom(info[k], "id", "name")...)
		}
		return items, nil
	})
}

func flows(a *app.App) cobra.CompletionFunc {
	return a.Fetched("authentication flows", func(ctx context.Context, c *kc.Client, _ []string, _ string) ([]app.Item, error) {
		v, err := c.Get(ctx, c.RealmPath("authentication", "flows"), nil)
		if err != nil {
			return nil, err
		}
		return app.ItemsFrom(v, "alias", "description"), nil
	})
}

var mapperColumns = []output.Column{
	output.Col("name", ".name"), output.Col("type", ".identityProviderMapper"),
	output.Col("sync mode", ".config.syncMode"), output.Col("id", ".id"),
}

func mappers(a *app.App, idpComp cobra.CompletionFunc) *cobra.Command {
	group := app.Group("mapper", "Manage an identity provider's mappers", "mappers")
	base := func(c *kc.Client, alias string, segs ...string) string {
		return c.RealmPath(append([]string{"identity-provider", "instances", alias}, segs...)...)
	}
	mapperID := func(ctx context.Context, c *kc.Client, alias, ref string) (string, error) {
		if app.IsUUID(ref) {
			return ref, nil
		}
		l := app.Lookup{What: "IdP mapper", NameField: "name",
			Path: func(context.Context, *kc.Client) (string, error) { return base(c, alias, "mappers"), nil }}
		return l.Find(ctx, c, ref)
	}
	mapperComp := func(cmd *cobra.Command, args []string, tc string) ([]string, cobra.ShellCompDirective) {
		if len(args) == 0 {
			return idpComp(cmd, args, tc)
		}
		if len(args) > 1 && cmd.Name() != "delete" {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return a.Fetched("IdP mappers", func(ctx context.Context, c *kc.Client, args []string, _ string) ([]app.Item, error) {
			v, err := c.Get(ctx, base(c, args[0], "mappers"), nil)
			if err != nil {
				return nil, err
			}
			return app.ItemsFrom(v, "name", "identityProviderMapper"), nil
		})(cmd, args, tc)
	}
	typeComp := a.Fetched("mapper types", func(ctx context.Context, c *kc.Client, args []string, _ string) ([]app.Item, error) {
		if len(args) == 0 {
			return nil, errs.Usage("name the identity provider first")
		}
		v, err := c.Get(ctx, base(c, args[0], "mapper-types"), nil)
		if err != nil {
			return nil, err
		}
		m, _ := v.(map[string]any)
		items := make([]app.Item, 0, len(m))
		for id, x := range m {
			t, _ := x.(map[string]any)
			name, _ := t["name"].(string)
			items = append(items, app.Item{Value: id, Desc: name})
		}
		sort.Slice(items, func(i, j int) bool { return items[i].Value < items[j].Value })
		return items, nil
	})
	fields := app.Fields{
		{Flag: "name", Usage: "mapper name"},
		{Flag: "mapper-type", Path: "identityProviderMapper", Usage: "mapper type id, e.g. hardcoded-role-idp-mapper"},
		{Flag: "sync-mode", Path: "config.syncMode", Usage: "INHERIT|IMPORT|LEGACY|FORCE"},
		{Flag: "config", Path: "config", Kind: app.KVString, Usage: "mapper config entry"},
	}
	bindCompletions := func(c *cobra.Command) {
		c.RegisterFlagCompletionFunc("mapper-type", typeComp)
		c.RegisterFlagCompletionFunc("sync-mode", app.Values("INHERIT", "IMPORT", "LEGACY", "FORCE"))
	}

	list := &cobra.Command{
		Use: "list <idp>", Aliases: []string{"ls"}, Short: "List an IdP's mappers",
		Args: app.ExactArgs(1, "<idp>"), ValidArgsFunction: app.NoMoreArgs(1, idpComp),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.Client()
			if err != nil {
				return err
			}
			v, err := c.Get(cmd.Context(), base(c, args[0], "mappers"), nil)
			if err != nil {
				return err
			}
			return a.Printer.Print(v, mapperColumns)
		},
	}
	get := &cobra.Command{
		Use: "get <idp> <mapper>", Short: "Show an IdP mapper",
		Args: app.ExactArgs(2, "<idp> <mapper>"), ValidArgsFunction: mapperComp,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			c, err := a.Client()
			if err != nil {
				return err
			}
			id, err := mapperID(ctx, c, args[0], args[1])
			if err != nil {
				return err
			}
			v, err := c.Get(ctx, base(c, args[0], "mappers", id), nil)
			if err != nil {
				return err
			}
			return a.Printer.Print(v, mapperColumns)
		},
	}
	types := &cobra.Command{
		Use: "types <idp>", Short: "List the mapper types this IdP supports",
		Args: app.ExactArgs(1, "<idp>"), ValidArgsFunction: app.NoMoreArgs(1, idpComp),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.Client()
			if err != nil {
				return err
			}
			v, err := c.Get(cmd.Context(), base(c, args[0], "mapper-types"), nil)
			if err != nil {
				return err
			}
			m, _ := v.(map[string]any)
			keys := make([]string, 0, len(m))
			for k := range m {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			rows := make([]any, 0, len(keys))
			for _, k := range keys {
				rows = append(rows, m[k])
			}
			return a.Printer.Print(rows, []output.Column{
				output.Col("id", ".id"), output.Col("name", ".name"), output.Col("category", ".category"),
			})
		},
	}

	var cb app.BodyFlags
	create := &cobra.Command{
		Use:   "create <idp>",
		Short: "Add a mapper to an IdP",
		Example: `  kcctl idp mapper create corp --name dept --mapper-type oidc-user-attribute-idp-mapper \
    --sync-mode FORCE --config claim=dept --config user.attribute=dept`,
		Args: app.ExactArgs(1, "<idp>"), ValidArgsFunction: app.NoMoreArgs(1, idpComp),
	}
	cfv := fields.Bind(create)
	cb.Bind(create)
	bindCompletions(create)
	create.RunE = func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		b, err := cb.Build(a, map[string]any{"config": map[string]any{}})
		if err != nil {
			return err
		}
		if err := cfv.Apply(cmd, b); err != nil {
			return err
		}
		// 26.8+ rejects a mapper whose alias differs from the path's.
		b["identityProviderAlias"] = args[0]
		if err := app.RequireFields(b, "name", "identityProviderMapper"); err != nil {
			return err
		}
		c, err := a.Client()
		if err != nil {
			return err
		}
		resp, err := c.Post(ctx, base(c, args[0], "mappers"), b)
		if err != nil {
			return err
		}
		name, _ := b["name"].(string)
		return a.ReportCreated("IdP mapper", name, resp.CreatedID())
	}

	var ub app.BodyFlags
	update := &cobra.Command{
		Use: "update <idp> <mapper>", Short: "Change an IdP mapper",
		Args: app.ExactArgs(2, "<idp> <mapper>"), ValidArgsFunction: mapperComp,
	}
	ufv := fields.Bind(update)
	ub.Bind(update)
	bindCompletions(update)
	update.RunE = func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		if !ub.Given() && !changed(cmd, fields) {
			return errs.Usage("nothing to change").WithHint("Pass field flags, -s path=value, or -f file.")
		}
		c, err := a.Client()
		if err != nil {
			return err
		}
		id, err := mapperID(ctx, c, args[0], args[1])
		if err != nil {
			return err
		}
		p := base(c, args[0], "mappers", id)
		cur, err := c.Get(ctx, p, nil)
		if err != nil {
			return err
		}
		b, _ := cur.(map[string]any)
		if b, err = ub.Build(a, b); err != nil {
			return err
		}
		if err := ufv.Apply(cmd, b); err != nil {
			return err
		}
		b["id"], b["identityProviderAlias"] = id, args[0]
		if _, err := c.Put(ctx, p, b); err != nil {
			return err
		}
		a.Printer.Success("Updated IdP mapper '%s'.", args[1])
		return nil
	}

	var yes bool
	del := &cobra.Command{
		Use: "delete <idp> <mapper>...", Aliases: []string{"rm"}, Short: "Remove IdP mappers",
		Args: app.MinArgs(2, "<idp> <mapper>..."), ValidArgsFunction: mapperComp,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			c, err := a.Client()
			if err != nil {
				return err
			}
			if len(args) > 2 {
				if err := a.Confirm(yes, "This removes "+app.IntString(len(args)-1)+" mappers from identity provider '"+args[0]+"'."); err != nil {
					return err
				}
			}
			for _, ref := range args[1:] {
				id, err := mapperID(ctx, c, args[0], ref)
				if err != nil {
					return err
				}
				if _, err := c.Delete(ctx, base(c, args[0], "mappers", id), nil); err != nil {
					return err
				}
				a.Printer.Success("Removed IdP mapper '%s'.", ref)
			}
			return nil
		},
	}
	app.YesFlag(del, &yes)
	group.AddCommand(list, get, types, create, update, del)
	return group
}

func changed(cmd *cobra.Command, fs app.Fields) bool {
	for _, f := range fs {
		if cmd.Flags().Changed(f.Flag) {
			return true
		}
	}
	return false
}

// importConfig asks the server to parse provider metadata (OIDC discovery,
// SAML descriptor) into an IdP config map, from a URL the server fetches
// or a local file uploaded as multipart.
func importConfig(a *app.App) *cobra.Command {
	var fromURL, file, providerID string
	cmd := &cobra.Command{
		Use:   "import-config",
		Short: "Parse OIDC discovery or SAML metadata into an IdP config",
		Example: `  kcctl idp import-config --provider-id oidc --from-url https://corp/.well-known/openid-configuration
  kcctl idp import-config --provider-id saml -f idp-metadata.xml -o json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if providerID == "" {
				return errs.Usage("--provider-id is required")
			}
			if (fromURL == "") == (file == "") {
				return errs.Usage("pass exactly one of --from-url or -f/--file")
			}
			c, err := a.Client()
			if err != nil {
				return err
			}
			p := c.RealmPath("identity-provider", "import-config")
			var resp *kc.Response
			if fromURL != "" {
				resp, err = c.Post(cmd.Context(), p, map[string]any{"providerId": providerID, "fromUrl": fromURL})
			} else {
				body, ct, ferr := multipartBody(a, file, providerID)
				if ferr != nil {
					return ferr
				}
				resp, err = c.Do(cmd.Context(), kc.Request{Method: "POST", Path: p, RawBody: body, ContentType: ct})
			}
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
	cmd.Flags().StringVar(&providerID, "provider-id", "", "provider type that parses the metadata (oidc, keycloak-oidc, saml)")
	cmd.Flags().StringVar(&fromURL, "from-url", "", "metadata URL for the server to fetch")
	cmd.Flags().StringVarP(&file, "file", "f", "", "local metadata file to upload ('-' for stdin)")
	cmd.RegisterFlagCompletionFunc("provider-id", app.Values("oidc", "keycloak-oidc", "saml"))
	return cmd
}

func multipartBody(a *app.App, file, providerID string) ([]byte, string, error) {
	var data []byte
	var err error
	name := "metadata"
	if file == "-" {
		data, err = readAll(a)
	} else {
		data, err = os.ReadFile(file)
		name = filepath.Base(file)
	}
	if err != nil {
		return nil, "", errs.Usage("cannot read %s: %v", file, err)
	}
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if err := w.WriteField("providerId", providerID); err != nil {
		return nil, "", err
	}
	fw, err := w.CreateFormFile("file", name)
	if err != nil {
		return nil, "", err
	}
	if _, err := fw.Write(data); err != nil {
		return nil, "", err
	}
	if err := w.Close(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), w.FormDataContentType(), nil
}

func readAll(a *app.App) ([]byte, error) {
	var buf bytes.Buffer
	_, err := buf.ReadFrom(a.In)
	return buf.Bytes(), err
}
