package api

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/gorilla/websocket"
)

const (
	// DefaultDanmakuMaxLength 只在 B 站的账号/房间约束暂时无法取得时使用。
	// 正常发送应优先使用 GetDanmakuMaxLength 返回的动态值。
	DefaultDanmakuMaxLength      = 40
	danmakuHeaderLength          = 16
	danmakuProtocolPlain         = 0
	danmakuProtocolHeartbeat     = 1
	danmakuProtocolAuth          = 1
	danmakuProtocolZlib          = 2
	danmakuProtocolBrotli        = 3
	danmakuOperationHeartbeat    = 2
	danmakuOperationOnline       = 3
	danmakuOperationCommand      = 5
	danmakuOperationAuth         = 7
	danmakuOperationAuthReply    = 8
	danmakuHeartbeatInterval     = 30 * time.Second
	danmakuAuthenticationTimeout = 12 * time.Second
	danmakuReadTimeout           = 75 * time.Second
	danmakuWriteTimeout          = 8 * time.Second
	danmakuWebSocketReadLimit    = 8 << 20
	danmakuDecodedPayloadLimit   = 16 << 20
	danmakuPacketNestingLimit    = 4
)

// GetDanmakuMaxLength 返回 B 站网页端为当前账号和直播间下发的弹幕字数上限。
func (c *Client) GetDanmakuMaxLength(ctx context.Context, roomID, sessdata, biliJCT string) (int, error) {
	roomID = strings.TrimSpace(roomID)
	if roomID == "" {
		return 0, fmt.Errorf("获取弹幕字数限制需要房间号")
	}
	path, err := c.endpointByName("GetDanmakuUserInfo")
	if err != nil {
		return 0, err
	}
	parsed, err := url.Parse(path)
	if err != nil {
		return 0, fmt.Errorf("准备获取弹幕字数限制失败: %w", err)
	}
	query := parsed.Query()
	query.Set("room_id", roomID)
	query.Set("from", "0")
	parsed.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return 0, fmt.Errorf("准备获取弹幕字数限制失败: %w", err)
	}
	setBilibiliBrowserHeaders(req)
	req.Header.Set("Referer", "https://live.bilibili.com/"+roomID)
	if cookie := browserCookie(sessdata, biliJCT); cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("获取弹幕字数限制失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, fmt.Errorf("获取弹幕字数限制失败：远程服务器返回 HTTP %d", resp.StatusCode)
	}
	var raw struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Msg     string `json:"msg"`
		Data    *struct {
			Property struct {
				Danmaku struct {
					Length flexibleInt64 `json:"length"`
				} `json:"danmu"`
			} `json:"property"`
		} `json:"data"`
	}
	decoder := json.NewDecoder(io.LimitReader(resp.Body, maxAPIResponseBytes+1))
	if err := decoder.Decode(&raw); err != nil {
		return 0, fmt.Errorf("解析弹幕字数限制失败: %w", err)
	}
	if raw.Code != 0 {
		message := strings.TrimSpace(raw.Message)
		if message == "" {
			message = strings.TrimSpace(raw.Msg)
		}
		return 0, fmt.Errorf("获取弹幕字数限制失败（错误码 %d）：%s", raw.Code, message)
	}
	if raw.Data == nil {
		return 0, fmt.Errorf("弹幕字数限制接口未返回数据")
	}
	limit := int(raw.Data.Property.Danmaku.Length)
	if limit <= 0 || limit > 1000 {
		return 0, fmt.Errorf("B 站返回了无效的弹幕字数限制：%d", limit)
	}
	return limit, nil
}

// UserProfile 是点击弹幕用户名后按需获取的公开资料。
// 不包含生日、所在地等与直播互动无关的个人信息。
type UserProfile struct {
	UserID       string
	Username     string
	Signature    string
	Level        int
	Official     string
	VIP          bool
	VIPLabel     string
	Followers    int64
	Following    int64
	ArchiveCount int64
	ArticleCount int64
	IsFollowing  bool
	IsSelf       bool
}

