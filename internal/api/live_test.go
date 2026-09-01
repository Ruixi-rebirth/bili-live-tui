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
	for _, name := range []string{"GetMyRoomID", "GetRoomSnapshot", "GetOnlineGoldRank", "GetRoomPlaybackURL", "GetDanmakuInfo", "GetDanmakuInfoLegacy", "SendDanmaku", "GetLiveAreas", "UploadRoomCover", "AddLiveTag", "DeleteLiveTag", "UpdateRoomNews", "UpdatePreLiveInfo", "UpdateLiveInfo", "StartLive", "StopLive", "GetTVQRCode", "CheckQRStatus"} {
		endpoint, ok := EndpointByName(name)
		if !ok || endpoint.Path == "" || endpoint.Method == "" {
			t.Fatalf("endpoint %q missing from catalog", name)
		}
	}
	if _, exists := EndpointByName("KeepAlive"); exists {
		t.Fatal("viewer mobile heartbeat must not be registered as a broadcaster endpoint")
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
			Body:       io.NopCloser(strings.NewReader(`{"code":0,"data":{"room_info":{"room_id":1,"title":"标题","description":"简介","tags":"游戏,聊天","area_name":"单机游戏","parent_area_name":"主机游戏","live_status":1,"online":"42"},"watched_show":{"num":1234}}}`)),
			Header:     make(http.Header),
		}, nil
	})
	client := NewClient(&http.Client{Transport: transport})
	client.BaseURL = "http://test.invalid"
	snapshot, err := client.GetRoomSnapshot(context.Background(), "1")
	if err != nil {
		t.Fatalf("GetRoomSnapshot() error = %v", err)
	}
	if snapshot.RoomID != "1" || snapshot.AreaName != "单机游戏" || snapshot.Online != 42 || !snapshot.OnlineKnown || snapshot.Watched != 1234 || !snapshot.WatchedKnown {
		t.Fatalf("GetRoomSnapshot() = %#v", snapshot)
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
			var data bytes.Buffer
			coverImage := image.NewRGBA(image.Rect(0, 0, 640, 360))
			if err := jpeg.Encode(&data, coverImage, nil); err != nil {
				return nil, err
			}
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(data.Bytes())), Header: http.Header{"Content-Type": []string{"image/jpeg"}}}, nil
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
	got, err := client.UploadRoomCoverURL(context.Background(), "1", "sess", "jct", "https://apis.example/cover.jpg")
	if err != nil {
		t.Fatalf("UploadRoomCoverURL() error = %v", err)
	}
	if !downloaded.Load() || got != "https://i.example/cover.jpg" {
		t.Fatalf("UploadRoomCoverURL() downloaded=%v url=%q", downloaded.Load(), got)
	}
}

func TestNormalizeCoverForUploadUpscalesAndEncodesJPEG(t *testing.T) {
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
	data, contentType, width, height, err := normalizeCoverForUpload(temp)
	if err != nil {
		t.Fatal(err)
	}
	if contentType != "image/jpeg" || width != 640 || height != 547 {
		t.Fatalf("normalized cover = %s %dx%d", contentType, width, height)
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || format != "jpeg" || config.Width != width || config.Height != height {
		t.Fatalf("encoded cover = %s %dx%d, err=%v", format, config.Width, config.Height, err)
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
	_, _, _, _, err = normalizeCoverForUpload(temp)
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
