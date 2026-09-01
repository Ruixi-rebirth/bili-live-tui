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
	"bili-live-tui/internal/utils"
)

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

const (
	ffmpegReconnectWindow      = 60 * time.Second
	ffmpegOutputConfirmTimeout = 15 * time.Second
	ffmpegStableRunDuration    = 10 * time.Second
)

var ffmpegReconnectDelays = []time.Duration{
	2 * time.Second,
	5 * time.Second,
	10 * time.Second,
	15 * time.Second,
}

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
	r.health.Active = false
	r.health.Reconnecting = true
	r.health.LastError = "FFmpeg 已启动，正在确认有效编码帧"
	r.mu.Unlock()
	go r.run(cmd, progress)
	return nil
}

func newFFmpegProcess(rtmpAddr, streamKey, orientation string) (*exec.Cmd, io.ReadCloser, error) {
	ffmpegPath, err := utils.GetExecutablePath("ffmpeg")
	if err != nil {
		return nil, nil, err
	}
	args := []string{"-hide_banner", "-nostats", "-progress", "pipe:2"}
	args = append(args, testSourceArgs(orientation)...)
	args = append(args,
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-b:a", "128k", "-f", "flv", rtmpAddr+streamKey,
	)
	cmd := exec.Command(ffmpegPath, args...)
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
	recoveryStarted := time.Time{}
	attempt := 0
	for {
		err, stable := r.waitFFmpegProcess(cmd, progress)
		if r.isStopping() {
			r.setStoppedHealth()
			r.finish()
			return
		}

		detail := r.ffmpegExitDetail(err)
		// 一次产生有效帧且稳定运行过的恢复应开始新的重连窗口；否则短暂
		// 拉起又退出会持续消耗原窗口，不能无限重置 60 秒倒计时。
		if recoveryStarted.IsZero() || stable {
			recoveryStarted = time.Now()
			attempt = 0
		}
		r.setReconnectHealth(detail)

		for {
			remaining := ffmpegReconnectWindow - time.Since(recoveryStarted)
			if remaining <= 0 {
				r.setReconnectTimeout(detail)
				r.finish()
				return
			}
			attempt++
			delay := ffmpegReconnectDelay(attempt - 1)
			if delay > remaining {
				delay = remaining
			}
			r.setReconnectWaiting(detail, attempt, delay)
			if !r.waitReconnectDelay(delay) {
				r.setStoppedHealth()
				r.finish()
				return
			}

			newCmd, newProgress, startErr := newFFmpegProcess(r.rtmpAddr, r.streamKey, r.orientation)
			if startErr != nil {
				detail = "启动 FFmpeg 重连进程失败：" + startErr.Error()
				r.setReconnectHealth(detail)
				continue
			}
			r.mu.Lock()
			if r.stopping {
				r.mu.Unlock()
				stopFFmpegProcess(newCmd, newProgress)
				r.setStoppedHealth()
				r.finish()
				return
			}
			r.cmd = newCmd
			r.health.Active = false
			r.health.Reconnecting = true
			r.health.LastError = fmt.Sprintf("FFmpeg 已重启，正在确认推流是否恢复（第 %d 次）", attempt)
			r.health.Duration = 0
			r.health.FPS = 0
			r.health.BitrateKbps = 0
			r.health.SkippedFrames = 0
			r.health.TotalFrames = 0
			r.lastLog = ""
			r.mu.Unlock()
			cmd, progress = newCmd, newProgress
			break
		}
	}
}

func (r *TestRuntime) waitFFmpegProcess(cmd *exec.Cmd, progress io.ReadCloser) (error, bool) {
	progressDone := make(chan struct{})
	outputReady := make(chan struct{})
	go func() {
		r.readProgressWithReady(progress, outputReady)
		close(progressDone)
	}()
	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()
	confirmTimer := time.NewTimer(ffmpegOutputConfirmTimeout)
	defer confirmTimer.Stop()
	var stableTimer *time.Timer
	var stableC <-chan time.Time
	stable := false
	defer func() {
		if stableTimer != nil {
			stableTimer.Stop()
		}
	}()
	for {
		select {
		case err := <-waitDone:
			<-progressDone
			_ = progress.Close()
			return err, stable
		case <-outputReady:
			outputReady = nil
			if !confirmTimer.Stop() {
				select {
				case <-confirmTimer.C:
				default:
				}
			}
			stableTimer = time.NewTimer(ffmpegStableRunDuration)
			stableC = stableTimer.C
			r.mu.Lock()
			if !r.stopping && r.cmd == cmd {
				r.health.LastError = "FFmpeg 已产生编码帧，正在确认推流稳定"
			}
			r.mu.Unlock()
		case <-confirmTimer.C:
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			err := <-waitDone
			<-progressDone
			_ = progress.Close()
			if err != nil {
				return fmt.Errorf("FFmpeg 在 %s 内未产生有效编码帧：%w", ffmpegOutputConfirmTimeout, err), false
			}
			return fmt.Errorf("FFmpeg 在 %s 内未产生有效编码帧", ffmpegOutputConfirmTimeout), false
		case <-stableC:
			stable = true
			stableC = nil
			r.mu.Lock()
			if !r.stopping && r.cmd == cmd {
				r.health.Active = true
				r.health.Reconnecting = false
				r.health.LastError = ""
			}
			r.mu.Unlock()
		}
	}
}

