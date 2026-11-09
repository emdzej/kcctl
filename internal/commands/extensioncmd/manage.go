package extensioncmd

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"sigs.k8s.io/yaml"

	"github.com/emdzej/kcctl/internal/app"
	"github.com/emdzej/kcctl/internal/errs"
	"github.com/emdzej/kcctl/internal/extension"
	"github.com/emdzej/kcctl/internal/kc"
	"github.com/emdzej/kcctl/internal/output"
)

// Loaded is what the root registered, for `extension list`.
type Loaded struct {
	Manifests []*extension.Manifest
	Plugins   []extension.Plugin
	// Shadowed names commands a built-in (or an earlier extension) already
	// owns, so they were skipped.
	Shadowed map[string][]string
	Problems []error
}

func New(a *app.App, l *Loaded) *cobra.Command {
	cmd := app.Group("extension", "Manage commands added by Keycloak extensions and plugins", "ext", "extensions")
	cmd.GroupID = "session"
	cmd.Long = `Extensions add commands for endpoints that Keycloak SPIs (admin REST
extensions) contribute. An extension is a manifest that describes those
commands; kcctl builds them like its own, with completion and output formats.

Manifests come from two places:
  discovered  served by the SPI itself at
              /admin/realms/{realm}/{provider-id}/kcctl-extension and fetched
              by 'kcctl extension discover' (also run after 'kcctl auth login')
  installed   added with 'kcctl extension install <file>'; installed wins

Plugins are executables named kcctl-<name> on PATH; 'kcctl <name> ...' runs
them with the session passed in KCCTL_* environment variables.

See https://github.com/emdzej/kcctl/blob/main/docs/extensions.md`
	cmd.AddCommand(list(a, l), discover(a), install(a), remove(a), show(a, l))
	return cmd
}

func list(a *app.App, l *Loaded) *cobra.Command {
	return &cobra.Command{
		Use: "list", Aliases: []string{"ls"}, Short: "List extensions and plugins for the current server", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			rows := []any{}
			for _, m := range l.Manifests {
				var names []string
				for _, g := range m.Commands {
					names = append(names, g.Name)
				}
				status := "ok"
				if sh := l.Shadowed[m.Name]; len(sh) > 0 {
					status = "shadowed: " + strings.Join(sh, ", ")
				}
				rows = append(rows, map[string]any{
					"name": m.Name, "kind": string(m.Source), "commands": strings.Join(names, ", "),
					"description": m.Description, "origin": m.Origin, "status": status,
				})
			}
			for _, p := range l.Plugins {
				status := "ok"
				if sh := l.Shadowed["plugin:"+p.Name]; len(sh) > 0 {
					status = "shadowed"
				}
				rows = append(rows, map[string]any{
					"name": p.Name, "kind": "plugin", "commands": p.Name, "origin": p.Path, "status": status,
				})
			}
			for _, e := range l.Problems {
				a.Printer.Warn("%v", e)
			}
			if len(rows) == 0 && !a.Printer.IsStructured() {
				a.Printer.Note("No extensions. Run 'kcctl extension discover' to look for them on the server.")
				return nil
			}
			return a.Printer.Print(rows, []output.Column{
				output.Col("name", ".name"), output.Col("kind", ".kind"), output.Col("commands", ".commands"),
				output.Col("status", ".status"), output.Col("description", ".description"),
			})
		},
	}
}

