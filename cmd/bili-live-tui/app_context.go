package main

import (
	"context"
	"errors"
	"fmt"

	"bili-live-tui/internal/api"
	"bili-live-tui/internal/config"
	"bili-live-tui/internal/diagnostics"
)

type appContext struct {
	client        *api.Client
	auth          *config.AuthData
	roomID        string
	diagnosticLog *diagnostics.Logger
}

func initAppContext(ctx context.Context, persist bool) (_ *appContext, resultErr error) {
	var diagnosticLog *diagnostics.Logger
	if persist {
		diagnosticLog, _ = diagnostics.Open()
	}
	// 初始化失败时资源仍归本函数所有，成功后才交给调用方关闭。
	defer func() {
		if resultErr != nil {
			_ = diagnosticLog.Close()
		}
	}()
	if diagnosticLog != nil {
		diagnosticLog.Printf("程序启动")
	}

	client := api.NewClient(nil)
	var auth *config.AuthData
	var err error
	if persist {
		auth, err = config.LoadAuth()
	}
	if !persist || err != nil {
		auth, err = performLogin(ctx, persist)
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				if diagnosticLog != nil {
					diagnosticLog.Printf("扫码登录失败: %v", err)
				}
				return nil, fmt.Errorf("登录失败: %w", err)
			}
			return nil, err
		}
	}

	roomID, err := client.GetMyRoomID(ctx, auth.SESSDATA)
	if err != nil && isAuthenticationError(err) {
		fmt.Println("登录凭证已失效，请重新扫码登录")
		auth, err = performLogin(ctx, persist)
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				if diagnosticLog != nil {
					diagnosticLog.Printf("重新登录失败: %v", err)
				}
				return nil, fmt.Errorf("重新登录失败: %w", err)
			}
			return nil, err
		}
		roomID, err = client.GetMyRoomID(ctx, auth.SESSDATA)
	}
	if err != nil {
		if diagnosticLog != nil {
			diagnosticLog.Printf("获取房间号失败: %v", err)
		}
		return nil, fmt.Errorf("获取房间号失败: %w", err)
	}

	return &appContext{
		client:        client,
		auth:          auth,
		roomID:        roomID,
		diagnosticLog: diagnosticLog,
	}, nil
}
