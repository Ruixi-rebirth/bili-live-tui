package api

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
)

func TestLiveSettingsValidate(t *testing.T) {
	tests := []struct {
		name     string
		settings LiveSettings
		wantErr  bool
	}{
		{"valid", LiveSettings{Title: "测试直播", AreaID: "376"}, false},
		{"missing title", LiveSettings{AreaID: "376"}, true},
		{"missing area", LiveSettings{Title: "测试直播"}, true},
		{"non numeric area", LiveSettings{Title: "测试直播", AreaID: "game"}, true},
		{"zero area", LiveSettings{Title: "测试直播", AreaID: "0"}, true},
		{"negative area", LiveSettings{Title: "测试直播", AreaID: "-1"}, true},
		{"long text is delegated to Bilibili", LiveSettings{Title: strings.Repeat("标", 500), Description: strings.Repeat("简", 1000), Announcement: strings.Repeat("公", 1000), Tags: strings.Repeat("标", 1000), AreaID: "376"}, false},
		{"invalid orientation", LiveSettings{Title: "测试直播", AreaID: "376", Orientation: "diagonal"}, true},
		{"valid remote OBS host", LiveSettings{Title: "测试直播", AreaID: "376", OBSHost: "obs.example.test"}, false},
		{"valid IPv6 OBS host", LiveSettings{Title: "测试直播", AreaID: "376", OBSHost: "[2001:db8::1]"}, false},
		{"OBS host with scheme", LiveSettings{Title: "测试直播", AreaID: "376", OBSHost: "ws://127.0.0.1"}, true},
		{"OBS host with port", LiveSettings{Title: "测试直播", AreaID: "376", OBSHost: "127.0.0.1:4455"}, true},
		{"valid custom OBS port", LiveSettings{Title: "测试直播", AreaID: "376", OBSPort: "4456"}, false},
		{"non numeric OBS port", LiveSettings{Title: "测试直播", AreaID: "376", OBSPort: "port"}, true},
		{"out of range OBS port", LiveSettings{Title: "测试直播", AreaID: "376", OBSPort: "65536"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.settings.Validate(); (got != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", got, tt.wantErr)
			}
		})
	}
}

func TestEndpointCatalog(t *testing.T) {
	for _, name := range []string{"GetMyRoomID", "GetRoomSnapshot", "GetOnlineGoldRank", "GetGuardTopList", "GetRoomPlaybackURL", "GetDanmakuInfo", "GetDanmakuInfoLegacy", "GetDanmakuUserInfo", "GetUserCard", "ModifyUserRelation", "GetRoomAdminSenior", "GetRoomAdmins", "AppointRoomAdmin", "DismissRoomAdmin", "SearchRoomUser", "GetMutedUsers", "MuteRoomUser", "UnmuteRoomUser", "GetRoomBlacklist", "BlacklistRoomUser", "UnblacklistRoomUser", "GetShieldKeywords", "AddShieldKeyword", "DeleteShieldKeyword", "GetRoomSilent", "SetRoomSilent", "SendDanmaku", "GetLiveAreas", "UploadRoomCover", "AddLiveTag", "DeleteLiveTag", "UpdateRoomNews", "UpdatePreLiveInfo", "UpdateLiveInfo", "StartLive", "StopLive", "GetTVQRCode", "CheckQRStatus"} {
		endpoint, ok := EndpointByName(name)
		if !ok || endpoint.Path == "" || endpoint.Method == "" {
			t.Fatalf("endpoint %q missing from catalog", name)
		}
	}
	if _, exists := EndpointByName("KeepAlive"); exists {
		t.Fatal("viewer mobile heartbeat must not be registered as a broadcaster endpoint")
	}
}

func TestNewClientUsesSystemProxyTransportByDefault(t *testing.T) {
	client := NewClient(nil)
	transport, ok := client.HTTPClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("default transport = %T, want *http.Transport", client.HTTPClient.Transport)
	}
	if transport.Proxy == nil {
		t.Fatal("default transport does not have a proxy resolver")
	}
}

