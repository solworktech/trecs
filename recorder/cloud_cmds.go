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
	libtrecs "trecs/lib"
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
	server := fs.String("server", "", "Server address, e.g. https://trecs.example.com (default: "+serverDefaultHelp()+")")
	email := fs.String("email", "", "Account email (asked for if omitted)")
	passStdin := fs.Bool("password-stdin", false, "Read the password from standard input instead of asking for it")
	_ = fs.Parse(args)

	*server = resolveServer(*server, savedServer(), os.Getenv("TRECS_SERVER"), defaultServer)
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
	description := fs.String("description", "", "Your own description (Markdown)")
	var fromAnnotations, fromCommands bool
	fs.BoolVar(&fromAnnotations, "annotations-as-description", false, "Describe the recording with its annotations marked \"show in nav menu\", each linking to its moment")
	fs.BoolVar(&fromAnnotations, "aad", false, "Short for -annotations-as-description")
	fs.BoolVar(&fromCommands, "commands-as-description", false, "Describe the recording with the list of commands typed in it (the default)")
	fs.BoolVar(&fromCommands, "cad", false, "Short for -commands-as-description")
	visibility := fs.String("visibility", "", "private, unlisted or public (default: private - recordings often show secrets)")
	audio := fs.String("audio", "", "Audio file to attach (default: audio.opus - or .mp3, .m4a, .ogg - beside the recording, if there is one)")
	markdown := fs.String("markdown", "", "Markdown file to keep as the recording's Markdown export (default: made from the recording)")
	_ = fs.Parse(args)
	if fs.NArg() != 1 {
		fail("usage: %s upload [-name N] [-description D | -annotations-as-description | -commands-as-description] [-visibility private|unlisted|public] [-audio FILE] [-markdown FILE] <session directory | terminal.jsonl>\n(options go before the path)", os.Args[0])
	}
	chosen := 0
	for _, on := range []bool{*description != "", fromAnnotations, fromCommands} {
		if on {
			chosen++
		}
	}
	if chosen > 1 {
		fail("choose one of -description, -annotations-as-description (-aad) and -commands-as-description (-cad)")
	}
	if *markdown != "" {
		if fi, err := os.Stat(*markdown); err != nil || fi.IsDir() {
			fail("-markdown %s: not a readable file", *markdown)
		}
	}
	terminal, sessionAudio, err := cloud.ResolveSessionFiles(fs.Arg(0))
	if err != nil {
		fail("%v", err)
	}
	if *audio == "" {
		*audio = sessionAudio
	}
	// the pictures the recording shows go with it: they are files beside it, and the server serves them to the player
	images, missing, err := libtrecs.ImageFiles(terminal)
	if err != nil {
		fail("%v", err)
	}
	if len(missing) > 0 {
		fail("the recording shows %s, which %s not in %s: put %s back, or delete the image in the editor",
			strings.Join(missing, ", "), pluralIs(len(missing)), libtrecs.ImagesPath(terminal), pluralThem(len(missing)))
	}
	uploadFiles(cloud.Upload{Name: *name, Description: *description, Visibility: *visibility, TerminalPath: terminal, AudioPath: *audio, MarkdownPath: *markdown, ImagePaths: images}, fromAnnotations)
}

func pluralIs(n int) string {
	if n == 1 {
		return "is"
	}
	return "are"
}

func pluralThem(n int) string {
	if n == 1 {
		return "it"
	}
	return "them"
}

// uploadFiles sends one recording to the server of the saved session and
// reports what happened. It exits non-zero on failure, saying what to do.
func uploadFiles(u cloud.Upload, annotationsAsDescription bool) {
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
	client := cloud.FromSession(session)
	rec, err := client.Upload(ctx, u)
	if err != nil {
		fail("upload failed: %v\nThe recording is untouched; upload it later with: %s upload %s", err, os.Args[0], u.TerminalPath)
	}
	secs := rec.DurationMs / 1000
	fmt.Printf("Uploaded \"%s\" (%d:%02d, %s) in %v\n", rec.Name, secs/60, secs%60, rec.Visibility, time.Since(start).Round(time.Millisecond))
	watchURL := session.Server + "/watch/" + rec.ID
	fmt.Printf("Watch: %s\n", watchURL)
	if annotationsAsDescription {
		if err := describeFromAnnotations(ctx, client, rec, u.TerminalPath, watchURL); err != nil {
			fail("the recording is uploaded, but its description could not be set: %v\nChange it from the recording's edit page.", err)
		}
	}
}

// describeFromAnnotations replaces the description of an uploaded recording with its annotations marked "show in
// nav menu", as links to their moments. That is done after the upload because the links need the recording's id, which the
// server gives it. With no such annotations nothing is changed (the description stays the list of commands), and
// says so.
func describeFromAnnotations(ctx context.Context, client *cloud.Client, rec *cloud.Recording, terminal, watchURL string) error {
	_, annotations, _, err := libtrecs.LoadFrames(terminal)
	if err != nil {
		return err
	}
	desc, used, total := libtrecs.AnnotationDescription(annotations, watchURL, descriptionMaxChars)
	if total == 0 {
		fmt.Fprintln(os.Stderr, "No annotation is marked \"show in nav menu\": the description is the list of commands instead.")
		return nil
	}
	if used == 0 {
		fmt.Fprintf(os.Stderr, "The first annotation alone is longer than the %d characters a description may have: the description is the list of commands instead.\n", descriptionMaxChars)
		return nil
	}
	if _, err := client.UpdateRecording(ctx, rec.ID, cloud.RecordingEdit{Name: rec.Name, Description: desc, Visibility: rec.Visibility}); err != nil {
		return err
	}
	if used < total {
		fmt.Fprintf(os.Stderr, "Only the first %d of %d annotations fit in the %d characters a description may have.\n", used, total, descriptionMaxChars)
	}
	fmt.Printf("Description: %d annotation(s) from the nav menu\n", used)
	return nil
}

// descriptionMaxChars is the longest description the server keeps.
const descriptionMaxChars = 2000
