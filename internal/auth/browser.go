package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/emdzej/keycloak-cli/internal/config"
	"github.com/emdzej/keycloak-cli/internal/errs"
)

// BrowserTimeout bounds how long sign-in may take in the browser.
const BrowserTimeout = 5 * time.Minute

// Browser signs in with the authorization code flow + PKCE (RFC 7636) using a
// loopback redirect (RFC 8252). Keycloak matches a registered
// http://127.0.0.1/* redirect against any port on loopback, so a random free
// port works without registering it.
//
// open is called with the URL to show; it should launch a browser and may
// fail, in which case the caller has already printed the URL for the user.
func (c *Client) Browser(ctx context.Context, secret string, open func(authURL string)) (*Session, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, errs.New(errs.CodeInternal, "cannot listen on a local port for the sign-in redirect: %v", err)
	}
	defer ln.Close()
	redirect := fmt.Sprintf("http://127.0.0.1:%d/callback", ln.Addr().(*net.TCPAddr).Port)

	state, err := randomString(24)
	if err != nil {
		return nil, err
	}
	verifier, err := randomString(48)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(verifier))
	q := url.Values{
		"client_id":             {c.ClientID},
		"response_type":         {"code"},
		"redirect_uri":          {redirect},
		"scope":                 {"openid"},
		"state":                 {state},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(sum[:])},
		"code_challenge_method": {"S256"},
	}
	authURL := c.endpoint("auth") + "?" + q.Encode()

	type result struct {
		code string
		err  error
	}
	done := make(chan result, 1)
	srv := &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/callback" {
			http.NotFound(w, r)
			return
		}
		p := r.URL.Query()
		// The state check stops another page from feeding us a code.
		if subtle.ConstantTimeCompare([]byte(p.Get("state")), []byte(state)) != 1 {
			writePage(w, http.StatusBadRequest, "Sign-in failed", "The response did not match this sign-in attempt. Run kcctl auth login again.")
			return
		}
		if e := p.Get("error"); e != "" {
			msg := e
			if d := p.Get("error_description"); d != "" {
				msg += ": " + d
			}
			writePage(w, http.StatusBadRequest, "Sign-in failed", msg)
			done <- result{err: browserError(e, msg, c.ClientID)}
			return
		}
		writePage(w, http.StatusOK, "Signed in", "kcctl is signed in. You can close this tab.")
		done <- result{code: p.Get("code")}
	})}
	go func() { _ = srv.Serve(ln) }()
	defer func() {
		sctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()

	open(authURL)

	ctx, cancel := context.WithTimeout(ctx, BrowserTimeout)
	defer cancel()
	var res result
	select {
	case res = <-done:
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, errs.New(errs.CodeAuth, "sign-in was not completed within %s", BrowserTimeout).
				WithHint(redirectHint(c.ClientID))
		}
		return nil, errs.Usage("Cancelled; not signed in.")
	}
	if res.err != nil {
		return nil, res.err
	}
	return c.exchange(ctx, config.GrantBrowser, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {res.code},
		"redirect_uri":  {redirect},
		"code_verifier": {verifier},
	}, secret)
}

// redirectHint covers the failure that never reaches the CLI: Keycloak shows
// "Invalid parameter: redirect_uri" in the browser and stops there.
func redirectHint(clientID string) string {
	return "If the browser showed 'Invalid parameter: redirect_uri', add http://127.0.0.1/* to the valid redirect URIs of client '" +
		clientID + "'. Without a browser on this machine, use --grant device."
}

func browserError(code, msg, clientID string) error {
	e := errs.New(errs.CodeAuth, "sign-in rejected: %s", msg)
	if code == "unauthorized_client" {
		e.Hint = "Enable 'Standard flow' on client '" + clientID + "', or sign in with another --client-id."
	}
	return e
}

func writePage(w http.ResponseWriter, status int, title, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><title>kcctl: %[1]s</title>
<body style="font-family:system-ui,sans-serif;max-width:32rem;margin:4rem auto"><h2>%[1]s</h2><p>%[2]s</p></body>`,
		html.EscapeString(title), html.EscapeString(msg))
}

func randomString(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", errs.New(errs.CodeInternal, "no randomness available: %v", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
