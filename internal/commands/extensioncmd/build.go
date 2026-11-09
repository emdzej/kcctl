// Package extensioncmd turns extension manifests into commands and manages
// them (`kcctl extension ...`). A built command behaves like a built-in one:
// id-or-name lookups, completion, output formats, confirmation, secrets
// only from files or prompts.
package extensioncmd

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/emdzej/kcctl/internal/app"
	"github.com/emdzej/kcctl/internal/compat"
	"github.com/emdzej/kcctl/internal/errs"
	"github.com/emdzej/kcctl/internal/extension"
	"github.com/emdzej/kcctl/internal/kc"
	"github.com/emdzej/kcctl/internal/output"
)

// GroupID is the help section extension commands are listed under.
const GroupID = "extensions"

// Build returns the top-level commands of one manifest.
func Build(a *app.App, m *extension.Manifest) []*cobra.Command {
	out := make([]*cobra.Command, 0, len(m.Commands))
	for i := range m.Commands {
		cmd := group(a, m, &m.Commands[i], "")
		cmd.GroupID = GroupID
		out = append(out, cmd)
	}
	return out
}

func group(a *app.App, m *extension.Manifest, g *extension.Group, parentBase string) *cobra.Command {
	short := g.Short
	if short == "" {
		short = "Commands from the " + m.Name + " extension"
	}
	cmd := app.Group(g.Name, short, g.Aliases...)
	cmd.Annotations = map[string]string{"extension": m.Name}
	base := joinPath(parentBase, g.Base)
	for i := range g.Verbs {
		cmd.AddCommand(verb(a, m, g, &g.Verbs[i], base))
	}
	for i := range g.Groups {
		cmd.AddCommand(group(a, m, &g.Groups[i], base))
	}
	return cmd
}

// joinPath appends p to base; an absolute p (/admin/...) replaces it.
func joinPath(base, p string) string {
	switch {
	case strings.HasPrefix(p, "/"):
		return strings.TrimRight(p, "/")
	case p == "":
		return base
	case base == "":
		return strings.Trim(p, "/")
	}
	return strings.TrimRight(base, "/") + "/" + strings.Trim(p, "/")
}

// requestPath fills placeholders and anchors a relative path at the target
// realm. Values are path-escaped, so a name can never add path segments.
func requestPath(c *kc.Client, p string, vars map[string]string) string {
	filled := extension.Fill(p, func(name string) (string, bool) {
		if name == "realm" {
			return url.PathEscape(c.Realm), true
		}
		v, ok := vars[name]
		return url.PathEscape(v), ok
	})
	if strings.HasPrefix(filled, "/") {
		return filled
	}
	return kc.RealmPathFor(c.Realm) + "/" + filled
}

func verb(a *app.App, m *extension.Manifest, g *extension.Group, v *extension.Verb, base string) *cobra.Command {
	path := joinPath(base, v.Path)
	cmd := &cobra.Command{
		Use:         v.Name + argsUse(v.Args),
		Aliases:     v.Aliases,
		Short:       v.Short,
		Long:        v.Long,
		Example:     indent(v.Example),
		Args:        argCount(v.Args),
		Annotations: map[string]string{"extension": m.Name},
	}
	if cmd.Short == "" {
		cmd.Short = v.Method + " " + path
	}
	cmd.ValidArgsFunction = func(cmd *cobra.Command, args []string, tc string) ([]string, cobra.ShellCompDirective) {
		if len(args) >= len(v.Args) {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		arg := v.Args[len(args)]
		return completion(a, g, base, arg.Lookup, arg.Complete, arg.Values)(cmd, args, tc)
	}

	query := bindQuery(a, cmd, g, base, v.Query)
	fields, secrets := bindFields(a, cmd, g, base, v.Fields)
	var body app.BodyFlags
	if v.Body {
		body.Bind(cmd)
	}
	var first, maxN int
	var all bool
	if v.Paged {
		cmd.Flags().IntVar(&first, "first", 0, "skip this many results")
		cmd.Flags().IntVar(&maxN, "max", 0, "return at most this many results (server default if unset)")
		cmd.Flags().BoolVar(&all, "all", false, "fetch every page")
	}
	var yes bool
	if v.Confirm != "" {
		app.YesFlag(cmd, &yes)
	}
	cols := columns(v.Columns, g.Columns)

	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		if err := CheckRequires(ctx, a, m); err != nil {
			return err
		}
		c, err := a.Client()
		if err != nil {
			return err
		}
		// raw keeps what the user typed, for messages; vars holds resolved ids.
		raw, vars := map[string]string{}, map[string]string{}
		for i, arg := range v.Args {
			if i >= len(args) {
				break
			}
			raw[arg.Name] = args[i]
			id, err := resolve(ctx, c, arg.Lookup, args[i])
			if err != nil {
				return err
			}
			vars[arg.Name] = id
		}
		q, err := query(ctx, c)
		if err != nil {
			return err
		}
		var reqBody map[string]any
		if v.Body || len(v.Fields) > 0 {
			if reqBody, err = body.Build(a, nil); err != nil {
				return err
			}
			if err := fields(ctx, c, reqBody); err != nil {
				return err
			}
			if err := secrets(reqBody); err != nil {
				return err
			}
			var required []string
			for _, f := range v.Fields {
				if f.Required && f.Kind != "secret" {
					required = append(required, fieldPath(f))
				}
			}
			if err := requireFields(reqBody, required); err != nil {
				return err
			}
		}
		if v.Confirm != "" {
			if err := a.Confirm(yes, fillMessage(v.Confirm, raw, reqBody, nil)); err != nil {
				return err
			}
		}
		p := requestPath(c, path, vars)

		if v.Paged && v.Method == "GET" {
			if all {
				items, err := app.Paginate(ctx, c, p, q, first)
				if err != nil {
					return err
				}
				return a.Printer.Print(items, cols)
			}
			if cmd.Flags().Changed("first") {
				q.Set("first", strconv.Itoa(first))
			}
			if cmd.Flags().Changed("max") {
				q.Set("max", strconv.Itoa(maxN))
			}
		}
		req := kc.Request{Method: v.Method, Path: p, Query: q}
		if reqBody != nil {
			req.Body = reqBody
		} else if v.Method == "POST" || v.Method == "PUT" {
			// Keycloak rejects body-less POST/PUT without a media type.
			req.Body = map[string]any{}
		}
		resp, err := c.Do(ctx, req)
		if err != nil {
			return err
		}
		return report(a, v, g, cols, resp, raw, reqBody)
	}
	return cmd
}