func (r *TestRuntime) isStopping() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.stopping
}

func (r *TestRuntime) setStoppedHealth() {
	r.mu.Lock()
	r.health.Active = false
	r.health.Reconnecting = false
	r.mu.Unlock()
}

func (r *TestRuntime) ffmpegExitDetail(err error) string {
	r.mu.RLock()
	detail := strings.TrimSpace(r.lastLog)
	r.mu.RUnlock()
	if err != nil {
		if detail != "" {
			return "FFmpeg 测试源已退出：" + err.Error() + "；" + detail
		}
		return "FFmpeg 测试源已退出：" + err.Error()
	}
	if detail != "" {
		return detail
	}
	return "FFmpeg 测试源已退出"
}

func (r *TestRuntime) setReconnectHealth(detail string) {
	r.mu.Lock()
	r.health.Active = false
	r.health.Reconnecting = true
	r.health.LastError = detail
	r.mu.Unlock()
}

func (r *TestRuntime) setReconnectWaiting(detail string, attempt int, delay time.Duration) {
	r.mu.Lock()
	r.health.Active = false
	r.health.Reconnecting = true
	r.health.LastError = fmt.Sprintf("%s；%s后进行第 %d 次重连", detail, formatReconnectDelay(delay), attempt)
	r.mu.Unlock()
}

func (r *TestRuntime) setReconnectTimeout(detail string) {
	r.mu.Lock()
	r.health.Active = false
	r.health.Reconnecting = false
	r.health.LastError = "FFmpeg 连续重连超时：" + detail
	r.mu.Unlock()
}

func (r *TestRuntime) waitReconnectDelay(delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-r.stopCh:
		return false
	case <-timer.C:
		return true
	}
}

func ffmpegReconnectDelay(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	if attempt >= len(ffmpegReconnectDelays) {
		return ffmpegReconnectDelays[len(ffmpegReconnectDelays)-1]
	}
	return ffmpegReconnectDelays[attempt]
}

func formatReconnectDelay(delay time.Duration) string {
	if delay%time.Second == 0 {
		return fmt.Sprintf("%d 秒", int(delay/time.Second))
	}
	return delay.Round(100 * time.Millisecond).String()
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
	r.readProgressWithReady(progress, nil)
}

func (r *TestRuntime) readProgressWithReady(progress io.Reader, outputReady chan struct{}) {
	scanner := bufio.NewScanner(progress)
	var readyOnce sync.Once
	markOutputReady := func() {
		if outputReady != nil {
			readyOnce.Do(func() { close(outputReady) })
		}
	}
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		key, value, ok := strings.Cut(line, "=")
		r.mu.Lock()
		recognized := ok
		if ok {
			switch key {
			case "frame":
				r.health.TotalFrames, _ = strconv.ParseInt(strings.TrimSpace(value), 10, 64)
				if r.health.TotalFrames > 0 {
					markOutputReady()
				}
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
			message := sanitizeFFmpegLogLine(line)
			// FFmpeg 结束时通常最后一行是“Conversion failed!”；保留更早的
			// Error/Server/Connection 行，才能在 TUI 和诊断日志中看到根因。
			if r.lastLog == "" || (!isFFmpegDiagnosticLine(r.lastLog) && isFFmpegDiagnosticLine(message)) {
				r.lastLog = message
			}
		}
		r.mu.Unlock()
	}
	if err := scanner.Err(); err != nil {
		r.mu.Lock()
		r.lastLog = "读取 FFmpeg 输出失败：" + err.Error()
		r.mu.Unlock()
	}
}

func isFFmpegDiagnosticLine(line string) bool {
	line = strings.ToLower(line)
	for _, marker := range []string{"error", "failed", "server", "connection", "refused", "denied", "timeout", "timed out"} {
		if strings.Contains(line, marker) {
			return true
		}
	}
	return false
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
