package ffmpeg

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	streamruntime "bili-live-tui/internal/stream"
)

func StartTestStream(rtmpAddr, streamKey string, orientation ...string) (*exec.Cmd, error) {
	// B 站的完整推流地址是 Addr + Code 拼接而成
	fullURL := rtmpAddr + streamKey
	direction := ""
	if len(orientation) > 0 {
		direction = orientation[0]
	}
	args := append(testSourceArgs(direction),
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-b:a", "128k", "-f", "flv", fullURL,
	)
	cmd := exec.Command("ffmpeg", args...)

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("启动 ffmpeg 失败: %v", err)
	}

	return cmd, nil
}

// testSourceArgs 生成带网格、色彩变化和移动方块的无依赖测试画面。
func testSourceArgs(orientation string) []string {
	size := "1280x720"
	filter := "drawgrid=width=160:height=90:thickness=2:color=white@0.18,drawbox=x='mod(180*t,1100)':y='90+45*sin(2*PI*t/3)':w=150:h=150:color=0xff4f9dff@0.82:t=fill,drawbox=x='1100-mod(140*t,1100)':y='470+35*sin(2*PI*t/4)':w=210:h=100:color=0xff6bd6a8@0.82:t=fill,drawbox=x='540+90*sin(2*PI*t/5)':y='285+55*cos(2*PI*t/5)':w=200:h=150:color=0xffff6b9d@0.78:t=fill"
	if orientation == streamruntimeOrientationPortrait {
		size = "720x1280"
		filter = "drawgrid=width=90:height=160:thickness=2:color=white@0.18,drawbox=x='mod(90*t,570)':y='180+80*sin(2*PI*t/3)':w=150:h=150:color=0xff4f9dff@0.82:t=fill,drawbox=x='500-mod(70*t,500)':y='820+35*sin(2*PI*t/4)':w=180:h=120:color=0xff6bd6a8@0.82:t=fill,drawbox=x='260+90*sin(2*PI*t/5)':y='560+55*cos(2*PI*t/5)':w=180:h=150:color=0xffff6b9d@0.78:t=fill"
	}
	return []string{
		"-re",
		"-f", "lavfi", "-i", "testsrc2=size=" + size + ":rate=30,hue=h=45*sin(2*PI*t/8)," + filter,
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=44100",
	}
}

func StopStream(cmd *exec.Cmd) error {
	if cmd != nil && cmd.Process != nil {
		return cmd.Process.Kill()
	}
	return nil
}

// TestRuntime 管理用于完整测试 RTMP 链路的 FFmpeg 合成源，不依赖 OBS。
// 正式推流默认使用 OBS，因此该运行时只在设置表单中明确选择后启用。
type TestRuntime struct {
	mu          sync.RWMutex
	cmd         *exec.Cmd
	rtmpAddr    string
	streamKey   string
	orientation string
	health      streamruntime.Health
	done        chan struct{}
	doneOnce    sync.Once
	stopOnce    sync.Once
	stopCh      chan struct{}
	stopping    bool
	lastLog     string
}

var ffmpegStreamURLPattern = regexp.MustCompile(`(?i)rtmps?://\S+`)

const streamruntimeOrientationPortrait = "portrait"

func NewTestRuntime(orientation ...string) *TestRuntime {
	direction := ""
	if len(orientation) > 0 {
		direction = orientation[0]
	}
	return &TestRuntime{
		health:      streamruntime.Health{Mode: streamruntime.ModeFFmpegTest},
		orientation: direction,
		done:        make(chan struct{}),
		stopCh:      make(chan struct{}),
	}
}

func (r *TestRuntime) Start(rtmpAddr, streamKey string) error {
	cmd, progress, err := newFFmpegProcess(rtmpAddr, streamKey, r.orientation)
	if err != nil {
		r.mu.Lock()
		r.health.Active = false
		r.health.Reconnecting = false
		r.health.LastError = err.Error()
		r.mu.Unlock()
		r.finish()
		return err
	}
	r.mu.Lock()
	if r.stopping {
		r.mu.Unlock()
		stopFFmpegProcess(cmd, progress)
		return fmt.Errorf("FFmpeg 测试源已停止")
	}
	r.cmd = cmd
	r.rtmpAddr = rtmpAddr
	r.streamKey = streamKey
	r.health.Active = true
	r.health.Reconnecting = false
	r.health.LastError = ""
	r.mu.Unlock()
	go r.run(cmd, progress)
	return nil
}

func newFFmpegProcess(rtmpAddr, streamKey, orientation string) (*exec.Cmd, io.ReadCloser, error) {
	args := []string{"-hide_banner", "-nostats", "-progress", "pipe:2"}
	args = append(args, testSourceArgs(orientation)...)
	args = append(args,
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-b:a", "128k", "-f", "flv", rtmpAddr+streamKey,
	)
	cmd := exec.Command("ffmpeg", args...)
	progress, err := cmd.StderrPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("读取 FFmpeg 状态失败: %w", err)
	}
	if err := cmd.Start(); err != nil {
		_ = progress.Close()
		return nil, nil, fmt.Errorf("启动 FFmpeg 测试源失败: %w", err)
	}
	return cmd, progress, nil
}

