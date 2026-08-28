package api

import (
	"context"
	"crypto/tls"
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

	"github.com/mattn/go-ieproxy"
)

const DefaultBaseURL = "https://api.live.bilibili.com"

func newAPITransport() *http.Transport {
	dialer := &net.Dialer{
		Timeout:   6 * time.Second,
		KeepAlive: 5 * time.Second,
	}
	return &http.Transport{
		Proxy:                 systemProxyFunc(),
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     false,
		TLSNextProto:          make(map[string]func(string, *tls.Conn) http.RoundTripper),
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   10,
		IdleConnTimeout:       15 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 8 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
}

var defaultAPITransport = newAPITransport()

// systemProxyFunc 保留 Go 对代理环境变量的处理，并在 Windows 上读取当前用户的
// WinINET 设置，包括静态代理、PAC 脚本和自动发现。
func systemProxyFunc() func(*http.Request) (*url.URL, error) {
	return ieproxy.GetProxyFunc()
}

const biliBrowserUserAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0 Safari/537.36"

const maxAPIResponseBytes int64 = 4 * 1024 * 1024



const (
	OrientationLandscape = "landscape"
	OrientationPortrait  = "portrait"
)

// LiveSettings 是开播前显示并提交到直播间的资料。
// AreaID 是 B 站 area_v2 分区编号（例如 376）。
type LiveSettings struct {
	Title        string
	Description  string
	Announcement string
	Tags         string
	AreaID       string
	// CoverPath 可以是本地图片路径或远程图片地址，提交房间资料前都会上传到 B 站。
	CoverPath string
	// StreamMode、OBSHost、OBSPort 和 OBSPassword 是本地启动选项，API 不会把它们写入 B 站房间资料。
	StreamMode  string
	OBSHost     string
	OBSPort     string
	OBSPassword string
	Orientation string
	// TagIDsJSON 保存标签名称到 B 站编号的映射，用于后续删除。
	TagIDsJSON string `json:"tag_ids,omitempty"`
}

func (s LiveSettings) Validate() error {
	if strings.TrimSpace(s.Title) == "" {
		return fmt.Errorf("直播标题不能为空")
	}
	if orientation := strings.TrimSpace(s.Orientation); orientation != "" && orientation != OrientationLandscape && orientation != OrientationPortrait {
		return fmt.Errorf("直播方向无效")
	}
	if value := strings.TrimSpace(s.OBSHost); value != "" {
		host := strings.Trim(value, "[]")
		if strings.Contains(value, "://") || strings.ContainsAny(value, "/\\ 	\r\n") || (strings.Contains(host, ":") && net.ParseIP(host) == nil) {
			return fmt.Errorf("OBS WebSocket 地址应填写主机名或 IP，不要包含协议、路径或端口")
		}
	}
	if value := strings.TrimSpace(s.OBSPort); value != "" {
		port, err := strconv.Atoi(value)
		if err != nil || port < 1 || port > 65535 {
			return fmt.Errorf("OBS WebSocket 端口必须是 1 到 65535 之间的数字")
		}
	}
	if strings.TrimSpace(s.AreaID) == "" {
		return fmt.Errorf("分区不能为空")
	}
	areaID, err := strconv.Atoi(s.AreaID)
	if err != nil {
		return fmt.Errorf("分区 ID 必须是数字")
	}
	if areaID <= 0 {
		return fmt.Errorf("分区 ID 必须是正数")
	}
	return nil
}

type LiveInfoResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Msg     string `json:"msg"`
	Data    struct {
		RoomID int `json:"room_id"`
	} `json:"data"`
}

type StartLiveResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Msg     string `json:"msg"`
	Data    struct {
		RTMP struct {
			Addr string `json:"addr"`
			Code string `json:"code"`
		} `json:"rtmp"`
	} `json:"data"`
}

type LiveArea struct {
	ID         string
	Name       string
	ParentID   string
	ParentName string
}

// RoomSnapshot 是直播主页使用的公开房间状态摘要。
// Online 是 B 站返回的当前人气，Watched 是接口提供时的累计观看人数。
type RoomSnapshot struct {
	AnchorID       string
	RoomID         string
	Title          string
	Description    string
	Tags           string
	Cover          string
	AreaID         string
	AreaName       string
	ParentAreaName string
	LiveStatus     int
	LiveTime       time.Time
	Online         int64
	OnlineKnown    bool
	Watched        int64
	WatchedKnown   bool
}

// OnlineRankMember 是网页端在线高能榜公开展示的成员。
// 该榜单只返回少量高能用户，不能视为完整在线成员名单。
type OnlineRankMember struct {
	UserID     string
	Username   string
	Rank       int
	Score      int64
	GuardLevel int
}

// OnlineRankSnapshot 同时包含接口返回的在线人数和在线高能榜。
type OnlineRankSnapshot struct {
	Online  int64
	Members []OnlineRankMember
}

// GuardMember 是直播间大航海（舰队）成员。
type GuardMember struct {
	UserID     string
	Username   string
	GuardLevel int
	Rank       int
	IsAlive    bool
}

// GuardSnapshot 是直播间大航海（舰队）快照。
type GuardSnapshot struct {
	Total   int64
	Members []GuardMember
}

