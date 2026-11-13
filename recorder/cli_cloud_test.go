package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/solworktech/trecs/cloud"
)

// exited is what a command's call of exit(n) panics with, so that the code after it does not run (as it would not in a program).
type exited int

// runCommand runs f as the program would, with what it prints to stdout and stderr captured and exit(n) stopping it. stdin is what
// it reads.
func runCommand(t *testing.T, stdin string, f func()) (stdout, stderr string, code int) {
	t.Helper()
	oldOut, oldErr, oldIn, oldExit := os.Stdout, os.Stderr, os.Stdin, exit
	defer func() { os.Stdout, os.Stderr, os.Stdin, exit = oldOut, oldErr, oldIn, oldExit }()
	outR, outW, _ := os.Pipe()
	errR, errW, _ := os.Pipe()
	inR, inW, _ := os.Pipe()
	_, _ = inW.WriteString(stdin)
	_ = inW.Close()
	os.Stdout, os.Stderr, os.Stdin = outW, errW, inR
	exit = func(n int) { panic(exited(n)) }

	var wg sync.WaitGroup
	var outB, errB []byte
	wg.Add(2)
	go func() { defer wg.Done(); outB, _ = io.ReadAll(outR) }()
	go func() { defer wg.Done(); errB, _ = io.ReadAll(errR) }()
	func() {
		defer func() {
			if r := recover(); r != nil {
				n, ok := r.(exited)
				if !ok {
					panic(r)
				}
				code = int(n)
			}
		}()
		f()
	}()
	_ = outW.Close()
	_ = errW.Close()
	wg.Wait()
	return string(outB), string(errB), code
}

// withServer points the saved session at a test server that answers with h, and returns the server's address.
func withServer(t *testing.T, h http.HandlerFunc) string {
	t.Helper()
	t.Setenv("TRECS_SESSION_FILE", filepath.Join(t.TempDir(), "session"))
	t.Setenv("TRECS_SERVER", "")
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	s := &cloud.Session{Server: srv.URL, Email: "ada@example.com", Cookies: []cloud.Cookie{{Name: "trecs_session", Value: "tok"}}}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	return srv.URL
}

func jsonReply(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

func apiRefusal(w http.ResponseWriter, status int, code, msg string) {
	b, _ := json.Marshal(map[string]any{"error": map[string]any{"code": code, "message": msg}})
	jsonReply(w, status, string(b))
}

// a recording small enough to be sent, with a command in it and nothing a secret scan would object to
func sessionDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	data, err := os.ReadFile(filepath.Join("..", "lib", "testdata", "marks-script.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "terminal.jsonl"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestLoginSavesTheSessionAndLogoutRemovesIt(t *testing.T) {
	var loggedOut bool
	srvURL := withServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/auth/login":
			var in map[string]string
			_ = json.NewDecoder(r.Body).Decode(&in)
			if in["email"] != "grace@example.com" || in["password"] != "hunter2" {
				apiRefusal(w, 401, "invalid_credentials", "Wrong email or password.")
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "trecs_session", Value: "new", Path: "/"})
			jsonReply(w, 200, `{"author":{}}`)
		case "/v1/auth/logout":
			loggedOut = true
			jsonReply(w, 200, `{}`)
		default:
			http.NotFound(w, r)
		}
	})
	_ = cloud.DeleteSession()

	out, errOut, code := runCommand(t, "hunter2\n", func() {
		runLogin([]string{"-server", srvURL, "-email", "grace@example.com", "-password-stdin"})
	})
	if code != 0 || !strings.Contains(out, "Logged in to "+srvURL) || !strings.Contains(out, "grace@example.com") {
		t.Fatalf("login: code %d, stdout %q, stderr %q", code, out, errOut)
	}
	if s, err := cloud.LoadSession(); err != nil || s.Email != "grace@example.com" {
		t.Fatalf("the session was not saved: %+v, %v", s, err)
	}

	out, _, code = runCommand(t, "", runLogout)
	if code != 0 || !loggedOut || !strings.Contains(out, "Logged out of") {
		t.Errorf("logout: code %d, told the server %v, stdout %q", code, loggedOut, out)
	}
	if _, err := cloud.LoadSession(); err == nil {
		t.Error("the session is still saved after logging out")
	}
	out, _, code = runCommand(t, "", runLogout)
	if code != 0 || !strings.Contains(out, "Not logged in") {
		t.Errorf("logout with no session: code %d, stdout %q", code, out)
	}
}

