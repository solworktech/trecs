package cloud

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func sessionEnv(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "cfg", "trecs", "session")
	t.Setenv("TRECS_SESSION_FILE", p)
	return p
}

func writeAPIError(w http.ResponseWriter, status int, code, msg string, fields ...FieldError) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": code, "message": msg, "fields": fields}})
}

func TestLoginSavesASessionOnlyTheOwnerCanRead(t *testing.T) {
	path := sessionEnv(t)
	exp := time.Now().Add(24 * time.Hour).UTC().Truncate(time.Second)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in map[string]string
		_ = json.NewDecoder(r.Body).Decode(&in)
		if r.Method != "POST" || r.URL.Path != "/v1/auth/login" || in["email"] != "ada@example.com" || in["password"] != "s3cret" {
			writeAPIError(w, 401, "invalid_credentials", "Wrong email or password.")
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "trecs_session", Value: "tok123", Path: "/", Expires: exp, Secure: true, HttpOnly: true})
		w.WriteHeader(200)
		_, _ = io.WriteString(w, `{"author":{}}`)
	}))
	defer srv.Close()

	c, err := New(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	s, err := c.Login(context.Background(), "ada@example.com", "s3cret")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadSession()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Server != srv.URL || loaded.Email != "ada@example.com" || len(loaded.Cookies) != 1 || loaded.Cookies[0].Value != "tok123" || !loaded.Expires.Equal(exp) {
		t.Errorf("loaded = %+v", loaded)
	}
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(path)
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("session file mode = %v, want 0600: it is a credential", fi.Mode().Perm())
		}
		di, _ := os.Stat(filepath.Dir(path))
		if di.Mode().Perm() != 0o700 {
			t.Errorf("session directory mode = %v, want 0700", di.Mode().Perm())
		}
	}
	// saving again replaces it, and leaves no temporary files behind
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Errorf("directory holds %d entries after two saves, want just the session", len(entries))
	}
}

func TestFailedLoginReportsTheServersMessageAndSavesNothing(t *testing.T) {
	path := sessionEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeAPIError(w, 401, "invalid_credentials", "Wrong email or password.")
	}))
	defer srv.Close()
	c, _ := New(srv.URL)
	_, err := c.Login(context.Background(), "ada@example.com", "nope")
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != 401 || ae.Code != "invalid_credentials" || !strings.Contains(err.Error(), "Wrong email or password") {
		t.Errorf("err = %v", err)
	}
	if _, statErr := os.Stat(path); statErr == nil {
		t.Error("a failed login must not leave a session file")
	}
	// a login that "succeeds" without setting a cookie is not a trecs server
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	defer srv2.Close()
	c2, _ := New(srv2.URL)
	if _, err := c2.Login(context.Background(), "a@b.co", "x"); err == nil || !strings.Contains(err.Error(), "no session cookie") {
		t.Errorf("err = %v", err)
	}
}

func TestSessionLoadingNamesWhatIsWrong(t *testing.T) {
	path := sessionEnv(t)
	if _, err := LoadSession(); !errors.Is(err, ErrNotLoggedIn) {
		t.Errorf("no file: %v", err)
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	_ = os.WriteFile(path, []byte("not json"), 0o600)
	if _, err := LoadSession(); err == nil || errors.Is(err, ErrNotLoggedIn) || !strings.Contains(err.Error(), "recorder login") {
		t.Errorf("corrupt file: %v", err)
	}
	expired := &Session{Server: "http://x.test", Cookies: []Cookie{{Name: "c", Value: "v"}}, Expires: time.Now().Add(-time.Minute)}
	if err := expired.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSession(); !errors.Is(err, ErrNotLoggedIn) {
		t.Errorf("expired: %v", err)
	}
	if err := DeleteSession(); err != nil {
		t.Fatal(err)
	}
	if err := DeleteSession(); err != nil {
		t.Errorf("deleting what is not there must not fail: %v", err)
	}
}

func TestUploadSendsTheCookieTheFieldsAndTheFiles(t *testing.T) {
	dir := t.TempDir()
	termPath := filepath.Join(dir, "terminal.jsonl")
	audioPath := filepath.Join(dir, "audio.mp3")
	termBody := `{"timestamp":1,"data":"x"}` + "\n" + strings.Repeat(`{"timestamp":2,"data":"y"}`+"\n", 5000)
	_ = os.WriteFile(termPath, []byte(termBody), 0o644)
	_ = os.WriteFile(audioPath, []byte("ID3-audio"), 0o644)

	var got struct {
		cookie, name, desc, vis, term, audio string
		hasDesc                              bool
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/v1/recordings" {
			http.NotFound(w, r)
			return
		}
		ck, _ := r.Cookie("trecs_session")
		if ck != nil {
			got.cookie = ck.Value
		}
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			writeAPIError(w, 400, "bad_form", err.Error())
			return
		}
		got.name, got.vis = r.FormValue("name"), r.FormValue("visibility")
		_, got.hasDesc = r.MultipartForm.Value["description"]
		for field, dst := range map[string]*string{"terminal": &got.term, "audio": &got.audio} {
			if f, _, err := r.FormFile(field); err == nil {
				b, _ := io.ReadAll(f)
				*dst = string(b)
			}
		}
		w.WriteHeader(201)
		_, _ = io.WriteString(w, `{"id":"abc","name":"My demo","visibility":"unlisted","duration_ms":1234,"cols":150,"rows":40,"description":"ls\ndf"}`)
	}))
	defer srv.Close()

	c := FromSession(&Session{Server: srv.URL, Cookies: []Cookie{{Name: "trecs_session", Value: "tok123"}}})
	rec, err := c.Upload(context.Background(), Upload{Name: "My demo", Visibility: "unlisted", TerminalPath: termPath, AudioPath: audioPath})
	if err != nil {
		t.Fatal(err)
	}
	if rec.ID != "abc" || rec.DurationMs != 1234 || *rec.Cols != 150 || rec.Description != "ls\ndf" {
		t.Errorf("rec = %+v", rec)
	}
	if got.cookie != "tok123" || got.name != "My demo" || got.vis != "unlisted" {
		t.Errorf("server saw %+v", got)
	}
	if got.hasDesc {
		t.Error("an unset description must be left out, so the server's default (the command list) applies")
	}
	if got.term != termBody || got.audio != "ID3-audio" {
		t.Errorf("files not delivered intact: terminal %d bytes (want %d), audio %q", len(got.term), len(termBody), got.audio)
	}
	// without audio: just the terminal
	got.audio = ""
	if _, err := c.Upload(context.Background(), Upload{TerminalPath: termPath}); err != nil || got.audio != "" || got.term != termBody {
		t.Errorf("no-audio upload: err=%v audio=%q", err, got.audio)
	}
}

