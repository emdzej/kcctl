// Package repl is `kcctl interactive`: a prompt with live, server-aware
// suggestions. Each submitted line runs as a child kcctl process, so a line
// behaves exactly as in a shell (exit codes, panics and global state can't
// leak between lines). Do not "optimise" this into in-process execution.
package repl

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/emdzej/kcctl/internal/app"
	"github.com/emdzej/kcctl/internal/config"
	"github.com/emdzej/kcctl/internal/errs"
	"github.com/emdzej/kcctl/internal/kc"
	"github.com/emdzej/kcctl/internal/output"
)

const greeting = `kcctl interactive mode. Suggestions follow what you type: Tab or → accepts, ↑/↓ choose, Esc hides them.
Type 'exit' or press Ctrl-D to leave.`

// Keys is shown in --help so the bindings are discoverable.
const Keys = `Keys:
  Tab, →            accept the suggestion
  ↑/↓, Shift-Tab    choose a suggestion (or walk history when none is shown)
  PgUp/PgDn         move a page in the suggestion list
  Ctrl-P/Ctrl-N     previous/next history entry
  Ctrl-R            search history
  Enter             run the line (an open quote or trailing \ continues it)
  Alt-Enter         insert a newline
  Esc               hide suggestions
  Ctrl-C            clear the line; on an empty line, exit
  Ctrl-D            exit
  Ctrl-A/E/W/U/K/L  start, end, delete word, delete to start/end, clear screen`

// New returns the interactive command. build makes the command tree used to
// validate lines as they are typed.
func New(a *app.App, build func() *cobra.Command) *cobra.Command {
	return &cobra.Command{
		Use:     "interactive",
		Aliases: []string{"i", "shell"},
		Short:   "Start a prompt with live completion",
		Long: "Start a prompt with live completion of commands, flags and server resources.\n\n" +
			"Global flags given here (e.g. --context prod -r acme) apply to every line;\n" +
			"the same flag typed on a line wins.\n\n" + Keys,
		GroupID: "session",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if a.Flags.NoInput {
				return errs.Usage("Interactive mode needs to prompt. Run the command directly with --no-input.")
			}
			if !a.StdinTTY || !a.StderrTTY {
				return errs.Usage("Not a terminal. Run the command directly: 'kcctl <command>'.")
			}
			return run(a, build, forwardedFlags(cmd))
		},
	}
}

// forwardedFlags re-creates the session's global flags for each child line.
func forwardedFlags(cmd *cobra.Command) []string {
	var out []string
	// VisitAll+Changed, not Visit: the flags were parsed on the subcommand's
	// merged flag set, so the root set's own "actual" map is empty.
	cmd.Root().PersistentFlags().VisitAll(func(f *pflag.Flag) {
		if !f.Changed {
			return
		}
		if f.Value.Type() == "count" {
			n, _ := strconv.Atoi(f.Value.String())
			for i := 0; i < n; i++ {
				out = append(out, "-v")
			}
			return
		}
		out = append(out, "--"+f.Name+"="+f.Value.String())
	})
	return out
}

func run(a *app.App, build func() *cobra.Command, prefix []string) error {
	self, err := os.Executable()
	if err != nil {
		return errs.New(errs.CodeInternal, "cannot find the kcctl executable: %v", err)
	}
	st := a.Printer.ErrStyle
	tree := build()
	complete := ProcessCompleter(self)
	hist := LoadHistory()
	fmt.Fprintln(os.Stderr, st.Dim(greeting))
	for {
		fmt.Fprintln(os.Stderr, statusLine(a, st, prefix))
		m := newPrompt(tree, complete, hist, st, prefix)
		p := tea.NewProgram(m, tea.WithOutput(os.Stderr), tea.WithInput(os.Stdin))
		if _, err := p.Run(); err != nil {
			return errs.New(errs.CodeInternal, "prompt failed: %v", err)
		}
		if m.outcome == outcomeExit {
			return nil
		}
		line := strings.TrimSpace(m.line())
		if line == "" {
			continue
		}
		hist.Add(line)
		words, _ := Split(line)
		args := stripLeadingName(Texts(words))
		if len(args) == 0 {
			continue
		}
		switch args[0] {
		case "exit", "quit":
			return nil
		case "clear":
			fmt.Fprint(os.Stderr, "\x1b[H\x1b[2J")
			continue
		case "interactive", "i", "shell":
			fmt.Fprintln(os.Stderr, st.Yellow("Already in interactive mode."))
			continue
		}
		runChild(self, append(append([]string{}, prefix...), args...))
	}
}

