package api

import (
	"context"
	"errors"
	"testing"
)

func TestQRRequestsRespectCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := GetTVQRCodeContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("GetTVQRCodeContext() error = %v", err)
	}
	if _, _, err := CheckQRStatusContext(ctx, "auth-code"); !errors.Is(err, context.Canceled) {
		t.Fatalf("CheckQRStatusContext() error = %v", err)
	}
}
