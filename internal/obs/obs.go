package obs

import (
	"fmt"
	"io"
	"log"
	"net"
	"os/exec"
	"strings"
	"sync"
	"time"

	streamruntime "bili-live-tui/internal/stream"
	"bili-live-tui/internal/utils"
	"github.com/andreykaipov/goobs"
	"github.com/andreykaipov/goobs/api/requests/config"
	"github.com/andreykaipov/goobs/api/requests/general"
	obsstream "github.com/andreykaipov/goobs/api/requests/stream"
	"github.com/andreykaipov/goobs/api/typedefs"
)

const OBSPort = "4455"

const (
	obsControlReconnectWindow = 60 * time.Second
	obsStartupReadyAttempts   = 41
	obsStartupReadyRetryDelay = 500 * time.Millisecond
)

// 检查端口是否被占用
func isObsRunning(port string) bool {
	timeout := time.Second
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("localhost", port), timeout)
	if err != nil {
		return false
	}
	if conn != nil {
		defer conn.Close()
		return true
	}
	return false
}

// 确保 OBS 已启动
func ensureObsAlive() error {
	if isObsRunning(OBSPort) {
		return nil // 已经运行中
	}

	command, err := utils.GetExecutablePath("obs", "obs-studio")
	if err != nil {
		return err
	}
	cmd := exec.Command(command)

	// 异步启动，不阻塞主程序
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("无法启动 OBS: %v", err)
	}
	// OBS 的生命周期独立于本启动器。释放子进程句柄，避免 OBS 初始化 WebSocket 时
	// 启动器一直持有子进程资源。
	_ = cmd.Process.Release()

	// 轮询等待 OBS WebSocket 端口就绪 (最多等 10 秒)
	for range 10 {
		time.Sleep(1 * time.Second)
		if isObsRunning("4455") {
			return nil
		}
	}

	return fmt.Errorf("OBS 启动超时或 WebSocket 未开启")
}

// Runtime 持有持续的 OBS WebSocket 会话，使 TUI 能在开播后观察推流状态，
// 而不是配置 RTMP 后立即断开连接。
type Runtime struct {
	password string

	mu              sync.RWMutex
	controlMu       sync.Mutex
	client          *goobs.Client
	health          streamruntime.Health
	done            chan struct{}
	doneOnce        sync.Once
	monitorStop     chan struct{}
	monitorStopOnce sync.Once
	stopping        bool
	controlFailedAt time.Time
	lastBytes       float64
	lastSample      time.Time
}

type healthSample struct {
	active         bool
	reconnecting   bool
	duration       time.Duration
	bytes          float64
	skippedFrames  int64
	totalFrames    int64
	statsAvailable bool
	fps            float64
	cpuPercent     float64
	memoryMB       float64
}

func NewRuntime(password string) *Runtime {
	return &Runtime{
		password:    password,
		health:      streamruntime.Health{Mode: streamruntime.ModeOBS},
		done:        make(chan struct{}),
		monitorStop: make(chan struct{}),
	}
}

func (r *Runtime) Start(rtmpAddr, streamKey string) error {
	if err := ensureObsAlive(); err != nil {
		return err
	}
	client, err := newRuntimeClient(r.password)
	if err != nil {
		return fmt.Errorf("连接 OBS WebSocket 失败: %w；请确认 OBS 已启用 WebSocket 服务且密码正确", err)
	}

	var status *obsstream.GetStreamStatusResponse
	err = retryOBSNotReady(obsStartupReadyAttempts, obsStartupReadyRetryDelay, func() error {
		var requestErr error
		status, requestErr = client.Stream.GetStreamStatus(&obsstream.GetStreamStatusParams{})
		return requestErr
	})
	if err != nil {
		_ = client.Disconnect()
		return fmt.Errorf("等待 OBS 就绪失败: %w", err)
	}
	if status.OutputActive {
		_ = client.Disconnect()
		return fmt.Errorf("OBS 已在推流，请先停止现有推流后再开始")
	}

	serviceType := "rtmp_custom"
	settings := typedefs.StreamServiceSettings{Server: rtmpAddr, Key: streamKey, UseAuth: false}
	err = retryOBSNotReady(obsStartupReadyAttempts, obsStartupReadyRetryDelay, func() error {
		_, requestErr := client.Config.SetStreamServiceSettings(&config.SetStreamServiceSettingsParams{
			StreamServiceType:     &serviceType,
			StreamServiceSettings: &settings,
		})
		return requestErr
	})
	if err != nil {
		_ = client.Disconnect()
		return fmt.Errorf("设置 OBS 推流地址失败: %w", err)
	}
	err = retryOBSNotReady(obsStartupReadyAttempts, obsStartupReadyRetryDelay, func() error {
		_, requestErr := client.Stream.StartStream(&obsstream.StartStreamParams{})
		return requestErr
	})
	if err != nil {
		_ = client.Disconnect()
		return fmt.Errorf("启动 OBS 推流失败: %w", err)
	}

	r.mu.Lock()
	r.client = client
	r.health.Active = true
	r.health.LastError = ""
	r.lastSample = time.Now()
	r.mu.Unlock()
	go r.monitor()
	return nil
}

