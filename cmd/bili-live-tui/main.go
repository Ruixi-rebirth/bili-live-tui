package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"bili-live-tui/internal/api"
	"bili-live-tui/internal/config"
	"bili-live-tui/internal/diagnostics"
	streamruntime "bili-live-tui/internal/stream"
	"bili-live-tui/tui"
)

const localizedUsageTemplate = `用法:{{if and .Runnable .HasAvailableSubCommands}}
  {{.CommandPath}}{{if or .HasAvailableFlags .HasAvailableInheritedFlags}} [选项]{{end}}
  {{.CommandPath}} [命令]{{else if .Runnable}}
  {{.CommandPath}}{{if or .HasAvailableFlags .HasAvailableInheritedFlags}} [选项]{{end}}{{else if .HasAvailableSubCommands}}
  {{.CommandPath}} [命令]{{end}}{{if gt (len .Aliases) 0}}

别名:
  {{.NameAndAliases}}{{end}}{{if .HasExample}}

示例:
{{.Example}}{{end}}{{if .HasAvailableSubCommands}}{{$cmds := .Commands}}{{if eq (len .Groups) 0}}

可用命令:{{range $cmds}}{{if (or .IsAvailableCommand (eq .Name "help"))}}
  {{rpad .Name .NamePadding }} {{.Short}}{{end}}{{end}}{{else}}{{range $group := .Groups}}

{{.Title}}{{range $cmds}}{{if (and (eq .GroupID $group.ID) (or .IsAvailableCommand (eq .Name "help")))}}
  {{rpad .Name .NamePadding }} {{.Short}}{{end}}{{end}}{{end}}{{if not .AllChildCommandsHaveGroup}}

其他命令:{{range $cmds}}{{if (and (eq .GroupID "") (or .IsAvailableCommand (eq .Name "help")))}}
  {{rpad .Name .NamePadding }} {{.Short}}{{end}}{{end}}{{end}}{{end}}{{end}}{{if .HasAvailableLocalFlags}}

选项:
{{.LocalFlags.FlagUsages | trimTrailingWhitespaces}}{{end}}{{if .HasAvailableInheritedFlags}}

全局选项:
{{.InheritedFlags.FlagUsages | trimTrailingWhitespaces}}{{end}}{{if .HasHelpSubCommands}}

其他帮助主题:{{range .Commands}}{{if .IsAdditionalHelpTopicCommand}}
  {{rpad .CommandPath .CommandPathPadding}} {{.Short}}{{end}}{{end}}{{end}}{{if .HasAvailableSubCommands}}

使用 "{{.CommandPath}} [命令] --help" 查看该命令的详细信息。{{end}}
`

func localizeCommand(cmd *cobra.Command) {
	cmd.InitDefaultHelpFlag()
	if helpFlag := cmd.Flags().Lookup("help"); helpFlag != nil {
		helpFlag.Usage = "显示帮助信息"
	}
	if descFlag := cmd.Flags().Lookup("no-descriptions"); descFlag != nil {
		descFlag.Usage = "禁用补全候选项的说明文本"
	}
	switch cmd.Name() {
	case "completion":
		cmd.Short = "生成指定 Shell 的自动补全脚本 (bash, zsh, fish)"
		cmd.Long = "为 bili-live-tui 生成对应 Shell 的命令行自动补全脚本。\n支持 bash、zsh、fish 以及 powershell。"
	case "help":
		cmd.Short = "查看任意命令的帮助信息"
	case "bash":
		cmd.Short = "生成 Bash 自动补全脚本"
		cmd.Long = "为 Bash 生成命令行自动补全脚本。\n\n当前会话生效:\n  source <(bili-live-tui completion bash)\n\n永久生效 (Linux):\n  bili-live-tui completion bash > /etc/bash_completion.d/bili-live-tui"
	case "zsh":
		cmd.Short = "生成 Zsh 自动补全脚本"
		cmd.Long = "为 Zsh 生成命令行自动补全脚本。\n\n当前会话生效:\n  source <(bili-live-tui completion zsh)\n\n永久生效 (写入 ~/.zshrc):\n  echo 'source <(bili-live-tui completion zsh)' >> ~/.zshrc"
	case "fish":
		cmd.Short = "生成 Fish 自动补全脚本"
		cmd.Long = "为 Fish 生成命令行自动补全脚本。\n\n当前会话生效:\n  bili-live-tui completion fish | source\n\n永久生效:\n  bili-live-tui completion fish > ~/.config/fish/completions/bili-live-tui.fish"
	case "powershell":
		cmd.Short = "生成 PowerShell 自动补全脚本"
		cmd.Long = "为 PowerShell 生成命令行自动补全脚本。\n\n当前会话生效:\n  bili-live-tui completion powershell | Out-String | Invoke-Expression"
	}
	for _, sub := range cmd.Commands() {
		localizeCommand(sub)
	}
}

