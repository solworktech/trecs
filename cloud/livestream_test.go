package cloud

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func loggedIn(srv *httptest.Server) *Client {
	return FromSession(&Session{Server: srv.URL, Cookies: []Cookie{{Name: "trecs_session", Value: "tok"}}})
}

func TestLivestreamIDTakesTheIDOrTheAddressOfItsPage(t *testing.T) {
	const id = "0123abcd-0123-4abc-8abc-0123456789ab"
	for _, in := range []string{id, "  " + id + " ", "https://trecs.run/live/" + id, "https://trecs.run/live/" + id + "/", strings.ToUpper(id)} {
		got, err := LivestreamID(in)
		if err != nil || got != id {
			t.Errorf("LivestreamID(%q) = %q, %v; want %q", in, got, err, id)
		}
	}
	for _, in := range []string{"", "nope", "https://trecs.run/live/", "https://trecs.run/live/not-an-id"} {
		if got, err := LivestreamID(in); err == nil {
			t.Errorf("LivestreamID(%q) = %q, want an error", in, got)
		}
	}
}

func TestLivestreamCallsGoToTheRightPlacesAndAnswersAreRead(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.RequestURI())
		if r.Method == "POST" && r.URL.Path == "/v1/livestreams" {
			var in LivestreamCreate
			_ = json.NewDecoder(r.Body).Decode(&in)
			if in.Name != "demo" || in.Visibility != "unlisted" {
				t.Errorf("create body = %+v", in)
			}
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/start"):
			_, _ = io.WriteString(w, `{"livestream":{"id":"x","status":"live"},"next_seq":7,"bytes_left":1000,"grace_seconds":30,"max_batch_bytes":4096}`)
		case strings.HasSuffix(r.URL.Path, "/end"):
			_, _ = io.WriteString(w, `{"livestream":{"id":"x","status":"ended"},"recording_id":"rec1","recording_url":"https://s/r/rec1"}`)
		case r.URL.Path == "/v1/me/quota":
			_, _ = io.WriteString(w, `{"storage_limit":1000,"storage_used":400,"request_url":"https://s/more"}`)
		default:
			_, _ = io.WriteString(w, `{"id":"x","name":"demo","status":"waiting","url":"https://s/live/x"}`)
		}
	}))
	defer srv.Close()
	c, ctx := loggedIn(srv), context.Background()

	if ls, err := c.CreateLivestream(ctx, LivestreamCreate{Name: "demo", Visibility: "unlisted"}); err != nil || ls.URL != "https://s/live/x" {
		t.Errorf("create: %+v, %v", ls, err)
	}
	if ls, err := c.GetLivestream(ctx, "a b"); err != nil || ls.Name != "demo" {
		t.Errorf("get: %+v, %v", ls, err)
	}
	st, err := c.StartLivestream(ctx, "x")
	if err != nil || st.NextSeq != 7 || st.BytesLeft == nil || *st.BytesLeft != 1000 || st.MaxBatchBytes != 4096 || st.Livestream.Status != "live" {
		t.Errorf("start: %+v, %v", st, err)
	}
	end, err := c.EndLivestream(ctx, "x")
	if err != nil || end.RecordingID == nil || *end.RecordingID != "rec1" {
		t.Errorf("end: %+v, %v", end, err)
	}
	q, err := c.GetQuota(ctx)
	if err != nil || q.Left() == nil || *q.Left() != 600 || q.RequestURL != "https://s/more" {
		t.Errorf("quota: %+v, %v", q, err)
	}
	want := []string{"POST /v1/livestreams", "GET /v1/livestreams/a%20b", "POST /v1/livestreams/x/start", "POST /v1/livestreams/x/end", "GET /v1/me/quota"}
	if strings.Join(seen, "|") != strings.Join(want, "|") {
		t.Errorf("calls:\n got  %q\n want %q", seen, want)
	}
}

