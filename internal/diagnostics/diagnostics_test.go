package diagnostics

import (
	"os"
	"strings"
	"testing"
)

func TestSanitizeCredentials(t *testing.T) {
	got := Sanitize("SESSDATA=secret; bili_jct=csrf&access_key=token&key=stream")
	for _, secret := range []string{"secret", "csrf", "token", "stream"} {
		if strings.Contains(got, secret) {
			t.Fatalf("sanitized message still contains %q: %s", secret, got)
		}
	}
}

func TestLoggerUsesPrivateFile(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	logger, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	logger.Printf("access_key=secret")
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(logger.Path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("log mode = %o, want 600", info.Mode().Perm())
	}
	body, err := os.ReadFile(logger.Path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "secret") {
		t.Fatalf("log contains credential: %s", body)
	}
}

func TestOpenBoundsExistingLogSize(t *testing.T) {
	cacheDir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cacheDir)
	directory := cacheDir + "/bili-live-tui"
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := directory + "/app.log"
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maxLogBytes); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	logger, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	logger.Printf("fresh diagnostics")
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() >= maxLogBytes {
		t.Fatalf("log was not bounded: size = %d", info.Size())
	}
}