type roomInfoWire struct {
	AnchorID       flexibleID     `json:"uid"`
	RoomID         flexibleID     `json:"room_id"`
	Title          string         `json:"title"`
	Description    string         `json:"description"`
	Tags           string         `json:"tags"`
	Cover          string         `json:"cover"`
	AreaID         flexibleID     `json:"area_id"`
	AreaName       string         `json:"area_name"`
	ParentAreaName string         `json:"parent_area_name"`
	LiveStatus     int            `json:"live_status"`
	LiveStartTime  *flexibleInt64 `json:"live_start_time"`
	LiveTime       string         `json:"live_time"`
	Online         *flexibleInt64 `json:"online"`
}

// GetRoomSnapshot 获取当前公开房间状态，不需要 Cookie，可在 TUI 打开时安全刷新。
func (c *Client) GetRoomSnapshot(ctx context.Context, roomID string) (RoomSnapshot, error) {
	path, err := c.endpointByName("GetRoomSnapshot")
	if err != nil {
		return RoomSnapshot{}, err
	}
	snapshot, primaryErr := c.getRoomSnapshotAt(ctx, path, roomID)
	if primaryErr == nil {
		return snapshot, nil
	}
	// B 站有时会对新版 Web 接口返回 -352 风控错误，旧房间接口提供相同的基础字段，
	// 可作为直播主页的安全备用接口。
	if !shouldFallbackRoomSnapshot(primaryErr) {
		return RoomSnapshot{}, primaryErr
	}
	legacyPath, legacyLookupErr := c.endpointByName("GetRoomSnapshotLegacy")
	if legacyLookupErr == nil {
		if snapshot, legacyErr := c.getRoomSnapshotAt(ctx, legacyPath, roomID); legacyErr == nil {
			return snapshot, nil
		}
	}
	return RoomSnapshot{}, primaryErr
}

func shouldFallbackRoomSnapshot(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return strings.Contains(message, "-352") || strings.Contains(message, "风控") || strings.Contains(message, "HTTP 404")
}

