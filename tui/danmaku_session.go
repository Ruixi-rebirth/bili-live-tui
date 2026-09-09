package tui

import (
	"context"
	"sync"
	"time"

	"bili-live-tui/internal/api"
)

const danmakuHistoryLimit = 500

const onlineRankRefreshInterval = 15 * time.Second

type danmakuConnectionPhase uint8

const (
	danmakuConnectionConnecting danmakuConnectionPhase = iota
	danmakuConnectionAuthenticating
	danmakuConnectionConnected
	danmakuConnectionRetrying
)

type danmakuConnectionState struct {
	phase      danmakuConnectionPhase
	attempt    int
	endpoint   string
	lastError  string
	retryDelay time.Duration
	confirmed  bool
}

func (state danmakuConnectionState) connected() bool {
	return state.phase == danmakuConnectionConnected
}

type danmakuStreamConnection interface {
	Events() <-chan api.DanmakuEvent
	Errors() <-chan error
	Close()
}

type danmakuStreamConnector func(context.Context) (danmakuStreamConnection, error)

// LiveDanmakuSession 独立于任意终端页面管理 WebSocket。
// 弹幕页和概览可以反复创建，而会话继续接收消息并保存有限长度的内存历史。
type LiveDanmakuSession struct {
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}

	mu              sync.RWMutex
	history         []api.DanmakuEvent
	historyRevision uint64
	connection      danmakuConnectionState
	draft           string
	online          int64
	onlineKnown     bool
	onlineUpdatedAt time.Time
	viewerOnline    int64
	viewerKnown     bool
	onlineRank      []api.OnlineRankMember
	onlineRankError string
	guardTotal      int64
	guardKnown      bool
	guardMembers    []api.GuardMember
	guardError      string
	stats           api.LiveSessionStats
	liveState       string
	subscribers     map[chan struct{}]struct{}
	closed          bool
}

type liveDanmakuSnapshot struct {
	history         []api.DanmakuEvent
	historyRevision uint64
	connection      danmakuConnectionState
	draft           string
	online          int64
	onlineKnown     bool
	viewerOnline    int64
	viewerKnown     bool
	onlineRank      []api.OnlineRankMember
	onlineRankError string
	guardTotal      int64
	guardKnown      bool
	guardMembers    []api.GuardMember
	guardError      string
	stats           api.LiveSessionStats
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
		connection:  danmakuConnectionState{phase: danmakuConnectionConnecting, attempt: 1},
		subscribers: make(map[chan struct{}]struct{}),
	}
	go runDanmakuStreamWithConnector(sessionCtx, connect, session.updateConnectionState, session.handleEvent, session.done)
	return session
}

func (s *LiveDanmakuSession) updateConnectionState(state danmakuConnectionState) {
	s.mu.Lock()
	if s.connection != state {
		s.connection = state
		s.notifyLocked()
	}
	s.mu.Unlock()
}

func (s *LiveDanmakuSession) handleEvent(event api.DanmakuEvent) {
	if event.Kind == api.DanmakuEventOnline {
		s.ObservePopularity(event.Online, time.Now())
		return
	}
	if event.Kind == api.DanmakuEventConnected {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if event.Kind == api.DanmakuEventSystem && (event.Command == "LIVE" || event.Command == "PREPARING") {
		if s.liveState == event.Command {
			return
		}
		s.liveState = event.Command
	}
	if event.Kind == api.DanmakuEventGift || event.Kind == api.DanmakuEventSuperChat || event.Kind == api.DanmakuEventGuard {
		s.stats.Observe(event)
	}

	// 大航海防重：B 站上舰常同时推送 GUARD_BUY 与 USER_TOAST_MSG，避免短时间内重复记录同一笔开通
	if event.Kind == api.DanmakuEventGuard {
		for i := len(s.history) - 1; i >= 0 && i >= len(s.history)-5; i-- {
			h := s.history[i]
			if h.Kind == api.DanmakuEventGuard &&
				h.Message.UserID != "" && h.Message.UserID == event.Message.UserID &&
				event.Message.Timestamp.Sub(h.Message.Timestamp) < 5*time.Second {
				return
			}
		}
	}

	// 连击礼物合并：当用户连续送出同种礼物时原地更新连击次数与总价值，避免暴风刷屏
	if event.Kind == api.DanmakuEventGift && (event.Message.GiftCombo > 1 || event.Message.BatchComboID != "") {
		for i := len(s.history) - 1; i >= 0 && i >= len(s.history)-10; i-- {
			h := &s.history[i]
			if h.Kind != api.DanmakuEventGift {
				continue
			}
			sameUser := (event.Message.UserID != "" && h.Message.UserID == event.Message.UserID) ||
				(event.Message.Username != "" && h.Message.Username == event.Message.Username)
			sameGift := h.Message.GiftName == event.Message.GiftName
			withinWindow := event.Message.Timestamp.Sub(h.Message.Timestamp) < 15*time.Second
			sameBatch := event.Message.BatchComboID != "" && h.Message.BatchComboID == event.Message.BatchComboID

			if sameUser && sameGift && (sameBatch || withinWindow) {
				if event.Message.GiftCombo > h.Message.GiftCombo {
					h.Message.GiftCombo = event.Message.GiftCombo
				}
				if event.Message.GiftTotalCoin > h.Message.GiftTotalCoin {
					h.Message.GiftTotalCoin = event.Message.GiftTotalCoin
				}
				if event.Message.BatchComboID != "" {
					h.Message.BatchComboID = event.Message.BatchComboID
				}
				h.Message.Text = event.Message.Text
				h.Message.Timestamp = event.Message.Timestamp
				s.historyRevision++
				s.notifyLocked()
				return
			}
		}
	}

	s.history = append(s.history, event)
	if overflow := len(s.history) - danmakuHistoryLimit; overflow > 0 {
		copy(s.history, s.history[overflow:])
		s.history = s.history[:danmakuHistoryLimit]
	}
	s.historyRevision++
	s.notifyLocked()
}

func (s *LiveDanmakuSession) snapshot() liveDanmakuSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return liveDanmakuSnapshot{
		history:         append([]api.DanmakuEvent(nil), s.history...),
		historyRevision: s.historyRevision,
		connection:      s.connection,
		draft:           s.draft,
		online:          s.online,
		onlineKnown:     s.onlineKnown,
		viewerOnline:    s.viewerOnline,
		viewerKnown:     s.viewerKnown,
		onlineRank:      append([]api.OnlineRankMember(nil), s.onlineRank...),
		onlineRankError: s.onlineRankError,
		guardTotal:      s.guardTotal,
		guardKnown:      s.guardKnown,
		guardMembers:    append([]api.GuardMember(nil), s.guardMembers...),
		guardError:      s.guardError,
		stats:           s.stats,
	}
}

