package app

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"
	"sigs.k8s.io/yaml"

	"github.com/emdzej/kcctl/internal/errs"
)

// BodyFlags are the -f/--set pair every create/update command takes.
type BodyFlags struct {
	File string
	Set  []string
}

func (b *BodyFlags) Bind(cmd *cobra.Command) {
	cmd.Flags().StringVarP(&b.File, "file", "f", "", "JSON or YAML representation to send ('-' for stdin)")
	cmd.Flags().StringArrayVarP(&b.Set, "set", "s", nil, "set a field: path=value (dotted path, JSON values allowed, e.g. -s enabled=true -s attributes.dept='[\"x\"]')")
	cmd.MarkFlagFilename("file", "json", "yaml", "yml")
}

func (b *BodyFlags) Given() bool { return b.File != "" || len(b.Set) > 0 }

// secretKeys may not be set on the command line, where they'd end up in shell
// history and process listings.
var secretKeys = map[string]bool{"password": true, "secret": true, "clientsecret": true}

// Build reads --file (if any) into base, then applies --set on top.
func (b *BodyFlags) Build(a *App, base map[string]any) (map[string]any, error) {
	if base == nil {
		base = map[string]any{}
	}
	if b.File != "" {
		v, err := ReadDocument(a, b.File)
		if err != nil {
			return nil, err
		}
		m, ok := v.(map[string]any)
		if !ok {
			return nil, errs.Usage("%s must contain a JSON/YAML object", b.File)
		}
		for k, x := range m {
			base[k] = x
		}
	}
	for _, s := range b.Set {
		k, v, ok := strings.Cut(s, "=")
		if !ok || k == "" {
			return nil, errs.Usage("--set %q must be path=value", s)
		}
		segs := strings.Split(k, ".")
		if secretKeys[strings.ToLower(segs[len(segs)-1])] {
			return nil, errs.Usage("--set cannot carry a secret (%s); use the command's --*-file option or a file passed with -f", k)
		}
		if err := SetPath(base, segs, ParseValue(v)); err != nil {
			return nil, err
		}
	}
	return base, nil
}

// ParseValue reads JSON literals (true, 3, ["a"], {"x":1}, null) and treats
// anything else as a string, so -s name=foo doesn't need quoting.
func ParseValue(s string) any {
	var v any
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	if err := dec.Decode(&v); err == nil && !dec.More() {
		return v
	}
	return s
}

func SetPath(m map[string]any, segs []string, v any) error {
	for i, s := range segs {
		if s == "" {
			return errs.Usage("empty segment in path %q", strings.Join(segs, "."))
		}
		if i == len(segs)-1 {
			m[s] = v
			return nil
		}
		next, ok := m[s].(map[string]any)
		if !ok {
			next = map[string]any{}
			m[s] = next
		}
		m = next
	}
	return nil
}

// ReadDocument loads JSON or YAML from a path or '-' (stdin).
func ReadDocument(a *App, path string) (any, error) {
	var data []byte
	var err error
	if path == "-" {
		data, err = io.ReadAll(a.In)
	} else {
		data, err = os.ReadFile(path)
	}
	if err != nil {
		return nil, errs.Usage("cannot read %s: %v", path, err)
	}
	return ParseDocument(data, path)
}

func ParseDocument(data []byte, name string) (any, error) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return nil, errs.Usage("%s is empty", name)
	}
	if data[0] != '{' && data[0] != '[' {
		j, err := yaml.YAMLToJSON(data)
		if err != nil {
			return nil, errs.Usage("%s is neither JSON nor YAML: %v", name, err)
		}
		data = j
	}
	var v any
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return nil, errs.Usage("%s is not valid JSON: %v", name, err)
	}
	return v, nil
}

// RequireFields turns a missing field into a usage error before the request,
// instead of an opaque 400 from the server.
func RequireFields(body map[string]any, fields ...string) error {
	var missing []string
	for _, f := range fields {
		if v, ok := body[f]; !ok || v == nil || v == "" {
			missing = append(missing, f)
		}
	}
	if len(missing) > 0 {
		return errs.Usage("missing required field(s): %s", strings.Join(missing, ", ")).
			WithHint("Pass them as flags, with -s field=value, or in the -f file.")
	}
	return nil
}

// YesFlag registers --yes for operations that affect more than the one thing
// named on the line.
func YesFlag(cmd *cobra.Command, yes *bool) {
	cmd.Flags().BoolVarP(yes, "yes", "y", false, "do not ask for confirmation")
}

// Confirm asks before a broad destructive change. Without a terminal it
// refuses unless --yes was given, so a script never hangs or acts silently.
func (a *App) Confirm(yes bool, what string) error {
	if yes {
		return nil
	}
	if !a.CanPrompt() {
		return errs.Usage("%s Pass --yes to go ahead.", what)
	}
	p := a.EnsurePrinter()
	fmt.Fprint(a.Err, p.ErrStyle.Bold(what)+" Continue? [y/N] ")
	line, _ := bufio.NewReader(a.In).ReadString('\n')
	ans := strings.ToLower(strings.TrimSpace(line))
	if ans != "y" && ans != "yes" {
		return errs.Usage("Not confirmed; nothing was changed.")
	}
	return nil
}

// ReadSecret takes a secret from --x-file, else piped stdin, else a hidden
// prompt. It never comes from a plain flag value.
func (a *App) ReadSecret(file, label string) (string, error) {
	if file != "" {
		var data []byte
		var err error
		if file == "-" {
			data, err = io.ReadAll(a.In)
		} else {
			data, err = os.ReadFile(file)
		}
		if err != nil {
			return "", errs.Usage("cannot read %s: %v", file, err)
		}
		return strings.TrimRight(string(data), "\r\n"), nil
	}
	if !a.StdinTTY {
		if a.Flags.NoInput {
			return "", errs.Usage("%s is required; pass it with a --*-file option.", label)
		}
		data, err := io.ReadAll(a.In)
		if err != nil {
			return "", errs.Usage("cannot read %s from stdin: %v", label, err)
		}
		s := strings.TrimRight(string(data), "\r\n")
		if s == "" {
			return "", errs.Usage("%s is required; pipe it on stdin or use a --*-file option.", label)
		}
		return s, nil
	}
	if !a.CanPrompt() {
		return "", errs.Usage("%s is required; pass it with a --*-file option.", label)
	}
	fmt.Fprintf(a.Err, "%s: ", label)
	f, _ := a.In.(*os.File)
	if f == nil {
		return "", errs.Usage("%s is required; pass it with a --*-file option.", label)
	}
	b, err := term.ReadPassword(int(f.Fd()))
	fmt.Fprintln(a.Err)
	if err != nil {
		return "", errs.Usage("Cancelled; nothing was changed.")
	}
	return string(b), nil
}

// Prompt reads a visible line (usernames); empty input returns def.
func (a *App) Prompt(label, def string) (string, error) {
	if !a.CanPrompt() {
		if def != "" {
			return def, nil
		}
		return "", errs.Usage("%s is required.", label)
	}
	q := label
	if def != "" {
		q += " [" + def + "]"
	}
	fmt.Fprint(a.Err, q+": ")
	line, err := bufio.NewReader(a.In).ReadString('\n')
	if err != nil && line == "" {
		return "", errs.Usage("Cancelled.")
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return def, nil
	}
	return line, nil
}

// IntString formats an optional int flag for a query string.
func IntString(i int) string { return strconv.Itoa(i) }
