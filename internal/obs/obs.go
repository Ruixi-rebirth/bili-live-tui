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
	"github.com/andreykaipov/goobs"
	"github.com/andreykaipov/goobs/api/requests/config"
	"github.com/andreykaipov/goobs/api/requests/general"
	obsstream "github.com/andreykaipov/goobs/api/requests/stream"
	"github.com/andreykaipov/goobs/api/typedefs"
)

const OBSPort = "4455"

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

	command := ""
	for _, candidate := range []string{"obs", "obs-studio"} {
		if path, err := exec.LookPath(candidate); err == nil {
			command = path
			break
		}
	}
	if command == "" {
		return fmt.Errorf("未找到 OBS Studio，请先安装 OBS，或确认 obs/obs-studio 命令在 PATH 中")
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

// SyncAndStartStream 自动拉起 OBS、填入密钥并开始推流。
func SyncAndStartStream(rtmpAddr string, streamKey string, obsPassword string) error {
	// 确保 OBS 进程已经启动。
	if err := ensureObsAlive(); err != nil {
		return err
	}

	// 连接 OBS WebSocket。
	client, err := goobs.New("localhost:4455", goobs.WithPassword(obsPassword))
	if err != nil {
		return err
	}
	defer client.Disconnect()

	// 填入 B 站推流码。
	serviceType := "rtmp_custom" // Go 语法限制不能直接 &"字符串"，必须先声明变量

	settings := typedefs.StreamServiceSettings{
		Server:  rtmpAddr,
		Key:     streamKey,
		UseAuth: false,
	}

	_, err = client.Config.SetStreamServiceSettings(&config.SetStreamServiceSettingsParams{
		StreamServiceType:     &serviceType, // 取指针
		StreamServiceSettings: &settings,    // 取指针
	})
	if err != nil {
		return fmt.Errorf("设置 OBS 推流地址失败: %v", err)
	}

	// 开始推流。
	_, err = client.Stream.StartStream(&obsstream.StartStreamParams{})
	if err != nil {
		return err
	}

	log.Println("OBS 自动推流已启动！")
	return nil
}

// Runtime 持有持续的 OBS WebSocket 会话，使 TUI 能在开播后观察推流状态，
// 而不是配置 RTMP 后立即断开连接。
type Runtime struct {
	password string

	mu              sync.RWMutex
	client          *goobs.Client
	health          streamruntime.Health
	done            chan struct{}
	doneOnce        sync.Once
	monitorStop     chan struct{}
	monitorStopOnce sync.Once
	stopping        bool
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
	client, err := goobs.New(
		"localhost:"+OBSPort,
		goobs.WithPassword(r.password),
		goobs.WithLogger(log.New(io.Discard, "", 0)),
		goobs.WithResponseTimeoutDuration(5*time.Second),
	)
	if err != nil {
		return fmt.Errorf("连接 OBS WebSocket 失败: %w；请确认 OBS 已启用 WebSocket 服务且密码正确", err)
	}

	status, err := client.Stream.GetStreamStatus(&obsstream.GetStreamStatusParams{})
	if err != nil {
		_ = client.Disconnect()
		return fmt.Errorf("读取 OBS 推流状态失败: %w", err)
	}
	if status.OutputActive {
		_ = client.Disconnect()
		return fmt.Errorf("OBS 已在推流，请先停止现有推流后再开始")
	}

	serviceType := "rtmp_custom"
	settings := typedefs.StreamServiceSettings{Server: rtmpAddr, Key: streamKey, UseAuth: false}
	if _, err := client.Config.SetStreamServiceSettings(&config.SetStreamServiceSettingsParams{
		StreamServiceType:     &serviceType,
		StreamServiceSettings: &settings,
	}); err != nil {
		_ = client.Disconnect()
		return fmt.Errorf("设置 OBS 推流地址失败: %w", err)
	}
	if _, err := client.Stream.StartStream(&obsstream.StartStreamParams{}); err != nil {
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
	status, err := client.Stream.GetStreamStatus(&obsstream.GetStreamStatusParams{})
	if err != nil {
		r.mu.Lock()
		r.health.LastError = "读取 OBS 推流状态失败：" + err.Error()
		r.mu.Unlock()
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

func (r *Runtime) applyHealthSample(sample healthSample, now time.Time) {
	r.mu.Lock()
	if elapsed := now.Sub(r.lastSample).Seconds(); elapsed > 0 && !r.lastSample.IsZero() && sample.bytes >= r.lastBytes {
		r.health.BitrateKbps = (sample.bytes - r.lastBytes) * 8 / elapsed / 1000
	}
	r.lastBytes = sample.bytes
	r.lastSample = now
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

func (r *Runtime) Stop() error {
	r.mu.Lock()
	if r.client == nil {
		r.mu.Unlock()
		return nil
	}
	r.stopping = true
	client := r.client
	r.mu.Unlock()
	r.monitorStopOnce.Do(func() { close(r.monitorStop) })

	var stopErr error
	status, err := client.Stream.GetStreamStatus(&obsstream.GetStreamStatusParams{})
	if err != nil {
		stopErr = fmt.Errorf("停止前读取 OBS 状态失败: %w", err)
		// 读取状态可能暂时失败，但 StopStream 仍可能成功到达 OBS。
		// 这里尽力停止，避免离开 TUI 后编码器仍在无提示地运行。
		if _, stopRequestErr := client.Stream.StopStream(&obsstream.StopStreamParams{}); stopRequestErr != nil {
			stopErr = fmt.Errorf("%v；停止 OBS 推流也失败: %w", stopErr, stopRequestErr)
		}
	} else if status.OutputActive {
		if _, err := client.Stream.StopStream(&obsstream.StopStreamParams{}); err != nil && !strings.Contains(strings.ToLower(err.Error()), "not active") {
			stopErr = fmt.Errorf("停止 OBS 推流失败: %w", err)
		}
	}
	if err := client.Disconnect(); err != nil && stopErr == nil {
		stopErr = fmt.Errorf("断开 OBS WebSocket 失败: %w", err)
	}
	r.mu.Lock()
	r.health.Active = false
	r.client = nil
	r.mu.Unlock()
	r.doneOnce.Do(func() { close(r.done) })
	return stopErr
}
