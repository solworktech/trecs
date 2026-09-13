package main

import (
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	libtrecs "trecs/lib"
)

// MediaRecorderImpl implements MediaRecorder
type MediaRecorderImpl struct {
	mediaType  string // "audio", "camera", "screen"
	config     *libtrecs.RecordingConfig
	outputPath string
	cmd        *exec.Cmd
	running    bool
	mutex      sync.Mutex
	done       chan error
}

// NewAudioRecorder creates a new audio recorder
func NewAudioRecorder(config *libtrecs.RecordingConfig, outputPath string) (*MediaRecorderImpl, error) {
	return &MediaRecorderImpl{
		mediaType:  "audio",
		config:     config,
		outputPath: outputPath,
		done:       make(chan error, 1),
	}, nil
}

// NewCameraRecorder creates a new camera recorder
func NewCameraRecorder(config *libtrecs.RecordingConfig, outputPath string) (*MediaRecorderImpl, error) {
	return &MediaRecorderImpl{
		mediaType:  "camera",
		config:     config,
		outputPath: outputPath,
		done:       make(chan error, 1),
	}, nil
}

// NewScreenRecorder creates a new screen recorder
func NewScreenRecorder(config *libtrecs.RecordingConfig, outputPath string) (*MediaRecorderImpl, error) {
	return &MediaRecorderImpl{
		mediaType:  "screen",
		config:     config,
		outputPath: outputPath,
		done:       make(chan error, 1),
	}, nil
}

// Start begins recording
func (mr *MediaRecorderImpl) Start() error {
	mr.mutex.Lock()
	defer mr.mutex.Unlock()

	if mr.running {
		return fmt.Errorf("media recorder already running")
	}

	var args []string

	switch mr.mediaType {
	case "audio":
		args = mr.buildAudioArgs()
	case "camera":
		args = mr.buildCameraArgs()
	case "screen":
		args = mr.buildScreenArgs()
	default:
		return fmt.Errorf("unknown media type: %s", mr.mediaType)
	}

	// Append output file
	args = append(args, "-y", mr.outputPath)

	mr.cmd = exec.Command("ffmpeg", args...)

	// Redirect FFMpeg output to log files, not console
	logFile, err := os.OpenFile(mr.outputPath+".log", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("failed to create ffmpeg log file: %w", err)
	}

	mr.cmd.Stderr = logFile
	mr.cmd.Stdout = logFile

	if err := mr.cmd.Start(); err != nil {
		_ = logFile.Close()
		return fmt.Errorf("failed to start ffmpeg: %w", err)
	}

	mr.running = true

	// Wait for process in background
	go func() {
		mr.done <- mr.cmd.Wait()
		_ = logFile.Close()
	}()

	return nil
}

// buildAudioArgs constructs FFMpeg arguments for audio recording
func (mr *MediaRecorderImpl) buildAudioArgs() []string {
	args := []string{
		"-loglevel", "error",  // Only show errors, suppress all other output
	}

	// Input device
	device := mr.config.AudioDevice
	if device == "" {
		device = "default"
	}

	// Platform-specific audio input
	switch os.Getenv("GOOS") {
	case "linux":
		args = append(args, "-f", "pulse", "-i", device)
	case "darwin":
		args = append(args, "-f", "avfoundation", "-i", device)
	case "windows":
		args = append(args, "-f", "dshow", "-i", fmt.Sprintf("audio=\"%s\"", device))
	default:
		args = append(args, "-f", "pulse", "-i", device)
	}

	// Codec and bitrate
	codec := mr.config.AudioCodec
	if codec == "" {
		codec = "aac"
	}
	bitrate := mr.config.AudioBitrate
	if bitrate == "" {
		bitrate = "128k"
	}

	args = append(args, "-c:a", codec, "-b:a", bitrate)

	return args
}

// buildCameraArgs constructs FFMpeg arguments for camera recording
func (mr *MediaRecorderImpl) buildCameraArgs() []string {
	args := []string{
		"-loglevel", "error",  // Only show errors, suppress all other output
	}

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

	// Input format, device, size, and framerate
	args = append(args,
		"-f", format,
		"-video_size", size,
		"-framerate", framerate,
		"-i", device,
		"-c:v", "libx264",
		"-crf", "23",
	)

	return args
}

// buildScreenArgs constructs FFMpeg arguments for screen recording
func (mr *MediaRecorderImpl) buildScreenArgs() []string {
	args := []string{
		"-loglevel", "error",  // Only show errors, suppress all other output
	}

	display := mr.config.ScreenDisplay
	if display == "" {
		display = ":0"
	}

	format := mr.config.ScreenFormat
	if format == "" {
		// Default based on OS
		switch os.Getenv("GOOS") {
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

	// Input parameters
	args = append(args,
		"-f", format,
		"-video_size", size,
		"-framerate", framerate,
		"-i", display,
		"-c:v", "libx264",
		"-crf", "23",
		"-pix_fmt", "yuv420p",
	)

	return args
}

// Stop terminates recording
func (mr *MediaRecorderImpl) Stop() error {
	mr.mutex.Lock()
	defer mr.mutex.Unlock()

	if !mr.running {
		return nil
	}

	mr.running = false

	if mr.cmd != nil && mr.cmd.Process != nil {
		// Try graceful shutdown with SIGTERM first
		_ = mr.cmd.Process.Signal(syscall.SIGTERM)
		
		// Give it a moment to shutdown gracefully
		time.Sleep(100 * time.Millisecond)
		
		// If still running, force kill
		if mr.cmd.ProcessState == nil {
			_ = mr.cmd.Process.Kill()
		}
	}

	return nil
}

// Wait blocks until recording completes
func (mr *MediaRecorderImpl) Wait() error {
	return <-mr.done
}
