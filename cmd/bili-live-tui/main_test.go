package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"bili-live-tui/internal/api"
	"bili-live-tui/internal/config"
	streamruntime "bili-live-tui/internal/stream"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestUploadCoverReportsUnavailableUploadEndpoint(t *testing.T) {
	const imageURL = "https://apis.klrvc.com/wp-content/uploads/2026/05/08b6353bd520260525230415.webp"
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.Method {
		case http.MethodGet:
			var data bytes.Buffer
			if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 640, 360))); err != nil {
				return nil, err
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewReader(data.Bytes())),
				Header:     http.Header{"Content-Type": []string{"image/webp"}},
			}, nil
		case http.MethodPost:
			return &http.Response{
				StatusCode: http.StatusNotFound,
				Body:       io.NopCloser(strings.NewReader("cover endpoint unavailable")),
				Header:     make(http.Header),
			}, nil
		default:
			t.Fatalf("unexpected method %s", r.Method)
			return nil, nil
		}
	})
	client := api.NewClient(&http.Client{Transport: transport})
	client.BaseURL = "http://test.invalid"
	got, err := uploadCover(context.Background(), client, "1", "sess", "jct", imageURL)
	if err == nil {
		t.Fatalf("uploadCover() unexpectedly succeeded with %q", got)
	}
	if !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("uploadCover() error = %v, want HTTP 404", err)
	}
}

func TestNewStreamRuntimeSelection(t *testing.T) {
	obsRuntime, err := newStreamRuntime(api.LiveSettings{OBSHost: "192.0.2.10", OBSPort: "4456"})
	if err != nil {
		t.Fatal(err)
	}
	if got := obsRuntime.Health().Mode; got != streamruntime.ModeOBS {
		t.Fatalf("default runtime mode = %q", got)
	}
	testRuntime, err := newStreamRuntime(api.LiveSettings{StreamMode: streamruntime.ModeFFmpegTest})
	if err != nil {
		t.Fatal(err)
	}
	if got := testRuntime.Health().Mode; got != streamruntime.ModeFFmpegTest {
		t.Fatalf("test runtime mode = %q", got)
	}
	if _, err := newStreamRuntime(api.LiveSettings{StreamMode: "unknown"}); err == nil {
		t.Fatal("unknown stream mode accepted")
	}
}

func TestAuthenticationErrorClassification(t *testing.T) {
	for _, message := range []string{"请求失败: 账号未登录", "token错误", "B站错误 65530"} {
		if !isAuthenticationError(fmt.Errorf("%s", message)) {
			t.Fatalf("authentication error %q was not recognized", message)
		}
	}
	if isAuthenticationError(fmt.Errorf("网络请求超时")) {
		t.Fatal("network timeout was misclassified as expired credentials")
	}
}

func TestEnsureRoomCanStartRejectsExistingLiveSession(t *testing.T) {
	if err := ensureRoomCanStart(api.RoomSnapshot{LiveStatus: 1}); err == nil || !strings.Contains(err.Error(), "已经开播") {
		t.Fatalf("live room preflight error = %v", err)
	}
	for _, status := range []int{0, 2} {
		if err := ensureRoomCanStart(api.RoomSnapshot{LiveStatus: status}); err != nil {
			t.Fatalf("room status %d was rejected: %v", status, err)
		}
	}
}

func TestLiveSettingsFromSnapshotPreservesEditableRoomFields(t *testing.T) {
	snapshot := api.RoomSnapshot{
		Title:       "已有直播",
		Description: "直播简介",
		Tags:        "游戏,聊天",
		AreaID:      "376",
		Cover:       "https://i.example/cover.jpg",
	}
	settings := liveSettingsFromSnapshot(snapshot)
	if settings.Title != snapshot.Title || settings.Description != snapshot.Description ||
		settings.Tags != snapshot.Tags || settings.AreaID != snapshot.AreaID || settings.CoverPath != snapshot.Cover {
		t.Fatalf("settings from room snapshot = %#v", settings)
	}
}

func TestWatchStreamOutputCancelsOnUnexpectedStop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	observed := make(chan struct{})
	go watchStreamOutput(ctx, done, func() { close(observed) })
	close(done)

	select {
	case <-observed:
	case <-time.After(time.Second):
		t.Fatal("unexpected output stop was not observed")
	}
}

func TestWatchStreamOutputIgnoresSessionShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	returned := make(chan struct{})
	called := make(chan struct{}, 1)
	go func() {
		watchStreamOutput(ctx, done, func() { called <- struct{}{} })
		close(returned)
	}()
	cancel()

	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("stream watcher did not return with the session")
	}
	select {
	case <-called:
		t.Fatal("normal session shutdown was reported as unexpected")
	default:
	}
}

