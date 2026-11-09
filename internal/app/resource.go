package app

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/emdzej/kcctl/internal/errs"
	"github.com/emdzej/kcctl/internal/kc"
	"github.com/emdzej/kcctl/internal/output"
)

type FieldKind int

const (
	String FieldKind = iota
	Bool
	Int
	Strings  // repeatable / comma-separated → JSON array
	KV       // repeatable key=value → JSON object of string arrays (attributes)
	KVString // repeatable key=value → JSON object of strings (config maps)
)

// Field maps a typed flag onto a body path, so common fields don't need -s.
type Field struct {
	Flag  string
	Path  string // dotted; defaults to Flag
	Kind  FieldKind
	Usage string
}

type Fields []Field

type fieldValues struct {
	fields Fields
	str    map[string]*string
	b      map[string]*bool
	i      map[string]*int
	list   map[string]*[]string
}

func (fs Fields) Bind(cmd *cobra.Command) *fieldValues {
	fv := &fieldValues{fields: fs, str: map[string]*string{}, b: map[string]*bool{}, i: map[string]*int{}, list: map[string]*[]string{}}
	for _, f := range fs {
		switch f.Kind {
		case String:
			fv.str[f.Flag] = cmd.Flags().String(f.Flag, "", f.Usage)
		case Bool:
			fv.b[f.Flag] = cmd.Flags().Bool(f.Flag, false, f.Usage)
		case Int:
			fv.i[f.Flag] = cmd.Flags().Int(f.Flag, 0, f.Usage)
		case Strings:
			fv.list[f.Flag] = cmd.Flags().StringSlice(f.Flag, nil, f.Usage)
		case KV, KVString:
			fv.list[f.Flag] = cmd.Flags().StringArray(f.Flag, nil, f.Usage+" (key=value, repeatable)")
		}
	}
	return fv
}

// Apply writes only flags the user actually set, so update never clobbers
// a field with a zero value.
func (fv *fieldValues) Apply(cmd *cobra.Command, body map[string]any) error {
	for _, f := range fv.fields {
		if !cmd.Flags().Changed(f.Flag) {
			continue
		}
		p := f.Path
		if p == "" {
			p = f.Flag
		}
		segs := strings.Split(p, ".")
		var v any
		switch f.Kind {
		case String:
			v = *fv.str[f.Flag]
		case Bool:
			v = *fv.b[f.Flag]
		case Int:
			v = *fv.i[f.Flag]
		case Strings:
			l := make([]any, 0, len(*fv.list[f.Flag]))
			for _, s := range *fv.list[f.Flag] {
				l = append(l, s)
			}
			v = l
		case KV, KVString:
			existing := map[string]any{}
			if cur, ok := lookupPath(body, segs).(map[string]any); ok {
				existing = cur
			}
			for _, kv := range *fv.list[f.Flag] {
				k, val, ok := strings.Cut(kv, "=")
				if !ok || k == "" {
					return errs.Usage("--%s %q must be key=value", f.Flag, kv)
				}
				switch {
				case f.Kind == KVString:
					existing[k] = val
				case val == "":
					delete(existing, k)
				default:
					existing[k] = []any{val}
				}
			}
			v = existing
		}
		if err := SetPath(body, segs, v); err != nil {
			return err
		}
	}
	return nil
}

func lookupPath(m map[string]any, segs []string) any {
	var cur any = m
	for _, s := range segs {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = mm[s]
	}
	return cur
}

// Resource generates list/get/create/update/delete for one collection, so
// every resource behaves identically: id-or-name refs, -f/-s bodies,
// GET-overlay-PUT updates, completion, and confirmation for bulk deletes.
type Resource struct {
	Noun   string
	Plural string
	Lookup Lookup
	// KeyByName: the item path uses the name itself (roles, realms, IdPs).
	KeyByName bool
	Columns   []output.Column
	// Paged lists accept first/max.
	Paged bool
	// ListFlags binds extra list filters and returns a function that adds
	// them to the query.
	ListFlags func(cmd *cobra.Command) func(q url.Values)
	Fields    Fields
	// CreateArg is the body field filled from create's optional positional.
	CreateArg string
	Required  []string
	Defaults  map[string]any
	// BeforeWrite can adjust the body (e.g. drop masked secrets) before
	// create (update=false) or update (update=true).
	BeforeWrite func(ctx context.Context, a *App, body map[string]any, update bool) error
	// UpdateMethod overrides PUT for the rare endpoint that differs.
	UpdateMethod string
	// ConfirmDelete: deleting one of these removes more than the thing
	// named (a realm, a group with members), so even one needs --yes.
	ConfirmDelete bool
	Example       map[string]string // per verb
}

func (r *Resource) Key(ctx context.Context, c *kc.Client, ref string) (string, error) {
	if r.KeyByName {
		if ref == "" {
			return "", errs.Usage("%s name is required", r.Noun)
		}
		return ref, nil
	}
	return r.Lookup.Find(ctx, c, ref)
}

