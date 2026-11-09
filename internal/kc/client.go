// Package kc is a thin, untyped client for the Keycloak admin REST API. Bodies
// stay as JSON maps on purpose: representations gain fields every release, and
// round-tripping through Go structs would silently drop the ones we don't know,
// corrupting updates on newer servers.
package kc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/emdzej/keycloak-cli/internal/errs"
)

type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

type Client struct {
	HTTP   *http.Client
	Server string
	Realm  string
	Tokens TokenSource
	// UserAgent identifies the CLI in server access logs.
	UserAgent string
}

type Request struct {
	Method string
	// Path is relative to the server root, e.g. /admin/realms/x/users.
	Path        string
	Query       url.Values
	Body        any
	RawBody     []byte
	ContentType string
	Accept      string
}

type Response struct {
	Status int
	Header http.Header
	Body   []byte
}

// JSON decodes the body; an empty body decodes to nil.
func (r *Response) JSON() (any, error) {
	if len(bytes.TrimSpace(r.Body)) == 0 {
		return nil, nil
	}
	var v any
	dec := json.NewDecoder(bytes.NewReader(r.Body))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return nil, errs.Wrap(errs.CodeServer, err, "server returned invalid JSON: %v", err)
	}
	return v, nil
}

// CreatedID is the last segment of the Location header that Keycloak returns
// for 201 Created, which is the only way it reports a new resource's id.
func (r *Response) CreatedID() string {
	loc := r.Header.Get("Location")
	if loc == "" {
		return ""
	}
	u, err := url.Parse(loc)
	if err != nil {
		return ""
	}
	id, _ := url.PathUnescape(path.Base(u.Path))
	return id
}

