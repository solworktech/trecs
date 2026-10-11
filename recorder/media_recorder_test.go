package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	libtrecs "trecs/lib"
)

func argsString(args []string) string { return strings.Join(args, " ") }

// ---- the platform ---------------------------------------------------------------------------------------------

// The platform was read from an environment variable that nothing sets: every machine got the Linux devices.
func TestThePlatformIsTheOneItRunsOnNotAnEnvironmentVariable(t *testing.T) {
	t.Setenv("GOOS", "windows")
	mr := newMediaRecorder("audio", &libtrecs.RecordingConfig{}, "x")
	if mr.goos != runtime.GOOS {
		t.Errorf("platform %q, running on %q", mr.goos, runtime.GOOS)
	}
}

func TestEachPlatformRecordsFromItsOwnDevices(t *testing.T) {
	cfg := func() *libtrecs.RecordingConfig { return &libtrecs.RecordingConfig{} }
	for _, tc := range []struct {
		goos, media, device, want string
		notWant                   string
	}{
		{"linux", "audio", "", "-f pulse -i default", "dshow"},
		{"darwin", "audio", "", "-f avfoundation -i :default", "pulse"},
		{"windows", "audio", "Microphone (USB Audio)", "-f dshow -i audio=Microphone (USB Audio)", "pulse"},
		{"freebsd", "audio", "", "-f pulse -i default", "dshow"},
		{"linux", "screen", "", "-f x11grab", "gdigrab"},
		{"darwin", "screen", "", "-f avfoundation", "x11grab"},
		{"windows", "screen", "", "-f gdigrab", "x11grab"},
	} {
		c := cfg()
		c.AudioDevice = tc.device
		mr := newMediaRecorder(tc.media, c, "x")
		mr.goos = tc.goos
		args, err := mr.buildArgs()
		if err != nil {
			t.Fatal(err)
		}
		got := argsString(args)
		if !strings.Contains(got, tc.want) || strings.Contains(got, tc.notWant) {
			t.Errorf("%s on %s: %s", tc.media, tc.goos, got)
		}
		if tc.goos == "windows" && tc.media == "audio" && strings.Contains(got, `"`) {
			t.Errorf("the arguments of a program are not shell text: no quotes: %s", got)
		}
	}
	// a macOS screen is a device number, a Windows one is the desktop
	mr := newMediaRecorder("screen", cfg(), "x")
	mr.goos = "darwin"
	if args, _ := mr.buildArgs(); !strings.Contains(argsString(args), "-i 1") {
		t.Errorf("%v", args)
	}
	mr.goos = "windows"
	if args, _ := mr.buildArgs(); !strings.Contains(argsString(args), "-i desktop") {
		t.Errorf("%v", args)
	}
}

// ---- audio ------------------------------------------------------------------------------------------------------

func TestAVoiceIsRecordedAsMonoOpusByDefault(t *testing.T) {
	mr := newMediaRecorder("audio", &libtrecs.RecordingConfig{}, "x")
	args, _ := mr.buildArgs()
	got := argsString(args)
	for _, want := range []string{"-c:a libopus", "-b:a 32k", "-ac 1", "-application voip", "-ar 48000"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %s", want, got)
		}
	}
	// what was asked for is what is done
	mr = newMediaRecorder("audio", &libtrecs.RecordingConfig{AudioCodec: "libopus", AudioBitrate: "24k"}, "x")
	args, _ = mr.buildArgs()
	if got := argsString(args); !strings.Contains(got, "-b:a 24k") || strings.Contains(got, "32k") {
		t.Error(got)
	}
	mr = newMediaRecorder("audio", &libtrecs.RecordingConfig{AudioCodec: "libmp3lame"}, "x")
	args, _ = mr.buildArgs()
	if got := argsString(args); !strings.Contains(got, "-c:a libmp3lame") || !strings.Contains(got, "-write_xing 1") || !strings.Contains(got, "-b:a 96k") {
		t.Error(got)
	}
}

func TestTheAudioFileIsNamedByItsCodec(t *testing.T) {
	for codec, want := range map[string]string{"": "opus", "libopus": "opus", "opus": "opus", "libmp3lame": "mp3", "mp3": "mp3", "aac": "m4a", "libvorbis": "ogg", "flac": "flac", "pcm_s16le": "mka"} {
		if got := audioExtension(codec); got != want {
			t.Errorf("%q: %q, want %q", codec, got, want)
		}
	}
	dir := t.TempDir()
	for codec, ext := range map[string]string{"": ".opus", "libmp3lame": ".mp3"} {
		sr, err := NewSessionRecorder(&libtrecs.RecordingConfig{OutputDir: dir, SessionName: "s" + ext, AudioEnabled: true, AudioCodec: codec})
		if err != nil {
			t.Fatal(err)
		}
		if f := sr.GetMetadata().AudioFile; !strings.HasSuffix(f, "audio"+ext) {
			t.Errorf("%q: %s", codec, f)
		}
	}
}

