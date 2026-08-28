package ffmpeg

import "testing"

func TestStopBeforeStartClosesRuntime(t *testing.T) {
	r := NewTestRuntime()
	if err := r.Stop(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-r.Done():
	default:
		t.Fatal("Stop did not close Done")
	}
	if err := r.Start("", ""); err == nil {
		t.Fatal("stopped instance restarted")
	}
	if err := r.Stop(); err != nil {
		t.Fatal(err)
	}
}

func TestRepeatedStartRejectedBeforeSpawning(t *testing.T) {
	r := NewTestRuntime()
	r.started = true
	if err := r.Start("", ""); err == nil {
		t.Fatal("duplicate Start accepted")
	}
}
