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

func TestReadProgressSignalsConfirmedOutput(t *testing.T) {
	runtime := NewTestRuntime()
	ready := make(chan struct{})
	runtime.readProgressWithReady(strings.NewReader("frame=1\n"), ready)
	select {
	case <-ready:
	default:
		t.Fatal("FFmpeg output progress was not confirmed")
	}
}

func TestFFmpegReconnectDelayStopsGrowing(t *testing.T) {
	want := []time.Duration{2 * time.Second, 5 * time.Second, 10 * time.Second, 15 * time.Second, 15 * time.Second}
	for attempt, expected := range want {
		if got := ffmpegReconnectDelay(attempt); got != expected {
			t.Fatalf("reconnect delay %d = %v, want %v", attempt, got, expected)
		}
	}
}

func TestTestRuntimeKeepsSanitizedFFmpegFailureDetail(t *testing.T) {
	runtime := NewTestRuntime()
	runtime.readProgress(strings.NewReader("[flv @ 0x1] Error opening output rtmp://example/live/?key=secret: Input/output error\nConversion failed!\n"))
	if strings.Contains(runtime.lastLog, "secret") || !strings.Contains(runtime.lastLog, "[REDACTED]") || !strings.Contains(runtime.lastLog, "Input/output error") {
		t.Fatalf("sanitized FFmpeg detail = %q", runtime.lastLog)
	}
}

func TestReadProgressDoesNotTreatProgressMarkerAsError(t *testing.T) {
	runtime := NewTestRuntime()
	runtime.readProgress(strings.NewReader("progress=continue\nprogress=end\n"))
	if runtime.lastLog != "" {
		t.Fatalf("progress marker became error detail: %q", runtime.lastLog)
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

func TestTestRuntimeHealthDetectsProgressStall(t *testing.T) {
	runtime := NewTestRuntime()
	runtime.health.Active = true
	runtime.health.BitrateKbps = 3000
	runtime.health.FPS = 30
	runtime.lastProgressAt = time.Now().Add(-5 * time.Second)

	health := runtime.Health()
	if health.Active {
		t.Fatal("health should not be active when progress stalled")
	}
	if !health.Reconnecting {
		t.Fatal("health should be reconnecting when progress stalled")
	}
	if health.BitrateKbps != 0 || health.FPS != 0 {
		t.Fatalf("stalled bitrate/fps should be 0: %#v", health)
	}
	if !strings.Contains(health.LastError, "推流数据中断") {
		t.Fatalf("stalled last error = %q", health.LastError)
	}
}

func TestFFmpegStreamArgsIncludesGOPAndBitrateControl(t *testing.T) {
	args := ffmpegStreamArgs("", "rtmp://live/", "secret")
	joined := strings.Join(args, " ")
	for _, required := range []string{
		"-g 60",
		"-keyint_min 60",
		"-sc_threshold 0",
		"-b:v 2500k",
		"-maxrate 3000k",
		"-bufsize 6000k",
		"-tune zerolatency",
		"-rw_timeout 15000000",
		"-flvflags no_duration_filesize",
		"-f flv rtmp://live/secret",
	} {
		if !strings.Contains(joined, required) {
			t.Errorf("FFmpeg args missing %q; got: %s", required, joined)
		}
	}
}

func TestFormatFFmpegExitMessageWith187And10053(t *testing.T) {
	msg1 := formatFFmpegExitMessage(nil, "[vost#0:0/libx264] Error submitting a packet to the muxer: Error number -10053 occurred")
	if !strings.Contains(msg1, "WSAECONNABORTED") {
		t.Errorf("expected WSAECONNABORTED in msg1, got: %s", msg1)
	}

	fakeErr := &fakeExitError{msg: "exit status 187"}
	msg2 := formatFFmpegExitMessage(fakeErr, "Connection reset")
	if strings.Contains(msg2, "WSAECONNABORTED") || !strings.Contains(msg2, "exit status 187") {
		t.Errorf("exit status alone must not imply a Winsock error, got: %s", msg2)
	}
}

type fakeExitError struct {
	msg string
}

func (f *fakeExitError) Error() string { return f.msg }
