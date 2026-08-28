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

	// 检查房间观众栏目。
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

	// 检查大航海栏目。
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
	if canChangeDanmakuUserRelation(message, &profile) {
		t.Fatal("current account unexpectedly allows changing its own relation")
	}
}

func TestCanChangeDanmakuUserRelationForFollowingStates(t *testing.T) {
	message := api.DanmakuMessage{UserID: "42"}
	if !canChangeDanmakuUserRelation(message, &api.UserProfile{IsFollowing: false}) {
		t.Fatal("unfollowed user should allow following")
	}
	if !canChangeDanmakuUserRelation(message, &api.UserProfile{IsFollowing: true}) {
		t.Fatal("followed user should allow unfollowing")
	}
}

func TestCycleFocusFollowsDanmakuPageOrder(t *testing.T) {
	applyTheme()
	app := tview.NewApplication()
	reply := tview.NewInputField()
	overview := newActionButton("直播概览", nil)
	roomManager := newActionButton("房间管理", nil)
	chat := tview.NewTextView()
	rank := tview.NewTextView()
	focusables := []tview.Primitive{reply, overview, roomManager, chat, rank}

	app.SetFocus(reply)
	for index, want := range []tview.Primitive{overview, roomManager, chat, rank, reply} {
		cycleFocus(app, focusables, false)
		if app.GetFocus() != want {
			t.Fatalf("Tab move %d focused %T, want %T", index+1, app.GetFocus(), want)
		}
	}

	delegatedApp := tview.NewApplication()
	delegatedReply := tview.NewInputField()
	delegatedOverview := newActionButton("直播概览", nil)
	delegatedReply.Focus(func(tview.Primitive) {})
	delegatedApp.SetFocus(tview.NewBox())
	cycleFocus(delegatedApp, []tview.Primitive{delegatedReply, delegatedOverview}, false)
	if delegatedApp.GetFocus() != delegatedOverview {
		t.Fatalf("delegated reply focus moved to %T, want live overview button", delegatedApp.GetFocus())
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

	// 检查醒目留言。
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

	// 检查大航海消息。
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

	// 检查平台警告。
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

	// 检查带明细的礼物消息。
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
	if session.stats.GiftEvents != 1 {
		t.Fatalf("gift events = %d, want 1", session.stats.GiftEvents)
	}
	if session.stats.GiftCount != 2 {
		t.Fatalf("gift count = %d, want 2", session.stats.GiftCount)
	}
}

func TestGiftComboStatsAccuracyWithGoldCoinAndComboSend(t *testing.T) {
	session := &LiveDanmakuSession{
		history:     make([]api.DanmakuEvent, 0),
		subscribers: make(map[chan struct{}]struct{}),
	}
	now := time.Now()

	// 模拟首击金瓜子礼物 (100金瓜子)
	first := api.DanmakuEvent{
		Kind: api.DanmakuEventGift,
		Message: api.DanmakuMessage{
			UserID:        "1001",
			Username:      "老板A",
			GiftName:      "小心心",
			GiftCount:     1,
			GiftCombo:     1,
			BatchComboID:  "batch_gold_1",
			GiftCoinType:  "gold",
			GiftTotalCoin: 100,
			Timestamp:     now,
		},
	}
	session.handleEvent(first)

	if session.stats.GiftEvents != 1 || session.stats.GiftCount != 1 || session.stats.GiftGoldCoin != 100 {
		t.Fatalf("first hit stats = %#v, want Events=1, Count=1, Gold=100", session.stats)
	}

	// 模拟连击第2击 (累计200金瓜子)
	second := api.DanmakuEvent{
		Kind: api.DanmakuEventGift,
		Message: api.DanmakuMessage{
			UserID:        "1001",
			Username:      "老板A",
			GiftName:      "小心心",
			GiftCount:     1,
			GiftCombo:     2,
			BatchComboID:  "batch_gold_1",
			GiftCoinType:  "gold",
			GiftTotalCoin: 200,
			Timestamp:     now.Add(200 * time.Millisecond),
		},
	}
	session.handleEvent(second)

	if session.stats.GiftEvents != 1 || session.stats.GiftCount != 2 || session.stats.GiftGoldCoin != 200 {
		t.Fatalf("second hit stats = %#v, want Events=1, Count=2, Gold=200", session.stats)
	}

	// 模拟 B 站发送的 COMBO_SEND 结算广播（累计总数2，累计金瓜子200）
	comboSend := api.DanmakuEvent{
		Kind:    api.DanmakuEventGift,
		Command: "COMBO_SEND",
		Message: api.DanmakuMessage{
			UserID:        "1001",
			Username:      "老板A",
			GiftName:      "小心心",
			GiftCount:     2,
			GiftCombo:     2,
			BatchComboID:  "batch_gold_1",
			GiftTotalCoin: 200,
			Timestamp:     now.Add(500 * time.Millisecond),
		},
	}
	session.handleEvent(comboSend)

	// COMBO_SEND 不应导致礼物数量或金瓜子翻倍
	if session.stats.GiftEvents != 1 || session.stats.GiftCount != 2 || session.stats.GiftGoldCoin != 200 {
		t.Fatalf("after COMBO_SEND stats = %#v, want Events=1, Count=2, Gold=200 (no double count)", session.stats)
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
		Command: "GUARD_BUY",
		Kind:    api.DanmakuEventGuard,
		Message: api.DanmakuMessage{
			UserID:     "5001",
			Username:   "船长大哥",
			Text:       "登船成为 舰长 ×1个月",
			GuardLevel: 3,
			GiftCount:  1,
			Timestamp:  now,
		},
	}
	session.handleEvent(buy)

	// 模拟同时到达的 USER_TOAST_MSG
	toast := api.DanmakuEvent{
		Command: "USER_TOAST_MSG",
		Kind:    api.DanmakuEventGuard,
		Message: api.DanmakuMessage{
			UserID:     "5001",
			Username:   "船长大哥",
			Text:       "登船成为 舰长 ×1月",
			GuardLevel: 3,
			GiftCount:  1,
			Timestamp:  now.Add(100 * time.Millisecond),
		},
	}
	session.handleEvent(toast)

	// 防重机制应确保只生成 1 条大航海历史记录，且统计数量只为 1
	if len(session.history) != 1 {
		t.Fatalf("history len = %d, want 1 after duplicate guard event", len(session.history))
	}
	if session.stats.GuardCount != 1 {
		t.Fatalf("guard count = %d, want 1 (deduped)", session.stats.GuardCount)
	}
}

func TestRenderOnlineRankPreservesScrollOffset(t *testing.T) {
	view := tview.NewTextView().SetScrollable(true)
	snapshot := liveDanmakuSnapshot{
		viewerOnline: 50,
		viewerKnown:  true,
		onlineRank: []api.OnlineRankMember{
			{UserID: "1", Username: "用户1", Rank: 1},
			{UserID: "2", Username: "用户2", Rank: 2},
			{UserID: "3", Username: "用户3", Rank: 3},
			{UserID: "4", Username: "用户4", Rank: 4},
			{UserID: "5", Username: "用户5", Rank: 5},
			{UserID: "6", Username: "用户6", Rank: 6},
			{UserID: "7", Username: "用户7", Rank: 7},
			{UserID: "8", Username: "用户8", Rank: 8},
		},
	}

	renderOnlineRank(view, snapshot, rankTabAudience)
	view.ScrollTo(3, 0)
	row, col := view.GetScrollOffset()
	if row != 3 {
		t.Fatalf("scroll row before refresh = %d, want 3", row)
	}

	// 模拟弹幕事件到来触发重新渲染
	renderOnlineRank(view, snapshot, rankTabAudience)
	rowAfter, colAfter := view.GetScrollOffset()
	if rowAfter != 3 || colAfter != col {
		t.Fatalf("scroll offset after refresh = (%d, %d), want (%d, %d)", rowAfter, colAfter, 3, col)
	}
}

type assertError string

func (err assertError) Error() string { return string(err) }

func TestConfirmModalOverlayNoBleedThrough(t *testing.T) {
	applyTheme()
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	screenWidth, screenHeight := 80, 12
	screen.SetSize(screenWidth, screenHeight)

	pages := tview.NewPages()

	// 模拟弹幕主页：底部有 reply 输入框与操作栏
	reply := tview.NewTextArea()
	reply.SetBorder(true)
	reply.SetTitle(" 弹幕回复 ")
	reply.SetText("这是一段底层输入框的内容，包含很长的文字", false)

	body := tview.NewFlex().SetDirection(tview.FlexRow)
	chat := tview.NewTextView()
	chat.SetBorder(true)
	chat.SetText(strings.Repeat("弹幕消息滚动中...\n", 20))
	body.AddItem(chat, 0, 1, false)
	body.AddItem(reply, 3, 0, false)

	root := workspacePage(workspaceHeader("弹幕互动"), body, nil)
	confirm := newConfirmModal("下播确认").
		SetText("确定下播并退出吗？").
		AddButtons([]string{"取消", "下播并退出"})

	pages.AddPage("main", root, true, true)
	pages.AddPage("confirm-stop", confirm, true, false)

	pages.SetRect(0, 0, screenWidth, screenHeight)
	pages.Draw(screen)

	// 显示确认弹窗
	pages.ShowPage("confirm-stop")
	pages.SendToFront("confirm-stop")
	pages.Draw(screen)

	// 获取弹窗面板的实际绘制矩形
	modalX, modalY, modalW, modalH := confirm.panel.GetRect()
	if modalW <= 0 || modalH <= 0 {
		t.Fatalf("modal rect invalid: (%d, %d, %d, %d)", modalX, modalY, modalW, modalH)
	}

	// 验证弹窗矩形内部绝对不包含底层控件的文字或边框穿插
	for y := modalY + 1; y < modalY+modalH-1; y++ {
		var rowContent strings.Builder
		for x := modalX + 1; x < modalX+modalW-1; x++ {
			r, _, _, _ := screen.GetContent(x, y)
			if r == 0 {
				r = ' '
			}
			rowContent.WriteRune(r)
		}
		str := rowContent.String()
		if strings.Contains(str, "底层输入框") || strings.Contains(str, "弹幕回复") {
			t.Errorf("Row %d inside modal contains leaked background text: %q", y, str)
		}
		if strings.Contains(str, "┌") || strings.Contains(str, "└") {
			t.Errorf("Row %d inside modal contains leaked border runes: %q", y, str)
		}
	}
}

func TestDanmakuEmojiTimestampAlwaysLeftAligned(t *testing.T) {
	applyTheme()

	testCases := []struct {
		name     string
		msg1Text string
		msg2Text string
	}{
		{name: "bilibili emote tag", msg1Text: "太强了 [doge]", msg2Text: "确实很强"},
		{name: "bilibili dog emote tag", msg1Text: "[dog]", msg2Text: "下一条弹幕"},
		{name: "wrapped bilibili dog emote tag", msg1Text: "这是一条需要在较窄窗口换行显示的长弹幕 [dog]", msg2Text: "下一条弹幕"},
		{name: "chinese emote tag", msg1Text: "给主播点赞 [点赞]", msg2Text: "加油加油"},
		{name: "heart with variation selector", msg1Text: "爱你哟 ❤️", msg2Text: "笔芯"},
		{name: "warning emoji with variation selector", msg1Text: "注意危险 ⚠️", msg2Text: "收到收到"},
		{name: "emoji with trailing spaces", msg1Text: "笑死我了 🤣   ", msg2Text: "哈哈哈"},
		{name: "trailing zero width space", msg1Text: "特殊表情 🌸\u200b\ufeff", msg2Text: "好看好看"},
		{name: "trailing carriage return", msg1Text: "回车结尾 🐶\r\n", msg2Text: "汪汪汪"},
		{name: "multiple emojis", msg1Text: "连续表情 🐶🐱🐭", msg2Text: "好多动物"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			screen := tcell.NewSimulationScreen("UTF-8")
			if err := screen.Init(); err != nil {
				t.Fatal(err)
			}
			defer screen.Fini()
			screenWidth, screenHeight := 60, 10
			screen.SetSize(screenWidth, screenHeight)

			chat := tview.NewTextView()
			chat.SetScrollable(true)
			chat.SetDynamicColors(true)
			chat.SetRegions(true)
			chat.SetWordWrap(true)
			chat.SetBorder(true)
			registry := newDanmakuUserRegionRegistry()

			ev1 := api.DanmakuEvent{
				Kind: api.DanmakuEventMessage,
				Message: api.DanmakuMessage{
					Username:  "UserA",
					Text:      tc.msg1Text,
					Timestamp: time.Date(2026, 1, 1, 15, 4, 5, 0, time.UTC),
				},
			}
			ev2 := api.DanmakuEvent{
				Kind: api.DanmakuEventMessage,
				Message: api.DanmakuMessage{
					Username:  "UserB",
					Text:      tc.msg2Text,
					Timestamp: time.Date(2026, 1, 1, 15, 4, 6, 0, time.UTC),
				},
			}

			chat.SetRect(0, 0, screenWidth, screenHeight)
			appendDanmakuEvent(chat, ev1, false, registry)
			chat.Draw(screen)
			appendDanmakuEvent(chat, ev2, true, registry)
			chat.Draw(screen)

			foundTimestamp := false
			for y := 1; y < screenHeight-1; y++ {
				var line strings.Builder
				for x := 0; x < screenWidth; x++ {
					r, _, _, _ := screen.GetContent(x, y)
					if r == 0 {
						r = ' '
					}
					line.WriteRune(r)
				}
				str := line.String()
				if strings.HasPrefix(str, "│") && strings.HasSuffix(str, "│") {
					runes := []rune(str)
					content := string(runes[1 : len(runes)-1])
					trimmed := strings.TrimLeft(content, " ")
					if strings.HasPrefix(trimmed, "15:04:06") {
						foundTimestamp = true
						if !strings.HasPrefix(content, "15:04:06") {
							t.Errorf("%s, Row %d: timestamp 15:04:06 not left aligned! Content: %q", tc.name, y, content)
						}
					}
				}
			}
			if !foundTimestamp {
				t.Fatalf("%s: expected timestamp 15:04:06 not found in rendered screen", tc.name)
			}
		})
	}
}

