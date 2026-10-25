package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"trecs/cloud"

	libtrecs "trecs/lib"
)

// The sender is what takes the records of a recording as they are written and gets them to the server, which is a different thing from
// writing them: the recording on this machine never waits for the network, and never loses a line because the network was down.
//
// Records are queued by the recorder (liveSender.Add, which only appends to a list), and one goroutine masks them, batches them and
// POSTs them, line numbered. The server says which line it wants next, so a batch that was sent and not answered is simply sent again,
// and lines it has are not taken twice. When the server cannot be reached the lines are kept (in memory) and tried again, for as long
// as the server keeps the stream waiting for this recorder - its grace, a few minutes. After that the stream is ended on its side
// with what it has, and here it is given up: the recording goes on, on this machine, and can be uploaded afterwards.

const (
	maxPendingSize = 64 << 20 // what is kept waiting for a server that cannot be reached
	sendTimeout    = 15 * time.Second
)

// These are variables only so that a test need not wait for them.
var (
	sendInterval = 250 * time.Millisecond // how often what has accumulated is sent
	quietFlush   = 300 * time.Millisecond // the last word is let go after this long without output
	graceMargin  = 10 * time.Second       // a stream is given up this long before the server would end it, so that the last tries are not wasted
	retryBase    = time.Second            // how long a failed server is left alone, doubled at each failure
	retryMax     = 8 * time.Second
	heartbeatMin = 5 * time.Second // a quiet recording sends a sign of life at least this often (a sixth of the server's grace, if that is more)
)

// Why a stream stopped before its recorder ended it.
const (
	stopQuota = "quota"
	stopLost  = "lost"
	stopOther = "other"
)

// liveSender streams a recording to a server.
type liveSender struct {
	client *cloud.Client
	id     string
	out    io.Writer // where it speaks: standard error. The terminal is raw, so lines end in \r\n

	grace    time.Duration
	maxBatch int

	mask *liveMasker

	mu       sync.Mutex
	queue    []any
	closing  bool
	stopped  string // why it stopped on its own ("" while it is going)
	wake     chan struct{}
	done     chan struct{}
	finalCtx context.Context

	// the lines, masked and encoded, that the server has not taken yet; ends[i] is where line first+i ends in pending
	pending []byte
	ends    []int
	first   int // the number of the first pending line

	bytesLeft *int64
	lastOK    time.Time
	lastTry   time.Time
	backoff   time.Duration // how long to leave the server alone after a failed try
	retrying  bool
	result    *cloud.LivestreamEnd
	resultErr error

	now func() time.Time // for tests
}

// newLiveSender begins sending to a stream the recorder has taken (see cloud.Client.StartLivestream).
func newLiveSender(client *cloud.Client, id string, st *cloud.LivestreamStart, out io.Writer) *liveSender {
	s := &liveSender{
		client: client, id: id, out: out,
		grace: time.Duration(st.GraceSeconds) * time.Second, maxBatch: int(st.MaxBatchBytes),
		mask: newLiveMasker(), first: st.NextSeq, bytesLeft: st.BytesLeft,
		wake: make(chan struct{}, 1), done: make(chan struct{}), now: time.Now,
	}
	if s.grace <= 0 {
		s.grace = 3 * time.Minute
	}
	if s.maxBatch <= 0 {
		s.maxBatch = 1 << 20
	}
	s.lastOK = s.now()
	go s.run()
	return s
}

// Add takes a record of the recording. It never waits for the network.
func (s *liveSender) Add(record any) {
	s.mu.Lock()
	if s.stopped == "" && !s.closing {
		s.queue = append(s.queue, record)
	}
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Stopped says why the stream stopped by itself: "" if it has not.
func (s *liveSender) Stopped() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stopped
}

// say speaks to the person, in the middle of a raw terminal.
func (s *liveSender) say(format string, args ...any) {
	_, _ = fmt.Fprintf(s.out, "\r\n"+format+"\r\n", args...)
}

