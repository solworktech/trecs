package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	libtrecs "trecs/lib"
)

// A media recorder is an ffmpeg that records a device - the microphone, the camera, the screen - to a file.
//
// Two things about it matter to a session that records the terminal as well, and both are about time:
//
//   - It begins when the session does. ffmpeg needs a moment after it is started to open its devices, and
//     what it records before then is nothing: a recording that starts the terminal and the media together is
//     out of step by that moment, and by an amount that is different every time. So the recorder can say when it is
//     capturing (WaitReady), and the session starts the terminal when all of them are.
//   - It ends when the session does, and is given the time to end properly. The index of an MP4, the header of an
//     MP3 and the last page of an Ogg are written when ffmpeg is told to finish, not when it is killed.

// mediaTimeouts are how long a media recorder waits for ffmpeg at each step.
type mediaTimeouts struct {
	ready          time.Duration // to begin capturing, from the start (device and encoder setup)
	stopGrace      time.Duration // to finish its file after being asked to
	interruptGrace time.Duration // to finish after an interrupt, if it did not react to being asked
	killGrace      time.Duration // to be gone after being killed
}

var defaultMediaTimeouts = mediaTimeouts{ready: 20 * time.Second, stopGrace: 10 * time.Second, interruptGrace: 3 * time.Second, killGrace: 2 * time.Second}

// MediaRecorderImpl implements MediaRecorder
type MediaRecorderImpl struct {
	mediaType  string // "audio", "camera", "screen"
	config     *libtrecs.RecordingConfig
	outputPath string

	// What the tests set: the platform whose devices are recorded, the program, a signal to record instead of a
	// device, the timeouts.
	goos      string
	ffmpeg    string
	inputArgs []string
	timeouts  mediaTimeouts

	mutex    sync.Mutex
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	started  bool
	logPath  string
	stopOnce sync.Once
	stopErr  error

	stopRequested atomic.Bool // Stop has asked ffmpeg to finish
	killed        atomic.Bool // and had to kill it

	ready     chan struct{} // closed when ffmpeg is capturing
	readyOnce sync.Once
	exited    chan struct{} // closed when ffmpeg has gone
	exitErr   error
}

func newMediaRecorder(mediaType string, config *libtrecs.RecordingConfig, outputPath string) *MediaRecorderImpl {
	return &MediaRecorderImpl{
		mediaType: mediaType, config: config, outputPath: outputPath,
		goos: runtime.GOOS, ffmpeg: "ffmpeg", timeouts: defaultMediaTimeouts,
		ready: make(chan struct{}), exited: make(chan struct{}),
	}
}

// NewAudioRecorder creates a new audio recorder
func NewAudioRecorder(config *libtrecs.RecordingConfig, outputPath string) (*MediaRecorderImpl, error) {
	return newMediaRecorder("audio", config, outputPath), nil
}

// NewCameraRecorder creates a new camera recorder
func NewCameraRecorder(config *libtrecs.RecordingConfig, outputPath string) (*MediaRecorderImpl, error) {
	return newMediaRecorder("camera", config, outputPath), nil
}

// NewScreenRecorder creates a new screen recorder
func NewScreenRecorder(config *libtrecs.RecordingConfig, outputPath string) (*MediaRecorderImpl, error) {
	return newMediaRecorder("screen", config, outputPath), nil
}

