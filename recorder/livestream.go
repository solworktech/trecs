package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"time"

	"trecs/cloud"
)

// `trecs livestream create` makes a stream, which has an address and an id and is waiting; `trecs livestream ID` records a shell as
// `record` does and, as it records, streams it to that address, where it is watched as it happens. When it ends, what was streamed
// is a recording on the server like any other (a new one every time), and the recording on this machine is the whole of it, with
// nothing masked.

// liveRun is a stream being streamed to, from the command that was given until the recording is over.
type liveRun struct {
	client *cloud.Client
	id     string
	info   *cloud.Livestream
	left   *int64 // what the account may still store; nil = no limit
	sender *liveSender
}

// livestreamWarning is what a person is told before a stream starts. left is what the account may still store (nil: no limit).
func livestreamWarning(left *int64) string {
	w := "This will start a live stream. We will make an effort to detect and mask any detected secrets, but we cannot guarantee detection.\n"
	if left == nil {
		return w
	}
	return w + fmt.Sprintf("You have %s available on the server, if you exceed this amount, live streaming will end gracefully but you can continue recording,\nyour recording will be saved locally and you upload it later.", formatBytes(*left))
}

// formatBytes is a size for a person: 512 MB, 1.5 GB.
func formatBytes(n int64) string {
	const kb, mb, gb = 1 << 10, 1 << 20, 1 << 30
	switch {
	case n >= gb:
		return trimZero(fmt.Sprintf("%.1f", float64(n)/gb)) + " GB"
	case n >= mb:
		return fmt.Sprintf("%d MB", n/mb)
	case n >= kb:
		return fmt.Sprintf("%d KB", n/kb)
	}
	return fmt.Sprintf("%d B", n)
}

func trimZero(s string) string { return strings.TrimSuffix(s, ".0") }

// confirmLivestream warns about what a stream does and asks the person to confirm it; it reports whether to go on. Only the exact answer
// YES goes on. With no person to ask (CI, a pipe) the warning is still shown, and it goes on. With a script (-commands-file) what is
// said about the script comes with it, and the question is asked once.
func confirmLivestream(file, endpoint string, left *int64, in io.Reader, out io.Writer, interactive bool, warnings int) bool {
	say(out, "\n%s\n\n", yellow(out, livestreamWarning(left)))
	if file != "" {
		say(out, "%s\n\n", yellow(out, fmt.Sprintf(scriptWarning, file)))
		if warnings > 0 {
			say(out, "%s\n", red(out, auditWarningLine(file, warnings)))
		}
		say(out, "%s\n", yellow(out, fmt.Sprintf("This will stream commands from %s to %s.", file, endpoint)))
	}
	if !interactive {
		return true
	}
	return askYES(in, out)
}

// askYES is the question every warning ends with.
func askYES(in io.Reader, out io.Writer) bool {
	say(out, "%s", yellow(out, "Type 'YES' to continue, any other input to abort: "))
	return readYES(in)
}

func runLivestream(args []string) {
	if len(args) > 0 && args[0] == "create" {
		runLivestreamCreate(args[1:])
		return
	}
	runLivestreamStream(args)
}