func (r *TestRuntime) run(cmd *exec.Cmd, progress io.ReadCloser) {
	r.mu.RLock()
	stopping := r.stopping
	r.mu.RUnlock()
	if stopping {
		r.mu.Lock()
		r.health.Active = false
		r.health.Reconnecting = false
		r.mu.Unlock()
		stopFFmpegProcess(cmd, progress)
		r.finish()
		return
	}
	progressDone := make(chan struct{})
	go func() {
		r.readProgress(progress)
		close(progressDone)
	}()
	err := cmd.Wait()
	<-progressDone
	_ = progress.Close()

	r.mu.Lock()
	if r.stopping {
		r.health.Active = false
		r.health.Reconnecting = false
		r.mu.Unlock()
		r.finish()
		return
	}
	detail := r.lastLog
	if err != nil {
		detail = "FFmpeg 测试源已退出：" + err.Error() + "；" + detail
	}
	r.health.Active = false
	r.health.Reconnecting = true
	r.health.LastError = strings.TrimSuffix(detail, "；")
	r.mu.Unlock()

	deadline := time.Now().Add(60 * time.Second)
	delays := []time.Duration{2 * time.Second, 5 * time.Second, 10 * time.Second, 15 * time.Second, 15 * time.Second, 13 * time.Second}
	for _, delay := range delays {
		if time.Now().After(deadline) {
			break
		}
		timer := time.NewTimer(delay)
		select {
		case <-r.stopCh:
			if !timer.Stop() {
				<-timer.C
			}
			r.finish()
			return
		case <-timer.C:
		}
		newCmd, newProgress, startErr := newFFmpegProcess(r.rtmpAddr, r.streamKey, r.orientation)
		if startErr == nil {
			r.mu.Lock()
			if r.stopping {
				r.mu.Unlock()
				stopFFmpegProcess(newCmd, newProgress)
				r.finish()
				return
			}
			r.cmd = newCmd
			r.health.Active = true
			r.health.Reconnecting = false
			r.health.LastError = ""
			r.lastLog = ""
			r.mu.Unlock()
			r.run(newCmd, newProgress)
			return
		}
		r.mu.Lock()
		r.health.LastError = fmt.Sprintf("等待网络恢复，重连失败：%v", startErr)
		r.mu.Unlock()
	}
	r.mu.Lock()
	r.health.Active = false
	r.health.Reconnecting = false
	r.health.LastError = "等待网络恢复超时：" + strings.TrimPrefix(r.health.LastError, "等待网络恢复，重连失败：")
	r.mu.Unlock()
	r.finish()
}

// stopFFmpegProcess 停止在重连期间创建、但与 Stop 发生竞态的进程。
// 调用方不能在持有 r.mu 时调用此函数。
func stopFFmpegProcess(cmd *exec.Cmd, progress io.ReadCloser) {
	if progress != nil {
		_ = progress.Close()
	}
	if cmd == nil || cmd.Process == nil {
		return
	}
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		_ = cmd.Process.Kill()
	}
	waitDone := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(waitDone)
	}()
	select {
	case <-waitDone:
	case <-time.After(3 * time.Second):
		_ = cmd.Process.Kill()
		<-waitDone
	}
}

func (r *TestRuntime) finish() {
	r.doneOnce.Do(func() { close(r.done) })
}

func (r *TestRuntime) readProgress(progress io.Reader) {
	scanner := bufio.NewScanner(progress)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		key, value, ok := strings.Cut(line, "=")
		r.mu.Lock()
		recognized := ok
		if ok {
			switch key {
			case "frame":
				r.health.TotalFrames, _ = strconv.ParseInt(strings.TrimSpace(value), 10, 64)
			case "fps":
				r.health.FPS, _ = strconv.ParseFloat(strings.TrimSpace(value), 64)
			case "drop_frames":
				r.health.SkippedFrames, _ = strconv.ParseInt(strings.TrimSpace(value), 10, 64)
			case "bitrate":
				value = strings.TrimSpace(strings.TrimSuffix(value, "kbits/s"))
				r.health.BitrateKbps, _ = strconv.ParseFloat(value, 64)
			case "out_time_ms":
				microseconds, _ := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
				r.health.Duration = time.Duration(microseconds) * time.Microsecond
			case "total_size", "out_time_us", "out_time", "dup_frames", "speed", "progress", "stream_0_0_q":
				// 标准 -progress 字段无需显示，但不能误当成错误详情。
			default:
				recognized = false
			}
		}
		if !recognized && line != "" {
			r.lastLog = sanitizeFFmpegLogLine(line)
		}
		r.mu.Unlock()
	}
	if err := scanner.Err(); err != nil {
		r.mu.Lock()
		r.lastLog = "读取 FFmpeg 输出失败：" + err.Error()
		r.mu.Unlock()
	}
}

func sanitizeFFmpegLogLine(line string) string {
	line = ffmpegStreamURLPattern.ReplaceAllString(strings.TrimSpace(line), "rtmp://[REDACTED]")
	const maxLength = 300
	runes := []rune(line)
	if len(runes) > maxLength {
		line = string(runes[:maxLength]) + "…"
	}
	return line
}

func (r *TestRuntime) Health() streamruntime.Health {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.health
}

func (r *TestRuntime) Done() <-chan struct{} { return r.done }

func (r *TestRuntime) Stop() error {
	r.mu.Lock()
	if r.stopping {
		r.mu.Unlock()
		return nil
	}
	r.stopping = true
	process := r.cmd
	r.mu.Unlock()
	r.stopOnce.Do(func() { close(r.stopCh) })
	if process == nil || process.Process == nil {
		select {
		case <-r.done:
			return nil
		case <-time.After(3 * time.Second):
			return fmt.Errorf("停止 FFmpeg 测试源超时")
		}
	}

	if err := process.Process.Signal(os.Interrupt); err != nil {
		_ = process.Process.Kill()
	}
	select {
	case <-r.done:
		return nil
	case <-time.After(3 * time.Second):
		if err := process.Process.Kill(); err != nil && !strings.Contains(strings.ToLower(err.Error()), "process already finished") {
			return fmt.Errorf("停止 FFmpeg 测试源失败: %w", err)
		}
		<-r.done
		return nil
	}
}
