package main

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"bili-live-tui/internal/api"
	"bili-live-tui/internal/utils"
)

const (
	previewURLTimeout    = 8 * time.Second
	previewReadyTimeout  = 45 * time.Second
	previewProbeTimeout  = 8 * time.Second
	previewRetryInterval = time.Second
	mpvStartupGrace      = 1200 * time.Millisecond
	mpvOutputTailBytes   = 8 * 1024
	mpvFrameReadyMarker  = "bili-live-tui-video-frame-ready"
)

var previewURLPattern = regexp.MustCompile(`https?://\S+`)

type livePreviewer struct {
	mu      sync.Mutex
	running bool
}

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
		previewer.setRunning(false)
	}()

	player, err := utils.GetExecutablePath("mpv.exe", "mpv")
	if err != nil {
		return err
	}
	playbackURL, err := waitForLiveFrame(ctx, player, roomID, func(loadCtx context.Context) (string, error) {
		requestCtx, cancelRequest := context.WithTimeout(loadCtx, previewURLTimeout)
		defer cancelRequest()
		return client.GetRoomPlaybackURL(requestCtx, roomID, sessdata, biliJCT)
	})
	if err != nil {
		return err
	}

	output := &tailBuffer{limit: mpvOutputTailBytes}
	command := exec.CommandContext(ctx, player, mpvPreviewArgs(roomID, playbackURL)...)
	command.Stdout = output
	command.Stderr = output
	if err := command.Start(); err != nil {
		return fmt.Errorf("启动 mpv 失败: %w", err)
	}
	started = true
	exited := make(chan error, 1)
	go func() {
		waitErr := command.Wait()
		previewer.setRunning(false)
		exited <- waitErr
	}()

	timer := time.NewTimer(mpvStartupGrace)
	defer timer.Stop()
	select {
	case waitErr := <-exited:
		return formatMPVStartError(waitErr, output.String())
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (previewer *livePreviewer) setRunning(running bool) {
	previewer.mu.Lock()
	previewer.running = running
	previewer.mu.Unlock()
}

func waitForLiveFrame(ctx context.Context, player, roomID string, loadURL func(context.Context) (string, error)) (string, error) {
	waitCtx, cancelWait := context.WithTimeout(ctx, previewReadyTimeout)
	defer cancelWait()
	var lastErr error
	for {
		playbackURL, err := loadURL(waitCtx)
		if err == nil {
			err = probeMPVFrame(waitCtx, player, roomID, playbackURL)
			if err == nil {
				return playbackURL, nil
			}
		}
		lastErr = err
		if err := waitForContext(waitCtx, previewRetryInterval); err == nil {
			continue
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if lastErr != nil {
			return "", fmt.Errorf("直播画面在 %s 内仍未就绪：%w", previewReadyTimeout, lastErr)
		}
		return "", fmt.Errorf("直播画面在 %s 内仍未就绪", previewReadyTimeout)
	}
}

func probeMPVFrame(ctx context.Context, player, roomID, playbackURL string) error {
	probeCtx, cancelProbe := context.WithTimeout(ctx, previewProbeTimeout)
	defer cancelProbe()
	output := &tailBuffer{limit: mpvOutputTailBytes}
	command := exec.CommandContext(probeCtx, player, mpvProbeArgs(roomID, playbackURL)...)
	command.Stdout = output
	command.Stderr = output
	err := command.Run()
	probeOutput := output.String()
	if err == nil && strings.Contains(probeOutput, mpvFrameReadyMarker) {
		return nil
	}
	if probeCtx.Err() != nil && ctx.Err() == nil {
		return fmt.Errorf("等待视频首帧超时")
	}
	if detail := lastMPVErrorLine(probeOutput); detail != "" {
		return fmt.Errorf("尚未读取到视频首帧：%s", detail)
	}
	if err == nil {
		return fmt.Errorf("mpv 未确认视频首帧")
	}
	return fmt.Errorf("尚未读取到视频首帧: %w", err)
}

func mpvProbeArgs(roomID, playbackURL string) []string {
	return []string{
		"--no-config",
		"--force-window=no",
		"--vo=null",
		"--audio=no",
		"--frames=1",
		"--term-status-msg=" + mpvFrameReadyMarker,
		"--network-timeout=6",
		"--referrer=https://live.bilibili.com/" + strings.TrimSpace(roomID),
		"--",
		playbackURL,
	}
}

func mpvPreviewArgs(roomID, playbackURL string) []string {
	return []string{
		"--force-window=no",
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

func formatMPVStartError(waitErr error, output string) error {
	detail := lastMPVErrorLine(output)
	if detail != "" {
		return fmt.Errorf("mpv 启动后立即退出：%s", detail)
	}
	if waitErr != nil {
		return fmt.Errorf("mpv 启动后立即退出: %w", waitErr)
	}
	return fmt.Errorf("mpv 启动后立即退出")
}

func lastMPVErrorLine(output string) string {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	for index := len(lines) - 1; index >= 0; index-- {
		line := strings.TrimSpace(strings.TrimSuffix(lines[index], "\r"))
		if line == "" || strings.HasPrefix(line, "Exiting...") {
			continue
		}
		line = previewURLPattern.ReplaceAllString(line, "[播放地址]")
		runes := []rune(line)
		if len(runes) > 240 {
			line = string(runes[:240]) + "…"
		}
		return line
	}
	return ""
}

type tailBuffer struct {
	mu    sync.Mutex
	data  []byte
	limit int
}

func (buffer *tailBuffer) Write(data []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	written := len(data)
	if buffer.limit <= 0 {
		return written, nil
	}
	if len(data) >= buffer.limit {
		buffer.data = append(buffer.data[:0], data[len(data)-buffer.limit:]...)
		return written, nil
	}
	overflow := len(buffer.data) + len(data) - buffer.limit
	if overflow > 0 {
		copy(buffer.data, buffer.data[overflow:])
		buffer.data = buffer.data[:len(buffer.data)-overflow]
	}
	buffer.data = append(buffer.data, data...)
	return written, nil
}

func (buffer *tailBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return string(buffer.data)
}