func TestARefusedLoginFailsWithTheHint(t *testing.T) {
	srvURL := withServer(t, func(w http.ResponseWriter, r *http.Request) {
		apiRefusal(w, 401, "invalid_credentials", "Wrong email or password.")
	})
	_, errOut, code := runCommand(t, "nope\n", func() {
		runLogin([]string{"-server", srvURL, "-email", "grace@example.com", "-password-stdin"})
	})
	if code != 1 || !strings.Contains(errOut, "login failed") || !strings.Contains(errOut, "forgot-password") {
		t.Errorf("code %d, stderr %q", code, errOut)
	}
}

func TestLogoutWorksWhenTheServerCannotBeReached(t *testing.T) {
	withServer(t, func(w http.ResponseWriter, r *http.Request) { apiRefusal(w, 500, "boom", "down") })
	_, errOut, code := runCommand(t, "", runLogout)
	if code != 0 || !strings.Contains(errOut, "could not be told") {
		t.Errorf("code %d, stderr %q", code, errOut)
	}
	if _, err := cloud.LoadSession(); err == nil {
		t.Error("the local session must go whatever the server says")
	}
}

func TestUploadSendsTheRecordingAndSaysWhereToWatchIt(t *testing.T) {
	var gotName, gotVisibility, gotMethod string
	var gotTerminal bool
	srvURL := withServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method + " " + r.URL.Path
		_ = r.ParseMultipartForm(32 << 20)
		if r.MultipartForm != nil {
			gotName, gotVisibility = r.FormValue("name"), r.FormValue("visibility")
			_, gotTerminal = r.MultipartForm.File["terminal"]
		}
		jsonReply(w, 201, `{"id":"11111111-2222-3333-4444-555555555555","name":"demo","visibility":"unlisted","duration_ms":65000}`)
	})
	dir := sessionDir(t)
	out, errOut, code := runCommand(t, "", func() {
		runUpload([]string{"-name", "demo", "-visibility", "unlisted", "-allow-secrets", dir})
	})
	if code != 0 || gotMethod != "POST /v1/recordings" || gotName != "demo" || gotVisibility != "unlisted" || !gotTerminal {
		t.Fatalf("code %d, request %q name %q visibility %q terminal %v, stderr %q", code, gotMethod, gotName, gotVisibility, gotTerminal, errOut)
	}
	if !strings.Contains(out, `Uploaded "demo" (1:05, unlisted)`) || !strings.Contains(out, "Watch: "+srvURL+"/watch/11111111-2222-3333-4444-555555555555") {
		t.Errorf("stdout %q", out)
	}
}

func TestUploadAnnotationsAsDescriptionWithNoneMarked(t *testing.T) {
	withServer(t, func(w http.ResponseWriter, r *http.Request) {
		jsonReply(w, 201, `{"id":"11111111-2222-3333-4444-555555555555","name":"demo","visibility":"private","duration_ms":1000}`)
	})
	_, errOut, code := runCommand(t, "", func() { runUpload([]string{"-aad", "-allow-secrets", sessionDir(t)}) })
	if code != 0 || !strings.Contains(errOut, "No annotation is marked") {
		t.Errorf("code %d, stderr %q", code, errOut)
	}
}

func TestUploadRefusedByTheServerSaysHowToTryAgain(t *testing.T) {
	withServer(t, func(w http.ResponseWriter, r *http.Request) { apiRefusal(w, 500, "boom", "the server fell over") })
	dir := sessionDir(t)
	_, errOut, code := runCommand(t, "", func() { runUpload([]string{"-allow-secrets", dir}) })
	if code != 1 || !strings.Contains(errOut, "upload failed") || !strings.Contains(errOut, "upload "+filepath.Join(dir, "terminal.jsonl")) {
		t.Errorf("code %d, stderr %q", code, errOut)
	}
}

func TestUploadRefusesWhatCannotWork(t *testing.T) {
	withServer(t, func(w http.ResponseWriter, r *http.Request) { t.Error("nothing should reach the server") })
	dir := sessionDir(t)
	for name, c := range map[string]struct {
		args []string
		want string
	}{
		"no path":                    {nil, "usage:"},
		"two descriptions":           {[]string{"-description", "x", "-cad", dir}, "choose one of"},
		"a markdown file that isn't": {[]string{"-markdown", filepath.Join(dir, "nope.md"), dir}, "not a readable file"},
		"a path that isn't":          {[]string{filepath.Join(dir, "nowhere")}, "no such file"},
		"a bad visibility":           {[]string{"-visibility", "everyone", "-allow-secrets", dir}, "visibility must be"},
	} {
		_, errOut, code := runCommand(t, "", func() { runUpload(c.args) })
		if code != 1 || !strings.Contains(errOut, c.want) {
			t.Errorf("%s: code %d, stderr %q (want %q)", name, code, errOut, c.want)
		}
	}
}

