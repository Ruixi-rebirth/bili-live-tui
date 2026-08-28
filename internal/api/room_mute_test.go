package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

func TestMutedUsersUsesWebPageParameter(t *testing.T) {
	for _, page := range []int{-1, 0, 1, 2, 20} {
		t.Run(strconv.Itoa(page), func(t *testing.T) {
			wantPage := max(page, 1)
			client := NewClient(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.Method != http.MethodPost || req.URL.Path != "/xlive/web-ucenter/v1/banned/GetSilentUserList" {
					t.Fatalf("unexpected request: %s %s", req.Method, req.URL.Path)
				}
				if err := req.ParseForm(); err != nil {
					t.Fatal(err)
				}
				if req.PostForm.Get("ps") != strconv.Itoa(wantPage) || req.PostForm.Has("pn") {
					t.Fatalf("Web uses ps as page, got %v", req.PostForm)
				}
				if req.PostForm.Get("room_id") != "123" || req.PostForm.Get("csrf") != "jct" || req.PostForm.Get("csrf_token") != "jct" {
					t.Fatalf("missing room or CSRF: %v", req.PostForm)
				}
				// 每页返回不同用户，防止 UI 看似翻页，实际一直读取同一页。
				body := fmt.Sprintf(`{"code":0,"data":{"data":[{"tuid":%d,"tname":"用户","name":"房管","block_end_time":"2026-09-10 12:00:00","is_anchor":0}],"total":20,"total_page":20}}`, wantPage)
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
			})})
			result, err := client.GetMutedRoomUsers(context.Background(), "123", page, "sess", "jct")
			if err != nil {
				t.Fatal(err)
			}
			if result.Page != wantPage || len(result.Items) != 1 || result.Items[0].UserID != strconv.Itoa(wantPage) || result.Items[0].ExpiresAt != "2026-09-10 12:00:00" {
				t.Fatalf("unexpected result: %#v", result)
			}
		})
	}
}

func TestMutedUsersEmptyAndFailedResponses(t *testing.T) {
	for _, tc := range []struct {
		name      string
		body      string
		wantError bool
	}{
		{"empty array", `{"code":0,"data":{"data":[],"total":0,"total_page":0}}`, false},
		{"empty null", `{"code":0,"data":{"data":null,"total":0}}`, false},
		{"denied", `{"code":-403,"message":"无权限"}`, true},
		{"malformed", `{"code":0,"data":{"data":"unexpected"}}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := NewClient(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})})
			result, err := client.GetMutedRoomUsers(context.Background(), "123", 1, "sess", "jct")
			if (err != nil) != tc.wantError {
				t.Fatalf("error = %v, want error %v", err, tc.wantError)
			}
			if len(result.Items) != 0 {
				t.Fatalf("unexpected items: %#v", result.Items)
			}
		})
	}
}
