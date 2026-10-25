package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"trecs/cloud"

	libtrecs "trecs/lib"
)

// fakeServer is the server end of a stream: it keeps the lines it is sent, as the real one does, and can be told to misbehave.
type fakeServer struct {
	mu       sync.Mutex
	lines    []string
	requests int
	ended    int
	fail     func(n int) (status int, body string) // for the n-th request to /frames (from 1); 0 = serve it
	left     *int64
	srv      *httptest.Server
}

func newFakeServer(t *testing.T) *fakeServer {
	f := &fakeServer{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/livestreams/{id}/frames", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.requests++
		if f.fail != nil {
			if status, body := f.fail(f.requests); status != 0 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				_, _ = io.WriteString(w, body)
				return
			}
		}
		seq, _ := strconv.Atoi(r.URL.Query().Get("seq"))
		b, _ := io.ReadAll(r.Body)
		have := len(f.lines)
		if seq > have {
			w.WriteHeader(http.StatusConflict)
			_, _ = fmt.Fprintf(w, `{"error":{"code":"gap","message":"gap"},"next_seq":%d}`, have)
			return
		}
		var ls []string
		if len(b) > 0 {
			ls = strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
		}
		for i, l := range ls {
			if seq+i >= have {
				f.lines = append(f.lines, l)
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"next_seq": len(f.lines), "bytes_left": f.left})
	})
	mux.HandleFunc("POST /v1/livestreams/{id}/end", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.ended++
		f.mu.Unlock()
		_, _ = io.WriteString(w, `{"livestream":{"id":"x","status":"ended"},"recording_id":"rec-1","recording_url":"http://site/watch/rec-1"}`)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeServer) endedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ended
}

func (f *fakeServer) got() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.lines...)
}

// syncBuf is a buffer that the sender's goroutine writes and a test reads.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func (s *syncBuf) Len() int { return len(s.String()) }

func quick(t *testing.T) {
	t.Helper()
	si, qf, gm, rb, rm := sendInterval, quietFlush, graceMargin, retryBase, retryMax
	sendInterval, quietFlush, graceMargin, retryBase, retryMax = 10*time.Millisecond, 30*time.Millisecond, 0, 10*time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() { sendInterval, quietFlush, graceMargin, retryBase, retryMax = si, qf, gm, rb, rm })
}

func newTestSender(f *fakeServer, grace int, out io.Writer) *liveSender {
	c := &cloud.Client{Server: f.srv.URL, Cookies: []cloud.Cookie{{Name: "s", Value: "v"}}}
	return newLiveSender(c, "3a1f0b5e-1111-4222-8333-444455556666", &cloud.LivestreamStart{GraceSeconds: grace, MaxBatchBytes: 1 << 20}, out)
}

func frame(ts int64, data string) libtrecs.TerminalFrame {
	return libtrecs.TerminalFrame{Timestamp: ts, Data: data}
}

func close5s(s *liveSender) (*cloud.LivestreamEnd, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.Close(ctx)
}

func TestTheSenderSendsTheRecordingMaskedAndInOrderAndEndsTheStream(t *testing.T) {
	quick(t)
	f := newFakeServer(t)
	var out syncBuf
	s := newTestSender(f, 180, &out)
	s.Add(libtrecs.NewRecordingMetaLine(80, 24))
	s.Add(frame(5, "$ echo "+testToken+"\r\n"))
	s.Add(frame(900, "done\r\n$ "))
	end, err := close5s(s)
	if err != nil || end.RecordingID == nil || *end.RecordingID != "rec-1" {
		t.Fatalf("%+v %v", end, err)
	}
	lines := f.got()
	if len(lines) != 3 || !strings.Contains(lines[0], `"type":"meta"`) {
		t.Fatalf("%q", lines)
	}
	if strings.Contains(strings.Join(lines, ""), testToken[4:30]) {
		t.Errorf("the secret was streamed: %q", lines)
	}
	var fr libtrecs.TerminalFrame
	if err := json.Unmarshal([]byte(lines[2]), &fr); err != nil || fr.Timestamp != 900 || fr.Data != "done\r\n$ " {
		t.Errorf("the last word is flushed when the recording ends: %q %v", lines[2], err)
	}
	if f.endedCount() != 1 || out.Len() != 0 {
		t.Errorf("ended %d times, said %q", f.endedCount(), out.String())
	}
}

func TestAWordLeftAloneForAMomentIsSentWithoutWaitingForMore(t *testing.T) {
	quick(t)
	f := newFakeServer(t)
	s := newTestSender(f, 180, io.Discard)
	s.Add(libtrecs.NewRecordingMetaLine(80, 24))
	s.Add(frame(5, "$ ls"))
	deadline := time.Now().Add(3 * time.Second)
	for len(f.got()) < 3 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := f.got(); len(got) != 3 || !strings.Contains(got[2], `"ls"`) {
		t.Errorf("%q", got)
	}
	_, _ = close5s(s)
}