func TestGetOnlineGoldRank(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		query := r.URL.Query()
		if r.URL.Path != "/xlive/general-interface/v1/rank/getOnlineGoldRank" || query.Get("roomId") != "1" || query.Get("ruid") != "42" || query.Get("pageSize") != "50" {
			t.Errorf("online rank request = %s", r.URL.String())
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"code":0,"message":"OK","data":{"onlineNum":"23","OnlineRankItem":[{"userRank":1,"uid":7,"name":"高能用户","score":"11","guard_level":3}]}}`)),
			Header:     make(http.Header),
		}, nil
	})
	client := NewClient(&http.Client{Transport: transport})
	client.BaseURL = "http://test.invalid"
	snapshot, err := client.getOnlineGoldRank(context.Background(), "1", 42, "sess", "jct")
	if err != nil {
		t.Fatalf("getOnlineGoldRank() error = %v", err)
	}
	if snapshot.Online != 23 || len(snapshot.Members) != 1 || snapshot.Members[0].Username != "高能用户" || snapshot.Members[0].Score != 11 || snapshot.Members[0].GuardLevel != 3 {
		t.Fatalf("online rank snapshot = %#v", snapshot)
	}
}

func TestGetGuardTopList(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		query := r.URL.Query()
		if r.URL.Path != "/guard/topList" || query.Get("roomid") != "1" || query.Get("ruid") != "42" || query.Get("page") != "1" {
			t.Errorf("guard top list request = %s", r.URL.String())
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body: io.NopCloser(strings.NewReader(`{
				"code": 0,
				"message": "OK",
				"data": {
					"info": {"num": 12, "page": 2, "now": 1},
					"top3": [
						{"uid": 101, "username": "提督甲", "rank": 1, "guard_level": 2, "is_alive": 1}
					],
					"list": [
						{"uid": 102, "username": "舰长乙", "rank": 2, "guard_level": 3, "is_alive": 0}
					]
				}
			}`)),
			Header: make(http.Header),
		}, nil
	})
	client := NewClient(&http.Client{Transport: transport})
	client.BaseURL = "http://test.invalid"
	snapshot, err := client.GetGuardTopList(context.Background(), "1", 42, 1)
	if err != nil {
		t.Fatalf("GetGuardTopList() error = %v", err)
	}
	if snapshot.Total != 12 || len(snapshot.Members) != 2 {
		t.Fatalf("guard snapshot = %#v", snapshot)
	}
	if snapshot.Members[0].Username != "提督甲" || snapshot.Members[0].GuardLevel != 2 || !snapshot.Members[0].IsAlive {
		t.Errorf("guard member[0] = %#v", snapshot.Members[0])
	}
	if snapshot.Members[1].Username != "舰长乙" || snapshot.Members[1].GuardLevel != 3 || snapshot.Members[1].IsAlive {
		t.Errorf("guard member[1] = %#v", snapshot.Members[1])
	}
}

func TestGetRoomPlaybackURL(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		query := r.URL.Query()
		if r.URL.Path != "/xlive/web-room/v2/index/getRoomPlayInfo" || query.Get("room_id") != "1" || query.Get("protocol") != "0,1" || query.Get("format") != "0,1,2" || query.Get("codec") != "0,1" {
			t.Errorf("playback request = %s", r.URL.String())
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"code":0,"data":{"live_status":1,"playurl_info":{"playurl":{"stream":[{"protocol_name":"http_stream","format":[{"format_name":"flv","codec":[{"codec_name":"avc","base_url":"/live/test.flv?","url_info":[{"host":"https://cdn.example.com","extra":"token=flv"}]}]}]},{"protocol_name":"http_hls","format":[{"format_name":"ts","codec":[{"codec_name":"avc","base_url":"/live/test.m3u8?","url_info":[{"host":"https://cdn.example.com","extra":"token=hls"}]}]}]}]}}}}`)),
			Header:     make(http.Header),
		}, nil
	})
	client := NewClient(&http.Client{Transport: transport})
	client.BaseURL = "http://test.invalid"
	playbackURL, err := client.GetRoomPlaybackURL(context.Background(), "1", "sess", "jct")
	if err != nil {
		t.Fatalf("GetRoomPlaybackURL() error = %v", err)
	}
	if playbackURL != "https://cdn.example.com/live/test.flv?token=flv" {
		t.Fatalf("playback URL = %q", playbackURL)
	}
}

func TestGetRoomSnapshot(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Query().Get("room_id") != "1" {
			t.Errorf("room_id = %q, want 1", r.URL.Query().Get("room_id"))
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"code":0,"data":{"room_info":{"uid":99,"room_id":1,"title":"标题","description":"简介","tags":"游戏,聊天","area_name":"单机游戏","parent_area_name":"主机游戏","live_status":1,"online":"42"},"watched_show":{"num":1234}}}`)),
			Header:     make(http.Header),
		}, nil
	})
	client := NewClient(&http.Client{Transport: transport})
	client.BaseURL = "http://test.invalid"
	snapshot, err := client.GetRoomSnapshot(context.Background(), "1")
	if err != nil {
		t.Fatalf("GetRoomSnapshot() error = %v", err)
	}
	if snapshot.AnchorID != "99" || snapshot.RoomID != "1" || snapshot.AreaName != "单机游戏" || snapshot.Online != 42 || !snapshot.OnlineKnown || snapshot.Watched != 1234 || !snapshot.WatchedKnown {
		t.Fatalf("GetRoomSnapshot() = %#v", snapshot)
	}
}

func TestGetRoomManagementCapabilitiesUsesBadgePermissionsAndAnchorIdentity(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/xlive/web-room/v1/index/getInfoByUser":
			if r.URL.Query().Get("room_id") != "1" || r.Header.Get("Cookie") != "SESSDATA=sess; bili_jct=jct" {
				t.Errorf("capability request = %s, cookie %q", r.URL.String(), r.Header.Get("Cookie"))
			}
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"code":0,"data":{"info":{"uid":"99"},"badge":{"is_room_admin":1,"admin_level":"2","permissions":[1,"2",100]}}}`)), Header: make(http.Header)}, nil
		case "/xlive/web-room/v1/index/getInfoByRoom":
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"code":0,"data":{"room_info":{"uid":99,"room_id":1}}}`)), Header: make(http.Header)}, nil
		default:
			return nil, fmt.Errorf("unexpected path %s", r.URL.Path)
		}
	})
	client := NewClient(&http.Client{Transport: transport})
	client.BaseURL = "http://test.invalid"
	capabilities, err := client.GetRoomManagementCapabilities(context.Background(), "1", "sess", "jct")
	if err != nil {
		t.Fatalf("GetRoomManagementCapabilities() error = %v", err)
	}
	if capabilities.UserID != "99" || capabilities.AnchorID != "99" || !capabilities.IsAnchor || !capabilities.IsAdmin || capabilities.AdminLevel != 2 || !capabilities.HasPermission(RoomPermissionBlacklist) {
		t.Fatalf("capabilities = %#v", capabilities)
	}
	if !capabilities.CanMute(2) || !capabilities.CanBlacklist(99) || !capabilities.CanMuteUser(0, false) {
		t.Fatalf("anchor capabilities should override target admin level: %#v", capabilities)
	}
}

func TestRoomManagementPermissionHierarchy(t *testing.T) {
	capabilities := RoomManagementCapabilities{IsAdmin: true, AdminLevel: 2, Permissions: []int{RoomPermissionMute}}
	if !capabilities.CanMute(1) || capabilities.CanMute(2) || capabilities.CanBlacklist(0) {
		t.Fatalf("permission hierarchy mismatch: %#v", capabilities)
	}
	if capabilities.CanMuteUser(0, false) {
		t.Fatalf("unknown target level was accepted: %#v", capabilities)
	}
}

func TestSearchRoomUsersSupportsArrayAndObjectPayloads(t *testing.T) {
	tests := []struct {
		name     string
		response string
	}{
		{
			name:     "array",
			response: `{"code":0,"data":[{"uid":42,"uname":"用户甲","admin_level":2},{"uid":42,"uname":"重复用户"},{"uname":"缺少UID"}]}`,
		},
		{
			name:     "object items",
			response: `{"code":0,"data":{"items":[{"tuid":"43","tname":"用户乙","admin_level":"1"}]}}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.Method != http.MethodGet || r.URL.Path != "/banned_service/v2/Silent/search_user" || r.URL.Query().Get("search") != "用户" {
					t.Errorf("search request = %s %s", r.Method, r.URL.String())
				}
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(tt.response)), Header: make(http.Header)}, nil
			})
			client := NewClient(&http.Client{Transport: transport})
			client.BaseURL = "http://test.invalid"

			results, err := client.SearchRoomUsers(context.Background(), " 用户 ", "sess", "jct")
			if err != nil {
				t.Fatalf("SearchRoomUsers() error = %v", err)
			}
			if len(results) != 1 || results[0].UserID == "" || results[0].Username == "" || results[0].AdminLevel == 0 || !results[0].AdminLevelKnown {
				t.Fatalf("search results = %#v", results)
			}
		})
	}
}