func discover(a *app.App) *cobra.Command {
	return &cobra.Command{
		Use:   "discover",
		Short: "Fetch the manifests that the server's admin REST extensions serve",
		Long: `Ask every admin REST extension on the server (SPI
admin-realm-restapi-extension) for its kcctl manifest and store what it finds
for this server. Extensions without a manifest are skipped quietly.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.Client()
			if err != nil {
				return err
			}
			found, err := Refresh(cmd.Context(), c)
			if err != nil {
				return err
			}
			rows := []any{}
			for _, d := range found {
				if d.Err != nil {
					a.Printer.Warn("%s: %v", d.Provider, d.Err)
					continue
				}
				var names []string
				for _, g := range d.Manifest.Commands {
					names = append(names, g.Name)
				}
				rows = append(rows, map[string]any{"provider": d.Provider, "name": d.Manifest.Name, "commands": strings.Join(names, ", ")})
			}
			if len(rows) == 0 && !a.Printer.IsStructured() {
				a.Printer.Note("No admin REST extension on %s serves a kcctl manifest.", c.Server)
				return nil
			}
			return a.Printer.Print(rows, []output.Column{
				output.Col("provider", ".provider"), output.Col("extension", ".name"), output.Col("commands", ".commands"),
			})
		},
	}
}

// Refresh re-reads the server's providers and rediscovers its manifests.
func Refresh(ctx context.Context, c *kc.Client) ([]extension.Discovered, error) {
	p, err := c.Profile(ctx, true)
	if err != nil {
		return nil, err
	}
	if p.Providers == nil {
		return nil, errs.New(errs.CodePermission, "this account cannot list the server's providers").
			WithHint("Install the manifest by hand with 'kcctl extension install <file>'.")
	}
	return extension.Discover(ctx, c, p.Providers[extension.AdminExtensionSPI])
}

// AutoDiscover runs discovery after sign-in and reports new commands in one
// line. It never fails the caller: a missing manifest is the normal case.
func AutoDiscover(ctx context.Context, a *app.App) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	c, err := a.Client()
	if err != nil {
		return
	}
	found, err := Refresh(ctx, c)
	if err != nil {
		return
	}
	var names []string
	for _, d := range found {
		if d.Manifest != nil {
			names = append(names, d.Manifest.Name)
		}
	}
	if len(names) > 0 {
		a.Printer.Note("Extensions on this server: %s (see 'kcctl extension list').", strings.Join(names, ", "))
	}
}

func install(a *app.App) *cobra.Command {
	return &cobra.Command{
		Use:   "install <file>",
		Short: "Install a manifest from a file ('-' for stdin)",
		Example: `  kcctl extension install ./kcctl-extension.yaml
  curl -s https://example.com/kcctl-extension.yaml | kcctl extension install -`,
		Args: app.ExactArgs(1, "<file>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			var data []byte
			var err error
			if args[0] == "-" {
				data, err = io.ReadAll(a.In)
			} else {
				data, err = os.ReadFile(args[0])
			}
			if err != nil {
				return errs.Usage("cannot read %s: %v", args[0], err)
			}
			m, err := extension.Install(data, args[0])
			if err != nil {
				return errs.New(errs.CodeValidation, "%v", err)
			}
			a.Printer.Success("Installed extension '%s'.", m.Name)
			return nil
		},
	}
}

func remove(a *app.App) *cobra.Command {
	return &cobra.Command{
		Use: "remove <name>", Aliases: []string{"rm", "uninstall"}, Short: "Remove an installed manifest",
		Args: app.ExactArgs(1, "<name>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := extension.Remove(args[0]); err != nil {
				return err
			}
			a.Printer.Success("Removed extension '%s'.", args[0])
			return nil
		},
	}
}

func show(a *app.App, l *Loaded) *cobra.Command {
	return &cobra.Command{
		Use: "show <name>", Short: "Show a loaded manifest", Args: app.ExactArgs(1, "<name>"),
		ValidArgsFunction: func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
			var out []string
			for _, m := range l.Manifests {
				out = append(out, m.Name+"\t"+m.Description)
			}
			return out, cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			for _, m := range l.Manifests {
				if m.Name != args[0] {
					continue
				}
				data, err := json.Marshal(m)
				if err != nil {
					return err
				}
				var v map[string]any
				_ = json.Unmarshal(data, &v)
				v["source"], v["origin"] = string(m.Source), m.Origin
				if a.Flags.Output != "" {
					return a.Printer.Print(v, nil)
				}
				// Plain YAML, ready to save and install elsewhere.
				out, err := yaml.Marshal(v)
				if err != nil {
					return err
				}
				_, err = a.Out.Write(out)
				return err
			}
			return errs.NotFound("no extension named %q", args[0]).WithHint("Run 'kcctl extension list'.")
		},
	}
}
