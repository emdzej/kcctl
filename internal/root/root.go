// Package root assembles the command tree. It is the only place that knows
// every command package, so adding a resource group is one line here.
package root

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"strings"

	"github.com/spf13/cobra"

	"github.com/emdzej/keycloak-cli/internal/app"
	"github.com/emdzej/keycloak-cli/internal/errs"
	"github.com/emdzej/keycloak-cli/internal/repl"
)

// Builder adds one top-level command (or group) to the tree.
type Builder func(a *app.App) *cobra.Command

var builders []Builder

// Register is called from command packages' wiring in commands.go.
func Register(b ...Builder) { builders = append(builders, b...) }

func New(a *app.App) *cobra.Command {
	root := &cobra.Command{
		Use:   "kcctl",
		Short: "Command-line client for the Keycloak admin API",
		Long: `kcctl manages Keycloak realms, users, clients and everything else the admin
REST API exposes, for Keycloak 26.6.3 and newer.

Get started:
  kcctl config set-context local --server http://localhost:8080
  kcctl auth login
  kcctl realm list

Run 'kcctl interactive' for a prompt with live completion.`,
		SilenceErrors:     true,
		SilenceUsage:      true,
		Version:           app.Version,
		Args:              cobra.NoArgs,
		RunE:              func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
		PersistentPreRunE: func(*cobra.Command, []string) error { return a.Init() },
	}
	root.SetVersionTemplate("kcctl {{.Version}}\n")
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return errs.Usage("%v", err)
	})
	a.BindGlobals(root)
	root.AddGroup(
		&cobra.Group{ID: "resources", Title: "Resources:"},
		&cobra.Group{ID: "session", Title: "Session and setup:"},
	)
	root.SetCompletionCommandGroupID("session")
	root.AddCommand(repl.New(a, func() *cobra.Command { return New(app.New()) }))
	for _, b := range builders {
		cmd := b(a)
		if cmd.GroupID == "" {
			cmd.GroupID = "resources"
		}
		root.AddCommand(cmd)
	}
	return root
}

// Main runs the CLI and returns the process exit code.
func Main(args []string) int {
	a := app.New()
	root := New(a)
	root.SetArgs(args)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	err := root.ExecuteContext(ctx)
	if err == nil {
		return 0
	}
	e := errs.As(err)
	// Cobra's own parse errors ("unknown command") arrive untyped.
	var ours *errs.Error
	if !errors.As(err, &ours) && isCobraUsage(err) {
		e = errs.Usage("%v", err).WithHint("Run 'kcctl --help' for usage.")
	}
	a.EnsurePrinter().Error(e)
	return e.ExitCode()
}

func isCobraUsage(err error) bool {
	s := err.Error()
	for _, p := range []string{"unknown command", "unknown flag", "unknown shorthand", "accepts ", "requires ", "invalid argument", "flag needs"} {
		if strings.Contains(s, p) {
			return true
		}
	}
	return false
}