func report(a *app.App, v *extension.Verb, g *extension.Group, cols []output.Column, resp *kc.Response, raw map[string]string, reqBody map[string]any) error {
	data, err := resp.JSON()
	if err != nil {
		return err
	}
	show := v.Method == "GET"
	if v.Print != nil {
		show = *v.Print
	}
	if v.Secret != "" {
		show = true
	}
	if show {
		if data == nil {
			a.Printer.Note("No content.")
			return nil
		}
		if err := a.Printer.Print(data, cols); err != nil {
			return err
		}
		if v.Secret != "" {
			a.Printer.Warn("The %s is shown only this once; store it now.", v.Secret)
		}
		return nil
	}
	id := resp.CreatedID()
	if a.Printer.IsStructured() {
		switch {
		case data != nil:
			return a.Printer.Print(data, nil)
		case id != "":
			return a.Printer.Print(map[string]any{"id": id}, nil)
		}
		return nil
	}
	resMap, _ := data.(map[string]any)
	if resMap == nil && id != "" {
		resMap = map[string]any{"id": id}
	}
	msg := v.Message
	switch {
	case msg != "":
	case id != "":
		msg = "Created " + g.Name + " " + id + "."
	default:
		msg = "Done."
	}
	a.Printer.Success("%s", fillMessage(msg, raw, reqBody, resMap))
	return nil
}

// fillMessage fills {name} from args, then the request body, then the
// response, using top-level scalar fields only.
func fillMessage(s string, raw map[string]string, body, resp map[string]any) string {
	return extension.Fill(s, func(name string) (string, bool) {
		if v, ok := raw[name]; ok {
			return v, true
		}
		for _, m := range []map[string]any{body, resp} {
			if x, ok := m[name]; ok && x != nil {
				switch x.(type) {
				case map[string]any, []any:
					continue
				}
				return fmt.Sprint(x), true
			}
		}
		return "", false
	})
}

// indent matches built-in help, where examples sit two spaces in.
func indent(s string) string {
	if s == "" {
		return s
	}
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		if !strings.HasPrefix(l, "  ") {
			lines[i] = "  " + l
		}
	}
	return strings.Join(lines, "\n")
}

func argsUse(args []extension.Arg) string {
	var b strings.Builder
	for _, a := range args {
		if a.Optional {
			b.WriteString(" [" + a.Name + "]")
		} else {
			b.WriteString(" <" + a.Name + ">")
		}
	}
	return b.String()
}

func argCount(args []extension.Arg) cobra.PositionalArgs {
	required := 0
	var names []string
	for _, a := range args {
		if !a.Optional {
			required++
			names = append(names, "<"+a.Name+">")
		}
	}
	return func(cmd *cobra.Command, got []string) error {
		if len(got) < required {
			return app.ExactArgs(required, strings.Join(names, " "))(cmd, got)
		}
		if len(got) > len(args) {
			return errs.Usage("too many arguments: expected at most %d but got %d", len(args), len(got))
		}
		return nil
	}
}