// GetUserProfile 获取指定 UID 的 B 站公开用户卡片。
// 身份和设备 Cookie 与弹幕连接共用，降低网页登录接口误判无头请求的概率。
func (c *Client) GetUserProfile(ctx context.Context, userID, sessdata, biliJCT string) (UserProfile, error) {
	userID = strings.TrimSpace(userID)
	uid, err := strconv.ParseInt(userID, 10, 64)
	if err != nil || uid <= 0 {
		return UserProfile{}, fmt.Errorf("无效的用户 UID：%s", userID)
	}
	path, err := c.endpointByName("GetUserCard")
	if err != nil {
		return UserProfile{}, err
	}
	parsed, err := url.Parse(path)
	if err != nil {
		return UserProfile{}, fmt.Errorf("准备查询用户资料失败: %w", err)
	}
	query := parsed.Query()
	query.Set("mid", userID)
	query.Set("photo", "false")
	parsed.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return UserProfile{}, fmt.Errorf("准备查询用户资料失败: %w", err)
	}
	setBilibiliBrowserHeaders(req)
	req.Header.Set("Referer", "https://space.bilibili.com/"+userID+"/")
	identity, identityErr := c.resolveDanmakuIdentity(ctx, sessdata, biliJCT)
	if identityErr == nil {
		req.Header.Set("Cookie", danmakuBrowserCookie(sessdata, biliJCT, identity))
	} else if cookie := browserCookie(sessdata, biliJCT); cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return UserProfile{}, fmt.Errorf("查询用户资料失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return UserProfile{}, fmt.Errorf("查询用户资料失败：远程服务器返回 HTTP %d", resp.StatusCode)
	}
	var raw struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    *struct {
			Card struct {
				Mid       flexibleID    `json:"mid"`
				Name      string        `json:"name"`
				Sign      string        `json:"sign"`
				Fans      flexibleInt64 `json:"fans"`
				Friend    flexibleInt64 `json:"friend"`
				Attention flexibleInt64 `json:"attention"`
				LevelInfo struct {
					CurrentLevel flexibleInt64 `json:"current_level"`
				} `json:"level_info"`
				Official struct {
					Title string `json:"title"`
				} `json:"Official"`
				VIP struct {
					Status flexibleInt64 `json:"status"`
					Label  struct {
						Text string `json:"text"`
					} `json:"label"`
				} `json:"vip"`
			} `json:"card"`
			Following    bool          `json:"following"`
			Follower     flexibleInt64 `json:"follower"`
			ArchiveCount flexibleInt64 `json:"archive_count"`
			ArticleCount flexibleInt64 `json:"article_count"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxAPIResponseBytes+1)).Decode(&raw); err != nil {
		return UserProfile{}, fmt.Errorf("解析用户资料失败: %w", err)
	}
	if raw.Code != 0 {
		message := strings.TrimSpace(raw.Message)
		if message == "" {
			message = "B 站未返回具体原因"
		}
		return UserProfile{}, fmt.Errorf("B 站拒绝查询用户资料（错误码 %d）：%s", raw.Code, message)
	}
	if raw.Data == nil {
		return UserProfile{}, fmt.Errorf("用户资料接口未返回数据")
	}
	card := raw.Data.Card
	profile := UserProfile{
		UserID:       strings.TrimSpace(string(card.Mid)),
		Username:     strings.TrimSpace(card.Name),
		Signature:    strings.TrimSpace(card.Sign),
		Level:        int(card.LevelInfo.CurrentLevel),
		Official:     strings.TrimSpace(card.Official.Title),
		VIP:          card.VIP.Status == 1,
		VIPLabel:     strings.TrimSpace(card.VIP.Label.Text),
		Followers:    int64(card.Fans),
		Following:    int64(card.Attention),
		ArchiveCount: int64(raw.Data.ArchiveCount),
		ArticleCount: int64(raw.Data.ArticleCount),
		IsFollowing:  raw.Data.Following,
	}
	if profile.UserID == "" {
		profile.UserID = userID
	}
	if identityErr == nil && identity.UID > 0 {
		profile.IsSelf = profile.UserID == strconv.FormatInt(identity.UID, 10)
	}
	if profile.Followers == 0 && raw.Data.Follower > 0 {
		profile.Followers = int64(raw.Data.Follower)
	}
	if profile.Following == 0 {
		profile.Following = int64(card.Friend)
	}
	return profile, nil
}

// SetUserFollowing 修改当前账号与指定用户的关注关系。
func (c *Client) SetUserFollowing(ctx context.Context, userID, sessdata, biliJCT string, following bool) error {
	userID = strings.TrimSpace(userID)
	uid, err := strconv.ParseInt(userID, 10, 64)
	if err != nil || uid <= 0 {
		return fmt.Errorf("无效的用户 UID：%s", userID)
	}
	if strings.TrimSpace(sessdata) == "" || strings.TrimSpace(biliJCT) == "" {
		return fmt.Errorf("修改关注状态需要有效的 SESSDATA 和 bili_jct")
	}
	params := url.Values{}
	params.Set("fid", userID)
	params.Set("act", "2")
	if following {
		params.Set("act", "1")
	}
	params.Set("re_src", "0")
	params.Set("csrf", biliJCT)
	params.Set("csrf_token", biliJCT)
	path, err := c.endpointByName("ModifyUserRelation")
	if err != nil {
		return err
	}
	var result struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Msg     string `json:"msg"`
	}
	headers := http.Header{
		"Cookie":     []string{"SESSDATA=" + sessdata + "; bili_jct=" + biliJCT},
		"User-Agent": []string{biliBrowserUserAgent},
		"Origin":     []string{"https://space.bilibili.com"},
		"Referer":    []string{"https://space.bilibili.com/" + userID + "/"},
	}
	if err := c.postFormWithHeaders(ctx, path, params, &result, headers); err != nil {
		return fmt.Errorf("修改关注状态失败: %w", err)
	}
	if result.Code != 0 {
		action := "关注"
		if !following {
			action = "取消关注"
		}
		return fmt.Errorf("%s失败（错误码 %d）：%s", action, result.Code, responseMessage(result.Message, result.Msg))
	}
	return nil
}

// DanmakuHost 是 B 站公布的一个 WebSocket 服务器地址。
type DanmakuHost struct {
	Host    string
	Port    int
	WSPort  int
	WSSPort int
}

// DanmakuInfo 包含订阅直播间弹幕所需的短期令牌和 WebSocket 服务器列表。
type DanmakuInfo struct {
	Token string
	Hosts []DanmakuHost
}

// WebSocketURLs 按 B 站公布顺序返回可用的安全 WebSocket 地址。
// 将转换逻辑放在这里，便于不建立网络连接就测试服务器选择。
func (info DanmakuInfo) WebSocketURLs() []string {
	result := make([]string, 0, len(info.Hosts))
	seen := make(map[string]struct{})
	for _, item := range info.Hosts {
		host := strings.TrimSpace(item.Host)
		if host == "" {
			continue
		}
		// API 通常返回裸主机名，但这里也接受完整 URL，兼容代理和测试数据。
		if parsed, err := url.Parse(host); err == nil && parsed.Host != "" {
			host = parsed.Hostname()
		}
		host = strings.Trim(host, "[]")
		port := item.WSSPort
		if port <= 0 {
			port = item.WSPort
		}
		if port <= 0 {
			port = item.Port
		}
		if port <= 0 {
			port = 443
		}
		endpoint := "wss://" + net.JoinHostPort(host, strconv.Itoa(port)) + "/sub"
		if _, ok := seen[endpoint]; ok {
			continue
		}
		seen[endpoint] = struct{}{}
		result = append(result, endpoint)
	}
	return result
}

type danmakuHostWire struct {
	Host    string        `json:"host"`
	Port    flexibleInt64 `json:"port"`
	WSPort  flexibleInt64 `json:"ws_port"`
	WSSPort flexibleInt64 `json:"wss_port"`
}

// GetDanmakuInfoWithCookie 携带当前网页登录 Cookie 获取弹幕连接信息。
// 即使读取公开弹幕不需要登录，新接口也越来越常对缺少浏览器会话的请求返回 -352。
func (c *Client) GetDanmakuInfoWithCookie(ctx context.Context, roomID, sessdata, biliJCT string) (DanmakuInfo, error) {
	return c.getDanmakuInfo(ctx, roomID, sessdata, biliJCT, danmakuIdentity{})
}

func (c *Client) getDanmakuInfo(ctx context.Context, roomID, sessdata, biliJCT string, identity danmakuIdentity) (DanmakuInfo, error) {
	if strings.TrimSpace(roomID) == "" {
		return DanmakuInfo{}, fmt.Errorf("获取弹幕连接信息需要房间号")
	}
	path, err := c.endpointByName("GetDanmakuInfo")
	if err != nil {
		return DanmakuInfo{}, err
	}
	info, primaryErr := c.getDanmakuInfoAt(ctx, path, roomID, sessdata, biliJCT, identity, false)
	if primaryErr == nil {
		return info, nil
	}
	// 新版 Web 接口的保护比旧房间接口更严格。无头客户端必须在这里回退，
	// 避免弹幕页因 -352 永远重复请求同一个被拦截的地址。
	if shouldFallbackDanmakuInfo(primaryErr) {
		if legacyPath, lookupErr := c.endpointByName("GetDanmakuInfoLegacy"); lookupErr == nil {
			if info, legacyErr := c.getDanmakuInfoAt(ctx, legacyPath, roomID, sessdata, biliJCT, identity, true); legacyErr == nil {
				return info, nil
			}
		}
	}
	return DanmakuInfo{}, primaryErr
}

func shouldFallbackDanmakuInfo(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return strings.Contains(message, "-352") || strings.Contains(message, "风控") || strings.Contains(message, "HTTP 404")
}

func (c *Client) getDanmakuInfoAt(ctx context.Context, path, roomID, sessdata, biliJCT string, identity danmakuIdentity, legacy bool) (DanmakuInfo, error) {
	parsed, err := url.Parse(path)
	if err != nil {
		return DanmakuInfo{}, fmt.Errorf("准备获取弹幕连接信息失败: %w", err)
	}
	query := parsed.Query()
	if legacy {
		query.Set("room_id", strings.TrimSpace(roomID))
	} else {
		query.Set("id", strings.TrimSpace(roomID))
		// 这些参数来自当前直播网页客户端，其中 web_location 用于让 B 站风控层
		// 区分直播间页面请求和普通 API 探测。
		query.Set("type", "0")
		query.Set("web_location", "444.8")
	}
	parsed.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return DanmakuInfo{}, fmt.Errorf("准备获取弹幕连接信息失败: %w", err)
	}
	setBilibiliBrowserHeaders(req)
	if cookie := danmakuBrowserCookie(sessdata, biliJCT, identity); cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return DanmakuInfo{}, fmt.Errorf("获取弹幕连接信息失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		message := strings.TrimSpace(string(body))
		if message != "" {
			return DanmakuInfo{}, fmt.Errorf("获取弹幕连接信息失败：远程服务器返回 HTTP %d：%s", resp.StatusCode, message)
		}
		return DanmakuInfo{}, fmt.Errorf("获取弹幕连接信息失败：远程服务器返回 HTTP %d", resp.StatusCode)
	}
	var raw struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Msg     string `json:"msg"`
		Data    struct {
			Token          string            `json:"token"`
			HostList       []danmakuHostWire `json:"host_list"`
			HostServerList []danmakuHostWire `json:"host_server_list"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return DanmakuInfo{}, fmt.Errorf("解析弹幕连接信息失败: %w", err)
	}
	if raw.Code != 0 {
		return DanmakuInfo{}, fmt.Errorf("获取弹幕连接信息失败（错误码 %d）：%s", raw.Code, responseMessage(raw.Message, raw.Msg))
	}
	info := DanmakuInfo{Token: strings.TrimSpace(raw.Data.Token)}
	hosts := raw.Data.HostList
	if len(hosts) == 0 {
		hosts = raw.Data.HostServerList
	}
	for _, host := range hosts {
		info.Hosts = append(info.Hosts, DanmakuHost{
			Host:    host.Host,
			Port:    int(host.Port),
			WSPort:  int(host.WSPort),
			WSSPort: int(host.WSSPort),
		})
	}
	if info.Token == "" {
		return DanmakuInfo{}, fmt.Errorf("弹幕连接信息缺少 token")
	}
	if len(info.WebSocketURLs()) == 0 {
		return DanmakuInfo{}, fmt.Errorf("弹幕连接信息缺少 websocket 服务器")
	}
	return info, nil
}

// DanmakuEventKind 标识直播间产生的有效消息类型。除 Online 外都带有可显示文本。
type DanmakuEventKind string

