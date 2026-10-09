// Package cloud is the recorder's side of a trecs server: logging in, keeping
// the session between runs, and uploading a recording. It speaks the server's
// HTTP API directly and uses only the standard library.
package cloud

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ErrNotLoggedIn means there is no usable session: none was saved, or it has
// expired, or the server no longer accepts it.
var ErrNotLoggedIn = errors.New("not logged in (or the session has expired): run `recorder login`")

// Cookie is one cookie the server set at login.
type Cookie struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Session is what login saves: which server, and the cookies that prove the
// login to it. It is a credential - Save writes it readable by its owner only.
type Session struct {
	Server  string    `json:"server"`
	Email   string    `json:"email,omitempty"`
	Cookies []Cookie  `json:"cookies"`
	Expires time.Time `json:"expires,omitempty"` // zero: the server gave no expiry
}

// SessionPath is where the session lives: $TRECS_SESSION_FILE if set (used by
// tests), else $XDG_CONFIG_HOME/trecs/session, else ~/.config/trecs/session.
func SessionPath() (string, error) {
	if p := os.Getenv("TRECS_SESSION_FILE"); p != "" {
		return p, nil
	}
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("cannot find a home directory for the session file: %w", err)
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "trecs", "session"), nil
}

// LoadSession reads the saved session. ErrNotLoggedIn if there is none, or it
// has expired; any other error means the file is unreadable or corrupt.
func LoadSession() (*Session, error) {
	path, err := SessionPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotLoggedIn
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	var s Session
	if err := json.Unmarshal(data, &s); err != nil || s.Server == "" || len(s.Cookies) == 0 {
		return nil, fmt.Errorf("%s is not a valid session file; run `recorder login` again", path)
	}
	if !s.Expires.IsZero() && time.Now().After(s.Expires) {
		return nil, ErrNotLoggedIn
	}
	return &s, nil
}

// Save writes the session, creating its directory (owner-only) if needed. The
// file is created with owner-only permissions before anything is written to
// it, via a temporary file and a rename, so a crash never leaves a half-written
// credential and the real file is never briefly world-readable.
func (s *Session) Save() error {
	path, err := SessionPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".session-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // a no-op once renamed
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// DeleteSession removes the saved session; there being none is not an error.
func DeleteSession() error {
	path, err := SessionPath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
