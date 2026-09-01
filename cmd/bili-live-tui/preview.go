package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"

	"bili-live-tui/internal/api"
)

const (
	previewPlaybackURLRefreshInterval = 30 * time.Second
	previewPlaybackURLRequestTimeout  = 8 * time.Second
)

type livePreviewer struct {
	mu      sync.Mutex
	running bool
}

// Start 打开一个 mpv 预览窗口。mpv 始终播放本机回环地址；媒体连接断开并重试时，
// 回环处理器会重新获取 B 站签名地址，避免旧地址失效后播放器直接退出。
func (previewer *livePreviewer) Start(ctx context.Context, client *api.Client, roomID, sessdata, biliJCT string) (err error) {
	previewer.mu.Lock()
	if previewer.running {
		previewer.mu.Unlock()
		return nil
	}
	previewer.running = true
	previewer.mu.Unlock()
	started := false
	defer func() {
		if started {
			return
		}
		previewer.mu.Lock()
		previewer.running = false
		previewer.mu.Unlock()
	}()

	player, err := findMPV()
	if err != nil {
		return err
	}
	requestCtx, cancelRequest := context.WithTimeout(ctx, previewPlaybackURLRequestTimeout)
	playbackURL, err := client.GetRoomPlaybackURL(requestCtx, roomID, sessdata, biliJCT)
	cancelRequest()
	if err != nil {
		return err
	}
	source := &livePlaybackURLSource{
		current:      playbackURL,
		checkedAt:    time.Now(),
		refreshAfter: previewPlaybackURLRefreshInterval,
		load: func(loadCtx context.Context) (string, error) {
			requestCtx, cancelRequest := context.WithTimeout(loadCtx, previewPlaybackURLRequestTimeout)
			defer cancelRequest()
			return client.GetRoomPlaybackURL(requestCtx, roomID, sessdata, biliJCT)
		},
	}
	previewURL, closeRedirector, err := startLivePreviewRedirector(source)
	if err != nil {
		return err
	}
	command := exec.CommandContext(ctx, player, mpvPreviewArgs(roomID, previewURL)...)
	if err := command.Start(); err != nil {
		_ = closeRedirector()
		return fmt.Errorf("启动 mpv 失败: %w", err)
	}
	started = true
	waitDone := make(chan error, 1)
	go func() {
		waitErr := command.Wait()
		_ = closeRedirector()
		previewer.mu.Lock()
		previewer.running = false
		previewer.mu.Unlock()
		waitDone <- waitErr
	}()
	startupTimer := time.NewTimer(750 * time.Millisecond)
	defer startupTimer.Stop()
	select {
	case waitErr := <-waitDone:
		if waitErr != nil {
			return fmt.Errorf("mpv 启动后立即退出: %w", waitErr)
		}
		return fmt.Errorf("mpv 启动后立即退出")
	case <-startupTimer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type livePlaybackURLSource struct {
	mu           sync.Mutex
	current      string
	checkedAt    time.Time
	refreshAfter time.Duration
	load         func(context.Context) (string, error)
}

func (source *livePlaybackURLSource) URL(ctx context.Context) (string, error) {
	source.mu.Lock()
	defer source.mu.Unlock()

	now := time.Now()
	if source.current != "" && now.Sub(source.checkedAt) < source.refreshAfter {
		return source.current, nil
	}
	source.checkedAt = now
	if source.load == nil {
		if source.current != "" {
			return source.current, nil
		}
		return "", fmt.Errorf("直播预览没有可用的播放地址")
	}
	fresh, err := source.load(ctx)
	if err != nil {
		// 短暂无法刷新时保留上一个地址，让 mpv 自身的传输层重连仍有机会恢复。
		if source.current != "" {
			return source.current, nil
		}
		return "", err
	}
	fresh = strings.TrimSpace(fresh)
	if fresh == "" {
		if source.current != "" {
			return source.current, nil
		}
		return "", fmt.Errorf("直播预览刷新后没有可用的播放地址")
	}
	source.current = fresh
	return fresh, nil
}

func startLivePreviewRedirector(source *livePlaybackURLSource) (string, func() error, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, fmt.Errorf("启动直播预览回环服务失败: %w", err)
	}
	tokenBytes := make([]byte, 16)
	if _, err := rand.Read(tokenBytes); err != nil {
		_ = listener.Close()
		return "", nil, fmt.Errorf("准备直播预览回环地址失败: %w", err)
	}
	path := "/" + hex.EncodeToString(tokenBytes)
	server := &http.Server{
		Handler:           livePreviewRedirectHandler(path, source),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		_ = server.Serve(listener)
	}()
	return "http://" + listener.Addr().String() + path, server.Close, nil
}

func livePreviewRedirectHandler(path string, source *livePlaybackURLSource) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != path {
			http.NotFound(writer, request)
			return
		}
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			writer.Header().Set("Allow", http.MethodGet+", "+http.MethodHead)
			http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		playbackURL, err := source.URL(request.Context())
		if err != nil {
			http.Error(writer, "live preview temporarily unavailable", http.StatusBadGateway)
			return
		}
		writer.Header().Set("Cache-Control", "no-store")
		http.Redirect(writer, request, playbackURL, http.StatusTemporaryRedirect)
	})
}

func mpvPreviewArgs(roomID, playbackURL string) []string {
	return []string{
		"--force-window=immediate",
		"--idle=yes",
		"--loop-file=inf",
		"--cache=yes",
		"--stream-lavf-o=reconnect=1,reconnect_at_eof=1,reconnect_streamed=1,reconnect_delay_max=5",
		"--mute=yes",
		"--title=bili-live-tui 直播预览",
		"--referrer=https://live.bilibili.com/" + strings.TrimSpace(roomID),
		"--",
		playbackURL,
	}
}

func findMPV() (string, error) {
	path, err := exec.LookPath("mpv")
	if err == nil {
		return path, nil
	}
	return "", fmt.Errorf("未找到当前系统的 mpv，请先安装原生版本并确保 mpv 在 PATH 中")
}