const (
	DanmakuEventMessage   DanmakuEventKind = "message"
	DanmakuEventGift      DanmakuEventKind = "gift"
	DanmakuEventSuperChat DanmakuEventKind = "super_chat"
	DanmakuEventGuard     DanmakuEventKind = "guard"
	DanmakuEventWarning   DanmakuEventKind = "warning"
	DanmakuEventSystem    DanmakuEventKind = "system"
	DanmakuEventOnline    DanmakuEventKind = "online"
	// DanmakuEventConnected 在服务器接受操作码 7 的认证后产生。
	// TCP/WebSocket 连接成功本身不能证明直播间订阅已经可用。
	DanmakuEventConnected DanmakuEventKind = "connected"
)

// DanmakuMessage 是与 B 站嵌套数组格式无关的标准化弹幕项。
// Timestamp 在解析数据包时设置。
type DanmakuMessage struct {
	Username      string
	UserID        string
	Text          string
	MedalName     string
	MedalLevel    int
	GuardLevel    int
	UserLevel     int
	WealthLevel   int
	IsAdmin       bool
	IsMystery     bool
	GiftName      string
	GiftCount     int
	GiftAction    string
	GiftCoinType  string // "gold"（付费电池/金瓜子）或 "silver"（免费银瓜子）
	GiftTotalCoin int64  // 瓜子总价值（1000 金瓜子 = 10 电池 = 1 元）
	GiftCombo     int    // 连击数
	BatchComboID  string // 连击批次 ID
	Price         int    // SC 醒目留言金额（元）或舰长折合人民币金额（元）
	Duration      int    // SC 醒目留言悬挂保留时长（秒）
	Timestamp     time.Time
}

// DanmakuEvent 由 DanmakuStream.Events 提供。
type DanmakuEvent struct {
	Kind    DanmakuEventKind
	Message DanmakuMessage
	Online  int64
	Command string
}

// LiveSessionStats 统计只有认证弹幕流才能观察到的本场数据。
// 人气来自弹幕心跳，礼物统计限定在本场会话内，因为 B 站公开房间接口没有可靠的累计礼物总数。
type LiveSessionStats struct {
	GiftEvents      int64
	GiftCount       int64
	GiftGoldCoin    int64
	SuperChatCount  int64
	SuperChatPrice  int64
	GuardCount      int64
	Popularity      int64
	PopularityKnown bool
}

func (stats *LiveSessionStats) Observe(event DanmakuEvent) {
	if stats == nil {
		return
	}
	switch event.Kind {
	case DanmakuEventGift:
		if event.Command == "COMBO_SEND" {
			return
		}
		stats.GiftEvents++
		count := event.Message.GiftCount
		if count <= 0 {
			count = 1
		}
		stats.GiftCount += int64(count)
		if event.Message.GiftCoinType == "gold" {
			stats.GiftGoldCoin += event.Message.GiftTotalCoin
		}
	case DanmakuEventSuperChat:
		stats.SuperChatCount++
		stats.SuperChatPrice += int64(event.Message.Price)
	case DanmakuEventGuard:
		count := event.Message.GiftCount
		if count <= 0 {
			count = 1
		}
		stats.GuardCount += int64(count)
	}
}

// ObserveComboGift 记录连击礼物的增量数量与瓜子变化，不重复计入独立的送礼事件。
func (stats *LiveSessionStats) ObserveComboGift(event DanmakuEvent, deltaCount int64, deltaCoin int64) {
	if stats == nil {
		return
	}
	if deltaCount > 0 {
		stats.GiftCount += deltaCount
	}
	if deltaCoin > 0 && event.Message.GiftCoinType == "gold" {
		stats.GiftGoldCoin += deltaCoin
	}
}

// DanmakuStream 管理一个直播弹幕 WebSocket，流结束时关闭 Events 和 Errors。
type DanmakuStream struct {
	conn       *websocket.Conn
	endpoint   string
	events     chan DanmakuEvent
	errors     chan error
	done       chan struct{}
	closeOnce  sync.Once
	finishOnce sync.Once
}

func (s *DanmakuStream) Events() <-chan DanmakuEvent { return s.events }

func (s *DanmakuStream) Errors() <-chan error { return s.errors }

// Endpoint 返回当前弹幕连接使用的服务器地址，不包含认证信息。
func (s *DanmakuStream) Endpoint() string {
	if s == nil {
		return ""
	}
	return s.endpoint
}

// Close 中断读取和心跳循环，可重复调用，也可在 WebSocket 正在读取时调用。
func (s *DanmakuStream) Close() {
	if s == nil {
		return
	}
	s.closeOnce.Do(func() {
		close(s.done)
		if s.conn != nil {
			_ = s.conn.Close()
		}
	})
}

// ConnectDanmakuWithCookie 是带 Cookie 的直播会话版本。
// 它根据 SESSDATA 获取对应 UID 和 buvid，确保令牌请求、WebSocket Cookie
// 以及操作码 7 的认证载荷描述同一个身份。
func (c *Client) ConnectDanmakuWithCookie(ctx context.Context, roomID, sessdata, biliJCT string) (*DanmakuStream, error) {
	return c.connectDanmaku(ctx, roomID, sessdata, biliJCT)
}

func (c *Client) connectDanmaku(ctx context.Context, roomID, sessdata, biliJCT string) (*DanmakuStream, error) {
	identity, err := c.resolveDanmakuIdentity(ctx, sessdata, biliJCT)
	if err != nil {
		return nil, err
	}
	info, err := c.getDanmakuInfo(ctx, roomID, sessdata, biliJCT, identity)
	if err != nil {
		return nil, err
	}
	endpoints := info.WebSocketURLs()
	if len(endpoints) == 0 {
		return nil, fmt.Errorf("弹幕连接信息没有可用的 websocket 地址")
	}
	endpoints = c.rotateDanmakuEndpoints(endpoints)
	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second, Proxy: systemProxyFunc()}
	headers := danmakuWebSocketHeaders(roomID, sessdata, biliJCT, identity)
	var lastErr error
	for _, endpoint := range endpoints {
		conn, response, dialErr := dialer.DialContext(ctx, endpoint, headers)
		if dialErr != nil {
			if response != nil && response.Body != nil {
				_ = response.Body.Close()
			}
			lastErr = dialErr
			continue
		}
		conn.SetReadLimit(danmakuWebSocketReadLimit)
		stream := &DanmakuStream{
			conn:     conn,
			endpoint: endpoint,
			events:   make(chan DanmakuEvent, 128),
			errors:   make(chan error, 1),
			done:     make(chan struct{}),
		}
		go stream.run(ctx, info.Token, roomID, identity)
		return stream, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("没有可用的弹幕 websocket 服务器")
	}
	return nil, fmt.Errorf("连接弹幕服务器失败: %w", lastErr)
}

// rotateDanmakuEndpoints 让连续的重连从不同节点开始。
// B 站的首个 host 偶尔会在 WebSocket 已建立后直接关闭连接，固定顺序会让
// 自动重连反复失败，直到用户手动重启程序。
func (c *Client) rotateDanmakuEndpoints(endpoints []string) []string {
	if len(endpoints) < 2 {
		return endpoints
	}
	c.danmakuEndpointMu.Lock()
	offset := c.danmakuEndpointOffset % len(endpoints)
	c.danmakuEndpointOffset = (offset + 1) % len(endpoints)
	c.danmakuEndpointMu.Unlock()

	ordered := make([]string, 0, len(endpoints))
	ordered = append(ordered, endpoints[offset:]...)
	ordered = append(ordered, endpoints[:offset]...)
	return ordered
}

func danmakuWebSocketHeaders(roomID, sessdata, biliJCT string, identity danmakuIdentity) http.Header {
	headers := http.Header{}
	headers.Set("Origin", "https://live.bilibili.com")
	headers.Set("Referer", "https://live.bilibili.com/"+strings.TrimSpace(roomID))
	headers.Set("User-Agent", biliBrowserUserAgent)
	if cookie := danmakuBrowserCookie(sessdata, biliJCT, identity); cookie != "" {
		headers.Set("Cookie", cookie)
	}
	return headers
}

