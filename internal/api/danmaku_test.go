package api

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/andybalholm/brotli"
)

func TestGetDanmakuInfoAndWebSocketURLs(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if got := r.URL.Query().Get("id"); got != "123" {
			t.Fatalf("room id = %q, want 123", got)
		}
		if got := r.URL.Query().Get("type"); got != "0" {
			t.Fatalf("type = %q, want 0", got)
		}
		if got := r.URL.Query().Get("web_location"); got != "444.8" {
			t.Fatalf("web_location = %q, want 444.8", got)
		}
		if got := r.Header.Get("User-Agent"); got == "" {
			t.Fatal("expected browser user-agent")
		}
		if got := r.Header.Get("Cookie"); got != "SESSDATA=sess; bili_jct=jct" {
			t.Fatalf("cookie = %q", got)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"code":0,"message":"0","data":{"token":"abc","host_list":[{"host":"chat.example.com","port":2243,"ws_port":2244,"wss_port":443},{"host":"chat.example.com","wss_port":443}]}}`)), Header: make(http.Header)}, nil
	})
	client := NewClient(&http.Client{Transport: transport})
	client.BaseURL = "http://test.invalid"
	info, err := client.GetDanmakuInfoWithCookie(context.Background(), "123", "sess", "jct")
	if err != nil {
		t.Fatalf("GetDanmakuInfo() error = %v", err)
	}
	if info.Token != "abc" || len(info.Hosts) != 2 {
		t.Fatalf("GetDanmakuInfo() = %#v", info)
	}
	urls := info.WebSocketURLs()
	if len(urls) != 1 || urls[0] != "wss://chat.example.com:443/sub" {
		t.Fatalf("WebSocketURLs() = %#v", urls)
	}
}

func TestRotateDanmakuEndpointsAcrossReconnects(t *testing.T) {
	client := NewClient(nil)
	endpoints := []string{"wss://one.example/sub", "wss://two.example/sub", "wss://three.example/sub"}
	first := client.rotateDanmakuEndpoints(endpoints)
	second := client.rotateDanmakuEndpoints(endpoints)
	third := client.rotateDanmakuEndpoints(endpoints)
	if got, want := strings.Join(first, ","), "wss://one.example/sub,wss://two.example/sub,wss://three.example/sub"; got != want {
		t.Fatalf("first endpoint order = %q, want %q", got, want)
	}
	if got, want := strings.Join(second, ","), "wss://two.example/sub,wss://three.example/sub,wss://one.example/sub"; got != want {
		t.Fatalf("second endpoint order = %q, want %q", got, want)
	}
	if got, want := strings.Join(third, ","), "wss://three.example/sub,wss://one.example/sub,wss://two.example/sub"; got != want {
		t.Fatalf("third endpoint order = %q, want %q", got, want)
	}
	if got := endpoints[0]; got != "wss://one.example/sub" {
		t.Fatalf("rotateDanmakuEndpoints mutated input: %q", got)
	}
}

func TestGetDanmakuInfoFallsBackToLegacyAfterRiskControl(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/xlive/web-room/v1/index/getDanmuInfo":
			if got := r.URL.Query().Get("web_location"); got != "444.8" {
				t.Fatalf("web_location = %q, want 444.8", got)
			}
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"code":-352,"message":"风控校验失败"}`)), Header: make(http.Header)}, nil
		case "/room/v1/Danmu/getConf":
			if got := r.URL.Query().Get("room_id"); got != "123" {
				t.Fatalf("legacy room_id = %q, want 123", got)
			}
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"code":0,"data":{"token":"legacy-token","host_server_list":[{"host":"chat.example.com","port":2243,"wss_port":443}]}}`)), Header: make(http.Header)}, nil
		default:
			return nil, fmt.Errorf("unexpected path %s", r.URL.Path)
		}
	})
	client := NewClient(&http.Client{Transport: transport})
	client.BaseURL = "http://test.invalid"
	info, err := client.GetDanmakuInfoWithCookie(context.Background(), "123", "sess", "jct")
	if err != nil {
		t.Fatalf("GetDanmakuInfoWithCookie() error = %v", err)
	}
	if info.Token != "legacy-token" || len(info.Hosts) != 1 {
		t.Fatalf("legacy info = %#v", info)
	}
}

func TestDanmakuWebSocketHeadersUseAnonymousTokenIdentity(t *testing.T) {
	headers := danmakuWebSocketHeaders("123", "", "", danmakuIdentity{})
	if got := headers.Get("Cookie"); got != "" {
		t.Fatalf("websocket Cookie = %q, want empty for uid=0 authentication", got)
	}
	if got := headers.Get("Origin"); got != "https://live.bilibili.com" {
		t.Fatalf("Origin = %q", got)
	}
	if got := headers.Get("Referer"); got != "https://live.bilibili.com/123" {
		t.Fatalf("Referer = %q", got)
	}
}

func TestResolveDanmakuIdentityAndWebSocketHeaders(t *testing.T) {
	requests := 0
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests++
		if got := r.Header.Get("Cookie"); got != "SESSDATA=sess; bili_jct=jct" {
			t.Fatalf("identity request Cookie = %q", got)
		}
		var body string
		switch r.URL.Path {
		case "/x/web-interface/nav":
			body = `{"code":0,"data":{"isLogin":true,"mid":12345}}`
		case "/x/frontend/finger/spi":
			body = `{"code":0,"data":{"b_3":"device-id"}}`
		default:
			return nil, fmt.Errorf("unexpected path %s", r.URL.Path)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})
	client := NewClient(&http.Client{Transport: transport})
	identity, err := client.resolveDanmakuIdentity(context.Background(), "sess", "jct")
	if err != nil {
		t.Fatalf("resolveDanmakuIdentity() error = %v", err)
	}
	if identity.UID != 12345 || identity.Buvid != "device-id" {
		t.Fatalf("identity = %#v", identity)
	}
	if _, err := client.resolveDanmakuIdentity(context.Background(), "sess", "jct"); err != nil {
		t.Fatalf("cached resolveDanmakuIdentity() error = %v", err)
	}
	if requests != 2 {
		t.Fatalf("identity request count = %d, want 2 with cache hit", requests)
	}

	headers := danmakuWebSocketHeaders("123", "sess", "jct", identity)
	if got := headers.Get("Cookie"); got != "SESSDATA=sess; bili_jct=jct; DedeUserID=12345; buvid3=device-id" {
		t.Fatalf("websocket Cookie = %q", got)
	}
	var payload struct {
		UID      int64  `json:"uid"`
		Buvid    string `json:"buvid"`
		Protover int    `json:"protover"`
	}
	if err := json.Unmarshal(danmakuAuthPayload("token", "123", identity), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.UID != identity.UID || payload.Buvid != identity.Buvid || payload.Protover != 3 {
		t.Fatalf("auth payload identity = %#v", payload)
	}
}

func TestParseDanmakuMessageGiftAndOnline(t *testing.T) {
	danmu := makeDanmakuPacket(danmakuOperationCommand, danmakuProtocolPlain, []byte(`{"cmd":"DANMU_MSG","info":[[0,0,0],"你好",[100,"小兔子"],[6,"草莓"]]}`))
	gift := makeDanmakuPacket(danmakuOperationCommand, danmakuProtocolPlain, []byte(`{"cmd":"SEND_GIFT","data":{"uid":100,"uname":"小兔子","giftName":"小花花","num":3}}`))
	onlineBody := make([]byte, 4)
	binary.BigEndian.PutUint32(onlineBody, 88)
	online := makeDanmakuPacket(danmakuOperationOnline, danmakuProtocolHeartbeat, onlineBody)
	events, err := parseDanmakuPackets(append(append(danmu, gift...), online...))
	if err != nil {
		t.Fatalf("parseDanmakuPackets() error = %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("event count = %d, want 3", len(events))
	}
	if events[0].Kind != DanmakuEventMessage || events[0].Message.Username != "小兔子" || events[0].Message.MedalName != "草莓" {
		t.Fatalf("ordinary event = %#v", events[0])
	}
	if events[1].Kind != DanmakuEventGift || events[1].Message.GiftCount != 3 || !strings.Contains(events[1].Message.Text, "小花花 ×3") {
		t.Fatalf("gift event = %#v", events[1])
	}
	var stats LiveSessionStats
	stats.Observe(events[1])
	if stats.GiftEvents != 1 || stats.GiftCount != 3 {
		t.Fatalf("gift stats = %#v", stats)
	}
	if events[2].Kind != DanmakuEventOnline || events[2].Online != 88 {
		t.Fatalf("online event = %#v", events[2])
	}
}

func TestLiveSessionStatsObserveGift(t *testing.T) {
	var stats LiveSessionStats
	stats.Observe(DanmakuEvent{Kind: DanmakuEventGift, Message: DanmakuMessage{
		GiftCount: 3,
	}})
	stats.Observe(DanmakuEvent{Kind: DanmakuEventGift, Message: DanmakuMessage{
		GiftCount: 1,
	}})
	if stats.GiftEvents != 2 || stats.GiftCount != 4 {
		t.Fatalf("gift stats = %#v", stats)
	}
}

func TestParseLikeAndUnknownDanmakuCommands(t *testing.T) {
	like := makeDanmakuPacket(danmakuOperationCommand, danmakuProtocolPlain, []byte(`{"cmd":"LIKE_INFO_V3_CLICK","data":{"uid":7,"uname":"点赞用户","click_count":3}}`))
	unknown := makeDanmakuPacket(danmakuOperationCommand, danmakuProtocolPlain, []byte(`{"cmd":"ROOM_CHANGE","data":{"title":"新标题"}}`))
	events, err := parseDanmakuPackets(append(like, unknown...))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Kind != DanmakuEventSystem || !strings.Contains(events[0].Message.Text, "×3") {
		t.Fatalf("like events = %#v", events)
	}
}

func TestParseGiftAcceptsStringNumbers(t *testing.T) {
	packet := makeDanmakuPacket(danmakuOperationCommand, danmakuProtocolPlain, []byte(`{"cmd":"SEND_GIFT","data":{"uid":"7","uname":"送礼用户","giftName":"小花花","num":"2","total_coin":"100","coin_type":"gold","combo_num":"2"}}`))
	events, err := parseDanmakuPackets(packet)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Kind != DanmakuEventGift {
		t.Fatalf("gift events = %#v", events)
	}
	message := events[0].Message
	if message.GiftCount != 2 || message.UserID != "7" {
		t.Fatalf("gift message = %#v", message)
	}
}

func TestParseSystemInteractionFiltersOrdinaryEntry(t *testing.T) {
	entry := makeDanmakuPacket(danmakuOperationCommand, danmakuProtocolPlain, []byte(`{"cmd":"INTERACT_WORD","data":{"uid":100,"uname":"路人","msg_type":1}}`))
	follow := makeDanmakuPacket(danmakuOperationCommand, danmakuProtocolPlain, []byte(`{"cmd":"INTERACT_WORD","data":{"uid":101,"uname":"新粉丝","msg_type":2}}`))
	events, err := parseDanmakuPackets(append(entry, follow...))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Message.Username != "新粉丝" || events[0].Message.Text != "关注了直播间" {
		t.Fatalf("filtered interaction events = %#v", events)
	}
}

func TestParseCompressedDanmakuPacket(t *testing.T) {
	nested := makeDanmakuPacket(danmakuOperationCommand, danmakuProtocolPlain, []byte(`{"cmd":"DANMU_MSG","info":[[0,0,0],"Zlib 弹幕",[7,"莓莓"],[]]}`))
	var compressed bytes.Buffer
	writer := zlib.NewWriter(&compressed)
	_, _ = writer.Write(nested)
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	events, err := parseDanmakuPackets(makeDanmakuPacket(danmakuOperationCommand, danmakuProtocolZlib, compressed.Bytes()))
	if err != nil {
		t.Fatalf("parseDanmakuPackets() error = %v", err)
	}
	if len(events) != 1 || events[0].Kind != DanmakuEventMessage || events[0].Message.Text != "Zlib 弹幕" {
		t.Fatalf("events = %#v", events)
	}
}

func TestParseBrotliDanmakuPacket(t *testing.T) {
	nested := makeDanmakuPacket(danmakuOperationCommand, danmakuProtocolPlain, []byte(`{"cmd":"DANMU_MSG","info":[[0,0,0],"Brotli 弹幕",[100,"测试用户"],[6,"草莓"]]}`))
	var compressed bytes.Buffer
	writer := brotli.NewWriter(&compressed)
	if _, err := writer.Write(nested); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	events, err := parseDanmakuPackets(makeDanmakuPacket(danmakuOperationCommand, danmakuProtocolBrotli, compressed.Bytes()))
	if err != nil {
		t.Fatalf("parseDanmakuPackets() error = %v", err)
	}
	if len(events) != 1 || events[0].Kind != DanmakuEventMessage || events[0].Message.Text != "Brotli 弹幕" {
		t.Fatalf("events = %#v", events)
	}
}

func TestReadDanmakuDecodedRejectsOversizedPayload(t *testing.T) {
	if _, err := readDanmakuDecodedLimit(strings.NewReader("12345"), 4); err == nil || !strings.Contains(err.Error(), "大小限制") {
		t.Fatalf("oversized decoded payload error = %v", err)
	}
}

func TestParseDanmakuPacketsRejectsExcessiveCompressionNesting(t *testing.T) {
	packet := makeDanmakuPacket(danmakuOperationCommand, danmakuProtocolPlain, []byte(`{"cmd":"ROOM_CHANGE"}`))
	for range danmakuPacketNestingLimit + 1 {
		var compressed bytes.Buffer
		writer := zlib.NewWriter(&compressed)
		_, _ = writer.Write(packet)
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		packet = makeDanmakuPacket(danmakuOperationCommand, danmakuProtocolZlib, compressed.Bytes())
	}
	if _, err := parseDanmakuPackets(packet); err == nil || !strings.Contains(err.Error(), "嵌套层数") {
		t.Fatalf("nested packet error = %v", err)
	}
}

func TestSendDanmaku(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if r.Header.Get("Cookie") != "SESSDATA=sess; bili_jct=csrf; DedeUserID=12345; buvid3=device-id" {
			t.Fatalf("cookie = %q", r.Header.Get("Cookie"))
		}
		_ = r.ParseForm()
		if got := r.PostForm.Get("msg"); got != "测试弹幕" {
			t.Fatalf("msg = %q", got)
		}
		if got := r.PostForm.Get("roomid"); got != "123" {
			t.Fatalf("roomid = %q", got)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"code":0,"message":"0"}`)), Header: make(http.Header)}, nil
	})
	client := NewClient(&http.Client{Transport: transport})
	client.BaseURL = "http://test.invalid"
	seedDanmakuSendIdentity(client, "sess")
	if err := client.SendDanmaku(context.Background(), "123", "sess", "csrf", "测试弹幕"); err != nil {
		t.Fatalf("SendDanmaku() error = %v", err)
	}
	if err := client.SendDanmaku(context.Background(), "123", "sess", "csrf", strings.Repeat("啊", DefaultDanmakuMaxLength+1)); err == nil {
		t.Fatal("expected length validation error")
	}
}