func (r *Resource) ItemPath(ctx context.Context, c *kc.Client, key string) (string, error) {
	base, err := r.Lookup.Path(ctx, c)
	if err != nil {
		return "", err
	}
	return base + "/" + url.PathEscape(key), nil
}

// RefCompletion completes the first positional as name (id).
func (r *Resource) RefCompletion(a *App) cobra.CompletionFunc {
	return a.Fetched(r.Plural, r.Lookup.Completer())
}

// Resolve does Key + ItemPath, the common start of every per-item command.
func (r *Resource) Resolve(ctx context.Context, a *App, ref string) (*kc.Client, string, error) {
	c, err := a.Client()
	if err != nil {
		return nil, "", err
	}
	key, err := r.Key(ctx, c, ref)
	if err != nil {
		return nil, "", err
	}
	p, err := r.ItemPath(ctx, c, key)
	return c, p, err
}

func (r *Resource) CRUD(a *App) []*cobra.Command {
	return []*cobra.Command{r.ListCmd(a), r.GetCmd(a), r.CreateCmd(a), r.UpdateCmd(a), r.DeleteCmd(a)}
}

func (r *Resource) ListCmd(a *App) *cobra.Command {
	var first, maxN int
	var all bool
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List " + r.Plural,
		Example: r.Example["list"],
		Args:    cobra.NoArgs,
	}
	var addQuery func(url.Values)
	if r.ListFlags != nil {
		addQuery = r.ListFlags(cmd)
	}
	if r.Paged {
		cmd.Flags().IntVar(&first, "first", 0, "skip this many results")
		cmd.Flags().IntVar(&maxN, "max", 0, "return at most this many results (server default if unset)")
		cmd.Flags().BoolVar(&all, "all", false, "fetch every page")
	}
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		ctx := cmd.Context()
		c, err := a.Client()
		if err != nil {
			return err
		}
		p, err := r.Lookup.Path(ctx, c)
		if err != nil {
			return err
		}
		q := url.Values{}
		if addQuery != nil {
			addQuery(q)
		}
		if !r.Paged {
			v, err := c.Get(ctx, p, q)
			if err != nil {
				return err
			}
			return a.Printer.Print(v, r.Columns)
		}
		if !all {
			if cmd.Flags().Changed("first") {
				q.Set("first", strconv.Itoa(first))
			}
			if cmd.Flags().Changed("max") {
				q.Set("max", strconv.Itoa(maxN))
			}
			v, err := c.Get(ctx, p, q)
			if err != nil {
				return err
			}
			return a.Printer.Print(v, r.Columns)
		}
		items, err := Paginate(ctx, c, p, q, first)
		if err != nil {
			return err
		}
		return a.Printer.Print(items, r.Columns)
	}
	return cmd
}

// Paginate walks first/max pages until a short page.
func Paginate(ctx context.Context, c *kc.Client, p string, q url.Values, start int) ([]any, error) {
	const page = 100
	out := []any{}
	for off := start; ; off += page {
		q.Set("first", strconv.Itoa(off))
		q.Set("max", strconv.Itoa(page))
		v, err := c.Get(ctx, p, q)
		if err != nil {
			return nil, err
		}
		l, _ := v.([]any)
		out = append(out, l...)
		if len(l) < page {
			return out, nil
		}
	}
}

func (r *Resource) refUse() string {
	if r.KeyByName {
		return "<name>"
	}
	return "<id|name>"
}

func (r *Resource) GetCmd(a *App) *cobra.Command {
	return &cobra.Command{
		Use:               "get " + r.refUse(),
		Short:             "Show one " + r.Noun,
		Example:           r.Example["get"],
		Args:              ExactArgs(1, r.refUse()),
		ValidArgsFunction: NoMoreArgs(1, r.RefCompletion(a)),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, p, err := r.Resolve(cmd.Context(), a, args[0])
			if err != nil {
				return err
			}
			v, err := c.Get(cmd.Context(), p, nil)
			if err != nil {
				return err
			}
			return a.Printer.Print(v, r.Columns)
		},
	}
}

func (r *Resource) CreateCmd(a *App) *cobra.Command {
	var body BodyFlags
	use := "create"
	if r.CreateArg != "" {
		use += " [" + r.CreateArg + "]"
	}
	cmd := &cobra.Command{
		Use:     use,
		Short:   "Create a " + r.Noun,
		Example: r.Example["create"],
		Args:    cobra.MaximumNArgs(boolInt(r.CreateArg != "")),
	}
	fv := r.Fields.Bind(cmd)
	body.Bind(cmd)
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		b := map[string]any{}
		for k, v := range r.Defaults {
			b[k] = v
		}
		b, err := body.Build(a, b)
		if err != nil {
			return err
		}
		if len(args) == 1 {
			b[r.CreateArg] = args[0]
		}
		if err := fv.Apply(cmd, b); err != nil {
			return err
		}
		if err := RequireFields(b, r.Required...); err != nil {
			return err
		}
		if r.BeforeWrite != nil {
			if err := r.BeforeWrite(ctx, a, b, false); err != nil {
				return err
			}
		}
		c, err := a.Client()
		if err != nil {
			return err
		}
		p, err := r.Lookup.Path(ctx, c)
		if err != nil {
			return err
		}
		resp, err := c.Post(ctx, p, b)
		if err != nil {
			return err
		}
		return a.ReportCreated(r.Noun, displayName(b, r), resp.CreatedID())
	}
	return cmd
}

