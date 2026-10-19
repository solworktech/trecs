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

func TestUploadSendsTheImagesUnderTheirOwnNames(t *testing.T) {
	dir := t.TempDir()
	termPath := filepath.Join(dir, "terminal.jsonl")
	_ = os.WriteFile(termPath, []byte(`{"timestamp":1,"data":"x"}`+"\n"), 0o644)
	_ = os.MkdirAll(filepath.Join(dir, "images"), 0o755)
	a := filepath.Join(dir, "images", "img-0123456789ab.png")
	b := filepath.Join(dir, "images", "img-ba9876543210.jpg")
	_ = os.WriteFile(a, []byte("PNGDATA"), 0o644)
	_ = os.WriteFile(b, []byte("JPGDATA"), 0o644)

	got := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		for _, fh := range r.MultipartForm.File["image"] {
			f, _ := fh.Open()
			data, _ := io.ReadAll(f)
			got[fh.Filename] = string(data)
		}
		w.WriteHeader(201)
		_, _ = io.WriteString(w, `{"id":"abc","name":"n","visibility":"private","duration_ms":1}`)
	}))
	defer srv.Close()

	c := FromSession(&Session{Server: srv.URL, Cookies: []Cookie{{Name: "trecs_session", Value: "t"}}})
	if _, err := c.Upload(context.Background(), Upload{TerminalPath: termPath, ImagePaths: []string{a, b}}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got["img-0123456789ab.png"] != "PNGDATA" || got["img-ba9876543210.jpg"] != "JPGDATA" {
		t.Errorf("the server got %v", got)
	}
	// an image that is not there is an error before anything is sent
	if _, err := c.Upload(context.Background(), Upload{TerminalPath: termPath, ImagePaths: []string{filepath.Join(dir, "images", "img-000000000000.png")}}); err == nil {
		t.Error("an image that is not there was sent")
	}
}
