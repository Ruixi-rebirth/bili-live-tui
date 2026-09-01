package tui

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"bili-live-tui/internal/api"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

type fakeDanmakuStream struct {
	events chan api.DanmakuEvent
	errors chan error
	closed chan struct{}
	once   sync.Once
}

func (s *fakeDanmakuStream) Events() <-chan api.DanmakuEvent { return s.events }
func (s *fakeDanmakuStream) Errors() <-chan error            { return s.errors }
func (s *fakeDanmakuStream) Close() {
	s.once.Do(func() { close(s.closed) })
}

func TestDanmakuStreamContinuesAfterAuthentication(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stream := &fakeDanmakuStream{
		events: make(chan api.DanmakuEvent, 2),
		errors: make(chan error),
		closed: make(chan struct{}),
	}
	stream.events <- api.DanmakuEvent{Kind: api.DanmakuEventConnected}
	stream.events <- api.DanmakuEvent{
		Kind:    api.DanmakuEventMessage,
		Message: api.DanmakuMessage{Username: "测试用户", Text: "认证后的弹幕"},
	}

	done := make(chan struct{})
	var observed []api.DanmakuEventKind
	chat := tview.NewTextView().SetText("正在连接弹幕服务器……")
	connectCalls := 0
	connect := func(context.Context) (danmakuStreamConnection, error) {
		connectCalls++
		return stream, nil
	}
	queueUI := func(update func()) { update() }
	onEvent := func(event api.DanmakuEvent) {
		observed = append(observed, event.Kind)
		if event.Kind == api.DanmakuEventMessage {
			cancel()
		}
	}

	go runDanmakuStreamWithConnector(
		ctx,
		connect,
		queueUI,
		onEvent,
		tview.NewTextView(),
		chat,
		done,
	)

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("danmaku stream did not stop after context cancellation")
	}

	if connectCalls != 1 {
		t.Fatalf("connector called %d times, want 1", connectCalls)
	}
	if len(observed) != 2 || observed[0] != api.DanmakuEventConnected || observed[1] != api.DanmakuEventMessage {
		t.Fatalf("observed events = %#v, want connected then message", observed)
	}
	if got := chat.GetText(true); got != "" {
		t.Fatalf("chat placeholder after connection = %q, want empty", got)
	}
	select {
	case <-stream.closed:
	default:
		t.Fatal("stream was not closed")
	}
}