func (c *Client) Do(ctx context.Context, req Request) (*Response, error) {
	u := c.Server + req.Path
	if len(req.Query) > 0 {
		u += "?" + req.Query.Encode()
	}
	var body io.Reader
	ct := req.ContentType
	switch {
	case req.RawBody != nil:
		body = bytes.NewReader(req.RawBody)
	case req.Body != nil:
		data, err := json.Marshal(req.Body)
		if err != nil {
			return nil, errs.Wrap(errs.CodeInternal, err, "cannot encode request body: %v", err)
		}
		body = bytes.NewReader(data)
		if ct == "" {
			ct = "application/json"
		}
	}
	hr, err := http.NewRequestWithContext(ctx, req.Method, u, body)
	if err != nil {
		return nil, errs.Usage("invalid request: %v", err)
	}
	if ct != "" {
		hr.Header.Set("Content-Type", ct)
	}
	accept := req.Accept
	if accept == "" {
		accept = "application/json"
	}
	hr.Header.Set("Accept", accept)
	if c.UserAgent != "" {
		hr.Header.Set("User-Agent", c.UserAgent)
	}
	if c.Tokens != nil {
		tok, err := c.Tokens.Token(ctx)
		if err != nil {
			return nil, err
		}
		hr.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := c.HTTP.Do(hr)
	if err != nil {
		if ctx.Err() != nil {
			return nil, errs.Usage("Cancelled.")
		}
		return nil, errs.Wrap(errs.CodeNetwork, err, "cannot reach %s: %v", c.Server, unwrapURLErr(err)).
			WithHint("Check the server URL and your network; use -v to see the request.")
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, errs.Wrap(errs.CodeNetwork, err, "reading response: %v", err)
	}
	out := &Response{Status: resp.StatusCode, Header: resp.Header, Body: data}
	if resp.StatusCode >= 400 {
		return out, httpError(req.Method, u, out)
	}
	return out, nil
}

func unwrapURLErr(err error) error {
	if ue, ok := err.(*url.Error); ok {
		return ue.Err
	}
	return err
}

// Get/Post/Put/Delete are conveniences returning decoded JSON.
func (c *Client) Get(ctx context.Context, p string, q url.Values) (any, error) {
	return c.json(ctx, Request{Method: http.MethodGet, Path: p, Query: q})
}

func (c *Client) Post(ctx context.Context, p string, body any) (*Response, error) {
	return c.Do(ctx, Request{Method: http.MethodPost, Path: p, Body: orEmpty(body)})
}

func (c *Client) Put(ctx context.Context, p string, body any) (*Response, error) {
	return c.Do(ctx, Request{Method: http.MethodPut, Path: p, Body: orEmpty(body)})
}

func (c *Client) Delete(ctx context.Context, p string, body any) (*Response, error) {
	return c.Do(ctx, Request{Method: http.MethodDelete, Path: p, Body: body})
}

// orEmpty keeps Content-Type set on body-less POST/PUT, which several
// Keycloak endpoints require (they reject a missing media type with 415).
func orEmpty(body any) any {
	if body == nil {
		return map[string]any{}
	}
	return body
}

func (c *Client) json(ctx context.Context, req Request) (any, error) {
	resp, err := c.Do(ctx, req)
	if err != nil {
		return nil, err
	}
	return resp.JSON()
}

// RealmPath builds /admin/realms/{realm}/seg/seg..., escaping every segment so
// names with spaces or slashes (role names, group paths) are safe.
func (c *Client) RealmPath(segs ...string) string {
	return RealmPathFor(c.Realm, segs...)
}

func RealmPathFor(realm string, segs ...string) string {
	var b strings.Builder
	b.WriteString("/admin/realms/")
	b.WriteString(url.PathEscape(realm))
	for _, s := range segs {
		b.WriteByte('/')
		b.WriteString(url.PathEscape(s))
	}
	return b.String()
}

func httpError(method, u string, r *Response) error {
	code := errs.CodeServer
	switch {
	case r.Status == http.StatusUnauthorized:
		code = errs.CodeAuth
	case r.Status == http.StatusForbidden:
		code = errs.CodePermission
	case r.Status == http.StatusNotFound:
		code = errs.CodeNotFound
	case r.Status == http.StatusBadRequest, r.Status == http.StatusConflict, r.Status == http.StatusUnprocessableEntity:
		code = errs.CodeValidation
	case r.Status < 500:
		code = errs.CodeUsage
	}
	detail := serverMessage(r.Body)
	msg := fmt.Sprintf("the server rejected this request (%d %s)", r.Status, http.StatusText(r.Status))
	if r.Status >= 500 {
		msg = fmt.Sprintf("the server failed (%d %s)", r.Status, http.StatusText(r.Status))
	}
	if detail != "" {
		msg += "\n  " + detail
	}
	e := errs.New(code, "%s", msg)
	var details any
	if err := json.Unmarshal(r.Body, &details); err == nil {
		e.Details = details
	} else if len(r.Body) > 0 {
		e.Details = string(r.Body)
	}
	e.Request = &errs.RequestInfo{
		Method: method, URL: u, Status: r.Status,
		RequestID: r.Header.Get("X-Request-Id"), Body: string(r.Body),
	}
	switch r.Status {
	case http.StatusUnauthorized:
		e.Hint = "Your session is invalid or expired. Run 'kcctl auth login'."
	case http.StatusForbidden:
		e.Hint = "Your account lacks the realm-management role this needs. Check with 'kcctl auth whoami'."
	case http.StatusNotImplemented:
		e.Hint = "This server does not implement the endpoint; check 'kcctl server info' for its version."
	default:
		if r.Status >= 500 {
			e.Hint = "Retry later; if it persists, check the server logs (use -v for the request id)."
		}
	}
	return e
}

// serverMessage extracts Keycloak's error text from its several error shapes:
// {errorMessage}, {error, error_description}, and {field, errorMessage, params}.
func serverMessage(body []byte) string {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		s := strings.TrimSpace(string(body))
		if strings.HasPrefix(s, "<") || len(s) > 300 {
			return ""
		}
		return s
	}
	var parts []string
	if f, ok := m["field"].(string); ok && f != "" {
		parts = append(parts, "field "+f+":")
	}
	for _, k := range []string{"errorMessage", "error_description", "error"} {
		if s, ok := m[k].(string); ok && s != "" {
			parts = append(parts, s)
			break
		}
	}
	if errsList, ok := m["errors"].([]any); ok {
		for _, x := range errsList {
			if em, ok := x.(map[string]any); ok {
				if s := serverMessage(mustJSON(em)); s != "" {
					parts = append(parts, s)
				}
			}
		}
	}
	return strings.Join(parts, " ")
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