func (c *Client) getRoomSnapshotAt(ctx context.Context, path, roomID string) (RoomSnapshot, error) {
	parsed, err := url.Parse(path)
	if err != nil {
		return RoomSnapshot{}, fmt.Errorf("准备获取直播状态失败: %w", err)
	}
	query := parsed.Query()
	query.Set("room_id", strings.TrimSpace(roomID))
	// 房间封面更新后 CDN/网关可能短暂缓存旧响应，状态查询加时间戳避免客户端复用旧结果。
	query.Set("_", strconv.FormatInt(time.Now().UnixNano(), 10))
	parsed.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return RoomSnapshot{}, fmt.Errorf("准备获取直播状态失败: %w", err)
	}
	setBilibiliBrowserHeaders(req)
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return RoomSnapshot{}, fmt.Errorf("获取直播状态失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return RoomSnapshot{}, fmt.Errorf("获取直播状态失败：远程服务器返回 HTTP %d", resp.StatusCode)
	}
	var raw struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Msg     string `json:"msg"`
		Data    struct {
			RoomInfo *roomInfoWire `json:"room_info"`
			roomInfoWire
			WatchedShow struct {
				Num *flexibleInt64 `json:"num"`
			} `json:"watched_show"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return RoomSnapshot{}, fmt.Errorf("解析直播状态失败: %w", err)
	}
	if raw.Code != 0 {
		return RoomSnapshot{}, fmt.Errorf("获取直播状态失败（错误码 %d）：%s", raw.Code, responseMessage(raw.Message, raw.Msg))
	}
	info := raw.Data.RoomInfo
	if info == nil {
		info = &raw.Data.roomInfoWire
	}
	if strings.TrimSpace(string(info.RoomID)) == "" {
		return RoomSnapshot{}, fmt.Errorf("直播状态接口未返回房间信息")
	}
	online, onlineKnown := int64(0), info.Online != nil
	if onlineKnown {
		online = int64(*info.Online)
	}
	watched, watchedKnown := int64(0), raw.Data.WatchedShow.Num != nil
	if watchedKnown {
		watched = int64(*raw.Data.WatchedShow.Num)
	}
	var liveTime time.Time
	if info.LiveStatus == 1 {
		if info.LiveStartTime != nil && *info.LiveStartTime > 0 {
			liveTime = time.Unix(int64(*info.LiveStartTime), 0)
		} else if rawLiveTime := strings.TrimSpace(info.LiveTime); rawLiveTime != "" && rawLiveTime != "0000-00-00 00:00:00" {
			if parsed, err := time.ParseInLocation("2006-01-02 15:04:05", rawLiveTime, time.Local); err == nil {
				liveTime = parsed
			}
		}
	}
	return RoomSnapshot{
		AnchorID:       string(info.AnchorID),
		RoomID:         string(info.RoomID),
		Title:          info.Title,
		Description:    info.Description,
		Tags:           info.Tags,
		Cover:          info.Cover,
		AreaID:         string(info.AreaID),
		AreaName:       info.AreaName,
		ParentAreaName: info.ParentAreaName,
		LiveStatus:     info.LiveStatus,
		LiveTime:       liveTime,
		Online:         online,
		OnlineKnown:    onlineKnown,
		Watched:        watched,
		WatchedKnown:   watchedKnown,
	}, nil
}

// GetOnlineGoldRankWithCookie 获取在线人数和高能榜。
func (c *Client) GetOnlineGoldRankWithCookie(ctx context.Context, roomID, sessdata, biliJCT string) (OnlineRankSnapshot, error) {
	anchorUID, err := c.resolveAnchorOrUserUID(ctx, roomID, sessdata, biliJCT)
	if err != nil {
		return OnlineRankSnapshot{}, fmt.Errorf("获取在线榜主播身份失败: %w", err)
	}
	return c.getOnlineGoldRank(ctx, roomID, anchorUID, sessdata, biliJCT)
}

func (c *Client) resolveAnchorOrUserUID(ctx context.Context, roomID, sessdata, biliJCT string) (int64, error) {
	if snapshot, err := c.GetRoomSnapshot(ctx, roomID); err == nil {
		if uid, err := strconv.ParseInt(strings.TrimSpace(snapshot.AnchorID), 10, 64); err == nil && uid > 0 {
			return uid, nil
		}
	}
	if strings.TrimSpace(sessdata) != "" {
		if identity, err := c.resolveDanmakuIdentity(ctx, sessdata, biliJCT); err == nil && identity.UID > 0 {
			return identity.UID, nil
		}
	}
	return 0, fmt.Errorf("无法获取主播 UID")
}

// GetGuardTopListWithCookie 自动解析主播 UID 并获取大航海列表快照。
func (c *Client) GetGuardTopListWithCookie(ctx context.Context, roomID, sessdata, biliJCT string) (GuardSnapshot, error) {
	anchorUID, err := c.resolveAnchorOrUserUID(ctx, roomID, sessdata, biliJCT)
	if err != nil {
		return GuardSnapshot{}, fmt.Errorf("获取大航海列表失败: %w", err)
	}
	return c.GetGuardTopList(ctx, roomID, anchorUID, 1)
}

// GetGuardTopList 获取指定房间的大航海（舰队）成员列表。
func (c *Client) GetGuardTopList(ctx context.Context, roomID string, anchorUID int64, page int) (GuardSnapshot, error) {
	roomID = strings.TrimSpace(roomID)
	if roomID == "" || anchorUID <= 0 {
		return GuardSnapshot{}, fmt.Errorf("获取大航海列表需要有效的房间号和主播 UID")
	}
	if page <= 0 {
		page = 1
	}
	path, err := c.endpointByName("GetGuardTopList")
	if err != nil {
		return GuardSnapshot{}, err
	}
	parsed, err := url.Parse(path)
	if err != nil {
		return GuardSnapshot{}, fmt.Errorf("准备获取大航海列表失败: %w", err)
	}
	query := parsed.Query()
	query.Set("roomid", roomID)
	query.Set("ruid", strconv.FormatInt(anchorUID, 10))
	query.Set("page", strconv.Itoa(page))
	parsed.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return GuardSnapshot{}, fmt.Errorf("准备获取大航海列表失败: %w", err)
	}
	setBilibiliBrowserHeaders(req)
	req.Header.Set("Referer", "https://live.bilibili.com/"+roomID)
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return GuardSnapshot{}, fmt.Errorf("获取大航海列表失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return GuardSnapshot{}, fmt.Errorf("获取大航海列表失败：远程服务器返回 HTTP %d", resp.StatusCode)
	}
	var raw struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Msg     string `json:"msg"`
		Data    *struct {
			Info struct {
				Num  flexibleInt64 `json:"num"`
				Page flexibleInt64 `json:"page"`
				Now  flexibleInt64 `json:"now"`
			} `json:"info"`
			Top3 []struct {
				UID        flexibleID    `json:"uid"`
				Username   string        `json:"username"`
				Rank       int           `json:"rank"`
				GuardLevel int           `json:"guard_level"`
				IsAlive    flexibleInt64 `json:"is_alive"`
			} `json:"top3"`
			List []struct {
				UID        flexibleID    `json:"uid"`
				Username   string        `json:"username"`
				Rank       int           `json:"rank"`
				GuardLevel int           `json:"guard_level"`
				IsAlive    flexibleInt64 `json:"is_alive"`
			} `json:"list"`
		} `json:"data"`
	}
	decoder := json.NewDecoder(io.LimitReader(resp.Body, maxAPIResponseBytes+1))
	if err := decoder.Decode(&raw); err != nil {
		return GuardSnapshot{}, fmt.Errorf("解析大航海列表失败: %w", err)
	}
	if raw.Code != 0 {
		return GuardSnapshot{}, fmt.Errorf("获取大航海列表失败（错误码 %d）：%s", raw.Code, responseMessage(raw.Message, raw.Msg))
	}
	if raw.Data == nil {
		return GuardSnapshot{}, fmt.Errorf("大航海接口未返回数据")
	}
	snapshot := GuardSnapshot{Total: int64(raw.Data.Info.Num)}
	appendMember := func(uid string, username string, rank, guardLevel int, isAlive bool) {
		username = strings.TrimSpace(username)
		if username == "" {
			return
		}
		snapshot.Members = append(snapshot.Members, GuardMember{
			UserID:     uid,
			Username:   username,
			Rank:       rank,
			GuardLevel: guardLevel,
			IsAlive:    isAlive,
		})
	}
	if page == 1 {
		for _, item := range raw.Data.Top3 {
			appendMember(string(item.UID), item.Username, item.Rank, item.GuardLevel, item.IsAlive > 0)
		}
	}
	for _, item := range raw.Data.List {
		appendMember(string(item.UID), item.Username, item.Rank, item.GuardLevel, item.IsAlive > 0)
	}
	return snapshot, nil
}

func (c *Client) getOnlineGoldRank(ctx context.Context, roomID string, anchorUID int64, sessdata, biliJCT string) (OnlineRankSnapshot, error) {
	roomID = strings.TrimSpace(roomID)
	if roomID == "" || anchorUID <= 0 {
		return OnlineRankSnapshot{}, fmt.Errorf("获取在线榜需要有效的房间号和主播 UID")
	}
	path, err := c.endpointByName("GetOnlineGoldRank")
	if err != nil {
		return OnlineRankSnapshot{}, err
	}
	parsed, err := url.Parse(path)
	if err != nil {
		return OnlineRankSnapshot{}, fmt.Errorf("准备获取在线榜失败: %w", err)
	}
	query := parsed.Query()
	query.Set("roomId", roomID)
	query.Set("ruid", strconv.FormatInt(anchorUID, 10))
	query.Set("page", "1")
	query.Set("pageSize", "50")
	parsed.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return OnlineRankSnapshot{}, fmt.Errorf("准备获取在线榜失败: %w", err)
	}
	setBilibiliBrowserHeaders(req)
	req.Header.Set("Referer", "https://live.bilibili.com/"+roomID)
	if cookie := browserCookie(sessdata, biliJCT); cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return OnlineRankSnapshot{}, fmt.Errorf("获取在线榜失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return OnlineRankSnapshot{}, fmt.Errorf("获取在线榜失败：远程服务器返回 HTTP %d", resp.StatusCode)
	}
	var raw struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Msg     string `json:"msg"`
		Data    *struct {
			OnlineNum flexibleInt64 `json:"onlineNum"`
			Items     []struct {
				UserRank   int           `json:"userRank"`
				UID        flexibleID    `json:"uid"`
				Name       string        `json:"name"`
				Score      flexibleInt64 `json:"score"`
				GuardLevel int           `json:"guard_level"`
			} `json:"OnlineRankItem"`
		} `json:"data"`
	}
	decoder := json.NewDecoder(io.LimitReader(resp.Body, maxAPIResponseBytes+1))
	if err := decoder.Decode(&raw); err != nil {
		return OnlineRankSnapshot{}, fmt.Errorf("解析在线榜失败: %w", err)
	}
	if raw.Code != 0 {
		return OnlineRankSnapshot{}, fmt.Errorf("获取在线榜失败（错误码 %d）：%s", raw.Code, responseMessage(raw.Message, raw.Msg))
	}
	if raw.Data == nil {
		return OnlineRankSnapshot{}, fmt.Errorf("在线榜接口未返回数据")
	}
	snapshot := OnlineRankSnapshot{Online: int64(raw.Data.OnlineNum)}
	for _, item := range raw.Data.Items {
		username := strings.TrimSpace(item.Name)
		if username == "" {
			continue
		}
		snapshot.Members = append(snapshot.Members, OnlineRankMember{
			UserID:     string(item.UID),
			Username:   username,
			Rank:       item.UserRank,
			Score:      int64(item.Score),
			GuardLevel: item.GuardLevel,
		})
	}
	return snapshot, nil
}

// GetRoomPlaybackURL 获取适合本地播放器预览的直播间回拉地址。
func (c *Client) GetRoomPlaybackURL(ctx context.Context, roomID, sessdata, biliJCT string) (string, error) {
	roomID = strings.TrimSpace(roomID)
	if roomID == "" {
		return "", fmt.Errorf("获取直播预览需要有效的房间号")
	}
	path, err := c.endpointByName("GetRoomPlaybackURL")
	if err != nil {
		return "", err
	}
	parsed, err := url.Parse(path)
	if err != nil {
		return "", fmt.Errorf("准备获取直播预览失败: %w", err)
	}
	query := parsed.Query()
	query.Set("room_id", roomID)
	query.Set("protocol", "0,1")
	query.Set("format", "0,1,2")
	query.Set("codec", "0,1")
	query.Set("qn", "10000")
	query.Set("platform", "web")
	query.Set("ptype", "8")
	parsed.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return "", fmt.Errorf("准备获取直播预览失败: %w", err)
	}
	setBilibiliBrowserHeaders(req)
	req.Header.Set("Referer", "https://live.bilibili.com/"+roomID)
	if cookie := browserCookie(sessdata, biliJCT); cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("获取直播预览失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("获取直播预览失败：远程服务器返回 HTTP %d", resp.StatusCode)
	}
	var raw struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Msg     string `json:"msg"`
		Data    *struct {
			LiveStatus  int `json:"live_status"`
			PlayURLInfo *struct {
				PlayURL struct {
					Streams []struct {
						ProtocolName string `json:"protocol_name"`
						Formats      []struct {
							FormatName string `json:"format_name"`
							Codecs     []struct {
								CodecName string `json:"codec_name"`
								BaseURL   string `json:"base_url"`
								URLInfo   []struct {
									Host  string `json:"host"`
									Extra string `json:"extra"`
								} `json:"url_info"`
							} `json:"codec"`
						} `json:"format"`
					} `json:"stream"`
				} `json:"playurl"`
			} `json:"playurl_info"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxAPIResponseBytes+1)).Decode(&raw); err != nil {
		return "", fmt.Errorf("解析直播预览失败: %w", err)
	}
	if raw.Code != 0 {
		return "", fmt.Errorf("获取直播预览失败（错误码 %d）：%s", raw.Code, responseMessage(raw.Message, raw.Msg))
	}
	if raw.Data == nil || raw.Data.LiveStatus != 1 {
		return "", fmt.Errorf("直播间当前未在直播")
	}
	if raw.Data.PlayURLInfo == nil {
		return "", fmt.Errorf("直播预览接口未返回播放地址")
	}
	bestURL := ""
	bestScore := int(^uint(0) >> 1)
	for _, stream := range raw.Data.PlayURLInfo.PlayURL.Streams {
		for _, format := range stream.Formats {
			for _, codec := range format.Codecs {
				if strings.TrimSpace(codec.BaseURL) == "" {
					continue
				}
				for _, info := range codec.URLInfo {
					candidate := strings.TrimSpace(info.Host) + codec.BaseURL + info.Extra
					playbackURL, parseErr := url.Parse(candidate)
					if parseErr == nil && playbackURL.Host != "" && (playbackURL.Scheme == "https" || playbackURL.Scheme == "http") {
						score := 0
						if !strings.EqualFold(codec.CodecName, "avc") {
							score += 100
						}
						switch strings.ToLower(stream.ProtocolName) {
						case "http_stream":
							// HTTP-FLV 可以边收边播，预览首帧通常比 HLS 更快。
						case "http_hls":
							score += 20
						default:
							score += 40
						}
						switch strings.ToLower(format.FormatName) {
						case "flv":
							// FLV 是 http_stream 的低延迟首选格式。
						case "fmp4":
							score += 2
						case "ts":
							score += 4
						default:
							score += 6
						}
						if score < bestScore {
							bestURL = playbackURL.String()
							bestScore = score
						}
					}
				}
			}
		}
	}
	if bestURL != "" {
		return bestURL, nil
	}
	return "", fmt.Errorf("直播预览接口未返回可用的播放地址")
}