// Close sends what is left, ends the stream on the server and returns what it became. It is the end of the recording.
func (s *liveSender) Close(ctx context.Context) (*cloud.LivestreamEnd, error) {
	s.mu.Lock()
	s.closing = true
	s.finalCtx = ctx
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
	<-s.done
	return s.result, s.resultErr
}

func (s *liveSender) run() {
	defer close(s.done)
	tick := time.NewTicker(sendInterval)
	defer tick.Stop()
	heartbeat := max(s.grace/6, heartbeatMin)
	lastRecord := s.now()
	for {
		select {
		case <-s.wake:
		case <-tick.C:
		}
		s.mu.Lock()
		queue, closing, ctx := s.queue, s.closing, s.finalCtx
		s.queue = nil
		stopped := s.stopped != ""
		s.mu.Unlock()

		if len(queue) > 0 {
			lastRecord = s.now()
		}
		if !stopped {
			for _, r := range queue {
				s.encode(r)
			}
			if closing || (s.mask.Holding() && s.now().Sub(lastRecord) >= quietFlush) {
				s.encodeFrames(s.mask.Flush())
			}
			if len(s.pending) > maxPendingSize {
				s.stop(stopLost, "")
			} else if (len(s.pending) > 0 || s.now().Sub(s.lastOK) >= heartbeat || closing) &&
				(closing || s.now().Sub(s.lastTry) >= s.backoff) {
				bg := context.Background()
				if closing && ctx != nil {
					bg = ctx
				}
				s.flush(bg, closing)
			}
		}
		if closing {
			s.finish(ctx)
			return
		}
	}
}

// encode masks one record and adds the lines it comes to to what is pending.
func (s *liveSender) encode(r any) {
	switch v := r.(type) {
	case libtrecs.TerminalFrame:
		s.encodeFrames(s.mask.Frame(v))
	default:
		s.addLine(r)
	}
}

func (s *liveSender) encodeFrames(fs []libtrecs.TerminalFrame) {
	for _, f := range fs {
		s.addLine(f)
	}
}

func (s *liveSender) addLine(v any) {
	var b bytes.Buffer
	if err := json.NewEncoder(&b).Encode(v); err != nil {
		return // a record that cannot be written is not one
	}
	s.pending = append(s.pending, b.Bytes()...)
	s.ends = append(s.ends, len(s.pending))
}

// flush sends what is pending, in batches the server accepts, until it is all taken or the server cannot be reached. final: this is the
// last try, and it is given until ctx is done rather than a few seconds; a stream that cannot be reached then is given up.
func (s *liveSender) flush(parent context.Context, final bool) {
	for s.Stopped() == "" {
		n := s.batchLines()
		s.lastTry = s.now()
		prev := s.first
		ctx, cancel := context.WithTimeout(parent, sendTimeout)
		var body []byte
		if n > 0 {
			body = s.pending[:s.ends[n-1]]
		}
		ans, err := s.client.SendFrames(ctx, s.id, s.first, body)
		cancel()
		switch {
		case err == nil:
			s.ack(ans)
			s.backoff = 0
			if s.retrying {
				s.retrying = false
				s.say("recorder: the connection to the server is back; streaming again")
			}
			if len(s.ends) == 0 || n == 0 || s.first == prev {
				return // all taken; or nothing was sent; or the server took none of it (the next tick tries again)
			}
			continue // more may be pending (a long batch was cut)
		case final && parent.Err() != nil:
			s.stop(stopLost, "")
			return
		}
		s.handleError(err)
		if !final || s.Stopped() != "" {
			return // the next tick tries again
		}
		// the recording is over and what is left must get there: it is tried again until the server answers or the time given runs out
		select {
		case <-time.After(s.backoff):
		case <-parent.Done():
		}
	}
}

// batchLines is how many of the pending lines go in one batch: as many as fit in what the server takes at once (at least one).
func (s *liveSender) batchLines() int {
	n := 0
	for i, end := range s.ends {
		if end > s.maxBatch && i > 0 {
			break
		}
		n = i + 1
		if end > s.maxBatch {
			break
		}
	}
	return n
}

