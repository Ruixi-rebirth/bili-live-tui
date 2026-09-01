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
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"bili-live-tui/internal/api"
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
	obsRuntime, err := newStreamRuntime(api.LiveSettings{})
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

func TestMPVPreviewArgsReconnectLiveStream(t *testing.T) {
	args := mpvPreviewArgs("123", "https://cdn.example.com/live.m3u8?token=value")
	joined := strings.Join(args, "\n")
	for _, expected := range []string{
		"--idle=yes",
		"--loop-file=inf",
		"--cache=yes",
		"reconnect=1",
		"reconnect_at_eof=1",
		"reconnect_streamed=1",
		"--referrer=https://live.bilibili.com/123",
	} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("mpv args missing %q: %#v", expected, args)
		}
	}
}

func TestLivePlaybackURLSourceRefreshesExpiredAddress(t *testing.T) {
	loads := 0
	source := &livePlaybackURLSource{
		current:      "https://cdn.example.com/old.m3u8",
		checkedAt:    time.Now().Add(-time.Minute),
		refreshAfter: 30 * time.Second,
		load: func(context.Context) (string, error) {
			loads++
			return "https://cdn.example.com/fresh.m3u8", nil
		},
	}

	got, err := source.URL(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://cdn.example.com/fresh.m3u8" || loads != 1 {
		t.Fatalf("refreshed URL = %q, loads = %d", got, loads)
	}
	got, err = source.URL(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://cdn.example.com/fresh.m3u8" || loads != 1 {
		t.Fatalf("cached URL = %q, loads = %d", got, loads)
	}
}

func TestLivePreviewRedirectHandler(t *testing.T) {
	source := &livePlaybackURLSource{
		current:      "https://cdn.example.com/live.m3u8?token=value",
		checkedAt:    time.Now(),
		refreshAfter: time.Minute,
	}
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/secret", nil)
	recorder := httptest.NewRecorder()
	livePreviewRedirectHandler("/secret", source).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusTemporaryRedirect {
		t.Fatalf("redirect status = %d", recorder.Code)
	}
	if location := recorder.Header().Get("Location"); location != source.current {
		t.Fatalf("redirect location = %q", location)
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
	if stopped || !errors.Is(err, cause) || !strings.Contains(err.Error(), "自动下播也失败") {
		t.Fatalf("failed rollback = stopped %v, error %v", stopped, err)
	}
}