// Start starts ffmpeg and returns at once. It is not recording yet: see WaitReady.
func (mr *MediaRecorderImpl) Start() error {
	mr.mutex.Lock()
	defer mr.mutex.Unlock()

	if mr.started {
		return fmt.Errorf("media recorder already running")
	}

	args, err := mr.buildArgs()
	if err != nil {
		return err
	}
	// Append output file
	args = append(args, "-y", mr.outputPath)

	// The file is made before ffmpeg makes it, so that it is private from the start (ffmpeg would make it as the umask says).
	if f, err := createPrivate(mr.outputPath); err != nil {
		return fmt.Errorf("failed to create %s: %w", mr.outputPath, err)
	} else {
		_ = f.Close()
	}

	cmd := exec.Command(mr.ffmpeg, args...)
	ownProcessGroup(cmd)

	// ffmpeg's own messages go to a log file, not the console: the terminal is the recording's.
	mr.logPath = mr.outputPath + ".log"
	logFile, err := os.OpenFile(mr.logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("failed to create ffmpeg log file: %w", err)
	}
	cmd.Stderr = logFile

	// stdin is how ffmpeg is asked to finish; stdout is where it reports what it has recorded (-progress).
	stdin, err := cmd.StdinPipe()
	if err != nil {
		_ = logFile.Close()
		return fmt.Errorf("failed to start ffmpeg: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = logFile.Close()
		return fmt.Errorf("failed to start ffmpeg: %w", err)
	}
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return fmt.Errorf("failed to start ffmpeg: %w", err)
	}
	mr.cmd, mr.stdin, mr.started = cmd, stdin, true

	progressDone := make(chan struct{})
	go func() {
		mr.watchProgress(stdout)
		close(progressDone)
	}()
	go func() {
		<-progressDone // all of what it said is read before Wait closes the pipe
		err := cmd.Wait()
		_ = logFile.Close()
		mr.exitErr = err
		close(mr.exited)
	}()
	return nil
}

// watchProgress reads what ffmpeg reports of its work (-progress): blocks of key=value lines, each ended by a
// "progress=" line. The first block that says something has been recorded is the moment it is capturing.
func (mr *MediaRecorderImpl) watchProgress(r io.Reader) {
	sc := bufio.NewScanner(r)
	block := map[string]string{}
	for sc.Scan() {
		k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), "=")
		if !ok {
			continue
		}
		block[k] = v
		if k != "progress" {
			continue
		}
		if _, ok := recordedSoFar(block); ok {
			mr.readyOnce.Do(func() { close(mr.ready) })
		}
		clear(block)
	}
}

// recordedSoFar says, of a block of ffmpeg's progress report, whether anything has been recorded yet, and how much.
func recordedSoFar(block map[string]string) (time.Duration, bool) {
	if us, err := strconv.ParseInt(block["out_time_us"], 10, 64); err == nil && us >= 0 { // "N/A" until there is something
		return time.Duration(us) * time.Microsecond, true
	}
	if n, err := strconv.ParseInt(block["frame"], 10, 64); err == nil && n > 0 { // a video, whose first packet is held up by the encoder
		return 0, true
	}
	if n, err := strconv.ParseInt(block["total_size"], 10, 64); err == nil && n > 0 {
		return 0, true
	}
	return 0, false
}

// WaitReady blocks until ffmpeg is capturing, or says why it is not: it stopped (with what it said), or it took
// too long. timeout 0: the usual.
func (mr *MediaRecorderImpl) WaitReady(timeout time.Duration) error {
	if timeout <= 0 {
		timeout = mr.timeouts.ready
	}
	select {
	case <-mr.ready:
		return nil
	case <-mr.exited:
		select {
		case <-mr.ready: // it said so, and then it was done: a recording of nothing, but not a failure to start
			return nil
		default:
		}
		return fmt.Errorf("ffmpeg stopped before it began recording (%v)%s", mr.exitErr, mr.logTail())
	case <-time.After(timeout):
		return fmt.Errorf("ffmpeg did not begin recording within %v%s", timeout, mr.logTail())
	}
}

// logTail is the end of what ffmpeg wrote to its log, for an error message.
func (mr *MediaRecorderImpl) logTail() string {
	data, err := os.ReadFile(mr.logPath)
	if err != nil || len(strings.TrimSpace(string(data))) == 0 {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) > 6 {
		lines = lines[len(lines)-6:]
	}
	return "\nffmpeg said:\n  " + strings.Join(lines, "\n  ")
}

// buildArgs is ffmpeg's command line, but for the output file.
func (mr *MediaRecorderImpl) buildArgs() ([]string, error) {
	args := []string{
		"-hide_banner",
		"-loglevel", "warning", // warnings and errors, to the log
		"-nostats", "-progress", "pipe:1", // how much it has recorded, to the recorder
	}
	if ffmpegHasStatsPeriod(mr.ffmpeg) {
		args = append(args, "-stats_period", "0.1") // report at once, not twice a second: the start is known sooner
	}

	var media []string
	switch mr.mediaType {
	case "audio":
		media = mr.buildAudioArgs()
	case "camera":
		media = mr.buildCameraArgs()
	case "screen":
		media = mr.buildScreenArgs()
	default:
		return nil, fmt.Errorf("unknown media type: %s", mr.mediaType)
	}
	return append(args, media...), nil
}

