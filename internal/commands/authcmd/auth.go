// Package authcmd signs in and out and shows the current session.
package authcmd

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/emdzej/keycloak-cli/internal/app"
	"github.com/emdzej/keycloak-cli/internal/auth"
	"github.com/emdzej/keycloak-cli/internal/config"
	"github.com/emdzej/keycloak-cli/internal/errs"
)

func New(a *app.App) *cobra.Command {
	cmd := app.Group("auth", "Sign in, sign out and inspect the session")
	cmd.GroupID = "session"
	cmd.AddCommand(login(a), logout(a), status(a), whoami(a))
	return cmd
}

func login(a *app.App) *cobra.Command {
	var grant, username, passwordFile, secretFile string
	var noBrowser bool
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Sign in and store the session for the current context",
		Long: `Sign in to the auth realm and store the session (OS keychain, or a 0600 file).

Grants:
  browser             sign in in your browser (works with identity providers and
                      passkeys); the client needs 'Standard flow' enabled and
                      http://127.0.0.1/* as a valid redirect URI
  password            username + password against the client (default: admin-cli)
  client_credentials  a confidential client's service account; for CI
  device              approve with a code in a browser on any device; the client
                      must enable the device grant (for SSH sessions)

The stock admin-cli client allows only password and client_credentials. For
browser or device sign-in, create a public client (e.g. 'kcctl') in the auth
realm once and point the context at it with --client-id. Step-by-step setup
and troubleshooting:
https://github.com/emdzej/keycloak-cli#browser-sign-in-accounts-without-a-password

Secrets are read from --password-file / --client-secret-file, piped stdin,
or a hidden prompt; never from a flag value. KCCTL_PASSWORD and
KCCTL_CLIENT_SECRET are honoured for unattended runs.`,
		Example: `  kcctl auth login --grant browser --client-id kcctl
  kcctl auth login --username admin
  kcctl auth login --grant client_credentials --client-id ci --client-secret-file ./secret
  kcctl auth login --grant device`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			r, err := a.Resolve()
			if err != nil {
				return err
			}
			if r.Token != "" {
				return errs.Usage("--token / KCCTL_TOKEN is set; there is nothing to sign in to")
			}
			g := r.Grant
			if grant != "" {
				g = config.Grant(grant)
			}
			ac, err := a.AuthClient()
			if err != nil {
				return err
			}
			readSecret := func(label string, file string, env string) (string, error) {
				if file == "" {
					if v := os.Getenv(env); v != "" {
						return v, nil
					}
				}
				return a.ReadSecret(file, label)
			}
			// A secret for password/device grants is optional (public clients).
			optionalSecret := func() (string, error) {
				if secretFile != "" {
					return a.ReadSecret(secretFile, "Client secret")
				}
				return os.Getenv("KCCTL_CLIENT_SECRET"), nil
			}
			var s *auth.Session
			switch g {
			case config.GrantPassword:
				user := username
				if user == "" {
					user = os.Getenv("KCCTL_USERNAME")
				}
				if user == "" {
					user, err = a.Prompt("Username", r.Username)
					if err != nil {
						return err
					}
				}
				pw, err := readSecret("Password for "+user, passwordFile, "KCCTL_PASSWORD")
				if err != nil {
					return err
				}
				secret, err := optionalSecret()
				if err != nil {
					return err
				}
				s, err = ac.Password(ctx, user, pw, secret)
				if err != nil {
					return err
				}
			case config.GrantClientCredentials:
				secret, err := readSecret("Client secret for "+r.ClientID, secretFile, "KCCTL_CLIENT_SECRET")
				if err != nil {
					return err
				}
				s, err = ac.ClientCredentials(ctx, secret)
				if err != nil {
					return err
				}
			case config.GrantBrowser:
				if a.Flags.NoInput {
					return errs.Usage("browser sign-in needs a person; use --grant client_credentials with --no-input")
				}
				secret, err := optionalSecret()
				if err != nil {
					return err
				}
				st := a.Printer.ErrStyle
				s, err = ac.Browser(ctx, secret, func(u string) {
					// Always print the link: the browser may not open (SSH,
					// containers) or may open on the wrong profile.
					if noBrowser {
						fmt.Fprintf(a.Err, "Open this link to sign in:\n  %s\n", st.Cyan(u))
					} else {
						fmt.Fprintf(a.Err, "Opening your browser to sign in. If it doesn't open, use:\n  %s\n", st.Cyan(u))
						if err := auth.OpenBrowser(u); err != nil {
							fmt.Fprintln(a.Err, st.Yellow("Could not open a browser: "+err.Error()))
						}
					}
					// A misconfigured client fails in the browser and never
					// calls back, so say up front what that looks like.
					fmt.Fprintln(a.Err, st.Dim("Waiting for sign-in… (Ctrl-C to cancel). If the browser shows\n'Invalid parameter: redirect_uri', client '"+r.ClientID+"' needs http://127.0.0.1/* as a redirect URI."))
				})
				if err != nil {
					return err
				}
			case config.GrantDevice:
				if a.Flags.NoInput {
					return errs.Usage("device sign-in needs a person to approve it; use --grant client_credentials with --no-input")
				}
				secret, err := optionalSecret()
				if err != nil {
					return err
				}
				d, err := ac.StartDevice(ctx, secret)
				if err != nil {
					return err
				}
				link := d.VerificationURIComplete
				if link == "" {
					link = d.VerificationURI
				}
				st := a.Printer.ErrStyle
				fmt.Fprintf(a.Err, "Open %s\nand confirm the code %s\n", st.Cyan(link), st.Bold(d.UserCode))
				fmt.Fprintln(a.Err, st.Dim("Waiting for approval… (Ctrl-C to cancel)"))
				s, err = ac.PollDevice(ctx, d, secret)
				if err != nil {
					return err
				}
			default:
				return errs.Usage("--grant must be one of %s", strings.Join(config.GrantNames(), ", "))
			}
			if err := a.Store.Save(r.Key, s); err != nil {
				return errs.Wrap(errs.CodeAuth, err, "signed in, but could not store the session: %v", err)
			}
			who := s.Username
			if who == "" {
				who = "service account of " + r.ClientID
			}
			a.Printer.Success("Signed in to %s (realm %s) as %s.", r.Server, r.AuthRealm, who)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&grant, "grant", "", "browser|password|client_credentials|device (default: context grant, then password)")
	f.BoolVar(&noBrowser, "no-browser", false, "with --grant browser: print the link instead of opening a browser")
	f.StringVar(&username, "username", "", "username for the password grant (default: context username)")
	f.StringVar(&passwordFile, "password-file", "", "read the password from this file ('-' for stdin)")
	f.StringVar(&secretFile, "client-secret-file", "", "read the client secret from this file ('-' for stdin)")
	cmd.RegisterFlagCompletionFunc("grant", app.Values(config.GrantNames()...))
	return cmd
}

