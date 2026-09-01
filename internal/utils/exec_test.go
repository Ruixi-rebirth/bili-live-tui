package utils

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGetExecutablePathFindsCurrentTestBinary(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Dir(executable)+string(os.PathListSeparator)+os.Getenv("PATH"))

	path, err := GetExecutablePath(filepath.Base(executable))
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Clean(path) != filepath.Clean(executable) {
		t.Fatalf("path = %q, want %q", path, executable)
	}
}

func TestGetExecutablePathReportsMissingExecutable(t *testing.T) {
	_, err := GetExecutablePath("missing-one", "missing-two")
	if !errors.Is(err, ErrExecutableNotFound) {
		t.Fatalf("error = %v, want ErrExecutableNotFound", err)
	}
	if !strings.Contains(err.Error(), "[missing-one, missing-two]") {
		t.Fatalf("error = %v, want executable names", err)
	}
}
