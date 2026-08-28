package api

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestDanmakuSenderSerializesRequests(t *testing.T) {
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var calls atomic.Int32
	sender := newDanmakuSender(func(context.Context, string, int) error {
		if calls.Add(1) == 1 {
			close(firstStarted)
			<-releaseFirst
		}
		return nil
	}, 0)

	firstDone := make(chan error, 1)
	go func() { firstDone <- sender.Send(context.Background(), "第一条", 20) }()
	<-firstStarted

	secondDone := make(chan error, 1)
	go func() { secondDone <- sender.Send(context.Background(), "第二条", 20) }()
	time.Sleep(10 * time.Millisecond)
	if got := calls.Load(); got != 1 {
		t.Fatalf("concurrent send calls = %d, want 1", got)
	}

	close(releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatalf("first Send() error = %v", err)
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("second Send() error = %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("total send calls = %d, want 2", got)
	}
}

func TestDanmakuSenderCancelsWhileWaitingForPreviousRequest(t *testing.T) {
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var calls atomic.Int32
	sender := newDanmakuSender(func(context.Context, string, int) error {
		calls.Add(1)
		close(firstStarted)
		<-releaseFirst
		return nil
	}, 0)

	firstDone := make(chan error, 1)
	go func() { firstDone <- sender.Send(context.Background(), "第一条", 20) }()
	<-firstStarted

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sender.Send(ctx, "第二条", 20); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Send() error = %v, want context.Canceled", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("send calls after cancellation = %d, want 1", got)
	}
	close(releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatalf("first Send() error = %v", err)
	}
}

func TestDanmakuSenderCancelsDuringCooldown(t *testing.T) {
	var calls atomic.Int32
	sender := newDanmakuSender(func(context.Context, string, int) error {
		calls.Add(1)
		return nil
	}, time.Hour)
	if err := sender.Send(context.Background(), "第一条", 20); err != nil {
		t.Fatalf("first Send() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := sender.Send(ctx, "第二条", 20); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cooldown Send() error = %v, want context deadline", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("send calls during cooldown = %d, want 1", got)
	}
}