func TestSearchRoomUsersKeepsMissingAdminLevelUnknown(t *testing.T) {
	transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"code":0,"data":[{"uid":42,"uname":"用户甲"},{"uid":43,"uname":"用户乙","admin_level":0}]}`)), Header: make(http.Header)}, nil
	})
	client := NewClient(&http.Client{Transport: transport})
	client.BaseURL = "http://test.invalid"

	results, err := client.SearchRoomUsers(context.Background(), "用户", "sess", "jct")
	if err != nil {
		t.Fatalf("SearchRoomUsers() error = %v", err)
	}
	if len(results) != 2 || results[0].AdminLevelKnown || !results[1].AdminLevelKnown || results[1].AdminLevel != 0 {
		t.Fatalf("search results = %#v", results)
	}
}

func TestSearchRoomUsersPreservesAPIErrorWithArrayData(t *testing.T) {
	transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"code":3,"message":"请先登录","data":[]}`)), Header: make(http.Header)}, nil
	})
	client := NewClient(&http.Client{Transport: transport})
	client.BaseURL = "http://test.invalid"

	_, err := client.SearchRoomUsers(context.Background(), "42", "sess", "jct")
	if err == nil || !strings.Contains(err.Error(), "请先登录") {
		t.Fatalf("SearchRoomUsers() error = %v", err)
	}
}

func TestGetRoomAdminSeniorStatusKeepsDisabledState(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Query().Get("anchor_id") != "99" {
			t.Errorf("anchor_id = %q", r.URL.Query().Get("anchor_id"))
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"code":0,"data":{"status":0}}`)), Header: make(http.Header)}, nil
	})
	client := NewClient(&http.Client{Transport: transport})
	client.BaseURL = "http://test.invalid"

	status, err := client.GetRoomAdminSeniorStatus(context.Background(), "99", "sess", "jct")
	if err != nil || status != 0 {
		t.Fatalf("GetRoomAdminSeniorStatus() = %d, %v", status, err)
	}
}

func TestRoomManagementMutationsMatchWebRequests(t *testing.T) {
	wantPath := ""
	wantValues := url.Values{}
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPost || r.URL.Path != wantPath {
			t.Errorf("request = %s %s, want POST %s", r.Method, r.URL.Path, wantPath)
		}
		body, _ := io.ReadAll(r.Body)
		values, err := url.ParseQuery(string(body))
		if err != nil {
			t.Errorf("parse form: %v", err)
		}
		for key, want := range wantValues {
			if got := values[key]; fmt.Sprint(got) != fmt.Sprint(want) {
				t.Errorf("%s = %v, want %v", key, got, want)
			}
		}
		if values.Get("csrf") != "jct" || values.Get("csrf_token") != "jct" {
			t.Errorf("csrf values = %v", values)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"code":0}`)), Header: make(http.Header)}, nil
	})
	client := NewClient(&http.Client{Transport: transport})
	client.BaseURL = "http://test.invalid"

	wantPath = "/xlive/web-ucenter/v1/banned/AddSilentUser"
	wantValues = url.Values{"room_id": {"1"}, "tuid": {"42"}, "msg": {"原弹幕"}, "mobile_app": {"web"}, "type": {"1"}, "hour": {"168"}}
	if err := client.MuteRoomUser(context.Background(), "1", "42", "原弹幕", RoomMuteSevenDays, "sess", "jct"); err != nil {
		t.Fatalf("MuteRoomUser() error = %v", err)
	}

	wantPath = "/xlive/app-ucenter/v2/xbanned/banned/AddBlack"
	wantValues = url.Values{"anchor_id": {"99"}, "tuid": {"42"}, "spmid": {"444.8.0.0"}}
	if err := client.BlacklistRoomUser(context.Background(), "99", "42", "sess", "jct"); err != nil {
		t.Fatalf("BlacklistRoomUser() error = %v", err)
	}

	wantPath = "/xlive/web-room/v1/banned/RoomSilent"
	wantValues = url.Values{"room_id": {"1"}, "type": {"wealth"}, "level": {"80"}, "minute": {"0"}}
	if err := client.SetRoomSilentState(context.Background(), "1", RoomSilentWealth, 80, 0, "sess", "jct"); err != nil {
		t.Fatalf("SetRoomSilentState() error = %v", err)
	}
}

