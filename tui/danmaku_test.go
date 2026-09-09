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
	var states []danmakuConnectionState
	connectCalls := 0
	connect := func(context.Context) (danmakuStreamConnection, error) {
		connectCalls++
		return stream, nil
	}
	onEvent := func(event api.DanmakuEvent) {
		observed = append(observed, event.Kind)
		if event.Kind == api.DanmakuEventMessage {
			cancel()
		}
	}

	go runDanmakuStreamWithConnector(
		ctx,
		connect,
		func(state danmakuConnectionState) { states = append(states, state) },
		onEvent,
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
	if len(states) < 3 || states[0].phase != danmakuConnectionConnecting ||
		states[1].phase != danmakuConnectionAuthenticating ||
		states[2].phase != danmakuConnectionConnected {
		t.Fatalf("connection states = %#v, want connecting, authenticating, connected", states)
	}
	select {
	case <-stream.closed:
	default:
		t.Fatal("stream was not closed")
	}
}

func TestDanmakuRetryDelayBacksOffAndCaps(t *testing.T) {
	want := []time.Duration{
		time.Second,
		2 * time.Second,
		5 * time.Second,
		10 * time.Second,
		15 * time.Second,
		15 * time.Second,
	}
	for index, expected := range want {
		if got := danmakuRetryDelay(index + 1); got != expected {
			t.Fatalf("retry delay %d = %v, want %v", index+1, got, expected)
		}
	}
	if got := danmakuRetryDelay(0); got != time.Second {
		t.Fatalf("retry delay after confirmed connection = %v, want 1s", got)
	}
}

func TestDanmakuStreamReconnectsAfterUnconfirmedConnectionEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := &fakeDanmakuStream{
		events: make(chan api.DanmakuEvent),
		errors: make(chan error),
		closed: make(chan struct{}),
	}
	close(first.events)
	close(first.errors)
	second := &fakeDanmakuStream{
		events: make(chan api.DanmakuEvent, 2),
		errors: make(chan error),
		closed: make(chan struct{}),
	}
	second.events <- api.DanmakuEvent{Kind: api.DanmakuEventConnected}
	second.events <- api.DanmakuEvent{Kind: api.DanmakuEventMessage, Message: api.DanmakuMessage{Text: "重连成功"}}

	connectCalls := 0
	connect := func(context.Context) (danmakuStreamConnection, error) {
		connectCalls++
		if connectCalls == 1 {
			return first, nil
		}
		return second, nil
	}
	done := make(chan struct{})
	var states []danmakuConnectionState
	go runDanmakuStreamWithConnector(
		ctx,
		connect,
		func(state danmakuConnectionState) { states = append(states, state) },
		func(event api.DanmakuEvent) {
			if event.Message.Text == "重连成功" {
				cancel()
			}
		},
		done,
	)

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("danmaku stream did not reconnect")
	}
	if connectCalls != 2 {
		t.Fatalf("connector calls = %d, want 2", connectCalls)
	}
	foundRetry := false
	for _, state := range states {
		if state.phase == danmakuConnectionRetrying && !state.confirmed {
			foundRetry = true
			break
		}
	}
	if !foundRetry {
		t.Fatalf("connection states = %#v, want an unconfirmed retry", states)
	}
	select {
	case <-first.closed:
	default:
		t.Fatal("first stream was not closed before reconnect")
	}
}

