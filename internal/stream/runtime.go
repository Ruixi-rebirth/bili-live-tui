package stream

import "time"

const (
	ModeOBS        = "obs"
	ModeFFmpegTest = "ffmpeg-test"
)

// Health 是发送媒体到 RTMP 地址的进程状态摘要，与具体推流源无关。
// 运行时不支持的字段保持为零值。
type Health struct {
	Mode          string
	Active        bool
	Reconnecting  bool
	Duration      time.Duration
	FPS           float64
	CPUPercent    float64
	MemoryMB      float64
	BitrateKbps   float64
	SkippedFrames int64
	TotalFrames   int64
	LastError     string
}

// Runtime 管理一个本地推流输出（OBS 或 FFmpeg 测试源）。
// 无论主动还是意外停止，Done 都会被关闭。
type Runtime interface {
	Start(rtmpAddr, streamKey string) error
	Health() Health
	Done() <-chan struct{}
	Stop() error
}