func logout(a *app.App) *cobra.Command {
	return &cobra.Command{
		Use: "logout", Short: "End the session on the server and forget it locally", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			r, err := a.Resolve()
			if err != nil {
				return err
			}
			s, _ := a.Store.Load(r.Key)
			if s == nil {
				a.Printer.Note("Not signed in; nothing to do.")
				return nil
			}
			if ac, err := a.AuthClient(); err == nil {
				if err := ac.Logout(cmd.Context(), s); err != nil {
					a.Printer.Warn("server-side logout failed (%v); the local session is removed anyway", err)
				}
			}
			if err := a.Store.Delete(r.Key); err != nil {
				return err
			}
			a.Printer.Success("Signed out of %s.", r.Server)
			return nil
		},
	}
}

// Status describes the session without contacting the server.
func Status(a *app.App) (map[string]any, error) {
	r, err := a.Resolve()
	if err != nil {
		return nil, err
	}
	m := map[string]any{
		"context": r.Name, "server": r.Server, "authRealm": r.AuthRealm,
		"realm": r.Realm, "clientId": r.ClientID,
	}
	tok := r.Token
	if tok != "" {
		m["source"] = "--token/KCCTL_TOKEN"
	} else {
		s, err := a.Store.Load(r.Key)
		if err != nil {
			return nil, err
		}
		if s == nil {
			m["state"] = "not signed in"
			return m, nil
		}
		m["grant"] = string(s.Grant)
		m["user"] = s.Username
		m["source"] = "stored session"
		tok = s.AccessToken
		switch {
		case s.AccessValid(0):
			m["state"] = "signed in"
		case s.RefreshValid() || (s.Grant == config.GrantClientCredentials && s.ClientSecret != ""):
			m["state"] = "signed in (token refreshes on next use)"
		default:
			m["state"] = "session expired"
		}
		if !s.RefreshExpiresAt.IsZero() {
			m["sessionExpires"] = s.RefreshExpiresAt.Local().Format(time.RFC3339)
		}
	}
	if cl, err := auth.ParseClaims(tok); err == nil {
		if cl.PreferredUsername != "" {
			m["user"] = cl.PreferredUsername
		}
		if cl.Expiry > 0 {
			exp := time.Unix(cl.Expiry, 0)
			m["tokenExpires"] = exp.Local().Format(time.RFC3339)
		}
	}
	return m, nil
}

func status(a *app.App) *cobra.Command {
	return &cobra.Command{
		Use: "status", Short: "Show the session for the current context (no network)", Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			m, err := Status(a)
			if err != nil {
				return err
			}
			return a.Printer.Print(m, nil)
		},
	}
}

func whoami(a *app.App) *cobra.Command {
	return &cobra.Command{
		Use: "whoami", Short: "Show who the token belongs to and its admin roles", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			tm, err := a.Tokens()
			if err != nil {
				return err
			}
			tok, err := tm.Token(cmd.Context())
			if err != nil {
				return err
			}
			cl, err := auth.ParseClaims(tok)
			if err != nil {
				return err
			}
			user := cl.PreferredUsername
			if s, _ := tm.Session(); user == "" && s != nil {
				// Lightweight access tokens (admin-cli's default) omit it.
				user = s.Username
			}
			m := map[string]any{
				"username": user, "subject": cl.Subject, "issuer": cl.Issuer, "client": cl.AuthorizedParty,
				"expires": time.Unix(cl.Expiry, 0).Local().Format(time.RFC3339),
			}
			if cl.Email != "" {
				m["email"] = cl.Email
			}
			if len(cl.RealmAccess.Roles) > 0 {
				m["realmRoles"] = toAny(cl.RealmAccess.Roles)
			}
			clientRoles := map[string]any{}
			for k, v := range cl.ResourceAccess {
				sort.Strings(v.Roles)
				clientRoles[k] = strings.Join(v.Roles, ", ")
			}
			if len(clientRoles) > 0 {
				m["clientRoles"] = clientRoles
			}
			return a.Printer.Print(m, nil)
		},
	}
}

func toAny(s []string) []any {
	sort.Strings(s)
	out := make([]any, len(s))
	for i, x := range s {
		out[i] = x
	}
	return out
}
