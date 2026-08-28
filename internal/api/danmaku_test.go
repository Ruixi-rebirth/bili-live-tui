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
		UID   int64  `json:"uid"`
		Buvid string `json:"buvid"`
	}
	if err := json.Unmarshal(danmakuAuthPayload("token", "123", identity), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.UID != identity.UID || payload.Buvid != identity.Buvid {
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
	nested := makeDanmakuPacket(danmakuOperationCommand, danmakuProtocolPlain, []byte(`{"cmd":"SUPER_CHAT_MESSAGE","data":{"user_info":{"uid":7,"uname":"莓莓"},"message":"晚上好","price":30}}`))
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
	if len(events) != 1 || events[0].Kind != DanmakuEventSuperChat || !strings.Contains(events[0].Message.Text, "¥30") {
		t.Fatalf("events = %#v", events)
	}
}

func TestSendDanmaku(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if !strings.Contains(r.Header.Get("Cookie"), "SESSDATA=sess") {
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
	if err := client.SendDanmaku(context.Background(), "123", "sess", "csrf", "测试弹幕"); err != nil {
		t.Fatalf("SendDanmaku() error = %v", err)
	}
	if err := client.SendDanmaku(context.Background(), "123", "sess", "csrf", strings.Repeat("啊", 81)); err == nil {
		t.Fatal("expected length validation error")
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
	err := client.SendDanmaku(context.Background(), "123", "sess", "csrf", "测试弹幕")
	if err == nil || !strings.Contains(err.Error(), "10031") || !strings.Contains(err.Error(), "发送频率过快") {
		t.Fatalf("SendDanmaku() error = %v, want server code and msg", err)
	}
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
