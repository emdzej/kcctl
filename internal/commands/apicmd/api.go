// Package apicmd holds the escape hatches: raw API calls and the bare token,
// for anything the curated commands don't cover.
package apicmd

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/emdzej/keycloak-cli/internal/app"
	"github.com/emdzej/keycloak-cli/internal/errs"
	"github.com/emdzej/keycloak-cli/internal/kc"
)

func Token(a *app.App) *cobra.Command {
	var header, export bool
	cmd := &cobra.Command{
		Use:   "token",
		Short: "Print a valid access token (refreshing it if needed)",
		Example: `  curl -H "$(kcctl token --header)" https://sso.example.com/admin/realms
  eval "$(kcctl token --export)"`,
		GroupID: "session",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if header && export {
				return errs.Usage("--header and --export cannot be combined")
			}
			tm, err := a.Tokens()
			if err != nil {
				return err
			}
			t, err := tm.Token(cmd.Context())
			if err != nil {
				return err
			}
			switch {
			case header:
				fmt.Fprintln(a.Out, "Authorization: Bearer "+t)
			case export:
				fmt.Fprintln(a.Out, "export KCCTL_TOKEN="+t)
			default:
				fmt.Fprintln(a.Out, t)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&header, "header", false, "print as an Authorization header")
	cmd.Flags().BoolVar(&export, "export", false, "print as a shell export of KCCTL_TOKEN")
	return cmd
}

var methods = []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"}

func API(a *app.App) *cobra.Command {
	var query, headers []string
	var data, out, contentType string
	var raw bool
	cmd := &cobra.Command{
		Use:   "api <METHOD> <path>",
		Short: "Call any admin API endpoint",
		Long: `Call any admin API endpoint with the current session.

A path without a leading '/' is relative to /admin/realms/{realm}/; a path
starting with '/' is relative to the server root (e.g. /admin/serverinfo).

--data takes inline JSON, @file (JSON or YAML) or @- for stdin.`,
		Example: `  kcctl api GET users -Q username=alice
  kcctl api GET /admin/serverinfo -o jsonpath='$.systemInfo.version'
  kcctl api POST groups --data '{"name":"ops"}'
  kcctl api PUT users/<id> --data @user.yaml
  kcctl api GET clients/<id>/installation/providers/keycloak-oidc-keycloak-json --raw`,
		Args: app.ExactArgs(2, "<METHOD> <path>"),
		ValidArgsFunction: func(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
			if len(args) == 0 {
				return methods, cobra.ShellCompDirectiveNoFileComp
			}
			return nil, cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			method := strings.ToUpper(args[0])
			if !contains(methods, method) {
				return errs.Usage("unknown method %q; use one of %s", args[0], strings.Join(methods, ", "))
			}
			c, err := a.Client()
			if err != nil {
				return err
			}
			p := args[1]
			if !strings.HasPrefix(p, "/") {
				p = c.RealmPath() + "/" + p
			}
			// Allow a query string in the path as well as -q.
			q := url.Values{}
			if before, after, ok := strings.Cut(p, "?"); ok {
				p = before
				if pq, err := url.ParseQuery(after); err == nil {
					q = pq
				}
			}
			for _, kv := range query {
				k, v, ok := strings.Cut(kv, "=")
				if !ok {
					return errs.Usage("--query %q must be key=value", kv)
				}
				q.Add(k, v)
			}
			req := kc.Request{Method: method, Path: p, Query: q, ContentType: contentType}
			if data != "" {
				body, err := readData(a, data)
				if err != nil {
					return err
				}
				req.Body = body
			}
			if raw {
				req.Accept = "*/*"
			}
			resp, err := doWithHeaders(cmd, c, req, headers)
			if err != nil {
				return err
			}
			if out != "" {
				if err := os.WriteFile(out, resp.Body, 0o600); err != nil {
					return errs.Usage("cannot write %s: %v", out, err)
				}
				a.Printer.Success("Wrote %d bytes to %s.", len(resp.Body), out)
				return nil
			}
			if id := resp.CreatedID(); id != "" && len(resp.Body) == 0 {
				return a.Printer.Print(map[string]any{"id": id, "location": resp.Header.Get("Location")}, nil)
			}
			ct := resp.Header.Get("Content-Type")
			if raw || (len(resp.Body) > 0 && !strings.Contains(ct, "json")) {
				_, err := a.Out.Write(resp.Body)
				return err
			}
			v, err := resp.JSON()
			if err != nil {
				return err
			}
			if v == nil {
				a.Printer.Success("%s %s: %d %s", method, p, resp.Status, http.StatusText(resp.Status))
				return nil
			}
			return a.Printer.Print(v, nil)
		},
	}
	f := cmd.Flags()
	f.StringArrayVarP(&query, "query", "Q", nil, "query parameter key=value (repeatable)")
	f.StringArrayVarP(&headers, "header", "H", nil, "extra request header 'Name: value' (repeatable)")
	f.StringVarP(&data, "data", "d", "", "request body: inline JSON, @file or @-")
	f.StringVar(&contentType, "content-type", "", "request Content-Type (default application/json with --data)")
	f.StringVar(&out, "out", "", "write the raw response body to this file")
	f.BoolVar(&raw, "raw", false, "print the response body as-is")
	return cmd
}

func readData(a *app.App, data string) (any, error) {
	if strings.HasPrefix(data, "@") {
		return app.ReadDocument(a, strings.TrimPrefix(data, "@"))
	}
	return app.ParseDocument([]byte(data), "--data")
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// doWithHeaders sends req with extra headers by wrapping the transport for
// this one call; kc.Request deliberately has no header map.
func doWithHeaders(cmd *cobra.Command, c *kc.Client, req kc.Request, headers []string) (*kc.Response, error) {
	if len(headers) == 0 {
		return c.Do(cmd.Context(), req)
	}
	h := http.Header{}
	for _, hv := range headers {
		k, v, ok := strings.Cut(hv, ":")
		if !ok {
			return nil, errs.Usage("--header %q must be 'Name: value'", hv)
		}
		h.Add(strings.TrimSpace(k), strings.TrimSpace(v))
	}
	cc := *c
	hc := *c.HTTP
	hc.Transport = headerTransport{next: c.HTTP.Transport, h: h}
	cc.HTTP = &hc
	return cc.Do(cmd.Context(), req)
}

type headerTransport struct {
	next http.RoundTripper
	h    http.Header
}

func (t headerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	for k, v := range t.h {
		r.Header[k] = v
	}
	return t.next.RoundTrip(r)
}
