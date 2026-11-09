// Package keycmd shows the realm's signing and encryption keys and the key
// providers that hold them.
package keycmd

import (
	"net/url"

	"github.com/spf13/cobra"

	"github.com/emdzej/keycloak-cli/internal/app"
	"github.com/emdzej/keycloak-cli/internal/output"
)

func New(a *app.App) *cobra.Command {
	cmd := app.Group("key", "Show realm keys and key providers", "keys")
	cmd.Long = `Show realm keys and key providers.

Key providers are components; add or rotate keys with
'kcctl api POST components --data @provider.json'.`
	cmd.AddCommand(list(a), providers(a))
	return cmd
}

func list(a *app.App) *cobra.Command {
	return &cobra.Command{
		Use: "list", Aliases: []string{"ls"},
		Short: "List the realm's keys (-o json shows the full document, including active kids)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.Client()
			if err != nil {
				return err
			}
			v, err := c.Get(cmd.Context(), c.RealmPath("keys"), nil)
			if err != nil {
				return err
			}
			if a.Printer.IsStructured() {
				return a.Printer.Print(v, nil)
			}
			// Tables show one row per key; the "active" map (algorithm to kid)
			// is only in the structured document.
			m, _ := v.(map[string]any)
			keys, _ := m["keys"].([]any)
			if keys == nil {
				keys = []any{}
			}
			return a.Printer.Print(keys, []output.Column{
				output.Col("kid", ".kid"), output.Col("type", ".type"), output.Col("algorithm", ".algorithm"),
				output.Col("use", ".use"), output.Col("status", ".status"), output.Col("provider", ".providerId"),
				output.Col("valid to", ".validTo"),
			})
		},
	}
}

func providers(a *app.App) *cobra.Command {
	return &cobra.Command{
		Use: "providers", Short: "List the realm's key providers", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.Client()
			if err != nil {
				return err
			}
			v, err := c.Get(cmd.Context(), c.RealmPath("components"), url.Values{"type": {"org.keycloak.keys.KeyProvider"}})
			if err != nil {
				return err
			}
			return a.Printer.Print(v, []output.Column{
				output.Col("name", ".name"), output.Col("provider", ".providerId"),
				output.Col("priority", ".config.priority[0]"), output.Col("enabled", ".config.enabled[0]"),
				output.Col("active", ".config.active[0]"), output.Col("id", ".id"),
			})
		},
	}
}