func (r *Runtime) monitor() {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.monitorStop:
			return
		case <-ticker.C:
			r.sampleHealth()
		}
	}
}

func (r *Runtime) sampleHealth() {
	r.mu.RLock()
	client := r.client
	stopping := r.stopping
	r.mu.RUnlock()
	if client == nil || stopping {
		return
	}
	r.controlMu.Lock()
	defer r.controlMu.Unlock()
	r.mu.RLock()
	if r.stopping || r.client != client {
		r.mu.RUnlock()
		return
	}
	r.mu.RUnlock()
	status, err := client.Stream.GetStreamStatus(&obsstream.GetStreamStatusParams{})
	if err != nil {
		if isOBSNotReady(err) {
			r.markControlNotReady(err)
			return
		}
		if r.markControlConnectionError(err) {
			return
		}
		r.mu.RLock()
		stopping = r.stopping
		r.mu.RUnlock()
		if stopping {
			return
		}
		if reconnectErr := r.reconnectControlClient(client); reconnectErr != nil {
			r.markControlConnectionError(reconnectErr)
		}
		return
	}
	stats, statsErr := client.General.GetStats(&general.GetStatsParams{})
	sample := healthSample{
		active:        status.OutputActive,
		reconnecting:  status.OutputReconnecting,
		duration:      time.Duration(status.OutputDuration) * time.Millisecond,
		bytes:         status.OutputBytes,
		skippedFrames: int64(status.OutputSkippedFrames),
		totalFrames:   int64(status.OutputTotalFrames),
	}
	if statsErr == nil {
		sample.statsAvailable = true
		sample.fps = stats.ActiveFps
		sample.cpuPercent = stats.CpuUsage
		sample.memoryMB = stats.MemoryUsage
	}
	r.applyHealthSample(sample, time.Now())
}

func newRuntimeClient(password string) (*goobs.Client, error) {
	return goobs.New(
		"localhost:"+OBSPort,
		goobs.WithPassword(password),
		goobs.WithLogger(log.New(io.Discard, "", 0)),
		goobs.WithResponseTimeoutDuration(5*time.Second),
	)
}

// retryOBSNotReady 只重试协议明确标记为可稍后重试的 207 状态。
// 其他错误通常是密码、请求或配置问题，应立即返回给用户。
func retryOBSNotReady(attempts int, delay time.Duration, operation func() error) error {
	if attempts < 1 {
		attempts = 1
	}
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		lastErr = operation()
		if lastErr == nil || !isOBSNotReady(lastErr) {
			return lastErr
		}
		if attempt < attempts && delay > 0 {
			time.Sleep(delay)
		}
	}
	return fmt.Errorf("OBS 在约 %d 秒内仍未准备完成: %w", int(time.Duration(attempts-1)*delay/time.Second), lastErr)
}

func isOBSNotReady(err error) bool {
	return err != nil && strings.Contains(err.Error(), "NotReady (207)")
}

func (r *Runtime) markControlNotReady(err error) bool {
	if err == nil {
		return false
	}
	timedOut := r.markControlConnectionError(err)
	if timedOut {
		return true
	}
	r.mu.Lock()
	if !r.stopping {
		r.health.LastError = "OBS 暂未就绪，正在等待恢复：" + err.Error()
	}
	r.mu.Unlock()
	return false
}

func (r *Runtime) markControlConnectionError(err error) bool {
	return r.markControlConnectionErrorAt(err, time.Now())
}

// markControlConnectionErrorAt 返回控制通道是否已经超过恢复窗口。
// 时间参数让边界状态无需真实等待一分钟即可测试。
func (r *Runtime) markControlConnectionErrorAt(err error, now time.Time) bool {
	if err == nil {
		return false
	}
	r.mu.Lock()
	if r.stopping {
		r.mu.Unlock()
		return false
	}
	if r.controlFailedAt.IsZero() {
		r.controlFailedAt = now
	}
	if now.Sub(r.controlFailedAt) >= obsControlReconnectWindow {
		r.health.Active = false
		r.health.Reconnecting = false
		r.health.LastError = fmt.Sprintf("OBS 控制连接在 %d 秒内未恢复：%v", int(obsControlReconnectWindow/time.Second), err)
		r.mu.Unlock()
		r.monitorStopOnce.Do(func() { close(r.monitorStop) })
		r.doneOnce.Do(func() { close(r.done) })
		return true
	}
	r.health.Reconnecting = true
	r.health.LastError = "OBS 控制连接断开，正在重连：" + err.Error()
	r.mu.Unlock()
	return false
}