func TestMPVPreviewArgsPlayBilibiliURLDirectly(t *testing.T) {
	args := mpvPreviewArgs("123", "https://cdn.example.com/live.m3u8?token=value")
	joined := strings.Join(args, "\n")
	for _, expected := range []string{
		"--force-window=immediate",
		"--terminal=no",
		"--no-config",
		"--cache=no",
		"--demuxer-readahead-secs=0",
		"--demuxer-lavf-analyzeduration=1",
		"--demuxer-lavf-probesize=1048576",
		"--stream-lavf-o=fflags=+nobuffer,reconnect=1,reconnect_at_eof=1,reconnect_streamed=1,reconnect_delay_max=5",
		"--referrer=https://live.bilibili.com/123",
		"https://cdn.example.com/live.m3u8?token=value",
	} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("mpv args missing %q: %#v", expected, args)
		}
	}
}

func TestLastMPVErrorLineRedactsPlaybackURL(t *testing.T) {
	got := lastMPVErrorLine("Failed to open https://cdn.example.com/live.m3u8?token=secret\nExiting... (Errors when loading file)\n")
	if strings.Contains(got, "secret") || !strings.Contains(got, "[播放地址]") {
		t.Fatalf("mpv error = %q", got)
	}
}

func TestTailBufferKeepsBoundedSuffix(t *testing.T) {
	buffer := &tailBuffer{limit: 5}
	_, _ = buffer.Write([]byte("1234"))
	_, _ = buffer.Write([]byte("567"))
	if got := buffer.String(); got != "34567" {
		t.Fatalf("tail buffer = %q", got)
	}
}

func TestWaitForContextStopsImmediatelyOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	started := time.Now()
	if err := waitForContext(ctx, time.Minute); !errors.Is(err, context.Canceled) {
		t.Fatalf("waitForContext() error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("cancelled wait took %v", elapsed)
	}
}

func TestRollbackLiveStartReportsWhetherPlatformStopped(t *testing.T) {
	cause := errors.New("local output failed")
	client := api.NewClient(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"code":0,"message":"0"}`)), Header: make(http.Header)}, nil
	})})
	client.BaseURL = "http://test.invalid"
	stopped, err := rollbackLiveStart(client, "1", "token", cause)
	if !stopped || !errors.Is(err, cause) {
		t.Fatalf("successful rollback = stopped %v, error %v", stopped, err)
	}

	client.HTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusBadGateway, Body: io.NopCloser(strings.NewReader("upstream unavailable")), Header: make(http.Header)}, nil
	})}
	stopped, err = rollbackLiveStart(client, "1", "token", cause)
	if stopped || !errors.Is(err, cause) || !strings.Contains(err.Error(), "自动下播失败") {
		t.Fatalf("failed rollback = stopped %v, error %v", stopped, err)
	}
}

func TestSyncLiveTagsHandlesNullTagIDsJSON(t *testing.T) {
	client := api.NewClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"code":0,"data":{"tag_id":10086}}`)),
			Header:     make(http.Header),
		}, nil
	})})
	client.BaseURL = "http://test.invalid"

	settings := &api.LiveSettings{
		Tags:       "单机游戏",
		TagIDsJSON: "null",
	}
	auth := &config.AuthData{
		SESSDATA: "sess",
		BiliJCT:  "jct",
	}

	if err := syncLiveTags(context.Background(), client, "123", auth, "", settings); err != nil {
		t.Fatalf("syncLiveTags error with null TagIDsJSON: %v", err)
	}
	if !strings.Contains(settings.TagIDsJSON, "10086") {
		t.Fatalf("expected TagIDsJSON to contain 10086, got %q", settings.TagIDsJSON)
	}
}

func TestRootCmdStructureAndCompletions(t *testing.T) {
	cmd := newRootCmd()
	if cmd == nil {
		t.Fatal("newRootCmd() returned nil")
	}
	cmd.InitDefaultCompletionCmd()

	subCmds := map[string]bool{}
	for _, c := range cmd.Commands() {
		subCmds[c.Name()] = true
	}
	for _, expected := range []string{"status", "stop", "logout", "completion"} {
		if !subCmds[expected] {
			t.Errorf("missing expected subcommand: %s", expected)
		}
	}

	if flag := cmd.PersistentFlags().Lookup("no-save-auth"); flag == nil {
		t.Error("missing persistent flag --no-save-auth")
	}

	for _, shell := range []string{"bash", "zsh", "fish"} {
		var buf bytes.Buffer
		testCmd := newRootCmd()
		var err error
		switch shell {
		case "bash":
			err = testCmd.GenBashCompletionV2(&buf, true)
		case "zsh":
			err = testCmd.GenZshCompletion(&buf)
		case "fish":
			err = testCmd.GenFishCompletion(&buf, true)
		}
		if err != nil {
			t.Errorf("completion %s failed: %v", shell, err)
		}
		if buf.Len() == 0 {
			t.Errorf("completion %s generated empty output", shell)
		}
	}
}

func TestRunLogout(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)

	// Logout when not logged in
	if err := runLogout(); err != nil {
		t.Fatalf("runLogout() failed: %v", err)
	}

	// Create dummy auth file
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
	if err := config.SaveAuth(poll); err != nil {
		t.Fatal(err)
	}

	// Logout when logged in
	if err := runLogout(); err != nil {
		t.Fatalf("runLogout() failed after SaveAuth: %v", err)
	}

	if _, err := config.LoadAuth(); err == nil {
		t.Fatal("LoadAuth succeeded after runLogout")
	}
}