func TestGetRoomSilentStateTreatsZeroDurationAsActive(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet || r.URL.Path != "/xlive/web-room/v1/banned/GetRoomSilent" || r.URL.Query().Get("room_id") != "1" {
			t.Errorf("room silent request = %s %s", r.Method, r.URL.String())
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"code":0,"data":{"type":"medal","level":12,"minute":0,"second":0}}`)),
			Header:     make(http.Header),
		}, nil
	})
	client := NewClient(&http.Client{Transport: transport})
	client.BaseURL = "http://test.invalid"

	state, err := client.GetRoomSilentState(context.Background(), "1", "sess", "jct")
	if err != nil {
		t.Fatalf("GetRoomSilentState() error = %v", err)
	}
	if !state.Enabled || state.Audience != RoomSilentMedal || state.Level != 12 || state.RemainingSeconds != 0 {
		t.Fatalf("room silent state = %#v", state)
	}
}

func TestRoomManagementInputValidation(t *testing.T) {
	client := NewClient(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("request should not be sent")
	})})
	if err := client.AddRoomShieldKeyword(context.Background(), "1", "   ", "sess", "jct"); err == nil {
		t.Fatal("empty shield keyword was accepted")
	}
	if err := client.SetRoomSilentState(context.Background(), "1", RoomSilentWealth, 0, 0, "sess", "jct"); err == nil {
		t.Fatal("invalid wealth level was accepted")
	}
	if err := client.MuteRoomUser(context.Background(), "1", "0", "", RoomMutePermanent, "sess", "jct"); err == nil {
		t.Fatal("invalid target UID was accepted")
	}
	if keyword, err := validateRoomShieldKeyword(strings.Repeat("词", 100)); err != nil || len([]rune(keyword)) != 100 {
		t.Fatalf("keyword should be left to upstream validation: length=%d, err=%v", len([]rune(keyword)), err)
	}
	if err := validateRoomSilent(RoomSilentWealth, 81, 0); err != nil {
		t.Fatalf("positive level should be left to upstream validation: %v", err)
	}
}

func TestRoomManagementListResponses(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		var response string
		switch r.URL.Path {
		case "/xlive/app-ucenter/v1/roomAdmin/get_by_anchor":
			response = `{"code":0,"data":{"data":[{"uid":42,"uname":"房管甲","ctime":"2026-09-01","admin_level":2}],"page":{"page":1,"total_page":3},"max_room_anchors_number":10}}`
		case "/xlive/web-ucenter/v1/banned/GetSilentUserList":
			body, _ := io.ReadAll(r.Body)
			values, err := url.ParseQuery(string(body))
			if err != nil {
				t.Errorf("parse muted-list form: %v", err)
			}
			if values.Get("room_id") != "1" || values.Get("pn") != "2" || values.Get("ps") != "20" {
				t.Errorf("muted-list form = %v", values)
			}
			response = `{"code":0,"data":{"data":[{"tuid":43,"tname":"用户乙","name":"主播","block_end_time":"永久","admin_level":0,"is_anchor":1}],"total":21,"total_page":3}}`
		case "/xlive/app-ucenter/v2/xbanned/banned/GetBlackList":
			response = `{"code":0,"data":{"data":[{"uid":44,"name":"用户丙","mtime":"2026-09-02","operator_name":"主播"}]}}`
		case "/xlive/web-ucenter/v1/banned/GetShieldKeywordList":
			response = `{"code":0,"data":{"keyword_list":[{"keyword":"测试词"}],"max_limit":20}}`
		default:
			return nil, fmt.Errorf("unexpected path %s", r.URL.Path)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(response)), Header: make(http.Header)}, nil
	})
	client := NewClient(&http.Client{Transport: transport})
	client.BaseURL = "http://test.invalid"

	admins, err := client.GetRoomAdmins(context.Background(), 1, "sess", "jct")
	if err != nil || len(admins.Items) != 1 || admins.Items[0].Level != 2 || !admins.Items[0].LevelKnown || admins.TotalPages != 3 || admins.MaxCount != 10 {
		t.Fatalf("admins = %#v, err = %v", admins, err)
	}
	muted, err := client.GetMutedRoomUsers(context.Background(), "1", 2, "sess", "jct")
	if err != nil || len(muted.Items) != 1 || muted.Items[0].UserID != "43" || !muted.Items[0].OperatorIsAnchor || muted.Page != 2 || muted.Total != 21 || muted.TotalPages != 3 {
		t.Fatalf("muted = %#v, err = %v", muted, err)
	}
	blacklist, err := client.GetRoomBlacklist(context.Background(), "99", 1, 10, "sess", "jct")
	if err != nil || len(blacklist.Items) != 1 || blacklist.Items[0].Username != "用户丙" || blacklist.Items[0].CreatedAt != "2026-09-02" || blacklist.Total != 1 {
		t.Fatalf("blacklist = %#v, err = %v", blacklist, err)
	}
	keywords, err := client.GetRoomShieldKeywords(context.Background(), "1", "sess", "jct")
	if err != nil || len(keywords.Keywords) != 1 || keywords.Keywords[0] != "测试词" || keywords.MaxCount != 20 {
		t.Fatalf("keywords = %#v, err = %v", keywords, err)
	}
}

func TestGetRoomSnapshotFallsBackToLegacyEndpoint(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/xlive/web-room/v1/index/getInfoByRoom" {
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"code":-352,"message":"风控校验失败"}`)), Header: make(http.Header)}, nil
		}
		if r.URL.Path == "/room/v1/Room/get_info" {
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"code":0,"data":{"room_id":1,"title":"备用标题","area_name":"单机游戏","live_status":1,"online":7}}`)), Header: make(http.Header)}, nil
		}
		return nil, fmt.Errorf("unexpected path %s", r.URL.Path)
	})
	client := NewClient(&http.Client{Transport: transport})
	client.BaseURL = "http://test.invalid"
	snapshot, err := client.GetRoomSnapshot(context.Background(), "1")
	if err != nil {
		t.Fatalf("GetRoomSnapshot() fallback error = %v", err)
	}
	if snapshot.Title != "备用标题" || snapshot.AreaName != "单机游戏" || snapshot.Online != 7 || !snapshot.OnlineKnown {
		t.Fatalf("fallback snapshot = %#v", snapshot)
	}
}

func TestUploadRoomCover(t *testing.T) {
	temp, err := os.CreateTemp(t.TempDir(), "cover-*.png")
	if err != nil {
		t.Fatal(err)
	}
	coverImage := image.NewRGBA(image.Rect(0, 0, 640, 360))
	for y := 0; y < 360; y++ {
		for x := 0; x < 640; x++ {
			coverImage.Set(x, y, color.RGBA{R: 20, G: 120, B: 220, A: 255})
		}
	}
	if err := png.Encode(temp, coverImage); err != nil {
		t.Fatal(err)
	}
	if _, err := temp.Write(make([]byte, maxRoomCoverBytes)); err != nil {
		t.Fatal(err)
	}
	if err := temp.Close(); err != nil {
		t.Fatal(err)
	}

	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if got := r.Header.Get("Cookie"); got != "SESSDATA=sess; bili_jct=jct" {
			t.Errorf("Cookie = %q", got)
		}
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data;") {
			t.Errorf("Content-Type = %q", r.Header.Get("Content-Type"))
		}
		body, _ := io.ReadAll(r.Body)
		contentType := r.Header.Get("Content-Type")
		mediaType, params, err := mime.ParseMediaType(contentType)
		if err != nil || mediaType != "multipart/form-data" {
			t.Errorf("invalid multipart content type: %q", contentType)
		} else {
			reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])
			part, partErr := reader.NextPart()
			if partErr != nil || part.FormName() != "bucket" {
				t.Errorf("bucket part form name = %q, err=%v", part.FormName(), partErr)
			} else if value, readErr := io.ReadAll(part); readErr != nil || string(value) != "live" {
				t.Errorf("bucket part value = %q, err=%v", value, readErr)
			}
			part, partErr = reader.NextPart()
			if partErr != nil || part.FormName() != "dir" {
				t.Errorf("dir part form name = %q, err=%v", part.FormName(), partErr)
			} else if value, readErr := io.ReadAll(part); readErr != nil || string(value) != "new_room_cover" {
				t.Errorf("dir part value = %q, err=%v", value, readErr)
			}
			part, partErr = reader.NextPart()
			if partErr != nil || part.Header.Get("Content-Type") != "image/jpeg" || part.FormName() != "file" || part.FileName() != "blob" {
				t.Errorf("cover part name=%q filename=%q content type=%q, err=%v", part.FormName(), part.FileName(), part.Header.Get("Content-Type"), partErr)
			}
		}
		if !bytes.Contains(body, []byte{0xff, 0xd8, 0xff}) || strings.Contains(string(body), `name="biz"`) || strings.Contains(string(body), `name="category"`) || r.URL.Query().Get("csrf") != "jct" || r.URL.Query().Get("csrf_token") != "" {
			t.Errorf("multipart body does not contain expected fields: %s", body)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"code":0,"data":{"url":"https://i.example/cover.png"}}`)), Header: make(http.Header)}, nil
	})
	client := NewClient(&http.Client{Transport: transport})
	client.BaseURL = "http://test.invalid"
	got, err := client.UploadRoomCover(context.Background(), "1", "sess", "jct", temp.Name())
	if err != nil {
		t.Fatalf("UploadRoomCover() error = %v", err)
	}
	if got != "https://i.example/cover.png" {
		t.Fatalf("UploadRoomCover() = %q", got)
	}
}