func (s *DanmakuStream) run(ctx context.Context, token, roomID string, identity danmakuIdentity) {
	defer s.finish()
	if err := s.conn.SetReadDeadline(time.Now().Add(danmakuAuthenticationTimeout)); err != nil {
		s.emitError(fmt.Errorf("设置弹幕认证超时失败: %w", err))
		return
	}
	authBody := danmakuAuthPayload(token, roomID, identity)
	if err := s.writePacket(danmakuOperationAuth, danmakuProtocolAuth, authBody); err != nil {
		s.emitError(fmt.Errorf("发送弹幕认证失败: %w", err))
		return
	}
	readErrors := make(chan error, 1)
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		s.readLoop(readErrors)
	}()
	defer func() {
		// 关闭套接字会解除 ReadMessage 阻塞。关闭事件通道前等待读取循环结束，
		// 避免正在解析的数据包发送到已关闭的通道。
		s.Close()
		<-readDone
	}()
	heartbeat := time.NewTicker(danmakuHeartbeatInterval)
	defer heartbeat.Stop()
	for {
		select {
		case <-ctx.Done():
			s.Close()
			return
		case <-s.done:
			return
		case err := <-readErrors:
			if err != nil && !isDanmakuCloseError(err) {
				s.emitError(fmt.Errorf("弹幕连接已断开: %w", err))
			}
			return
		case <-heartbeat.C:
			if err := s.writePacket(danmakuOperationHeartbeat, danmakuProtocolHeartbeat, nil); err != nil {
				s.emitError(fmt.Errorf("发送弹幕心跳失败: %w", err))
				s.Close()
				return
			}
		}
	}
}

func danmakuAuthPayload(token, roomID string, identity danmakuIdentity) []byte {
	var room any = strings.TrimSpace(roomID)
	if parsedRoomID, err := strconv.ParseInt(strings.TrimSpace(roomID), 10, 64); err == nil {
		room = parsedRoomID
	}
	payload := struct {
		UID      int64  `json:"uid"`
		RoomID   any    `json:"roomid"`
		Protover int    `json:"protover"`
		Buvid    string `json:"buvid,omitempty"`
		Platform string `json:"platform"`
		Type     int    `json:"type"`
		Key      string `json:"key"`
		Version  int    `json:"version"`
	}{
		UID: identity.UID, RoomID: room, Protover: 3, Buvid: identity.Buvid, Platform: "web", Type: 2, Key: token, Version: 1,
	}
	body, _ := json.Marshal(payload)
	return body
}

type danmakuIdentity struct {
	UID   int64
	Buvid string
}

const danmakuIdentityCacheTTL = 30 * time.Minute

func (c *Client) resolveDanmakuIdentity(ctx context.Context, sessdata, biliJCT string) (danmakuIdentity, error) {
	sessdata = strings.TrimSpace(sessdata)
	if sessdata == "" {
		return danmakuIdentity{}, nil
	}

	c.danmakuIdentityMu.Lock()
	if c.danmakuIdentityFor == sessdata &&
		c.danmakuIdentity.UID > 0 &&
		c.danmakuIdentity.Buvid != "" &&
		time.Since(c.danmakuIdentityAt) < danmakuIdentityCacheTTL {
		identity := c.danmakuIdentity
		c.danmakuIdentityMu.Unlock()
		return identity, nil
	}
	c.danmakuIdentityMu.Unlock()

	uid, err := c.getDanmakuUID(ctx, sessdata, biliJCT)
	if err != nil {
		c.CloseIdleConnections()
		return danmakuIdentity{}, err
	}
	buvid, err := c.getDanmakuBuvid(ctx, sessdata, biliJCT)
	if err != nil {
		c.CloseIdleConnections()
		return danmakuIdentity{}, err
	}
	identity := danmakuIdentity{UID: uid, Buvid: buvid}
	c.danmakuIdentityMu.Lock()
	c.danmakuIdentity = identity
	c.danmakuIdentityFor = sessdata
	c.danmakuIdentityAt = time.Now()
	c.danmakuIdentityMu.Unlock()
	return identity, nil
}

func (c *Client) getDanmakuUID(ctx context.Context, sessdata, biliJCT string) (int64, error) {
	path, err := c.endpointByName("GetWebNav")
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, path, nil)
	if err != nil {
		return 0, fmt.Errorf("准备获取弹幕用户身份失败: %w", err)
	}
	setBilibiliBrowserHeaders(req)
	req.Header.Set("Referer", "https://www.bilibili.com/")
	req.Header.Set("Cookie", browserCookie(sessdata, biliJCT))
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		c.CloseIdleConnections()
		return 0, fmt.Errorf("获取弹幕用户身份失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		c.CloseIdleConnections()
		return 0, fmt.Errorf("获取弹幕用户身份失败：远程服务器返回 HTTP %d", resp.StatusCode)
	}
	var raw struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			IsLogin bool          `json:"isLogin"`
			Mid     flexibleInt64 `json:"mid"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return 0, fmt.Errorf("解析弹幕用户身份失败: %w", err)
	}
	if raw.Code != 0 || !raw.Data.IsLogin || raw.Data.Mid <= 0 {
		message := strings.TrimSpace(raw.Message)
		if message == "" {
			message = "登录状态无效"
		}
		return 0, fmt.Errorf("获取弹幕用户身份失败: %s", message)
	}
	return int64(raw.Data.Mid), nil
}

func (c *Client) getDanmakuBuvid(ctx context.Context, sessdata, biliJCT string) (string, error) {
	path, err := c.endpointByName("GetBuvid")
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, path, nil)
	if err != nil {
		return "", fmt.Errorf("准备获取弹幕设备标识失败: %w", err)
	}
	setBilibiliBrowserHeaders(req)
	req.Header.Set("Referer", "https://www.bilibili.com/")
	req.Header.Set("Cookie", browserCookie(sessdata, biliJCT))
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		c.CloseIdleConnections()
		return "", fmt.Errorf("获取弹幕设备标识失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		c.CloseIdleConnections()
		return "", fmt.Errorf("获取弹幕设备标识失败：远程服务器返回 HTTP %d", resp.StatusCode)
	}
	var raw struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			Buvid string `json:"b_3"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return "", fmt.Errorf("解析弹幕设备标识失败: %w", err)
	}
	buvid := strings.TrimSpace(raw.Data.Buvid)
	if raw.Code != 0 || buvid == "" {
		message := strings.TrimSpace(raw.Message)
		if message == "" {
			message = "接口未返回 buvid3"
		}
		return "", fmt.Errorf("获取弹幕设备标识失败: %s", message)
	}
	return buvid, nil
}

func danmakuBrowserCookie(sessdata, biliJCT string, identity danmakuIdentity) string {
	cookie := browserCookie(sessdata, biliJCT)
	parts := make([]string, 0, 3)
	if cookie != "" {
		parts = append(parts, cookie)
	}
	if identity.UID > 0 {
		parts = append(parts, "DedeUserID="+strconv.FormatInt(identity.UID, 10))
	}
	if identity.Buvid != "" {
		parts = append(parts, "buvid3="+identity.Buvid)
	}
	return strings.Join(parts, "; ")
}

func (s *DanmakuStream) readLoop(readErrors chan<- error) {
	defer close(readErrors)
	authenticated := false
	for {
		_, data, err := s.conn.ReadMessage()
		if err != nil {
			readErrors <- err
			return
		}
		events, parseErr := parseDanmakuPackets(data)
		if parseErr != nil {
			readErrors <- parseErr
			return
		}
		if !authenticated {
			for _, event := range events {
				if event.Kind == DanmakuEventConnected {
					authenticated = true
					break
				}
			}
		}
		if authenticated {
			if err := s.conn.SetReadDeadline(time.Now().Add(danmakuReadTimeout)); err != nil {
				readErrors <- fmt.Errorf("刷新弹幕读取超时失败: %w", err)
				return
			}
		}
		for _, event := range events {
			if !s.emitEvent(event) {
				return
			}
		}
	}
}

func (s *DanmakuStream) finish() {
	s.Close()
	s.finishOnce.Do(func() {
		close(s.events)
		close(s.errors)
	})
}

func (s *DanmakuStream) emitEvent(event DanmakuEvent) bool {
	select {
	case s.events <- event:
		return true
	case <-s.done:
		return false
	}
}

func (s *DanmakuStream) emitError(err error) {
	if err == nil {
		return
	}
	select {
	case s.errors <- err:
	default:
	}
}

func (s *DanmakuStream) writePacket(operation uint32, version uint16, body []byte) error {
	packet := make([]byte, danmakuHeaderLength+len(body))
	binary.BigEndian.PutUint32(packet[0:4], uint32(len(packet)))
	binary.BigEndian.PutUint16(packet[4:6], danmakuHeaderLength)
	binary.BigEndian.PutUint16(packet[6:8], version)
	binary.BigEndian.PutUint32(packet[8:12], operation)
	binary.BigEndian.PutUint32(packet[12:16], 1)
	copy(packet[16:], body)
	if err := s.conn.SetWriteDeadline(time.Now().Add(danmakuWriteTimeout)); err != nil {
		return err
	}
	return s.conn.WriteMessage(websocket.BinaryMessage, packet)
}

