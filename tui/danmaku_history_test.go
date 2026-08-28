package tui

import (
	"bili-live-tui/internal/api"
	"testing"
	"time"
)

func TestGiftMergeUsesIDsBeforeNamesAndTime(t *testing.T) {
	a := api.DanmakuMessage{UserID: "1", Username: "同名用户", GiftName: "礼物", BatchComboID: "a", Timestamp: time.Now()}
	b := a
	b.UserID = "2"
	if giftMessagesMatch(a, b) {
		t.Fatal("different users merged by name")
	}
	b = a
	b.BatchComboID = "b"
	if giftMessagesMatch(a, b) {
		t.Fatal("different batches merged by time")
	}
	b = a
	if !giftMessagesMatch(a, b) {
		t.Fatal("same batch not matched")
	}
	a.BatchComboID = ""
	b.BatchComboID = ""
	b.Timestamp = a.Timestamp.Add(-time.Second)
	if giftMessagesMatch(a, b) {
		t.Fatal("negative time window matched")
	}
}

func TestGuardDedupOnlyPairsEquivalentCommands(t *testing.T) {
	a := api.DanmakuEvent{Kind: api.DanmakuEventGuard, Command: "GUARD_BUY", Message: api.DanmakuMessage{UserID: "1", GuardLevel: 3, GiftCount: 1, Timestamp: time.Now()}}
	b := a
	if duplicateGuardNotice(a, b) {
		t.Fatal("independent purchases must not be suppressed")
	}
	b.Command = "USER_TOAST_MSG"
	if !duplicateGuardNotice(a, b) {
		t.Fatal("paired notification not deduplicated")
	}
	b.Message.GuardLevel = 2
	if duplicateGuardNotice(a, b) {
		t.Fatal("different guard levels merged")
	}
}

func TestStaleComboDoesNotRegressHistory(t *testing.T) {
	session := &LiveDanmakuSession{}
	event := api.DanmakuEvent{Kind: api.DanmakuEventGift, Message: api.DanmakuMessage{UserID: "1", GiftName: "礼物", BatchComboID: "a", GiftCombo: 3, GiftCount: 3, Text: "连击3", Timestamp: time.Now()}}
	session.handleEvent(event)
	stats := session.Stats()
	event.Message.GiftCombo = 2
	event.Message.Text = "连击2"
	session.handleEvent(event)
	if session.history[0].Message.Text != "连击3" || session.Stats() != stats {
		t.Fatal("stale combo changed current state")
	}
}