func TestUploadRoomCoverURL(t *testing.T) {
	var downloaded atomic.Bool
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.Method {
		case http.MethodGet:
			downloaded.Store(true)
			if r.Header.Get("User-Agent") != biliBrowserUserAgent {
				t.Errorf("cover download User-Agent = %q", r.Header.Get("User-Agent"))
			}
			var data bytes.Buffer
			coverImage := image.NewRGBA(image.Rect(0, 0, 640, 360))
			if err := jpeg.Encode(&data, coverImage, nil); err != nil {
				return nil, err
			}
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(data.Bytes())), Header: http.Header{"Content-Type": []string{"text/plain"}}}, nil
		case http.MethodPost:
			body, _ := io.ReadAll(r.Body)
			if len(body) == 0 {
				t.Errorf("uploaded body is empty")
			}
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"code":0,"data":{"url":"https://i.example/cover.jpg"}}`)), Header: make(http.Header)}, nil
		default:
			return nil, fmt.Errorf("unexpected method %s", r.Method)
		}
	})
	client := NewClient(&http.Client{Transport: transport})
	client.BaseURL = "http://test.invalid"
	got, err := client.UploadRoomCoverURL(context.Background(), "1", "sess", "jct", "https://apis.example/download")
	if err != nil {
		t.Fatalf("UploadRoomCoverURL() error = %v", err)
	}
	if !downloaded.Load() || got != "https://i.example/cover.jpg" {
		t.Fatalf("UploadRoomCoverURL() downloaded=%v url=%q", downloaded.Load(), got)
	}
}

func TestUploadRoomCoverURLRejectsHTMLBehindImageURL(t *testing.T) {
	var uploaded atomic.Bool
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodPost {
			uploaded.Store(true)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader("<!doctype html><title>Access denied</title>")),
			Header:     http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
		}, nil
	})
	client := NewClient(&http.Client{Transport: transport})
	_, err := client.UploadRoomCoverURL(context.Background(), "1", "sess", "jct", "https://apis.example/cover.jpg")
	if err == nil || !strings.Contains(err.Error(), "检测类型：text/html") {
		t.Fatalf("UploadRoomCoverURL() error = %v", err)
	}
	if uploaded.Load() {
		t.Fatal("invalid remote content must not be uploaded")
	}
}

func TestUploadRoomCoverURLDoesNotWriteRetryLogsToTerminal(t *testing.T) {
	originalStderr := os.Stderr
	readStderr, writeStderr, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = writeStderr
	t.Cleanup(func() {
		os.Stderr = originalStderr
		_ = readStderr.Close()
		_ = writeStderr.Close()
	})

	client := NewClient(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("download unavailable")
	})})
	_, downloadErr := client.UploadRoomCoverURL(context.Background(), "1", "sess", "jct", "https://apis.example/cover.jpg")
	if downloadErr == nil {
		t.Fatal("UploadRoomCoverURL() unexpectedly succeeded")
	}
	if err := writeStderr.Close(); err != nil {
		t.Fatal(err)
	}
	written, err := io.ReadAll(readStderr)
	if err != nil {
		t.Fatal(err)
	}
	if len(written) != 0 {
		t.Fatalf("cover download wrote outside the TUI: %q", written)
	}
}

func TestRemoteCoverExtensionRejectsTruncatedJPEG(t *testing.T) {
	_, err := remoteCoverExtension([]byte{0xff, 0xd8, 0xff, 0xe0}, "image/jpeg")
	if err == nil || !strings.Contains(err.Error(), "数据不完整或已损坏") {
		t.Fatalf("remoteCoverExtension() error = %v", err)
	}
}

func TestNormalizeCoverForUploadMatchesWebCoverOutput(t *testing.T) {
	temp, err := os.CreateTemp(t.TempDir(), "small-cover-*.png")
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(temp, image.NewRGBA(image.Rect(0, 0, 173, 148))); err != nil {
		t.Fatal(err)
	}
	if _, err := temp.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	data, contentType, width, height, err := normalizeCoverForUpload(context.Background(), temp)
	if err != nil {
		t.Fatal(err)
	}
	if contentType != "image/jpeg" || width != webRoomCoverWidth || height != webRoomCoverHeight {
		t.Fatalf("normalized cover = %s %dx%d", contentType, width, height)
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || format != "jpeg" || config.Width != width || config.Height != height {
		t.Fatalf("encoded cover = %s %dx%d, err=%v", format, config.Width, config.Height, err)
	}
}

func TestEncodeJPEGWithinLimitReducesQualityWhenNeeded(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, webRoomCoverWidth, webRoomCoverHeight))
	state := uint32(1)
	for y := 0; y < webRoomCoverHeight; y++ {
		for x := 0; x < webRoomCoverWidth; x++ {
			offset := src.PixOffset(x, y)
			for channel := 0; channel < 3; channel++ {
				state ^= state << 13
				state ^= state >> 17
				state ^= state << 5
				src.Pix[offset+channel] = byte(state)
			}
			src.Pix[offset+3] = 0xff
		}
	}

	const limit = 120 * 1024
	data, quality, err := encodeJPEGWithinLimit(context.Background(), src, limit)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > limit {
		t.Fatalf("encoded cover size = %d, limit = %d", len(data), limit)
	}
	if quality <= 0 || quality >= webRoomCoverQuality {
		t.Fatalf("fallback JPEG quality = %d", quality)
	}
	if config, format, err := image.DecodeConfig(bytes.NewReader(data)); err != nil || format != "jpeg" || config.Width != webRoomCoverWidth || config.Height != webRoomCoverHeight {
		t.Fatalf("fallback encoded cover = %s %dx%d, err=%v", format, config.Width, config.Height, err)
	}
}

func TestCenteredCropRectUsesImageCenter(t *testing.T) {
	tests := []struct {
		name   string
		bounds image.Rectangle
		want   image.Rectangle
	}{
		{name: "portrait", bounds: image.Rect(0, 0, 400, 800), want: image.Rect(0, 250, 400, 550)},
		{name: "landscape", bounds: image.Rect(10, 20, 810, 420), want: image.Rect(143, 20, 676, 420)},
		{name: "four by three", bounds: image.Rect(5, 7, 725, 547), want: image.Rect(5, 7, 725, 547)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := centeredCropRect(test.bounds, webRoomCoverWidth, webRoomCoverHeight); got != test.want {
				t.Fatalf("centeredCropRect(%v) = %v, want %v", test.bounds, got, test.want)
			}
		})
	}
}

func TestNormalizeCoverForUploadRejectsUnknownFormat(t *testing.T) {
	temp, err := os.CreateTemp(t.TempDir(), "invalid-cover-*.jpg")
	if err != nil {
		t.Fatal(err)
	}
	defer temp.Close()
	if _, err := temp.WriteString("not an image"); err != nil {
		t.Fatal(err)
	}
	if _, err := temp.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	_, _, _, _, err = normalizeCoverForUpload(context.Background(), temp)
	if err == nil || !strings.Contains(err.Error(), "请提供有效的 JPG、PNG 或 WebP 图片") {
		t.Fatalf("invalid cover error = %v", err)
	}
}

func TestUpdateLiveInfoWithCookie(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if got := r.Header.Get("Cookie"); got != "SESSDATA=sess; bili_jct=jct" {
			t.Errorf("Cookie = %q", got)
		}
		body, _ := io.ReadAll(r.Body)
		values, _ := url.ParseQuery(string(body))
		for key, want := range map[string]string{"room_id": "1", "title": "标题", "area_v2": "376", "csrf": "jct", "csrf_token": "jct"} {
			if values.Get(key) != want {
				t.Errorf("%s = %q, want %q", key, values.Get(key), want)
			}
		}
		if values.Get("cover") != "" {
			t.Errorf("cover = %q, want empty; cover is updated by UpdatePreLiveInfo", values.Get("cover"))
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"code":0,"message":"0"}`)), Header: make(http.Header)}, nil
	})
	client := NewClient(&http.Client{Transport: transport})
	client.BaseURL = "http://test.invalid"
	err := client.UpdateLiveInfoWithCookie(context.Background(), "1", "token", "sess", "jct", LiveSettings{Title: "标题", AreaID: "376", CoverPath: "https://example.com/cover.jpg"})
	if err != nil {
		t.Fatalf("UpdateLiveInfoWithCookie() error = %v", err)
	}
}