func columns(lists ...[]extension.Column) []output.Column {
	for _, l := range lists {
		if len(l) == 0 {
			continue
		}
		out := make([]output.Column, 0, len(l))
		for _, c := range l {
			out = append(out, output.Col(c.Header, c.Path))
		}
		return out
	}
	return nil
}

// resolve turns a name into the id the API wants; no lookup passes it through.
func resolve(ctx context.Context, c *kc.Client, lookup, ref string) (string, error) {
	switch lookup {
	case "user":
		return app.Users.Find(ctx, c, ref)
	case "client":
		return app.ClientUUID(ctx, c, ref)
	case "group":
		return app.FindGroup(ctx, c, ref)
	case "client-scope":
		return app.ClientScopes.Find(ctx, c, ref)
	case "organization":
		return app.Organizations.Find(ctx, c, ref)
	}
	return ref, nil
}

func completion(a *app.App, g *extension.Group, base, lookup, complete string, values []string) cobra.CompletionFunc {
	if len(values) > 0 {
		return app.Values(values...)
	}
	kind := complete
	if kind == "" {
		kind = lookup
	}
	switch kind {
	case "user":
		return a.UserCompleter()
	case "client":
		return a.ClientCompleter()
	case "group":
		return a.GroupCompleter()
	case "client-scope":
		return a.ClientScopeCompleter()
	case "organization":
		return a.Fetched("organizations", app.Organizations.Completer())
	case "realm":
		return a.CompleteRealms
	case "realm-role":
		return a.RealmRoleCompleter()
	case "self":
		return selfCompleter(a, g, base)
	}
	return func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
}

// selfCompleter lists the group's own items as its complete section says.
func selfCompleter(a *app.App, g *extension.Group, base string) cobra.CompletionFunc {
	spec := g.Complete
	return a.Fetched(g.Name, func(ctx context.Context, c *kc.Client, _ []string, _ string) ([]app.Item, error) {
		q := url.Values{}
		for k, v := range spec.Query {
			q.Set(k, v)
		}
		v, err := c.Get(ctx, requestPath(c, joinPath(base, spec.Path), nil), q)
		if err != nil {
			return nil, err
		}
		list, _ := v.([]any)
		out := make([]app.Item, 0, len(list))
		for _, x := range list {
			val := field(x, spec.Value)
			if val == "" {
				continue
			}
			out = append(out, app.Item{Value: val, Desc: field(x, spec.Desc)})
		}
		return out, nil
	})
}

// field reads a dotted path (".a.b" or "a.b") from a JSON value as a string.
func field(v any, p string) string {
	if p == "" {
		return ""
	}
	cur := v
	for _, s := range strings.Split(strings.TrimPrefix(p, "."), ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur = m[s]
	}
	if cur == nil {
		return ""
	}
	switch cur.(type) {
	case map[string]any, []any:
		return ""
	}
	return fmt.Sprint(cur)
}

type queryFunc func(ctx context.Context, c *kc.Client) (url.Values, error)

func bindQuery(a *app.App, cmd *cobra.Command, g *extension.Group, base string, params []extension.Param) queryFunc {
	str := map[string]*string{}
	list := map[string]*[]string{}
	b := map[string]*bool{}
	n := map[string]*int{}
	for _, p := range params {
		switch p.Kind {
		case "bool":
			b[p.Flag] = cmd.Flags().Bool(p.Flag, false, p.Usage)
		case "int":
			n[p.Flag] = cmd.Flags().Int(p.Flag, 0, p.Usage)
		case "strings":
			list[p.Flag] = cmd.Flags().StringSlice(p.Flag, nil, p.Usage)
		default:
			str[p.Flag] = cmd.Flags().String(p.Flag, "", p.Usage)
		}
		if p.Lookup != "" || p.Complete != "" || len(p.Values) > 0 {
			_ = cmd.RegisterFlagCompletionFunc(p.Flag, completion(a, g, base, p.Lookup, p.Complete, p.Values))
		}
	}
	return func(ctx context.Context, c *kc.Client) (url.Values, error) {
		q := url.Values{}
		for _, p := range params {
			if !cmd.Flags().Changed(p.Flag) {
				continue
			}
			name := p.Param
			if name == "" {
				name = p.Flag
			}
			switch p.Kind {
			case "bool":
				q.Set(name, strconv.FormatBool(*b[p.Flag]))
			case "int":
				q.Set(name, strconv.Itoa(*n[p.Flag]))
			case "strings":
				for _, s := range *list[p.Flag] {
					id, err := resolve(ctx, c, p.Lookup, s)
					if err != nil {
						return nil, err
					}
					q.Add(name, id)
				}
			default:
				id, err := resolve(ctx, c, p.Lookup, *str[p.Flag])
				if err != nil {
					return nil, err
				}
				q.Set(name, id)
			}
		}
		return q, nil
	}
}