func TestSuperAdminWarningModal(t *testing.T) {
	applyTheme()
	modal := newConfirmModal("平台超管警告")
	modal.SetText("涉嫌违规内容，请立即整改！")
	modal.AddButtons([]string{"我已知晓并整改"})

	if modal.panel.GetTitle() != " 平台超管警告 " {
		t.Fatalf("unexpected title: %q", modal.panel.GetTitle())
	}
	if modal.form.GetButtonCount() != 1 {
		t.Fatalf("expected 1 button, got %d", modal.form.GetButtonCount())
	}
	if modal.form.GetButton(0).GetLabel() != "我已知晓并整改" {
		t.Fatalf("unexpected button label: %q", modal.form.GetButton(0).GetLabel())
	}

	doneCalled := false
	modal.SetDoneFunc(func(index int, label string) {
		doneCalled = true
		if index != 0 || label != "我已知晓并整改" {
			t.Fatalf("unexpected done callback: (%d, %q)", index, label)
		}
	})
	// 模拟点击按钮。
	modal.form.GetButton(0).SetSelectedFunc(func() {
		if modal.done != nil {
			modal.done(0, "我已知晓并整改")
		}
	})
	// 触发完成回调。
	modal.done(0, "我已知晓并整改")
	if !doneCalled {
		t.Fatal("expected done handler to be called")
	}
}