type flexibleID string

type flexibleInt64 int64

type flexibleBool bool

func (value *flexibleBool) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		*value = false
		return nil
	}
	if strings.HasPrefix(trimmed, `"`) {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		trimmed = strings.TrimSpace(s)
	}
	switch strings.ToLower(trimmed) {
	case "", "0", "false":
		*value = false
		return nil
	case "1", "true":
		*value = true
		return nil
	default:
		if n, err := strconv.ParseInt(trimmed, 10, 64); err == nil {
			*value = flexibleBool(n != 0)
			return nil
		}
		return fmt.Errorf("无法将 %q 解析为布尔值", trimmed)
	}
}

func (value *flexibleInt64) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		*value = 0
		return nil
	}
	if strings.HasPrefix(trimmed, `"`) {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		trimmed = strings.TrimSpace(s)
		if trimmed == "" {
			*value = 0
			return nil
		}
	}
	parsed, err := strconv.ParseInt(trimmed, 10, 64)
	if err != nil {
		return err
	}
	*value = flexibleInt64(parsed)
	return nil
}

func (id *flexibleID) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		*id = ""
		return nil
	}
	if strings.HasPrefix(trimmed, `"`) {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		*id = flexibleID(s)
		return nil
	}
	*id = flexibleID(trimmed)
	return nil
}

