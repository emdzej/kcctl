// Package httpx builds the HTTP client shared by token and admin calls and
// owns request tracing, so -v shows token exchanges as well as API calls.
package httpx

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/emdzej/kcctl/internal/errs"
)

// Verbosity is process-wide: 1 = one line per request, 2 = headers + bodies.
var Verbosity int

// TraceOut is where traces go; tests swap it.
var TraceOut io.Writer = os.Stderr

const maxTraceBody = 16000

type Options struct {
	CAFile   string
	Insecure bool
	Timeout  time.Duration
}

func NewClient(o Options) (*http.Client, error) {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if o.Insecure {
		tlsCfg.InsecureSkipVerify = true //nolint:gosec // explicit user opt-in
	}
	if o.CAFile != "" {
		pem, err := os.ReadFile(o.CAFile)
		if err != nil {
			return nil, errs.Usage("cannot read CA file %s: %v", o.CAFile, err)
		}
		pool, _ := x509.SystemCertPool()
		if pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errs.Usage("no certificates found in %s", o.CAFile)
		}
		tlsCfg.RootCAs = pool
	}
	tr.TLSClientConfig = tlsCfg
	timeout := o.Timeout
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	return &http.Client{Transport: &tracer{next: tr}, Timeout: timeout}, nil
}

type tracer struct{ next http.RoundTripper }

func (t *tracer) RoundTrip(req *http.Request) (*http.Response, error) {
	if Verbosity == 0 {
		return t.next.RoundTrip(req)
	}
	start := time.Now()
	var reqBody []byte
	if Verbosity >= 2 && req.Body != nil {
		reqBody, _ = io.ReadAll(req.Body)
		req.Body = io.NopCloser(bytes.NewReader(reqBody))
	}
	resp, err := t.next.RoundTrip(req)
	dur := time.Since(start).Round(time.Millisecond)
	if err != nil {
		fmt.Fprintf(TraceOut, "> %s %s  error after %s: %v\n", req.Method, req.URL, dur, err)
		return resp, err
	}
	rid := resp.Header.Get("X-Request-Id")
	line := fmt.Sprintf("> %s %s  %d  %s", req.Method, req.URL, resp.StatusCode, dur)
	if rid != "" {
		line += "  request-id=" + rid
	}
	fmt.Fprintln(TraceOut, line)
	if Verbosity >= 2 {
		writeHeaders("  > ", req.Header)
		if len(reqBody) > 0 {
			fmt.Fprintln(TraceOut, "  > "+truncate(MaskSecrets(string(reqBody))))
		}
		writeHeaders("  < ", resp.Header)
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		resp.Body = io.NopCloser(bytes.NewReader(body))
		if len(body) > 0 {
			fmt.Fprintln(TraceOut, "  < "+truncate(MaskSecrets(string(body))))
		}
	}
	return resp, nil
}

func writeHeaders(prefix string, h http.Header) {
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := strings.Join(h[k], ", ")
		if strings.EqualFold(k, "Authorization") || strings.EqualFold(k, "Cookie") {
			v = mask(v)
		}
		fmt.Fprintf(TraceOut, "%s%s: %s\n", prefix, k, v)
	}
}

func truncate(s string) string {
	if len(s) > maxTraceBody {
		return s[:maxTraceBody] + fmt.Sprintf("… (%d more bytes)", len(s)-maxTraceBody)
	}
	return s
}

var secretRe = regexp.MustCompile(`(?i)("?(?:access_token|refresh_token|id_token|client_secret|password|secret|value|device_code)"?\s*[:=]\s*"?)([^"&,\s}]+)`)

// MaskSecrets keeps the length so a trace still shows whether a value was
// present and roughly what it was, without leaking it.
func MaskSecrets(s string) string {
	return secretRe.ReplaceAllStringFunc(s, func(m string) string {
		sub := secretRe.FindStringSubmatch(m)
		return sub[1] + mask(sub[2])
	})
}

func mask(v string) string {
	if strings.HasPrefix(v, "Bearer ") {
		return "Bearer " + mask(v[7:])
	}
	return fmt.Sprintf("****(%d)", len(v))
}
