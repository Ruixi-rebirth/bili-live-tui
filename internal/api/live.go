package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	_ "image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gabriel-vasile/mimetype"
	"github.com/go-resty/resty/v2"
	"github.com/shamspias/fennec"
	xdraw "golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

const DefaultBaseURL = "https://api.live.bilibili.com"

const biliBrowserUserAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0 Safari/537.36"

const maxRoomCoverBytes int64 = 5 * 1024 * 1024

const maxRoomCoverSourceBytes int64 = 64 * 1024 * 1024

// 上传前会把图片等比调整到 B 站图片服务接受的像素范围。
const maxRoomCoverDimension = 4096

// 先检查图片配置，避免恶意图片头部诱使解码器分配过大的像素缓冲区。
const maxRoomCoverSourceDimension = 16384

// 限制解码前的像素总数，避免恶意压缩图片占用过多内存。
const maxRoomCoverSourcePixels int64 = 64 * 1024 * 1024

const minRoomCoverWidth = 640

const minRoomCoverProcessingWidth = 656

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
	// CoverPath 可以是本地图片路径或远程图片地址；提交房间资料前都会上传到 B 站。
	CoverPath string
	// StreamMode 和 OBSPassword 是本地启动选项，API 不会把它们写入 B 站房间资料。
	StreamMode  string
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
	RoomID         string
	Title          string
	Description    string
	Tags           string
	Cover          string
	AreaName       string
	ParentAreaName string
	LiveStatus     int
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

type roomInfoWire struct {
	RoomID         flexibleID     `json:"room_id"`
	Title          string         `json:"title"`
	Description    string         `json:"description"`
	Tags           string         `json:"tags"`
	Cover          string         `json:"cover"`
	AreaName       string         `json:"area_name"`
	ParentAreaName string         `json:"parent_area_name"`
	LiveStatus     int            `json:"live_status"`
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
	// B 站有时会对新版 Web 接口返回 -352 风控错误；旧房间接口提供相同的基础字段，
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
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", biliBrowserUserAgent)
	req.Header.Set("Referer", "https://live.bilibili.com/")
	req.Header.Set("Origin", "https://live.bilibili.com")
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
		return RoomSnapshot{}, fmt.Errorf("获取直播状态失败: %s", raw.Message)
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
	return RoomSnapshot{
		RoomID:         string(info.RoomID),
		Title:          info.Title,
		Description:    info.Description,
		Tags:           info.Tags,
		Cover:          info.Cover,
		AreaName:       info.AreaName,
		ParentAreaName: info.ParentAreaName,
		LiveStatus:     info.LiveStatus,
		Online:         online,
		OnlineKnown:    onlineKnown,
		Watched:        watched,
		WatchedKnown:   watchedKnown,
	}, nil
}

