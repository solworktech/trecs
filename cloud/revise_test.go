package cloud

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestReviseSendsOnlyWhatIsGivenToTheRecording(t *testing.T) {
	dir := t.TempDir()
	audioPath := filepath.Join(dir, "narration.mp3")
	_ = os.WriteFile(audioPath, []byte("ID3-new"), 0o644)

	var method, path string
	var fields, files []string
	var audio, cad string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			writeAPIError(w, 400, "bad_form", err.Error())
			return
		}
		fields, files = nil, nil
		for k := range r.MultipartForm.Value {
			fields = append(fields, k)
		}
		for k := range r.MultipartForm.File {
			files = append(files, k)
		}
		cad = r.FormValue("commands_as_description")
		if f, _, err := r.FormFile("audio"); err == nil {
			b, _ := io.ReadAll(f)
			audio = string(b)
		}
		_, _ = io.WriteString(w, `{"id":"11111111-2222-3333-4444-555555555555","name":"Renamed","visibility":"public","duration_ms":9000}`)
	}))
	defer srv.Close()

	c := FromSession(&Session{Server: srv.URL, Cookies: []Cookie{{Name: "trecs_session", Value: "tok"}}})
	id := "11111111-2222-3333-4444-555555555555"
	rec, err := c.Revise(context.Background(), id, Upload{Name: "Renamed", AudioPath: audioPath})
	if err != nil {
		t.Fatal(err)
	}
	if method != "PATCH" || path != "/v1/recordings/"+id {
		t.Errorf("sent %s %s", method, path)
	}
	if len(fields) != 1 || fields[0] != "name" || len(files) != 1 || files[0] != "audio" || audio != "ID3-new" {
		t.Errorf("fields %v, files %v, audio %q: only the name and the audio were given", fields, files, audio)
	}
	if rec.Name != "Renamed" || rec.DurationMs != 9000 {
		t.Errorf("rec = %+v", rec)
	}

	termPath := filepath.Join(dir, "terminal.jsonl")
	_ = os.WriteFile(termPath, []byte(`{"timestamp":1,"data":"x"}`+"\n"), 0o644)
	if _, err := c.Revise(context.Background(), id, Upload{TerminalPath: termPath, CommandsAsDescription: true}); err != nil {
		t.Fatal(err)
	}
	if cad != "true" || len(files) != 1 || files[0] != "terminal" {
		t.Errorf("commands_as_description=%q files %v", cad, files)
	}

	if _, err := c.Revise(context.Background(), " ", Upload{Name: "x"}); err == nil {
		t.Error("a recording without an id")
	}
	if _, err := FromSession(&Session{Server: srv.URL}).Revise(context.Background(), id, Upload{Name: "x"}); err != ErrNotLoggedIn {
		t.Errorf("not logged in: %v", err)
	}
}

func TestRecordingIDIsTakenFromAnIDOrALink(t *testing.T) {
	const id = "0a1b2c3d-4e5f-6a7b-8c9d-0e1f2a3b4c5d"
	for _, in := range []string{id, " " + id + "\n", "https://trecs.example/watch/" + id, "http://localhost:8080/watch/" + id + "/?t=12", "HTTPS://x.io/watch/" + id} {
		if got, err := RecordingID(in); err != nil || got != id {
			t.Errorf("%q: %q, %v", in, got, err)
		}
	}
	for _, in := range []string{"", "abc", "https://trecs.example/watch/", "https://trecs.example/channels/" + id[:20], id + "x"} {
		if got, err := RecordingID(in); err == nil {
			t.Errorf("%q: gave %q", in, got)
		}
	}
}
