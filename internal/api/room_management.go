package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

const (
	// 这些数值来自 B 站当前直播网页客户端的权限常量（模块 80972）。
	// 接口返回整数权限列表，因此这里保留数值定义。
	RoomPermissionMute           = 1
	RoomPermissionBlacklist      = 2
	RoomPermissionAnonymousView  = 100
	RoomPermissionAnonymousMute  = 101
	RoomPermissionAnonymousBlock = 102
)

// RoomManagementCapabilities 描述当前登录账号在指定直播间的管理权限。
// 权限数字来自 getInfoByUser，不能仅凭房管名称推断。
type RoomManagementCapabilities struct {
	UserID      string
	AnchorID    string
	IsAnchor    bool
	IsAdmin     bool
	AdminLevel  int
	Permissions []int
}

func (capabilities RoomManagementCapabilities) HasPermission(permission int) bool {
	for _, candidate := range capabilities.Permissions {
		if candidate == permission {
			return true
		}
	}
	return false
}

func (capabilities RoomManagementCapabilities) CanMute(targetAdminLevel int) bool {
	return capabilities.CanMuteUser(targetAdminLevel, true)
}

// CanMuteUser 判断是否可对特定用户执行禁言。普通房管必须已从上游获知
// 对方的房管等级，主播不受这项层级判断限制。
func (capabilities RoomManagementCapabilities) CanMuteUser(targetAdminLevel int, targetAdminLevelKnown bool) bool {
	return capabilities.IsAnchor || targetAdminLevelKnown && capabilities.IsAdmin &&
		capabilities.HasPermission(RoomPermissionMute) &&
		capabilities.AdminLevel > targetAdminLevel
}

func (capabilities RoomManagementCapabilities) CanBlacklist(targetAdminLevel int) bool {
	return capabilities.CanBlacklistUser(targetAdminLevel, true)
}

// CanBlacklistUser 判断是否可将特定用户加入黑名单。
func (capabilities RoomManagementCapabilities) CanBlacklistUser(targetAdminLevel int, targetAdminLevelKnown bool) bool {
	return capabilities.IsAnchor || targetAdminLevelKnown && capabilities.IsAdmin &&
		capabilities.HasPermission(RoomPermissionBlacklist) &&
		capabilities.AdminLevel > targetAdminLevel
}

type roomManagementCapabilitiesWire struct {
	UID         flexibleID      `json:"uid"`
	IsAnchor    flexibleBool    `json:"is_anchor"`
	IsAdmin     flexibleBool    `json:"is_admin"`
	AdminLevel  flexibleInt64   `json:"admin_level"`
	Permissions []flexibleInt64 `json:"permissions"`
	Info        struct {
		UID flexibleID `json:"uid"`
	} `json:"info"`
	Badge struct {
		IsRoomAdmin flexibleBool    `json:"is_room_admin"`
		AdminLevel  flexibleInt64   `json:"admin_level"`
		Permissions []flexibleInt64 `json:"permissions"`
	} `json:"badge"`
}

