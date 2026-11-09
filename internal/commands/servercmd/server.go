// Package servercmd reports what the target server is and supports.
package servercmd

import (
	"sort"

	"github.com/spf13/cobra"

	"github.com/emdzej/keycloak-cli/internal/app"
	"github.com/emdzej/keycloak-cli/internal/compat"
	"github.com/emdzej/keycloak-cli/internal/output"
)

func New(a *app.App) *cobra.Command {
	cmd := app.Group("server", "Inspect the Keycloak server")
	cmd.GroupID = "session"
	cmd.AddCommand(info(a), features(a), raw(a))
	return cmd
}

func info(a *app.App) *cobra.Command {
	var refresh bool
	cmd := &cobra.Command{
		Use:   "info",
		Short: "Show the server version and which version-dependent commands it supports",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.Client()
			if err != nil {
				return err
			}
			p, err := c.Profile(cmd.Context(), refresh)
			if err != nil {
				return err
			}
			caps := map[string]any{}
			for k, v := range p.Capabilities() {
				caps[string(k)] = v
			}
			m := map[string]any{
				"server":       c.Server,
				"version":      p.Version.String(),
				"supported":    p.Version.AtLeast(compat.Minimum),
				"capabilities": caps,
			}
			if p.Features == nil {
				m["features"] = "not visible to this account (needs a master-realm admin)"
			}
			return a.Printer.Print(m, nil)
		},
	}
	cmd.Flags().BoolVar(&refresh, "refresh", false, "ignore the cached version and ask the server again")
	return cmd
}

func features(a *app.App) *cobra.Command {
	return &cobra.Command{
		Use: "features", Short: "List server feature flags", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.Client()
			if err != nil {
				return err
			}
			si, err := c.ServerInfo(cmd.Context())
			if err != nil {
				return err
			}
			l, _ := si["features"].([]any)
			sort.Slice(l, func(i, j int) bool {
				ni, _ := l[i].(map[string]any)["name"].(string)
				nj, _ := l[j].(map[string]any)["name"].(string)
				return ni < nj
			})
			return a.Printer.Print(l, []output.Column{
				output.Col("name", ".name"), output.Col("enabled", ".enabled"),
				output.Col("type", ".type"), output.Col("label", ".label"),
			})
		},
	}
}

func raw(a *app.App) *cobra.Command {
	return &cobra.Command{
		Use: "serverinfo", Short: "Print the full /admin/serverinfo document", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.Client()
			if err != nil {
				return err
			}
			si, err := c.ServerInfo(cmd.Context())
			if err != nil {
				return err
			}
			if a.Printer.Format == "" {
				a.Printer.Format = "json"
			}
			return a.Printer.Print(si, nil)
		},
	}
}
