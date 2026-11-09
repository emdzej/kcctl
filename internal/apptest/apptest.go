// Package apptest runs commands against a stub Keycloak. Every request must
// have a reply in the table; an unexpected request fails the test, so tests
// also pin down which endpoints a command calls.
package apptest

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/cobra"

	"github.com/emdzej/keycloak-cli/internal/app"
	"github.com/emdzej/keycloak-cli/internal/auth"
	"github.com/emdzej/keycloak-cli/internal/errs"
)

// Reply is a canned response. Body is JSON-encoded unless it is a string.
type Reply struct {
	Status int
	Body   any
	Header map[string]string
}

// Sent is a request the command made.
type Sent struct {
	Method string
	Path   string
	Query  string
	Body   any
}

type Result struct {
	Stdout, Stderr string
	Code           int
	Sent           []Sent
}

// Replies maps "METHOD /path" (path without query) to a reply. Keycloak's
// serverinfo is answered automatically with Version unless overridden.
type Replies map[string]Reply

type Options struct {
	Version  string          // server version for /admin/serverinfo; default 26.6.3
	Features map[string]bool // server feature flags
	Stdin    string
	TTY      bool // pretend stdin/stderr are terminals
}

// Run builds a root holding only the given commands, so a package's tests
// don't depend on the rest of the tree.
func Run(t *testing.T, builders []func(*app.App) *cobra.Command, replies Replies, opts Options, args ...string) Result {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	t.Setenv("KCCTL_NO_KEYRING", "1")
	for _, k := range []string{"KCCTL_CONTEXT", "KCCTL_SERVER", "KCCTL_REALM", "KCCTL_TOKEN", "KCCTL_AUTH_REALM", "NO_COLOR"} {
		t.Setenv(k, "")
	}
	t.Setenv("NO_COLOR", "1")

	var mu sync.Mutex
	var sent []Sent
	ver := opts.Version
	if ver == "" {
		ver = "26.6.3"
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		var body any
		if len(data) > 0 {
			if json.Unmarshal(data, &body) != nil {
				body = string(data)
			}
		}
		key := r.Method + " " + r.URL.EscapedPath()
		mu.Lock()
		if key != "GET /admin/serverinfo" {
			sent = append(sent, Sent{Method: r.Method, Path: r.URL.EscapedPath(), Query: r.URL.RawQuery, Body: body})
		}
		mu.Unlock()
		rep, ok := replies[key]
		if !ok && key == "GET /admin/serverinfo" {
			feats := []any{}
			for n, on := range opts.Features {
				feats = append(feats, map[string]any{"name": n, "enabled": on})
			}
			rep = Reply{Body: map[string]any{"systemInfo": map[string]any{"version": ver}, "features": feats}}
			ok = true
		}
		if !ok {
			t.Errorf("unexpected request: %s ?%s", key, r.URL.RawQuery)
			w.WriteHeader(599)
			return
		}
		for k, v := range rep.Header {
			w.Header().Set(k, strings.ReplaceAll(v, "{server}", "http://"+r.Host))
		}
		status := rep.Status
		if status == 0 {
			status = 200
			if rep.Body == nil {
				status = 204
			}
		}
		if rep.Body != nil {
			w.Header().Set("Content-Type", "application/json")
		}
		w.WriteHeader(status)
		switch b := rep.Body.(type) {
		case nil:
		case string:
			io.WriteString(w, b)
		default:
			json.NewEncoder(w).Encode(b)
		}
	}))
	defer srv.Close()

	a := app.New()
	var out, errb bytes.Buffer
	a.Out, a.Err = &out, &errb
	a.In = strings.NewReader(opts.Stdin)
	a.StdinTTY, a.StderrTTY, a.StdoutTTY = opts.TTY, opts.TTY, false
	a.Store = memStore{}

	root := &cobra.Command{Use: "kcctl", SilenceErrors: true, SilenceUsage: true,
		PersistentPreRunE: func(*cobra.Command, []string) error { return a.Init() }}
	a.BindGlobals(root)
	for _, b := range builders {
		root.AddCommand(b(a))
	}
	full := append([]string{"--server", srv.URL, "--token", "test-token", "-r", "test"}, args...)
	root.SetArgs(full)
	root.SetOut(&out)
	root.SetErr(&errb)
	err := root.ExecuteContext(context.Background())
	code := 0
	if err != nil {
		e := errs.As(err)
		a.EnsurePrinter().Error(e)
		code = e.ExitCode()
	}
	mu.Lock()
	defer mu.Unlock()
	return Result{Stdout: out.String(), Stderr: errb.String(), Code: code, Sent: sent}
}

type memStore map[string]*auth.Session

func (m memStore) Load(k string) (*auth.Session, error) { return m[k], nil }
func (m memStore) Save(k string, s *auth.Session) error { m[k] = s; return nil }
func (m memStore) Delete(k string) error                { delete(m, k); return nil }

// JSON decodes stdout of a -o json run into the envelope's data.
func (r Result) JSON(t *testing.T) any {
	t.Helper()
	var env struct {
		Data  any `json:"data"`
		Error any `json:"error"`
	}
	if err := json.Unmarshal([]byte(r.Stdout), &env); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, r.Stdout)
	}
	return env.Data
}

// Last returns the last non-serverinfo request, failing if there was none.
func (r Result) Last(t *testing.T) Sent {
	t.Helper()
	if len(r.Sent) == 0 {
		t.Fatal("no requests were sent")
	}
	return r.Sent[len(r.Sent)-1]
}
