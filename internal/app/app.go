// Package app is the CLI runtime shared by every command package: global
// flags, lazily resolved connection, printer, and helpers for the conventions
// (id-or-name refs, body input, confirmation, secrets, completion).
package app

import (
	"context"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/emdzej/kcctl/internal/auth"
	"github.com/emdzej/kcctl/internal/compat"
	"github.com/emdzej/kcctl/internal/config"
	"github.com/emdzej/kcctl/internal/errs"
	"github.com/emdzej/kcctl/internal/httpx"
	"github.com/emdzej/kcctl/internal/kc"
	"github.com/emdzej/kcctl/internal/output"
)

// Version is stamped by the release build.
var Version = "0.0.0-dev"

type Flags struct {
	Context   string
	Server    string
	Realm     string
	AuthRealm string
	ClientID  string
	Token     string
	Output    string
	Color     string
	NoColor   bool
	Quiet     bool
	NoInput   bool
	Verbose   int
	Insecure  bool
}

type App struct {
	Flags   Flags
	In      io.Reader
	Out     io.Writer
	Err     io.Writer
	Printer *output.Printer
	Store   auth.Store
	// StdinTTY/StderrTTY are captured once so tests can fake a terminal.
	StdinTTY  bool
	StderrTTY bool
	StdoutTTY bool

	cfg      *config.File
	resolved *config.Resolved
	tokens   *auth.Manager
	client   *kc.Client
	profile  *compat.Profile
}

func New() *App {
	return &App{
		In: os.Stdin, Out: os.Stdout, Err: os.Stderr,
		Store:     auth.DefaultStore(),
		StdinTTY:  term.IsTerminal(int(os.Stdin.Fd())),
		StderrTTY: term.IsTerminal(int(os.Stderr.Fd())),
		StdoutTTY: term.IsTerminal(int(os.Stdout.Fd())),
	}
}

// BindGlobals declares the global flags exactly once, on the root. Nothing has
// a default here: defaults are applied in Resolve so a context value is never
// shadowed by a flag default.
func (a *App) BindGlobals(root *cobra.Command) {
	f := root.PersistentFlags()
	f.StringVar(&a.Flags.Context, "context", "", "context to use (default: current context)")
	f.StringVar(&a.Flags.Server, "server", "", "Keycloak base URL, e.g. https://sso.example.com")
	f.StringVarP(&a.Flags.Realm, "realm", "r", "", "realm to act on (default: context realm, then auth realm)")
	f.StringVar(&a.Flags.AuthRealm, "auth-realm", "", "realm to sign in to (default: master)")
	f.StringVar(&a.Flags.ClientID, "client-id", "", "OAuth client used to sign in (default: admin-cli)")
	f.StringVar(&a.Flags.Token, "token", "", "use this access token instead of the stored session")
	f.StringVarP(&a.Flags.Output, "output", "o", "", "output format: table|vtable|json|yaml|jsonpath=<expr>|custom-columns=<H:path,...>")
	f.StringVar(&a.Flags.Color, "color", "", "color output: auto|always|never")
	f.BoolVar(&a.Flags.NoColor, "no-color", false, "disable color")
	f.BoolVarP(&a.Flags.Quiet, "quiet", "q", false, "suppress notes and success messages")
	f.BoolVar(&a.Flags.NoInput, "no-input", false, "never prompt; fail instead (for scripts)")
	f.CountVarP(&a.Flags.Verbose, "verbose", "v", "trace requests (-v), with headers and bodies (-vv)")
	f.BoolVar(&a.Flags.Insecure, "insecure", false, "skip TLS certificate verification")
	f.Lookup("color").NoOptDefVal = "always"

	root.RegisterFlagCompletionFunc("output", Values("table", "vtable", "json", "yaml", "jsonpath=", "custom-columns="))
	root.RegisterFlagCompletionFunc("color", Values("auto", "always", "never"))
	root.RegisterFlagCompletionFunc("context", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		cfg, err := config.Load()
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		var out []string
		for _, c := range cfg.Contexts {
			out = append(out, c.Name+"\t"+c.Server)
		}
		return out, cobra.ShellCompDirectiveNoFileComp
	})
	root.RegisterFlagCompletionFunc("realm", a.CompleteRealms)
	root.RegisterFlagCompletionFunc("auth-realm", a.CompleteRealms)
}

// Init runs after flag parsing, before any command.
func (a *App) Init() error {
	v := a.Flags.Verbose
	if v == 0 {
		switch os.Getenv("KCCTL_VERBOSE") {
		case "1":
			v = 1
		case "2":
			v = 2
		}
	}
	a.Flags.Verbose = v
	httpx.Verbosity = v
	mode := output.ColorMode(a.Flags.Color)
	if a.Flags.NoColor {
		mode = output.ColorNever
	}
	switch mode {
	case "", output.ColorAuto, output.ColorAlways, output.ColorNever:
	default:
		return errs.Usage("--color must be auto, always or never")
	}
	if err := output.ValidateFormat(a.Flags.Output); err != nil {
		return err
	}
	a.Printer = &output.Printer{
		Format:   a.Flags.Output,
		Out:      a.Out,
		Err:      a.Err,
		OutStyle: output.NewStyle(output.UseColor(mode, fileOf(a.Out))),
		ErrStyle: output.NewStyle(output.UseColor(mode, fileOf(a.Err))),
		Quiet:    a.Flags.Quiet,
		Verbose:  v,
	}
	if a.StdoutTTY {
		a.Printer.MaxCell = 60
	}
	return nil
}