func TestQuotaLeft(t *testing.T) {
	if (&Quota{}).Left() != nil {
		t.Error("no limit must be nil")
	}
	if l := (&Quota{StorageLimit: 10, StorageUsed: 25}).Left(); l == nil || *l != 0 {
		t.Errorf("over the limit must be 0, got %v", l)
	}
}

func TestLivestreamCallsNeedALogin(t *testing.T) {
	c := FromSession(&Session{Server: "http://unused.invalid"})
	ctx := context.Background()
	if _, err := c.CreateLivestream(ctx, LivestreamCreate{}); !errors.Is(err, ErrNotLoggedIn) {
		t.Errorf("create: %v", err)
	}
	if _, err := c.SendFrames(ctx, "x", 0, nil); !errors.Is(err, ErrNotLoggedIn) {
		t.Errorf("send: %v", err)
	}
}

func TestLivestreamRefusalsKeepTheirCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeAPIError(w, 409, "already_live", "Another recorder is streaming to this.")
	}))
	defer srv.Close()
	_, err := loggedIn(srv).StartLivestream(context.Background(), "x")
	var ae *APIError
	if !errors.As(err, &ae) || !ae.IsCode("already_live") || ae.IsCode("not_live") || ae.Error() == "" {
		t.Fatalf("err = %#v", err)
	}
	if IsTransient(err) {
		t.Error("a refusal is not transient")
	}
}

func TestJSONCallAnswerThatIsNotJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "<html>") }))
	defer srv.Close()
	if _, err := loggedIn(srv).GetQuota(context.Background()); err == nil || !strings.Contains(err.Error(), "not understood") {
		t.Errorf("err = %v", err)
	}
}

func TestSendFramesAnswers(t *testing.T) {
	var gotSeq, gotType, gotBody string
	status, answer := 200, `{"next_seq":3,"bytes_left":50}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSeq, gotType = r.URL.Query().Get("seq"), r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, answer)
	}))
	defer srv.Close()
	c, ctx := loggedIn(srv), context.Background()

	a, err := c.SendFrames(ctx, "x", 2, []byte("{}\n"))
	if err != nil || a.NextSeq != 3 || a.BytesLeft == nil || *a.BytesLeft != 50 || gotSeq != "2" || gotType != "application/x-ndjson" || gotBody != "{}\n" {
		t.Errorf("ok: %+v, %v (seq %q, type %q, body %q)", a, err, gotSeq, gotType, gotBody)
	}

	status, answer = 200, "garbage"
	if _, err := c.SendFrames(ctx, "x", 0, nil); err == nil || !strings.Contains(err.Error(), "not understood") {
		t.Errorf("garbage: %v", err)
	}

	status, answer = 409, `{"error":{"code":"gap"},"next_seq":5}`
	var gap *ErrGap
	if _, err := c.SendFrames(ctx, "x", 9, nil); !errors.As(err, &gap) || gap.NextSeq != 5 || !strings.Contains(gap.Error(), "5") || IsTransient(err) {
		t.Errorf("gap: %v", err)
	}

	status, answer = 401, ``
	if _, err := c.SendFrames(ctx, "x", 0, nil); !errors.Is(err, ErrNotLoggedIn) {
		t.Errorf("401: %v", err)
	}

	status, answer = 503, `{"error":{"code":"busy","message":"later"}}`
	_, err = c.SendFrames(ctx, "x", 0, nil)
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != 503 || ae.Code != "busy" || !IsTransient(err) {
		t.Errorf("503: %#v", err)
	}
}

func TestIsTransient(t *testing.T) {
	for err, want := range map[error]bool{
		nil:                            false,
		context.Canceled:               false,
		ErrNotLoggedIn:                 false,
		&ErrGap{NextSeq: 1}:            false,
		&APIError{Status: 400}:         false,
		&APIError{Status: 429}:         true,
		&APIError{Status: 500}:         true,
		errors.New("connection reset"): true,
		context.DeadlineExceeded:       true,
	} {
		if got := IsTransient(err); got != want {
			t.Errorf("IsTransient(%v) = %v, want %v", err, got, want)
		}
	}
}
