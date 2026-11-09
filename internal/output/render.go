// Package output renders command results and errors. Results go to stdout;
// notes, success messages and errors go to stderr so pipelines stay clean.
package output

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/mattn/go-runewidth"
	"github.com/ohler55/ojg/jp"
	"sigs.k8s.io/yaml"

	"github.com/emdzej/keycloak-cli/internal/errs"
)

// Column is one table column; Path is a JSONPath relative to the row
// (".username", "attributes.email[0]").
type Column struct {
	Header string
	Path   string
}

func Col(header, path string) Column { return Column{Header: header, Path: path} }

type Printer struct {
	Format   string
	Out      io.Writer
	Err      io.Writer
	OutStyle Style
	ErrStyle Style
	Quiet    bool
	Verbose  int
	// MaxCell truncates table cells when writing to a terminal; 0 disables.
	MaxCell int
}

// Validate rejects an unknown -o before any request is made.
func ValidateFormat(f string) error {
	name, arg := splitFormat(f)
	switch name {
	case "", "table", "vtable", "json", "yaml":
		return nil
	case "jsonpath":
		if arg == "" {
			return errs.Usage("-o jsonpath needs an expression, e.g. -o jsonpath='$[*].id'")
		}
		_, err := parsePath(arg)
		if err != nil {
			return errs.Usage("invalid jsonpath %q: %v", arg, err)
		}
		return nil
	case "custom-columns":
		if _, err := parseColumns(arg); err != nil {
			return err
		}
		return nil
	}
	return errs.Usage("unknown output format %q", f).
		WithHint("Use one of table, vtable, json, yaml, jsonpath=<expr>, custom-columns=<H:path,...>.")
}

// splitFormat accepts both "jsonpath=x" and kubectl's "jsonpath:x".
func splitFormat(f string) (string, string) {
	if i := strings.IndexAny(f, "=:"); i > 0 {
		return f[:i], f[i+1:]
	}
	return f, ""
}

// IsStructured reports whether output is machine-oriented, in which case
// commands must not print decorative notes to stdout.
func (p *Printer) IsStructured() bool {
	name, _ := splitFormat(p.Format)
	return name == "json" || name == "yaml" || name == "jsonpath"
}

type envelope struct {
	Data  any        `json:"data"`
	Error *errorBody `json:"error"`
}

type errorBody struct {
	Code    errs.Code `json:"code"`
	Message string    `json:"message"`
	Hint    string    `json:"hint,omitempty"`
	Details any       `json:"details,omitempty"`
}

// Print renders a result. columns is the command's default table view; nil
// lets the shape of the data decide.
func (p *Printer) Print(data any, columns []Column) error {
	name, arg := splitFormat(p.Format)
	switch name {
	case "json":
		return p.writeJSON(envelope{Data: data})
	case "yaml":
		return p.writeYAML(envelope{Data: data})
	case "jsonpath":
		return p.jsonpath(data, arg)
	case "custom-columns":
		cols, err := parseColumns(arg)
		if err != nil {
			return err
		}
		return p.table(asList(data), cols)
	case "vtable":
		if list, ok := data.([]any); ok {
			for i, item := range list {
				if i > 0 {
					fmt.Fprintln(p.Out)
				}
				p.object(item, columns, 0)
			}
			return nil
		}
		p.object(data, columns, 0)
		return nil
	}
	switch d := data.(type) {
	case nil:
		return nil
	case []any:
		if len(d) == 0 {
			p.Note("No results.")
			return nil
		}
		if allScalars(d) {
			for _, x := range d {
				fmt.Fprintln(p.Out, scalar(x))
			}
			return nil
		}
		if columns == nil {
			columns = inferColumns(d)
		}
		return p.table(d, columns)
	case map[string]any:
		p.object(d, nil, 0)
		return nil
	default:
		fmt.Fprintln(p.Out, scalar(d))
		return nil
	}
}