func setBilibiliBrowserHeaders(req *http.Request) {
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", biliBrowserUserAgent)
	req.Header.Set("Referer", "https://live.bilibili.com/")
	req.Header.Set("Origin", "https://live.bilibili.com")
}

func browserCookie(sessdata, biliJCT string) string {
	parts := make([]string, 0, 2)
	if value := strings.TrimSpace(sessdata); value != "" {
		parts = append(parts, "SESSDATA="+value)
	}
	if value := strings.TrimSpace(biliJCT); value != "" {
		parts = append(parts, "bili_jct="+value)
	}
	return strings.Join(parts, "; ")
}

func isDanmakuCloseError(err error) bool {
	return websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway, websocket.CloseNoStatusReceived) || strings.Contains(strings.ToLower(err.Error()), "use of closed network connection")
}

func parseDanmakuPackets(data []byte) ([]DanmakuEvent, error) {
	return parseDanmakuPacketsDepth(data, 0)
}

func parseDanmakuPacketsDepth(data []byte, depth int) ([]DanmakuEvent, error) {
	if len(data) > danmakuDecodedPayloadLimit {
		return nil, fmt.Errorf("弹幕解压数据超过大小限制: %d", len(data))
	}
	if depth > danmakuPacketNestingLimit {
		return nil, fmt.Errorf("弹幕压缩包嵌套层数超过限制: %d", danmakuPacketNestingLimit)
	}
	if len(data) < danmakuHeaderLength {
		return nil, fmt.Errorf("弹幕数据包长度不足: %d", len(data))
	}
	var events []DanmakuEvent
	for len(data) > 0 {
		if len(data) < danmakuHeaderLength {
			return events, fmt.Errorf("弹幕数据包尾部长度不足: %d", len(data))
		}
		packetLength := int(binary.BigEndian.Uint32(data[0:4]))
		headerLength := int(binary.BigEndian.Uint16(data[4:6]))
		version := binary.BigEndian.Uint16(data[6:8])
		operation := binary.BigEndian.Uint32(data[8:12])
		if packetLength < danmakuHeaderLength || headerLength < danmakuHeaderLength || headerLength > packetLength || packetLength > len(data) {
			return events, fmt.Errorf("弹幕数据包长度无效: packet=%d header=%d data=%d", packetLength, headerLength, len(data))
		}
		body := data[headerLength:packetLength]
		if version == danmakuProtocolZlib {
			reader, err := zlib.NewReader(bytes.NewReader(body))
			if err != nil {
				return events, fmt.Errorf("解压弹幕数据失败: %w", err)
			}
			decoded, readErr := readDanmakuDecoded(reader)
			_ = reader.Close()
			if readErr != nil {
				return events, fmt.Errorf("读取解压弹幕数据失败: %w", readErr)
			}
			nested, nestedErr := parseDanmakuPacketsDepth(decoded, depth+1)
			if nestedErr != nil {
				return events, nestedErr
			}
			events = append(events, nested...)
		} else if version == danmakuProtocolBrotli {
			// 协议 3 使用 Brotli 压缩，解压后仍是一个或多个标准弹幕包。
			reader := brotli.NewReader(bytes.NewReader(body))
			decoded, readErr := readDanmakuDecoded(reader)
			if readErr != nil {
				return events, fmt.Errorf("读取 brotli 弹幕数据失败: %w", readErr)
			}
			nested, nestedErr := parseDanmakuPacketsDepth(decoded, depth+1)
			if nestedErr != nil {
				return events, nestedErr
			}
			events = append(events, nested...)
		} else {
			event, ok, err := parseDanmakuPayload(operation, body)
			if err != nil {
				return events, err
			}
			if ok {
				events = append(events, event)
			}
		}
		data = data[packetLength:]
	}
	return events, nil
}

func readDanmakuDecoded(reader io.Reader) ([]byte, error) {
	return readDanmakuDecodedLimit(reader, danmakuDecodedPayloadLimit)
}

func readDanmakuDecodedLimit(reader io.Reader, limit int64) ([]byte, error) {
	decoded, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(decoded)) > limit {
		return nil, fmt.Errorf("解压数据超过大小限制: %d", len(decoded))
	}
	return decoded, nil
}

func parseDanmakuPayload(operation uint32, body []byte) (DanmakuEvent, bool, error) {
	switch operation {
	case danmakuOperationOnline:
		if len(body) < 4 {
			return DanmakuEvent{}, false, fmt.Errorf("弹幕人气数据长度不足")
		}
		return DanmakuEvent{Kind: DanmakuEventOnline, Online: int64(binary.BigEndian.Uint32(body[len(body)-4:]))}, true, nil
	case danmakuOperationAuthReply:
		if len(body) == 0 {
			return DanmakuEvent{}, false, fmt.Errorf("弹幕认证响应为空")
		}
		var reply struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal(body, &reply); err != nil {
			return DanmakuEvent{}, false, fmt.Errorf("解析弹幕认证响应失败: %w", err)
		}
		if reply.Code != 0 {
			message := strings.TrimSpace(reply.Message)
			if message == "" {
				message = strconv.Itoa(reply.Code)
			}
			return DanmakuEvent{}, false, fmt.Errorf("弹幕认证失败: %s", message)
		}
		return DanmakuEvent{Kind: DanmakuEventConnected}, true, nil
	case danmakuOperationCommand:
		return parseDanmakuCommand(body)
	default:
		return DanmakuEvent{}, false, nil
	}
}