// buildAudioArgs constructs FFMpeg arguments for audio recording
func (mr *MediaRecorderImpl) buildAudioArgs() []string {
	var args []string

	device := mr.config.AudioDevice
	if device == "" {
		device = "default"
	}

	// Platform-specific audio input
	switch {
	case mr.inputArgs != nil:
		args = append(args, mr.inputArgs...)
	case mr.goos == "darwin":
		args = append(args, "-f", "avfoundation", "-i", ":"+device) // "video:audio": no video
	case mr.goos == "windows":
		// An argument, not shell text: no quotes, whatever the device is called.
		args = append(args, "-f", "dshow", "-i", "audio="+device)
	default: // linux, and what is likely to be like it
		args = append(args, "-thread_queue_size", "1024", "-f", "pulse", "-i", device)
	}

	return append(args, audioCodecArgs(mr.config.AudioCodec, mr.config.AudioBitrate)...)
}

// audioCodecArgs says how audio is encoded.
//
// A voice is one channel, and what Opus does with it is the point: an hour of speech at 32 kbit/s is 14 MB where
// 128 kbit/s stereo MP3 is 58 MB, and it sounds better. In an Ogg container it also has what a player needs to
// find its way in the file (the position in time is in every page), and a file that was cut short - ffmpeg killed,
// the machine off - plays up to where it was cut.
func audioCodecArgs(codec, bitrate string) []string {
	if bitrate == "" {
		bitrate = defaultAudioBitrate(codec)
	}
	var args []string
	switch strings.ToLower(codec) {
	case "", "libopus", "opus":
		args = []string{"-c:a", "libopus", "-application", "voip", "-vbr", "on", "-ar", "48000", "-flush_packets", "1"}
	case "libmp3lame", "mp3":
		args = []string{"-c:a", "libmp3lame", "-write_xing", "1"} // the header that says how long it is, and where things are
	default:
		args = []string{"-c:a", codec}
	}
	args = append(args, "-ac", "1")
	if bitrate != "" {
		args = append(args, "-b:a", bitrate)
	}
	return args
}

// defaultAudioBitrate is what a codec is set to for a voice when nothing else is asked for.
func defaultAudioBitrate(codec string) string {
	switch strings.ToLower(codec) {
	case "", "libopus", "opus":
		return "32k"
	case "libmp3lame", "mp3":
		return "96k"
	case "aac", "libfdk_aac":
		return "64k"
	}
	return ""
}

// audioExtension is the extension of the file an audio codec is recorded to: what ffmpeg chooses the container by.
func audioExtension(codec string) string {
	switch strings.ToLower(codec) {
	case "", "libopus", "opus":
		return "opus"
	case "libmp3lame", "mp3":
		return "mp3"
	case "aac", "libfdk_aac":
		return "m4a"
	case "libvorbis", "vorbis":
		return "ogg"
	case "flac":
		return "flac"
	}
	return "mka" // Matroska takes any codec
}

// buildCameraArgs constructs FFMpeg arguments for camera recording
func (mr *MediaRecorderImpl) buildCameraArgs() []string {
	device := mr.config.CameraDevice
	if device == "" {
		device = "/dev/video0"
	}

	format := mr.config.CameraFormat
	if format == "" {
		format = "v4l2"
	}

	size := mr.config.CameraSize
	if size == "" {
		size = "640x480"
	}

	framerate := mr.config.CameraFramerate
	if framerate == "" {
		framerate = "30"
	}

	var args []string
	if mr.inputArgs != nil {
		args = append(args, mr.inputArgs...)
	} else {
		// Input format, device, size, and framerate
		args = append(args, "-thread_queue_size", "1024", "-f", format, "-video_size", size, "-framerate", framerate, "-i", device)
	}
	return append(args, videoCodecArgs()...)
}