func TestUploadErrors(t *testing.T) {
	dir := t.TempDir()
	termPath := filepath.Join(dir, "t.jsonl")
	_ = os.WriteFile(termPath, []byte(`{"timestamp":1,"data":"x"}`), 0o644)
	status := 401
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		writeAPIError(w, status, "x", "the server said no", FieldError{"terminal", "not a valid trecs recording"})
	}))
	defer srv.Close()
	logged := FromSession(&Session{Server: srv.URL, Cookies: []Cookie{{Name: "c", Value: "v"}}})

	if _, err := logged.Upload(context.Background(), Upload{TerminalPath: termPath}); !errors.Is(err, ErrNotLoggedIn) {
		t.Errorf("a 401 means the session is no longer good: %v", err)
	}
	status = 422
	_, err := logged.Upload(context.Background(), Upload{TerminalPath: termPath})
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != 422 || !strings.Contains(err.Error(), "terminal: not a valid trecs recording") {
		t.Errorf("a refused upload must show the server's reason, field by field: %v", err)
	}
	if _, err := (&Client{Server: srv.URL}).Upload(context.Background(), Upload{TerminalPath: termPath}); !errors.Is(err, ErrNotLoggedIn) {
		t.Errorf("no cookies: %v", err)
	}
	if _, err := logged.Upload(context.Background(), Upload{TerminalPath: filepath.Join(dir, "missing.jsonl")}); err == nil {
		t.Error("a missing file must fail before anything is sent")
	}
}

func TestLogoutCallsTheServer(t *testing.T) {
	var hit string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ck, _ := r.Cookie("c")
		if ck != nil {
			hit = r.Method + " " + r.URL.Path + " " + ck.Value
		}
		w.WriteHeader(204)
	}))
	defer srv.Close()
	if err := FromSession(&Session{Server: srv.URL, Cookies: []Cookie{{Name: "c", Value: "v"}}}).Logout(context.Background()); err != nil {
		t.Fatal(err)
	}
	if hit != "POST /v1/auth/logout v" {
		t.Errorf("hit = %q", hit)
	}
}

func TestNormalizeServer(t *testing.T) {
	for in, want := range map[string]string{"https://trecs.example.com/": "https://trecs.example.com", " http://localhost:8080 ": "http://localhost:8080", "https://h.test/api//": "https://h.test/api"} {
		if got, err := NormalizeServer(in); err != nil || got != want {
			t.Errorf("%q -> %q, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "trecs.example.com", "ftp://x.test", "https://", "https://user:pw@x.test", "javascript:alert(1)"} {
		if _, err := NormalizeServer(bad); err == nil {
			t.Errorf("%q should be refused", bad)
		}
	}
}

func TestResolveSessionFiles(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := ResolveSessionFiles(dir); err == nil || !strings.Contains(err.Error(), "terminal.jsonl") {
		t.Errorf("an empty dir: %v", err)
	}
	_ = os.WriteFile(filepath.Join(dir, "terminal.jsonl"), []byte("x"), 0o644)
	if term, audio, err := ResolveSessionFiles(dir); err != nil || filepath.Base(term) != "terminal.jsonl" || audio != "" {
		t.Errorf("dir without audio: %q %q %v", term, audio, err)
	}
	_ = os.WriteFile(filepath.Join(dir, "audio.mp3"), []byte("x"), 0o644)
	for _, in := range []string{dir, filepath.Join(dir, "terminal.jsonl")} { // the directory, or the file in it
		if term, audio, err := ResolveSessionFiles(in); err != nil || filepath.Base(term) != "terminal.jsonl" || filepath.Base(audio) != "audio.mp3" {
			t.Errorf("%s: %q %q %v", in, term, audio, err)
		}
	}
}