func parseDanmakuCommand(body []byte) (DanmakuEvent, bool, error) {
	var envelope struct {
		Cmd  string          `json:"cmd"`
		Info json.RawMessage `json:"info"`
		Data json.RawMessage `json:"data"`
		Msg  string          `json:"msg"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return DanmakuEvent{}, false, fmt.Errorf("解析弹幕消息失败: %w", err)
	}
	command := strings.SplitN(envelope.Cmd, ":", 2)[0]
	switch command {
	case "DANMU_MSG":
		return parseDanmuMessage(envelope.Info, command)
	case "SEND_GIFT":
		return parseGift(envelope.Data, command)
	case "COMBO_SEND":
		return parseComboSend(envelope.Data, command)
	case "SUPER_CHAT_MESSAGE", "SUPER_CHAT_MESSAGE_JPN":
		return parseSuperChat(envelope.Data, command)
	case "GUARD_BUY":
		return parseGuardBuy(envelope.Data, command)
	case "USER_TOAST_MSG":
		return parseUserToastMsg(envelope.Data, command)
	case "WARNING", "CUT_OFF":
		return parseWarning(envelope.Data, envelope.Msg, command)
	case "ROOM_CHANGE":
		return parseRoomChange(envelope.Data, command)
	case "ROOM_BLOCK_MSG":
		return parseRoomBlockMsg(envelope.Data, command)
	case "LIVE":
		return parseLiveState(envelope.Data, true, command)
	case "PREPARING":
		return parseLiveState(envelope.Data, false, command)
	case "WELCOME", "WELCOME_GUARD", "INTERACT_WORD":
		return parseSystemInteraction(envelope.Data, command)
	case "LIKE_INFO_V3_CLICK":
		return parseLikeInteraction(envelope.Data, command)
	default:
		return DanmakuEvent{}, false, nil
	}
}

func parseDanmuMessage(raw json.RawMessage, command string) (DanmakuEvent, bool, error) {
	var fields []json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return DanmakuEvent{}, false, fmt.Errorf("解析普通弹幕失败: %w", err)
	}
	if len(fields) < 3 {
		return DanmakuEvent{}, false, nil
	}
	message := DanmakuMessage{Text: jsonStringAt(fields, 1), Timestamp: time.Now()}
	var user []json.RawMessage
	if err := json.Unmarshal(fields[2], &user); err == nil {
		message.UserID = jsonStringAt(user, 0)
		message.Username = jsonStringAt(user, 1)
		message.IsAdmin = jsonIntAt(user, 2) != 0
	}
	if len(fields) > 3 {
		var medal []json.RawMessage
		if err := json.Unmarshal(fields[3], &medal); err == nil && len(medal) >= 2 {
			message.MedalLevel = jsonIntAt(medal, 0)
			message.MedalName = jsonStringAt(medal, 1)
			message.GuardLevel = jsonIntAt(medal, 10)
		}
	}
	if len(fields) > 4 {
		var level []json.RawMessage
		if err := json.Unmarshal(fields[4], &level); err == nil {
			message.UserLevel = jsonIntAt(level, 0)
		}
	}
	// 新版弹幕包会在 info[0][15].user 中附带更完整且结构化的用户信息。
	// 旧包仍以上面的数组字段为准，这里仅覆盖实际存在的值。
	applyStructuredDanmakuUser(fields[0], &message)
	if strings.TrimSpace(message.Text) == "" {
		return DanmakuEvent{}, false, nil
	}
	return DanmakuEvent{Kind: DanmakuEventMessage, Message: message, Command: command}, true, nil
}

func parseGift(raw json.RawMessage, command string) (DanmakuEvent, bool, error) {
	var data struct {
		UID            flexibleID    `json:"uid"`
		Uname          string        `json:"uname"`
		GiftName       string        `json:"giftName"`
		Num            flexibleInt64 `json:"num"`
		Action         string        `json:"action"`
		CoinType       string        `json:"coin_type"`
		TotalCoin      flexibleInt64 `json:"total_coin"`
		ComboNum       flexibleInt64 `json:"combo_num"`
		BatchComboID   string        `json:"batch_combo_id"`
		WealthLevel    flexibleInt64 `json:"wealth_level"`
		BatchComboSend *struct {
			BatchComboID  string        `json:"batch_combo_id"`
			BatchComboNum flexibleInt64 `json:"batch_combo_num"`
			BlindGift     *struct {
				OriginalGiftName string `json:"original_gift_name"`
				GiftAction       string `json:"gift_action"`
			} `json:"blind_gift"`
		} `json:"batch_combo_send"`
		MedalInfo struct {
			MedalName  string        `json:"medal_name"`
			MedalLevel flexibleInt64 `json:"medal_level"`
			GuardLevel flexibleInt64 `json:"guard_level"`
		} `json:"medal_info"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return DanmakuEvent{}, false, fmt.Errorf("解析礼物消息失败: %w", err)
	}
	if data.GiftName == "" {
		return DanmakuEvent{}, false, nil
	}
	count := int(data.Num)
	if count <= 0 {
		count = 1
	}
	action := strings.TrimSpace(data.Action)
	if action == "" {
		action = "送出"
	}
	batchComboID := strings.TrimSpace(data.BatchComboID)
	combo := int(data.ComboNum)
	if data.BatchComboSend != nil {
		if batchComboID == "" {
			batchComboID = strings.TrimSpace(data.BatchComboSend.BatchComboID)
		}
		if combo <= 0 && data.BatchComboSend.BatchComboNum > 0 {
			combo = int(data.BatchComboSend.BatchComboNum)
		}
		if data.BatchComboSend.BlindGift != nil && strings.TrimSpace(data.BatchComboSend.BlindGift.GiftAction) != "" {
			action = strings.TrimSpace(data.BatchComboSend.BlindGift.GiftAction)
		}
	}
	var details []string
	if data.CoinType == "gold" && data.TotalCoin > 0 {
		battery := int64(data.TotalCoin) / 100
		if battery > 0 {
			details = append(details, fmt.Sprintf("%d电池", battery))
		}
	}
	if combo > 1 {
		details = append(details, fmt.Sprintf("连击x%d", combo))
	}
	detailText := ""
	if len(details) > 0 {
		detailText = fmt.Sprintf(" (%s)", strings.Join(details, " · "))
	}
	text := fmt.Sprintf("%s %s ×%d%s", action, data.GiftName, count, detailText)
	return DanmakuEvent{Kind: DanmakuEventGift, Command: command, Message: DanmakuMessage{
		Username:      data.Uname,
		UserID:        string(data.UID),
		Text:          text,
		GiftName:      data.GiftName,
		GiftCount:     count,
		GiftAction:    action,
		GiftCoinType:  data.CoinType,
		GiftTotalCoin: int64(data.TotalCoin),
		GiftCombo:     combo,
		BatchComboID:  batchComboID,
		MedalName:     strings.TrimSpace(data.MedalInfo.MedalName),
		MedalLevel:    int(data.MedalInfo.MedalLevel),
		GuardLevel:    int(data.MedalInfo.GuardLevel),
		WealthLevel:   int(data.WealthLevel),
		Timestamp:     time.Now(),
	}}, true, nil
}

func parseComboSend(raw json.RawMessage, command string) (DanmakuEvent, bool, error) {
	var data struct {
		UID            flexibleID    `json:"uid"`
		Uname          string        `json:"uname"`
		GiftName       string        `json:"gift_name"`
		GiftNum        flexibleInt64 `json:"gift_num"`
		ComboNum       flexibleInt64 `json:"combo_num"`
		BatchComboNum  flexibleInt64 `json:"batch_combo_num"`
		BatchComboID   string        `json:"batch_combo_id"`
		ComboTotalCoin flexibleInt64 `json:"combo_total_coin"`
		Action         string        `json:"action"`
		MedalInfo      struct {
			MedalName  string        `json:"medal_name"`
			MedalLevel flexibleInt64 `json:"medal_level"`
			GuardLevel flexibleInt64 `json:"guard_level"`
		} `json:"medal_info"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return DanmakuEvent{}, false, fmt.Errorf("解析连击消息失败: %w", err)
	}
	giftName := strings.TrimSpace(data.GiftName)
	if giftName == "" {
		return DanmakuEvent{}, false, nil
	}
	combo := int(data.BatchComboNum)
	if combo <= 0 {
		combo = int(data.ComboNum)
	}
	count := int(data.GiftNum)
	if count <= 0 {
		count = 1
	}
	action := strings.TrimSpace(data.Action)
	if action == "" {
		action = "投喂"
	}
	var details []string
	if data.ComboTotalCoin > 0 {
		battery := int64(data.ComboTotalCoin) / 100
		if battery > 0 {
			details = append(details, fmt.Sprintf("%d电池", battery))
		}
	}
	if combo > 1 {
		details = append(details, fmt.Sprintf("连击x%d", combo))
	}
	detailText := ""
	if len(details) > 0 {
		detailText = fmt.Sprintf(" (%s)", strings.Join(details, " · "))
	}
	text := fmt.Sprintf("%s %s ×%d%s", action, giftName, count, detailText)
	return DanmakuEvent{
		Kind:    DanmakuEventGift,
		Command: command,
		Message: DanmakuMessage{
			Username:      strings.TrimSpace(data.Uname),
			UserID:        string(data.UID),
			Text:          text,
			GiftName:      giftName,
			GiftCount:     count,
			GiftAction:    action,
			GiftTotalCoin: int64(data.ComboTotalCoin),
			GiftCombo:     combo,
			BatchComboID:  strings.TrimSpace(data.BatchComboID),
			MedalName:     strings.TrimSpace(data.MedalInfo.MedalName),
			MedalLevel:    int(data.MedalInfo.MedalLevel),
			GuardLevel:    int(data.MedalInfo.GuardLevel),
			Timestamp:     time.Now(),
		},
	}, true, nil
}

func parseSuperChat(raw json.RawMessage, command string) (DanmakuEvent, bool, error) {
	var data struct {
		ID        flexibleInt64 `json:"id"`
		UID       flexibleID    `json:"uid"`
		Price     flexibleInt64 `json:"price"`
		Message   string        `json:"message"`
		Time      flexibleInt64 `json:"time"`
		StartTime flexibleInt64 `json:"start_time"`
		UserInfo  struct {
			Uname      string        `json:"uname"`
			Face       string        `json:"face"`
			UserLevel  flexibleInt64 `json:"user_level"`
			GuardLevel flexibleInt64 `json:"guard_level"`
		} `json:"user_info"`
		MedalInfo struct {
			MedalName  string        `json:"medal_name"`
			MedalLevel flexibleInt64 `json:"medal_level"`
			GuardLevel flexibleInt64 `json:"guard_level"`
		} `json:"medal_info"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return DanmakuEvent{}, false, fmt.Errorf("解析醒目留言失败: %w", err)
	}
	uname := strings.TrimSpace(data.UserInfo.Uname)
	if uname == "" {
		uname = "匿名用户"
	}
	guardLevel := int(data.UserInfo.GuardLevel)
	if guardLevel == 0 {
		guardLevel = int(data.MedalInfo.GuardLevel)
	}
	return DanmakuEvent{
		Kind:    DanmakuEventSuperChat,
		Command: command,
		Message: DanmakuMessage{
			Username:   uname,
			UserID:     string(data.UID),
			Text:       strings.TrimSpace(data.Message),
			Price:      int(data.Price),
			Duration:   int(data.Time),
			MedalName:  strings.TrimSpace(data.MedalInfo.MedalName),
			MedalLevel: int(data.MedalInfo.MedalLevel),
			GuardLevel: guardLevel,
			UserLevel:  int(data.UserInfo.UserLevel),
			Timestamp:  time.Now(),
		},
	}, true, nil
}

