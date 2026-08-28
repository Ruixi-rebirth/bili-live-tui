package api

import (
	"net/http"
	"sort"
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
	"GetDanmakuInfo":        {Name: "GetDanmakuInfo", Method: http.MethodGet, Path: "/xlive/web-room/v1/index/getDanmuInfo", Description: "获取直播间弹幕 websocket 连接信息"},
	"GetDanmakuInfoLegacy":  {Name: "GetDanmakuInfoLegacy", Method: http.MethodGet, Path: "/room/v1/Danmu/getConf", Description: "获取直播间弹幕连接信息（备用接口）"},
	"GetWebNav":             {Name: "GetWebNav", Method: http.MethodGet, Path: "https://api.bilibili.com/x/web-interface/nav", Description: "获取当前网页登录用户 UID"},
	"GetBuvid":              {Name: "GetBuvid", Method: http.MethodGet, Path: "https://api.bilibili.com/x/frontend/finger/spi", Description: "获取网页设备标识 buvid3"},
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

func ListEndpoints() []Endpoint {
	result := make([]Endpoint, 0, len(Endpoints))
	for _, endpoint := range Endpoints {
		result = append(result, endpoint)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}
