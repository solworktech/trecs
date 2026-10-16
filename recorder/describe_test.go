package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"trecs/cloud"
)

func TestDescribeFromAnnotationsPutsTheNavAnnotationsAsLinks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "terminal.jsonl")
	lines := []string{
		`{"type":"meta","width":100,"height":30,"env":"Ubuntu 24.04.1 LTS","shell":"bash"}`,
		`{"type":"annotation","timestamp":50504,"id":"b","text":"**Step 1 – Edit nginx.conf in vim, bottom-up.**\nChanges lower go first.","kind":"timed","durationSeconds":6,"showInNav":true}`,
		`{"type":"annotation","timestamp":9501,"id":"a","text":"**Step 0 – Back up the config.**","kind":"timed","durationSeconds":6,"showInNav":true}`,
		`{"type":"annotation","timestamp":12000,"id":"c","text":"not in the nav","kind":"timed","durationSeconds":4}`,
		`{"timestamp":5,"data":"x"}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var method, urlPath, body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		method, urlPath, body = r.Method, r.URL.Path, string(b)
		_, _ = io.WriteString(w, `{"id":"rid","name":"N","description":"D","visibility":"unlisted"}`)
	}))
	defer srv.Close()
	client := cloud.FromSession(&cloud.Session{Server: srv.URL, Cookies: []cloud.Cookie{{Name: "trecs_session", Value: "t"}}})

	rec := &cloud.Recording{ID: "rid", Name: "My nginx demo", Visibility: "unlisted"}
	if err := describeFromAnnotations(context.Background(), client, rec, path, "http://trecs.solwork.io/watch/rid"); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Name, Description, Visibility string
		PreviewAtMs                   *int64 `json:"preview_at_ms"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	want := "[**[00:09]**](http://trecs.solwork.io/watch/rid?t=9) **Step 0 – Back up the config.**\n" +
		"[**[00:50]**](http://trecs.solwork.io/watch/rid?t=50) **Step 1 – Edit nginx.conf in vim, bottom-up.**"
	if method != "PUT" || urlPath != "/v1/recordings/rid" || got.Description != want || got.Name != "My nginx demo" || got.Visibility != "unlisted" {
		t.Errorf("sent %s %s %s", method, urlPath, body)
	}

	// nothing marked for the nav menu: nothing is sent, and the commands list stays
	none := filepath.Join(t.TempDir(), "n.jsonl")
	_ = os.WriteFile(none, []byte(`{"type":"annotation","timestamp":1,"id":"z","text":"x","kind":"timed"}`+"\n"+`{"timestamp":5,"data":"x"}`+"\n"), 0o644)
	method = ""
	if err := describeFromAnnotations(context.Background(), client, rec, none, "http://h/watch/rid"); err != nil || method != "" {
		t.Errorf("no nav annotations: err %v, sent %q", err, method)
	}
}