func displayName(b map[string]any, r *Resource) string {
	for _, k := range []string{r.Lookup.NameField, r.CreateArg, "name", "alias"} {
		if s, ok := b[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// ReportCreated prints the new id: on stdout for structured output (so
// scripts can capture it), as a success note otherwise.
func (a *App) ReportCreated(noun, name, id string) error {
	if a.Printer.IsStructured() {
		return a.Printer.Print(map[string]any{"id": id, "name": name}, nil)
	}
	switch {
	case name != "" && id != "" && id != name:
		a.Printer.Success("Created %s '%s' (%s).", noun, name, id)
	case name != "":
		a.Printer.Success("Created %s '%s'.", noun, name)
	default:
		a.Printer.Success("Created %s %s.", noun, id)
	}
	return nil
}

func (r *Resource) UpdateCmd(a *App) *cobra.Command {
	var body BodyFlags
	cmd := &cobra.Command{
		Use:               "update " + r.refUse(),
		Short:             "Change a " + r.Noun + " (reads it, applies your changes, writes it back)",
		Example:           r.Example["update"],
		Args:              ExactArgs(1, r.refUse()),
		ValidArgsFunction: NoMoreArgs(1, r.RefCompletion(a)),
	}
	fv := r.Fields.Bind(cmd)
	body.Bind(cmd)
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		if !body.Given() && !anyChanged(cmd, r.Fields) {
			return errs.Usage("nothing to change").WithHint("Pass field flags, -s path=value, or -f file.")
		}
		c, p, err := r.Resolve(ctx, a, args[0])
		if err != nil {
			return err
		}
		cur, err := c.Get(ctx, p, nil)
		if err != nil {
			return err
		}
		b, _ := cur.(map[string]any)
		if b, err = body.Build(a, b); err != nil {
			return err
		}
		if err := fv.Apply(cmd, b); err != nil {
			return err
		}
		if r.BeforeWrite != nil {
			if err := r.BeforeWrite(ctx, a, b, true); err != nil {
				return err
			}
		}
		method := r.UpdateMethod
		if method == "" {
			method = "PUT"
		}
		if _, err := c.Do(ctx, kc.Request{Method: method, Path: p, Body: b}); err != nil {
			return err
		}
		a.Printer.Success("Updated %s '%s'.", r.Noun, args[0])
		return nil
	}
	return cmd
}

func anyChanged(cmd *cobra.Command, fs Fields) bool {
	for _, f := range fs {
		if cmd.Flags().Changed(f.Flag) {
			return true
		}
	}
	return false
}

func (r *Resource) DeleteCmd(a *App) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:               "delete " + r.refUse() + "...",
		Aliases:           []string{"rm"},
		Short:             "Delete " + r.Plural,
		Example:           r.Example["delete"],
		Args:              MinArgs(1, r.refUse()),
		ValidArgsFunction: r.RefCompletion(a),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			// A single named delete is its own confirmation; several are not.
			switch {
			case r.ConfirmDelete:
				what := fmt.Sprintf("This deletes %s '%s' and everything in it.", r.Noun, strings.Join(args, "', '"))
				if err := a.Confirm(yes, what); err != nil {
					return err
				}
			case len(args) > 1:
				c, err := a.Client()
				if err != nil {
					return err
				}
				if err := a.Confirm(yes, fmt.Sprintf("This deletes %d %s in realm '%s'.", len(args), r.Plural, c.Realm)); err != nil {
					return err
				}
			}
			for _, ref := range args {
				c, p, err := r.Resolve(ctx, a, ref)
				if err != nil {
					return err
				}
				if _, err := c.Delete(ctx, p, nil); err != nil {
					return err
				}
				a.Printer.Success("Deleted %s '%s'.", r.Noun, ref)
			}
			return nil
		},
	}
	YesFlag(cmd, &yes)
	return cmd
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ExactArgs/MinArgs give usage errors that name the missing argument.
func ExactArgs(n int, names string) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) < n {
			return errs.Usage("missing argument %s", names).WithHint("See '" + cmd.CommandPath() + " --help'.")
		}
		if len(args) > n {
			return errs.Usage("too many arguments: expected %d but got %d", n, len(args))
		}
		return nil
	}
}

func MinArgs(n int, names string) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) < n {
			return errs.Usage("missing argument %s", names).WithHint("See '" + cmd.CommandPath() + " --help'.")
		}
		return nil
	}
}

// Group makes a parent command that only holds subcommands.
func Group(use, short string, aliases ...string) *cobra.Command {
	return &cobra.Command{Use: use, Short: short, Aliases: aliases, Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() }}
}