func newRootCmd() *cobra.Command {
	var (
		noColorFlag bool
		onceFlag    bool
	)

	rootCmd := &cobra.Command{
		Use:   "bili-live-tui",
		Short: "面向 Bilibili 主播的轻量级终端直播控制台",
		Long: `bili-live-tui 是一个面向 Bilibili 主播的轻量级终端直播控制台。
突破 5000 粉丝推流限制，让 Linux / macOS / Windows 都能自由使用 OBS 推流。
集开播设置、自动联动、实时弹幕与房管治理于一体。`,
		SilenceUsage:  true,
		SilenceErrors: true,
		PreRun: func(cmd *cobra.Command, args []string) {
			allNoColor := noColorFlag || os.Getenv("NO_COLOR") != ""
			tui.SetNoColor(allNoColor)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTUI(cmd.Context(), !onceFlag)
		},
	}

	rootCmd.SetUsageTemplate(localizedUsageTemplate)
	rootCmd.Flags().BoolVar(&noColorFlag, "no-color", false, "禁用界面颜色高亮")
	rootCmd.Flags().BoolVar(&onceFlag, "once", false, "单次会话模式：不读取本地已有凭据与设置，也绝不保存任何数据到本地")

	statusCmd := &cobra.Command{
		Use:   "status",
		Short: "查看当前直播间开播状态与推流状态",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStatus(cmd.Context())
		},
	}

	stopCmd := &cobra.Command{
		Use:   "stop",
		Short: "一键向 B 站发送下播请求并停止推流",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStop(cmd.Context())
		},
	}

	logoutCmd := &cobra.Command{
		Use:   "logout",
		Short: "退出登录并清除本机保存的凭据",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLogout()
		},
	}

	rootCmd.AddCommand(statusCmd, stopCmd, logoutCmd)

	rootCmd.InitDefaultCompletionCmd()
	rootCmd.InitDefaultHelpCmd()
	localizeCommand(rootCmd)

	return rootCmd
}

func runLogout() error {
	removed, err := config.RemoveAuth()
	if err != nil {
		return fmt.Errorf("清除本地凭证失败: %w", err)
	}
	if removed {
		fmt.Println("✅ 已成功退出登录并清除本机凭据")
	} else {
		fmt.Println("本地未发现已保存的登录凭据，当前处于未登录状态")
	}
	return nil
}

type appContext struct {
	client        *api.Client
	auth          *config.AuthData
	roomID        string
	diagnosticLog *diagnostics.Logger
}

func initAppContext(ctx context.Context, persist bool) (*appContext, error) {
	diagnosticLog, _ := diagnostics.Open()
	if diagnosticLog != nil {
		diagnosticLog.Printf("程序启动")
	}

	client := api.NewClient(nil)
	var auth *config.AuthData
	var err error
	if persist {
		auth, err = config.LoadAuth()
	}
	if !persist || err != nil {
		auth, err = performLogin(ctx, persist)
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				if diagnosticLog != nil {
					diagnosticLog.Printf("扫码登录失败: %v", err)
				}
				return nil, fmt.Errorf("登录失败: %w", err)
			}
			return nil, err
		}
	}

	roomID, err := client.GetMyRoomID(ctx, auth.SESSDATA)
	if err != nil && isAuthenticationError(err) {
		fmt.Println("登录凭证已失效，请重新扫码登录")
		auth, err = performLogin(ctx, persist)
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				if diagnosticLog != nil {
					diagnosticLog.Printf("重新登录失败: %v", err)
				}
				return nil, fmt.Errorf("重新登录失败: %w", err)
			}
			return nil, err
		}
		roomID, err = client.GetMyRoomID(ctx, auth.SESSDATA)
	}
	if err != nil {
		if diagnosticLog != nil {
			diagnosticLog.Printf("获取房间号失败: %v", err)
		}
		return nil, fmt.Errorf("获取房间号失败: %w", err)
	}

	return &appContext{
		client:        client,
		auth:          auth,
		roomID:        roomID,
		diagnosticLog: diagnosticLog,
	}, nil
}