func (p *Printer) writeJSON(v any) error {
	enc := json.NewEncoder(p.Out)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

func (p *Printer) writeYAML(v any) error {
	data, err := yaml.Marshal(v)
	if err != nil {
		return err
	}
	_, err = p.Out.Write(data)
	return err
}

func (p *Printer) jsonpath(data any, expr string) error {
	x, err := parsePath(expr)
	if err != nil {
		return errs.Usage("invalid jsonpath %q: %v", expr, err)
	}
	for _, r := range x.Get(normalize(data)) {
		switch r.(type) {
		case map[string]any, []any:
			b, _ := json.Marshal(r)
			fmt.Fprintln(p.Out, string(b))
		default:
			fmt.Fprintln(p.Out, scalar(r))
		}
	}
	return nil
}

// parsePath accepts "$.a", ".a", "a" and kubectl's "{.a}".
func parsePath(expr string) (jp.Expr, error) {
	e := strings.TrimSpace(expr)
	e = strings.TrimSuffix(strings.TrimPrefix(e, "{"), "}")
	switch {
	case strings.HasPrefix(e, "$"):
	case strings.HasPrefix(e, "."), strings.HasPrefix(e, "["):
		e = "$" + e
	default:
		e = "$." + e
	}
	return jp.ParseString(e)
}

func parseColumns(spec string) ([]Column, error) {
	if spec == "" {
		return nil, errs.Usage("-o custom-columns needs HEADER:path pairs, e.g. -o custom-columns=ID:.id,NAME:.name")
	}
	var cols []Column
	for _, part := range strings.Split(spec, ",") {
		h, pth, ok := strings.Cut(part, ":")
		if !ok || h == "" || pth == "" {
			return nil, errs.Usage("custom-columns entry %q must be HEADER:path", part)
		}
		if _, err := parsePath(pth); err != nil {
			return nil, errs.Usage("invalid path %q in custom-columns: %v", pth, err)
		}
		cols = append(cols, Column{Header: h, Path: pth})
	}
	return cols, nil
}

// normalize turns json.Number into float/int so jsonpath filters compare.
func normalize(v any) any {
	switch t := v.(type) {
	case json.Number:
		if i, err := t.Int64(); err == nil {
			return i
		}
		f, _ := t.Float64()
		return f
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, x := range t {
			out[k] = normalize(x)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = normalize(x)
		}
		return out
	}
	return v
}

func asList(data any) []any {
	if l, ok := data.([]any); ok {
		return l
	}
	if data == nil {
		return nil
	}
	return []any{data}
}

func cell(row any, path string) string {
	x, err := parsePath(path)
	if err != nil {
		return ""
	}
	res := x.Get(normalize(row))
	if len(res) == 0 {
		return ""
	}
	if len(res) == 1 {
		return scalar(res[0])
	}
	parts := make([]string, len(res))
	for i, r := range res {
		parts[i] = scalar(r)
	}
	return strings.Join(parts, ",")
}

func (p *Printer) table(rows []any, cols []Column) error {
	if len(rows) == 0 {
		p.Note("No results.")
		return nil
	}
	grid := make([][]string, len(rows)+1)
	grid[0] = make([]string, len(cols))
	for i, c := range cols {
		grid[0][i] = strings.ToUpper(c.Header)
	}
	for r, row := range rows {
		grid[r+1] = make([]string, len(cols))
		for i, c := range cols {
			v := cell(row, c.Path)
			if p.MaxCell > 0 && runewidth.StringWidth(v) > p.MaxCell {
				v = runewidth.Truncate(v, p.MaxCell, "…")
			}
			grid[r+1][i] = v
		}
	}
	widths := make([]int, len(cols))
	for _, row := range grid {
		for i, v := range row {
			widths[i] = max(widths[i], runewidth.StringWidth(v))
		}
	}
	for r, row := range grid {
		var b strings.Builder
		for i, v := range row {
			if i == len(row)-1 {
				b.WriteString(v)
				break
			}
			b.WriteString(runewidth.FillRight(v, widths[i]+3))
		}
		line := strings.TrimRight(b.String(), " ")
		if r == 0 {
			line = p.OutStyle.Bold(line)
		}
		fmt.Fprintln(p.Out, line)
	}
	return nil
}

// object prints FIELD/VALUE pairs for scalars, then nested values as indented
// sections, so a single resource reads top-down without a JSON dump.
func (p *Printer) object(v any, columns []Column, depth int) {
	m, ok := v.(map[string]any)
	if !ok {
		fmt.Fprintln(p.Out, strings.Repeat("  ", depth)+scalar(v))
		return
	}
	indent := strings.Repeat("  ", depth)
	var scalars, nested []string
	for _, k := range sortedKeys(m) {
		if isScalarish(m[k]) {
			scalars = append(scalars, k)
		} else {
			nested = append(nested, k)
		}
	}
	if depth == 0 && len(columns) > 0 {
		// Show the view's fields first; the rest of the record still follows.
		seen := map[string]bool{}
		for _, c := range columns {
			key := strings.TrimPrefix(c.Path, ".")
			if _, exists := m[key]; exists {
				seen[key] = true
			}
		}
		var head, tail []string
		for _, k := range scalars {
			if seen[k] {
				head = append(head, k)
			} else {
				tail = append(tail, k)
			}
		}
		scalars = append(head, tail...)
	}
	w := 0
	for _, k := range scalars {
		w = max(w, runewidth.StringWidth(k))
	}
	for _, k := range scalars {
		fmt.Fprintf(p.Out, "%s%s  %s\n", indent, p.OutStyle.Bold(runewidth.FillRight(k, w)), scalarCell(m[k]))
	}
	for _, k := range nested {
		val := m[k]
		if isEmpty(val) {
			continue
		}
		fmt.Fprintf(p.Out, "%s%s\n", indent, p.OutStyle.Bold(k+":"))
		switch t := val.(type) {
		case map[string]any:
			p.object(t, nil, depth+1)
		case []any:
			for i, item := range t {
				if i > 0 {
					fmt.Fprintln(p.Out, indent+"  "+p.OutStyle.Dim("-"))
				}
				p.object(item, nil, depth+1)
			}
		}
	}
}

func isEmpty(v any) bool {
	switch t := v.(type) {
	case map[string]any:
		return len(t) == 0
	case []any:
		return len(t) == 0
	}
	return v == nil
}

// isScalarish treats lists of scalars as one comma-joined cell.
func isScalarish(v any) bool {
	switch t := v.(type) {
	case map[string]any:
		return len(t) == 0
	case []any:
		return allScalars(t)
	}
	return true
}

func allScalars(l []any) bool {
	for _, x := range l {
		switch x.(type) {
		case map[string]any, []any:
			return false
		}
	}
	return true
}

func scalarCell(v any) string {
	if l, ok := v.([]any); ok {
		parts := make([]string, len(l))
		for i, x := range l {
			parts[i] = scalar(x)
		}
		return strings.Join(parts, ", ")
	}
	if m, ok := v.(map[string]any); ok && len(m) == 0 {
		return ""
	}
	return scalar(v)
}

func scalar(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case json.Number:
		return t.String()
	case float64:
		if t == float64(int64(t)) {
			return fmt.Sprintf("%d", int64(t))
		}
		return fmt.Sprintf("%g", t)
	case map[string]any, []any:
		b, _ := json.Marshal(t)
		return string(b)
	}
	return fmt.Sprint(v)
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		ri, rj := keyRank(keys[i]), keyRank(keys[j])
		if ri != rj {
			return ri < rj
		}
		return keys[i] < keys[j]
	})
	return keys
}