func TestUploadIsNotDoneWhenSecretsAreFoundAndNobodyAnswers(t *testing.T) {
	withServer(t, func(w http.ResponseWriter, r *http.Request) { t.Error("a recording with a secret in it was sent") })
	dir := t.TempDir()
	// a recording whose command line holds a token
	line, _ := json.Marshal(map[string]any{"timestamp": 1, "data": "\x1b]133;A\a$ \x1b]133;B\aGITHUB_TOKEN=" + testToken + "\r\n\x1b]133;C\a\x1b]133;D;0\a", "width": 0, "height": 0})
	_ = os.WriteFile(filepath.Join(dir, "terminal.jsonl"), []byte(`{"type":"meta","width":80,"height":24}`+"\n"+string(line)+"\n"), 0o644)
	_, _, code := runCommand(t, "", func() { runUpload([]string{dir}) })
	if code != 1 {
		t.Errorf("code %d: a recording with a secret in it must not be uploaded without a yes", code)
	}
}

func TestUpdateChangesWhatIsGiven(t *testing.T) {
	var method, path, name, cad string
	srvURL := withServer(t, func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		_ = r.ParseMultipartForm(32 << 20)
		name, cad = r.FormValue("name"), r.FormValue("commands_as_description")
		jsonReply(w, 200, `{"id":"11111111-2222-3333-4444-555555555555","name":"Renamed","visibility":"public","duration_ms":9000}`)
	})
	id := "11111111-2222-3333-4444-555555555555"
	out, errOut, code := runCommand(t, "", func() { runUpdate([]string{"-name", "Renamed", srvURL + "/watch/" + id}) })
	if code != 0 || method != "PATCH" || path != "/v1/recordings/"+id || name != "Renamed" {
		t.Fatalf("code %d, %s %s name %q, stderr %q", code, method, path, name, errOut)
	}
	if !strings.Contains(out, `Updated "Renamed" (0:09, public): name`) || !strings.Contains(out, "Watch: "+srvURL+"/watch/"+id) {
		t.Errorf("stdout %q", out)
	}

	dir := sessionDir(t)
	out, errOut, code = runCommand(t, "", func() { runUpdate([]string{"-cad", "-allow-secrets", id, dir}) })
	if code != 0 || cad != "true" || !strings.Contains(out, "terminal") || !strings.Contains(out, "description") {
		t.Errorf("with the session: code %d, commands_as_description %q, stdout %q, stderr %q", code, cad, out, errOut)
	}
}

func TestUpdateRefusesWhatCannotWork(t *testing.T) {
	withServer(t, func(w http.ResponseWriter, r *http.Request) { t.Error("nothing should reach the server") })
	id := "11111111-2222-3333-4444-555555555555"
	for name, c := range map[string]struct {
		args []string
		want string
	}{
		"no id":                           {nil, "usage:"},
		"a bad id":                        {[]string{"-name", "x", "nope"}, "not a recording id"},
		"nothing to change":               {[]string{id}, "nothing to change"},
		"a bad visibility":                {[]string{"-visibility", "all", id}, "visibility must be"},
		"two descriptions":                {[]string{"-description", "x", "-aad", id}, "choose one of"},
		"a description without a session": {[]string{"-cad", id}, "give its directory"},
		"an audio file that isn't":        {[]string{"-audio", "/nonexistent/a.mp3", id}, "not a readable file"},
	} {
		_, errOut, code := runCommand(t, "", func() { runUpdate(c.args) })
		if code != 1 || !strings.Contains(errOut, c.want) {
			t.Errorf("%s: code %d, stderr %q (want %q)", name, code, errOut, c.want)
		}
	}
}

func TestUpdateRefusedForNoRoomSaysTheRecordingIsAsItWas(t *testing.T) {
	withServer(t, func(w http.ResponseWriter, r *http.Request) {
		apiRefusal(w, 403, "quota_exceeded", "You have used all your storage.")
	})
	_, errOut, code := runCommand(t, "", func() { runUpdate([]string{"-name", "x", "11111111-2222-3333-4444-555555555555"}) })
	if code != 1 || !strings.Contains(errOut, "as it was") {
		t.Errorf("code %d, stderr %q", code, errOut)
	}
}