// reconnectControlClient 只恢复 OBS WebSocket 控制通道，不重启 OBS 或重新开始推流。
// 调用方必须持有 controlMu，避免与状态读取和 Stop 并发使用 goobs 客户端。
func (r *Runtime) reconnectControlClient(previous *goobs.Client) error {
	client, err := newRuntimeClient(r.password)
	if err != nil {
		return fmt.Errorf("OBS 控制连接重连失败: %w", err)
	}
	r.mu.Lock()
	if r.stopping || r.client != previous {
		r.mu.Unlock()
		_ = client.Disconnect()
		return fmt.Errorf("OBS 控制连接已停止")
	}
	r.client = client
	r.health.Reconnecting = true
	r.health.LastError = "OBS 控制连接已恢复，正在确认推流状态"
	r.mu.Unlock()
	if previous != nil {
		_ = previous.Disconnect()
	}
	return nil
}

func (r *Runtime) applyHealthSample(sample healthSample, now time.Time) {
	r.mu.Lock()
	if elapsed := now.Sub(r.lastSample).Seconds(); elapsed > 0 && !r.lastSample.IsZero() && sample.bytes >= r.lastBytes {
		r.health.BitrateKbps = (sample.bytes - r.lastBytes) * 8 / elapsed / 1000
	}
	r.lastBytes = sample.bytes
	r.lastSample = now
	r.controlFailedAt = time.Time{}
	r.health.Active = sample.active
	r.health.Reconnecting = sample.reconnecting
	r.health.Duration = sample.duration
	r.health.SkippedFrames = sample.skippedFrames
	r.health.TotalFrames = sample.totalFrames
	if sample.statsAvailable {
		r.health.FPS = sample.fps
		r.health.CPUPercent = sample.cpuPercent
		r.health.MemoryMB = sample.memoryMB
	}
	if sample.active {
		r.health.LastError = ""
	} else if !r.stopping {
		r.health.LastError = "OBS 推流已意外停止"
	}
	active := r.health.Active
	r.mu.Unlock()
	if !active {
		r.doneOnce.Do(func() { close(r.done) })
	}
}

func (r *Runtime) Health() streamruntime.Health {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.health
}

func (r *Runtime) Done() <-chan struct{} { return r.done }

func (r *Runtime) Stop() (stopErr error) {
	r.mu.Lock()
	if r.client == nil || r.stopping {
		r.mu.Unlock()
		return nil
	}
	r.stopping = true
	r.mu.Unlock()
	r.monitorStopOnce.Do(func() { close(r.monitorStop) })
	r.controlMu.Lock()
	defer r.controlMu.Unlock()

	// 监控协程可能正在更换控制连接，因此要在获得 controlMu 后再取客户端。
	r.mu.RLock()
	client := r.client
	r.mu.RUnlock()
	defer func() {
		if client != nil {
			if err := client.Disconnect(); err != nil && stopErr == nil && !isOBSClientDisconnected(err) {
				stopErr = fmt.Errorf("断开 OBS WebSocket 失败: %w", err)
			}
		}
		r.mu.Lock()
		r.health.Active = false
		r.client = nil
		r.mu.Unlock()
		r.doneOnce.Do(func() { close(r.done) })
	}()

	status, err := client.Stream.GetStreamStatus(&obsstream.GetStreamStatusParams{})
	if isOBSClientDisconnected(err) {
		freshClient, reconnectErr := newRuntimeClient(r.password)
		if reconnectErr != nil {
			return fmt.Errorf("停止 OBS 推流失败：控制连接已断开，重新连接失败: %w", reconnectErr)
		}
		_ = client.Disconnect()
		client = freshClient
		status, err = client.Stream.GetStreamStatus(&obsstream.GetStreamStatusParams{})
	}
	if err == nil && !status.OutputActive {
		return nil
	}
	// 即使状态读取暂时失败，停止请求仍可能成功到达 OBS。
	if _, requestErr := client.Stream.StopStream(&obsstream.StopStreamParams{}); requestErr != nil && !isOBSOutputNotRunning(requestErr) {
		if isOBSClientDisconnected(requestErr) {
			return fmt.Errorf("停止 OBS 推流失败：OBS 控制连接已断开")
		}
		return fmt.Errorf("停止 OBS 推流失败: %w", requestErr)
	}
	return nil
}

func isOBSClientDisconnected(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "client already disconnected")
}

func isOBSOutputNotRunning(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "outputnotrunning (501)") || strings.Contains(message, "not active")
}