func TestDanmakuStreamStopsWhileWaitingToReconnect(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	connectCalls := 0
	go runDanmakuStreamWithConnector(
		ctx,
		func(context.Context) (danmakuStreamConnection, error) {
			connectCalls++
			return nil, context.DeadlineExceeded
		},
		func(state danmakuConnectionState) {
			if state.phase == danmakuConnectionRetrying {
				cancel()
			}
		},
		func(api.DanmakuEvent) {},
		done,
	)

	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("danmaku stream did not stop after cancelling reconnect wait")
	}
	if connectCalls != 1 {
		t.Fatalf("connector calls = %d, want 1", connectCalls)
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

func TestLiveDanmakuSessionTracksConnectionStateSeparatelyFromStatusText(t *testing.T) {
	session := &LiveDanmakuSession{subscribers: make(map[chan struct{}]struct{})}
	session.updateConnectionState(danmakuConnectionState{
		phase:     danmakuConnectionConnected,
		endpoint:  "example.test:443",
		confirmed: true,
	})
	if snapshot := session.snapshot(); !snapshot.connection.connected() {
		t.Fatalf("connected snapshot = %#v, want connected", snapshot)
	}
	session.updateConnectionState(danmakuConnectionState{
		phase:      danmakuConnectionRetrying,
		lastError:  "unexpected EOF",
		retryDelay: time.Second,
		confirmed:  true,
	})
	if snapshot := session.snapshot(); snapshot.connection.connected() {
		t.Fatalf("disconnected snapshot = %#v, want disconnected", snapshot)
	}
}

func TestLiveDanmakuSessionDeduplicatesLiveStateNotifications(t *testing.T) {
	session := &LiveDanmakuSession{subscribers: make(map[chan struct{}]struct{})}
	live := api.DanmakuEvent{Kind: api.DanmakuEventSystem, Command: "LIVE"}
	preparing := api.DanmakuEvent{Kind: api.DanmakuEventSystem, Command: "PREPARING"}

	session.handleEvent(live)
	session.handleEvent(live)
	session.handleEvent(preparing)
	session.handleEvent(preparing)
	session.handleEvent(live)

	snapshot := session.snapshot()
	if len(snapshot.history) != 3 {
		t.Fatalf("live state history length = %d, want 3", len(snapshot.history))
	}
	if snapshot.historyRevision != 3 {
		t.Fatalf("live state history revision = %d, want 3", snapshot.historyRevision)
	}
	commands := []string{
		snapshot.history[0].Command,
		snapshot.history[1].Command,
		snapshot.history[2].Command,
	}
	if strings.Join(commands, ",") != "LIVE,PREPARING,LIVE" {
		t.Fatalf("live state history = %v", commands)
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
	legacy := tcell.NewEventKey(tcell.KeyCtrlU, 0, tcell.ModNone)
	rawControl := tcell.NewEventKey(tcell.KeyRune, rune(tcell.KeyCtrlU), tcell.ModNone)
	enhanced := tcell.NewEventKey(tcell.KeyRune, 'U', tcell.ModCtrl|tcell.ModShift)
	plain := tcell.NewEventKey(tcell.KeyRune, 'u', tcell.ModNone)
	if !matchesControlShortcut(legacy, tcell.KeyCtrlU, 'u') {
		t.Fatal("legacy Ctrl+U event was not recognized")
	}
	if !matchesControlShortcut(rawControl, tcell.KeyCtrlU, 'u') {
		t.Fatal("raw control-byte Ctrl+U event was not recognized")
	}
	if !matchesControlShortcut(enhanced, tcell.KeyCtrlU, 'u') {
		t.Fatal("modified-rune Ctrl+U event was not recognized")
	}
	if matchesControlShortcut(plain, tcell.KeyCtrlU, 'u') {
		t.Fatal("plain u was recognized as Ctrl+U")
	}
}

func TestDanmakuStatusDoesNotDuplicateViewerCount(t *testing.T) {
	snapshot := liveDanmakuSnapshot{
		connection:   danmakuConnectionState{phase: danmakuConnectionConnected, confirmed: true},
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
		connection:  danmakuConnectionState{phase: danmakuConnectionConnected, endpoint: "broadcast.example:443", confirmed: true},
		online:      88,
		onlineKnown: true,
	}
	if got := formatDanmakuSessionStatus(snapshot); got != "弹幕已连接 · 节点 broadcast.example:443 · 当前人气 88" {
		t.Fatalf("danmaku status = %q", got)
	}
}

func TestDanmakuStatusFormatsRetryDetailsTogether(t *testing.T) {
	snapshot := liveDanmakuSnapshot{connection: danmakuConnectionState{
		phase:      danmakuConnectionRetrying,
		attempt:    3,
		lastError:  "unexpected EOF",
		retryDelay: 5 * time.Second,
	}}
	if got := formatDanmakuSessionStatus(snapshot); got != "弹幕连接失败（第 3 次，unexpected EOF），5 秒后重试……" {
		t.Fatalf("retry status = %q", got)
	}
}

func TestRenderOnlineRank(t *testing.T) {
	applyTheme()
	view := tview.NewTextView().SetDynamicColors(true).SetRegions(true)
	registry := newDanmakuUserRegionRegistry()
	snapshot := liveDanmakuSnapshot{
		viewerOnline: 23,
		viewerKnown:  true,
		onlineRank: []api.OnlineRankMember{
			{UserID: "42", Username: "用户[甲]", Rank: 1, Score: 11, GuardLevel: 3},
		},
		guardTotal: 5,
		guardKnown: true,
		guardMembers: []api.GuardMember{
			{UserID: "100", Username: "船员[乙]", Rank: 1, GuardLevel: 1, IsAlive: true},
		},
	}

	// 1. Audience Tab
	renderOnlineRank(view, snapshot, rankTabAudience, registry)
	if view.GetTitle() != " 房间观众 · 在线 23 人 " {
		t.Fatalf("online rank title = %q", view.GetTitle())
	}
	text := view.GetText(true)
	if !strings.Contains(text, "观众") || !strings.Contains(text, "用户[甲]") || !strings.Contains(text, "舰长") || !strings.Contains(text, "11") {
		t.Fatalf("online rank text = %q", text)
	}
	if len(registry.order) != 1 {
		t.Fatalf("online rank regions = %#v", registry.order)
	}
	message, ok := registry.Lookup(registry.order[0])
	if !ok || message.UserID != "42" || message.Username != "用户[甲]" || message.GuardLevel != 3 {
		t.Fatalf("online rank region lookup = (%#v, %v)", message, ok)
	}

	// 2. Guard Tab
	renderOnlineRank(view, snapshot, rankTabGuard, registry)
	if view.GetTitle() != " 大航海 · 共 5 人 " {
		t.Fatalf("guard tab title = %q", view.GetTitle())
	}
	guardText := view.GetText(true)
	if !strings.Contains(guardText, "大航海") || !strings.Contains(guardText, "船员[乙]") || !strings.Contains(guardText, "总督") || !strings.Contains(guardText, "在线") {
		t.Fatalf("guard tab text = %q", guardText)
	}
	guardMsg, ok := registry.Lookup(registry.order[0])
	if !ok || guardMsg.UserID != "100" || guardMsg.Username != "船员[乙]" || guardMsg.GuardLevel != 1 {
		t.Fatalf("guard region lookup = (%#v, %v)", guardMsg, ok)
	}
}

func TestPrependDanmakuMention(t *testing.T) {
	if text, ok := prependDanmakuMention("已有内容", "用户甲", 40); !ok || text != "@用户甲 已有内容" {
		t.Fatalf("mention = (%q, %v)", text, ok)
	}
	if text, ok := prependDanmakuMention("@用户甲 已有内容", "用户甲", 40); !ok || text != "@用户甲 已有内容" {
		t.Fatalf("duplicate mention = (%q, %v)", text, ok)
	}
	if text, ok := prependDanmakuMention("已有内容", "用户甲", 5); ok || text != "@用户甲 已有内容" {
		t.Fatalf("overlong mention = (%q, %v)", text, ok)
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
	renderDanmakuHistory(chat, []api.DanmakuEvent{old})

	// 修订号增加两次代表一次清空和一次新增；此时必须完整渲染当前历史。
	revision := updateDanmakuHistory(chat, liveDanmakuSnapshot{history: []api.DanmakuEvent{fresh}, historyRevision: 3}, 1)
	text := chat.GetText(true)
	if revision != 3 || strings.Contains(text, "旧弹幕") || !strings.Contains(text, "新弹幕") {
		t.Fatalf("history after clear = %q, revision=%d", text, revision)
	}
}

func TestDanmakuSnapshotConfirmsSendFromNewOwnMessage(t *testing.T) {
	oldMatch := api.DanmakuEvent{Kind: api.DanmakuEventMessage, Message: api.DanmakuMessage{UserID: "42", Text: "测试弹幕"}}
	otherUser := api.DanmakuEvent{Kind: api.DanmakuEventMessage, Message: api.DanmakuMessage{UserID: "43", Text: "测试弹幕"}}
	ownMatch := api.DanmakuEvent{Kind: api.DanmakuEventMessage, Message: api.DanmakuMessage{UserID: "42", Text: "测试弹幕"}}

	if danmakuSnapshotConfirmsSend(liveDanmakuSnapshot{history: []api.DanmakuEvent{oldMatch}, historyRevision: 1}, 1, "测试弹幕", "42") {
		t.Fatal("message preceding the submission was accepted as confirmation")
	}
	if danmakuSnapshotConfirmsSend(liveDanmakuSnapshot{history: []api.DanmakuEvent{oldMatch, otherUser}, historyRevision: 2}, 1, "测试弹幕", "42") {
		t.Fatal("another user's message was accepted as confirmation")
	}
	if !danmakuSnapshotConfirmsSend(liveDanmakuSnapshot{history: []api.DanmakuEvent{oldMatch, otherUser, ownMatch}, historyRevision: 3}, 1, " 测试弹幕 ", "42") {
		t.Fatal("new own message did not confirm the submission")
	}
}

func TestDanmakuSnapshotConfirmationSupportsLegacyMessageWithoutUID(t *testing.T) {
	legacy := api.DanmakuEvent{Kind: api.DanmakuEventMessage, Message: api.DanmakuMessage{Text: "测试弹幕"}}
	snapshot := liveDanmakuSnapshot{history: []api.DanmakuEvent{legacy}, historyRevision: 8}
	if !danmakuSnapshotConfirmsSend(snapshot, 7, "测试弹幕", "42") {
		t.Fatal("legacy message without UID did not confirm the submission")
	}
	if danmakuSnapshotConfirmsSend(snapshot, 7, "另一条弹幕", "42") {
		t.Fatal("different legacy message confirmed the submission")
	}
}

func TestDanmakuSendStatusFormatters(t *testing.T) {
	got := formatDanmakuSendAcceptedStatus(1, true)
	if got != "第 1 条弹幕已发送，正在等待直播间显示……" {
		t.Fatalf("formatDanmakuSendAcceptedStatus(1, true) = %q", got)
	}
	got = formatDanmakuSendAcceptedStatus(2, false)
	if got != "第 2 条弹幕已发送（弹幕服务正在连接，暂未收到实时回显）。" {
		t.Fatalf("formatDanmakuSendAcceptedStatus(2, false) = %q", got)
	}
	got = formatDanmakuSendTimeoutStatus(1, true)
	if got != "第 1 条弹幕已发送（若直播间未显示，可能被 B 站审核过滤）。" {
		t.Fatalf("formatDanmakuSendTimeoutStatus(1, true) = %q", got)
	}
	got = formatDanmakuSendTimeoutStatus(2, false)
	if got != "第 2 条弹幕已发送（弹幕服务未连接，未收到实时回显）。" {
		t.Fatalf("formatDanmakuSendTimeoutStatus(2, false) = %q", got)
	}
	got = formatDanmakuSendConfirmedStatus(3)
	if got != "第 3 条弹幕已在直播间显示。" {
		t.Fatalf("formatDanmakuSendConfirmedStatus(3) = %q", got)
	}
	got = formatDanmakuSendConfirmedStatus(0)
	if got != "弹幕已在直播间显示。" {
		t.Fatalf("formatDanmakuSendConfirmedStatus(0) = %q", got)
	}
}

func TestDanmakuReplyCounterAndUpstreamLimit(t *testing.T) {
	field := tview.NewInputField()
	field.SetText("你a好")
	updateDanmakuReplyCounter(field, 40, true, false)
	if got := field.GetTitle(); got != " 3/40 " {
		t.Fatalf("counter title = %q, want %q", got, " 3/40 ")
	}
	updateDanmakuReplyCounter(field, 40, false, false)
	if got := field.GetTitle(); got != " 3/… " {
		t.Fatalf("loading counter title = %q, want %q", got, " 3/… ")
	}
	updateDanmakuReplyCounter(field, 40, true, true)
	if got := field.GetTitle(); got != " 3/40 默认 " {
		t.Fatalf("fallback counter title = %q, want %q", got, " 3/40 默认 ")
	}
	if acceptsDanmakuInput(field, strings.Repeat("啊", 41), 40) {
		t.Fatal("input longer than upstream limit was accepted")
	}

	field.SetText(strings.Repeat("啊", 45))
	if !acceptsDanmakuInput(field, strings.Repeat("啊", 44), 40) {
		t.Fatal("deleting an oversized saved draft was rejected")
	}
	if acceptsDanmakuInput(field, strings.Repeat("啊", 46), 40) {
		t.Fatal("growing an oversized saved draft was accepted")
	}
}

func TestCanManageDanmakuUserRespectsIdentityAndHierarchy(t *testing.T) {
	anchor := api.RoomManagementCapabilities{UserID: "1", AnchorID: "1", IsAnchor: true, IsAdmin: true}
	if !canManageDanmakuUser(anchor, api.DanmakuMessage{UserID: "2", Username: "用户"}, &api.UserProfile{}) {
		t.Fatal("anchor could not manage ordinary user")
	}
	if canManageDanmakuUser(anchor, api.DanmakuMessage{UserID: "1", Username: "自己"}, &api.UserProfile{IsSelf: true}) {
		t.Fatal("self-management was enabled")
	}
	if canManageDanmakuUser(anchor, api.DanmakuMessage{UserID: "2", IsMystery: true}, &api.UserProfile{}) {
		t.Fatal("mystery-user management was enabled")
	}

	advanced := api.RoomManagementCapabilities{UserID: "3", IsAdmin: true, AdminLevel: 2, Permissions: []int{api.RoomPermissionMute}}
	if !canManageDanmakuUser(advanced, api.DanmakuMessage{UserID: "2"}, &api.UserProfile{}) {
		t.Fatal("advanced admin could not manage ordinary user")
	}
	if canManageDanmakuUser(advanced, api.DanmakuMessage{UserID: "2", IsAdmin: true}, &api.UserProfile{}) {
		t.Fatal("admin target with unknown level was not handled conservatively")
	}
}

func TestParseDanmakuManagementLevel(t *testing.T) {
	if level, err := parseDanmakuManagementLevel(" 121 "); err != nil || level != 121 {
		t.Fatalf("level = %d, err = %v", level, err)
	}
	for _, value := range []string{"", "0", "-1", "一点五"} {
		if _, err := parseDanmakuManagementLevel(value); err == nil {
			t.Fatalf("invalid level %q was accepted", value)
		}
	}
}

func TestFormatRoomSilentState(t *testing.T) {
	state := api.RoomSilentState{Enabled: true, Audience: api.RoomSilentMedal, Level: 12}
	if got := formatRoomSilentState(state); !strings.Contains(got, "粉丝勋章低于 12") || !strings.Contains(got, "手动关闭") {
		t.Fatalf("silent state = %q", got)
	}
}

func TestRoomManagerTableCells(t *testing.T) {
	if got := formatRoomManagerTableUser("黑名单用户", "3001"); got != "黑名单用户 · UID 3001" {
		t.Fatalf("table user = %q", got)
	}
	clicked := false
	actionCell := roomManagerTableActionCell("删除", errorColor, func() { clicked = true })
	if actionCell.Text != "删除" || actionCell.Clicked == nil || !actionCell.Clicked() || !clicked {
		t.Fatalf("table action cell is not clickable: %#v", actionCell)
	}
}

func TestDanmakuUsernameRegionKeepsDetailsOutOfTimeline(t *testing.T) {
	chat := tview.NewTextView().SetDynamicColors(true).SetRegions(true)
	registry := newDanmakuUserRegionRegistry()
	message := api.DanmakuMessage{
		Username: "用户甲", UserID: "42", Text: "你好",
		MedalName: "草莓", MedalLevel: 7, GuardLevel: 3,
	}
	appendDanmakuEvent(chat, api.DanmakuEvent{Kind: api.DanmakuEventMessage, Message: message}, false, registry)
	text := chat.GetText(true)
	if !strings.Contains(text, "用户甲：你好") {
		t.Fatalf("timeline text = %q", text)
	}
	if strings.Contains(text, "草莓") || strings.Contains(text, "舰长") {
		t.Fatalf("user details leaked into timeline = %q", text)
	}
	rawText := chat.GetText(false)
	if strings.Contains(rawText, "::u]") || !strings.Contains(rawText, "::b]") {
		t.Fatalf("clickable username style = %q", rawText)
	}
	if len(registry.order) != 1 {
		t.Fatalf("user regions = %#v", registry.order)
	}
	got, ok := registry.Lookup(registry.order[0])
	if !ok || got.UserID != "42" || got.MedalName != "草莓" {
		t.Fatalf("region lookup = (%#v, %v)", got, ok)
	}
}

func TestFormatDanmakuUserCardCombinesPacketAndPublicProfile(t *testing.T) {
	message := api.DanmakuMessage{
		Username: "用户甲", UserID: "42", MedalName: "草莓", MedalLevel: 7,
		GuardLevel: 3, UserLevel: 5, WealthLevel: 8, IsAdmin: true,
	}
	profile := api.UserProfile{
		UserID: "42", Username: "主页昵称", Signature: "测试签名", Level: 6,
		Official: "官方认证", VIP: true, VIPLabel: "年度大会员",
		Followers: 123, Following: 45, ArchiveCount: 7, ArticleCount: 2, IsFollowing: true,
	}
	card := formatDanmakuUserCard(message, &profile, false, nil)
	for _, expected := range []string{"用户甲", "UID 42", "直播间身份", "房管", "舰长", "草莓 Lv.7", "弹幕 UL 5", "财富 Lv.8", "B 站公开资料", "等级 Lv.6", "粉丝 123", "视频 7 · 专栏 2", "官方认证", "年度大会员", "测试签名"} {
		if !strings.Contains(card, expected) {
			t.Fatalf("user card missing %q: %q", expected, card)
		}
	}
}

func TestFormatDanmakuUserCardKeepsPacketDetailsOnQueryFailure(t *testing.T) {
	message := api.DanmakuMessage{Username: "用户甲", UserID: "42", MedalName: "草莓", MedalLevel: 7}
	card := formatDanmakuUserCard(message, nil, false, assertError("风控校验失败"))
	if !strings.Contains(card, "草莓 Lv.7") || !strings.Contains(card, "公开资料暂不可用") || !strings.Contains(card, "风控校验失败") {
		t.Fatalf("fallback user card = %q", card)
	}
}

func TestFormatDanmakuUserCardHidesEmptyIdentityAndSelfRelation(t *testing.T) {
	message := api.DanmakuMessage{Username: "本人", UserID: "42"}
	profile := api.UserProfile{UserID: "42", Username: "本人", IsSelf: true}
	card := formatDanmakuUserCard(message, &profile, false, nil)
	if strings.Contains(card, "直播间身份") || strings.Contains(card, "关系") || strings.Contains(card, "未关注") {
		t.Fatalf("self card contains irrelevant sections = %q", card)
	}
	if canFollowDanmakuUser(message, &profile) {
		t.Fatal("current account unexpectedly allows following itself")
	}
}

func TestDanmakuUserCardHeightAdaptsToContent(t *testing.T) {
	short := formatDanmakuUserCard(api.DanmakuMessage{Username: "用户甲", UserID: "42"}, nil, true, nil)
	rich := formatDanmakuUserCard(api.DanmakuMessage{
		Username: "用户甲", UserID: "42", MedalName: "草莓", MedalLevel: 7,
		GuardLevel: 3, UserLevel: 5, WealthLevel: 8, IsAdmin: true,
	}, &api.UserProfile{
		Username: "主页昵称", Signature: "一段用于测试自适应高度的公开签名", Level: 6,
		Official: "官方认证", VIP: true, VIPLabel: "年度大会员",
		Followers: 123, Following: 45, ArchiveCount: 7, ArticleCount: 2,
	}, false, nil)
	shortHeight := danmakuUserCardHeight(short, 45)
	richHeight := danmakuUserCardHeight(rich, 45)
	if shortHeight >= richHeight {
		t.Fatalf("adaptive heights = short %d, rich %d", shortHeight, richHeight)
	}
	if shortHeight < 9 || richHeight > 24 {
		t.Fatalf("adaptive heights outside bounds = short %d, rich %d", shortHeight, richHeight)
	}
}

func TestDanmakuUserCardButtonsTouchBottomBorder(t *testing.T) {
	applyTheme()
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	screen.SetSize(45, 12)

	panel, content, actions := newDanmakuUserCardPanel()
	content.SetText("用户资料")
	actions.AddButton("关注", func() {})
	actions.AddButton("关闭", func() {})
	actions.SetFocus(1)
	panel.SetRect(0, 0, 45, 12)
	panel.Draw(screen)

	_, buttonY, _, buttonHeight := actions.GetButton(1).GetRect()
	if buttonY+buttonHeight != 11 {
		t.Fatalf("button bottom = %d, want bottom border row 11 immediately after it", buttonY+buttonHeight)
	}
}

func TestAppendDanmakuEventSpecialKinds(t *testing.T) {
	applyTheme()
	chat := tview.NewTextView().SetDynamicColors(true).SetRegions(true)
	registry := newDanmakuUserRegionRegistry()

	// 1. SuperChat
	scEvent := api.DanmakuEvent{
		Kind: api.DanmakuEventSuperChat,
		Message: api.DanmakuMessage{
			Username: "醒目用户",
			UserID:   "101",
			Text:     "支持主播！",
			Price:    30,
		},
	}
	appendDanmakuEvent(chat, scEvent, false, registry)
	scText := chat.GetText(true)
	if !strings.Contains(scText, "醒目留言 ¥30") || !strings.Contains(scText, "醒目用户：支持主播！") {
		t.Fatalf("super chat render error: %q", scText)
	}

	// 2. Guard
	guardEvent := api.DanmakuEvent{
		Kind: api.DanmakuEventGuard,
		Message: api.DanmakuMessage{
			Username: "舰长老板",
			UserID:   "102",
			Text:     "登船成为 舰长 ×1个月",
			Price:    198,
		},
	}
	appendDanmakuEvent(chat, guardEvent, true, registry)
	guardText := chat.GetText(true)
	if !strings.Contains(guardText, "[大航海]") || !strings.Contains(guardText, "登船成为 舰长") {
		t.Fatalf("guard render error: %q", guardText)
	}

	// 3. Warning
	warningEvent := api.DanmakuEvent{
		Kind: api.DanmakuEventWarning,
		Message: api.DanmakuMessage{
			Username: "超管警告",
			Text:     "涉嫌违规，请立即整改",
		},
	}
	appendDanmakuEvent(chat, warningEvent, true, registry)
	warningText := chat.GetText(true)
	if !strings.Contains(warningText, "[超管警告]") || !strings.Contains(warningText, "涉嫌违规，请立即整改") {
		t.Fatalf("warning render error: %q", warningText)
	}

	// 4. Gift with details
	giftEvent := api.DanmakuEvent{
		Kind: api.DanmakuEventGift,
		Message: api.DanmakuMessage{
			Username: "送礼大佬",
			UserID:   "103",
			Text:     "投喂 摩天大楼 ×1 (10000电池 · 连击x5)",
		},
	}
	appendDanmakuEvent(chat, giftEvent, true, registry)
	giftText := chat.GetText(true)
	if !strings.Contains(giftText, "[礼物]") || !strings.Contains(giftText, "10000电池 · 连击x5") {
		t.Fatalf("gift render error: %q", giftText)
	}
}

func TestGiftComboInPlaceUpdate(t *testing.T) {
	session := &LiveDanmakuSession{
		history:     make([]api.DanmakuEvent, 0),
		subscribers: make(map[chan struct{}]struct{}),
	}

	now := time.Now()
	// 第一击：辣条 x1 (combo 1)
	first := api.DanmakuEvent{
		Kind: api.DanmakuEventGift,
		Message: api.DanmakuMessage{
			UserID:        "1001",
			Username:      "水友A",
			GiftName:      "辣条",
			GiftCount:     1,
			GiftCombo:     1,
			BatchComboID:  "batch_001",
			GiftTotalCoin: 100,
			Text:          "投喂 辣条 ×1",
			Timestamp:     now,
		},
	}
	session.handleEvent(first)

	if len(session.history) != 1 {
		t.Fatalf("history len = %d, want 1", len(session.history))
	}
	if session.history[0].Message.GiftCombo != 1 {
		t.Fatalf("gift combo = %d, want 1", session.history[0].Message.GiftCombo)
	}

	// 第二击：同用户同批次辣条 (combo 2)
	second := api.DanmakuEvent{
		Kind: api.DanmakuEventGift,
		Message: api.DanmakuMessage{
			UserID:        "1001",
			Username:      "水友A",
			GiftName:      "辣条",
			GiftCount:     1,
			GiftCombo:     2,
			BatchComboID:  "batch_001",
			GiftTotalCoin: 200,
			Text:          "投喂 辣条 ×1 (连击x2)",
			Timestamp:     now.Add(500 * time.Millisecond),
		},
	}
	session.handleEvent(second)

	// 连击合并后历史长度仍为 1，但内容已更新为连击x2
	if len(session.history) != 1 {
		t.Fatalf("history len after combo = %d, want 1 (in-place update)", len(session.history))
	}
	if session.history[0].Message.GiftCombo != 2 {
		t.Fatalf("gift combo = %d, want 2", session.history[0].Message.GiftCombo)
	}
	if !strings.Contains(session.history[0].Message.Text, "连击x2") {
		t.Fatalf("gift text = %q, want containing 连击x2", session.history[0].Message.Text)
	}

	// 测试 updateDanmakuHistory 在连击原地更新时会触发重新渲染而不是在末尾硬追加新行
	chat := tview.NewTextView().SetMaxLines(danmakuHistoryLimit)
	rev := updateDanmakuHistory(chat, liveDanmakuSnapshot{
		history:         []api.DanmakuEvent{first},
		historyRevision: 1,
	}, 0)
	textBefore := chat.GetText(true)
	if !strings.Contains(textBefore, "投喂 辣条 ×1") || strings.Contains(textBefore, "连击x2") {
		t.Fatalf("initial chat render error: %q", textBefore)
	}

	// 连击更新快照：history 长度仍为 1，revision 变为 2
	rev = updateDanmakuHistory(chat, liveDanmakuSnapshot{
		history:         []api.DanmakuEvent{session.history[0]},
		historyRevision: 2,
	}, rev)
	textAfter := chat.GetText(true)
	if !strings.Contains(textAfter, "连击x2") {
		t.Fatalf("chat text after combo update = %q, want containing 连击x2", textAfter)
	}
	if strings.Count(textAfter, "水友A") != 1 {
		t.Fatalf("expected single line for user A, got %d occurrences in %q", strings.Count(textAfter, "水友A"), textAfter)
	}
}

func TestGuardDeduplication(t *testing.T) {
	session := &LiveDanmakuSession{
		history:     make([]api.DanmakuEvent, 0),
		subscribers: make(map[chan struct{}]struct{}),
	}
	now := time.Now()

	// 模拟 GUARD_BUY
	buy := api.DanmakuEvent{
		Kind: api.DanmakuEventGuard,
		Message: api.DanmakuMessage{
			UserID:     "5001",
			Username:   "船长大哥",
			Text:       "登船成为 舰长 ×1个月",
			GuardLevel: 3,
			Timestamp:  now,
		},
	}
	session.handleEvent(buy)

	// 模拟同时到达的 USER_TOAST_MSG
	toast := api.DanmakuEvent{
		Kind: api.DanmakuEventGuard,
		Message: api.DanmakuMessage{
			UserID:     "5001",
			Username:   "船长大哥",
			Text:       "登船成为 舰长 ×1月",
			GuardLevel: 3,
			Timestamp:  now.Add(100 * time.Millisecond),
		},
	}
	session.handleEvent(toast)

	// 防重机制应确保只生成 1 条大航海历史记录
	if len(session.history) != 1 {
		t.Fatalf("history len = %d, want 1 after duplicate guard event", len(session.history))
	}
}

type assertError string

func (err assertError) Error() string { return string(err) }