func TestLivestreamCreatePrintsWhatIsNeededToStream(t *testing.T) {
	var body map[string]string
	withServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		jsonReply(w, 201, `{"id":"0123abcd-0123-4abc-8abc-0123456789ab","name":"talk","visibility":"unlisted","status":"waiting","url":"https://s/live/0123abcd"}`)
	})
	out, _, code := runCommand(t, "", func() { runLivestream([]string{"create", "-name", "talk", "-visibility", "unlisted"}) })
	if code != 0 || body["name"] != "talk" || body["visibility"] != "unlisted" {
		t.Fatalf("code %d, body %v", code, body)
	}
	for _, want := range []string{`"talk" (unlisted)`, "ID: 0123abcd-0123-4abc-8abc-0123456789ab", "Watch page: https://s/live/0123abcd", "livestream 0123abcd-0123-4abc-8abc-0123456789ab"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout lacks %q: %q", want, out)
		}
	}
}

func TestLivestreamCreateRefusals(t *testing.T) {
	withServer(t, func(w http.ResponseWriter, r *http.Request) { apiRefusal(w, 500, "boom", "down") })
	for name, c := range map[string]struct {
		args []string
		want string
	}{
		"an argument":    {[]string{"create", "extra"}, "usage:"},
		"a visibility":   {[]string{"create", "-visibility", "x"}, "visibility must be"},
		"a server error": {[]string{"create"}, "could not create the livestream"},
	} {
		_, errOut, code := runCommand(t, "", func() { runLivestream(c.args) })
		if code != 1 || !strings.Contains(errOut, c.want) {
			t.Errorf("%s: code %d, stderr %q", name, code, errOut)
		}
	}
	t.Setenv("TRECS_SESSION_FILE", filepath.Join(t.TempDir(), "none"))
	if _, errOut, code := runCommand(t, "", func() { runLivestream([]string{"create"}) }); code != 1 || errOut == "" {
		t.Errorf("not logged in: code %d, stderr %q", code, errOut)
	}
}

func TestStreamingToALivestreamThatCannotBeStreamedTo(t *testing.T) {
	const id = "0123abcd-0123-4abc-8abc-0123456789ab"
	status := ""
	withServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/livestreams/"+id && status == "missing":
			apiRefusal(w, 404, "livestream_not_found", "no such stream")
		case r.URL.Path == "/v1/livestreams/"+id:
			jsonReply(w, 200, `{"id":"`+id+`","status":"live","url":"https://s/live/x","started_at":"2026-11-13T10:00:00Z"}`)
		}
	})
	for name, c := range map[string]struct {
		args   []string
		status string
		want   string
	}{
		"no id":         {nil, "", "usage:"},
		"audio":         {[]string{id, "-audio"}, "", "-audio is not supported"},
		"a stray word":  {[]string{id, "extra"}, "", "unexpected argument"},
		"a bad id":      {[]string{"nope"}, "", "not a livestream id"},
		"a missing one": {[]string{id}, "missing", "there is no livestream"},
		"a live one":    {[]string{id}, "live", "already being streamed to"},
	} {
		status = c.status
		_, errOut, code := runCommand(t, "", func() { runLivestream(c.args) })
		if code != 1 || !strings.Contains(errOut, c.want) {
			t.Errorf("%s: code %d, stderr %q (want %q)", name, code, errOut, c.want)
		}
	}
}

