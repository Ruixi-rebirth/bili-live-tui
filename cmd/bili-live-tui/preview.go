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
	previewURLTimeout  = 8 * time.Second
	mpvStartupGrace    = 1200 * time.Millisecond
	mpvOutputTailBytes = 8 * 1024
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
	requestCtx, cancelRequest := context.WithTimeout(ctx, previewURLTimeout)
	defer cancelRequest()
	playbackURL, err := client.GetRoomPlaybackURL(requestCtx, roomID, sessdata, biliJCT)
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

func mpvPreviewArgs(roomID, playbackURL string) []string {
	return []string{
		// The preview is a real desktop window. --force-window=no would run mpv
		// invisibly and make the TUI claim success without showing a frame.
		"--force-window=immediate",
		"--terminal=no",
		"--no-config",
		"--cache=no",
		"--demuxer-readahead-secs=0",
		"--demuxer-lavf-analyzeduration=1",
		"--demuxer-lavf-probesize=1048576",
		"--stream-lavf-o=fflags=+nobuffer,reconnect=1,reconnect_at_eof=1,reconnect_streamed=1,reconnect_delay_max=5",
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