func TestGetDanmakuMaxLength(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if got := r.URL.Path; got != "/xlive/web-room/v1/index/getInfoByUser" {
			t.Fatalf("path = %q", got)
		}
		if got := r.URL.Query().Get("room_id"); got != "123" {
			t.Fatalf("room_id = %q", got)
		}
		if got := r.URL.Query().Get("from"); got != "0" {
			t.Fatalf("from = %q", got)
		}
		if got := r.Header.Get("Cookie"); got != "SESSDATA=sess; bili_jct=csrf" {
			t.Fatalf("cookie = %q", got)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"code":0,"data":{"property":{"danmu":{"length":"30"}}}}`)),
			Header:     make(http.Header),
		}, nil
	})
	client := NewClient(&http.Client{Transport: transport})
	client.BaseURL = "http://test.invalid"
	limit, err := client.GetDanmakuMaxLength(context.Background(), "123", "sess", "csrf")
	if err != nil {
		t.Fatalf("GetDanmakuMaxLength() error = %v", err)
	}
	if limit != 30 {
		t.Fatalf("GetDanmakuMaxLength() = %d, want 30", limit)
	}
}

func TestSendDanmakuUsesUpstreamLimit(t *testing.T) {
	transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"code":0}`)), Header: make(http.Header)}, nil
	})
	client := NewClient(&http.Client{Transport: transport})
	client.BaseURL = "http://test.invalid"
	seedDanmakuSendIdentity(client, "sess")
	message := strings.Repeat("啊", DefaultDanmakuMaxLength+1)
	if err := client.SendDanmakuWithLimit(context.Background(), "123", "sess", "csrf", message, 50); err != nil {
		t.Fatalf("SendDanmakuWithLimit() error = %v", err)
	}
}

