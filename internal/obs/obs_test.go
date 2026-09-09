package obs

import (
	"net"
	"strings"
	"testing"
	"time"
)

func TestOutputHasOBSProcess(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   bool
	}{
		{name: "Windows 64-bit", output: `"obs64.exe","1234","Console","1","100,000 K"`, want: true},
		{name: "Windows 32-bit", output: `"obs32.exe","1234","Console","1","100,000 K"`, want: true},
		{name: "Linux", output: "/usr/bin/obs\n", want: true},
		{name: "unrelated", output: `"notepad.exe","1234","Console","1","1,000 K"`, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := outputHasOBSProcess(test.output); got != test.want {
				t.Fatalf("outputHasOBSProcess() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestRuntimeOBSWebSocketPort(t *testing.T) {
	if runtime := NewRuntime("", "", "password"); runtime.host != DefaultHost || runtime.port != DefaultPort {
		t.Fatalf("default endpoint = %s, want %s", net.JoinHostPort(runtime.host, runtime.port), net.JoinHostPort(DefaultHost, DefaultPort))
	}
	if got := NewRuntime("192.0.2.10", "4456", "password").port; got != "4456" {
		t.Fatalf("custom port = %q, want 4456", got)
	}
	if got := NewRuntime("192.0.2.10", "4456", "password").host; got != "192.0.2.10" {
		t.Fatalf("custom host = %q, want 192.0.2.10", got)
	}
}

func TestOBSHostNormalizationAndLocalDetection(t *testing.T) {
	for _, host := range []string{"", "localhost", "127.0.0.1", "::1", "[::1]"} {
		if !isLocalOBSHost(host) {
			t.Errorf("isLocalOBSHost(%q) = false, want true", host)
		}
	}
	for _, host := range []string{"192.0.2.10", "obs.example.test"} {
		if isLocalOBSHost(host) {
			t.Errorf("isLocalOBSHost(%q) = true, want false", host)
		}
	}
}

func TestApplyHealthSampleTracksOutputAndUnexpectedStop(t *testing.T) {
	runtime := NewRuntime("", "", "")
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

	runtime.applyHealthSample(healthSample{reconnecting: true, bytes: 2000}, now.Add(time.Second))
	health = runtime.Health()
	if health.Active || !health.Reconnecting || !strings.Contains(health.LastError, "正在重连") {
		t.Fatalf("reconnecting output health = %#v", health)
	}
	select {
	case <-runtime.Done():
		t.Fatal("reconnecting OBS output was reported as done")
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
	runtime := NewRuntime("", "", "")
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
	runtime := NewRuntime("", "", "")
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
	runtime := NewRuntime("", "", "")
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

func TestOutputReconnectStopsAfterWindow(t *testing.T) {
	runtime := NewRuntime("", "", "")
	started := time.Unix(100, 0)
	runtime.applyHealthSample(healthSample{reconnecting: true}, started)
	select {
	case <-runtime.Done():
		t.Fatal("OBS reconnect stopped before its window elapsed")
	default:
	}
	runtime.applyHealthSample(healthSample{reconnecting: true}, started.Add(obsOutputReconnectWindow-time.Second))
	select {
	case <-runtime.Done():
		t.Fatal("OBS reconnect stopped before its deadline")
	default:
	}
	runtime.applyHealthSample(healthSample{reconnecting: true}, started.Add(obsOutputReconnectWindow))
	health := runtime.Health()
	if health.Active || health.Reconnecting || !strings.Contains(health.LastError, "60 秒内未恢复") {
		t.Fatalf("timed-out output health = %#v", health)
	}
	select {
	case <-runtime.Done():
	default:
		t.Fatal("output reconnect timeout did not close Done")
	}
}

func TestSuccessfulOutputResetsReconnectWindow(t *testing.T) {
	runtime := NewRuntime("", "", "")
	started := time.Unix(100, 0)
	runtime.applyHealthSample(healthSample{reconnecting: true}, started)
	runtime.applyHealthSample(healthSample{active: true}, started.Add(30*time.Second))
	if !runtime.outputReconnectAt.IsZero() {
		t.Fatalf("output reconnect timestamp = %v, want reset", runtime.outputReconnectAt)
	}
	runtime.applyHealthSample(healthSample{reconnecting: true}, started.Add(obsOutputReconnectWindow+time.Second))
	select {
	case <-runtime.Done():
		t.Fatal("new output reconnect reused the previous reconnect window")
	default:
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

func TestOBSOutputRunningIncludesAutomaticReconnect(t *testing.T) {
	for _, state := range []struct {
		active       bool
		reconnecting bool
		want         bool
	}{
		{active: true, want: true},
		{reconnecting: true, want: true},
		{active: true, reconnecting: true, want: true},
		{},
	} {
		if got := isOBSOutputRunning(state.active, state.reconnecting); got != state.want {
			t.Fatalf("isOBSOutputRunning(%v, %v) = %v, want %v", state.active, state.reconnecting, got, state.want)
		}
	}
}

type assertError string

func (err assertError) Error() string { return string(err) }

func TestApplyHealthSampleDetectsStalledUpload(t *testing.T) {
	runtime := NewRuntime("", "", "")
	start := time.Unix(100, 0)
	runtime.lastBytes = 1000
	runtime.lastSample = start

	// 首个有效样本建立推流活动
	runtime.applyHealthSample(healthSample{
		active:   true,
		duration: 5 * time.Second,
		bytes:    2000,
	}, start.Add(2*time.Second))

	if runtime.Health().BitrateKbps <= 0 || runtime.Health().LastError != "" {
		t.Fatalf("initial sample should be normal: %#v", runtime.Health())
	}

	// 模拟断网：数据量停止增长超过 4 秒
	runtime.applyHealthSample(healthSample{
		active:   true,
		duration: 7 * time.Second,
		bytes:    2000,
	}, start.Add(4*time.Second))

	runtime.applyHealthSample(healthSample{
		active:   true,
		duration: 9 * time.Second,
		bytes:    2000,
	}, start.Add(6*time.Second))

	runtime.applyHealthSample(healthSample{
		active:   true,
		duration: 11 * time.Second,
		bytes:    2000,
	}, start.Add(8*time.Second))

	health := runtime.Health()
	if health.BitrateKbps != 0 {
		t.Fatalf("stalled bitrate = %v, want 0", health.BitrateKbps)
	}
	if !strings.Contains(health.LastError, "网络可能已中断") {
		t.Fatalf("stalled last error = %q, want network interrupt message", health.LastError)
	}
}
