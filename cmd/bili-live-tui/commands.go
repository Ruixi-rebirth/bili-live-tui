package main

import (
	"context"
	"fmt"
	"time"

	"bili-live-tui/internal/api"
	"bili-live-tui/internal/config"
	"bili-live-tui/internal/diagnostics"
)

func runLogout() error {
	removed, err := config.RemoveAuth()
	if err != nil {
		return fmt.Errorf("清除本地凭证失败: %w", err)
	}
	if removed {
		fmt.Println("✅ 已成功退出登录并清除本机凭据")
	} else {
		fmt.Println("本地未发现已保存的登录凭据，当前处于未登录状态")
	}
	return nil
}

func runStatus(ctx context.Context) error {
	app, err := initAppContext(ctx, true)
	if err != nil {
		return err
	}
	if app.diagnosticLog != nil {
		defer app.diagnosticLog.Close()
	}
	return handleStatusAction(ctx, app.client, app.roomID)
}

func runStop(ctx context.Context) error {
	app, err := initAppContext(ctx, true)
	if err != nil {
		return err
	}
	if app.diagnosticLog != nil {
		defer app.diagnosticLog.Close()
	}
	return handleStopAction(ctx, app.client, app.roomID, app.auth.AccessToken, app.diagnosticLog)
}

func handleStatusAction(ctx context.Context, client *api.Client, roomID string) error {
	snapshot, err := client.GetRoomSnapshot(ctx, roomID)
	if err != nil {
		return fmt.Errorf("获取房间状态失败: %w", err)
	}
	statusText := "未开播"
	var extraStatus string
	if snapshot.LiveStatus == 1 {
		statusText = "开播中 🔴"
		if !snapshot.LiveTime.IsZero() {
			duration := time.Since(snapshot.LiveTime).Truncate(time.Second)
			extraStatus = fmt.Sprintf("开播时间: %s\n已播时长: %s\n", snapshot.LiveTime.Format("2006-01-02 15:04:05"), duration)
		}
	}
	fmt.Printf("直播间号: %s\n房间标题: %s\n直播分区: %s\n当前状态: %s\n%s", roomID, snapshot.Title, snapshot.AreaName, statusText, extraStatus)
	return nil
}

func handleStopAction(ctx context.Context, client *api.Client, roomID string, accessToken string, logger *diagnostics.Logger) error {
	requestCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	fmt.Printf("正在向 B 站请求结束房间 %s 的直播……\n", roomID)
	if err := client.StopLive(requestCtx, roomID, accessToken); err != nil {
		if logger != nil {
			logger.Printf("命令行下播失败: %v", err)
		}
		return fmt.Errorf("下播失败: %w", err)
	}
	if logger != nil {
		logger.Printf("命令行下播成功 room=%s", roomID)
	}
	fmt.Printf("✅ 直播间 %s 已成功下播！\n", roomID)
	return nil
}