func TestLiveTagEndpoints(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if got := r.Header.Get("Cookie"); got != "SESSDATA=sess; bili_jct=jct" {
			t.Errorf("Cookie = %q", got)
		}
		if err := r.ParseForm(); err != nil {
			return nil, err
		}
		switch r.URL.Path {
		case "/xlive/app-blink/v1/liveTagService/AddLiveTag":
			if r.PostForm.Get("room_id") != "1" || r.PostForm.Get("tag_content") != "游戏" || r.PostForm.Get("csrf") != "jct" {
				t.Errorf("unexpected add tag form: %v", r.PostForm)
			}
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"code":0,"data":{"tag_id":4238676}}`)), Header: make(http.Header)}, nil
		case "/xlive/app-blink/v1/liveTagService/DeleteLiveTag":
			if r.PostForm.Get("room_id") != "1" || r.PostForm.Get("tag_id") != "4238676" || r.PostForm.Get("csrf_token") != "jct" {
				t.Errorf("unexpected delete tag form: %v", r.PostForm)
			}
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"code":0,"message":"0"}`)), Header: make(http.Header)}, nil
		default:
			return nil, fmt.Errorf("unexpected path %s", r.URL.Path)
		}
	})
	client := NewClient(&http.Client{Transport: transport})
	client.BaseURL = "http://test.invalid"
	tagID, err := client.AddLiveTag(context.Background(), "1", "sess", "jct", "游戏")
	if err != nil || tagID != "4238676" {
		t.Fatalf("AddLiveTag() = %q, %v", tagID, err)
	}
	if err := client.DeleteLiveTag(context.Background(), "1", "sess", "jct", tagID); err != nil {
		t.Fatalf("DeleteLiveTag() error = %v", err)
	}
}