type Client struct {
	BaseURL    string
	HTTPClient *http.Client

	danmakuIdentityMu     sync.Mutex
	danmakuIdentity       danmakuIdentity
	danmakuIdentityFor    string
	danmakuIdentityAt     time.Time
	danmakuEndpointMu     sync.Mutex
	danmakuEndpointOffset int
}

func NewClient(httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Transport: newAPITransport(), Timeout: 10 * time.Second}
	}
	return &Client{BaseURL: DefaultBaseURL, HTTPClient: httpClient}
}

// CloseIdleConnections 关闭底层连接池中的所有空闲连接，避免网络切换或断网后复用半关连接。
func (c *Client) CloseIdleConnections() {
	if c != nil && c.HTTPClient != nil && c.HTTPClient.Transport != nil {
		tr, ok := c.HTTPClient.Transport.(interface{ CloseIdleConnections() })
		if ok {
			tr.CloseIdleConnections()
		}
	}
}

func (c *Client) do(req *http.Request) (*http.Response, error) {
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		c.CloseIdleConnections()
		return nil, err
	}
	return resp, nil
}

func (c *Client) endpoint(path string) string {
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return path
	}
	return strings.TrimRight(c.BaseURL, "/") + path
}

func (c *Client) endpointByName(name string) (string, error) {
	e, ok := EndpointByName(name)
	if !ok {
		return "", fmt.Errorf("未定义 API: %s", name)
	}
	return c.endpoint(e.Path), nil
}

func (c *Client) postForm(ctx context.Context, path string, params url.Values, out any) error {
	return c.postFormWithHeaders(ctx, path, params, out, nil)
}