func TestFormatDanmakuUserCardShowsDanmakuText(t *testing.T) {
	message := api.DanmakuMessage{
		Username: "测试老哥",
		UserID:   "999",
		Text:     "主播好菜啊退订了",
	}
	card := formatDanmakuUserCard(message, nil, false, nil)
	if !strings.Contains(card, "测试老哥") || !strings.Contains(card, "UID 999") {
		t.Fatalf("card missing username or uid: %q", card)
	}
	if !strings.Contains(card, "发言") || !strings.Contains(card, "主播好菜啊退订了") {
		t.Fatalf("card missing danmaku message context: %q", card)
	}
}

func TestMuteConfirmModalButtonsAndDurations(t *testing.T) {
	applyTheme()
	modal := newConfirmModal("禁言用户")
	durations := []string{"取消", "本场", "2小时", "4小时", "1天", "7天", "永久"}
	modal.SetText("确定禁言 用户甲 吗？请选择禁言时长：").AddButtons(durations)

	if modal.form.GetButtonCount() != len(durations) {
		t.Fatalf("expected %d buttons, got %d", len(durations), modal.form.GetButtonCount())
	}
	for i, want := range durations {
		got := strings.TrimSpace(modal.form.GetButton(i).GetLabel())
		if got != want {
			t.Fatalf("button %d label = %q, want %q", i, got, want)
		}
	}
	if modal.form.RowCount() != 3 {
		t.Fatalf("mute actions use %d rows, want 3", modal.form.RowCount())
	}
	if modal.form.PreferredHeight() != 5 {
		t.Fatalf("mute actions height = %d, want 5", modal.form.PreferredHeight())
	}
}

func TestAvailableRoomAdminLevelChoices(t *testing.T) {
	ordinaryOnly := availableRoomAdminLevelChoices(false, false)
	if len(ordinaryOnly) != 1 || ordinaryOnly[0].level != 1 || ordinaryOnly[0].label != "普通房管" {
		t.Fatalf("ordinary choices = %#v", ordinaryOnly)
	}

	all := availableRoomAdminLevelChoices(true, true)
	if len(all) != 2 || all[0].level != 1 || all[1].level != 2 || all[1].label != "高级房管" {
		t.Fatalf("enabled choices = %#v", all)
	}
}