// runChild runs one line. Ctrl-C goes to the child (same process group);
// the session ignores it meanwhile so it survives.
func runChild(self string, args []string) {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	defer signal.Stop(sig)
	c := exec.Command(self, args...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	c.Env = append(os.Environ(), "KCCTL_INTERACTIVE=1")
	_ = c.Run()
}

// statusLine shows where the next line will act, without network calls:
// context · server · realm · version · user · expiry. Production-looking
// contexts are red so they're hard to miss.
func statusLine(a *app.App, st output.Style, prefix []string) string {
	cfg, err := config.Load()
	if err != nil {
		return st.Red("config: " + err.Error())
	}
	flags := overridesFrom(prefix)
	r, err := config.Resolve(cfg, flags, config.EnvOverrides())
	if r == nil || r.Server == "" {
		if err != nil {
			return st.Yellow(errs.As(err).Message + " — " + errs.As(err).Hint)
		}
		return st.Yellow("no server configured")
	}
	var parts []string
	sep := st.Dim(" · ")
	name := r.Name
	if name == "" {
		name = "(no context)"
	}
	if isProd(name) || isProd(r.Server) {
		parts = append(parts, "context: "+st.BoldRed(name))
	} else {
		parts = append(parts, "context: "+st.Cyan(name))
	}
	parts = append(parts, "server: "+r.Server, "realm: "+st.Bold(r.Realm))
	if v := kc.CachedVersion(r.Server); v != "" {
		parts = append(parts, "keycloak "+v)
	}
	if r.Token != "" {
		parts = append(parts, st.Green("using --token"))
		return strings.Join(parts, sep)
	}
	s, _ := a.Store.Load(r.Key)
	switch {
	case s == nil:
		parts = append(parts, st.Yellow("not signed in"))
	default:
		if s.Username != "" {
			parts = append(parts, "user: "+st.Green(s.Username))
		}
		switch {
		case s.AccessValid(0):
			parts = append(parts, "expires in "+humanDur(time.Until(s.ExpiresAt)))
		case s.RefreshValid():
			parts = append(parts, st.Dim("refreshes on next command"))
		case s.Grant == config.GrantClientCredentials && s.ClientSecret != "":
			parts = append(parts, st.Dim("renews on next command"))
		default:
			parts = append(parts, st.Yellow("session expired, run 'auth login'"))
		}
	}
	return strings.Join(parts, sep)
}

func overridesFrom(prefix []string) config.Overrides {
	var o config.Overrides
	for _, f := range prefix {
		k, v, _ := strings.Cut(strings.TrimPrefix(f, "--"), "=")
		switch k {
		case "context":
			o.Context = v
		case "server":
			o.Server = v
		case "realm":
			o.Realm = v
		case "auth-realm":
			o.AuthRealm = v
		case "client-id":
			o.ClientID = v
		case "token":
			o.Token = v
		}
	}
	return o
}

func isProd(s string) bool {
	s = strings.ToLower(s)
	for _, p := range []string{"prod", "production", "live", "prd"} {
		if strings.Contains(s, p) {
			return true
		}
	}
	return false
}

func humanDur(d time.Duration) string {
	switch {
	case d < time.Minute:
		return strconv.Itoa(int(d.Seconds())) + "s"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + "m"
	default:
		return strconv.Itoa(int(d.Hours())) + "h"
	}
}

func itoa(i int) string { return strconv.Itoa(i) }