// GetOnlineGoldRankWithCookie 获取在线人数和高能榜。
func (c *Client) GetOnlineGoldRankWithCookie(ctx context.Context, roomID, sessdata, biliJCT string) (OnlineRankSnapshot, error) {
	identity, err := c.resolveDanmakuIdentity(ctx, sessdata, biliJCT)
	if err != nil {
		return OnlineRankSnapshot{}, fmt.Errorf("获取在线榜主播身份失败: %w", err)
	}
	if identity.UID <= 0 {
		return OnlineRankSnapshot{}, fmt.Errorf("获取在线榜主播身份失败: UID 无效")
	}
	return c.getOnlineGoldRank(ctx, roomID, identity.UID, sessdata, biliJCT)
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
		return OnlineRankSnapshot{}, fmt.Errorf("获取在线榜失败: %s", strings.TrimSpace(raw.Message))
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
		return "", fmt.Errorf("获取直播预览失败: %s", strings.TrimSpace(raw.Message))
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

// UploadRoomCover 使用 B 站 Web 图片接口上传本地图片，并返回更新封面接口使用的地址。
// 它与资料更新分开，以便在修改房间资料前先展示上传错误。
func (c *Client) UploadRoomCover(ctx context.Context, roomID, sessdata, biliJCT, filePath string) (string, error) {
	if strings.TrimSpace(sessdata) == "" || strings.TrimSpace(biliJCT) == "" {
		return "", fmt.Errorf("上传直播封面需要有效的 SESSDATA 和 bili_jct")
	}
	path := strings.TrimSpace(filePath)
	if path == "" {
		return "", fmt.Errorf("直播封面文件不能为空")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("打开直播封面失败: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", fmt.Errorf("读取直播封面信息失败: %w", err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("直播封面必须是图片文件")
	}
	if info.Size() > maxRoomCoverSourceBytes {
		return "", fmt.Errorf("直播封面源文件不能超过 64 MB")
	}
	detectedMIME, err := mimetype.DetectFile(path)
	if err != nil {
		return "", fmt.Errorf("识别直播封面失败: %w", err)
	}
	if !isSupportedCoverMIME(detectedMIME) {
		return "", fmt.Errorf("直播封面实际格式不受支持：%s", detectedMIME.String())
	}
	uploadData, uploadType, width, height, err := normalizeCoverForUpload(ctx, file)
	if err != nil {
		return "", fmt.Errorf("处理直播封面失败: %w", err)
	}

	var body bytes.Buffer
	multipartWriter := multipart.NewWriter(&body)
	// Web 上传接口要求先提交 bucket、dir，再提交名为 file 的文件字段。
	if err := multipartWriter.WriteField("bucket", "live"); err != nil {
		return "", fmt.Errorf("准备直播封面上传失败: %w", err)
	}
	if err := multipartWriter.WriteField("dir", "new_room_cover"); err != nil {
		return "", fmt.Errorf("准备直播封面上传失败: %w", err)
	}
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", `form-data; name="file"; filename="blob"`)
	header.Set("Content-Type", uploadType)
	part, err := multipartWriter.CreatePart(header)
	if err != nil {
		return "", fmt.Errorf("准备直播封面上传失败: %w", err)
	}
	if _, err := part.Write(uploadData); err != nil {
		return "", fmt.Errorf("读取直播封面失败: %w", err)
	}
	if err := multipartWriter.Close(); err != nil {
		return "", fmt.Errorf("完成直播封面上传请求失败: %w", err)
	}

	pathURL, err := c.endpointByName("UploadRoomCover")
	if err != nil {
		return "", err
	}
	parsedURL, err := url.Parse(pathURL)
	if err != nil {
		return "", err
	}
	query := parsedURL.Query()
	query.Set("csrf", biliJCT)
	parsedURL.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, parsedURL.String(), bytes.NewReader(body.Bytes()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", multipartWriter.FormDataContentType())
	req.Header.Set("Cookie", "SESSDATA="+sessdata+"; bili_jct="+biliJCT)
	req.Header.Set("User-Agent", biliBrowserUserAgent)
	req.Header.Set("Referer", "https://live.bilibili.com/")
	req.Header.Set("Origin", "https://live.bilibili.com")
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		message := strings.TrimSpace(string(body))
		if message != "" {
			return "", fmt.Errorf("B 站封面上传接口返回 HTTP %d：%s", resp.StatusCode, message)
		}
		return "", fmt.Errorf("B 站封面上传接口返回 HTTP %d", resp.StatusCode)
	}
	var result struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Msg     string `json:"msg"`
		Data    struct {
			URL      string `json:"url"`
			Link     string `json:"link"`
			Location string `json:"location"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("解析直播封面上传响应失败: %w", err)
	}
	if result.Code != 0 {
		message := strings.TrimSpace(result.Message)
		if message == "" {
			message = strings.TrimSpace(result.Msg)
		}
		if result.Code == 42603 {
			if width > 0 && height > 0 {
				return "", fmt.Errorf("上传直播封面失败（code=%d）：B 站未接受处理后的图片尺寸（当前 %d×%d）", result.Code, width, height)
			}
			return "", fmt.Errorf("上传直播封面失败（code=%d）：B 站未接受处理后的图片尺寸", result.Code)
		}
		return "", fmt.Errorf("上传直播封面失败（code=%d）: %s", result.Code, responseMessage(message, ""))
	}
	coverURL := strings.TrimSpace(result.Data.URL)
	if coverURL == "" {
		coverURL = strings.TrimSpace(result.Data.Location)
	}
	if coverURL == "" {
		coverURL = strings.TrimSpace(result.Data.Link)
	}
	if coverURL == "" {
		return "", fmt.Errorf("直播封面上传接口未返回图片地址")
	}
	return coverURL, nil
}

func normalizeCoverForUpload(ctx context.Context, file *os.File) ([]byte, string, int, int, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, "", 0, 0, err
	}
	config, _, err := image.DecodeConfig(file)
	if err != nil {
		return nil, "", 0, 0, fmt.Errorf("请提供有效的 JPG、PNG 或 WebP 图片")
	}
	if config.Width <= 0 || config.Height <= 0 || config.Width > maxRoomCoverSourceDimension || config.Height > maxRoomCoverSourceDimension {
		return nil, "", 0, 0, fmt.Errorf("图片尺寸超出处理范围（最大 %d×%d）", maxRoomCoverSourceDimension, maxRoomCoverSourceDimension)
	}
	if int64(config.Width)*int64(config.Height) > maxRoomCoverSourcePixels {
		return nil, "", 0, 0, fmt.Errorf("图片像素数量超出处理范围")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, "", 0, 0, err
	}
	src, err := fennec.OpenAndOrient(file.Name())
	if err != nil {
		return nil, "", 0, 0, fmt.Errorf("请提供有效的 JPG、PNG 或 WebP 图片")
	}
	bounds := src.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if width <= 0 || height <= 0 {
		return nil, "", 0, 0, fmt.Errorf("图片尺寸无效")
	}
	maxSide := width
	if height > maxSide {
		maxSide = height
	}
	scale := 1.0
	if width < minRoomCoverProcessingWidth {
		scale = float64(minRoomCoverProcessingWidth) / float64(width)
	}
	if float64(maxSide)*scale > maxRoomCoverDimension {
		scale = float64(maxRoomCoverDimension) / float64(maxSide)
		if float64(width)*scale < minRoomCoverWidth {
			return nil, "", 0, 0, fmt.Errorf("图片尺寸过于狭长，无法满足封面宽度要求")
		}
	}
	dw, dh := int(float64(width)*scale), int(float64(height)*scale)
	if dw < 1 {
		dw = 1
	}
	if dh < 1 {
		dh = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	xdraw.Draw(dst, dst.Bounds(), image.NewUniform(color.White), image.Point{}, xdraw.Src)
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), xdraw.Over, nil)
	return compressCoverWithinLimit(ctx, dst, maxRoomCoverBytes)
}

func compressCoverWithinLimit(ctx context.Context, src image.Image, limit int64) ([]byte, string, int, int, error) {
	opts := fennec.DefaultOptions()
	opts.Format = fennec.JPEG
	opts.TargetSize = int(limit)
	opts.MaxWidth = maxRoomCoverDimension
	opts.MaxHeight = maxRoomCoverDimension
	opts.AutoOrient = false
	result, err := fennec.CompressImage(ctx, src, opts)
	if err != nil {
		return nil, "", 0, 0, err
	}
	data := result.Bytes()
	if int64(len(data)) > limit {
		return nil, "", 0, 0, fmt.Errorf("图片处理结果仍超过 %d MB", limit/(1024*1024))
	}
	width, height := result.FinalDimensions.X, result.FinalDimensions.Y
	if width < minRoomCoverWidth || height < 1 {
		return nil, "", 0, 0, fmt.Errorf("图片压缩后的尺寸无效（当前 %d×%d）", width, height)
	}
	return data, "image/jpeg", width, height, nil
}

// UploadRoomCoverURL 下载远程图片，再通过与本地文件相同的 B 站接口上传。
// 远程地址只作为图片来源，最终提交给直播间的是 B 站返回的图片地址。
func (c *Client) UploadRoomCoverURL(ctx context.Context, roomID, sessdata, biliJCT, imageURL string) (string, error) {
	parsed, err := url.ParseRequestURI(strings.TrimSpace(imageURL))
	if err != nil || (!strings.EqualFold(parsed.Scheme, "http") && !strings.EqualFold(parsed.Scheme, "https")) || parsed.Host == "" {
		return "", fmt.Errorf("直播封面 URL 无效")
	}
	downloadHTTPClient := &http.Client{
		Transport:     c.HTTPClient.Transport,
		CheckRedirect: c.HTTPClient.CheckRedirect,
		Jar:           c.HTTPClient.Jar,
		Timeout:       30 * time.Second,
	}
	downloadClient := resty.NewWithClient(downloadHTTPClient).
		SetRetryCount(2).
		SetRetryWaitTime(200 * time.Millisecond).
		SetRetryMaxWaitTime(time.Second).
		SetResponseBodyLimit(int(maxRoomCoverSourceBytes))
	resp, err := downloadClient.R().
		SetContext(ctx).
		SetHeaders(map[string]string{
			"Accept":     "image/jpeg,image/png,image/webp;q=0.9,*/*;q=0.1",
			"User-Agent": biliBrowserUserAgent,
		}).
		Get(parsed.String())
	if err != nil {
		if errors.Is(err, resty.ErrResponseBodyTooLarge) {
			return "", fmt.Errorf("直播封面源文件不能超过 64 MB")
		}
		return "", fmt.Errorf("下载直播封面失败: %w", err)
	}
	if !resp.IsSuccess() {
		return "", fmt.Errorf("下载直播封面失败：远程服务器返回 HTTP %d", resp.StatusCode())
	}
	data := resp.Body()
	ext, err := remoteCoverExtension(data, resp.Header().Get("Content-Type"))
	if err != nil {
		return "", err
	}
	temp, err := os.CreateTemp("", "bili-live-cover-*"+ext)
	if err != nil {
		return "", fmt.Errorf("准备远程直播封面失败: %w", err)
	}
	path := temp.Name()
	defer os.Remove(path)
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return "", fmt.Errorf("保存远程直播封面失败: %w", err)
	}
	if err := temp.Close(); err != nil {
		return "", fmt.Errorf("保存远程直播封面失败: %w", err)
	}
	return c.UploadRoomCover(ctx, roomID, sessdata, biliJCT, path)
}

func remoteCoverExtension(data []byte, declaredContentType string) (string, error) {
	detectedMIME := mimetype.Detect(data)
	detectedContentType := strings.ToLower(strings.TrimSpace(strings.Split(detectedMIME.String(), ";")[0]))
	if !isSupportedCoverMIME(detectedMIME) {
		return "", remoteCoverTypeError(declaredContentType, detectedContentType, false)
	}
	_, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return "", remoteCoverTypeError(declaredContentType, detectedContentType, true)
	}
	switch format {
	case "jpeg":
		return ".jpg", nil
	case "png":
		return ".png", nil
	case "webp":
		return ".webp", nil
	default:
		return "", fmt.Errorf("远程直播封面格式不受支持：%s", format)
	}
}

func isSupportedCoverMIME(detectedMIME *mimetype.MIME) bool {
	return detectedMIME.Is("image/jpeg") || detectedMIME.Is("image/png") || detectedMIME.Is("image/webp")
}

func remoteCoverTypeError(declaredContentType, detectedContentType string, damaged bool) error {
	declaredContentType = strings.TrimSpace(strings.Split(declaredContentType, ";")[0])
	if declaredContentType == "" {
		declaredContentType = "未提供"
	}
	if damaged {
		return fmt.Errorf("远程图片数据不完整或已损坏（响应类型：%s，检测类型：%s）", declaredContentType, detectedContentType)
	}
	return fmt.Errorf("远程服务器返回的不是 JPG、PNG 或 WebP 图片（响应类型：%s，检测类型：%s）", declaredContentType, detectedContentType)
}

type flexibleID string

type flexibleInt64 int64

func (value *flexibleInt64) UnmarshalJSON(data []byte) error {
	if string(data) == "null" || len(data) == 0 {
		*value = 0
		return nil
	}
	if data[0] == '"' {
		var text string
		if err := json.Unmarshal(data, &text); err != nil {
			return err
		}
		parsed, err := strconv.ParseInt(strings.TrimSpace(text), 10, 64)
		if err != nil {
			return err
		}
		*value = flexibleInt64(parsed)
		return nil
	}
	var number json.Number
	if err := json.Unmarshal(data, &number); err != nil {
		return err
	}
	parsed, err := strconv.ParseInt(number.String(), 10, 64)
	if err != nil {
		return err
	}
	*value = flexibleInt64(parsed)
	return nil
}

func (id *flexibleID) UnmarshalJSON(data []byte) error {
	if len(data) > 0 && data[0] == '"' {
		var value string
		if err := json.Unmarshal(data, &value); err != nil {
			return err
		}
		*id = flexibleID(value)
		return nil
	}
	var value json.Number
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*id = flexibleID(value.String())
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
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &Client{BaseURL: DefaultBaseURL, HTTPClient: httpClient}
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
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		message := strings.TrimSpace(string(body))
		if message != "" {
			return fmt.Errorf("b 站接口返回 HTTP %d：%s", resp.StatusCode, message)
		}
		return fmt.Errorf("b 站接口返回 HTTP %d", resp.StatusCode)
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
	req.Header.Set("Cookie", "SESSDATA="+sessdata)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", biliBrowserUserAgent)
	req.Header.Set("Referer", "https://live.bilibili.com/")
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
		return "", fmt.Errorf("请求失败: %s", result.Message)
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
		return nil, fmt.Errorf("获取分区列表失败: %s", raw.Message)
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
		message := strings.TrimSpace(result.Message)
		if message == "" {
			message = strings.TrimSpace(result.Msg)
		}
		return "", fmt.Errorf("新增直播标签失败: %s", responseMessage(message, ""))
	}
	tagID := strings.TrimSpace(string(result.Data.TagID))
	if tagID == "" {
		tagID = strings.TrimSpace(string(result.Data.ID))
	}
	if tagID == "" {
		// 部分版本的接口成功时不返回编号；标签已经写入，后续删除时无法用本地映射定位。
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
		message := strings.TrimSpace(result.Message)
		if message == "" {
			message = strings.TrimSpace(result.Msg)
		}
		return fmt.Errorf("删除直播标签失败: %s", responseMessage(message, ""))
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
		message := strings.TrimSpace(result.Message)
		if message == "" {
			message = strings.TrimSpace(result.Msg)
		}
		return fmt.Errorf("设置直播公告失败: %s", responseMessage(message, ""))
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
		message := strings.TrimSpace(result.Message)
		if message == "" {
			message = strings.TrimSpace(result.Msg)
		}
		return fmt.Errorf("更新直播封面失败: %s", responseMessage(message, ""))
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
		message := strings.TrimSpace(result.Message)
		if message == "" {
			message = strings.TrimSpace(result.Msg)
		}
		if result.Code == 1 && strings.Contains(message, "分区") {
			return fmt.Errorf("设置开播信息失败: %s；请确认分区 ID 是当前有效的子分区，并检查登录凭证是否仍有效", responseMessage(message, ""))
		}
		return fmt.Errorf("设置开播信息失败: %s", responseMessage(message, ""))
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
		return "", "", fmt.Errorf("开播失败（错误码 %d）: %s", result.Code, responseMessage(result.Message, result.Msg))
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
		return fmt.Errorf("下播失败（错误码 %d）: %s", result.Code, responseMessage(result.Message, result.Msg))
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