// GetRoomManagementCapabilities 获取当前账号的主播/房管等级和细粒度权限。
func (c *Client) GetRoomManagementCapabilities(ctx context.Context, roomID, sessdata, biliJCT string) (RoomManagementCapabilities, error) {
	roomID = strings.TrimSpace(roomID)
	if roomID == "" {
		return RoomManagementCapabilities{}, fmt.Errorf("获取房间管理权限需要房间号")
	}
	path, err := c.endpointByName("GetDanmakuUserInfo")
	if err != nil {
		return RoomManagementCapabilities{}, err
	}
	parsed, err := url.Parse(path)
	if err != nil {
		return RoomManagementCapabilities{}, fmt.Errorf("准备获取房间管理权限失败: %w", err)
	}
	query := parsed.Query()
	query.Set("room_id", roomID)
	query.Set("from", "0")
	parsed.RawQuery = query.Encode()
	var raw struct {
		Code    int                            `json:"code"`
		Message string                         `json:"message"`
		Msg     string                         `json:"msg"`
		Data    roomManagementCapabilitiesWire `json:"data"`
	}
	if err := c.getLiveCookieJSON(ctx, parsed.String(), sessdata, biliJCT, &raw); err != nil {
		return RoomManagementCapabilities{}, fmt.Errorf("获取房间管理权限失败: %w", err)
	}
	if raw.Code != 0 {
		return RoomManagementCapabilities{}, fmt.Errorf("获取房间管理权限失败（错误码 %d）：%s", raw.Code, responseMessage(raw.Message, raw.Msg))
	}
	capabilities := RoomManagementCapabilities{
		UserID:     strings.TrimSpace(string(raw.Data.UID)),
		IsAnchor:   bool(raw.Data.IsAnchor),
		IsAdmin:    bool(raw.Data.IsAdmin),
		AdminLevel: int(raw.Data.AdminLevel),
	}
	if capabilities.UserID == "" {
		capabilities.UserID = strings.TrimSpace(string(raw.Data.Info.UID))
	}
	if !capabilities.IsAdmin {
		capabilities.IsAdmin = bool(raw.Data.Badge.IsRoomAdmin)
	}
	if capabilities.AdminLevel == 0 {
		capabilities.AdminLevel = int(raw.Data.Badge.AdminLevel)
	}
	permissions := raw.Data.Permissions
	if len(permissions) == 0 {
		permissions = raw.Data.Badge.Permissions
	}
	for _, permission := range permissions {
		capabilities.Permissions = append(capabilities.Permissions, int(permission))
	}
	if capabilities.UserID == "" {
		if identity, identityErr := c.resolveDanmakuIdentity(ctx, sessdata, biliJCT); identityErr == nil && identity.UID > 0 {
			capabilities.UserID = strconv.FormatInt(identity.UID, 10)
		}
	}
	if !capabilities.IsAnchor && capabilities.UserID != "" {
		if snapshot, snapshotErr := c.GetRoomSnapshot(ctx, roomID); snapshotErr == nil {
			capabilities.AnchorID = snapshot.AnchorID
			capabilities.IsAnchor = capabilities.AnchorID != "" && capabilities.AnchorID == capabilities.UserID
		}
	}
	if capabilities.IsAnchor && capabilities.AnchorID == "" {
		capabilities.AnchorID = capabilities.UserID
	}
	if capabilities.IsAnchor {
		capabilities.IsAdmin = true
	}
	return capabilities, nil
}

type RoomAdmin struct {
	UserID      string
	Username    string
	AppointedAt string
	Level       int
	LevelKnown  bool
}

type RoomAdminPage struct {
	Items      []RoomAdmin
	Page       int
	TotalPages int
	MaxCount   int
}

func (c *Client) GetRoomAdminSeniorStatus(ctx context.Context, anchorID, sessdata, biliJCT string) (int, error) {
	if err := validateLiveUserID("主播 UID", anchorID); err != nil {
		return 0, err
	}
	var raw struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Msg     string `json:"msg"`
		Data    struct {
			Status flexibleInt64 `json:"status"`
		} `json:"data"`
	}
	if err := c.getLiveEndpointJSON(ctx, "GetRoomAdminSenior", sessdata, biliJCT, url.Values{"anchor_id": {anchorID}}, &raw); err != nil {
		return 0, fmt.Errorf("获取高级房管状态失败: %w", err)
	}
	if raw.Code != 0 {
		return 0, fmt.Errorf("获取高级房管状态失败（错误码 %d）：%s", raw.Code, responseMessage(raw.Message, raw.Msg))
	}
	return int(raw.Data.Status), nil
}

