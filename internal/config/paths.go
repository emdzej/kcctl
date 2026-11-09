package config

import (
	"os"
	"path/filepath"
	"runtime"
)

const appName = "kcctl"

// ConfigDir follows XDG on every platform, including macOS, because CLI users
// expect ~/.config rather than ~/Library/Application Support. Windows uses
// %AppData% as its native equivalent.
func ConfigDir() string {
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, appName)
	}
	if runtime.GOOS == "windows" {
		if d := os.Getenv("AppData"); d != "" {
			return filepath.Join(d, appName)
		}
	}
	return filepath.Join(home(), ".config", appName)
}

// StateDir holds tokens (when no keyring), history and the server-info cache.
func StateDir() string {
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, appName)
	}
	if runtime.GOOS == "windows" {
		if d := os.Getenv("LocalAppData"); d != "" {
			return filepath.Join(d, appName, "state")
		}
	}
	return filepath.Join(home(), ".local", "state", appName)
}

func ConfigFile() string {
	if f := os.Getenv("KCCTL_CONFIG"); f != "" {
		return f
	}
	return filepath.Join(ConfigDir(), "config.yaml")
}

func home() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return h
}

// WriteFilePrivate writes atomically with 0600 so a crash never leaves a
// half-written config and other users never see tokens.
func WriteFilePrivate(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
