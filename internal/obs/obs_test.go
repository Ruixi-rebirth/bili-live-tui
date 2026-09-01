package obs

import (
	"strings"
	"testing"
	"time"
)

func TestApplyHealthSampleTracksOutputAndUnexpectedStop(t *testing.T) {
	runtime := NewRuntime("")
	now := time.Unix(100, 0)
	runtime.lastBytes = 1000
	runtime.lastSample = now.Add(-2 * time.Second)

	runtime.applyHealthSample(healthSample{
		active:         true,
		reconnecting:   true,
		duration:       3 * time.Second,
		bytes:          2000,
		skippedFrames:  2,
		totalFrames:    120,
		statsAvailable: true,
		fps:            60,
		cpuPercent:     12.5,
		memoryMB:       256,
	}, now)
	health := runtime.Health()
	if !health.Active || !health.Reconnecting {
		t.Fatalf("output state = active %v, reconnecting %v", health.Active, health.Reconnecting)
	}
	if health.BitrateKbps != 4 {
		t.Fatalf("bitrate = %v Kbps, want 4", health.BitrateKbps)
	}
	if health.Duration != 3*time.Second || health.SkippedFrames != 2 || health.TotalFrames != 120 {
		t.Fatalf("output counters = %#v", health)
	}
	if health.FPS != 60 || health.CPUPercent != 12.5 || health.MemoryMB != 256 {
		t.Fatalf("OBS stats = %#v", health)
	}
	select {
	case <-runtime.Done():
		t.Fatal("active output was reported as done")
	default:
	}

	runtime.applyHealthSample(healthSample{bytes: 2000}, now.Add(2*time.Second))
	health = runtime.Health()
	if health.Active || health.LastError != "OBS 推流已意外停止" {
		t.Fatalf("unexpected stop health = %#v", health)
	}
	select {
	case <-runtime.Done():
	case <-time.After(time.Second):
		t.Fatal("unexpected OBS stop did not close Done")
	}
}

func TestMarkControlConnectionErrorShowsReconnectState(t *testing.T) {
	runtime := NewRuntime("")
	runtime.health.Active = true
	if runtime.markControlConnectionErrorAt(assertError("socket closed"), time.Unix(100, 0)) {
		t.Fatal("first control error exhausted reconnect window")
	}
	health := runtime.Health()
	if !health.Active || !health.Reconnecting || health.LastError == "" {
		t.Fatalf("control reconnect health = %#v", health)
	}
}

func TestControlConnectionErrorStopsAfterReconnectWindow(t *testing.T) {
	runtime := NewRuntime("")
	runtime.health.Active = true
	started := time.Unix(100, 0)
	if runtime.markControlConnectionErrorAt(assertError("socket closed"), started) {
		t.Fatal("first control error exhausted reconnect window")
	}
	if runtime.markControlConnectionErrorAt(assertError("still closed"), started.Add(obsControlReconnectWindow-time.Second)) {
		t.Fatal("control reconnect stopped before its window elapsed")
	}
	if !runtime.markControlConnectionErrorAt(assertError("still closed"), started.Add(obsControlReconnectWindow)) {
		t.Fatal("control reconnect did not stop at its deadline")
	}
	health := runtime.Health()
	if health.Active || health.Reconnecting || !strings.Contains(health.LastError, "60 秒内未恢复") {
		t.Fatalf("timed-out control health = %#v", health)
	}
	select {
	case <-runtime.Done():
	default:
		t.Fatal("control timeout did not close Done")
	}
}

func TestSuccessfulHealthSampleResetsControlReconnectWindow(t *testing.T) {
	runtime := NewRuntime("")
	started := time.Unix(100, 0)
	runtime.health.Active = true
	runtime.markControlConnectionErrorAt(assertError("socket closed"), started)
	runtime.applyHealthSample(healthSample{active: true}, started.Add(time.Second))
	if !runtime.controlFailedAt.IsZero() {
		t.Fatalf("control failure timestamp = %v, want reset", runtime.controlFailedAt)
	}
	if runtime.markControlConnectionErrorAt(assertError("socket closed again"), started.Add(obsControlReconnectWindow+time.Second)) {
		t.Fatal("new control outage reused the previous reconnect window")
	}
}

func TestRetryOBSNotReadyEventuallySucceeds(t *testing.T) {
	attempts := 0
	err := retryOBSNotReady(3, 0, func() error {
		attempts++
		if attempts < 3 {
			return assertError("request GetStreamStatus: NotReady (207): OBS is not ready")
		}
		return nil
	})
	if err != nil || attempts != 3 {
		t.Fatalf("retry result = error %v, attempts %d", err, attempts)
	}
}

func TestRetryOBSNotReadyDoesNotHidePermanentError(t *testing.T) {
	attempts := 0
	err := retryOBSNotReady(3, 0, func() error {
		attempts++
		return assertError("authentication failed")
	})
	if err == nil || attempts != 1 {
		t.Fatalf("permanent error retry result = error %v, attempts %d", err, attempts)
	}
}

func TestRetryOBSNotReadyStopsAtLimit(t *testing.T) {
	attempts := 0
	err := retryOBSNotReady(3, 0, func() error {
		attempts++
		return assertError("request GetStreamStatus: NotReady (207)")
	})
	if err == nil || attempts != 3 || !strings.Contains(err.Error(), "NotReady (207)") {
		t.Fatalf("not-ready timeout = error %v, attempts %d", err, attempts)
	}
}

func TestOBSNotReadyClassification(t *testing.T) {
	if !isOBSNotReady(assertError("request GetStreamStatus: NotReady (207): OBS is not ready")) {
		t.Fatal("OBS NotReady response was not classified as retryable")
	}
	if isOBSNotReady(assertError("request failed: GenericError (205)")) {
		t.Fatal("permanent OBS error was classified as NotReady")
	}
}

func TestOBSStopErrorClassification(t *testing.T) {
	if !isOBSClientDisconnected(assertError("request GetStreamStatus: client already disconnected")) {
		t.Fatal("disconnected client error was not recognized")
	}
	if isOBSClientDisconnected(assertError("request timed out")) {
		t.Fatal("unrelated error was classified as disconnected")
	}
	for _, message := range []string{"request StopStream: OutputNotRunning (501)", "output is not active"} {
		if !isOBSOutputNotRunning(assertError(message)) {
			t.Fatalf("inactive output error %q was not recognized", message)
		}
	}
}

type assertError string

func (err assertError) Error() string { return string(err) }
