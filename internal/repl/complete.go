package repl

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// Suggestion is one candidate from cobra's completion protocol.
type Suggestion struct {
	Value string
	Desc  string
}

type Completion struct {
	Items   []Suggestion
	NoSpace bool
	// Help carries ActiveHelp lines, e.g. "could not list users (403)".
	Help []string
}

// Completer returns candidates for the word being typed after args.
type Completer func(args []string, toComplete string) Completion

// ProcessCompleter asks a child `kcctl __complete`, exactly as a shell does,
// so the prompt and shell completion are one code path. A child per request
// also keeps state isolated: cobra registers flag completions in a global map
// that would grow with every in-process tree.
func ProcessCompleter(self string) Completer {
	return func(args []string, toComplete string) Completion {
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		c := exec.CommandContext(ctx, self, append(append([]string{cobra.ShellCompRequestCmd}, args...), toComplete)...)
		c.Env = append(os.Environ(), "KCCTL_COMPLETE_TIMEOUT=10s")
		out, _ := c.Output()
		if ctx.Err() != nil {
			return Completion{Help: []string{"suggestions timed out"}}
		}
		return parseCompletion(string(out))
	}
}

// TreeCompleter runs __complete in-process on a fresh tree; used in tests.
func TreeCompleter(build func() *cobra.Command) Completer {
	return func(args []string, toComplete string) Completion {
		root := build()
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetErr(io.Discard)
		root.SetArgs(append(append([]string{cobra.ShellCompRequestCmd}, args...), toComplete))
		_ = root.Execute()
		return parseCompletion(out.String())
	}
}

func parseCompletion(s string) Completion {
	var c Completion
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for _, l := range lines {
		switch {
		case l == "":
		case strings.HasPrefix(l, ":"):
			d, err := strconv.Atoi(l[1:])
			if err == nil {
				c.NoSpace = cobra.ShellCompDirective(d)&cobra.ShellCompDirectiveNoSpace != 0
			}
		case strings.HasPrefix(l, "_activeHelp_ "):
			c.Help = append(c.Help, strings.TrimPrefix(l, "_activeHelp_ "))
		case strings.HasPrefix(l, "Completion ended with directive"):
		default:
			v, d, _ := strings.Cut(l, "\t")
			c.Items = append(c.Items, Suggestion{Value: v, Desc: d})
		}
	}
	return c
}

// Diagnose finds the first word that doesn't fit the command tree: an unknown
// subcommand or flag. It returns the word index (-1 if none) and a message.
// It is advisory only; Enter still runs the line.
// root is only read, so one tree serves the whole session.
func Diagnose(root *cobra.Command, words []string) (int, string) {
	// help and completion are only added when a tree executes.
	root.InitDefaultHelpCmd()
	root.InitDefaultCompletionCmd()
	cmd := root
	for i, w := range words {
		if strings.HasPrefix(w, "-") {
			name := strings.TrimLeft(w, "-")
			name, _, _ = strings.Cut(name, "=")
			if name == "" {
				continue
			}
			var known bool
			if strings.HasPrefix(w, "--") {
				known = lookupFlag(cmd, name, false) != nil
			} else {
				known = true
				// -vv is two flags; in -ojson the rest after -o is its value.
				for _, r := range name {
					f := lookupFlag(cmd, string(r), true)
					if f == nil {
						known = false
						break
					}
					if f.Value.Type() != "bool" && f.Value.Type() != "count" {
						break
					}
				}
			}
			if !known {
				return i, "unknown flag '" + w + "' for '" + cmd.CommandPath() + "'"
			}
			continue
		}
		if i > 0 && takesValue(cmd, words[i-1]) {
			continue
		}
		if !cmd.HasAvailableSubCommands() {
			continue
		}
		next := findSub(cmd, w)
		if next == nil {
			return i, "unknown command '" + w + "' for '" + cmd.CommandPath() + "'"
		}
		cmd = next
	}
	return -1, ""
}

func findSub(cmd *cobra.Command, name string) *cobra.Command {
	for _, c := range cmd.Commands() {
		if c.Name() == name || c.HasAlias(name) {
			return c
		}
	}
	return nil
}

// takesValue reports whether prev is a flag that consumes the next word.
func takesValue(cmd *cobra.Command, prev string) bool {
	if !strings.HasPrefix(prev, "-") || strings.Contains(prev, "=") {
		return false
	}
	name := strings.TrimLeft(prev, "-")
	f := lookupFlag(cmd, name, false)
	if f == nil && !strings.HasPrefix(prev, "--") && len(name) == 1 {
		f = lookupFlag(cmd, name, true)
	}
	return f != nil && f.NoOptDefVal == "" && f.Value.Type() != "bool"
}

// lookupFlag searches local, own persistent and inherited flags; before a
// tree executes, cobra hasn't merged them into Flags().
func lookupFlag(cmd *cobra.Command, name string, short bool) *pflag.Flag {
	for _, fs := range []*pflag.FlagSet{cmd.Flags(), cmd.PersistentFlags(), cmd.InheritedFlags()} {
		var f *pflag.Flag
		if short {
			f = fs.ShorthandLookup(name)
		} else {
			f = fs.Lookup(name)
		}
		if f != nil {
			return f
		}
	}
	return nil
}
