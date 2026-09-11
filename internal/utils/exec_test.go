package utils

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
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

func TestConfigureAndResolveExecutablePath(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	executable := filepath.Join(t.TempDir(), "custom-tool")
	if err := os.WriteFile(executable, []byte("test"), 0o700); err != nil {
		t.Fatal(err)
	}
	configured, err := ConfigureExecutablePath("test-tool", "测试工具", executable)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := ResolveExecutable("test-tool", "测试工具", "missing-test-tool")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Clean(resolved) != filepath.Clean(configured) {
		t.Fatalf("resolved path = %q, configured = %q", resolved, configured)
	}
}

func TestConfigureExecutablePathAcceptsNullConfig(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "windows" {
		t.Skip("configuration isolation uses XDG_CONFIG_HOME or APPDATA")
	}
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)
	t.Setenv("APPDATA", configDir)
	path, err := executablePathsFilePath()
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteJSONAtomically(path, nil); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ConfigureExecutablePath("test-tool", "测试工具", executable); err != nil {
		t.Fatalf("cannot configure an executable after reading JSON null: %v", err)
	}
	paths, err := loadExecutablePaths()
	if err != nil || paths["test-tool"] != executable {
		t.Fatalf("configured paths = %v, error = %v", paths, err)
	}
}

func TestResolvingExecutableDoesNotCreateConfigDirectory(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "windows" {
		t.Skip("configuration isolation uses XDG_CONFIG_HOME or APPDATA")
	}
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)
	t.Setenv("APPDATA", configDir)
	_, err := ResolveExecutable("missing-tool", "缺失工具", "definitely-missing-tool")
	if !errors.Is(err, ErrExecutableNotFound) {
		t.Fatalf("expected configurable missing executable error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(configDir, "bili-live-tui")); !os.IsNotExist(err) {
		t.Fatalf("read-only executable lookup created a directory: %v", err)
	}
}

func TestNormalizeExecutablePathInput(t *testing.T) {
	want := filepath.FromSlash("some directory/tool")
	for _, input := range []string{
		"  some directory/tool  ",
		`"some directory/tool"`,
		`'some directory/tool'`,
	} {
		if got := normalizeExecutablePathInput(input); got != want {
			t.Errorf("normalizeExecutablePathInput(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestResolveExecutableReturnsConfigurableError(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	_, err := ResolveExecutable("missing-tool", "缺失工具", "definitely-missing-tool")
	var missing *ExecutableNotFoundError
	if !errors.As(err, &missing) {
		t.Fatalf("error = %v, want ExecutableNotFoundError", err)
	}
	if missing.Key != "missing-tool" || missing.DisplayName != "缺失工具" {
		t.Fatalf("missing executable = %#v", missing)
	}
}

func TestExecutableProbeAcceptsExpectedProgram(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	executable := filepath.Join(t.TempDir(), "expected-tool")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nprintf 'expected tool version 1.0\\n'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := ConfigureExecutablePath("expected-tool", "Expected Tool", executable, ExecutableProbe{
		Args:           []string{"--version"},
		OutputContains: "expected tool version",
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestExecutableProbeRejectsWrongProgram(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	executable := filepath.Join(t.TempDir(), "wrong-tool")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nprintf 'different program\\n'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := ConfigureExecutablePath("expected-tool", "Expected Tool", executable, ExecutableProbe{
		Args:           []string{"--version"},
		OutputContains: "expected tool version",
	})
	if err == nil || !strings.Contains(err.Error(), "不是有效的 Expected Tool") {
		t.Fatalf("probe error = %v", err)
	}
}