func runStatus(ctx context.Context) error {
	app, err := initAppContext(ctx, true)
	if err != nil {
		return err
	}
	if app.diagnosticLog != nil {
		defer app.diagnosticLog.Close()
	}
	handleStatusAction(ctx, app.client, app.roomID)
	return nil
}

func runStop(ctx context.Context) error {
	app, err := initAppContext(ctx, true)
	if err != nil {
		return err
	}
	if app.diagnosticLog != nil {
		defer app.diagnosticLog.Close()
	}
	handleStopAction(app.client, app.roomID, app.auth.AccessToken, app.diagnosticLog)
	return nil
}

func main() {
	signalCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	ctx, cancel := context.WithCancel(signalCtx)
	defer cancel()

	rootCmd := newRootCmd()
	if err := rootCmd.ExecuteContext(ctx); err != nil {
		if !errors.Is(err, context.Canceled) {
			fmt.Fprintf(os.Stderr, "%v\n", err)
		}
		os.Exit(1)
	}
}

func runTUI(ctx context.Context, persist bool) error {
	app, err := initAppContext(ctx, persist)
	if err != nil {
		return err
	}
	if app.diagnosticLog != nil {
		defer app.diagnosticLog.Close()
	}

	client := app.client
	auth := app.auth
	roomID := app.roomID
	diagnosticLog := app.diagnosticLog

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// 启动 TUI 前检测直播间是否已处于开播状态（如意外强退或在其他端开播）
	initialSnapshot, snapshotErr := client.GetRoomSnapshot(ctx, roomID)
	if snapshotErr == nil && initialSnapshot.LiveStatus == 1 {
		action, dialogErr := tui.RunAlreadyLiveDialog(ctx, roomID, initialSnapshot.Title, func() error {
			return stopLiveSync(client, roomID, auth.AccessToken)
		})
		if dialogErr != nil || action == tui.AlreadyLiveActionExit || action == tui.AlreadyLiveActionStopLive {
			return nil
		}
		if action == tui.AlreadyLiveActionDanmaku {
			handleLiveTakeover(ctx, client, roomID, auth, initialSnapshot, diagnosticLog)
			return nil
		}
		// 只有“重新开播”会在旧直播清理成功后继续进入常规开播流程。
	}

	areas, areaErr := client.GetLiveAreas(ctx, auth.AccessToken)
	if areaErr != nil {
		areas = nil
	}
	var savedSettings *api.LiveSettings
	if persist {
		var settingsLoadErr error
		savedSettings, settingsLoadErr = config.LoadLiveSettings()
		if settingsLoadErr != nil && !os.IsNotExist(settingsLoadErr) {
			diagnosticLog.Printf("读取上次开播信息失败，将使用默认值: %v", settingsLoadErr)
			savedSettings = nil
		}
	}
	if savedSettings == nil || strings.TrimSpace(savedSettings.CoverPath) == "" {
		if snapshot, snapshotErr := client.GetRoomSnapshot(ctx, roomID); snapshotErr == nil && strings.TrimSpace(snapshot.Cover) != "" {
			if savedSettings == nil {
				savedSettings = &api.LiveSettings{}
			}
			savedSettings.CoverPath = snapshot.Cover
		}
	}
	var liveStream streamruntime.Runtime
	var liveStartedAt time.Time
	var platformLiveStarted atomic.Bool
	previousTags := ""
	if savedSettings != nil {
		previousTags = savedSettings.Tags
	}
	rollbackStartedLive := func(cause error) error {
		stopped, rollbackErr := rollbackLiveStart(client, roomID, auth.AccessToken, cause)
		if stopped {
			platformLiveStarted.Store(false)
		}
		return rollbackErr
	}
	settings, err := tui.RunLiveSettings(ctx, areas, savedSettings, func(liveSettings *api.LiveSettings) error {
		runtime, startedAt, startErr := prepareAndStartLive(
			ctx, client, roomID, auth, liveSettings, savedSettings, previousTags,
			&platformLiveStarted, rollbackStartedLive, diagnosticLog,
		)
		if startErr != nil {
			return startErr
		}
		liveStream = runtime
		liveStartedAt = startedAt
		return nil
	})
	if err != nil {
		if liveStream != nil {
			if stopErr := liveStream.Stop(); stopErr != nil {
				diagnosticLog.Printf("取消开播时停止本地推流失败: %v", stopErr)
			}
		}
		if platformLiveStarted.Load() {
			stopCtx, cancelStop := context.WithTimeout(context.Background(), 10*time.Second)
			stopErr := client.StopLive(stopCtx, roomID, auth.AccessToken)
			cancelStop()
			if stopErr != nil {
				diagnosticLog.Printf("取消开播时调用下播接口失败: %v", stopErr)
				fmt.Fprintln(os.Stderr, "自动下播失败，请到 B 站直播中心确认状态")
			} else {
				platformLiveStarted.Store(false)
			}
		}
		if !errors.Is(err, tui.ErrLiveSettingsCancelled) && !errors.Is(err, context.Canceled) {
			diagnosticLog.Printf("开播设置流程失败: %v", err)
			fmt.Fprintf(os.Stderr, "开播失败: %v\n", err)
		}
		return nil
	}
	if persist {
		if saveErr := config.SaveLiveSettings(settings); saveErr != nil {
			diagnosticLog.Printf("保存下次开播默认值失败: %v", saveErr)
		}
	}
	// 资料、开播和所选本地推流全部成功后才进入弹幕页，避免短暂回到终端外壳。

	// OBS/FFmpeg 通过 RTMP 连接维持直播。
	// B 站移动端心跳接口用于观众观看任务，不属于主播推流会话，不能在这里调用。
	danmakuSession := tui.NewLiveDanmakuSession(ctx, client, roomID, auth.SESSDATA, auth.BiliJCT)
	previewer := &livePreviewer{}
	previewLive := func() error {
		return previewer.Start(ctx, client, roomID, auth.SESSDATA, auth.BiliJCT)
	}
	var outputEndedUnexpectedly atomic.Bool
	go watchStreamOutput(ctx, liveStream.Done(), func() {
		outputEndedUnexpectedly.Store(true)
		cancel()
	})

	// 开播成功后在弹幕页和直播概览之间切换。
	// 页面本身不再输出控制台日志，所有交互都在 TUI 内完成。
	var roomSnapshot *api.RoomSnapshot
	loadRoomSnapshot := func() (api.RoomSnapshot, error) {
		snapshot, err := client.GetRoomSnapshot(ctx, roomID)
		if err != nil {
			return api.RoomSnapshot{}, err
		}
		// 房间接口只作为弹幕心跳尚未返回数据时的临时兜底，不覆盖会话中的最新人气。
		if online, known := danmakuSession.Popularity(); known {
			snapshot.Online = online
			snapshot.OnlineKnown = true
		}
		return snapshot, nil
	}
	streamHealth := func() streamruntime.Health {
		if liveStream == nil {
			return streamruntime.Health{}
		}
		return liveStream.Health()
	}
	saveEdit := func(edited api.LiveSettings) (api.LiveSettings, error) {
		updated, saveErr := saveLiveSettings(ctx, client, roomID, auth, settings, edited)
		if saveErr != nil {
			return api.LiveSettings{}, saveErr
		}
		if persist {
			if saveErr := config.SaveLiveSettings(updated); saveErr != nil {
				diagnosticLog.Printf("保存下次开播默认值失败: %v", saveErr)
			}
		}
		return updated, nil
	}
	overviewOpts := tui.DanmakuOverviewOptions{
		StartedAt:        liveStartedAt,
		RoomID:           roomID,
		Settings:         &settings,
		Areas:            areas,
		RoomSnapshot:     roomSnapshot,
		LoadRoomSnapshot: loadRoomSnapshot,
		OnSnapshot: func(fresh api.RoomSnapshot) {
			roomSnapshot = &fresh
		},
		HealthLoader: streamHealth,
		SaveEdit:     saveEdit,
		PreviewLive:  previewLive,
	}
	if _, err := tui.RunDanmaku(ctx, danmakuSession, client, roomID, auth.SESSDATA, auth.BiliJCT, streamHealth, overviewOpts); err != nil {
		diagnosticLog.Printf("弹幕界面异常: %v", err)
		fmt.Fprintf(os.Stderr, "弹幕界面异常: %v\n", err)
	}

	danmakuSession.Close()
	cancel()
	if outputEndedUnexpectedly.Load() {
		detail := strings.TrimSpace(liveStream.Health().LastError)
		if detail == "" {
			detail = "未返回详细原因"
		}
		diagnosticLog.Printf("本地推流意外停止: %s", detail)
		fmt.Fprintf(os.Stderr, "本地推流已意外断开（%s）。\n程序未自动调用 B 站下播接口，直播间当前状态请以 B 站为准。重新开播前请确认没有其他客户端正在推流，若需下播请执行 bili-live-tui --stop\n", detail)
	}

	if liveStream != nil {
		if err := liveStream.Stop(); err != nil {
			diagnosticLog.Printf("停止本地推流失败: %v", err)
			fmt.Fprintf(os.Stderr, "停止本地推流遇到问题: %v\n", err)
		}
	}

	if !outputEndedUnexpectedly.Load() {
		if err := stopLiveSync(client, roomID, auth.AccessToken); err != nil {
			diagnosticLog.Printf("调用下播接口失败: %v", err)
			fmt.Fprintf(os.Stderr, "调用下播接口失败: %v\n", err)
		} else {
			diagnosticLog.Printf("直播已安全结束 room=%s", roomID)
		}
	}
	return nil
}

