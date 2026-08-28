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
// 实例只允许启动一次。启动失败、主动停止或意外停止均关闭 Done。
// Stop 可在 Start 之前调用，也允许重复调用。
type Runtime interface {
	Start(rtmpAddr, streamKey string) error
	Health() Health
	Done() <-chan struct{}
	Stop() error
}
