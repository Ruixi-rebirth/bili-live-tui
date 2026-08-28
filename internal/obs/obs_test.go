package obs

import (
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