func stopLiveSync(client *api.Client, roomID, accessToken string) error {
	stopCtx, cancelStop := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelStop()
	return client.StopLive(stopCtx, roomID, accessToken)
}

func handleStatusAction(ctx context.Context, client *api.Client, roomID string) {
	snapshot, err := client.GetRoomSnapshot(ctx, roomID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "获取房间状态失败: %v\n", err)
		return
	}
	statusText := "未开播"
	var extraStatus string
	if snapshot.LiveStatus == 1 {
		statusText = "开播中 🔴"
		if !snapshot.LiveTime.IsZero() {
			duration := time.Since(snapshot.LiveTime).Truncate(time.Second)
			extraStatus = fmt.Sprintf("开播时间: %s\n已播时长: %s\n", snapshot.LiveTime.Format("2006-01-02 15:04:05"), duration)
		}
	}
	fmt.Printf("直播间号: %s\n房间标题: %s\n直播分区: %s\n当前状态: %s\n%s", roomID, snapshot.Title, snapshot.AreaName, statusText, extraStatus)
}

func handleStopAction(client *api.Client, roomID string, accessToken string, logger *diagnostics.Logger) {
	fmt.Printf("正在向 B 站请求结束房间 %s 的直播……\n", roomID)
	if err := stopLiveSync(client, roomID, accessToken); err != nil {
		if logger != nil {
			logger.Printf("命令行下播失败: %v", err)
		}
		fmt.Fprintf(os.Stderr, "下播失败: %v\n", err)
		return
	}
	if logger != nil {
		logger.Printf("命令行下播成功 room=%s", roomID)
	}
	fmt.Printf("✅ 直播间 %s 已成功下播！\n", roomID)
}