func (c *Client) postFormWithHeaders(ctx context.Context, path string, params url.Values, out any, headers http.Header) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint(path), strings.NewReader(params.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for key, values := range headers {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	resp, err := c.do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		message := strings.TrimSpace(string(body))
		if message != "" {
			return fmt.Errorf("B 站接口返回 HTTP %d：%s", resp.StatusCode, message)
		}
		return fmt.Errorf("B 站接口返回 HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxAPIResponseBytes+1))
	if err != nil {
		return err
	}
	if int64(len(body)) > maxAPIResponseBytes {
		return fmt.Errorf("B 站接口响应超过 %d MiB 限制", maxAPIResponseBytes/(1024*1024))
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("解析 B 站响应失败: %w", err)
	}
	return nil
}

func (c *Client) GetMyRoomID(ctx context.Context, sessdata string) (string, error) {
	path, err := c.endpointByName("GetMyRoomID")
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, path, nil)
	if err != nil {
		return "", err
	}
	setBilibiliBrowserHeaders(req)
	req.Header.Set("Cookie", "SESSDATA="+sessdata)
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("B 站接口返回 HTTP %d", resp.StatusCode)
	}
	var result LiveInfoResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("解析房间信息失败: %w", err)
	}
	if result.Code != 0 {
		return "", fmt.Errorf("获取我的房间号失败（错误码 %d）：%s", result.Code, responseMessage(result.Message, result.Msg))
	}
	if result.Data.RoomID == 0 {
		return "", fmt.Errorf("该账号尚未开通直播间，请先去 B 站实名认证并开通")
	}
	return strconv.Itoa(result.Data.RoomID), nil
}