func (c *Client) GetRoomAdmins(ctx context.Context, page int, sessdata, biliJCT string) (RoomAdminPage, error) {
	if page <= 0 {
		page = 1
	}
	var raw struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Msg     string `json:"msg"`
		Data    struct {
			Items []struct {
				UID        flexibleID     `json:"uid"`
				Username   string         `json:"uname"`
				CreatedAt  string         `json:"ctime"`
				AdminLevel *flexibleInt64 `json:"admin_level"`
			} `json:"data"`
			Page struct {
				Number     flexibleInt64 `json:"page"`
				TotalPages flexibleInt64 `json:"total_page"`
			} `json:"page"`
			MaxCount flexibleInt64 `json:"max_room_anchors_number"`
		} `json:"data"`
	}
	if err := c.getLiveEndpointJSON(ctx, "GetRoomAdmins", sessdata, biliJCT, url.Values{"page": {strconv.Itoa(page)}}, &raw); err != nil {
		return RoomAdminPage{}, fmt.Errorf("获取房管列表失败: %w", err)
	}
	if raw.Code != 0 {
		return RoomAdminPage{}, fmt.Errorf("获取房管列表失败（错误码 %d）：%s", raw.Code, responseMessage(raw.Message, raw.Msg))
	}
	result := RoomAdminPage{Page: int(raw.Data.Page.Number), TotalPages: int(raw.Data.Page.TotalPages), MaxCount: int(raw.Data.MaxCount)}
	if result.Page <= 0 {
		result.Page = page
	}
	for _, item := range raw.Data.Items {
		admin := RoomAdmin{UserID: string(item.UID), Username: strings.TrimSpace(item.Username), AppointedAt: strings.TrimSpace(item.CreatedAt)}
		if item.AdminLevel != nil {
			admin.Level = int(*item.AdminLevel)
			admin.LevelKnown = true
		}
		result.Items = append(result.Items, admin)
	}
	return result, nil
}

func (c *Client) AppointRoomAdmin(ctx context.Context, userID string, level int, sessdata, biliJCT string) error {
	if strings.TrimSpace(userID) == "" {
		return fmt.Errorf("任命房管需要 UID 或用户名")
	}
	if level != 1 && level != 2 {
		return fmt.Errorf("无效的房管等级：%d", level)
	}
	return c.roomManagementAction(ctx, "AppointRoomAdmin", "任命房管", sessdata, biliJCT, url.Values{"admin": {strings.TrimSpace(userID)}, "admin_level": {strconv.Itoa(level)}})
}

func (c *Client) DismissRoomAdmin(ctx context.Context, userID, sessdata, biliJCT string) error {
	if err := validateLiveUserID("用户 UID", userID); err != nil {
		return err
	}
	return c.roomManagementAction(ctx, "DismissRoomAdmin", "撤销房管", sessdata, biliJCT, url.Values{"uid": {userID}})
}

type RoomUserSearchResult struct {
	UserID          string
	Username        string
	AdminLevel      int
	AdminLevelKnown bool
}