func TestSendDanmakuReportsMsgFallback(t *testing.T) {
	transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"code":10031,"message":"","msg":"发送频率过快"}`)),
			Header:     make(http.Header),
		}, nil
	})
	client := NewClient(&http.Client{Transport: transport})
	client.BaseURL = "http://test.invalid"
	seedDanmakuSendIdentity(client, "sess")
	err := client.SendDanmaku(context.Background(), "123", "sess", "csrf", "测试弹幕")
	if err == nil || !strings.Contains(err.Error(), "10031") || !strings.Contains(err.Error(), "发送频率过快") {
		t.Fatalf("SendDanmaku() error = %v, want server code and msg", err)
	}
}

func TestSendDanmakuRejectsSoftBlockedMessage(t *testing.T) {
	transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"code":0,"message":"f","msg":"f","data":[]}`)),
			Header:     make(http.Header),
		}, nil
	})
	client := NewClient(&http.Client{Transport: transport})
	client.BaseURL = "http://test.invalid"
	seedDanmakuSendIdentity(client, "sess")
	err := client.SendDanmaku(context.Background(), "123", "sess", "csrf", "违禁词弹幕")
	if err == nil || !strings.Contains(err.Error(), "拦截") || !strings.Contains(err.Error(), "违禁词") {
		t.Fatalf("SendDanmaku() error = %v, want soft-block error", err)
	}
}

