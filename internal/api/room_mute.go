package api

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// 禁言列表、禁言和解除禁言使用同一组 Web 接口。
// 分页约定独立于黑名单接口，参见 docs/upstream-contracts.md。
type RoomMutedUser struct {
	UserID           string
	Username         string
	OperatorName     string
	ExpiresAt        string
	OperatorIsAnchor bool
}

type RoomMutedUserPage struct {
	Items      []RoomMutedUser
	Page       int
	Total      int
	TotalPages int
}

func (c *Client) GetMutedRoomUsers(ctx context.Context, roomID string, page int, sessdata, biliJCT string) (RoomMutedUserPage, error) {
	if err := validateLiveUserID("房间号", roomID); err != nil {
		return RoomMutedUserPage{}, err
	}
	if page <= 0 {
		page = 1
	}
	var raw struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Msg     string `json:"msg"`
		Data    struct {
			Items []struct {
				UID              flexibleID   `json:"tuid"`
				Username         string       `json:"tname"`
				OperatorName     string       `json:"name"`
				ExpiresAt        string       `json:"block_end_time"`
				OperatorIsAnchor flexibleBool `json:"is_anchor"`
			} `json:"data"`
			Total      flexibleInt64 `json:"total"`
			TotalPages flexibleInt64 `json:"total_page"`
		} `json:"data"`
	}
	// 此接口与黑名单接口不同：官方直播网页用 ps 传页码，不传 pn。
	// 不能套用其他接口的 pn/ps 分页约定，否则第一页会被请求成第 20 页。
	params := url.Values{"room_id": {roomID}, "ps": {strconv.Itoa(page)}}
	if err := c.postRoomManagementJSON(ctx, "GetMutedUsers", sessdata, biliJCT, params, &raw); err != nil {
		return RoomMutedUserPage{}, fmt.Errorf("获取禁言名单失败: %w", err)
	}
	if raw.Code != 0 {
		return RoomMutedUserPage{}, fmt.Errorf("获取禁言名单失败（错误码 %d）：%s", raw.Code, responseMessage(raw.Message, raw.Msg))
	}
	result := RoomMutedUserPage{
		Page:       page,
		Total:      int(raw.Data.Total),
		TotalPages: int(raw.Data.TotalPages),
		Items:      make([]RoomMutedUser, 0, len(raw.Data.Items)),
	}
	for _, item := range raw.Data.Items {
		result.Items = append(result.Items, RoomMutedUser{UserID: string(item.UID), Username: strings.TrimSpace(item.Username), OperatorName: strings.TrimSpace(item.OperatorName), ExpiresAt: strings.TrimSpace(item.ExpiresAt), OperatorIsAnchor: bool(item.OperatorIsAnchor)})
	}
	return result, nil
}

type RoomUserMuteDuration string

const (
	RoomMutePermanent RoomUserMuteDuration = "permanent"
	RoomMuteSevenDays RoomUserMuteDuration = "seven-days"
	RoomMuteOneDay    RoomUserMuteDuration = "one-day"
	RoomMuteFourHours RoomUserMuteDuration = "four-hours"
	RoomMuteTwoHours  RoomUserMuteDuration = "two-hours"
	RoomMuteThisLive  RoomUserMuteDuration = "this-live"
)

func (duration RoomUserMuteDuration) parameters() (muteType, hours int, ok bool) {
	switch duration {
	case RoomMutePermanent:
		return 1, -1, true
	case RoomMuteSevenDays:
		return 1, 168, true
	case RoomMuteOneDay:
		return 1, 24, true
	case RoomMuteFourHours:
		return 1, 4, true
	case RoomMuteTwoHours:
		return 1, 2, true
	case RoomMuteThisLive:
		return 2, 0, true
	default:
		return 0, 0, false
	}
}

func (c *Client) MuteRoomUser(ctx context.Context, roomID, userID, message string, duration RoomUserMuteDuration, sessdata, biliJCT string) error {
	if err := validateLiveUserID("房间号", roomID); err != nil {
		return err
	}
	if err := validateLiveUserID("用户 UID", userID); err != nil {
		return err
	}
	muteType, hours, ok := duration.parameters()
	if !ok {
		return fmt.Errorf("无效的禁言时长：%s", duration)
	}
	params := url.Values{"room_id": {roomID}, "tuid": {userID}, "msg": {strings.TrimSpace(message)}, "mobile_app": {"web"}, "type": {strconv.Itoa(muteType)}, "hour": {strconv.Itoa(hours)}}
	return c.roomManagementAction(ctx, "MuteRoomUser", "禁言用户", sessdata, biliJCT, params)
}

func (c *Client) UnmuteRoomUser(ctx context.Context, roomID, userID, sessdata, biliJCT string) error {
	if err := validateLiveUserID("房间号", roomID); err != nil {
		return err
	}
	if err := validateLiveUserID("用户 UID", userID); err != nil {
		return err
	}
	return c.roomManagementAction(ctx, "UnmuteRoomUser", "解除用户禁言", sessdata, biliJCT, url.Values{"room_id": {roomID}, "tuid": {userID}, "mobi_app": {"web"}})
}