func TestWhatCouldNotBeSentIsSentAgainOnceAndInOrder(t *testing.T) {
	quick(t)
	f := newFakeServer(t)
	f.fail = func(n int) (int, string) {
		if n <= 3 {
			return 503, `{"error":{"code":"unavailable","message":"down"}}`
		}
		return 0, ""
	}
	var out syncBuf
	s := newTestSender(f, 180, &out)
	s.Add(libtrecs.NewRecordingMetaLine(80, 24))
	for i := range 5 {
		s.Add(frame(int64(i), fmt.Sprintf("line %d\r\n", i)))
	}
	if _, err := close5s(s); err != nil {
		t.Fatal(err)
	}
	lines := f.got()
	if len(lines) != 6 {
		t.Fatalf("%d lines: %q", len(lines), lines)
	}
	for i := range 5 {
		if !strings.Contains(lines[i+1], fmt.Sprintf("line %d", i)) {
			t.Errorf("line %d: %q", i+1, lines[i+1])
		}
	}
	if !strings.Contains(out.String(), "cannot reach the server") || !strings.Contains(out.String(), "is back") {
		t.Errorf("the person is told when it is down and when it is back: %q", out.String())
	}
	if s.Stopped() != "" {
		t.Errorf("stopped: %s", s.Stopped())
	}
}

func TestAStreamThatGoesOverTheQuotaStopsAndSaysSo(t *testing.T) {
	quick(t)
	f := newFakeServer(t)
	f.fail = func(n int) (int, string) {
		if n >= 2 {
			return 403, `{"error":{"code":"quota_exceeded","message":"This would take 2 MB of your 10 MB."}}`
		}
		return 0, ""
	}
	var out syncBuf
	s := newTestSender(f, 180, &out)
	s.Add(libtrecs.NewRecordingMetaLine(80, 24))
	s.Add(frame(1, "first\r\n"))
	deadline := time.Now().Add(3 * time.Second)
	for len(f.got()) < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	s.Add(frame(2, "second\r\n"))
	for s.Stopped() == "" && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if s.Stopped() != stopQuota {
		t.Fatalf("stopped: %q", s.Stopped())
	}
	want := "Your session exceeded your storage quota. Streaming stopped, but you can continue recording, your recording will be saved locally and you upload it later."
	if !strings.Contains(out.String(), want) {
		t.Errorf("said %q", out.String())
	}
	s.Add(frame(3, "after\r\n")) // the recording goes on; nothing more is sent
	end, err := close5s(s)
	if err != nil || end == nil || f.endedCount() != 1 {
		t.Errorf("what fitted is kept: the stream must still be ended: %+v %v ended=%d", end, err, f.endedCount())
	}
	if strings.Count(out.String(), "Streaming stopped") != 1 {
		t.Errorf("said more than once: %q", out.String())
	}
}

func TestAStreamIsGivenUpWhenTheServerIsGoneForLongerThanItWaits(t *testing.T) {
	quick(t)
	f := newFakeServer(t)
	f.fail = func(int) (int, string) { return 502, "" }
	var out syncBuf
	s := newTestSender(f, 1, &out) // the server waits 1 s
	s.Add(libtrecs.NewRecordingMetaLine(80, 24))
	deadline := time.Now().Add(5 * time.Second)
	for s.Stopped() == "" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if s.Stopped() != stopLost {
		t.Fatalf("stopped: %q", s.Stopped())
	}
	if !strings.Contains(out.String(), "Streaming stopped, but you can continue recording") {
		t.Errorf("%q", out.String())
	}
}

func TestAStreamTheServerHasEndedIsNotSentToAgain(t *testing.T) {
	quick(t)
	f := newFakeServer(t)
	f.fail = func(int) (int, string) {
		return 409, `{"error":{"code":"not_live","message":"This livestream is not live."}}`
	}
	var out syncBuf
	s := newTestSender(f, 180, &out)
	s.Add(libtrecs.NewRecordingMetaLine(80, 24))
	deadline := time.Now().Add(3 * time.Second)
	for s.Stopped() == "" && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if s.Stopped() != stopOther || !strings.Contains(out.String(), "the server has ended this stream") {
		t.Errorf("%q %q", s.Stopped(), out.String())
	}
	f.mu.Lock()
	n := f.requests
	f.mu.Unlock()
	time.Sleep(100 * time.Millisecond)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.requests != n {
		t.Error("kept sending to a stream that is over")
	}
}

func TestAQuietRecordingKeepsTheStreamAlive(t *testing.T) {
	quick(t)
	hb := heartbeatMin
	heartbeatMin = 20 * time.Millisecond
	defer func() { heartbeatMin = hb }()
	f := newFakeServer(t)
	s := newTestSender(f, 1, io.Discard)
	defer func() { _, _ = close5s(s) }()
	time.Sleep(900 * time.Millisecond) // nothing is recorded; a sixth of the 1 s the server waits is a sign of life every 166 ms
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.requests < 3 {
		t.Errorf("only %d signs of life in 900 ms: the server would end the stream", f.requests)
	}
}
