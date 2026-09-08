package api

import (
	"net/http"
)

type Endpoint struct {
	Name        string
	Method      string
	Path        string
	Description string
}

var Endpoints = map[string]Endpoint{
	"GetMyRoomID":           {Name: "GetMyRoomID", Method: http.MethodGet, Path: "/xlive/web-ucenter/user/live_info", Description: "获取当前账号的直播间"},
	"GetRoomSnapshot":       {Name: "GetRoomSnapshot", Method: http.MethodGet, Path: "/xlive/web-room/v1/index/getInfoByRoom", Description: "获取直播间实时状态和人气"},
	"GetRoomSnapshotLegacy": {Name: "GetRoomSnapshotLegacy", Method: http.MethodGet, Path: "/room/v1/Room/get_info", Description: "获取直播间基础状态（备用接口）"},
	"GetOnlineGoldRank":     {Name: "GetOnlineGoldRank", Method: http.MethodGet, Path: "/xlive/general-interface/v1/rank/getOnlineGoldRank", Description: "获取在线人数和在线高能榜"},
	"GetRoomPlaybackURL":    {Name: "GetRoomPlaybackURL", Method: http.MethodGet, Path: "/xlive/web-room/v2/index/getRoomPlayInfo", Description: "获取直播间回拉播放地址"},
	"GetDanmakuInfo":        {Name: "GetDanmakuInfo", Method: http.MethodGet, Path: "/xlive/web-room/v1/index/getDanmuInfo", Description: "获取直播间弹幕 websocket 连接信息"},
	"GetDanmakuInfoLegacy":  {Name: "GetDanmakuInfoLegacy", Method: http.MethodGet, Path: "/room/v1/Danmu/getConf", Description: "获取直播间弹幕连接信息（备用接口）"},
	"GetDanmakuUserInfo":    {Name: "GetDanmakuUserInfo", Method: http.MethodGet, Path: "/xlive/web-room/v1/index/getInfoByUser", Description: "获取当前账号在直播间的弹幕发送限制"},
	"GetWebNav":             {Name: "GetWebNav", Method: http.MethodGet, Path: "https://api.bilibili.com/x/web-interface/nav", Description: "获取当前网页登录用户 UID"},
	"GetBuvid":              {Name: "GetBuvid", Method: http.MethodGet, Path: "https://api.bilibili.com/x/frontend/finger/spi", Description: "获取网页设备标识 buvid3"},
	"GetUserCard":           {Name: "GetUserCard", Method: http.MethodGet, Path: "https://api.bilibili.com/x/web-interface/card", Description: "获取用户公开资料卡片"},
	"ModifyUserRelation":    {Name: "ModifyUserRelation", Method: http.MethodPost, Path: "https://api.bilibili.com/x/relation/modify", Description: "关注或取消关注用户"},
	"GetRoomAdminSenior":    {Name: "GetRoomAdminSenior", Method: http.MethodGet, Path: "/xlive/app-ucenter/v1/roomAdmin/senior_switch", Description: "获取高级房管功能状态"},
	"GetRoomAdmins":         {Name: "GetRoomAdmins", Method: http.MethodGet, Path: "/xlive/app-ucenter/v1/roomAdmin/get_by_anchor", Description: "获取房管列表"},
	"AppointRoomAdmin":      {Name: "AppointRoomAdmin", Method: http.MethodPost, Path: "/xlive/web-ucenter/v1/roomAdmin/appoint", Description: "任命或变更房管"},
	"DismissRoomAdmin":      {Name: "DismissRoomAdmin", Method: http.MethodPost, Path: "/xlive/app-ucenter/v1/roomAdmin/dismiss", Description: "撤销房管"},
	"SearchRoomUser":        {Name: "SearchRoomUser", Method: http.MethodGet, Path: "/banned_service/v2/Silent/search_user", Description: "按 UID 或用户名搜索直播用户"},
	"GetMutedUsers":         {Name: "GetMutedUsers", Method: http.MethodPost, Path: "/xlive/web-ucenter/v1/banned/GetSilentUserList", Description: "获取直播间禁言名单"},
	"MuteRoomUser":          {Name: "MuteRoomUser", Method: http.MethodPost, Path: "/xlive/web-ucenter/v1/banned/AddSilentUser", Description: "禁言直播间用户"},
	"UnmuteRoomUser":        {Name: "UnmuteRoomUser", Method: http.MethodPost, Path: "/xlive/web-ucenter/v1/banned/DelSilentUser", Description: "解除直播间用户禁言"},
	"GetRoomBlacklist":      {Name: "GetRoomBlacklist", Method: http.MethodGet, Path: "/xlive/app-ucenter/v2/xbanned/banned/GetBlackList", Description: "获取直播间黑名单"},
	"BlacklistRoomUser":     {Name: "BlacklistRoomUser", Method: http.MethodPost, Path: "/xlive/app-ucenter/v2/xbanned/banned/AddBlack", Description: "将用户添加到直播间黑名单"},
	"UnblacklistRoomUser":   {Name: "UnblacklistRoomUser", Method: http.MethodPost, Path: "/xlive/app-ucenter/v2/xbanned/banned/DelBlack", Description: "将用户移出直播间黑名单"},
	"GetShieldKeywords":     {Name: "GetShieldKeywords", Method: http.MethodPost, Path: "/xlive/web-ucenter/v1/banned/GetShieldKeywordList", Description: "获取直播间屏蔽词"},
	"AddShieldKeyword":      {Name: "AddShieldKeyword", Method: http.MethodPost, Path: "/xlive/web-ucenter/v1/banned/AddShieldKeyword", Description: "添加直播间屏蔽词"},
	"DeleteShieldKeyword":   {Name: "DeleteShieldKeyword", Method: http.MethodPost, Path: "/xlive/web-ucenter/v1/banned/DelShieldKeyword", Description: "删除直播间屏蔽词"},
	"GetRoomSilent":         {Name: "GetRoomSilent", Method: http.MethodGet, Path: "/xlive/web-room/v1/banned/GetRoomSilent", Description: "获取直播间全局禁言状态"},
	"SetRoomSilent":         {Name: "SetRoomSilent", Method: http.MethodPost, Path: "/xlive/web-room/v1/banned/RoomSilent", Description: "设置直播间全局禁言"},
	"SendDanmaku":           {Name: "SendDanmaku", Method: http.MethodPost, Path: "/msg/send", Description: "发送直播间弹幕"},
	"GetLiveAreas":          {Name: "GetLiveAreas", Method: http.MethodGet, Path: "/room/v1/Area/getList", Description: "获取直播分区列表"},
	"UploadRoomCover":       {Name: "UploadRoomCover", Method: http.MethodPost, Path: "https://api.bilibili.com/x/upload/web/image", Description: "上传直播封面并返回图片地址"},
	"AddLiveTag":            {Name: "AddLiveTag", Method: http.MethodPost, Path: "/xlive/app-blink/v1/liveTagService/AddLiveTag", Description: "新增直播标签"},
	"DeleteLiveTag":         {Name: "DeleteLiveTag", Method: http.MethodPost, Path: "/xlive/app-blink/v1/liveTagService/DeleteLiveTag", Description: "删除直播标签"},
	"UpdateRoomNews":        {Name: "UpdateRoomNews", Method: http.MethodPost, Path: "/xlive/app-blink/v1/index/updateRoomNews", Description: "更新直播间公告"},
	"UpdatePreLiveInfo":     {Name: "UpdatePreLiveInfo", Method: http.MethodPost, Path: "/xlive/app-blink/v1/preLive/UpdatePreLiveInfo", Description: "更新预开播资料"},
	"UpdateLiveInfo":        {Name: "UpdateLiveInfo", Method: http.MethodPost, Path: "/room/v1/Room/update", Description: "使用 SESSDATA/bili_jct 更新标题、简介和分区"},
	"StartLive":             {Name: "StartLive", Method: http.MethodPost, Path: "/room/v1/Room/startLive", Description: "开播并获取 RTMP 推流地址"},
	"StopLive":              {Name: "StopLive", Method: http.MethodPost, Path: "/room/v1/Room/stopLive", Description: "结束直播"},
	"GetTVQRCode":           {Name: "GetTVQRCode", Method: http.MethodPost, Path: "https://passport.bilibili.com/x/passport-tv-login/qrcode/auth_code", Description: "获取扫码登录二维码"},
	"CheckQRStatus":         {Name: "CheckQRStatus", Method: http.MethodPost, Path: "https://passport.bilibili.com/x/passport-tv-login/qrcode/poll", Description: "轮询扫码登录状态"},
}

func EndpointByName(name string) (Endpoint, bool) {
	endpoint, ok := Endpoints[name]
	return endpoint, ok
}