func TestUpdateRoomNews(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/xlive/app-blink/v1/index/updateRoomNews" {
			return nil, fmt.Errorf("unexpected path %s", r.URL.Path)
		}
		if err := r.ParseForm(); err != nil {
			return nil, err
		}
		if r.PostForm.Get("room_id") != "1" || r.PostForm.Get("content") != "今晚直播" || r.PostForm.Get("csrf_token") != "jct" {
			t.Errorf("unexpected room news form: %v", r.PostForm)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"code":0,"message":"0"}`)), Header: make(http.Header)}, nil
	})
	client := NewClient(&http.Client{Transport: transport})
	client.BaseURL = "http://test.invalid"
	if err := client.UpdateRoomNews(context.Background(), "1", "sess", "jct", "今晚直播"); err != nil {
		t.Fatalf("UpdateRoomNews() error = %v", err)
	}
}

func TestUpdatePreLiveCoverUsesWebPayload(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/xlive/app-blink/v1/preLive/UpdatePreLiveInfo" {
			return nil, fmt.Errorf("unexpected path %s", r.URL.Path)
		}
		if err := r.ParseForm(); err != nil {
			return nil, err
		}
		for key, want := range map[string]string{
			"platform": "web", "mobi_app": "web", "build": "1",
			"cover": "http://i0.hdslb.com/test.jpg", "coverVertical": "",
			"liveDirectionType": "1", "aiCoverTaskId": "", "csrf": "jct",
			"csrf_token": "jct", "visit_id": "",
		} {
			if got := r.PostForm.Get(key); got != want {
				t.Errorf("%s = %q, want %q", key, got, want)
			}
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"code":0,"message":"0"}`)), Header: make(http.Header)}, nil
	})
	client := NewClient(&http.Client{Transport: transport})
	client.BaseURL = "http://test.invalid"
	if err := client.UpdatePreLiveCover(context.Background(), "1", "sess", "jct", "http://i0.hdslb.com/test.jpg"); err != nil {
		t.Fatalf("UpdatePreLiveCover() error = %v", err)
	}
}

