package tui

import (
	"context"
	"sync"
	"time"

	"bili-live-tui/internal/api"
	"github.com/rivo/tview"
)

const danmakuHistoryLimit = 500

const onlineRankRefreshInterval = 15 * time.Second

// LiveDanmakuSession 独立于任意终端页面管理 WebSocket。
// 弹幕页和概览可以反复创建，而会话继续接收消息并保存有限长度的内存历史。
type LiveDanmakuSession struct {
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}

	mu              sync.RWMutex
	history         []api.DanmakuEvent
	historyRevision uint64
	status          string
	placeholder     string
	draft           string
	online          int64
	onlineKnown     bool
	onlineUpdatedAt time.Time
	viewerOnline    int64
	viewerKnown     bool
	onlineRank      []api.OnlineRankMember
	onlineRankError string
	stats           api.LiveSessionStats
	subscribers     map[chan struct{}]struct{}
	closed          bool
}

type liveDanmakuSnapshot struct {
	history         []api.DanmakuEvent
	historyRevision uint64
	status          string
	placeholder     string
	draft           string
	online          int64
	onlineKnown     bool
	viewerOnline    int64
	viewerKnown     bool
	onlineRank      []api.OnlineRankMember
	onlineRankError string
}

// NewLiveDanmakuSession 启动一个会自动重连的认证弹幕流。
// 只有整个直播会话结束时才应调用 Close。
func NewLiveDanmakuSession(ctx context.Context, client *api.Client, roomID, sessdata, biliJCT string) *LiveDanmakuSession {
	connect := func(ctx context.Context) (danmakuStreamConnection, error) {
		return client.ConnectDanmakuWithCookie(ctx, roomID, sessdata, biliJCT)
	}
	session := newLiveDanmakuSessionWithConnector(ctx, connect)
	go session.seedPopularityFromRoom(client, roomID)
	go session.pollOnlineRank(client, roomID, sessdata, biliJCT)
	return session
}

func newLiveDanmakuSessionWithConnector(ctx context.Context, connect danmakuStreamConnector) *LiveDanmakuSession {
	if ctx == nil {
		ctx = context.Background()
	}
	sessionCtx, cancel := context.WithCancel(ctx)
	session := &LiveDanmakuSession{
		ctx:         sessionCtx,
		cancel:      cancel,
		done:        make(chan struct{}),
		status:      "正在连接弹幕服务……",
		placeholder: "正在连接弹幕服务器……",
		subscribers: make(map[chan struct{}]struct{}),
	}

	statusView := tview.NewTextView().SetText(session.status)
	chatView := tview.NewTextView().SetText(session.placeholder)
	queueState := func(update func()) {
		update()
		session.updateConnectionState(statusView.GetText(true), chatView.GetText(true))
	}
	onEvent := func(event api.DanmakuEvent) {
		if event.Kind == api.DanmakuEventOnline {
			session.ObservePopularity(event.Online, time.Now())
			return
		}
		session.mu.Lock()
		changed := false
		switch event.Kind {
		case api.DanmakuEventGift:
			session.stats.Observe(event)
			changed = true
		}
		if changed {
			session.notifyLocked()
		}
		session.mu.Unlock()
	}
	go runDanmakuStreamWithConnector(sessionCtx, connect, queueState, session.appendEvent, onEvent, statusView, chatView, session.done)
	return session
}

func (s *LiveDanmakuSession) updateConnectionState(status, placeholder string) {
	s.mu.Lock()
	changed := s.status != status || s.placeholder != placeholder
	s.status = status
	s.placeholder = placeholder
	if changed {
		s.notifyLocked()
	}
	s.mu.Unlock()
}

func (s *LiveDanmakuSession) appendEvent(event api.DanmakuEvent) {
	if event.Kind == api.DanmakuEventOnline || event.Kind == api.DanmakuEventConnected {
		return
	}
	s.mu.Lock()
	s.history = append(s.history, event)
	if overflow := len(s.history) - danmakuHistoryLimit; overflow > 0 {
		copy(s.history, s.history[overflow:])
		s.history = s.history[:danmakuHistoryLimit]
	}
	s.historyRevision++
	s.notifyLocked()
	s.mu.Unlock()
}

func (s *LiveDanmakuSession) snapshot() liveDanmakuSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return liveDanmakuSnapshot{
		history:         append([]api.DanmakuEvent(nil), s.history...),
		historyRevision: s.historyRevision,
		status:          s.status,
		placeholder:     s.placeholder,
		draft:           s.draft,
		online:          s.online,
		onlineKnown:     s.onlineKnown,
		viewerOnline:    s.viewerOnline,
		viewerKnown:     s.viewerKnown,
		onlineRank:      append([]api.OnlineRankMember(nil), s.onlineRank...),
		onlineRankError: s.onlineRankError,
	}
}

