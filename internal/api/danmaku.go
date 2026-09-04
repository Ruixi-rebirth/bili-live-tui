package api

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"encoding/json"
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
	danmakuHeaderLength        = 16
	danmakuProtocolPlain       = 0
	danmakuProtocolHeartbeat   = 1
	danmakuProtocolAuth        = 1
	danmakuProtocolZlib        = 2
	danmakuProtocolBrotli      = 3
	danmakuOperationHeartbeat  = 2
	danmakuOperationOnline     = 3
	danmakuOperationCommand    = 5
	danmakuOperationAuth       = 7
	danmakuOperationAuthReply  = 8
	danmakuHeartbeatInterval   = 30 * time.Second
	danmakuWebSocketReadLimit  = 8 << 20
	danmakuDecodedPayloadLimit = 16 << 20
	danmakuPacketNestingLimit  = 4
)

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
		// 这些参数来自当前直播网页客户端；其中 web_location 用于让 B 站风控层
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
		return DanmakuInfo{}, fmt.Errorf("获取弹幕连接信息失败: %s", raw.Message)
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
	DanmakuEventMessage DanmakuEventKind = "message"
	DanmakuEventGift    DanmakuEventKind = "gift"
	DanmakuEventSystem  DanmakuEventKind = "system"
	DanmakuEventOnline  DanmakuEventKind = "online"
	// DanmakuEventConnected 在服务器接受操作码 7 的认证后产生。
	// TCP/WebSocket 连接成功本身不能证明直播间订阅已经可用。
	DanmakuEventConnected DanmakuEventKind = "connected"
)

// DanmakuMessage 是与 B 站嵌套数组格式无关的标准化弹幕项。
// Timestamp 在解析数据包时设置。
type DanmakuMessage struct {
	Username   string
	UserID     string
	Text       string
	MedalName  string
	MedalLevel int
	GiftName   string
	GiftCount  int
	Timestamp  time.Time
}

// DanmakuEvent 由 DanmakuStream.Events 提供。
type DanmakuEvent struct {
	Kind    DanmakuEventKind
	Message DanmakuMessage
	Online  int64
	Command string
}

// LiveSessionStats 统计只有认证弹幕流才能观察到的本场数据。
// 人气来自弹幕心跳；礼物统计限定在本场会话内，因为 B 站公开房间接口没有可靠的累计礼物总数。
type LiveSessionStats struct {
	GiftEvents      int64
	GiftCount       int64
	Popularity      int64
	PopularityKnown bool
}

func (stats *LiveSessionStats) Observe(event DanmakuEvent) {
	if stats == nil {
		return
	}
	switch event.Kind {
	case DanmakuEventGift:
		stats.GiftEvents++
		count := event.Message.GiftCount
		if count <= 0 {
			count = 1
		}
		stats.GiftCount += int64(count)
	}
}

// DanmakuStream 管理一个已认证的直播弹幕 WebSocket。
// 流结束时会关闭 Events 和 Errors；离开弹幕页时调用方应调用 Close。
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
	defer c.danmakuIdentityMu.Unlock()
	if c.danmakuIdentityFor == sessdata && c.danmakuIdentity.UID > 0 && c.danmakuIdentity.Buvid != "" && time.Since(c.danmakuIdentityAt) < danmakuIdentityCacheTTL {
		return c.danmakuIdentity, nil
	}

	uid, err := c.getDanmakuUID(ctx, sessdata, biliJCT)
	if err != nil {
		return danmakuIdentity{}, err
	}
	buvid, err := c.getDanmakuBuvid(ctx, sessdata, biliJCT)
	if err != nil {
		return danmakuIdentity{}, err
	}
	identity := danmakuIdentity{UID: uid, Buvid: buvid}
	c.danmakuIdentity = identity
	c.danmakuIdentityFor = sessdata
	c.danmakuIdentityAt = time.Now()
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
		return 0, fmt.Errorf("获取弹幕用户身份失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
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
		return "", fmt.Errorf("获取弹幕设备标识失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
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
	for {
		_, data, err := s.conn.ReadMessage()
		if err != nil {
			readErrors <- err
			return
		}
		events, parseErr := parseDanmakuPackets(data)
		if parseErr != nil {
			s.emitError(parseErr)
			continue
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
	}
	if len(fields) > 3 {
		var medal []json.RawMessage
		if err := json.Unmarshal(fields[3], &medal); err == nil && len(medal) >= 2 {
			message.MedalLevel = jsonIntAt(medal, 0)
			message.MedalName = jsonStringAt(medal, 1)
		}
	}
	if strings.TrimSpace(message.Text) == "" {
		return DanmakuEvent{}, false, nil
	}
	return DanmakuEvent{Kind: DanmakuEventMessage, Message: message, Command: command}, true, nil
}

func parseGift(raw json.RawMessage, command string) (DanmakuEvent, bool, error) {
	var data struct {
		UID      flexibleID    `json:"uid"`
		Uname    string        `json:"uname"`
		GiftName string        `json:"giftName"`
		Num      flexibleInt64 `json:"num"`
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
	text := fmt.Sprintf("送出 %s ×%d", data.GiftName, count)
	return DanmakuEvent{Kind: DanmakuEventGift, Command: command, Message: DanmakuMessage{
		Username:  data.Uname,
		UserID:    string(data.UID),
		Text:      text,
		GiftName:  data.GiftName,
		GiftCount: count,
		Timestamp: time.Now(),
	}}, true, nil
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
func (c *Client) SendDanmaku(ctx context.Context, roomID, sessdata, biliJCT, message string) error {
	if strings.TrimSpace(roomID) == "" {
		return fmt.Errorf("发送弹幕需要房间号")
	}
	message = strings.TrimSpace(message)
	if message == "" {
		return fmt.Errorf("弹幕内容不能为空")
	}
	if len([]rune(message)) > 80 {
		return fmt.Errorf("弹幕内容不能超过 80 个字符")
	}
	if strings.TrimSpace(sessdata) == "" || strings.TrimSpace(biliJCT) == "" {
		return fmt.Errorf("发送弹幕需要有效的 SESSDATA 和 bili_jct")
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
		Code    int    `json:"code"`
		Message string `json:"message"`
		Msg     string `json:"msg"`
	}
	path, err := c.endpointByName("SendDanmaku")
	if err != nil {
		return err
	}
	headers := http.Header{
		"Cookie": []string{"SESSDATA=" + sessdata + "; bili_jct=" + biliJCT},
	}
	headers.Set("Referer", "https://live.bilibili.com/"+strings.TrimSpace(roomID))
	headers.Set("Origin", "https://live.bilibili.com")
	headers.Set("User-Agent", biliBrowserUserAgent)
	if err := c.postFormWithHeaders(ctx, path, params, &result, headers); err != nil {
		return err
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
