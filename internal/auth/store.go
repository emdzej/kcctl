package auth

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/emdzej/kcctl/internal/config"
)

const keyringService = "kcctl"

// Session is what survives between invocations for one token slot.
type Session struct {
	AccessToken      string       `json:"accessToken"`
	RefreshToken     string       `json:"refreshToken,omitempty"`
	ExpiresAt        time.Time    `json:"expiresAt"`
	RefreshExpiresAt time.Time    `json:"refreshExpiresAt,omitzero"`
	Grant            config.Grant `json:"grant"`
	ClientID         string       `json:"clientId"`
	Username         string       `json:"username,omitempty"`
	// ClientSecret is kept so client_credentials sessions renew without a
	// prompt, mirroring kcadm. It lives in the keyring when one is available.
	ClientSecret string `json:"clientSecret,omitempty"`
}

func (s *Session) AccessValid(skew time.Duration) bool {
	return s != nil && s.AccessToken != "" && time.Now().Add(skew).Before(s.ExpiresAt)
}

func (s *Session) RefreshValid() bool {
	if s == nil || s.RefreshToken == "" {
		return false
	}
	// Offline tokens have no refresh expiry.
	return s.RefreshExpiresAt.IsZero() || time.Now().Before(s.RefreshExpiresAt)
}

type Store interface {
	Load(key string) (*Session, error)
	Save(key string, s *Session) error
	Delete(key string) error
}

// DefaultStore prefers the OS keyring and falls back to a 0600 file when the
// keyring is disabled or unavailable (headless Linux, CI containers).
func DefaultStore() Store {
	file := &fileStore{path: filepath.Join(config.StateDir(), "tokens.json")}
	if os.Getenv("KCCTL_NO_KEYRING") == "1" {
		return file
	}
	return &keyringStore{fallback: file}
}

type keyringStore struct{ fallback *fileStore }

func (k *keyringStore) Load(key string) (*Session, error) {
	v, err := keyring.Get(keyringService, key)
	if errors.Is(err, keyring.ErrNotFound) {
		return k.fallback.Load(key)
	}
	if err != nil {
		return k.fallback.Load(key)
	}
	var s Session
	if err := json.Unmarshal([]byte(v), &s); err != nil {
		return nil, nil //nolint:nilnil // a corrupt entry is treated as signed out
	}
	return &s, nil
}

func (k *keyringStore) Save(key string, s *Session) error {
	data, _ := json.Marshal(s)
	if err := keyring.Set(keyringService, key, string(data)); err != nil {
		return k.fallback.Save(key, s)
	}
	// Don't leave a stale copy in the file once the keyring works.
	_ = k.fallback.Delete(key)
	return nil
}

func (k *keyringStore) Delete(key string) error {
	err := keyring.Delete(keyringService, key)
	ferr := k.fallback.Delete(key)
	if err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return ferr
	}
	return nil
}

type fileStore struct {
	path string
	mu   sync.Mutex
}

func (f *fileStore) read() (map[string]*Session, error) {
	data, err := os.ReadFile(f.path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]*Session{}, nil
	}
	if err != nil {
		return nil, err
	}
	m := map[string]*Session{}
	if err := json.Unmarshal(data, &m); err != nil {
		return map[string]*Session{}, nil
	}
	return m, nil
}

func (f *fileStore) Load(key string) (*Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, err := f.read()
	if err != nil {
		return nil, err
	}
	return m[key], nil
}

func (f *fileStore) Save(key string, s *Session) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, err := f.read()
	if err != nil {
		return err
	}
	m[key] = s
	data, _ := json.MarshalIndent(m, "", "  ")
	return config.WriteFilePrivate(f.path, data)
}

func (f *fileStore) Delete(key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, err := f.read()
	if err != nil {
		return err
	}
	if _, ok := m[key]; !ok {
		return nil
	}
	delete(m, key)
	data, _ := json.MarshalIndent(m, "", "  ")
	return config.WriteFilePrivate(f.path, data)
}