// EnsurePrinter covers errors raised before Init ran (flag parse failures).
func (a *App) EnsurePrinter() *output.Printer {
	if a.Printer == nil {
		a.Printer = &output.Printer{Out: a.Out, Err: a.Err, Format: a.Flags.Output,
			ErrStyle: output.NewStyle(output.UseColor(output.ColorAuto, fileOf(a.Err)))}
	}
	return a.Printer
}

func fileOf(w io.Writer) *os.File {
	f, _ := w.(*os.File)
	return f
}

// CanPrompt is true only with a terminal on both ends and --no-input unset.
func (a *App) CanPrompt() bool {
	return !a.Flags.NoInput && a.StdinTTY && a.StderrTTY
}

func (a *App) Config() (*config.File, error) {
	if a.cfg == nil {
		c, err := config.Load()
		if err != nil {
			return nil, err
		}
		a.cfg = c
	}
	return a.cfg, nil
}

func (a *App) overrides() config.Overrides {
	return config.Overrides{
		Context: a.Flags.Context, Server: a.Flags.Server, AuthRealm: a.Flags.AuthRealm,
		Realm: a.Flags.Realm, ClientID: a.Flags.ClientID, Token: a.Flags.Token, Insecure: a.Flags.Insecure,
	}
}

func (a *App) Resolve() (*config.Resolved, error) {
	if a.resolved != nil {
		return a.resolved, nil
	}
	cfg, err := a.Config()
	if err != nil {
		return nil, err
	}
	r, err := config.Resolve(cfg, a.overrides(), config.EnvOverrides())
	if err != nil {
		return nil, err
	}
	a.resolved = r
	return r, nil
}

func (a *App) AuthClient() (*auth.Client, error) {
	r, err := a.Resolve()
	if err != nil {
		return nil, err
	}
	h, err := httpx.NewClient(httpx.Options{CAFile: r.CAFile, Insecure: r.Insecure})
	if err != nil {
		return nil, err
	}
	return &auth.Client{HTTP: h, Server: r.Server, AuthRealm: r.AuthRealm, ClientID: r.ClientID}, nil
}

func (a *App) Tokens() (*auth.Manager, error) {
	if a.tokens != nil {
		return a.tokens, nil
	}
	r, err := a.Resolve()
	if err != nil {
		return nil, err
	}
	ac, err := a.AuthClient()
	if err != nil {
		return nil, err
	}
	a.tokens = &auth.Manager{Resolved: r, Client: ac, Store: a.Store}
	return a.tokens, nil
}

// Client is the admin API client scoped to the resolved target realm.
func (a *App) Client() (*kc.Client, error) {
	if a.client != nil {
		return a.client, nil
	}
	r, err := a.Resolve()
	if err != nil {
		return nil, err
	}
	tm, err := a.Tokens()
	if err != nil {
		return nil, err
	}
	h, err := httpx.NewClient(httpx.Options{CAFile: r.CAFile, Insecure: r.Insecure})
	if err != nil {
		return nil, err
	}
	a.client = &kc.Client{HTTP: h, Server: r.Server, Realm: r.Realm, Tokens: tm, UserAgent: "kcctl/" + Version}
	return a.client, nil
}

// Profile is the target server's version/features, cached across runs.
func (a *App) Profile(ctx context.Context) (*compat.Profile, error) {
	if a.profile != nil {
		return a.profile, nil
	}
	c, err := a.Client()
	if err != nil {
		return nil, err
	}
	p, err := c.Profile(ctx, false)
	if err != nil {
		return nil, err
	}
	if !p.Version.AtLeast(compat.Minimum) {
		a.EnsurePrinter().Warn("Keycloak %s is older than %s, the oldest version kcctl supports; some commands may fail.", p.Version, compat.Minimum)
	}
	a.profile = p
	return p, nil
}

// Require fails with a clear message when the server lacks a capability.
func (a *App) Require(ctx context.Context, c compat.Capability) error {
	p, err := a.Profile(ctx)
	if err != nil {
		return err
	}
	if !p.Has(c) {
		return errs.New(errs.CodeUsage, "%s.", capitalize(p.Unsupported(c)))
	}
	return nil
}

// Has is the soft variant of Require for choosing between endpoint shapes.
func (a *App) Has(ctx context.Context, c compat.Capability) (bool, error) {
	p, err := a.Profile(ctx)
	if err != nil {
		return false, err
	}
	return p.Has(c), nil
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// CompletionContext bounds remote lookups so a slow server never blocks a
// shell's Tab; the REPL passes its own longer deadline via the env.
func CompletionContext() (context.Context, context.CancelFunc) {
	d := 1500 * time.Millisecond
	if v := os.Getenv("KCCTL_COMPLETE_TIMEOUT"); v != "" {
		if pd, err := time.ParseDuration(v); err == nil {
			d = pd
		}
	}
	return context.WithTimeout(context.Background(), d)
}
