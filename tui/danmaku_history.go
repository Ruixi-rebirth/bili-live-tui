package tui

import (
	"time"

	"bili-live-tui/internal/api"
)

// giftMessagesMatch 优先采用稳定标识，不能用同名或时间接近覆盖不同的 UID/批次。
func giftMessagesMatch(previous, incoming api.DanmakuMessage) bool {
	if previous.GiftName == "" || previous.GiftName != incoming.GiftName {
		return false
	}
	if previous.UserID != "" && incoming.UserID != "" {
		if previous.UserID != incoming.UserID {
			return false
		}
	} else if previous.Username == "" || previous.Username != incoming.Username {
		return false
	}
	if previous.BatchComboID != "" && incoming.BatchComboID != "" {
		return previous.BatchComboID == incoming.BatchComboID
	}
	if previous.Timestamp.IsZero() || incoming.Timestamp.IsZero() {
		return false
	}
	elapsed := incoming.Timestamp.Sub(previous.Timestamp)
	return elapsed >= 0 && elapsed < 15*time.Second
}

func duplicateGuardNotice(previous, incoming api.DanmakuEvent) bool {
	paired := previous.Command == "GUARD_BUY" && incoming.Command == "USER_TOAST_MSG" ||
		previous.Command == "USER_TOAST_MSG" && incoming.Command == "GUARD_BUY"
	if !paired || previous.Kind != api.DanmakuEventGuard || incoming.Kind != api.DanmakuEventGuard {
		return false
	}
	a, b := previous.Message, incoming.Message
	if a.UserID == "" || a.UserID != b.UserID || a.GuardLevel != b.GuardLevel || a.GiftCount != b.GiftCount {
		return false
	}
	if a.Timestamp.IsZero() || b.Timestamp.IsZero() {
		return false
	}
	elapsed := b.Timestamp.Sub(a.Timestamp)
	return elapsed >= 0 && elapsed < 5*time.Second
}
