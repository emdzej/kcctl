package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/emdzej/kcctl/internal/errs"
	"github.com/emdzej/kcctl/internal/kc"
)

// Item is one completion candidate. Desc is shown beside it; for resources
// with both a name and an id, Value is the name and Desc carries the id, and
// either is accepted on the line.
type Item struct {
	Value string
	Desc  string
}

func Values(vals ...string) cobra.CompletionFunc {
	return func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		d := cobra.ShellCompDirectiveNoFileComp
		for _, v := range vals {
			if strings.HasSuffix(v, "=") {
				d |= cobra.ShellCompDirectiveNoSpace
			}
		}
		return vals, d
	}
}

// Fetcher lists candidates from the server.
type Fetcher func(ctx context.Context, c *kc.Client, args []string, toComplete string) ([]Item, error)

// Fetched is the single source of remote completions for both the shell and
// the interactive prompt. Failures become an ActiveHelp line ("could not list
// users (403)") rather than silently empty results.
func (a *App) Fetched(what string, fn Fetcher) cobra.CompletionFunc {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		ctx, cancel := CompletionContext()
		defer cancel()
		c, err := a.Client()
		if err != nil {
			return cobra.AppendActiveHelp(nil, "could not list "+what+": "+firstLine(err)), cobra.ShellCompDirectiveNoFileComp
		}
		items, err := fn(ctx, c, args, toComplete)
		if err != nil {
			return cobra.AppendActiveHelp(nil, "could not list "+what+" ("+reason(err)+")"), cobra.ShellCompDirectiveNoFileComp
		}
		out := make([]string, 0, len(items))
		for _, it := range items {
			if !strings.HasPrefix(strings.ToLower(it.Value), strings.ToLower(toComplete)) {
				continue
			}
			if it.Desc != "" {
				out = append(out, it.Value+"\t"+it.Desc)
			} else {
				out = append(out, it.Value)
			}
		}
		return out, cobra.ShellCompDirectiveNoFileComp
	}
}

func reason(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timed out"
	}
	e := errs.As(err)
	if e.Request != nil {
		return fmt.Sprint(e.Request.Status)
	}
	return firstLine(err)
}

func firstLine(err error) string {
	s, _, _ := strings.Cut(err.Error(), "\n")
	return s
}

// CompleteRealms lists realm names; used by --realm everywhere.
func (a *App) CompleteRealms(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	return a.Fetched("realms", func(ctx context.Context, c *kc.Client, _ []string, _ string) ([]Item, error) {
		v, err := c.Get(ctx, "/admin/realms", map[string][]string{"briefRepresentation": {"true"}})
		if err != nil {
			return nil, err
		}
		return ItemsFrom(v, "realm", "displayName"), nil
	})(cmd, args, toComplete)
}

// ItemsFrom turns a JSON list into items using valueField and descField.
func ItemsFrom(v any, valueField, descField string) []Item {
	list, _ := v.([]any)
	out := make([]Item, 0, len(list))
	for _, x := range list {
		m, _ := x.(map[string]any)
		val, _ := m[valueField].(string)
		if val == "" {
			continue
		}
		desc, _ := m[descField].(string)
		out = append(out, Item{Value: val, Desc: desc})
	}
	return out
}

// NoMoreArgs stops completion after n positionals.
func NoMoreArgs(n int, f cobra.CompletionFunc) cobra.CompletionFunc {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) >= n {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return f(cmd, args, toComplete)
	}
}
