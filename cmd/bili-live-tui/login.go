package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"bili-live-tui/internal/api"
	"bili-live-tui/internal/config"
	"github.com/mdp/qrterminal/v3"
)

// performLogin 执行可取消的扫码登录，不在辅助函数中直接终止整个进程。
func performLogin(ctx context.Context, saveAuth bool) (*config.AuthData, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	fmt.Println("正在请求 TV 端登录二维码...")

	qrURL, authCode, err := api.GetTVQRCodeContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("获取二维码失败: %w", err)
	}

	// 使用半方块字符保持二维码接近正方形，并缩小四周留白。
	qrterminal.GenerateWithConfig(qrURL, qrterminal.Config{
		Level:          qrterminal.L,
		Writer:         os.Stdout,
		HalfBlocks:     true,
		BlackChar:      " ",
		BlackWhiteChar: "▄",
		WhiteBlackChar: "▀",
		WhiteChar:      "█",
		QuietZone:      1,
	})
	fmt.Println("请使用哔哩哔哩手机 APP 扫码登录")

	for {
		status, pollData, err := api.CheckQRStatusContext(ctx, authCode)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			fmt.Println("网络请求异常，重试中...")
			if err := waitForContext(ctx, 3*time.Second); err != nil {
				return nil, err
			}
			continue
		}

		switch status {
		case api.QRStatusSuccess:
			fmt.Println("\n登录成功，已获取 APP Access Token")

			auth, err := config.ExtractAuthData(pollData)
			if err != nil {
				return nil, err
			}
			if saveAuth {
				if err := config.SaveAuth(pollData); err != nil {
					fmt.Printf("保存登录凭证失败: %v\n", err)
				}
			} else {
				fmt.Println("（临时会话模式：本次登录凭据不保存至本地磁盘）")
			}
			return auth, nil

		case api.QRStatusExpired:
			return nil, fmt.Errorf("二维码已失效，请重新运行程序")

		case api.QRStatusWaiting:
			// 还在等，保持静默

		case api.QRStatusScanned:
			fmt.Println("已扫码，请在手机端点击确认...")
		}

		if err := waitForContext(ctx, 2*time.Second); err != nil {
			return nil, err
		}
	}
}

func waitForContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