func handleLiveTakeover(
	ctx context.Context,
	client *api.Client,
	roomID string,
	auth *config.AuthData,
	initialSnapshot api.RoomSnapshot,
	diagnosticLog *diagnostics.Logger,
) {
	danmakuSession := tui.NewLiveDanmakuSession(ctx, client, roomID, auth.SESSDATA, auth.BiliJCT)
	defer danmakuSession.Close()

	areas, _ := client.GetLiveAreas(ctx, auth.AccessToken)
	previewer := &livePreviewer{}
	currentSettings := liveSettingsFromSnapshot(initialSnapshot)

	liveStartedAt := time.Now()
	if !initialSnapshot.LiveTime.IsZero() {
		liveStartedAt = initialSnapshot.LiveTime
	}
	overviewOpts := tui.DanmakuOverviewOptions{
		StartedAt:    liveStartedAt,
		RoomID:       roomID,
		Settings:     &currentSettings,
		Areas:        areas,
		RoomSnapshot: &initialSnapshot,
		Notice:       "当前仅接管弹幕与房间管理，不会启动本地推流。",
		LoadRoomSnapshot: func() (api.RoomSnapshot, error) {
			return client.GetRoomSnapshot(ctx, roomID)
		},
		PreviewLive: func() error {
			return previewer.Start(ctx, client, roomID, auth.SESSDATA, auth.BiliJCT)
		},
	}
	navigation, danmakuErr := tui.RunDanmaku(ctx, danmakuSession, client, roomID, auth.SESSDATA, auth.BiliJCT, nil, overviewOpts)
	if danmakuErr != nil {
		if diagnosticLog != nil {
			diagnosticLog.Printf("接管已有直播时弹幕界面异常: %v", danmakuErr)
		}
		fmt.Fprintf(os.Stderr, "弹幕界面异常: %v\n", danmakuErr)
	}
	if danmakuErr == nil && navigation == tui.NavigationQuit {
		fmt.Printf("正在结束 B 站直播……\n")
		if stopErr := stopLiveSync(client, roomID, auth.AccessToken); stopErr != nil {
			if diagnosticLog != nil {
				diagnosticLog.Printf("接管已有直播后下播失败: %v", stopErr)
			}
			fmt.Fprintf(os.Stderr, "下播失败: %v\n", stopErr)
		} else {
			if diagnosticLog != nil {
				diagnosticLog.Printf("接管已有直播后下播成功 room=%s", roomID)
			}
			fmt.Printf("✅ 直播已安全结束。\n")
		}
	}
}