func TestStatsPeriodComesWith4_4(t *testing.T) {
	for v, want := range map[string]bool{
		"ffmpeg version 4.2.7-0ubuntu0.1 Copyright": false, "ffmpeg version 4.3.9": false, "ffmpeg version 4.4": true,
		"ffmpeg version 5.1.2": true, "ffmpeg version 6.1.1-3ubuntu5": true, "ffmpeg version n7.0": true,
		"ffmpeg version N-109999-gabcdef": true, "something else entirely": true,
	} {
		if got := versionHasStatsPeriod(v); got != want {
			t.Errorf("%q: %v", v, got)
		}
	}
}

func TestWhatFFmpegSaysOfWhatItHasRecorded(t *testing.T) {
	for name, tc := range map[string]struct {
		block map[string]string
		ok    bool
		d     time.Duration
	}{
		"audio with a time":       {map[string]string{"out_time_us": "20000"}, true, 20 * time.Millisecond},
		"nothing yet":             {map[string]string{"out_time_us": "N/A", "frame": "0", "total_size": "0"}, false, 0},
		"an old ffmpeg's nothing": {map[string]string{"out_time_us": "-9223372036854775808"}, false, 0},
		"a video held up":         {map[string]string{"out_time_us": "N/A", "frame": "3"}, true, 0},
		"bytes written":           {map[string]string{"total_size": "4096"}, true, 0},
	} {
		d, ok := recordedSoFar(tc.block)
		if ok != tc.ok || d != tc.d {
			t.Errorf("%s: %v %v", name, d, ok)
		}
	}
}

// ---- real ffmpeg ------------------------------------------------------------------------------------------------

func needFFmpeg(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("no ffmpeg")
	}
	out, _ := exec.Command("ffmpeg", "-hide_banner", "-encoders").Output()
	if !bytes.Contains(out, []byte("libopus")) || !bytes.Contains(out, []byte("libmp3lame")) {
		t.Skip("this ffmpeg has no libopus and libmp3lame")
	}
}

// a tone, in real time, in place of a microphone
func toneRecorder(t *testing.T, codec string) (*MediaRecorderImpl, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "audio."+audioExtension(codec))
	mr, _ := NewAudioRecorder(&libtrecs.RecordingConfig{AudioCodec: codec}, path)
	mr.inputArgs = []string{"-re", "-f", "lavfi", "-i", "sine=frequency=330:sample_rate=48000"}
	return mr, path
}

func probe(t *testing.T, path string, entries ...string) string {
	t.Helper()
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("no ffprobe")
	}
	out, err := exec.Command("ffprobe", "-v", "error", "-show_entries", strings.Join(entries, ":"), "-of", "default=nw=1:nk=0", path).CombinedOutput()
	if err != nil {
		t.Fatalf("ffprobe %s: %v\n%s", path, err, out)
	}
	return string(out)
}

func TestARecorderSaysWhenItIsCapturingAndStopsWithAFinishedFile(t *testing.T) {
	needFFmpeg(t)
	mr, path := toneRecorder(t, "")
	began := time.Now()
	if err := mr.Start(); err != nil {
		t.Fatal(err)
	}
	if err := mr.WaitReady(10 * time.Second); err != nil {
		t.Fatal(err)
	}
	ready := time.Since(began)
	if ready > 5*time.Second {
		t.Errorf("took %v to be capturing", ready)
	}
	time.Sleep(1500 * time.Millisecond)

	stopping := time.Now()
	if err := mr.Stop(); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(stopping); took > 4*time.Second {
		t.Errorf("stopping took %v", took)
	}
	if err := mr.Wait(); err != nil {
		t.Errorf("Wait after a Stop: %v", err)
	}
	out := probe(t, path, "stream=codec_name,channels,sample_rate", "format=duration")
	d := durationOf(t, out)
	if !strings.Contains(out, "codec_name=opus") || !strings.Contains(out, "channels=1") || d < 1.0 || d > 3.5 {
		t.Errorf("%s", out)
	}
	t.Logf("capturing after %v, %.2f s recorded, %d bytes", ready.Round(time.Millisecond), d, fileSize(t, path))
}

func durationOf(t *testing.T, probeOut string) float64 {
	t.Helper()
	for _, l := range strings.Split(probeOut, "\n") {
		if v, ok := strings.CutPrefix(l, "duration="); ok {
			var f float64
			if _, err := fmt.Sscan(v, &f); err == nil {
				return f
			}
		}
	}
	t.Fatalf("no duration in %q", probeOut)
	return 0
}

func fileSize(t *testing.T, p string) int64 {
	t.Helper()
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Size()
}

// The sample that played badly on Android had none of these: no header to say how long it is, and no table to
// seek by. An MP3 from here has it.
func TestAnMP3HasTheHeaderThatSaysHowLongItIs(t *testing.T) {
	needFFmpeg(t)
	mr, path := toneRecorder(t, "libmp3lame")
	if err := mr.Start(); err != nil {
		t.Fatal(err)
	}
	if err := mr.WaitReady(10 * time.Second); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1200 * time.Millisecond)
	if err := mr.Stop(); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	head := data[:min(len(data), 4096)]
	if !bytes.Contains(head, []byte("Info")) && !bytes.Contains(head, []byte("Xing")) {
		t.Error("no Xing/Info header")
	}
	out, _ := exec.Command("ffprobe", "-v", "warning", "-i", path).CombinedOutput()
	if strings.Contains(string(out), "Estimating duration") {
		t.Errorf("a player has to guess how long it is: %s", out)
	}
}