// buildScreenArgs constructs FFMpeg arguments for screen recording
func (mr *MediaRecorderImpl) buildScreenArgs() []string {
	display := mr.config.ScreenDisplay
	if display == "" {
		display = ":0"
	}

	format := mr.config.ScreenFormat
	if format == "" {
		// Default based on OS
		switch mr.goos {
		case "darwin":
			format = "avfoundation"
			display = "1"
		case "windows":
			format = "gdigrab"
			display = "desktop"
		default:
			format = "x11grab"
		}
	}

	size := mr.config.ScreenSize
	if size == "" {
		size = "1920x1080"
	}

	framerate := mr.config.ScreenFramerate
	if framerate == "" {
		framerate = "30"
	}

	var args []string
	if mr.inputArgs != nil {
		args = append(args, mr.inputArgs...)
	} else {
		// Input parameters
		args = append(args, "-thread_queue_size", "1024", "-f", format, "-video_size", size, "-framerate", framerate, "-i", display)
	}
	return append(args, videoCodecArgs()...)
}

// videoCodecArgs: H.264, quickly. A slower preset looks ahead, which holds back the first frames when it begins and
// takes longer to be done when it ends.
func videoCodecArgs() []string {
	return []string{"-c:v", "libx264", "-preset", "veryfast", "-crf", "23", "-pix_fmt", "yuv420p"}
}

// Stop ends the recording, and returns when the file is finished: ffmpeg is asked to finish (the way it is asked
// to: "q" on its standard input), then interrupted if it does not, and killed only if it still does not - which
// leaves a file that may be incomplete, and the error says so. Safe to call more than once, from anywhere: they
// all wait for the same end.
func (mr *MediaRecorderImpl) Stop() error {
	mr.stopOnce.Do(func() { mr.stopErr = mr.stopProcess() })
	return mr.stopErr
}

func (mr *MediaRecorderImpl) stopProcess() error {
	mr.mutex.Lock()
	started, stdin, cmd := mr.started, mr.stdin, mr.cmd
	mr.mutex.Unlock()
	if !started {
		return nil
	}
	if mr.exitedWithin(0) { // it ended by itself - stopped by a failure, and Wait has what it said
		return nil
	}

	mr.stopRequested.Store(true)
	if stdin != nil {
		_, _ = io.WriteString(stdin, "q")
		_ = stdin.Close()
	}
	if mr.exitedWithin(mr.timeouts.stopGrace) {
		return nil
	}
	// It did not hear: the same request, by signal.
	_ = cmd.Process.Signal(os.Interrupt)
	if mr.exitedWithin(mr.timeouts.interruptGrace) {
		return nil
	}
	mr.killed.Store(true)
	_ = cmd.Process.Kill()
	mr.exitedWithin(mr.timeouts.killGrace)
	return fmt.Errorf("ffmpeg did not finish %s in time and was killed: the file may be incomplete", mr.outputPath)
}

func (mr *MediaRecorderImpl) exitedWithin(d time.Duration) bool {
	if d <= 0 {
		select {
		case <-mr.exited:
			return true
		default:
			return false
		}
	}
	select {
	case <-mr.exited:
		return true
	case <-time.After(d):
		return false
	}
}

// Wait blocks until ffmpeg has ended, and returns what ended it: nil for the end that Stop asks for, whatever status
// ffmpeg gives that end (it is 255 when it is interrupted), and the error for any other.
func (mr *MediaRecorderImpl) Wait() error {
	<-mr.exited
	if mr.exitErr != nil && mr.stopRequested.Load() && !mr.killed.Load() {
		return nil
	}
	return mr.exitErr
}

var (
	ffmpegVersion  = regexp.MustCompile(`ffmpeg version [nN]?(\d+)\.(\d+)`)
	statsPeriodFor sync.Map // ffmpeg program -> bool
)

// ffmpegHasStatsPeriod says whether this ffmpeg has the -stats_period option, which came with 4.4: from its version.
func ffmpegHasStatsPeriod(program string) bool {
	if v, ok := statsPeriodFor.Load(program); ok {
		return v.(bool)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, program, "-version").Output()
	has := err == nil && versionHasStatsPeriod(string(out))
	statsPeriodFor.Store(program, has)
	return has
}

func versionHasStatsPeriod(versionOutput string) bool {
	m := ffmpegVersion.FindStringSubmatch(versionOutput)
	if m == nil {
		return true // a build from git (N-12345-g...), or one that is not recognised: recent
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	return major > 4 || (major == 4 && minor >= 4)
}
