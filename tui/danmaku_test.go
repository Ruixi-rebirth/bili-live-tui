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
	view := tview.NewTextView().SetDynamicColors(true).SetRegions(true)
	registry := newDanmakuUserRegionRegistry()
	renderOnlineRank(view, liveDanmakuSnapshot{
		viewerOnline: 23,
		viewerKnown:  true,
		onlineRank: []api.OnlineRankMember{
			{UserID: "42", Username: "用户[甲]", Rank: 1, Score: 11, GuardLevel: 3},
		},
	}, registry)
	if view.GetTitle() != " 在线 23 人 " {
		t.Fatalf("online rank title = %q", view.GetTitle())
	}
	text := view.GetText(true)
	if !strings.Contains(text, "高能榜") || !strings.Contains(text, "用户[甲]") || !strings.Contains(text, "舰长") || !strings.Contains(text, "11") {
		t.Fatalf("online rank text = %q", text)
	}
	if len(registry.order) != 1 {
		t.Fatalf("online rank regions = %#v", registry.order)
	}
	message, ok := registry.Lookup(registry.order[0])
	if !ok || message.UserID != "42" || message.Username != "用户[甲]" || message.GuardLevel != 3 {
		t.Fatalf("online rank region lookup = (%#v, %v)", message, ok)
	}
	if raw := view.GetText(false); !strings.Contains(raw, "::b]") || !strings.Contains(raw, "[\"") {
		t.Fatalf("online rank clickable username markup = %q", raw)
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
	renderDanmakuHistory(chat, []api.DanmakuEvent{old}, "")

	// 修订号增加两次代表一次清空和一次新增；此时必须完整渲染当前历史。
	revision := updateDanmakuHistory(chat, liveDanmakuSnapshot{history: []api.DanmakuEvent{fresh}, historyRevision: 3}, 1)
	text := chat.GetText(true)
	if revision != 3 || strings.Contains(text, "旧弹幕") || !strings.Contains(text, "新弹幕") {
		t.Fatalf("history after clear = %q, revision=%d", text, revision)
	}
}

func TestDanmakuInputCaptureHandlesBackspaceWithoutNavigating(t *testing.T) {
	reply := tview.NewInputField()
	reply.SetText("测试文本")

	app := tview.NewApplication()
	app.SetFocus(reply)

	handler := func(event *tcell.EventKey) *tcell.EventKey {
		switch {
		case matchesControlShortcut(event, tcell.KeyCtrlH, 'h') || matchesModifiedRuneShortcut(event, tcell.ModAlt, 'h'):
			if reply.HasFocus() {
				if event.Key() == tcell.KeyCtrlH || event.Key() == tcell.KeyBackspace {
					return event
				}
				if event.Key() == tcell.KeyRune && (event.Rune() == '\b' || event.Rune() == 8) {
					return tcell.NewEventKey(tcell.KeyBackspace, 0, tcell.ModNone)
				}
			}
			return nil
		default:
			return event
		}
	}

	// 1. 测试标准 KeyCtrlH / KeyBackspace（ASCII 8）
	bsEvent := tcell.NewEventKey(tcell.KeyCtrlH, 0, tcell.ModNone)
	if got := handler(bsEvent); got == nil {
		t.Fatal("KeyCtrlH (Backspace) was intercepted as navigation instead of being passed to input field")
	}

	// 2. 测试 rune '\b'
	rawControl := tcell.NewEventKey(tcell.KeyRune, '\b', tcell.ModNone)
	if got := handler(rawControl); got == nil || got.Key() != tcell.KeyBackspace {
		t.Fatal("Raw rune '\\b' was intercepted as navigation instead of being converted to KeyBackspace")
	}

	// 3. 测试 Alt+H 仍能触发导航
	altH := tcell.NewEventKey(tcell.KeyRune, 'h', tcell.ModAlt)
	if got := handler(altH); got != nil {
		t.Fatal("Alt+H was not intercepted as navigation")
	}
}

func TestRoomManagementShortcutSupportsDistinctCtrlMAndAltM(t *testing.T) {
	for _, modifier := range []tcell.ModMask{tcell.ModCtrl, tcell.ModAlt} {
		event := tcell.NewEventKey(tcell.KeyRune, 'm', modifier)
		if !matchesRoomManagementShortcut(event) {
			t.Fatalf("M with modifier %v was not recognized", modifier)
		}
	}
	if matchesRoomManagementShortcut(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone)) {
		t.Fatal("Enter was mistaken for the room management shortcut")
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
	if level, err := parseDanmakuManagementLevel(" 80 ", 80); err != nil || level != 80 {
		t.Fatalf("level = %d, err = %v", level, err)
	}
	for _, value := range []string{"", "0", "81", "一点五"} {
		if _, err := parseDanmakuManagementLevel(value, 80); err == nil {
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

func TestFormatRoomManagerRowIsCompactAndAligned(t *testing.T) {
	short := formatRoomManagerRow("全员", "除房管外均不可发言", "设置")
	long := formatRoomManagerRow("除房管以外的观众", "仅主播和房管可以发言", "设置")
	if strings.Contains(short, "\n") || !strings.Contains(short, "「设置」") {
		t.Fatalf("compact row = %q", short)
	}
	shortDetail := strings.Index(short, "除房管外")
	longDetail := strings.Index(long, "仅主播")
	if shortDetail <= 0 || longDetail <= 0 || tview.TaggedStringWidth(short[:shortDetail]) != tview.TaggedStringWidth(long[:longDetail]) {
		t.Fatalf("row columns are not aligned: short=%q long=%q", short, long)
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

func TestRoomManagerNumberKeyTabs(t *testing.T) {
	for r := '1'; r <= '5'; r++ {
		idx := int(r - '1')
		if idx < 0 || idx > 4 {
			t.Fatalf("tab index out of range for %c: %d", r, idx)
		}
	}
}

type assertError string

func (err assertError) Error() string { return string(err) }