func (s *LiveDanmakuSession) pollOnlineRank(client *api.Client, roomID, sessdata, biliJCT string) {
	if client == nil || s.ctx == nil {
		return
	}
	refresh := func() {
		requestCtx, cancel := context.WithTimeout(s.ctx, 8*time.Second)
		rank, err := client.GetOnlineGoldRankWithCookie(requestCtx, roomID, sessdata, biliJCT)
		cancel()
		s.mu.Lock()
		if err != nil {
			message := err.Error()
			if s.onlineRankError != message {
				s.onlineRankError = message
				s.notifyLocked()
			}
			s.mu.Unlock()
			return
		}
		s.viewerOnline = rank.Online
		s.viewerKnown = true
		s.onlineRank = append(s.onlineRank[:0], rank.Members...)
		s.onlineRankError = ""
		s.notifyLocked()
		s.mu.Unlock()
	}
	refresh()
	ticker := time.NewTicker(onlineRankRefreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			refresh()
		}
	}
}

func (s *LiveDanmakuSession) subscribe() (<-chan struct{}, func()) {
	updates := make(chan struct{}, 1)
	s.mu.Lock()
	if s.closed {
		close(updates)
		s.mu.Unlock()
		return updates, func() {}
	}
	s.subscribers[updates] = struct{}{}
	// 挂载后始终安排一次刷新，消除页面首次快照与注册更新之间的极小窗口，
	// 确保这段时间收到的消息能立即显示。
	updates <- struct{}{}
	s.mu.Unlock()
	return updates, func() {
		s.mu.Lock()
		if _, exists := s.subscribers[updates]; exists {
			delete(s.subscribers, updates)
			close(updates)
		}
		s.mu.Unlock()
	}
}

func (s *LiveDanmakuSession) notifyLocked() {
	for subscriber := range s.subscribers {
		select {
		case subscriber <- struct{}{}:
		default:
		}
	}
}

func (s *LiveDanmakuSession) SetDraft(draft string) {
	s.mu.Lock()
	s.draft = draft
	s.mu.Unlock()
}

func (s *LiveDanmakuSession) ClearHistory() {
	s.mu.Lock()
	s.history = nil
	s.historyRevision++
	s.placeholder = "暂无弹幕记录。"
	s.notifyLocked()
	s.mu.Unlock()
}

func (s *LiveDanmakuSession) Stats() api.LiveSessionStats {
	s.mu.RLock()
	defer s.mu.RUnlock()
	stats := s.stats
	stats.Popularity = s.online
	stats.PopularityKnown = s.onlineKnown
	return stats
}

// Popularity 返回弹幕心跳中的当前人气。
// B 站的房间接口和弹幕心跳可能来自不同时间点，因此概览页面优先使用同一会话的值。
func (s *LiveDanmakuSession) Popularity() (int64, bool) {
	if s == nil {
		return 0, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.online, s.onlineKnown
}

// ObservePopularity 记录最近一次收到的弹幕心跳人气，并通知已挂载的页面刷新。
// 页面始终显示会话中最后一次收到的有效数据。
func (s *LiveDanmakuSession) ObservePopularity(value int64, observedAt time.Time) {
	if s == nil {
		return
	}
	if value <= 0 {
		return
	}
	if observedAt.IsZero() {
		observedAt = time.Now()
	}
	s.mu.Lock()
	if s.onlineKnown && observedAt.Before(s.onlineUpdatedAt) {
		s.mu.Unlock()
		return
	}
	changed := !s.onlineKnown || s.online != value
	s.online = value
	s.onlineKnown = true
	s.onlineUpdatedAt = observedAt
	if changed {
		s.notifyLocked()
	}
	s.mu.Unlock()
}

func (s *LiveDanmakuSession) seedPopularityFromRoom(client *api.Client, roomID string) {
	if client == nil || s == nil || s.ctx == nil {
		return
	}
	requestCtx, cancel := context.WithTimeout(s.ctx, 8*time.Second)
	snapshot, err := client.GetRoomSnapshot(requestCtx, roomID)
	cancel()
	if err == nil && snapshot.OnlineKnown {
		s.ObservePopularity(snapshot.Online, time.Now())
	}
}

func (s *LiveDanmakuSession) Close() {
	if s == nil {
		return
	}
	s.cancel()
	<-s.done
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		for subscriber := range s.subscribers {
			close(subscriber)
			delete(s.subscribers, subscriber)
		}
	}
	s.mu.Unlock()
}