// GetLiveAreas 返回当前可用的子分区列表。
func (c *Client) GetLiveAreas(ctx context.Context, accessToken string) ([]LiveArea, error) {
	path, err := c.endpointByName("GetLiveAreas")
	if err != nil {
		return nil, err
	}
	query := url.Values{}
	query.Set("appkey", AppKey)
	query.Set("access_key", accessToken)
	query.Set("platform", "android")
	query.Set("ts", strconv.FormatInt(time.Now().Unix(), 10))
	query.Set("sign", GenerateSign(query))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, path+"?"+query.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("B 站接口返回 HTTP %d", resp.StatusCode)
	}
	var raw struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Msg     string `json:"msg"`
		Data    []struct {
			ID   flexibleID `json:"id"`
			Name string     `json:"name"`
			List []struct {
				ID   flexibleID `json:"id"`
				Name string     `json:"name"`
			} `json:"list"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("解析分区列表失败: %w", err)
	}
	if raw.Code != 0 {
		return nil, fmt.Errorf("获取分区列表失败（错误码 %d）：%s", raw.Code, responseMessage(raw.Message, raw.Msg))
	}
	areas := make([]LiveArea, 0)
	for _, parent := range raw.Data {
		for _, child := range parent.List {
			areas = append(areas, LiveArea{
				ID:         string(child.ID),
				Name:       child.Name,
				ParentID:   string(parent.ID),
				ParentName: parent.Name,
			})
		}
	}
	if len(areas) == 0 {
		return nil, fmt.Errorf("分区列表为空")
	}
	return areas, nil
}

// AddLiveTag 新增一个直播标签，返回 B 站分配的标签编号。
func (c *Client) AddLiveTag(ctx context.Context, roomID, sessdata, biliJCT, content string) (string, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return "", fmt.Errorf("直播标签不能为空")
	}
	params := url.Values{}
	params.Set("room_id", roomID)
	params.Set("tag_content", content)
	params.Set("csrf", biliJCT)
	params.Set("csrf_token", biliJCT)
	var result struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Msg     string `json:"msg"`
		Data    struct {
			TagID flexibleID `json:"tag_id"`
			ID    flexibleID `json:"id"`
		} `json:"data"`
	}
	if err := c.postLiveCookieForm(ctx, "AddLiveTag", sessdata, biliJCT, params, &result); err != nil {
		return "", err
	}
	if result.Code != 0 {
		return "", fmt.Errorf("新增直播标签失败（错误码 %d）：%s", result.Code, responseMessage(result.Message, result.Msg))
	}
	tagID := strings.TrimSpace(string(result.Data.TagID))
	if tagID == "" {
		tagID = strings.TrimSpace(string(result.Data.ID))
	}
	if tagID == "" {
		// 部分版本的接口成功时不返回编号，标签已经写入，后续删除时无法用本地映射定位。
		return "", nil
	}
	return tagID, nil
}

// DeleteLiveTag 删除一个直播标签。
func (c *Client) DeleteLiveTag(ctx context.Context, roomID, sessdata, biliJCT, tagID string) error {
	tagID = strings.TrimSpace(tagID)
	if tagID == "" {
		return fmt.Errorf("直播标签编号不能为空")
	}
	params := url.Values{}
	params.Set("room_id", roomID)
	params.Set("tag_id", tagID)
	params.Set("csrf", biliJCT)
	params.Set("csrf_token", biliJCT)
	var result struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Msg     string `json:"msg"`
	}
	if err := c.postLiveCookieForm(ctx, "DeleteLiveTag", sessdata, biliJCT, params, &result); err != nil {
		return err
	}
	if result.Code != 0 {
		return fmt.Errorf("删除直播标签失败（错误码 %d）：%s", result.Code, responseMessage(result.Message, result.Msg))
	}
	return nil
}

func (c *Client) postLiveCookieForm(ctx context.Context, endpointName, sessdata, biliJCT string, params url.Values, out any) error {
	if strings.TrimSpace(sessdata) == "" || strings.TrimSpace(biliJCT) == "" {
		return fmt.Errorf("调用直播接口需要有效的 SESSDATA 和 bili_jct")
	}
	path, err := c.endpointByName(endpointName)
	if err != nil {
		return err
	}
	headers := http.Header{
		"Cookie":     []string{"SESSDATA=" + sessdata + "; bili_jct=" + biliJCT},
		"User-Agent": []string{biliBrowserUserAgent},
		"Referer":    []string{"https://live.bilibili.com/"},
		"Origin":     []string{"https://live.bilibili.com"},
	}
	return c.postFormWithHeaders(ctx, path, params, out, headers)
}

func validateLiveUserID(label, value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("%s不能为空", label)
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed <= 0 {
		return fmt.Errorf("%s必须是正整数", label)
	}
	return nil
}

func (c *Client) getLiveCookieJSON(ctx context.Context, path, sessdata, biliJCT string, out any) error {
	if strings.TrimSpace(sessdata) == "" || strings.TrimSpace(biliJCT) == "" {
		return fmt.Errorf("调用直播接口需要有效的 SESSDATA 和 bili_jct")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint(path), nil)
	if err != nil {
		return err
	}
	setBilibiliBrowserHeaders(req)
	req.Header.Set("Cookie", browserCookie(sessdata, biliJCT))
	req.Header.Set("Referer", "https://live.bilibili.com/")
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("B 站接口返回 HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxAPIResponseBytes+1))
	if err != nil {
		return err
	}
	if int64(len(body)) > maxAPIResponseBytes {
		return fmt.Errorf("B 站接口响应超过 %d MiB 限制", maxAPIResponseBytes/(1024*1024))
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("解析 B 站响应失败: %w", err)
	}
	return nil
}

func (c *Client) getLiveEndpointJSON(ctx context.Context, endpointName, sessdata, biliJCT string, query url.Values, out any) error {
	path, err := c.endpointByName(endpointName)
	if err != nil {
		return err
	}
	parsed, err := url.Parse(path)
	if err != nil {
		return err
	}
	values := parsed.Query()
	for key, entries := range query {
		for _, entry := range entries {
			values.Add(key, entry)
		}
	}
	parsed.RawQuery = values.Encode()
	return c.getLiveCookieJSON(ctx, parsed.String(), sessdata, biliJCT, out)
}

func (c *Client) postRoomManagementJSON(ctx context.Context, endpointName, sessdata, biliJCT string, params url.Values, out any) error {
	values := make(url.Values, len(params)+2)
	for key, entries := range params {
		values[key] = append([]string(nil), entries...)
	}
	values.Set("csrf", biliJCT)
	values.Set("csrf_token", biliJCT)
	return c.postLiveCookieForm(ctx, endpointName, sessdata, biliJCT, values, out)
}

func (c *Client) roomManagementAction(ctx context.Context, endpointName, action, sessdata, biliJCT string, params url.Values) error {
	var raw struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Msg     string `json:"msg"`
	}
	if err := c.postRoomManagementJSON(ctx, endpointName, sessdata, biliJCT, params, &raw); err != nil {
		return fmt.Errorf("%s失败: %w", action, err)
	}
	if raw.Code != 0 {
		return fmt.Errorf("%s失败（错误码 %d）：%s", action, raw.Code, responseMessage(raw.Message, raw.Msg))
	}
	return nil
}

// UpdateRoomNews 更新直播间公告。
func (c *Client) UpdateRoomNews(ctx context.Context, roomID, sessdata, biliJCT, announcement string) error {
	params := url.Values{}
	params.Set("room_id", roomID)
	params.Set("content", strings.TrimSpace(announcement))
	params.Set("csrf", biliJCT)
	params.Set("csrf_token", biliJCT)
	var result struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Msg     string `json:"msg"`
	}
	if err := c.postLiveCookieForm(ctx, "UpdateRoomNews", sessdata, biliJCT, params, &result); err != nil {
		return err
	}
	if result.Code != 0 {
		return fmt.Errorf("设置直播公告失败（错误码 %d）：%s", result.Code, responseMessage(result.Message, result.Msg))
	}
	return nil
}

// UpdatePreLiveCover 更新开播前使用的封面地址。
func (c *Client) UpdatePreLiveCover(ctx context.Context, roomID, sessdata, biliJCT, coverURL string, orientation ...string) error {
	params := url.Values{}
	coverURL = strings.TrimSpace(coverURL)
	if coverURL == "" {
		return fmt.Errorf("直播封面地址不能为空")
	}
	params.Set("platform", "web")
	params.Set("mobi_app", "web")
	params.Set("build", "1")
	params.Set("cover", coverURL)
	params.Set("coverVertical", "")
	direction := OrientationLandscape
	if len(orientation) > 0 && orientation[0] == OrientationPortrait {
		direction = OrientationPortrait
	}
	if direction == OrientationPortrait {
		params.Set("liveDirectionType", "2")
	} else {
		params.Set("liveDirectionType", "1")
	}
	params.Set("aiCoverTaskId", "")
	params.Set("csrf", biliJCT)
	params.Set("csrf_token", biliJCT)
	params.Set("visit_id", "")
	var result struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Msg     string `json:"msg"`
	}
	if err := c.postLiveCookieForm(ctx, "UpdatePreLiveInfo", sessdata, biliJCT, params, &result); err != nil {
		return err
	}
	if result.Code != 0 {
		return fmt.Errorf("更新直播封面失败（错误码 %d）：%s", result.Code, responseMessage(result.Message, result.Msg))
	}
	return nil
}

// UpdateLiveInfoWithCookie 使用 Room/update 所需的 Web API 凭证更新房间资料，AccessToken 可选。
func (c *Client) UpdateLiveInfoWithCookie(ctx context.Context, roomID, accessToken, sessdata, biliJCT string, settings LiveSettings) error {
	return c.updateLiveInfo(ctx, roomID, accessToken, sessdata, biliJCT, settings, true)
}

// UpdateLiveInfoBeforeStart 更新开播资料，但把分区交给 startLive 接口提交。
// B 站部分账号的 Room/update 接口暂时拒绝修改分区，会返回“系统维护中”。
func (c *Client) UpdateLiveInfoBeforeStart(ctx context.Context, roomID, accessToken, sessdata, biliJCT string, settings LiveSettings) error {
	return c.updateLiveInfo(ctx, roomID, accessToken, sessdata, biliJCT, settings, false)
}

func (c *Client) updateLiveInfo(ctx context.Context, roomID, accessToken, sessdata, biliJCT string, settings LiveSettings, includeArea bool) error {
	if err := settings.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(sessdata) == "" || strings.TrimSpace(biliJCT) == "" {
		return fmt.Errorf("设置开播信息需要有效的 SESSDATA 和 bili_jct")
	}
	params := url.Values{}
	params.Set("room_id", roomID)
	params.Set("title", strings.TrimSpace(settings.Title))
	params.Set("description", settings.Description)
	if includeArea {
		params.Set("area_v2", settings.AreaID)
	}
	params.Set("csrf", biliJCT)
	params.Set("csrf_token", biliJCT)
	var result struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Msg     string `json:"msg"`
	}
	path, err := c.endpointByName("UpdateLiveInfo")
	if err != nil {
		return err
	}
	headers := http.Header{
		"Cookie":     []string{"SESSDATA=" + sessdata + "; bili_jct=" + biliJCT},
		"User-Agent": []string{biliBrowserUserAgent},
		"Referer":    []string{"https://live.bilibili.com/"},
	}
	if err := c.postFormWithHeaders(ctx, path, params, &result, headers); err != nil {
		return err
	}
	if result.Code != 0 {
		msg := responseMessage(result.Message, result.Msg)
		if result.Code == 1 && strings.Contains(msg, "分区") {
			return fmt.Errorf("设置开播信息失败（错误码 %d）：%s，请确认分区 ID 是当前有效的子分区，并检查登录凭证是否仍有效", result.Code, msg)
		}
		return fmt.Errorf("设置开播信息失败（错误码 %d）：%s", result.Code, msg)
	}
	return nil
}

func (c *Client) StartLive(ctx context.Context, roomID, accessToken string, settings LiveSettings) (string, string, error) {
	if err := settings.Validate(); err != nil {
		return "", "", err
	}
	params := url.Values{}
	params.Set("appkey", AppKey)
	params.Set("access_key", accessToken)
	params.Set("room_id", roomID)
	params.Set("platform", "android")
	params.Set("area_v2", settings.AreaID)
	if settings.Orientation == OrientationPortrait {
		params.Set("is_portrait", "1")
	} else {
		params.Set("is_portrait", "0")
	}
	params.Set("ts", strconv.FormatInt(time.Now().Unix(), 10))
	params.Set("sign", GenerateSign(params))
	var result StartLiveResponse
	path, err := c.endpointByName("StartLive")
	if err != nil {
		return "", "", err
	}
	if err := c.postForm(ctx, path, params, &result); err != nil {
		return "", "", err
	}
	if result.Code != 0 {
		return "", "", fmt.Errorf("开播失败（错误码 %d）：%s", result.Code, responseMessage(result.Message, result.Msg))
	}
	if result.Data.RTMP.Addr == "" || result.Data.RTMP.Code == "" {
		return "", "", fmt.Errorf("开播接口未返回有效推流地址")
	}
	return result.Data.RTMP.Addr, result.Data.RTMP.Code, nil
}

func (c *Client) StopLive(ctx context.Context, roomID, accessToken string) error {
	params := url.Values{}
	params.Set("appkey", AppKey)
	params.Set("access_key", accessToken)
	params.Set("room_id", roomID)
	params.Set("platform", "android")
	params.Set("ts", strconv.FormatInt(time.Now().Unix(), 10))
	params.Set("sign", GenerateSign(params))
	var result struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Msg     string `json:"msg"`
	}
	path, err := c.endpointByName("StopLive")
	if err != nil {
		return err
	}
	if err := c.postForm(ctx, path, params, &result); err != nil {
		return err
	}
	if result.Code != 0 {
		return fmt.Errorf("下播失败（错误码 %d）：%s", result.Code, responseMessage(result.Message, result.Msg))
	}
	return nil
}

func responseMessage(message, fallback string) string {
	if message = strings.TrimSpace(message); message != "" {
		return message
	}
	if fallback = strings.TrimSpace(fallback); fallback != "" {
		return fallback
	}
	return "B 站未返回具体原因"
}
