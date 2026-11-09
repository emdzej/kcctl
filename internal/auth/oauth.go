// Package auth obtains, stores and refreshes admin tokens against Keycloak's
// OpenID Connect endpoints. It has no CLI dependencies.
package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/emdzej/kcctl/internal/config"
	"github.com/emdzej/kcctl/internal/errs"
)

type tokenResponse struct {
	AccessToken      string `json:"access_token"`
	RefreshToken     string `json:"refresh_token"`
	IDToken          string `json:"id_token"`
	ExpiresIn        int    `json:"expires_in"`
	RefreshExpiresIn int    `json:"refresh_expires_in"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

type Client struct {
	HTTP      *http.Client
	Server    string
	AuthRealm string
	ClientID  string
}

func (c *Client) endpoint(suffix string) string {
	return fmt.Sprintf("%s/realms/%s/protocol/openid-connect/%s", c.Server, url.PathEscape(c.AuthRealm), suffix)
}

func (c *Client) post(ctx context.Context, suffix string, form url.Values) (*tokenResponse, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint(suffix), strings.NewReader(form.Encode()))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, 0, errs.Wrap(errs.CodeNetwork, err, "cannot reach %s: %v", c.Server, err).
			WithHint("Check the server URL and your network; use -v to see the request.")
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var tr tokenResponse
	_ = json.Unmarshal(body, &tr)
	if resp.StatusCode >= 300 && tr.Error == "" {
		tr.Error = http.StatusText(resp.StatusCode)
		tr.ErrorDescription = strings.TrimSpace(string(body))
	}
	return &tr, resp.StatusCode, nil
}

func (c *Client) exchange(ctx context.Context, grant config.Grant, form url.Values, secret string) (*Session, error) {
	form.Set("client_id", c.ClientID)
	if secret != "" {
		form.Set("client_secret", secret)
	}
	tr, status, err := c.post(ctx, "token", form)
	if err != nil {
		return nil, err
	}
	if tr.Error != "" {
		return nil, tokenError(status, tr)
	}
	return c.session(grant, tr, secret), nil
}

func (c *Client) session(grant config.Grant, tr *tokenResponse, secret string) *Session {
	now := time.Now()
	s := &Session{
		AccessToken:  tr.AccessToken,
		RefreshToken: tr.RefreshToken,
		ExpiresAt:    now.Add(time.Duration(tr.ExpiresIn) * time.Second),
		Grant:        grant,
		ClientID:     c.ClientID,
		ClientSecret: secret,
	}
	if tr.RefreshExpiresIn > 0 {
		s.RefreshExpiresAt = now.Add(time.Duration(tr.RefreshExpiresIn) * time.Second)
	}
	// admin-cli issues lightweight access tokens without preferred_username,
	// so the name for status displays comes from whichever token has it.
	for _, t := range []string{tr.AccessToken, tr.IDToken} {
		if cl, err := ParseClaims(t); err == nil && cl.PreferredUsername != "" {
			s.Username = cl.PreferredUsername
			break
		}
	}
	return s
}

func tokenError(status int, tr *tokenResponse) error {
	msg := tr.Error
	if tr.ErrorDescription != "" {
		msg += ": " + tr.ErrorDescription
	}
	code := errs.CodeAuth
	if status >= 500 {
		code = errs.CodeServer
	}
	e := errs.New(code, "sign-in rejected (%d): %s", status, msg)
	// Keycloak reports a missing or wrong client secret as unauthorized_client
	// with "Invalid client credentials", not as invalid_client, so the
	// description decides between the two hints.
	badCredentials := tr.Error == "invalid_client" || strings.Contains(tr.ErrorDescription, "Invalid client credentials")
	switch {
	case badCredentials:
		e.Hint = "The client is confidential (Client authentication is on): turn that off for a CLI client, or pass --client-secret-file."
	case tr.Error == "unauthorized_client":
		e.Hint = "The client does not allow this grant. Enable it on the client or pick another --grant."
	}
	return e
}

func (c *Client) Password(ctx context.Context, username, password, secret string) (*Session, error) {
	return c.exchange(ctx, config.GrantPassword, url.Values{
		"grant_type": {"password"}, "username": {username}, "password": {password}, "scope": {"openid"},
	}, secret)
}

func (c *Client) ClientCredentials(ctx context.Context, secret string) (*Session, error) {
	return c.exchange(ctx, config.GrantClientCredentials, url.Values{"grant_type": {"client_credentials"}}, secret)
}

func (c *Client) Refresh(ctx context.Context, s *Session) (*Session, error) {
	ns, err := c.exchange(ctx, s.Grant, url.Values{
		"grant_type": {"refresh_token"}, "refresh_token": {s.RefreshToken},
	}, s.ClientSecret)
	if err != nil {
		return nil, err
	}
	// Keycloak may omit a new refresh token; keep the old one then.
	if ns.RefreshToken == "" {
		ns.RefreshToken, ns.RefreshExpiresAt = s.RefreshToken, s.RefreshExpiresAt
	}
	if ns.Username == "" {
		ns.Username = s.Username
	}
	return ns, nil
}

// Logout ends the SSO session server-side so the refresh token is dead even
// if a copy leaked; failure is not fatal because local state is cleared anyway.
func (c *Client) Logout(ctx context.Context, s *Session) error {
	if s.RefreshToken == "" {
		return nil
	}
	form := url.Values{"client_id": {c.ClientID}, "refresh_token": {s.RefreshToken}}
	if s.ClientSecret != "" {
		form.Set("client_secret", s.ClientSecret)
	}
	_, status, err := c.post(ctx, "logout", form)
	if err != nil {
		return err
	}
	if status >= 300 {
		return errs.New(errs.CodeAuth, "server-side logout returned %d", status)
	}
	return nil
}

type DeviceAuth struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

func (c *Client) StartDevice(ctx context.Context, secret string) (*DeviceAuth, error) {
	form := url.Values{"client_id": {c.ClientID}, "scope": {"openid"}}
	if secret != "" {
		form.Set("client_secret", secret)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint("auth/device"), strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, errs.Wrap(errs.CodeNetwork, err, "cannot reach %s: %v", c.Server, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		var tr tokenResponse
		_ = json.Unmarshal(body, &tr)
		if tr.Error == "" {
			tr.Error = http.StatusText(resp.StatusCode)
		}
		e := tokenError(resp.StatusCode, &tr).(*errs.Error)
		if tr.Error == "unauthorized_client" || resp.StatusCode == 400 {
			e.Hint = "Enable 'OAuth 2.0 Device Authorization Grant' on client '" + c.ClientID + "', or use --grant password."
		}
		return nil, e
	}
	var d DeviceAuth
	if err := json.Unmarshal(body, &d); err != nil {
		return nil, errs.Wrap(errs.CodeServer, err, "unexpected device authorization response")
	}
	if d.Interval <= 0 {
		d.Interval = 5
	}
	return &d, nil
}

func (c *Client) PollDevice(ctx context.Context, d *DeviceAuth, secret string) (*Session, error) {
	interval := time.Duration(d.Interval) * time.Second
	deadline := time.Now().Add(time.Duration(d.ExpiresIn) * time.Second)
	for {
		select {
		case <-ctx.Done():
			return nil, errs.Usage("Cancelled; not signed in.")
		case <-time.After(interval):
		}
		if d.ExpiresIn > 0 && time.Now().After(deadline) {
			return nil, errs.New(errs.CodeAuth, "the device code expired before sign-in completed")
		}
		form := url.Values{
			"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
			"device_code": {d.DeviceCode},
			"client_id":   {c.ClientID},
		}
		if secret != "" {
			form.Set("client_secret", secret)
		}
		tr, status, err := c.post(ctx, "token", form)
		if err != nil {
			return nil, err
		}
		switch tr.Error {
		case "":
			return c.session(config.GrantDevice, tr, secret), nil
		case "authorization_pending":
			continue
		case "slow_down":
			interval += 5 * time.Second
			continue
		default:
			return nil, tokenError(status, tr)
		}
	}
}

// Claims is the subset of the access token the CLI displays. The token is not
// verified: it is only read for status and never trusted for decisions.
type Claims struct {
	Subject           string `json:"sub"`
	PreferredUsername string `json:"preferred_username"`
	Email             string `json:"email"`
	AuthorizedParty   string `json:"azp"`
	Issuer            string `json:"iss"`
	Expiry            int64  `json:"exp"`
	RealmAccess       struct {
		Roles []string `json:"roles"`
	} `json:"realm_access"`
	ResourceAccess map[string]struct {
		Roles []string `json:"roles"`
	} `json:"resource_access"`
}

func ParseClaims(token string) (*Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return nil, errs.New(errs.CodeAuth, "token is not a JWT")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, errs.New(errs.CodeAuth, "token payload is not base64url")
	}
	var c Claims
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, errs.New(errs.CodeAuth, "token payload is not JSON")
	}
	return &c, nil
}