func TestStartingAndEndingALivestream(t *testing.T) {
	const id = "0123abcd-0123-4abc-8abc-0123456789ab"
	startStatus, startBody := 200, `{"livestream":{"id":"x","status":"live","url":"https://s/live/x"},"next_seq":0,"bytes_left":2048,"grace_seconds":30,"max_batch_bytes":65536}`
	endBody := `{"livestream":{"id":"x","status":"ended"},"recording_id":"rec1","recording_url":"https://s/watch/rec1"}`
	withServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/start"):
			jsonReply(w, startStatus, startBody)
		case strings.HasSuffix(r.URL.Path, "/end"):
			jsonReply(w, 200, endBody)
		case strings.HasSuffix(r.URL.Path, "/frames"):
			jsonReply(w, 200, `{"next_seq":1}`)
		}
	})
	session, _ := cloud.LoadSession()
	run := func() *liveRun {
		return &liveRun{client: cloud.FromSession(session), id: id, info: &cloud.Livestream{URL: "https://s/live/x"}}
	}

	l := run()
	if err := l.start(); err != nil || l.sender == nil || l.left == nil || *l.left != 2048 {
		t.Fatalf("start: %v, %+v", err, l)
	}
	out, _, _ := runCommand(t, "", func() { l.finish("/tmp/rec/terminal.jsonl") })
	if !strings.Contains(out, "Streaming ended. The stream is now a recording: https://s/watch/rec1") {
		t.Errorf("finish: %q", out)
	}

	startStatus, startBody = 409, `{"error":{"code":"already_live","message":"x"}}`
	if err := run().start(); err == nil || !strings.Contains(err.Error(), "already being streamed to") {
		t.Errorf("already live: %v", err)
	}
	startStatus, startBody = 403, `{"error":{"code":"forbidden","message":"x"}}`
	if err := run().start(); err == nil || !strings.Contains(err.Error(), "only the account that made") {
		t.Errorf("not the owner: %v", err)
	}
	startStatus, startBody = 500, `{"error":{"code":"boom","message":"x"}}`
	if err := run().start(); err == nil {
		t.Error("a server error must be an error")
	}

	// nothing to end or release when it never started
	l = run()
	l.release()
	if out, _, _ := runCommand(t, "", func() { l.finish("x") }); out != "" {
		t.Errorf("finish with no sender printed %q", out)
	}
	if deref(nil) != "" || deref(strp("a")) != "a" || startedAt(&cloud.Livestream{}) != "a moment ago" {
		t.Error("deref / startedAt")
	}
}

// runMain is main() as the program is started with args.
func runMain(t *testing.T, stdin string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	old := os.Args
	defer func() { os.Args = old }()
	os.Args = append([]string{"trecs"}, args...)
	return runCommand(t, stdin, main)
}

func TestRecordACommandsFileAndUploadItToAServer(t *testing.T) {
	bash := testBash(t)
	var uploaded, hasTerminal bool
	var name, visibility string
	withServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.URL.Path == "/v1/recordings" {
			_ = r.ParseMultipartForm(32 << 20)
			uploaded, name, visibility = true, r.FormValue("name"), r.FormValue("visibility")
			_, hasTerminal = r.MultipartForm.File["terminal"]
			jsonReply(w, 201, `{"id":"11111111-2222-3333-4444-555555555555","name":"ci","visibility":"unlisted","duration_ms":3000}`)
			return
		}
		http.NotFound(w, r)
	})
	dir := t.TempDir()
	cmds := filepath.Join(dir, "cmds.txt")
	_ = os.WriteFile(cmds, []byte("echo hello from the script\nprintf 'a\\nb\\n'\n"), 0o644)
	out, errOut, code := runMain(t, "", "record", "-output", dir, "-name", "ci", "-commands-file", cmds, "-cmd", bash,
		"-upload", "-visibility", "unlisted")
	if code != 0 {
		t.Fatalf("code %d\nstdout %q\nstderr %q", code, out, errOut)
	}
	if !uploaded || name != "ci" || visibility != "unlisted" || !hasTerminal {
		t.Errorf("uploaded %v, name %q, visibility %q, terminal %v", uploaded, name, visibility, hasTerminal)
	}
	for _, want := range []string{"Recording completed successfully", "Uploaded \"ci\"", "Watch: "} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout lacks %q: %q", want, out)
		}
	}
	rec, err := os.ReadFile(filepath.Join(dir, "ci", "terminal.jsonl"))
	if err != nil || !strings.Contains(string(rec), "hello from the script") || !strings.Contains(string(rec), "133;C") {
		t.Errorf("the recording: %v, %.200q", err, rec)
	}
}

