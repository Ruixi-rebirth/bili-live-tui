package api

import (
	"context"
	"fmt"
	"time"
)

const defaultDanmakuSendInterval = time.Second

type danmakuSendFunc func(context.Context, string, int) error

// DanmakuSender 串行发送弹幕，并限制连续请求的最短间隔。
type DanmakuSender struct {
	gate        chan struct{}
	interval    time.Duration
	lastAttempt time.Time
	send        danmakuSendFunc
}

func NewDanmakuSender(client *Client, roomID, sessdata, biliJCT string) *DanmakuSender {
	var send danmakuSendFunc
	if client != nil {
		send = func(ctx context.Context, message string, maxLength int) error {
			return client.SendDanmakuWithLimit(ctx, roomID, sessdata, biliJCT, message, maxLength)
		}
	}
	return newDanmakuSender(send, defaultDanmakuSendInterval)
}

func newDanmakuSender(send danmakuSendFunc, interval time.Duration) *DanmakuSender {
	gate := make(chan struct{}, 1)
	gate <- struct{}{}
	return &DanmakuSender{gate: gate, interval: max(interval, 0), send: send}
}

func (s *DanmakuSender) Send(ctx context.Context, message string, maxLength int) error {
	if s == nil || s.send == nil {
		return fmt.Errorf("弹幕发送器未初始化")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.gate:
	}
	defer func() { s.gate <- struct{}{} }()

	if remaining := time.Until(s.lastAttempt.Add(s.interval)); remaining > 0 {
		timer := time.NewTimer(remaining)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}

	s.lastAttempt = time.Now()
	return s.send(ctx, message, maxLength)
}