// keyRank puts identity fields first since Go maps have no order.
func keyRank(k string) int {
	switch k {
	case "id":
		return 0
	case "name", "alias", "clientId", "username", "realm":
		return 1
	}
	return 2
}

// inferColumns picks scalar fields shared by the rows, identity first, and
// caps the count so an unknown shape still fits a terminal.
func inferColumns(rows []any) []Column {
	count := map[string]int{}
	for _, r := range rows {
		if m, ok := r.(map[string]any); ok {
			for k, v := range m {
				if isScalarish(v) {
					count[k]++
				}
			}
		}
	}
	keys := make(map[string]any, len(count))
	for k := range count {
		keys[k] = nil
	}
	var cols []Column
	for _, k := range sortedKeys(keys) {
		if len(cols) == 6 {
			break
		}
		cols = append(cols, Column{Header: k, Path: "." + k})
	}
	return cols
}

func (p *Printer) Note(format string, args ...any) {
	if p.Quiet {
		return
	}
	fmt.Fprintln(p.Err, p.ErrStyle.Dim(fmt.Sprintf(format, args...)))
}

func (p *Printer) Success(format string, args ...any) {
	if p.Quiet {
		return
	}
	fmt.Fprintln(p.Err, p.ErrStyle.Green(fmt.Sprintf(format, args...)))
}

func (p *Printer) Warn(format string, args ...any) {
	fmt.Fprintln(p.Err, p.ErrStyle.Yellow("warning: "+fmt.Sprintf(format, args...)))
}

// Error reports a failure. Structured formats also get the envelope on stdout
// so scripts parsing JSON see failures in the same shape as results.
func (p *Printer) Error(e *errs.Error) {
	name, _ := splitFormat(p.Format)
	if name == "json" || name == "yaml" {
		env := envelope{Error: &errorBody{Code: e.Code, Message: e.Message, Hint: e.Hint, Details: e.Details}}
		if name == "json" {
			_ = p.writeJSON(env)
		} else {
			_ = p.writeYAML(env)
		}
	}
	s := p.ErrStyle
	lines := strings.Split(e.Message, "\n")
	fmt.Fprintln(p.Err, s.Red("error: ")+lines[0])
	for _, l := range lines[1:] {
		fmt.Fprintln(p.Err, l)
	}
	if e.Hint != "" {
		fmt.Fprintln(p.Err, "  "+s.Yellow("Hint: ")+e.Hint)
	}
	if p.Verbose > 0 && e.Request != nil {
		r := e.Request
		fmt.Fprintln(p.Err, s.Dim(fmt.Sprintf("  %s %s -> %d", r.Method, r.URL, r.Status)))
		if r.RequestID != "" {
			fmt.Fprintln(p.Err, s.Dim("  request-id: "+r.RequestID))
		}
		if r.Body != "" {
			fmt.Fprintln(p.Err, s.Dim("  body: "+r.Body))
		}
	}
}

// Raw writes v as bare JSON or YAML (no envelope), for files meant to be
// re-imported, such as realm exports.
func (p *Printer) Raw(v any, yamlOut bool) error {
	if yamlOut {
		return p.writeYAML(v)
	}
	return p.writeJSON(v)
}