func TestRecordAScriptedSessionWithNoUpload(t *testing.T) {
	bash := testBash(t)
	dir := t.TempDir()
	cmds := filepath.Join(dir, "cmds.txt")
	_ = os.WriteFile(cmds, []byte("true\nfalse\necho done\n"), 0o644)
	out, errOut, code := runMain(t, "", "record", "-output", dir, "-name", "plain", "-commands-file", cmds, "-cmd", bash)
	if code != 0 {
		t.Fatalf("code %d\nstdout %q\nstderr %q", code, out, errOut)
	}
	if !strings.Contains(out, "Recording completed successfully") || strings.Contains(out, "Uploaded") {
		t.Errorf("stdout %q", out)
	}
	for _, f := range []string{"terminal.jsonl"} {
		if _, err := os.Stat(filepath.Join(dir, "plain", f)); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
}

func TestRecordRefusesWhatCannotWork(t *testing.T) {
	dir := t.TempDir()
	for name, c := range map[string][]string{
		"no command":          {},
		"an unknown command":  {"frobnicate"},
		"a name with a slash": {"record", "-name", "a/b", "-output", dir},
		"a missing script":    {"record", "-output", dir, "-commands-file", filepath.Join(dir, "nope.txt")},
		"a bad flag value":    {"record", "-visibility", "everyone", "-upload", "-output", dir},
	} {
		_, errOut, code := runMain(t, "", c...)
		if code != 1 {
			t.Errorf("%s: code %d, stderr %q", name, code, errOut)
		}
	}
}

func TestVersionHelpAndScan(t *testing.T) {
	if out, _, code := runMain(t, "", "version"); code != 0 || !strings.Contains(strings.ToLower(out), "trecs") {
		t.Errorf("version: code %d, %q", code, out)
	}
	if out, _, code := runMain(t, "", "help"); code != 0 || !strings.Contains(out, "record") {
		t.Errorf("help: code %d, %q", code, out)
	}
	dir := sessionDir(t)
	if out, _, code := runMain(t, "", "scan", dir); code != 0 || !strings.Contains(out, "No secrets found") {
		t.Errorf("scan: code %d, %q", code, out)
	}
	secret := t.TempDir()
	line, _ := json.Marshal(map[string]any{"timestamp": 1, "data": "\x1b]133;A\a$ \x1b]133;B\aGITHUB_TOKEN=" + testToken + "\r\n\x1b]133;C\a\x1b]133;D;0\a", "width": 0, "height": 0})
	_ = os.WriteFile(filepath.Join(secret, "terminal.jsonl"), []byte(`{"type":"meta","width":80,"height":24}`+"\n"+string(line)+"\n"), 0o644)
	if out, _, code := runMain(t, "", "scan", secret); code != 1 || !strings.Contains(out, "ghp_") {
		t.Errorf("scan with a secret: code %d, %q", code, out)
	}
	if _, _, code := runMain(t, "", "scan"); code != 1 {
		t.Errorf("scan with no path: code %d", code)
	}
}

func TestStreamAScriptedSessionToAServer(t *testing.T) {
	bash := testBash(t)
	const id = "0123abcd-0123-4abc-8abc-0123456789ab"
	var started, ended bool
	var lines int
	srvURL := withServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/v1/livestreams/"+id:
			jsonReply(w, 200, `{"id":"`+id+`","name":"talk","status":"waiting","url":"https://s/live/x"}`)
		case r.URL.Path == "/v1/me/quota":
			jsonReply(w, 200, `{"storage_limit":0,"storage_used":0}`)
		case strings.HasSuffix(r.URL.Path, "/start"):
			started = true
			jsonReply(w, 200, `{"livestream":{"id":"`+id+`","status":"live","url":"https://s/live/x"},"next_seq":0,"max_batch_bytes":65536,"grace_seconds":30}`)
		case strings.HasSuffix(r.URL.Path, "/frames"):
			b, _ := io.ReadAll(r.Body)
			n := strings.Count(string(b), "\n")
			lines += n
			jsonReply(w, 200, `{"next_seq":`+strconv.Itoa(lines)+`}`)
		case strings.HasSuffix(r.URL.Path, "/end"):
			ended = true
			jsonReply(w, 200, `{"livestream":{"id":"`+id+`","status":"ended"},"recording_id":"rec1","recording_url":"https://s/watch/rec1"}`)
		default:
			http.NotFound(w, r)
		}
	})
	_ = srvURL
	dir := t.TempDir()
	cmds := filepath.Join(dir, "cmds.txt")
	_ = os.WriteFile(cmds, []byte("echo streaming now\n"), 0o644)
	out, errOut, code := runMain(t, "", "livestream", id, "-output", dir, "-name", "live", "-commands-file", cmds, "-cmd", bash)
	if code != 0 {
		t.Fatalf("code %d\nstdout %q\nstderr %q", code, out, errOut)
	}
	if !started || !ended || lines == 0 {
		t.Errorf("started %v, ended %v, lines sent %d", started, ended, lines)
	}
	if !strings.Contains(out, "Streaming ended. The stream is now a recording: https://s/watch/rec1") {
		t.Errorf("stdout %q", out)
	}
}