func (c *Client) SearchRoomUsers(ctx context.Context, keyword, sessdata, biliJCT string) ([]RoomUserSearchResult, error) {
	keyword = strings.TrimSpace(keyword)
	if keyword == "" {
		return nil, fmt.Errorf("搜索用户需要 UID 或用户名")
	}
	var raw struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Msg     string          `json:"msg"`
		Data    json.RawMessage `json:"data"`
	}
	if err := c.getLiveEndpointJSON(ctx, "SearchRoomUser", sessdata, biliJCT, url.Values{"search": {keyword}}, &raw); err != nil {
		return nil, fmt.Errorf("搜索直播用户失败: %w", err)
	}
	if raw.Code != 0 {
		return nil, fmt.Errorf("搜索直播用户失败（错误码 %d）：%s", raw.Code, responseMessage(raw.Message, raw.Msg))
	}
	type searchItem struct {
		UID        flexibleID     `json:"uid"`
		TargetUID  flexibleID     `json:"tuid"`
		Username   string         `json:"uname"`
		TargetName string         `json:"tname"`
		Name       string         `json:"name"`
		AdminLevel *flexibleInt64 `json:"admin_level"`
	}
	var items []searchItem
	if len(raw.Data) > 0 && string(raw.Data) != "null" {
		if err := json.Unmarshal(raw.Data, &items); err != nil {
			var container struct {
				Items  []searchItem `json:"items"`
				Data   []searchItem `json:"data"`
				Result []searchItem `json:"result"`
				List   []searchItem `json:"list"`
			}
			if containerErr := json.Unmarshal(raw.Data, &container); containerErr != nil {
				return nil, fmt.Errorf("解析搜索直播用户结果失败: %w", err)
			}
			switch {
			case len(container.Items) > 0:
				items = container.Items
			case len(container.Data) > 0:
				items = container.Data
			case len(container.Result) > 0:
				items = container.Result
			default:
				items = container.List
			}
		}
	}
	result := make([]RoomUserSearchResult, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		userID := strings.TrimSpace(string(item.UID))
		if userID == "" {
			userID = strings.TrimSpace(string(item.TargetUID))
		}
		if userID == "" {
			continue
		}
		if err := validateLiveUserID("用户 UID", userID); err != nil {
			continue
		}
		if _, exists := seen[userID]; exists {
			continue
		}
		seen[userID] = struct{}{}
		username := strings.TrimSpace(item.Username)
		if username == "" {
			username = strings.TrimSpace(item.TargetName)
		}
		if username == "" {
			username = strings.TrimSpace(item.Name)
		}
		entry := RoomUserSearchResult{UserID: userID, Username: username}
		if item.AdminLevel != nil {
			entry.AdminLevel = int(*item.AdminLevel)
			entry.AdminLevelKnown = true
		}
		result = append(result, entry)
	}
	return result, nil
}

type RoomBlacklistedUser struct {
	UserID    string
	Username  string
	CreatedAt string
}

type RoomBlacklistPage struct {
	Items      []RoomBlacklistedUser
	Page       int
	TotalPages int
	Total      int
}

func (c *Client) GetRoomBlacklist(ctx context.Context, anchorID string, page, pageSize int, sessdata, biliJCT string) (RoomBlacklistPage, error) {
	if err := validateLiveUserID("主播 UID", anchorID); err != nil {
		return RoomBlacklistPage{}, err
	}
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 20
	}
	var raw struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Msg     string `json:"msg"`
		Data    struct {
			Items []struct {
				UID        flexibleID `json:"uid"`
				TargetUID  flexibleID `json:"tuid"`
				Username   string     `json:"uname"`
				TargetName string     `json:"tname"`
				Name       string     `json:"name"`
				CreatedAt  string     `json:"ctime"`
				ModifiedAt string     `json:"mtime"`
			} `json:"data"`
			Page       flexibleInt64 `json:"pn"`
			TotalPages flexibleInt64 `json:"total_page"`
			Total      flexibleInt64 `json:"total"`
		} `json:"data"`
	}
	query := url.Values{
		"anchor_id": {anchorID},
		"pn":        {strconv.Itoa(page)},
		"ps":        {strconv.Itoa(pageSize)},
	}
	if err := c.getLiveEndpointJSON(ctx, "GetRoomBlacklist", sessdata, biliJCT, query, &raw); err != nil {
		return RoomBlacklistPage{}, fmt.Errorf("获取直播间黑名单失败: %w", err)
	}
	if raw.Code != 0 {
		return RoomBlacklistPage{}, fmt.Errorf("获取直播间黑名单失败（错误码 %d）：%s", raw.Code, responseMessage(raw.Message, raw.Msg))
	}
	result := RoomBlacklistPage{Page: int(raw.Data.Page), TotalPages: int(raw.Data.TotalPages), Total: int(raw.Data.Total)}
	if result.Page <= 0 {
		result.Page = page
	}
	for _, item := range raw.Data.Items {
		userID := strings.TrimSpace(string(item.UID))
		if userID == "" {
			userID = strings.TrimSpace(string(item.TargetUID))
		}
		username := strings.TrimSpace(item.Username)
		if username == "" {
			username = strings.TrimSpace(item.TargetName)
		}
		if username == "" {
			username = strings.TrimSpace(item.Name)
		}
		createdAt := strings.TrimSpace(item.CreatedAt)
		if createdAt == "" {
			createdAt = strings.TrimSpace(item.ModifiedAt)
		}
		result.Items = append(result.Items, RoomBlacklistedUser{UserID: userID, Username: username, CreatedAt: createdAt})
	}
	if result.Total == 0 {
		result.Total = len(result.Items)
	}
	return result, nil
}

