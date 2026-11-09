// Package clientscopecmd manages client scopes, their protocol mappers and
// their role scope mappings. Whether a scope is a realm default lives on the
// realm (`realm default-scope`), not on the scope itself.
package clientscopecmd

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/emdzej/keycloak-cli/internal/app"
	"github.com/emdzej/keycloak-cli/internal/kc"
	"github.com/emdzej/keycloak-cli/internal/output"
)

var scopes = &app.Resource{
	Noun: "client scope", Plural: "client scopes", Lookup: app.ClientScopes,
	Columns: []output.Column{
		output.Col("name", ".name"), output.Col("protocol", ".protocol"),
		output.Col("description", ".description"), output.Col("id", ".id"),
	},
	Fields: app.Fields{
		{Flag: "name", Usage: "scope name (the value clients request in 'scope')"},
		{Flag: "description", Usage: "description"},
		{Flag: "protocol", Usage: "openid-connect|saml|oid4vc"},
		{Flag: "attr", Path: "attributes", Kind: app.KVString, Usage: "set an attribute, e.g. include.in.token.scope=true, display.on.consent.screen=false"},
	},
	CreateArg: "name",
	Required:  []string{"name"},
	Defaults:  map[string]any{"protocol": "openid-connect"},
	Example: map[string]string{
		"create": "  kcctl client-scope create dept --description 'Department claim' --attr include.in.token.scope=true",
		"update": "  kcctl client-scope update dept --attr display.on.consent.screen=false",
	},
}

func New(a *app.App) *cobra.Command {
	cmd := app.Group("client-scope", "Manage client scopes", "client-scopes", "scope")
	cmd.AddCommand(scopes.CRUD(a)...)
	for _, c := range cmd.Commands() {
		if c.Name() == "create" || c.Name() == "update" {
			c.RegisterFlagCompletionFunc("protocol", app.Values("openid-connect", "saml", "oid4vc"))
		}
	}
	comp := scopes.RefCompletion(a)
	scopePath := func(seg string) func(ctx context.Context, c *kc.Client, ref string) (string, error) {
		return func(ctx context.Context, c *kc.Client, ref string) (string, error) {
			id, err := app.ClientScopes.Find(ctx, c, ref)
			if err != nil {
				return "", err
			}
			return c.RealmPath("client-scopes", id, seg), nil
		}
	}
	sm := a.RoleMappingCmds("client-scope", comp, scopePath("scope-mappings"))
	sm.Use = "scope-mapping"
	sm.Aliases = []string{"scope-mappings"}
	sm.Short = "Manage which roles the client scope puts in tokens (scope mappings)"
	cmd.AddCommand(
		a.ProtocolMapperCmds("client-scope", comp, scopePath("protocol-mappers")),
		sm,
	)
	return cmd
}
