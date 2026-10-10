package cloud

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// APIError is an error the server reported, in its standard envelope.
type APIError struct {
	Status  int
	Code    string
	Message string
	Fields  []FieldError
}

// FieldError names one invalid field of a request.
type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

func (e *APIError) Error() string {
	msg := e.Message
	if msg == "" {
		msg = http.StatusText(e.Status)
	}
	for _, f := range e.Fields {
		msg += fmt.Sprintf("\n  %s: %s", f.Field, f.Message)
	}
	return msg
}

// NormalizeServer checks a server address and trims it to scheme://host[/path].
func NormalizeServer(raw string) (string, error) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return "", fmt.Errorf("%q does not include a schema: use something like https://trecs.example.com", raw)
	}
	return raw, nil
}

// Client talks to one trecs server, with the cookies of a login if it has them.
type Client struct {
	Server  string
	Cookies []Cookie
	HTTP    *http.Client // nil: a default with sensible timeouts
}

// New returns a client for a server address (see NormalizeServer).
func New(server string) (*Client, error) {
	s, err := NormalizeServer(server)
	if err != nil {
		return nil, err
	}
	return &Client{Server: s}, nil
}

// FromSession returns a client logged in as the saved session. The server the
// session belongs to is used: credentials are never sent anywhere else.
func FromSession(s *Session) *Client {
	return &Client{Server: s.Server, Cookies: s.Cookies}
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	// No overall timeout: an upload of a large recording can legitimately take
	// a while. Each call is bounded by its context instead.
	return &http.Client{}
}

func (c *Client) newRequest(ctx context.Context, method, path string, body io.Reader, contentType string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.Server+path, body)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Accept", "application/json")
	// Cookies are set by hand (not through a cookie jar) so a session cookie
	// marked Secure still works against a plain-http development server.
	for _, ck := range c.Cookies {
		req.AddCookie(&http.Cookie{Name: ck.Name, Value: ck.Value})
	}
	return req, nil
}

// do sends the request and turns any non-2xx answer into an *APIError (or
// ErrNotLoggedIn for a 401 when withSession is set).
func (c *Client) do(req *http.Request, withSession bool) (*http.Response, error) {
	resp, err := c.http().Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 == 2 {
		return resp, nil
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusUnauthorized && withSession {
		return nil, ErrNotLoggedIn
	}
	var env struct {
		Error struct {
			Code    string       `json:"code"`
			Message string       `json:"message"`
			Fields  []FieldError `json:"fields"`
		} `json:"error"`
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	_ = json.Unmarshal(body, &env) // not JSON (a proxy's error page, say): fall back to the status text
	return nil, &APIError{Status: resp.StatusCode, Code: env.Error.Code, Message: env.Error.Message, Fields: env.Error.Fields}
}

// Login logs in with email and password and returns the session to save. A
// wrong password is an *APIError carrying the server's own message.
func (c *Client) Login(ctx context.Context, email, password string) (*Session, error) {
	payload, err := json.Marshal(map[string]string{"email": email, "password": password})
	if err != nil {
		return nil, err
	}
	req, err := c.newRequest(ctx, http.MethodPost, "/v1/auth/login", bytes.NewReader(payload), "application/json")
	if err != nil {
		return nil, err
	}
	resp, err := c.do(req, false)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	s := &Session{Server: c.Server, Email: email}
	for _, ck := range resp.Cookies() {
		if ck.Value == "" || ck.MaxAge < 0 {
			continue
		}
		s.Cookies = append(s.Cookies, Cookie{Name: ck.Name, Value: ck.Value})
		if !ck.Expires.IsZero() && (s.Expires.IsZero() || ck.Expires.Before(s.Expires)) {
			s.Expires = ck.Expires
		}
	}
	if len(s.Cookies) == 0 {
		return nil, errors.New("the server accepted the login but set no session cookie - is this a trecs server?")
	}
	return s, nil
}

// Logout ends the session on the server. The caller removes the local copy
// either way.
func (c *Client) Logout(ctx context.Context) error {
	req, err := c.newRequest(ctx, http.MethodPost, "/v1/auth/logout", nil, "")
	if err != nil {
		return err
	}
	resp, err := c.do(req, true)
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

// Upload describes a recording to send. TerminalPath is required; the rest are
// optional (the server defaults the name, makes it private, and - if there is
// no description - lists the commands typed).
type Upload struct {
	Name, Description, Visibility string
	TerminalPath, AudioPath       string
}

// Recording is what the server returns about an uploaded recording.
type Recording struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Visibility  string `json:"visibility"`
	DurationMs  int64  `json:"duration_ms"`
	Cols        *int   `json:"cols"`
	Rows        *int   `json:"rows"`
}

// Upload sends a recording. The files are streamed, not read into memory:
// recordings with audio can be large.
func (c *Client) Upload(ctx context.Context, u Upload) (*Recording, error) {
	if len(c.Cookies) == 0 {
		return nil, ErrNotLoggedIn
	}
	term, err := os.Open(u.TerminalPath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = term.Close() }()
	var audio *os.File
	if u.AudioPath != "" {
		if audio, err = os.Open(u.AudioPath); err != nil {
			return nil, err
		}
		defer func() { _ = audio.Close() }()
	}

	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		pw.CloseWithError(writeParts(mw, u, term, audio))
	}()

	req, err := c.newRequest(ctx, http.MethodPost, "/v1/recordings", pr, mw.FormDataContentType())
	if err != nil {
		_ = pr.Close()
		return nil, err
	}
	resp, err := c.do(req, true)
	if err != nil {
		_ = pr.Close()
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	var rec Recording
	if err := json.NewDecoder(resp.Body).Decode(&rec); err != nil {
		return nil, fmt.Errorf("the upload succeeded but the server's answer was not understood: %w", err)
	}
	return &rec, nil
}

func writeParts(mw *multipart.Writer, u Upload, term, audio *os.File) error {
	for _, f := range [][2]string{{"name", u.Name}, {"description", u.Description}, {"visibility", u.Visibility}} {
		if f[1] == "" {
			continue // leave it to the server's default
		}
		if err := mw.WriteField(f[0], f[1]); err != nil {
			return err
		}
	}
	for _, p := range []struct {
		field string
		file  *os.File
	}{{"terminal", term}, {"audio", audio}} {
		if p.file == nil {
			continue
		}
		w, err := mw.CreateFormFile(p.field, filepath.Base(p.file.Name()))
		if err != nil {
			return err
		}
		if _, err := io.Copy(w, p.file); err != nil {
			return err
		}
	}
	return mw.Close()
}

// ResolveSessionFiles accepts what `recorder record` produced - a session
// directory - or a terminal recording file, and returns the terminal file and
// the audio track beside it, if there is one.
func ResolveSessionFiles(path string) (terminal, audio string, err error) {
	fi, err := os.Stat(path)
	if err != nil {
		return "", "", err
	}
	dir := path
	terminal = path
	if fi.IsDir() {
		terminal = filepath.Join(path, "terminal.jsonl")
		if _, err := os.Stat(terminal); err != nil {
			return "", "", fmt.Errorf("%s has no terminal.jsonl: is it a session directory?", path)
		}
	} else {
		dir = filepath.Dir(path)
	}
	if a := filepath.Join(dir, "audio.mp3"); fileExists(a) {
		audio = a
	}
	return terminal, audio, nil
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

// DefaultTimeout bounds the quick calls (login, logout).
const DefaultTimeout = 30 * time.Second