func seedDanmakuSendIdentity(client *Client, sessdata string) {
	client.danmakuIdentity = danmakuIdentity{UID: 12345, Buvid: "device-id"}
	client.danmakuIdentityFor = sessdata
	client.danmakuIdentityAt = time.Now()
}

func makeDanmakuPacket(operation uint32, version uint16, body []byte) []byte {
	packet := make([]byte, danmakuHeaderLength+len(body))
	binary.BigEndian.PutUint32(packet[0:4], uint32(len(packet)))
	binary.BigEndian.PutUint16(packet[4:6], danmakuHeaderLength)
	binary.BigEndian.PutUint16(packet[6:8], version)
	binary.BigEndian.PutUint32(packet[8:12], operation)
	binary.BigEndian.PutUint32(packet[12:16], 1)
	copy(packet[danmakuHeaderLength:], body)
	return packet
}

func TestParseDanmakuMessageIncludesStructuredUserDetails(t *testing.T) {
	metadata := make([]any, 16)
	metadata[15] = map[string]any{
		"user": map[string]any{
			"uid":    "42",
			"base":   map[string]any{"name": "新版用户", "is_mystery": true},
			"medal":  map[string]any{"name": "草莓", "level": 12, "guard_level": 3},
			"wealth": map[string]any{"level": 8},
		},
	}
	info := []any{metadata, "测试弹幕", []any{7, "旧用户名", 1}, []any{6, "旧粉丝牌"}, []any{5}}
	raw, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	event, ok, err := parseDanmuMessage(raw, "DANMU_MSG")
	if err != nil || !ok {
		t.Fatalf("parseDanmuMessage() = (%#v, %v, %v)", event, ok, err)
	}
	message := event.Message
	if message.UserID != "42" || message.Username != "新版用户" || !message.IsAdmin || !message.IsMystery {
		t.Fatalf("structured identity = %#v", message)
	}
	if message.MedalName != "草莓" || message.MedalLevel != 12 || message.GuardLevel != 3 || message.UserLevel != 5 || message.WealthLevel != 8 {
		t.Fatalf("structured badges = %#v", message)
	}
}

