package cloud

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Livestream is a stream as the server describes it.
type Livestream struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Visibility  string `json:"visibility"`
	// Status is waiting (made, never streamed to), live, or ended.
	Status string `json:"status"`
	// URL is the address of the stream's page: what to share, and what the recorder says it is streaming to.
	URL             string     `json:"url"`
	StartedAt       *time.Time `json:"started_at"`
	EndedAt         *time.Time `json:"ended_at"`
	LastRecordingID *string    `json:"last_recording_id"`
}

// LivestreamCreate is what a new stream is given; all of it is optional.
type LivestreamCreate struct {
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	Visibility  string `json:"visibility,omitempty"`
}

// LivestreamStart is what the server tells a recorder that has taken a stream.
type LivestreamStart struct {
	Livestream Livestream `json:"livestream"`
	NextSeq    int        `json:"next_seq"`
	// BytesLeft is what the account may still store; nil = no limit.
	BytesLeft     *int64 `json:"bytes_left"`
	GraceSeconds  int    `json:"grace_seconds"`
	MaxBatchBytes int64  `json:"max_batch_bytes"`
}

// LivestreamEnd is what a stream became. RecordingID is "" when nothing had been streamed.
type LivestreamEnd struct {
	Livestream   Livestream `json:"livestream"`
	RecordingID  *string    `json:"recording_id"`
	RecordingURL *string    `json:"recording_url"`
}

// FramesAnswer is the server's answer to a batch: the line it wants next, and what the account may still store.
type FramesAnswer struct {
	NextSeq   int    `json:"next_seq"`
	BytesLeft *int64 `json:"bytes_left"`
}

// ErrGap is a batch that begins after the line the server wants: it is missing lines before. NextSeq is the one it wants.
type ErrGap struct{ NextSeq int }

func (e *ErrGap) Error() string {
	return fmt.Sprintf("the server is missing lines before this batch (it wants line %d)", e.NextSeq)
}

// IsCode says that the server refused with this error code (not_live, already_live, livestream_not_found ...).
func (e *APIError) IsCode(code string) bool { return e.Code == code }

var livestreamIDPattern = publicIDPattern

// LivestreamID is the id of a stream from what a person has at hand: the id itself, or the address of its page
// (https://server/live/<id>).
func LivestreamID(arg string) (string, error) {
	arg = strings.TrimSpace(arg)
	id := arg
	if i := strings.LastIndex(strings.TrimRight(arg, "/"), "/"); i >= 0 {
		id = strings.TrimRight(arg, "/")[i+1:]
	}
	if !livestreamIDPattern.MatchString(id) {
		return "", fmt.Errorf("%q is not a livestream id (the xxxxxxxx-xxxx-... that `livestream create` printed) or the address of its page", arg)
	}
	return strings.ToLower(id), nil
}

// CreateLivestream makes a stream, which is waiting until a recorder streams to it.
func (c *Client) CreateLivestream(ctx context.Context, in LivestreamCreate) (*Livestream, error) {
	var out Livestream
	if err := c.jsonCall(ctx, http.MethodPost, "/v1/livestreams", in, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetLivestream is a stream's details.
func (c *Client) GetLivestream(ctx context.Context, id string) (*Livestream, error) {
	var out Livestream
	if err := c.jsonCall(ctx, http.MethodGet, "/v1/livestreams/"+url.PathEscape(id), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// StartLivestream takes a stream: this recorder is now the one streaming to it. An *APIError with code already_live says another is.
func (c *Client) StartLivestream(ctx context.Context, id string) (*LivestreamStart, error) {
	var out LivestreamStart
	if err := c.jsonCall(ctx, http.MethodPost, "/v1/livestreams/"+url.PathEscape(id)+"/start", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// EndLivestream ends the stream; what was streamed becomes a recording.
func (c *Client) EndLivestream(ctx context.Context, id string) (*LivestreamEnd, error) {
	var out LivestreamEnd
	if err := c.jsonCall(ctx, http.MethodPost, "/v1/livestreams/"+url.PathEscape(id)+"/end", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SendFrames sends lines of a recording, the first of which is line seq of it. An empty body is a sign of life. A batch that
// begins after what the server has is an *ErrGap; one the server already has is taken as the answer says (next_seq).
func (c *Client) SendFrames(ctx context.Context, id string, seq int, lines []byte) (*FramesAnswer, error) {
	if len(c.Cookies) == 0 {
		return nil, ErrNotLoggedIn
	}
	req, err := c.newRequest(ctx, http.MethodPost, "/v1/livestreams/"+url.PathEscape(id)+"/frames?seq="+strconv.Itoa(seq), bytes.NewReader(lines), "application/x-ndjson")
	if err != nil {
		return nil, err
	}
	resp, err := c.http().Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 == 2 {
		var a FramesAnswer
		if err := json.Unmarshal(body, &a); err != nil {
			return nil, fmt.Errorf("the server's answer was not understood: %w", err)
		}
		return &a, nil
	}
	var env struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		NextSeq int `json:"next_seq"`
	}
	_ = json.Unmarshal(body, &env)
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return nil, ErrNotLoggedIn
	case resp.StatusCode == http.StatusConflict && env.Error.Code == "gap":
		return nil, &ErrGap{NextSeq: env.NextSeq}
	}
	return nil, &APIError{Status: resp.StatusCode, Code: env.Error.Code, Message: env.Error.Message}
}

// jsonCall is a call with a JSON body (or none) and a JSON answer, made as the saved session.
func (c *Client) jsonCall(ctx context.Context, method, path string, in, out any) error {
	if len(c.Cookies) == 0 {
		return ErrNotLoggedIn
	}
	var body io.Reader
	ct := ""
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body, ct = bytes.NewReader(b), "application/json"
	}
	req, err := c.newRequest(ctx, method, path, body, ct)
	if err != nil {
		return err
	}
	resp, err := c.do(req, true)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("the server's answer was not understood: %w", err)
	}
	return nil
}

// IsTransient says that a call failed for a reason that may be gone in a moment: the network, or the server being down or
// overloaded. A refusal by the server (an *APIError below 500, ErrNotLoggedIn, an *ErrGap) is not.
func IsTransient(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	var ae *APIError
	if errors.As(err, &ae) {
		return ae.Status >= 500 || ae.Status == http.StatusTooManyRequests
	}
	var gap *ErrGap
	if errors.Is(err, ErrNotLoggedIn) || errors.As(err, &gap) {
		return false
	}
	return true
}

// Quota is what the account may store and has stored (0 = no limit).
type Quota struct {
	StorageLimit int64  `json:"storage_limit"`
	StorageUsed  int64  `json:"storage_used"`
	RequestURL   string `json:"request_url"`
}

// Left is how much more the account may store; nil = no limit.
func (q *Quota) Left() *int64 {
	if q.StorageLimit == 0 {
		return nil
	}
	left := max(q.StorageLimit-q.StorageUsed, 0)
	return &left
}

// GetQuota is the caller's storage limit and use.
func (c *Client) GetQuota(ctx context.Context) (*Quota, error) {
	var out Quota
	if err := c.jsonCall(ctx, http.MethodGet, "/v1/me/quota", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
