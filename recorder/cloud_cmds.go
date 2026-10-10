package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"time"

	"golang.org/x/term"

	"trecs/cloud"
)

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "Error: "+format+"\n", args...)
	os.Exit(1)
}

// savedServer is the server of the saved session, "" if there is none.
func savedServer() string {
	if s, err := cloud.LoadSession(); err == nil {
		return s.Server
	}
	return ""
}

func readLine(r *bufio.Reader) (string, error) {
	line, err := r.ReadString('\n')
	if err != nil && (!errors.Is(err, io.EOF) || line == "") {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func runLogin(args []string) {
	fs := flag.NewFlagSet("login", flag.ExitOnError)
	server := fs.String("server", os.Getenv("TRECS_SERVER"), "Server address, e.g. https://trecs.example.com (default: $TRECS_SERVER, else the server you last logged in to)")
	email := fs.String("email", "", "Account email (asked for if omitted)")
	passStdin := fs.Bool("password-stdin", false, "Read the password from standard input instead of asking for it")
	_ = fs.Parse(args)

	if *server == "" {
		*server = savedServer()
	}
	if *server == "" {
		var err error
		in := bufio.NewReader(os.Stdin)
		fmt.Fprint(os.Stderr, "API server URL: ")
		if *server, err = readLine(in); err != nil {
			fail("reading API server URL: %v", err)
		}
		// fail("which server? Use -server https://trecs.example.com (or set $TRECS_SERVER)")
	}
	client, err := cloud.New(*server)
	if err != nil {
		fail("%v", err)
	}

	in := bufio.NewReader(os.Stdin)
	if *email == "" {
		fmt.Fprint(os.Stderr, "Email: ")
		if *email, err = readLine(in); err != nil {
			fail("reading the email: %v", err)
		}
	}
	var password string
	if fd := int(os.Stdin.Fd()); !*passStdin && term.IsTerminal(fd) {
		fmt.Fprint(os.Stderr, "Password: ")
		b, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
		if err != nil {
			fail("reading the password: %v", err)
		}
		password = string(b)
	} else if password, err = readLine(in); err != nil {
		fail("reading the password from standard input: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), cloud.DefaultTimeout)
	defer cancel()
	session, err := client.Login(ctx, strings.TrimSpace(*email), password)
	if err != nil {
		fail("login failed: %v", err)
	}
	if err := session.Save(); err != nil {
		fail("logged in, but could not save the session: %v", err)
	}
	path, _ := cloud.SessionPath()
	fmt.Printf("Logged in to %s as %s.\nSession saved to %s\n", session.Server, session.Email, path)
}

func runLogout() {
	session, err := cloud.LoadSession()
	if errors.Is(err, cloud.ErrNotLoggedIn) {
		_ = cloud.DeleteSession() // an expired file is as good as none
		fmt.Println("Not logged in.")
		return
	}
	if err != nil {
		fail("%v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), cloud.DefaultTimeout)
	defer cancel()
	// The local copy goes whatever the server says: logging out must work when
	// the server is unreachable, and the session expires there on its own.
	if err := cloud.FromSession(session).Logout(ctx); err != nil && !errors.Is(err, cloud.ErrNotLoggedIn) {
		fmt.Fprintf(os.Stderr, "Warning: the server could not be told (%v); the session there will expire by itself.\n", err)
	}
	if err := cloud.DeleteSession(); err != nil {
		fail("could not remove the saved session: %v", err)
	}
	fmt.Printf("Logged out of %s.\n", session.Server)
}

var visibilities = map[string]bool{"private": true, "unlisted": true, "public": true}

func runUpload(args []string) {
	fs := flag.NewFlagSet("upload", flag.ExitOnError)
	name := fs.String("name", "", "Recording title (default: a timestamp)")
	description := fs.String("description", "", "Description (default: the list of commands typed in the recording)")
	visibility := fs.String("visibility", "", "private, unlisted or public (default: private - recordings often show secrets)")
	audio := fs.String("audio", "", "Audio file to attach (default: audio.mp3 beside the recording, if there is one)")
	_ = fs.Parse(args)
	if fs.NArg() != 1 {
		fail("usage: recorder upload [-name N] [-description D] [-visibility private|unlisted|public] [-audio FILE] <session directory | terminal.jsonl>\n(options go before the path)")
	}
	terminal, sessionAudio, err := cloud.ResolveSessionFiles(fs.Arg(0))
	if err != nil {
		fail("%v", err)
	}
	if *audio == "" {
		*audio = sessionAudio
	}
	uploadFiles(cloud.Upload{Name: *name, Description: *description, Visibility: *visibility, TerminalPath: terminal, AudioPath: *audio})
}

// uploadFiles sends one recording to the server of the saved session and
// reports what happened. It exits non-zero on failure, saying what to do.
func uploadFiles(u cloud.Upload) {
	if u.Visibility != "" && !visibilities[u.Visibility] {
		fail("visibility must be private, unlisted or public, not %q", u.Visibility)
	}
	session, err := cloud.LoadSession()
	if err != nil {
		fail("%v", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	fmt.Printf("Uploading %s to %s ...\n", u.TerminalPath, session.Server)
	start := time.Now()
	rec, err := cloud.FromSession(session).Upload(ctx, u)
	if err != nil {
		fail("upload failed: %v\nThe recording is untouched; upload it later with: recorder upload %s", err, u.TerminalPath)
	}
	secs := rec.DurationMs / 1000
	fmt.Printf("Uploaded \"%s\" (%d:%02d, %s) in %v\n", rec.Name, secs/60, secs%60, rec.Visibility, time.Since(start).Round(time.Millisecond))
	fmt.Printf("Watch: %s/watch/%s\n", session.Server, rec.ID)
}