func (s *LiveDanmakuSession) pollOnlineRank(client *api.Client, roomID, sessdata, biliJCT string) {
	if client == nil || s.ctx == nil {
		return
	}
	refresh := func() {
		requestCtx, cancel := context.WithTimeout(s.ctx, 8*time.Second)
		defer cancel()
		rank, rankErr := client.GetOnlineGoldRankWithCookie(requestCtx, roomID, sessdata, biliJCT)
		guard, guardErr := client.GetGuardTopListWithCookie(requestCtx, roomID, sessdata, biliJCT)
		s.mu.Lock()
		defer s.mu.Unlock()
		changed := false
		if rankErr != nil || guardErr != nil {
			client.CloseIdleConnections()
		}
		if rankErr != nil {
			if s.onlineRankError != rankErr.Error() {
				s.onlineRankError = rankErr.Error()
				changed = true
			}
		} else {
			s.viewerOnline = rank.Online
			s.viewerKnown = true
			s.onlineRank = append(s.onlineRank[:0], rank.Members...)
			if s.onlineRankError != "" {
				s.onlineRankError = ""
				changed = true
			}
			changed = true
		}
		if guardErr != nil {
			if s.guardError != guardErr.Error() {
				s.guardError = guardErr.Error()
				changed = true
			}
		} else {
			s.guardTotal = guard.Total
			s.guardKnown = true
			s.guardMembers = append(s.guardMembers[:0], guard.Members...)
			if s.guardError != "" {
				s.guardError = ""
				changed = true
			}
			changed = true
		}
		if changed {
			s.notifyLocked()
		}
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
	if value < 0 {
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

func runDanmakuStreamWithConnector(
	ctx context.Context,
	connect danmakuStreamConnector,
	updateState func(danmakuConnectionState),
	handleEvent func(api.DanmakuEvent),
	done chan<- struct{},
) {
	defer close(done)
	failures := 0
	for {
		attempt := failures + 1
		updateState(danmakuConnectionState{phase: danmakuConnectionConnecting, attempt: attempt})
		stream, err := connect(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			failures++
			delay := danmakuRetryDelay(failures)
			updateState(danmakuConnectionState{
				phase:      danmakuConnectionRetrying,
				attempt:    attempt,
				lastError:  err.Error(),
				retryDelay: delay,
			})
			if !waitDanmakuRetry(ctx, delay) {
				return
			}
			continue
		}

		endpoint := ""
		if details, ok := stream.(interface{ Endpoint() string }); ok {
			endpoint = details.Endpoint()
		}
		updateState(danmakuConnectionState{
			phase:    danmakuConnectionAuthenticating,
			attempt:  attempt,
			endpoint: endpoint,
		})

		confirmed := false
		ended := false
		lastError := ""
		events := stream.Events()
		errors := stream.Errors()
		for !ended {
			select {
			case <-ctx.Done():
				stream.Close()
				ended = true
			case event, ok := <-events:
				if !ok {
					events = nil
					ended = errors == nil
					continue
				}
				if !confirmed && event.Kind == api.DanmakuEventConnected {
					confirmed = true
					updateState(danmakuConnectionState{
						phase:     danmakuConnectionConnected,
						attempt:   attempt,
						endpoint:  endpoint,
						confirmed: true,
					})
				}
				handleEvent(event)
			case streamErr, ok := <-errors:
				if ok && streamErr != nil {
					lastError = streamErr.Error()
				} else if !ok {
					errors = nil
					ended = events == nil
				}
			}
		}
		stream.Close()
		if ctx.Err() != nil {
			return
		}

		if confirmed {
			failures = 0
		} else {
			failures++
		}
		delay := danmakuRetryDelay(failures)
		updateState(danmakuConnectionState{
			phase:      danmakuConnectionRetrying,
			attempt:    attempt,
			endpoint:   endpoint,
			lastError:  lastError,
			retryDelay: delay,
			confirmed:  confirmed,
		})
		if !waitDanmakuRetry(ctx, delay) {
			return
		}
	}
}

func danmakuRetryDelay(failures int) time.Duration {
	delays := [...]time.Duration{time.Second, 2 * time.Second, 5 * time.Second, 10 * time.Second, 15 * time.Second}
	if failures <= 0 {
		return delays[0]
	}
	index := failures - 1
	if index >= len(delays) {
		index = len(delays) - 1
	}
	return delays[index]
}

func waitDanmakuRetry(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
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