// ack drops the lines the server has taken.
func (s *liveSender) ack(a *cloud.FramesAnswer) {
	s.lastOK = s.now()
	s.bytesLeft = a.BytesLeft
	s.dropTo(a.NextSeq)
}

// dropTo forgets the pending lines before line seq.
func (s *liveSender) dropTo(seq int) {
	k := seq - s.first
	if k <= 0 {
		return
	}
	k = min(k, len(s.ends))
	cut := s.ends[k-1]
	s.pending = append(s.pending[:0:0], s.pending[cut:]...)
	rest := make([]int, len(s.ends)-k)
	for i := range rest {
		rest[i] = s.ends[k+i] - cut
	}
	s.ends = rest
	s.first += k
}

// handleError decides what a refused or failed batch means.
func (s *liveSender) handleError(err error) {
	var ae *cloud.APIError
	var gap *cloud.ErrGap
	switch {
	case errors.As(err, &gap):
		if gap.NextSeq >= s.first && gap.NextSeq <= s.first+len(s.ends) {
			s.dropTo(gap.NextSeq) // it has more than was thought; the next try goes from there
			return
		}
		s.stop(stopOther, fmt.Sprintf("the server's copy of the stream (it wants line %d) and this one (from line %d) no longer match", gap.NextSeq, s.first))
	case errors.Is(err, cloud.ErrNotLoggedIn):
		s.stop(stopOther, "the login expired or was ended: log in again with `"+progName()+" login`")
	case errors.As(err, &ae) && ae.QuotaExceeded():
		s.stop(stopQuota, "")
	case errors.As(err, &ae) && (ae.Code == "not_live" || ae.Code == "livestream_not_found"):
		s.stop(stopOther, "the server has ended this stream (it heard nothing from this recorder for too long, or it was ended elsewhere)")
	case cloud.IsTransient(err):
		if s.now().Sub(s.lastOK) > s.grace-graceMargin {
			s.stop(stopLost, "")
			return
		}
		s.backoff = min(max(2*s.backoff, retryBase), retryMax)
		if !s.retrying {
			s.retrying = true
			s.say("recorder: cannot reach the server (%v); the stream is kept for up to %v and sent when it can be", shortErr(err), s.grace.Round(time.Second))
		}
		// the next tick tries again
	default:
		s.stop(stopOther, fmt.Sprintf("the server refused what was sent: %v", err))
	}
}

func shortErr(err error) string {
	msg := err.Error()
	if len(msg) > 120 {
		msg = msg[:120] + "…"
	}
	return msg
}

func progName() string { return "trecs" }

// stop ends the sending for good, and says why. The recording goes on, on this machine.
func (s *liveSender) stop(why, detail string) {
	s.mu.Lock()
	if s.stopped != "" {
		s.mu.Unlock()
		return
	}
	s.stopped = why
	s.queue = nil
	ending := s.closing
	s.mu.Unlock()
	s.pending, s.ends = nil, nil
	if why == stopLost && ending {
		return // the recording is over: the caller says what became of the stream
	}
	switch why {
	case stopQuota:
		s.say("Your session exceeded your storage quota. Streaming stopped, but you can continue recording, your recording will be saved locally and you upload it later.")
	case stopLost:
		s.say("The server could not be reached for longer than it keeps a stream waiting. Streaming stopped, but you can continue recording, your recording will be saved locally and you upload it later.")
	default:
		s.say("Streaming stopped: %s. You can continue recording, your recording will be saved locally and you upload it later.", detail)
	}
}

// finish ends the stream on the server (whatever it had becomes a recording), trying for a short while if the server cannot be reached.
func (s *liveSender) finish(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	var err error
	for attempt := 0; attempt < 4; attempt++ {
		var end *cloud.LivestreamEnd
		c, cancel := context.WithTimeout(ctx, sendTimeout)
		end, err = s.client.EndLivestream(c, s.id)
		cancel()
		if err == nil {
			s.result = end
			return
		}
		if !cloud.IsTransient(err) || ctx.Err() != nil {
			break
		}
		select {
		case <-time.After(time.Duration(attempt+1) * time.Second):
		case <-ctx.Done():
		}
	}
	s.resultErr = err
}