func prepareAndStartLive(
	ctx context.Context,
	client *api.Client,
	roomID string,
	auth *config.AuthData,
	liveSettings *api.LiveSettings,
	savedSettings *api.LiveSettings,
	previousTags string,
	platformLiveStarted *atomic.Bool,
	rollbackStartedLive func(error) error,
	diagnosticLog *diagnostics.Logger,
) (streamruntime.Runtime, time.Time, error) {
	roomSnapshot, snapshotErr := client.GetRoomSnapshot(ctx, roomID)
	if snapshotErr != nil {
		return nil, time.Time{}, fmt.Errorf("开播前确认直播间状态失败: %w", snapshotErr)
	}
	if err := ensureRoomCanStart(roomSnapshot); err != nil {
		return nil, time.Time{}, err
	}
	if err := preflightStreamExecutable(*liveSettings); err != nil {
		return nil, time.Time{}, err
	}
	existingCover := ""
	if savedSettings != nil {
		existingCover = strings.TrimSpace(savedSettings.CoverPath)
	}
	cover := strings.TrimSpace(liveSettings.CoverPath)
	if cover == "" {
		liveSettings.CoverPath = existingCover
	} else if cover != existingCover || !isRemoteCoverURL(cover) {
		coverURL, uploadErr := uploadCover(ctx, client, roomID, auth.SESSDATA, auth.BiliJCT, cover)
		if uploadErr != nil {
			return nil, time.Time{}, uploadErr
		}
		liveSettings.CoverPath = coverURL
		if err := client.UpdatePreLiveCover(ctx, roomID, auth.SESSDATA, auth.BiliJCT, coverURL, liveSettings.Orientation); err != nil {
			return nil, time.Time{}, err
		}
	}
	if updateErr := client.UpdateLiveInfoBeforeStart(ctx, roomID, auth.AccessToken, auth.SESSDATA, auth.BiliJCT, *liveSettings); updateErr != nil {
		return nil, time.Time{}, updateErr
	}
	if tagErr := syncLiveTags(ctx, client, roomID, auth, previousTags, liveSettings); tagErr != nil {
		return nil, time.Time{}, tagErr
	}
	if strings.TrimSpace(liveSettings.Announcement) != "" {
		if err := client.UpdateRoomNews(ctx, roomID, auth.SESSDATA, auth.BiliJCT, liveSettings.Announcement); err != nil {
			return nil, time.Time{}, err
		}
	}
	addr, key, startErr := client.StartLive(ctx, roomID, auth.AccessToken, *liveSettings)
	if startErr != nil {
		return nil, time.Time{}, startErr
	}
	platformLiveStarted.Store(true)
	if ctx.Err() != nil {
		return nil, time.Time{}, rollbackStartedLive(ctx.Err())
	}
	runtime, runtimeErr := newStreamRuntime(*liveSettings)
	if runtimeErr != nil {
		return nil, time.Time{}, rollbackStartedLive(runtimeErr)
	}
	streamErr := runtime.Start(addr, key)
	if streamErr != nil {
		return nil, time.Time{}, rollbackStartedLive(streamErr)
	}
	if ctx.Err() != nil {
		cause := ctx.Err()
		if stopErr := runtime.Stop(); stopErr != nil {
			cause = fmt.Errorf("%w；停止刚启动的本地推流失败：%v", cause, stopErr)
		}
		return nil, time.Time{}, rollbackStartedLive(cause)
	}
	liveStartedAt := time.Now()
	if diagnosticLog != nil {
		diagnosticLog.Printf("直播启动成功 room=%s mode=%s", roomID, liveSettings.StreamMode)
	}
	return runtime, liveStartedAt, nil
}