func TestUpdatePreLiveCoverUsesPortraitDirection(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if err := r.ParseForm(); err != nil {
			return nil, err
		}
		if got := r.PostForm.Get("liveDirectionType"); got != "2" {
			t.Errorf("liveDirectionType = %q, want 2", got)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"code":0}`)), Header: make(http.Header)}, nil
	})
	client := NewClient(&http.Client{Transport: transport})
	client.BaseURL = "http://test.invalid"
	if err := client.UpdatePreLiveCover(context.Background(), "1", "sess", "jct", "http://i0.hdslb.com/test.jpg", OrientationPortrait); err != nil {
		t.Fatalf("UpdatePreLiveCover() portrait error = %v", err)
	}
}

func TestUpdatePreLiveCoverRejectsEmptyURL(t *testing.T) {
	client := NewClient(nil)
	if err := client.UpdatePreLiveCover(context.Background(), "1", "sess", "jct", " "); err == nil {
		t.Fatal("empty cover URL was accepted")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestUpdateLiveInfoRequiresCookie(t *testing.T) {
	client := NewClient(nil)
	err := client.UpdateLiveInfoWithCookie(context.Background(), "1", "token", "", "", LiveSettings{Title: "标题", AreaID: "376"})
	if err == nil || !strings.Contains(err.Error(), "SESSDATA") {
		t.Fatalf("expected cookie validation error, got %v", err)
	}
}

func TestStartAndStopLive(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if r.PostForm.Get("room_id") != "1" || r.PostForm.Get("access_key") != "token" || r.PostForm.Get("sign") == "" {
			t.Errorf("unexpected live form: %v", r.PostForm)
		}
		switch r.URL.Path {
		case "/room/v1/Room/startLive":
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"code":0,"data":{"rtmp":{"addr":"rtmp://example/live/","code":"?key=secret"}}}`)), Header: make(http.Header)}, nil
		case "/room/v1/Room/stopLive":
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"code":0,"message":"0"}`)), Header: make(http.Header)}, nil
		default:
			return nil, fmt.Errorf("unexpected path %s", r.URL.Path)
		}
	})
	client := NewClient(&http.Client{Transport: transport})
	client.BaseURL = "http://test.invalid"
	addr, key, err := client.StartLive(context.Background(), "1", "token", LiveSettings{Title: "标题", AreaID: "376"})
	if err != nil || addr != "rtmp://example/live/" || key != "?key=secret" {
		t.Fatalf("StartLive() = (%q, %q, %v)", addr, key, err)
	}
	if err := client.StopLive(context.Background(), "1", "token"); err != nil {
		t.Fatalf("StopLive() error = %v", err)
	}
}

func TestStartLiveUsesPortraitFlag(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if err := r.ParseForm(); err != nil {
			return nil, err
		}
		if got := r.PostForm.Get("is_portrait"); got != "1" {
			t.Errorf("is_portrait = %q, want 1", got)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"code":0,"data":{"rtmp":{"addr":"rtmp://example/live/","code":"?key=secret"}}}`)), Header: make(http.Header)}, nil
	})
	client := NewClient(&http.Client{Transport: transport})
	client.BaseURL = "http://test.invalid"
	if _, _, err := client.StartLive(context.Background(), "1", "token", LiveSettings{Title: "标题", AreaID: "376", Orientation: OrientationPortrait}); err != nil {
		t.Fatalf("StartLive() portrait error = %v", err)
	}
}

func TestStopLiveReportsMsgFallback(t *testing.T) {
	transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"code":65530,"message":"","msg":"token错误"}`)), Header: make(http.Header)}, nil
	})
	client := NewClient(&http.Client{Transport: transport})
	client.BaseURL = "http://test.invalid"
	err := client.StopLive(context.Background(), "1", "token")
	if err == nil || !strings.Contains(err.Error(), "65530") || !strings.Contains(err.Error(), "token错误") {
		t.Fatalf("StopLive() error = %v", err)
	}
}

func TestPostFormRejectsOversizedSuccessfulResponse(t *testing.T) {
	transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
		body := io.LimitReader(strings.NewReader(strings.Repeat("x", int(maxAPIResponseBytes)+1)), maxAPIResponseBytes+1)
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(body), Header: make(http.Header)}, nil
	})
	client := NewClient(&http.Client{Transport: transport})
	client.BaseURL = "http://test.invalid"
	var result any
	err := client.postForm(context.Background(), "/oversized", nil, &result)
	if err == nil || !strings.Contains(err.Error(), "响应超过") {
		t.Fatalf("oversized response error = %v", err)
	}
}

func TestFlexibleID(t *testing.T) {
	var quoted, numeric flexibleID
	if err := quoted.UnmarshalJSON([]byte(`"12"`)); err != nil || quoted != "12" {
		t.Fatalf("quoted ID = %q, err=%v", quoted, err)
	}
	if err := numeric.UnmarshalJSON([]byte(`12`)); err != nil || numeric != "12" {
		t.Fatalf("numeric ID = %q, err=%v", numeric, err)
	}
}
