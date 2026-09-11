package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"bili-live-tui/internal/api"
	"bili-live-tui/internal/ffmpeg"
	"bili-live-tui/internal/obs"
	streamruntime "bili-live-tui/internal/stream"
)

func ensureRoomCanStart(snapshot api.RoomSnapshot) error {
	if snapshot.LiveStatus == 1 {
		return fmt.Errorf("检测到直播间已经开播，请先下播后再开始（可使用 bili-live-tui stop 一键下播）")
	}
	return nil
}

func liveSettingsFromSnapshot(snapshot api.RoomSnapshot) api.LiveSettings {
	return api.LiveSettings{
		Title:       snapshot.Title,
		Description: snapshot.Description,
		Tags:        snapshot.Tags,
		AreaID:      snapshot.AreaID,
		CoverPath:   snapshot.Cover,
	}
}

func watchStreamOutput(ctx context.Context, done <-chan struct{}, onUnexpectedStop func()) {
	select {
	case <-done:
		if ctx.Err() == nil && onUnexpectedStop != nil {
			onUnexpectedStop()
		}
	case <-ctx.Done():
	}
}

func isAuthenticationError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{"未登录", "登录失效", "登录过期", "token错误", "token 错误", "-101", "65530"} {
		if strings.Contains(message, strings.ToLower(marker)) {
			return true
		}
	}
	return false
}

func rollbackLiveStart(client *api.Client, roomID, accessToken string, cause error) (bool, error) {
	rollbackCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := client.StopLive(rollbackCtx, roomID, accessToken); err != nil {
		return false, fmt.Errorf("%w；清理已开启的 B 站直播时自动下播失败：%v，请到 B 站直播中心确认状态", cause, err)
	}
	return true, cause
}

func newStreamRuntime(settings api.LiveSettings) (streamruntime.Runtime, error) {
	switch strings.TrimSpace(settings.StreamMode) {
	case "", streamruntime.ModeOBS:
		return obs.NewRuntime(settings.OBSHost, settings.OBSPort, settings.OBSPassword), nil
	case streamruntime.ModeFFmpegTest:
		return ffmpeg.NewTestRuntime(settings.Orientation), nil
	default:
		return nil, fmt.Errorf("不支持的推流方式: %s", settings.StreamMode)
	}
}

func preflightStreamExecutable(settings api.LiveSettings) error {
	switch strings.TrimSpace(settings.StreamMode) {
	case "", streamruntime.ModeOBS:
		return obs.Preflight(settings.OBSHost, settings.OBSPort, settings.OBSPassword)
	case streamruntime.ModeFFmpegTest:
		_, err := ffmpeg.ExecutablePath()
		return err
	default:
		return fmt.Errorf("不支持的推流方式: %s", settings.StreamMode)
	}
}