func (c *Client) BlacklistRoomUser(ctx context.Context, anchorID, userID, sessdata, biliJCT string) error {
	if err := validateLiveUserID("主播 UID", anchorID); err != nil {
		return err
	}
	if err := validateLiveUserID("用户 UID", userID); err != nil {
		return err
	}
	return c.roomManagementAction(ctx, "BlacklistRoomUser", "将用户添加到直播间黑名单", sessdata, biliJCT, url.Values{
		"anchor_id": {anchorID},
		"tuid":      {userID},
		"spmid":     {"444.8.0.0"},
	})
}

func (c *Client) UnblacklistRoomUser(ctx context.Context, anchorID, userID, sessdata, biliJCT string) error {
	if err := validateLiveUserID("主播 UID", anchorID); err != nil {
		return err
	}
	if err := validateLiveUserID("用户 UID", userID); err != nil {
		return err
	}
	return c.roomManagementAction(ctx, "UnblacklistRoomUser", "将用户移出直播间黑名单", sessdata, biliJCT, url.Values{
		"anchor_id": {anchorID},
		"tuid":      {userID},
		"spmid":     {"444.8.0.0"},
	})
}

type RoomShieldKeywordState struct {
	Keywords []string
	MaxCount int
}

func (c *Client) GetRoomShieldKeywords(ctx context.Context, roomID, sessdata, biliJCT string) (RoomShieldKeywordState, error) {
	if err := validateLiveUserID("房间号", roomID); err != nil {
		return RoomShieldKeywordState{}, err
	}
	var raw struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Msg     string `json:"msg"`
		Data    struct {
			KeywordList []struct {
				Keyword string `json:"keyword"`
			} `json:"keyword_list"`
			MaxCount flexibleInt64 `json:"max_limit"`
		} `json:"data"`
	}
	if err := c.postRoomManagementJSON(ctx, "GetShieldKeywords", sessdata, biliJCT, url.Values{"room_id": {roomID}}, &raw); err != nil {
		return RoomShieldKeywordState{}, fmt.Errorf("获取弹幕观看屏蔽词失败: %w", err)
	}
	if raw.Code != 0 {
		return RoomShieldKeywordState{}, fmt.Errorf("获取弹幕观看屏蔽词失败（错误码 %d）：%s", raw.Code, responseMessage(raw.Message, raw.Msg))
	}
	result := RoomShieldKeywordState{MaxCount: int(raw.Data.MaxCount)}
	for _, item := range raw.Data.KeywordList {
		if keyword := strings.TrimSpace(item.Keyword); keyword != "" {
			result.Keywords = append(result.Keywords, keyword)
		}
	}
	return result, nil
}

func validateRoomShieldKeyword(keyword string) (string, error) {
	keyword = strings.TrimSpace(keyword)
	if keyword == "" {
		return "", fmt.Errorf("屏蔽词不能为空")
	}
	return keyword, nil
}

func (c *Client) AddRoomShieldKeyword(ctx context.Context, roomID, keyword, sessdata, biliJCT string) error {
	if err := validateLiveUserID("房间号", roomID); err != nil {
		return err
	}
	keyword, err := validateRoomShieldKeyword(keyword)
	if err != nil {
		return err
	}
	return c.roomManagementAction(ctx, "AddShieldKeyword", "添加弹幕观看屏蔽词", sessdata, biliJCT, url.Values{"room_id": {roomID}, "keyword": {keyword}})
}