func fieldPath(f extension.Field) string {
	if f.Path != "" {
		return f.Path
	}
	return f.Flag
}

var fieldKinds = map[string]app.FieldKind{
	"": app.String, "string": app.String, "bool": app.Bool, "int": app.Int,
	"strings": app.Strings, "kv": app.KV, "kvstring": app.KVString,
}

// bindFields maps typed flags onto the body. Lookups are resolved after the
// flags apply; secrets come from --<flag>-file, piped stdin or a prompt.
func bindFields(a *app.App, cmd *cobra.Command, g *extension.Group, base string, fs []extension.Field) (
	apply func(ctx context.Context, c *kc.Client, body map[string]any) error,
	secrets func(body map[string]any) error,
) {
	var plain app.Fields
	files := map[string]*string{}
	var secretFields []extension.Field
	for _, f := range fs {
		if f.Kind == "secret" {
			secretFields = append(secretFields, f)
			files[f.Flag] = cmd.Flags().String(f.Flag+"-file", "", f.Usage+" (read from this file, '-' for stdin)")
			cmd.MarkFlagFilename(f.Flag + "-file")
			continue
		}
		plain = append(plain, app.Field{Flag: f.Flag, Path: f.Path, Kind: fieldKinds[f.Kind], Usage: f.Usage})
	}
	bound := plain.Bind(cmd)
	for _, f := range fs {
		if f.Kind != "secret" && (f.Lookup != "" || f.Complete != "" || len(f.Values) > 0) {
			_ = cmd.RegisterFlagCompletionFunc(f.Flag, completion(a, g, base, f.Lookup, f.Complete, f.Values))
		}
	}
	apply = func(ctx context.Context, c *kc.Client, body map[string]any) error {
		if err := bound.Apply(cmd, body); err != nil {
			return err
		}
		for _, f := range fs {
			if f.Lookup == "" || !cmd.Flags().Changed(f.Flag) {
				continue
			}
			segs := strings.Split(fieldPath(f), ".")
			switch cur := getPath(body, segs).(type) {
			case string:
				id, err := resolve(ctx, c, f.Lookup, cur)
				if err != nil {
					return err
				}
				if err := app.SetPath(body, segs, id); err != nil {
					return err
				}
			case []any:
				for i, x := range cur {
					s, _ := x.(string)
					id, err := resolve(ctx, c, f.Lookup, s)
					if err != nil {
						return err
					}
					cur[i] = id
				}
			}
		}
		return nil
	}
	secrets = func(body map[string]any) error {
		for _, f := range secretFields {
			file := *files[f.Flag]
			if file == "" && !f.Required {
				continue
			}
			label := f.Usage
			if label == "" {
				label = f.Flag
			}
			s, err := a.ReadSecret(file, capitalize(label))
			if err != nil {
				return err
			}
			if err := app.SetPath(body, strings.Split(fieldPath(f), "."), s); err != nil {
				return err
			}
		}
		return nil
	}
	return apply, secrets
}

func getPath(m map[string]any, segs []string) any {
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

func requireFields(body map[string]any, paths []string) error {
	var missing []string
	for _, p := range paths {
		if v := getPath(body, strings.Split(p, ".")); v == nil || v == "" {
			missing = append(missing, p)
		}
	}
	if len(missing) > 0 {
		return errs.Usage("missing required field(s): %s", strings.Join(missing, ", ")).
			WithHint("Pass them as flags, with -s field=value, or in the -f file.")
	}
	return nil
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// CheckRequires fails when the server is known to lack what the extension
// needs. Unknown (features hidden from non-master admins) passes.
func CheckRequires(ctx context.Context, a *app.App, m *extension.Manifest) error {
	r := m.Requires
	if len(r.Providers) == 0 && len(r.Features) == 0 && r.MinVersion == "" {
		return nil
	}
	p, err := a.Profile(ctx)
	if err != nil {
		return err
	}
	hint := "Run 'kcctl server info --refresh' if the server changed recently."
	for _, sp := range r.Providers {
		spi, id, _ := strings.Cut(sp, "/")
		if has, known := p.HasProvider(spi, id); known && !has {
			return errs.Usage("the %s extension needs the %s provider, which this server does not have", m.Name, sp).WithHint(hint)
		}
	}
	for _, f := range r.Features {
		if p.Features != nil && !p.Features[f] {
			return errs.Usage("the %s extension needs the %s feature, which is disabled on this server", m.Name, f).WithHint(hint)
		}
	}
	if r.MinVersion != "" {
		if min, err := compat.Parse(r.MinVersion); err == nil && !p.Version.AtLeast(min) {
			return errs.Usage("the %s extension needs Keycloak %s or newer (server is %s)", m.Name, min, p.Version)
		}
	}
	return nil
}
