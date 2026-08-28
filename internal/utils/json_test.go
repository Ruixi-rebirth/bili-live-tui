package utils

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteJSONAtomicallyAndReadJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "test.json")

	type testStruct struct {
		Name  string `json:"name"`
		Value int    `json:"value"`
	}

	want := testStruct{Name: "hello", Value: 42}
	if err := WriteJSONAtomically(path, want); err != nil {
		t.Fatalf("WriteJSONAtomically failed: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("file perm = %o, want 600", got)
	}

	var got testStruct
	if err := ReadJSON(path, &got); err != nil {
		t.Fatalf("ReadJSON failed: %v", err)
	}
	if got != want {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestWriteJSONAtomicallyFailureCleansUp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.json")
	const orig = "original"
	if err := os.WriteFile(path, []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}

	// Channel cannot be serialized to JSON
	err := WriteJSONAtomically(path, map[string]any{"bad": make(chan int)})
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != orig {
		t.Fatalf("file corrupted: %s", string(data))
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".test.json.tmp-") {
			t.Fatalf("temp file was not cleaned up: %s", e.Name())
		}
	}
}