func guardLevelName(level int) string {
	switch level {
	case 1:
		return "总督"
	case 2:
		return "提督"
	case 3:
		return "舰长"
	default:
		return "大航海"
	}
}

func parseGuardBuy(raw json.RawMessage, command string) (DanmakuEvent, bool, error) {
	var data struct {
		UID        flexibleID    `json:"uid"`
		Username   string        `json:"username"`
		GuardLevel flexibleInt64 `json:"guard_level"`
		Num        flexibleInt64 `json:"num"`
		Price      flexibleInt64 `json:"price"`
		GiftName   string        `json:"gift_name"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return DanmakuEvent{}, false, fmt.Errorf("解析上舰消息失败: %w", err)
	}
	username := strings.TrimSpace(data.Username)
	if username == "" {
		username = "匿名用户"
	}
	guardName := guardLevelName(int(data.GuardLevel))
	if data.GiftName != "" {
		guardName = data.GiftName
	}
	count := int(data.Num)
	if count <= 0 {
		count = 1
	}
	text := fmt.Sprintf("登船成为 %s ×%d个月", guardName, count)
	return DanmakuEvent{
		Kind:    DanmakuEventGuard,
		Command: command,
		Message: DanmakuMessage{
			Username:   username,
			UserID:     string(data.UID),
			Text:       text,
			GuardLevel: int(data.GuardLevel),
			GiftName:   guardName,
			GiftCount:  count,
			Price:      int(data.Price / 1000),
			Timestamp:  time.Now(),
		},
	}, true, nil
}

func parseUserToastMsg(raw json.RawMessage, command string) (DanmakuEvent, bool, error) {
	var data struct {
		UID        flexibleID    `json:"uid"`
		Username   string        `json:"username"`
		GuardLevel flexibleInt64 `json:"guard_level"`
		RoleName   string        `json:"role_name"`
		Num        flexibleInt64 `json:"num"`
		Unit       string        `json:"unit"`
		Price      flexibleInt64 `json:"price"`
		ToastMsg   string        `json:"toast_msg"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return DanmakuEvent{}, false, fmt.Errorf("解析上舰广播失败: %w", err)
	}
	username := strings.TrimSpace(data.Username)
	if username == "" {
		username = "匿名用户"
	}
	guardName := guardLevelName(int(data.GuardLevel))
	if data.RoleName != "" {
		guardName = data.RoleName
	}
	count := int(data.Num)
	if count <= 0 {
		count = 1
	}
	unit := strings.TrimSpace(data.Unit)
	if unit == "" {
		unit = "月"
	}
	text := fmt.Sprintf("登船成为 %s ×%d%s", guardName, count, unit)
	return DanmakuEvent{
		Kind:    DanmakuEventGuard,
		Command: command,
		Message: DanmakuMessage{
			Username:   username,
			UserID:     string(data.UID),
			Text:       text,
			GuardLevel: int(data.GuardLevel),
			GiftName:   guardName,
			GiftCount:  count,
			Price:      int(data.Price / 1000),
			Timestamp:  time.Now(),
		},
	}, true, nil
}

func parseWarning(raw json.RawMessage, envelopeMsg, command string) (DanmakuEvent, bool, error) {
	msg := strings.TrimSpace(envelopeMsg)
	if len(raw) > 0 {
		var data struct {
			Msg string `json:"msg"`
		}
		if json.Unmarshal(raw, &data) == nil && strings.TrimSpace(data.Msg) != "" {
			msg = strings.TrimSpace(data.Msg)
		}
	}
	if msg == "" {
		if command == "CUT_OFF" {
			msg = "直播已被超管切断/关闭"
		} else {
			msg = "收到超管直播警告，请注意直播合规"
		}
	}
	return DanmakuEvent{
		Kind:    DanmakuEventWarning,
		Command: command,
		Message: DanmakuMessage{
			Username:  "超管警告",
			Text:      msg,
			Timestamp: time.Now(),
		},
	}, true, nil
}

func parseRoomChange(raw json.RawMessage, command string) (DanmakuEvent, bool, error) {
	var data struct {
		Title    string `json:"title"`
		AreaName string `json:"area_name"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return DanmakuEvent{}, false, fmt.Errorf("解析房间变更消息失败: %w", err)
	}
	text := "房间信息已更新"
	if data.Title != "" && data.AreaName != "" {
		text = fmt.Sprintf("房间变更：【%s】 分区：%s", data.Title, data.AreaName)
	} else if data.Title != "" {
		text = fmt.Sprintf("房间标题更新：【%s】", data.Title)
	} else if data.AreaName != "" {
		text = fmt.Sprintf("房间分区更新：%s", data.AreaName)
	}
	return DanmakuEvent{
		Kind:    DanmakuEventSystem,
		Command: command,
		Message: DanmakuMessage{
			Username:  "系统通知",
			Text:      text,
			Timestamp: time.Now(),
		},
	}, true, nil
}

func parseRoomBlockMsg(raw json.RawMessage, command string) (DanmakuEvent, bool, error) {
	var data struct {
		UID      flexibleID    `json:"uid"`
		Uname    string        `json:"uname"`
		Operator flexibleInt64 `json:"operator"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return DanmakuEvent{}, false, fmt.Errorf("解析禁言广播失败: %w", err)
	}
	uname := strings.TrimSpace(data.Uname)
	if uname == "" {
		uname = string(data.UID)
	}
	op := "管理员"
	if data.Operator == 1 {
		op = "主播"
	}
	text := fmt.Sprintf("%s 已被%s禁言", uname, op)
	return DanmakuEvent{
		Kind:    DanmakuEventSystem,
		Command: command,
		Message: DanmakuMessage{
			Username:  "禁言通知",
			Text:      text,
			UserID:    string(data.UID),
			Timestamp: time.Now(),
		},
	}, true, nil
}

func parseLiveState(raw json.RawMessage, isLive bool, command string) (DanmakuEvent, bool, error) {
	text := "直播已开始"
	if !isLive {
		text = "直播已结束"
	}
	return DanmakuEvent{
		Kind:    DanmakuEventSystem,
		Command: command,
		Message: DanmakuMessage{
			Username:  "系统通知",
			Text:      text,
			Timestamp: time.Now(),
		},
	}, true, nil
}

func applyStructuredDanmakuUser(raw json.RawMessage, message *DanmakuMessage) {
	if message == nil {
		return
	}
	var metadata []json.RawMessage
	if json.Unmarshal(raw, &metadata) != nil || len(metadata) <= 15 {
		return
	}
	var extra struct {
		User *struct {
			UID  flexibleID `json:"uid"`
			Base struct {
				Name      string `json:"name"`
				IsMystery bool   `json:"is_mystery"`
			} `json:"base"`
			Medal *struct {
				Name       string        `json:"name"`
				Level      flexibleInt64 `json:"level"`
				GuardLevel flexibleInt64 `json:"guard_level"`
			} `json:"medal"`
			Wealth *struct {
				Level flexibleInt64 `json:"level"`
			} `json:"wealth"`
		} `json:"user"`
	}
	if json.Unmarshal(metadata[15], &extra) != nil || extra.User == nil {
		return
	}
	user := extra.User
	if value := strings.TrimSpace(string(user.UID)); value != "" {
		message.UserID = value
	}
	if value := strings.TrimSpace(user.Base.Name); value != "" {
		message.Username = value
	}
	message.IsMystery = user.Base.IsMystery
	if user.Medal != nil {
		if value := strings.TrimSpace(user.Medal.Name); value != "" {
			message.MedalName = value
		}
		if user.Medal.Level > 0 {
			message.MedalLevel = int(user.Medal.Level)
		}
		if user.Medal.GuardLevel > 0 {
			message.GuardLevel = int(user.Medal.GuardLevel)
		}
	}
	if user.Wealth != nil && user.Wealth.Level > 0 {
		message.WealthLevel = int(user.Wealth.Level)
	}
}