func runLivestreamCreate(args []string) {
	fs := flag.NewFlagSet("livestream create", flag.ExitOnError)
	name := fs.String("name", "", "The stream's title (default: a timestamp)")
	description := fs.String("description", "", "The stream's description (Markdown)")
	visibility := fs.String("visibility", "", "private (default), unlisted or public. The recordings the stream makes have it too")
	_ = fs.Parse(args)
	if fs.NArg() != 0 {
		fail("usage: %s livestream create [-name N] [-description D] [-visibility private|unlisted|public]", os.Args[0])
	}
	if *visibility != "" && !visibilities[*visibility] {
		fail("visibility must be private, unlisted or public, not %q", *visibility)
	}
	session, err := cloud.LoadSession()
	if err != nil {
		fail("%v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), cloud.DefaultTimeout)
	defer cancel()
	ls, err := cloud.FromSession(session).CreateLivestream(ctx, cloud.LivestreamCreate{Name: *name, Description: *description, Visibility: *visibility})
	if err != nil {
		fail("could not create the livestream: %v", err)
	}
	fmt.Printf("Created the livestream \"%s\" (%s).\n", ls.Name, ls.Visibility)
	fmt.Printf("ID: %s\n", ls.ID)
	fmt.Printf("Watch page: %s\n", ls.URL)
	fmt.Printf("To start streaming, type `%s livestream %s`\n", os.Args[0], ls.ID)
}

// runLivestreamStream is `livestream ID [options]`: the options of `record` that make sense for a stream (there is no audio, camera or
// screen in one yet, and no upload: the server has the recording when it ends).
func runLivestreamStream(args []string) {
	var idArg string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") { // livestream ID [options]
		idArg, args = args[0], args[1:]
	}
	fs := flag.NewFlagSet("livestream", flag.ExitOnError)
	audio := fs.Bool("audio", false, "Not available for live streams yet")
	outputDir := fs.String("output", defaultOutputDir(), "Where the recording on this machine is kept")
	sessionName := fs.String("name", "", "Name of the recording on this machine, a plain one: no / or .. (default: timestamp YYYY_MM_DD_HH_MM_SS)")
	terminalCmd := fs.String("cmd", "", "Shell to run (default: the shell this was run from)")
	extras := defineExtras(fs)
	_ = fs.Parse(args)
	if idArg == "" {
		if fs.NArg() != 1 {
			fail("usage: %s livestream <id | address of the stream's page> [options]\n(make one first with `%s livestream create`)", os.Args[0], os.Args[0])
		}
		idArg = fs.Arg(0)
	} else if fs.NArg() != 0 {
		fail("unexpected argument %q: after the livestream's id come only options (see `%s help`)", fs.Arg(0), os.Args[0])
	}
	if *audio {
		fail("-audio is not supported for live streams yet. Record with `%s record -audio` and upload that instead", os.Args[0])
	}
	id, err := cloud.LivestreamID(idArg)
	if err != nil {
		fail("%v", err)
	}
	session, err := cloud.LoadSession()
	if err != nil {
		fail("%v", err)
	}
	client := cloud.FromSession(session)

	ctx, cancel := context.WithTimeout(context.Background(), cloud.DefaultTimeout)
	info, err := client.GetLivestream(ctx, id)
	if err != nil {
		cancel()
		var ae *cloud.APIError
		if errors.As(err, &ae) && ae.Status == 404 {
			fail("there is no livestream %s on %s that your account can see (is it from another server, or another account? `%s login` logs in again)", id, session.Server, os.Args[0])
		}
		fail("%v", err)
	}
	if info.Status == "live" {
		cancel()
		fail("%s is already being streamed to (since %s). Nothing was started.\nIf the recorder that was streaming to it is gone, the server ends the stream by itself a few minutes after the last it heard from it, and it can be streamed to again.",
			info.URL, startedAt(info))
	}
	q, err := client.GetQuota(ctx)
	cancel()
	if err != nil {
		fail("could not ask the server how much storage you have: %v", err)
	}

	*extras.noUpload = true // the server has the recording when the stream ends
	live := &liveRun{client: client, id: id, info: info, left: q.Left()}
	t, f := true, false
	empty := ""
	runRecord(&t, &f, &f, &f, outputDir, sessionName, terminalCmd,
		strp("default"), strp("libopus"), &empty, strp("/dev/video0"), strp("640x480"), strp(":0"), strp("1920x1080"), &f, &empty, extras, live)
}

func strp(s string) *string { return &s }

func startedAt(ls *cloud.Livestream) string {
	if ls.StartedAt == nil {
		return "a moment ago"
	}
	return ls.StartedAt.Local().Format("15:04:05")
}

// confirm is the warning and the question before a stream starts.
func (l *liveRun) confirm(scriptFile string, warnings int) bool {
	return confirmLivestream(scriptFile, l.info.URL, l.left, os.Stdin, os.Stderr, stdinIsTerminal(), warnings)
}

// start takes the stream: from here this recorder is the one streaming to it. It fails, saying so, if another is, or if it is not
// this account's.
func (l *liveRun) start() error {
	ctx, cancel := context.WithTimeout(context.Background(), cloud.DefaultTimeout)
	defer cancel()
	st, err := l.client.StartLivestream(ctx, l.id)
	if err != nil {
		var ae *cloud.APIError
		switch {
		case errors.As(err, &ae) && ae.Code == "already_live":
			return fmt.Errorf("%s is already being streamed to. Nothing was started", l.info.URL)
		case errors.As(err, &ae) && ae.Status == 403:
			return errors.New("only the account that made a livestream can stream to it: log in with that account (`" + os.Args[0] + " login`)")
		}
		return err
	}
	l.info = &st.Livestream
	if st.BytesLeft != nil {
		l.left = st.BytesLeft
	}
	l.sender = newLiveSender(l.client, l.id, st, os.Stderr)
	return nil
}

// release gives the stream back when the recording could not begin after it was taken.
func (l *liveRun) release() {
	if l.sender == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), cloud.DefaultTimeout)
	defer cancel()
	_, _ = l.sender.Close(ctx)
}

// finish ends the stream and says what became of it.
func (l *liveRun) finish(localFile string) {
	if l.sender == nil {
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	end, err := l.sender.Close(ctx)
	why := l.sender.Stopped()
	fmt.Println()
	switch {
	case err != nil:
		fmt.Printf("The stream could not be ended on the server (%v).\n", err)
		fmt.Println("The server ends it by itself a few minutes after it last heard from this recorder, and keeps what it received as a recording.")
	case end.RecordingID == nil:
		fmt.Println("Streaming ended. Nothing had been streamed, so there is no recording on the server.")
	case why != "":
		fmt.Printf("Streaming ended early. What was streamed before it stopped is a recording: %s\n", deref(end.RecordingURL))
	default:
		fmt.Printf("Streaming ended. The stream is now a recording: %s\n", deref(end.RecordingURL))
	}
	if why != "" || err != nil {
		fmt.Printf("The recording on this machine is whole: %s\n", localFile)
		if end != nil && end.RecordingID != nil {
			fmt.Printf("To replace the partial one on the server with it, once there is room: %s update %s %s\n", os.Args[0], *end.RecordingID, localFile)
		} else {
			fmt.Printf("To put it on the server: %s upload %s\n", os.Args[0], localFile)
		}
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
