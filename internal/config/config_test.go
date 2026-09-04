package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bili-live-tui/internal/api"
)

func TestSaveAuthUsesPrivatePermissions(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	poll := &api.TVQRPollResponse{}
	poll.Data.AccessToken = "token"
	poll.Data.CookieInfo.Cookies = append(poll.Data.CookieInfo.Cookies,
		struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		}{Name: "SESSDATA", Value: "sess"},
		struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		}{Name: "bili_jct", Value: "jct"},
	)
	if err := SaveAuth(poll); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(configHome, AppName, AuthFileName)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("auth file mode = %o, want 600", got)
	}
	dirInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o700 {
		t.Fatalf("auth directory mode = %o, want 700", got)
	}
	auth, err := LoadAuth()
	if err != nil {
		t.Fatal(err)
	}
	if auth.AccessToken != "token" || auth.SESSDATA != "sess" || auth.BiliJCT != "jct" {
		t.Fatalf("loaded auth = %#v", auth)
	}
}

func TestAuthDataValidationRejectsIncompleteCredentials(t *testing.T) {
	if err := (AuthData{AccessToken: "token", SESSDATA: "sess", BiliJCT: "jct"}).Validate(); err != nil {
		t.Fatalf("complete credentials rejected: %v", err)
	}
	if err := (AuthData{AccessToken: "token", SESSDATA: "sess"}).Validate(); err == nil {
		t.Fatal("credentials without bili_jct were accepted")
	}
}

func TestSaveAuthRejectsIncompleteLoginResponse(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	poll := &api.TVQRPollResponse{}
	poll.Data.AccessToken = "token"
	if err := SaveAuth(poll); err == nil {
		t.Fatal("incomplete login response was saved")
	}
}

func TestSaveAuthRejectsNilResponse(t *testing.T) {
	if err := SaveAuth(nil); err == nil {
		t.Fatal("nil login response was accepted")
	}
}

func TestSaveAndLoadLiveSettings(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	want := api.LiveSettings{
		Title:       "下次继续直播",
		Description: "直播简介",
		Tags:        "游戏,聊天",
		AreaID:      "376",
		CoverPath:   "https://example.com/cover.webp",
		StreamMode:  "obs",
		OBSHost:     "192.0.2.10",
		OBSPort:     "4456",
		OBSPassword: "private-password",
	}
	if err := SaveLiveSettings(want); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(configHome, AppName, LiveSettingsFileName)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("live settings mode = %o, want 600", got)
	}
	got, err := LoadLiveSettings()
	if err != nil {
		t.Fatal(err)
	}
	if *got != want {
		t.Fatalf("loaded settings = %#v, want %#v", *got, want)
	}
}

func TestAtomicJSONWritePreservesExistingFileOnEncodeFailure(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "settings.json")
	const original = "原有配置\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	err := writeJSONAtomically(path, map[string]any{"unsupported": make(chan int)})
	if err == nil {
		t.Fatal("unsupported JSON value was saved")
	}
	content, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(content) != original {
		t.Fatalf("existing file changed after failed save: %q", content)
	}
	entries, readDirErr := os.ReadDir(directory)
	if readDirErr != nil {
		t.Fatal(readDirErr)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".settings.json.tmp-") {
			t.Fatalf("temporary file was not cleaned up: %s", entry.Name())
		}
	}
}