func parseLikeInteraction(raw json.RawMessage, command string) (DanmakuEvent, bool, error) {
	var data struct {
		UID        flexibleID    `json:"uid"`
		Uname      string        `json:"uname"`
		ClickCount flexibleInt64 `json:"click_count"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return DanmakuEvent{}, false, fmt.Errorf("解析点赞消息失败: %w", err)
	}
	if strings.TrimSpace(data.Uname) == "" {
		return DanmakuEvent{}, false, nil
	}
	text := "点赞了直播间"
	if data.ClickCount > 1 {
		text = fmt.Sprintf("点赞了直播间 ×%d", data.ClickCount)
	}
	return DanmakuEvent{Kind: DanmakuEventSystem, Command: command, Message: DanmakuMessage{
		Username:  data.Uname,
		UserID:    string(data.UID),
		Text:      text,
		Timestamp: time.Now(),
	}}, true, nil
}

func parseSystemInteraction(raw json.RawMessage, command string) (DanmakuEvent, bool, error) {
	var data struct {
		UID     flexibleID    `json:"uid"`
		Uname   string        `json:"uname"`
		MsgType flexibleInt64 `json:"msg_type"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return DanmakuEvent{}, false, fmt.Errorf("解析互动消息失败: %w", err)
	}
	if data.Uname == "" {
		return DanmakuEvent{}, false, nil
	}
	if command == "INTERACT_WORD" && data.MsgType == 1 {
		// 普通进房事件数量远高于真实弹幕，在活跃房间会迅速淹没聊天内容。
		// 关注、分享等主动互动仍保留为可见消息。
		return DanmakuEvent{}, false, nil
	}
	text := map[string]string{
		"WELCOME":       "进入了直播间",
		"WELCOME_GUARD": "进入了直播间",
		"INTERACT_WORD": "来过直播间",
	}[command]
	if command == "INTERACT_WORD" {
		if data.MsgType == 2 {
			text = "关注了直播间"
		} else {
			text = "与直播间互动"
		}
	}
	if text == "" {
		return DanmakuEvent{}, false, nil
	}
	return DanmakuEvent{Kind: DanmakuEventSystem, Command: command, Message: DanmakuMessage{
		Username:  data.Uname,
		UserID:    string(data.UID),
		Text:      text,
		Timestamp: time.Now(),
	}}, true, nil
}

func jsonStringAt(values []json.RawMessage, index int) string {
	if index < 0 || index >= len(values) {
		return ""
	}
	var text string
	if json.Unmarshal(values[index], &text) == nil {
		return strings.TrimSpace(text)
	}
	var number json.Number
	if json.Unmarshal(values[index], &number) == nil {
		return number.String()
	}
	return ""
}

func jsonIntAt(values []json.RawMessage, index int) int {
	value, err := strconv.Atoi(jsonStringAt(values, index))
	if err != nil {
		return 0
	}
	return value
}

// SendDanmaku 通过 B 站 Web API 发送一条认证弹幕。
// 未预先查询房间约束的调用方使用默认的兜底上限。
func (c *Client) SendDanmaku(ctx context.Context, roomID, sessdata, biliJCT, message string) error {
	return c.SendDanmakuWithLimit(ctx, roomID, sessdata, biliJCT, message, DefaultDanmakuMaxLength)
}

// SendDanmakuWithLimit 使用 B 站为当前账号和直播间下发的字数上限发送弹幕。
func (c *Client) SendDanmakuWithLimit(ctx context.Context, roomID, sessdata, biliJCT, message string, maxLength int) error {
	if strings.TrimSpace(roomID) == "" {
		return fmt.Errorf("发送弹幕需要房间号")
	}
	message = strings.TrimSpace(message)
	if message == "" {
		return fmt.Errorf("弹幕内容不能为空")
	}
	if maxLength <= 0 {
		maxLength = DefaultDanmakuMaxLength
	}
	if length := len([]rune(message)); length > maxLength {
		return fmt.Errorf("弹幕内容不能超过 %d 个字符（当前 %d 个）", maxLength, length)
	}
	if strings.TrimSpace(sessdata) == "" || strings.TrimSpace(biliJCT) == "" {
		return fmt.Errorf("发送弹幕需要有效的 SESSDATA 和 bili_jct")
	}
	identity, err := c.resolveDanmakuIdentity(ctx, sessdata, biliJCT)
	if err != nil {
		return fmt.Errorf("准备发送弹幕失败: %w", err)
	}
	params := url.Values{}
	params.Set("bubble", "0")
	params.Set("msg", message)
	params.Set("color", "16777215")
	params.Set("mode", "1")
	params.Set("fontsize", "25")
	params.Set("rnd", strconv.FormatInt(time.Now().Unix(), 10))
	params.Set("roomid", strings.TrimSpace(roomID))
	params.Set("room_type", "0")
	params.Set("csrf", biliJCT)
	params.Set("csrf_token", biliJCT)
	var result struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Msg     string          `json:"msg"`
		Data    json.RawMessage `json:"data"`
	}
	path, err := c.endpointByName("SendDanmaku")
	if err != nil {
		return err
	}
	headers := make(http.Header)
	headers.Set("Cookie", danmakuBrowserCookie(sessdata, biliJCT, identity))
	headers.Set("Referer", "https://live.bilibili.com/"+strings.TrimSpace(roomID))
	headers.Set("Origin", "https://live.bilibili.com")
	headers.Set("User-Agent", biliBrowserUserAgent)
	headers.Set("Accept", "application/json")
	sendErr := c.postFormWithHeaders(ctx, path, params, &result, headers)
	if sendErr != nil {
		return fmt.Errorf("%w: %w", ErrDanmakuDeliveryUnknown, sendErr)
	}
	if isProhibitedDanmakuResponse(result.Code, result.Message, result.Msg, result.Data) {
		return ErrDanmakuProhibited
	}
	if result.Code != 0 {
		message := strings.TrimSpace(result.Message)
		if message == "" {
			message = strings.TrimSpace(result.Msg)
		}
		if message == "" {
			message = "B 站未返回具体原因"
		}
		return fmt.Errorf("B 站拒绝发送（错误码 %d）：%s", result.Code, message)
	}
	return nil
}

// ErrDanmakuDeliveryUnknown 表示请求过程中断，无法判断 B 站是否已经收到弹幕。
// 调用方不应自动重试，否则响应丢失时可能发送出重复内容。
var ErrDanmakuDeliveryUnknown = errors.New("无法确认弹幕是否已发送")

// IsDanmakuDeliveryUnknown 判断发送结果是否因连接中断而无法确认。
func IsDanmakuDeliveryUnknown(err error) bool {
	return errors.Is(err, ErrDanmakuDeliveryUnknown)
}

// ErrDanmakuShielded 表示 B 站返回过滤标记，具体原因未知。
var ErrDanmakuShielded = errors.New("这条弹幕被 B 站拦截了。")

// ErrDanmakuProhibited 为兼容旧调用的别名。
var ErrDanmakuProhibited = ErrDanmakuShielded

// IsDanmakuProhibited 判断错误是否包含 B 站的过滤提示。
func IsDanmakuProhibited(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrDanmakuShielded) || errors.Is(err, ErrDanmakuProhibited) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "违禁词") ||
		strings.Contains(msg, "敏感") ||
		strings.Contains(msg, "违规") ||
		strings.Contains(msg, "blacklist") ||
		strings.Contains(msg, "10030") ||
		strings.Contains(msg, "10012") ||
		strings.Contains(msg, "屏蔽词")
}

func isProhibitedDanmakuResponse(code int, message, msg string, data ...json.RawMessage) bool {
	cleanMessage := strings.TrimSpace(message)
	cleanMsg := strings.TrimSpace(msg)
	cleanData := ""
	if len(data) > 0 && len(data[0]) > 0 {
		cleanData = strings.TrimSpace(string(data[0]))
	}
	if strings.EqualFold(cleanMessage, "f") || strings.EqualFold(cleanMsg, "f") {
		return true
	}
	if strings.EqualFold(cleanData, `"f"`) || strings.EqualFold(cleanData, `["f"]`) || cleanData == "f" {
		return true
	}
	combined := strings.ToLower(cleanMessage + " " + cleanMsg)
	for _, keyword := range []string{
		"filter",
		"blacklist",
		"违禁",
		"敏感",
		"违规",
		"屏蔽词",
		"关键词",
		"不合规",
		"非法字符",
		"包含限制内容",
	} {
		if strings.Contains(combined, keyword) {
			return true
		}
	}
	return code == 10030 || code == 10012
}
