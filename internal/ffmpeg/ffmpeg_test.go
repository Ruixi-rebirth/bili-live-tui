package ffmpeg

import (
	"strings"
	"testing"
	"time"
)

func TestTestRuntimeParsesFFmpegProgress(t *testing.T) {
	runtime := NewTestRuntime()
	runtime.readProgress(strings.NewReader(strings.Join([]string{
		"frame=90",
		"fps=29.97",
		"bitrate=2450.5kbits/s",
		"drop_frames=2",
		"out_time_ms=3000000",
	}, "\n")))
	health := runtime.Health()
	if health.TotalFrames != 90 || health.SkippedFrames != 2 {
		t.Fatalf("frame health = %#v", health)
	}
	if health.FPS != 29.97 || health.BitrateKbps != 2450.5 {
		t.Fatalf("rate health = %#v", health)
	}
	if health.Duration != 3*time.Second {
		t.Fatalf("duration = %v, want 3s", health.Duration)
	}
}

func TestTestRuntimeKeepsSanitizedFFmpegFailureDetail(t *testing.T) {
	runtime := NewTestRuntime()
	runtime.readProgress(strings.NewReader("[flv @ 0x1] Error opening output rtmp://example/live/?key=secret: Input/output error\n"))
	if strings.Contains(runtime.lastLog, "secret") || !strings.Contains(runtime.lastLog, "[REDACTED]") || !strings.Contains(runtime.lastLog, "Input/output error") {
		t.Fatalf("sanitized FFmpeg detail = %q", runtime.lastLog)
	}
}

func TestTestSourceArgsFollowOrientation(t *testing.T) {
	landscape := strings.Join(testSourceArgs(""), " ")
	portrait := strings.Join(testSourceArgs(streamruntimeOrientationPortrait), " ")
	if !strings.Contains(landscape, "size=1280x720") || strings.Contains(landscape, "size=720x1280") {
		t.Fatalf("landscape source = %q", landscape)
	}
	if !strings.Contains(portrait, "size=720x1280") || strings.Contains(portrait, "size=1280x720") {
		t.Fatalf("portrait source = %q", portrait)
	}
}
