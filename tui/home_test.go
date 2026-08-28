package tui

import (
	"strings"
	"testing"
	"time"

	"bili-live-tui/internal/api"
	streamruntime "bili-live-tui/internal/stream"
)

func TestLiveInfoSummaryUsesReadableAreaAndKnownMetrics(t *testing.T) {
	summary := liveInfoSummary("123", api.LiveSettings{
		Title:     "测试直播",
		AreaID:    "376",
		CoverPath: "https://i.example/cover.jpg",
	}, []api.LiveArea{{ID: "376", Name: "单机游戏", ParentName: "主机游戏"}}, &api.RoomSnapshot{
		Online:       42,
		OnlineKnown:  true,
		Watched:      1234,
		WatchedKnown: true,
	})
	if !strings.Contains(summary, "主机游戏 / 单机游戏") {
		t.Fatalf("summary does not contain readable area: %s", summary)
	}
	if strings.Contains(summary, "分区[-]　376") {
		t.Fatalf("summary still displays numeric area ID: %s", summary)
	}
	if !strings.Contains(summary, "当前人气[-]　42") || !strings.Contains(summary, "累计观看[-]　1234") {
		t.Fatalf("summary does not contain known metrics: %s", summary)
	}
}

func TestFormatLiveDuration(t *testing.T) {
	if got, want := formatLiveDuration(1*time.Hour+2*time.Minute+3*time.Second), "01:02:03"; got != want {
		t.Fatalf("formatLiveDuration() = %q, want %q", got, want)
	}
}

func TestLiveInfoSummaryShowsSessionGiftStats(t *testing.T) {
	summary := liveInfoSummaryWithStats("123", api.LiveSettings{
		Title:  "测试直播",
		AreaID: "376",
	}, nil, nil, &api.LiveSessionStats{GiftEvents: 2, GiftCount: 5})
	if !strings.Contains(summary, "本场礼物[-]　2 次 / 共 5 个") {
		t.Fatalf("summary does not contain session gift stats: %s", summary)
	}
}

func TestLiveInfoSummaryPrefersSessionPopularity(t *testing.T) {
	summary := liveInfoSummaryWithStats("123", api.LiveSettings{Title: "标题", AreaID: "1"}, nil, &api.RoomSnapshot{Online: 42, OnlineKnown: true}, &api.LiveSessionStats{Popularity: 88, PopularityKnown: true})
	if !strings.Contains(summary, "当前人气[-]　88") || strings.Contains(summary, "当前人气[-]　42") {
		t.Fatalf("summary = %q, want session popularity 88", summary)
	}
}

func TestFormatStreamHealth(t *testing.T) {
	text := formatStreamHealth(streamruntime.Health{
		Mode:          streamruntime.ModeOBS,
		Active:        true,
		FPS:           60,
		BitrateKbps:   4500,
		SkippedFrames: 3,
		TotalFrames:   1200,
		CPUPercent:    8.5,
	})
	for _, want := range []string{"OBS", "4500 kbps", "60.0 FPS", "掉帧 3 帧（0.25%）", "CPU 8.5%"} {
		if !strings.Contains(text, want) {
			t.Fatalf("health text %q missing %q", text, want)
		}
	}
	errorText := formatStreamHealth(streamruntime.Health{Mode: streamruntime.ModeOBS, LastError: "连接断开"})
	if !strings.Contains(errorText, "OBS异常") || !strings.Contains(errorText, "连接断开") {
		t.Fatalf("error health text = %q", errorText)
	}
	cleanText := formatStreamHealth(streamruntime.Health{Mode: streamruntime.ModeOBS, Active: true, TotalFrames: 1200})
	if strings.Contains(cleanText, "掉帧") {
		t.Fatalf("zero dropped frames should stay hidden: %q", cleanText)
	}
}
