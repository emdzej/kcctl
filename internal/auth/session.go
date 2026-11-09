package auth

import (
	"context"
	"sync"
	"time"

	"github.com/emdzej/keycloak-cli/internal/config"
	"github.com/emdzej/keycloak-cli/internal/errs"
)

// refreshSkew renews slightly early so a token never expires mid-request.
const refreshSkew = 30 * time.Second

// Manager hands out a valid access token for one resolved connection,
// refreshing it at most once at a time: with "Revoke Refresh Token" enabled,
// Keycloak invalidates a refresh token on first use, so concurrent refreshes
// would sign the user out.
type Manager struct {
	Resolved *config.Resolved
	Client   *Client
	Store    Store

	mu      sync.Mutex
	current *Session
}

func (m *Manager) Token(ctx context.Context) (string, error) {
	if m.Resolved.Token != "" {
		return m.Resolved.Token, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current == nil {
		s, err := m.Store.Load(m.Resolved.Key)
		if err != nil {
			return "", errs.Wrap(errs.CodeAuth, err, "cannot read stored session: %v", err)
		}
		m.current = s
	}
	s := m.current
	if s.AccessValid(refreshSkew) {
		return s.AccessToken, nil
	}
	var ns *Session
	var err error
	switch {
	case s.RefreshValid():
		ns, err = m.Client.Refresh(ctx, s)
	case s != nil && s.Grant == config.GrantClientCredentials && s.ClientSecret != "":
		ns, err = m.Client.ClientCredentials(ctx, s.ClientSecret)
	case s == nil:
		return "", m.notSignedIn()
	default:
		return "", errs.New(errs.CodeAuth, "Session for context '%s' expired.", m.label()).
			WithHint("Run 'kcctl auth login' to sign in again.")
	}
	if err != nil {
		return "", err
	}
	m.current = ns
	if err := m.Store.Save(m.Resolved.Key, ns); err != nil {
		return "", errs.Wrap(errs.CodeAuth, err, "cannot store refreshed session: %v", err)
	}
	return ns.AccessToken, nil
}

// Session returns the stored session without refreshing; used by status views.
func (m *Manager) Session() (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current == nil {
		s, err := m.Store.Load(m.Resolved.Key)
		if err != nil {
			return nil, err
		}
		m.current = s
	}
	return m.current, nil
}

func (m *Manager) label() string {
	if m.Resolved.Name != "" {
		return m.Resolved.Name
	}
	return m.Resolved.Key
}

func (m *Manager) notSignedIn() error {
	return errs.New(errs.CodeAuth, "Not signed in for context '%s'.", m.label()).
		WithHint("Run 'kcctl auth login' (or pass --token / set KCCTL_TOKEN).")
}
