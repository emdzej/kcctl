package extensioncmd

import (
	"errors"
	"os"
	"os/exec"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/emdzej/kcctl/internal/app"
	"github.com/emdzej/kcctl/internal/errs"
	"github.com/emdzej/kcctl/internal/extension"
)

// ExitError carries a plugin's exit code through cobra to Main unchanged.
type ExitError struct{ Code int }

func (e *ExitError) Error() string { return "plugin exited with status " + strconv.Itoa(e.Code) }

// PluginCmd is a placeholder so plugins show up in help and completion.
// Flags are not parsed: everything after the name belongs to the plugin.
func PluginCmd(a *app.App, p extension.Plugin) *cobra.Command {
	return &cobra.Command{
		Use:                p.Name,
		Short:              "Plugin (" + p.Path + ")",
		GroupID:            GroupID,
		DisableFlagParsing: true,
		Annotations:        map[string]string{"plugin": p.Path},
		RunE: func(cmd *cobra.Command, args []string) error {
			return RunPlugin(a, p, args)
		},
	}
}

// RunPlugin execs the plugin with stdio attached. The target reaches it
// through KCCTL_* variables; the connection ones are those kcctl itself
// reads, so a plugin calling `$KCCTL_BIN token` or `$KCCTL_BIN api` talks to
// the same server with the same session.
func RunPlugin(a *app.App, p extension.Plugin, args []string) error {
	c := exec.Command(p.Path, args...)
	c.Stdin, c.Stdout, c.Stderr = a.In, a.Out, a.Err
	c.Env = append(os.Environ(), PluginEnv(a)...)
	err := c.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &ee):
		return &ExitError{Code: ee.ExitCode()}
	}
	return errs.Usage("cannot run plugin %s: %v", p.Path, err)
}

// PluginEnv passes on what was given on kcctl's command line, plus the
// resolved target when there is a context to resolve.
func PluginEnv(a *app.App) []string {
	f := a.Flags
	env := []string{}
	set := func(k, v string) {
		if v != "" {
			env = append(env, k+"="+v)
		}
	}
	if self, err := os.Executable(); err == nil {
		set("KCCTL_BIN", self)
	}
	if r, err := a.Resolve(); err == nil {
		set("KCCTL_CONTEXT", r.Name)
		set("KCCTL_SERVER", r.Server)
		set("KCCTL_AUTH_REALM", r.AuthRealm)
		set("KCCTL_REALM", r.Realm)
		set("KCCTL_CLIENT_ID", r.ClientID)
	} else {
		set("KCCTL_CONTEXT", f.Context)
		set("KCCTL_SERVER", f.Server)
		set("KCCTL_AUTH_REALM", f.AuthRealm)
		set("KCCTL_REALM", f.Realm)
		set("KCCTL_CLIENT_ID", f.ClientID)
	}
	// Only a token the user gave; a stored session is fetched with `kcctl token`.
	set("KCCTL_TOKEN", f.Token)
	set("KCCTL_OUTPUT", f.Output)
	if f.NoInput {
		set("KCCTL_NO_INPUT", "1")
	}
	if f.Verbose > 0 {
		set("KCCTL_VERBOSE", strconv.Itoa(f.Verbose))
	}
	return env
}