func (c *Client) DeleteRoomShieldKeyword(ctx context.Context, roomID, keyword, sessdata, biliJCT string) error {
	if err := validateLiveUserID("房间号", roomID); err != nil {
		return err
	}
	keyword, err := validateRoomShieldKeyword(keyword)
	if err != nil {
		return err
	}
	return c.roomManagementAction(ctx, "DeleteShieldKeyword", "删除弹幕观看屏蔽词", sessdata, biliJCT, url.Values{"room_id": {roomID}, "keyword": {keyword}})
}

type RoomSilentState struct {
	Enabled          bool
	Audience         string
	Level            int
	DurationMinutes  int
	RemainingSeconds int
}

const (
	RoomSilentAll       = "all"
	RoomSilentNonFans   = "follow"
	RoomSilentWealth    = "wealth"
	RoomSilentMedal     = "medal"
	RoomSilentNonMember = "member"
	RoomSilentOff       = "off"
)

func (c *Client) GetRoomSilentState(ctx context.Context, roomID, sessdata, biliJCT string) (RoomSilentState, error) {
	if err := validateLiveUserID("房间号", roomID); err != nil {
		return RoomSilentState{}, err
	}
	var raw struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Msg     string `json:"msg"`
		Data    struct {
			Type   string        `json:"type"`
			Level  flexibleInt64 `json:"level"`
			Minute flexibleInt64 `json:"minute"`
			Second flexibleInt64 `json:"second"`
		} `json:"data"`
	}
	if err := c.getLiveEndpointJSON(ctx, "GetRoomSilent", sessdata, biliJCT, url.Values{"room_id": {roomID}}, &raw); err != nil {
		return RoomSilentState{}, fmt.Errorf("获取直播间全局禁言状态失败: %w", err)
	}
	if raw.Code != 0 {
		return RoomSilentState{}, fmt.Errorf("获取直播间全局禁言状态失败（错误码 %d）：%s", raw.Code, responseMessage(raw.Message, raw.Msg))
	}
	audience := strings.TrimSpace(raw.Data.Type)
	return RoomSilentState{
		Enabled:          audience != "" && audience != RoomSilentOff,
		Audience:         audience,
		Level:            int(raw.Data.Level),
		DurationMinutes:  int(raw.Data.Minute),
		RemainingSeconds: int(raw.Data.Second),
	}, nil
}

func validateRoomSilent(audience string, level, minutes int) error {
	switch audience {
	case RoomSilentOff, RoomSilentAll, RoomSilentNonFans, RoomSilentNonMember:
		if level != 1 {
			return fmt.Errorf("该全局禁言类型不使用等级")
		}
	case RoomSilentWealth:
		if level < 1 {
			return fmt.Errorf("财富等级必须是正整数")
		}
	case RoomSilentMedal:
		if level < 1 {
			return fmt.Errorf("粉丝牌等级必须是正整数")
		}
	default:
		return fmt.Errorf("无效的全局禁言类型：%s", audience)
	}
	if minutes < 0 {
		return fmt.Errorf("全局禁言时长不能为负数")
	}
	return nil
}

func (c *Client) SetRoomSilentState(ctx context.Context, roomID, audience string, level, minutes int, sessdata, biliJCT string) error {
	if err := validateLiveUserID("房间号", roomID); err != nil {
		return err
	}
	audience = strings.TrimSpace(audience)
	if err := validateRoomSilent(audience, level, minutes); err != nil {
		return err
	}
	return c.roomManagementAction(ctx, "SetRoomSilent", "设置直播间全局禁言", sessdata, biliJCT, url.Values{
		"room_id": {roomID},
		"type":    {audience},
		"level":   {strconv.Itoa(level)},
		"minute":  {strconv.Itoa(minutes)},
	})
}