func TestAnInputThatFailsIsReportedWithWhatFFmpegSaid(t *testing.T) {
	needFFmpeg(t)
	mr, _ := toneRecorder(t, "")
	mr.inputArgs = []string{"-i", "/no/such/microphone.wav"}
	if err := mr.Start(); err != nil {
		t.Fatal(err)
	}
	err := mr.WaitReady(10 * time.Second)
	if err == nil || !strings.Contains(err.Error(), "stopped before it began recording") || !strings.Contains(err.Error(), "No such file or directory") {
		t.Errorf("%v", err)
	}
	if mr.Wait() == nil {
		t.Error("the failure is what Wait returns")
	}
	if err := mr.Stop(); err != nil {
		t.Errorf("stopping what has stopped: %v", err)
	}
}

func TestStoppingTwiceFromTwoPlacesWaitsForTheSameEnd(t *testing.T) {
	needFFmpeg(t)
	mr, path := toneRecorder(t, "")
	if err := mr.Start(); err != nil {
		t.Fatal(err)
	}
	if err := mr.WaitReady(10 * time.Second); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Add(1)
		go func() { defer wg.Done(); errs[i] = mr.Stop() }()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Error(err)
		}
	}
	select {
	case <-mr.exited:
	default:
		t.Error("Stop returned before ffmpeg had gone")
	}
	probe(t, path, "format=duration") // a file that can be read
}

func TestStoppingARecorderThatNeverStartedIsNothing(t *testing.T) {
	mr, _ := NewAudioRecorder(&libtrecs.RecordingConfig{}, filepath.Join(t.TempDir(), "a.opus"))
	if err := mr.Stop(); err != nil {
		t.Error(err)
	}
}

// ---- an ffmpeg that does not do as it is asked -----------------------------------------------------------------

func fakeFFmpeg(t *testing.T, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell script")
	}
	path := filepath.Join(t.TempDir(), "ffmpeg")
	script := "#!/bin/sh\nif [ \"$1\" = \"-version\" ]; then echo 'ffmpeg version 6.0'; exit 0; fi\n" + body
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func slowToStop(t *testing.T, script string) *MediaRecorderImpl {
	mr, _ := NewAudioRecorder(&libtrecs.RecordingConfig{}, filepath.Join(t.TempDir(), "a.opus"))
	mr.ffmpeg = fakeFFmpeg(t, script)
	mr.timeouts = mediaTimeouts{ready: 5 * time.Second, stopGrace: 300 * time.Millisecond, interruptGrace: 300 * time.Millisecond, killGrace: 2 * time.Second}
	return mr
}

// reports progress, and ignores the request to finish, and the interrupt
const stubborn = "trap '' INT TERM\nwhile true; do echo out_time_us=0; echo progress=continue; sleep 0.1; done\n"

// the same, but the interrupt is heard
const hearsInterrupt = "trap 'exit 0' INT\nwhile true; do echo out_time_us=0; echo progress=continue; sleep 0.1; done\n"

func TestAnFFmpegThatIgnoresTheRequestIsInterruptedAndThenKilled(t *testing.T) {
	mr := slowToStop(t, stubborn)
	if err := mr.Start(); err != nil {
		t.Fatal(err)
	}
	if err := mr.WaitReady(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	began := time.Now()
	err := mr.Stop()
	if err == nil || !strings.Contains(err.Error(), "killed") {
		t.Errorf("%v", err)
	}
	if took := time.Since(began); took > 3*time.Second {
		t.Errorf("stopping took %v", took)
	}
	if mr.Wait() == nil {
		t.Error("a kill is not a good end")
	}
	if !mr.exitedWithin(0) {
		t.Error("still running")
	}
}

func TestAnInterruptIsTriedBeforeAKill(t *testing.T) {
	mr := slowToStop(t, hearsInterrupt)
	if err := mr.Start(); err != nil {
		t.Fatal(err)
	}
	if err := mr.WaitReady(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	if err := mr.Stop(); err != nil {
		t.Errorf("an end that was asked for and heard: %v", err)
	}
	if err := mr.Wait(); err != nil {
		t.Errorf("%v", err)
	}
}

func TestARecorderThatIsNeverCapturingIsGivenUpOn(t *testing.T) {
	mr := slowToStop(t, "trap '' INT TERM\nwhile true; do sleep 0.1; done\n") // says nothing
	if err := mr.Start(); err != nil {
		t.Fatal(err)
	}
	err := mr.WaitReady(400 * time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "did not begin recording within") {
		t.Errorf("%v", err)
	}
	_ = mr.Stop()
}
