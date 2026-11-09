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

	"github.com/emdzej/kcctl/internal/app"
	"github.com/emdzej/kcctl/internal/commands/extensioncmd"
	"github.com/emdzej/kcctl/internal/config"
	"github.com/emdzej/kcctl/internal/errs"
	"github.com/emdzej/kcctl/internal/extension"
	"github.com/emdzej/kcctl/internal/repl"
)

// Builder adds one top-level command (or group) to the tree.
type Builder func(a *app.App) *cobra.Command

var builders []Builder

// Register is called from command packages' wiring in commands.go.
func Register(b ...Builder) { builders = append(builders, b...) }

// New builds the tree for the target named on os.Args (or env, or config),
// which decides the server whose discovered extensions are loaded.
func New(a *app.App) *cobra.Command { return NewFor(a, os.Args[1:]) }

func NewFor(a *app.App, args []string) *cobra.Command {
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
	loaded := &extensioncmd.Loaded{Shadowed: map[string][]string{}}
	root.AddCommand(extensioncmd.New(a, loaded))
	addExtensions(a, root, args, loaded)
	return root
}

// addExtensions registers manifest commands and plugins after the built-ins,
// which always win a name clash: an extension must never be able to change
// what an existing command does.
func addExtensions(a *app.App, root *cobra.Command, args []string, l *extensioncmd.Loaded) {
	l.Manifests, l.Problems = extension.Load(targetServer(root, args))
	l.Plugins = extension.Plugins()
	if len(l.Manifests) == 0 && len(l.Plugins) == 0 {
		return
	}
	root.AddGroup(&cobra.Group{ID: extensioncmd.GroupID, Title: "Extensions:"})
	for _, m := range l.Manifests {
		for _, cmd := range extensioncmd.Build(a, m) {
			if taken(root, cmd) {
				l.Shadowed[m.Name] = append(l.Shadowed[m.Name], cmd.Name())
				continue
			}
			root.AddCommand(cmd)
		}
	}
	for _, p := range l.Plugins {
		cmd := extensioncmd.PluginCmd(a, p)
		if taken(root, cmd) {
			l.Shadowed["plugin:"+p.Name] = []string{p.Name}
			continue
		}
		root.AddCommand(cmd)
	}
}

func taken(root *cobra.Command, cmd *cobra.Command) bool {
	names := append([]string{cmd.Name()}, cmd.Aliases...)
	for _, c := range root.Commands() {
		for _, n := range names {
			if c.Name() == n || c.HasAlias(n) {
				return true
			}
		}
	}
	return false
}

// targetServer resolves the server before flags are parsed, from the
// --context/--server given ahead of the command, then env and config.
func targetServer(root *cobra.Command, args []string) string {
	vals, _ := scanGlobals(root, args)
	cfg, err := config.Load()
	if err != nil {
		return ""
	}
	r, _ := config.Resolve(cfg, config.Overrides{Context: vals["context"], Server: vals["server"]}, config.EnvOverrides())
	if r == nil {
		return ""
	}
	return r.Server
}

// scanGlobals reads root flags up to the first positional and returns their
// values and the positional's index (-1 if none, or an unknown flag first).
func scanGlobals(root *cobra.Command, args []string) (map[string]string, int) {
	vals := map[string]string{}
	fs := root.PersistentFlags()
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			return vals, -1
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			return vals, i
		}
		name, val, hasVal := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		f := fs.Lookup(name)
		if f == nil && !strings.HasPrefix(arg, "--") && len(name) > 0 {
			f = fs.ShorthandLookup(name[:1])
			if f != nil && len(name) > 1 && f.NoOptDefVal == "" {
				val, hasVal = name[1:], true // -rmaster
			}
		}
		if f == nil {
			return vals, -1
		}
		if !hasVal && f.NoOptDefVal == "" {
			if i+1 >= len(args) {
				return vals, -1
			}
			i++
			val = args[i]
		}
		vals[f.Name] = val
	}
	return vals, -1
}

// runPlugin handles `kcctl [global flags] <plugin> [plugin args]` before
// cobra, so the plugin's own flags reach it untouched. It reports false when
// the line isn't a plugin call.
func runPlugin(a *app.App, root *cobra.Command, args []string) (bool, error) {
	_, i := scanGlobals(root, args)
	if i < 0 {
		return false, nil
	}
	for _, c := range root.Commands() {
		if c.Name() != args[i] || c.Annotations["plugin"] == "" {
			continue
		}
		if err := root.PersistentFlags().Parse(args[:i]); err != nil {
			return true, errs.Usage("%v", err)
		}
		if err := a.Init(); err != nil {
			return true, err
		}
		return true, extensioncmd.RunPlugin(a, extension.Plugin{Name: c.Name(), Path: c.Annotations["plugin"]}, args[i+1:])
	}
	return false, nil
}

// Main runs the CLI and returns the process exit code.
func Main(args []string) int {
	a := app.New()
	root := NewFor(a, args)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	handled, err := runPlugin(a, root, args)
	if !handled {
		root.SetArgs(args)
		err = root.ExecuteContext(ctx)
	}
	if err == nil {
		return 0
	}
	var pe *extensioncmd.ExitError
	if errors.As(err, &pe) {
		return pe.Code
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