func TestParseGiftIncludesUserDetails(t *testing.T) {
	raw := json.RawMessage(`{"uid":42,"uname":"送礼用户","giftName":"小花花","num":2,"wealth_level":7,"medal_info":{"medal_name":"草莓","medal_level":9,"guard_level":2}}`)
	event, ok, err := parseGift(raw, "SEND_GIFT")
	if err != nil || !ok {
		t.Fatalf("parseGift() = (%#v, %v, %v)", event, ok, err)
	}
	message := event.Message
	if message.MedalName != "草莓" || message.MedalLevel != 9 || message.GuardLevel != 2 || message.WealthLevel != 7 {
		t.Fatalf("gift user details = %#v", message)
	}
}

func TestGetUserProfileParsesPublicCard(t *testing.T) {
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/x/web-interface/card" || request.URL.Query().Get("mid") != "42" {
			t.Fatalf("user card request = %s", request.URL.String())
		}
		if got := request.Header.Get("Referer"); got != "https://space.bilibili.com/42/" {
			t.Fatalf("referer = %q", got)
		}
		body := `{"code":0,"data":{"card":{"mid":"42","name":"用户甲","sign":"一个签名","fans":123,"attention":45,"level_info":{"current_level":6},"Official":{"title":"官方认证"},"vip":{"status":1,"label":{"text":"年度大会员"}}},"following":true,"archive_count":7,"article_count":2}}`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})
	client := NewClient(&http.Client{Transport: transport})
	profile, err := client.GetUserProfile(context.Background(), "42", "", "")
	if err != nil {
		t.Fatalf("GetUserProfile() error = %v", err)
	}
	if profile.UserID != "42" || profile.Username != "用户甲" || profile.Signature != "一个签名" || profile.Level != 6 {
		t.Fatalf("profile identity = %#v", profile)
	}
	if profile.Followers != 123 || profile.Following != 45 || profile.ArchiveCount != 7 || profile.ArticleCount != 2 || !profile.IsFollowing {
		t.Fatalf("profile counts = %#v", profile)
	}
	if !profile.VIP || profile.VIPLabel != "年度大会员" || profile.Official != "官方认证" {
		t.Fatalf("profile badges = %#v", profile)
	}
}