func TestLiveDanmakuSessionPersistsAcrossViews(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream := &fakeDanmakuStream{
		events: make(chan api.DanmakuEvent, 4),
		errors: make(chan error),
		closed: make(chan struct{}),
	}
	connectCalls := 0
	session := newLiveDanmakuSessionWithConnector(ctx, func(context.Context) (danmakuStreamConnection, error) {
		connectCalls++
		return stream, nil
	})
	session.SetDraft("尚未发送的内容")
	stream.events <- api.DanmakuEvent{Kind: api.DanmakuEventConnected}
	stream.events <- api.DanmakuEvent{Kind: api.DanmakuEventMessage, Message: api.DanmakuMessage{Username: "用户甲", Text: "切换前"}}

	waitForHistory := func(want int) liveDanmakuSnapshot {
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			snapshot := session.snapshot()
			if len(snapshot.history) == want {
				return snapshot
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatalf("history did not reach %d entries", want)
		return liveDanmakuSnapshot{}
	}
	waitForHistory(1)

	// 这里模拟切换到概览、暂时没有弹幕页面挂载的情况。
	stream.events <- api.DanmakuEvent{Kind: api.DanmakuEventOnline, Online: 88}
	stream.events <- api.DanmakuEvent{Kind: api.DanmakuEventMessage, Message: api.DanmakuMessage{Username: "用户乙", Text: "概览期间"}}
	snapshot := waitForHistory(2)
	if connectCalls != 1 {
		t.Fatalf("connector called %d times, want one persistent websocket", connectCalls)
	}
	if snapshot.history[0].Message.Text != "切换前" || snapshot.history[1].Message.Text != "概览期间" {
		t.Fatalf("persistent history = %#v", snapshot.history)
	}
	if snapshot.draft != "尚未发送的内容" {
		t.Fatalf("draft = %q, want preserved input", snapshot.draft)
	}
	if !snapshot.onlineKnown || snapshot.online != 88 {
		t.Fatalf("online snapshot = (%d, %t), want (88, true)", snapshot.online, snapshot.onlineKnown)
	}
	if got := formatDanmakuSessionStatus(snapshot); got != "弹幕已连接 · 当前人气 88" {
		t.Fatalf("mounted status = %q, want persisted popularity", got)
	}
	if got, known := session.Popularity(); !known || got != 88 {
		t.Fatalf("session popularity = (%d, %t), want (88, true)", got, known)
	}

	session.Close()
	select {
	case <-stream.closed:
	default:
		t.Fatal("persistent stream was not closed with the live session")
	}
}

func TestLiveDanmakuSessionSubscriptionStartsWithRefresh(t *testing.T) {
	session := &LiveDanmakuSession{
		subscribers: make(map[chan struct{}]struct{}),
	}
	updates, unsubscribe := session.subscribe()
	defer unsubscribe()

	select {
	case <-updates:
	case <-time.After(time.Second):
		t.Fatal("new subscription did not request an initial refresh")
	}
}

func TestObservePopularityKeepsLatestValue(t *testing.T) {
	session := &LiveDanmakuSession{subscribers: make(map[chan struct{}]struct{})}
	now := time.Now()
	session.ObservePopularity(42, now)
	session.ObservePopularity(88, now.Add(time.Second))
	session.ObservePopularity(0, now.Add(2*time.Second))
	session.ObservePopularity(7, now.Add(-time.Second))
	if got, known := session.Popularity(); !known || got != 0 {
		t.Fatalf("popularity = (%d, %t), want latest value (0, true)", got, known)
	}
}

func TestMatchesControlShortcutSupportsLegacyAndModifiedRuneEvents(t *testing.T) {
	legacy := tcell.NewEventKey(tcell.KeyCtrlH, 0, tcell.ModNone)
	rawControl := tcell.NewEventKey(tcell.KeyRune, '\b', tcell.ModNone)
	enhanced := tcell.NewEventKey(tcell.KeyRune, 'H', tcell.ModCtrl|tcell.ModShift)
	plain := tcell.NewEventKey(tcell.KeyRune, 'h', tcell.ModNone)
	if !matchesControlShortcut(legacy, tcell.KeyCtrlH, 'h') {
		t.Fatal("legacy Ctrl+H event was not recognized")
	}
	if !matchesControlShortcut(rawControl, tcell.KeyCtrlH, 'h') {
		t.Fatal("raw control-byte Ctrl+H event was not recognized")
	}
	if !matchesControlShortcut(enhanced, tcell.KeyCtrlH, 'h') {
		t.Fatal("modified-rune Ctrl+H event was not recognized")
	}
	if matchesControlShortcut(plain, tcell.KeyCtrlH, 'h') {
		t.Fatal("plain h was recognized as Ctrl+H")
	}
	alt := tcell.NewEventKey(tcell.KeyRune, 'h', tcell.ModAlt)
	if !matchesModifiedRuneShortcut(alt, tcell.ModAlt, 'h') {
		t.Fatal("Alt+H event was not recognized")
	}
}

func TestDanmakuStatusDoesNotDuplicateViewerCount(t *testing.T) {
	snapshot := liveDanmakuSnapshot{
		status:       "弹幕已连接，消息会实时显示。",
		online:       88,
		onlineKnown:  true,
		viewerOnline: 23,
		viewerKnown:  true,
	}
	if got := formatDanmakuSessionStatus(snapshot); got != "弹幕已连接 · 当前人气 88" {
		t.Fatalf("danmaku status = %q", got)
	}
}

func TestDanmakuStatusPreservesConnectedNode(t *testing.T) {
	snapshot := liveDanmakuSnapshot{
		status:      "弹幕已连接 · 节点 broadcast.example:443 · 当前人气 42",
		online:      88,
		onlineKnown: true,
	}
	if got := formatDanmakuSessionStatus(snapshot); got != "弹幕已连接 · 节点 broadcast.example:443 · 当前人气 88" {
		t.Fatalf("danmaku status = %q", got)
	}
}

func TestRenderOnlineRank(t *testing.T) {
	view := tview.NewTextView().SetDynamicColors(true)
	renderOnlineRank(view, liveDanmakuSnapshot{
		viewerOnline: 23,
		viewerKnown:  true,
		onlineRank: []api.OnlineRankMember{
			{Username: "用户[甲]", Rank: 1, Score: 11, GuardLevel: 3},
		},
	})
	if view.GetTitle() != " 在线 23 人 " {
		t.Fatalf("online rank title = %q", view.GetTitle())
	}
	text := view.GetText(true)
	if !strings.Contains(text, "高能榜") || !strings.Contains(text, "用户[甲]") || !strings.Contains(text, "舰长") || !strings.Contains(text, "11") {
		t.Fatalf("online rank text = %q", text)
	}
}

func TestUpdateDanmakuHistoryAppendsOnlyNewEvents(t *testing.T) {
	chat := tview.NewTextView().SetMaxLines(danmakuHistoryLimit)
	first := api.DanmakuEvent{Kind: api.DanmakuEventMessage, Message: api.DanmakuMessage{Username: "用户甲", Text: "第一条"}}
	second := api.DanmakuEvent{Kind: api.DanmakuEventMessage, Message: api.DanmakuMessage{Username: "用户乙", Text: "第二条"}}

	revision := updateDanmakuHistory(chat, liveDanmakuSnapshot{history: []api.DanmakuEvent{first}, historyRevision: 1}, 0)
	if revision != 1 {
		t.Fatalf("revision = %d, want 1", revision)
	}
	before := chat.GetText(true)
	revision = updateDanmakuHistory(chat, liveDanmakuSnapshot{history: []api.DanmakuEvent{first, second}, historyRevision: 2}, revision)
	after := chat.GetText(true)
	if !strings.Contains(after, "第一条") || !strings.Contains(after, "第二条") {
		t.Fatalf("incremental history = %q", after)
	}
	if strings.Count(after, "第一条") != 1 || !strings.HasPrefix(after, before) {
		t.Fatalf("existing history was unexpectedly redrawn or duplicated: before=%q after=%q", before, after)
	}
}

func TestUpdateDanmakuHistoryHandlesClearAndCoalescedAppend(t *testing.T) {
	chat := tview.NewTextView().SetMaxLines(danmakuHistoryLimit)
	old := api.DanmakuEvent{Kind: api.DanmakuEventMessage, Message: api.DanmakuMessage{Username: "旧用户", Text: "旧弹幕"}}
	fresh := api.DanmakuEvent{Kind: api.DanmakuEventMessage, Message: api.DanmakuMessage{Username: "新用户", Text: "新弹幕"}}
	renderDanmakuHistory(chat, []api.DanmakuEvent{old}, "")

	// 修订号增加两次代表一次清空和一次新增；此时必须完整渲染当前历史。
	revision := updateDanmakuHistory(chat, liveDanmakuSnapshot{history: []api.DanmakuEvent{fresh}, historyRevision: 3}, 1)
	text := chat.GetText(true)
	if revision != 3 || strings.Contains(text, "旧弹幕") || !strings.Contains(text, "新弹幕") {
		t.Fatalf("history after clear = %q, revision=%d", text, revision)
	}
}