func TestGetUserProfileReportsRiskControl(t *testing.T) {
	client := NewClient(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"code":-352,"message":"风控校验失败"}`)), Header: make(http.Header)}, nil
	})})
	_, err := client.GetUserProfile(context.Background(), "42", "", "")
	if err == nil || !strings.Contains(err.Error(), "-352") || !strings.Contains(err.Error(), "风控") {
		t.Fatalf("risk-control error = %v", err)
	}
}

func TestGetUserProfileMarksCurrentAccount(t *testing.T) {
	client := NewClient(&http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/x/web-interface/card" {
			t.Fatalf("unexpected profile request = %s", request.URL.String())
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"code":0,"data":{"card":{"mid":"42","name":"本人"}}}`)), Header: make(http.Header)}, nil
	})})
	client.danmakuIdentity = danmakuIdentity{UID: 42, Buvid: "device-id"}
	client.danmakuIdentityFor = "sess"
	client.danmakuIdentityAt = time.Now()

	profile, err := client.GetUserProfile(context.Background(), "42", "sess", "jct")
	if err != nil {
		t.Fatalf("GetUserProfile() error = %v", err)
	}
	if !profile.IsSelf {
		t.Fatalf("current account profile = %#v, want IsSelf", profile)
	}
}

func TestGetUserProfileRejectsInvalidUIDWithoutRequest(t *testing.T) {
	client := NewClient(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid UID triggered a network request")
		return nil, nil
	})})
	if _, err := client.GetUserProfile(context.Background(), "not-a-uid", "", ""); err == nil {
		t.Fatal("invalid UID was accepted")
	}
}

func TestSetUserFollowingUsesWebRelationAction(t *testing.T) {
	for _, test := range []struct {
		following bool
		wantAct   string
	}{{following: true, wantAct: "1"}, {following: false, wantAct: "2"}} {
		transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
			if request.URL.Path != "/x/relation/modify" {
				t.Fatalf("relation path = %q", request.URL.Path)
			}
			if err := request.ParseForm(); err != nil {
				return nil, err
			}
			if request.PostForm.Get("fid") != "42" || request.PostForm.Get("act") != test.wantAct || request.PostForm.Get("csrf") != "jct" {
				t.Fatalf("relation form = %#v", request.PostForm)
			}
			if got := request.Header.Get("Cookie"); got != "SESSDATA=sess; bili_jct=jct" {
				t.Fatalf("relation cookie = %q", got)
			}
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"code":0}`)), Header: make(http.Header)}, nil
		})
		client := NewClient(&http.Client{Transport: transport})
		if err := client.SetUserFollowing(context.Background(), "42", "sess", "jct", test.following); err != nil {
			t.Fatalf("SetUserFollowing(%v) error = %v", test.following, err)
		}
	}
}

func TestSetUserFollowingKeepsServerError(t *testing.T) {
	client := NewClient(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"code":22014,"message":"已经关注"}`)), Header: make(http.Header)}, nil
	})})
	err := client.SetUserFollowing(context.Background(), "42", "sess", "jct", true)
	if err == nil || !strings.Contains(err.Error(), "22014") || !strings.Contains(err.Error(), "已经关注") {
		t.Fatalf("relation error = %v", err)
	}
}
