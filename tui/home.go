package tui

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"bili-live-tui/internal/api"
	streamruntime "bili-live-tui/internal/stream"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

const roomSnapshotRefreshInterval = 30 * time.Second

type roomSnapshotLoader func() (api.RoomSnapshot, error)

// RunHome 显示简洁的直播概览。仅当用户确认下播时返回 true，否则返回弹幕页。
func RunHome(_ io.Reader, _ io.Writer) (bool, error) {
	action, err := runHome(time.Now(), "", nil, nil, nil, false, "", nil, nil, nil, nil)
	return action == HomeActionStop, err
}

// RunHomeAt 是带开播时间的版本，用于用户在弹幕页停留后返回概览。
// 保留 RunHome 包装函数，以兼容不记录开播时间的原有调用方。
func RunHomeAt(startedAt time.Time, _ io.Reader, _ io.Writer) (bool, error) {
	action, err := runHome(startedAt, "", nil, nil, nil, false, "", nil, nil, nil, nil)
	return action == HomeActionStop, err
}

// RunHomeAtWithEditor 是直播主流程使用的增强概览入口。
// 选择编辑按钮时返回 HomeActionEdit，由调用方打开表单并执行认证更新，避免嵌套终端应用。
func RunHomeAtWithEditor(startedAt time.Time, settings api.LiveSettings, notice string, _ io.Reader, _ io.Writer) (HomeAction, error) {
	return runHome(startedAt, "", &settings, nil, nil, true, notice, nil, nil, nil, nil)
}

// RunHomeAtWithEditorAndInfo 是完整的直播概览入口。
// 分区列表将内部编号转换为可读名称，可选快照提供当前公开房间指标。
func RunHomeAtWithEditorAndInfo(startedAt time.Time, roomID string, settings api.LiveSettings, areas []api.LiveArea, snapshot *api.RoomSnapshot, notice string, _ io.Reader, _ io.Writer) (HomeAction, error) {
	return runHome(startedAt, roomID, &settings, areas, snapshot, true, notice, nil, nil, nil, nil)
}

// RunHomeAtWithLiveStatus 是完整的直播概览入口。
// 页面打开期间会定时调用加载器刷新房间指标，用户无需记忆刷新快捷键。
func RunHomeAtWithLiveStatus(startedAt time.Time, roomID string, settings api.LiveSettings, areas []api.LiveArea, snapshot *api.RoomSnapshot, notice string, loader func() (api.RoomSnapshot, error), onSnapshot func(api.RoomSnapshot), _ io.Reader, _ io.Writer) (HomeAction, error) {
	return runHome(startedAt, roomID, &settings, areas, snapshot, true, notice, loader, onSnapshot, nil, nil)
}

// RunHomeAtWithLiveStatusAndStats 还显示弹幕流收集的本场会话统计，例如本场收到的礼物数。
func RunHomeAtWithLiveStatusAndStats(startedAt time.Time, roomID string, settings api.LiveSettings, areas []api.LiveArea, snapshot *api.RoomSnapshot, sessionStats api.LiveSessionStats, notice string, loader func() (api.RoomSnapshot, error), onSnapshot func(api.RoomSnapshot), _ io.Reader, _ io.Writer) (HomeAction, error) {
	return runHome(startedAt, roomID, &settings, areas, snapshot, true, notice, loader, onSnapshot, &sessionStats, nil)
}

// RunHomeAtWithLiveStatusStatsAndHealth 是主流程的完整概览，
// 同时包含 B 站房间指标和本地 OBS/FFmpeg 输出状态。
func RunHomeAtWithLiveStatusStatsAndHealth(startedAt time.Time, roomID string, settings api.LiveSettings, areas []api.LiveArea, snapshot *api.RoomSnapshot, sessionStats api.LiveSessionStats, notice string, loader func() (api.RoomSnapshot, error), onSnapshot func(api.RoomSnapshot), healthLoader func() streamruntime.Health, _ io.Reader, _ io.Writer) (HomeAction, error) {
	return RunHomeAtWithLiveStatusStatsAndHealthContext(context.Background(), startedAt, roomID, settings, areas, snapshot, sessionStats, notice, loader, onSnapshot, healthLoader, nil, nil)
}

// RunHomeAtWithLiveStatusStatsAndHealthContext 是主流程使用的信号感知概览。
// 取消 ctx 会停止 TUI，使调用方即使收到 SIGTERM 也能执行 OBS 和 B 站清理。
func RunHomeAtWithLiveStatusStatsAndHealthContext(ctx context.Context, startedAt time.Time, roomID string, settings api.LiveSettings, areas []api.LiveArea, snapshot *api.RoomSnapshot, sessionStats api.LiveSessionStats, notice string, loader func() (api.RoomSnapshot, error), onSnapshot func(api.RoomSnapshot), healthLoader func() streamruntime.Health, _ io.Reader, _ io.Writer) (HomeAction, error) {
	return runHomeWithContext(ctx, startedAt, roomID, &settings, areas, snapshot, true, notice, loader, onSnapshot, &sessionStats, nil, healthLoader, nil, nil)
}

// RunHomeAtWithLiveStatusStatsAndHealthContextAndEditor 在概览内执行资料编辑，避免网络操作期间退出终端界面。
// 可选的 statsLoader 会定时读取长期弹幕连接的最新会话统计。
func RunHomeAtWithLiveStatusStatsAndHealthContextAndEditor(ctx context.Context, startedAt time.Time, roomID string, settings *api.LiveSettings, areas []api.LiveArea, snapshot *api.RoomSnapshot, sessionStats api.LiveSessionStats, notice string, loader func() (api.RoomSnapshot, error), onSnapshot func(api.RoomSnapshot), healthLoader func() streamruntime.Health, edit func() error, input io.Reader, output io.Writer, statsLoaders ...func() api.LiveSessionStats) (HomeAction, error) {
	return RunHomeAtWithLiveStatusStatsAndHealthContextAndEditorAndPreview(ctx, startedAt, roomID, settings, areas, snapshot, sessionStats, notice, loader, onSnapshot, healthLoader, edit, nil, input, output, statsLoaders...)
}

// RunHomeAtWithLiveStatusStatsAndHealthContextAndEditorAndPreview 还提供可选的外部播放器预览入口。
func RunHomeAtWithLiveStatusStatsAndHealthContextAndEditorAndPreview(ctx context.Context, startedAt time.Time, roomID string, settings *api.LiveSettings, areas []api.LiveArea, snapshot *api.RoomSnapshot, sessionStats api.LiveSessionStats, notice string, loader func() (api.RoomSnapshot, error), onSnapshot func(api.RoomSnapshot), healthLoader func() streamruntime.Health, edit, preview func() error, _ io.Reader, _ io.Writer, statsLoaders ...func() api.LiveSessionStats) (HomeAction, error) {
	var statsLoader func() api.LiveSessionStats
	if len(statsLoaders) > 0 {
		statsLoader = statsLoaders[0]
	}
	return runHomeWithContext(ctx, startedAt, roomID, settings, areas, snapshot, true, notice, loader, onSnapshot, &sessionStats, statsLoader, healthLoader, edit, preview)
}

func runHome(startedAt time.Time, roomID string, settings *api.LiveSettings, areas []api.LiveArea, snapshot *api.RoomSnapshot, showEdit bool, notice string, loader roomSnapshotLoader, onSnapshot func(api.RoomSnapshot), sessionStats *api.LiveSessionStats, healthLoader func() streamruntime.Health) (HomeAction, error) {
	return runHomeWithContext(context.Background(), startedAt, roomID, settings, areas, snapshot, showEdit, notice, loader, onSnapshot, sessionStats, nil, healthLoader, nil, nil)
}

func runHomeWithContext(ctx context.Context, startedAt time.Time, roomID string, settings *api.LiveSettings, areas []api.LiveArea, snapshot *api.RoomSnapshot, showEdit bool, notice string, loader roomSnapshotLoader, onSnapshot func(api.RoomSnapshot), sessionStats *api.LiveSessionStats, statsLoader func() api.LiveSessionStats, healthLoader func() streamruntime.Health, edit, preview func() error) (HomeAction, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	applyTheme()
	app := tview.NewApplication().EnableMouse(true).EnablePaste(true).SetTitle("bili-live-tui")
	var applicationRunning atomic.Bool
	applicationRunning.Store(true)
	refreshDone := make(chan struct{})
	var stopRefresh sync.Once
	stopApplication := func() {
		applicationRunning.Store(false)
		stopRefresh.Do(func() { close(refreshDone) })
		app.Stop()
	}
	action := HomeActionDanmaku
	if startedAt.IsZero() {
		startedAt = time.Now()
	}

	status := tview.NewTextView()
	status.SetDynamicColors(true)
	status.SetTextAlign(tview.AlignCenter)
	status.SetBackgroundColor(panelColor)
	status.SetBorder(true)
	status.SetBorderColor(tview.Styles.BorderColor)
	status.SetTitle(" ♡ 直播概览 ♡ ")
	status.SetTitleColor(tview.Styles.TitleColor)
	currentSnapshot := snapshot
	setStatusText := func() {
		if statsLoader != nil {
			latest := statsLoader()
			sessionStats = &latest
		}
		statusText := fmt.Sprintf("[%s]直播中[-]\n\n开播时间  %s\n\n直播时长  %s", accentColor.String(), startedAt.Format("15:04:05"), formatLiveDuration(time.Since(startedAt)))
		if settings != nil {
			statusText += "\n\n" + liveInfoSummaryWithStats(roomID, *settings, areas, currentSnapshot, sessionStats)
		}
		if healthLoader != nil {
			statusText += "\n\n" + formatStreamHealth(healthLoader())
		}
		if message := strings.TrimSpace(notice); message != "" {
			statusText += "\n\n[" + mutedColor.String() + "]" + tview.Escape(message) + "[-]"
		}
		status.SetText(statusText)
	}
	setStatusText()

	pages := tview.NewPages()
	var actionBar *tview.Flex

	confirm := styleModal(tview.NewModal()).
		SetText("确定下播并退出吗？").
		AddButtons([]string{"取消", "下播并退出"})
	confirm.SetDoneFunc(func(buttonIndex int, _ string) {
		if buttonIndex == 1 {
			action = HomeActionStop
			stopApplication()
			return
		}
		pages.HidePage("confirm-stop")
		app.SetFocus(actionBar)
	})

	buttons := make([]*tview.Button, 0, 4)
	buttons = append(buttons, newHomeActionButton("返回弹幕", stopApplication))
	if preview != nil {
		var previewBusy atomic.Bool
		buttons = append(buttons, newHomeActionButton("预览直播", func() {
			if !previewBusy.CompareAndSwap(false, true) {
				return
			}
			notice = "正在打开直播预览……"
			setStatusText()
			go func() {
				err := preview()
				if !applicationRunning.Load() {
					return
				}
				app.QueueUpdateDraw(func() {
					previewBusy.Store(false)
					if err != nil {
						notice = err.Error()
					} else {
						notice = "直播预览已打开；默认静音，可在 mpv 中按 m 开启声音。"
					}
					setStatusText()
				})
			}()
		}))
	}
	if showEdit {
		buttons = append(buttons, newHomeActionButton("修改资料", func() {
			if edit != nil {
				app.Suspend(func() {
					if err := edit(); err != nil {
						notice = "保存直播资料失败：" + err.Error()
					} else {
						notice = "直播资料已更新"
					}
					setStatusText()
				})
				return
			}
			action = HomeActionEdit
			stopApplication()
		}))
	}
	buttons = append(buttons, newHomeActionButton("下播退出", func() {
		pages.ShowPage("confirm-stop")
		app.SetFocus(confirm)
	}))
	actionBar = centeredActionBar(buttons)

	body := tview.NewFlex()
	body.SetDirection(tview.FlexRow)
	body.SetBackgroundColor(panelColor)
	body.AddItem(status, 0, 1, true)
	body.AddItem(actionBar, 1, 0, true)
	footerText := "Esc 下播确认"
	if loader != nil {
		footerText = "房间状态每 30 秒自动刷新　Esc 下播确认"
	}
	if healthLoader != nil || statsLoader != nil {
		go func() {
			ticker := time.NewTicker(2 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					if !applicationRunning.Load() {
						return
					}
					if applicationRunning.Load() {
						app.QueueUpdateDraw(func() {
							if applicationRunning.Load() {
								setStatusText()
							}
						})
					}
				case <-refreshDone:
					return
				}
			}
		}()
	}
	root := centeredPage(
		nil,
		body,
		pageFooter(footerText+"　Tab 选择　Enter 执行　Esc 下播确认　Ctrl+C 退出"),
	)

	pages.AddPage("main", root, true, true)
	pages.AddPage("confirm-stop", confirm, true, false)
	if loader != nil {
		ticker := time.NewTicker(roomSnapshotRefreshInterval)
		refresh := func() {
			fresh, refreshErr := loader()
			select {
			case <-refreshDone:
				return
			default:
			}
			if !applicationRunning.Load() {
				return
			}
			app.QueueUpdateDraw(func() {
				if !applicationRunning.Load() {
					return
				}
				if refreshErr != nil {
					notice = "房间实时状态暂不可用：" + refreshErr.Error()
				} else {
					currentSnapshot = &fresh
					if onSnapshot != nil {
						onSnapshot(fresh)
					}
					notice = ""
				}
				setStatusText()
			})
		}
		go func() {
			defer ticker.Stop()
			// 页面打开后立即获取一次，之后保持 30 秒刷新周期。
			// 网络请求在后台执行，不阻塞从弹幕页进入概览。
			go refresh()
			for {
				select {
				case <-ticker.C:
					go refresh()
				case <-refreshDone:
					return
				}
			}
		}()
	}
	defer func() {
		applicationRunning.Store(false)
		stopRefresh.Do(func() { close(refreshDone) })
	}()
	viewDone := make(chan struct{})
	defer close(viewDone)
	go func() {
		select {
		case <-ctx.Done():
			stopApplication()
		case <-viewDone:
		}
	}()

	app.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch event.Key() {
		case tcell.KeyTab, tcell.KeyBacktab:
			// 按钮是普通控件而非 Form 项，因此显式循环焦点，让概览中的 Tab 行为稳定；弹窗焦点不处理。
			for index, button := range buttons {
				if app.GetFocus() != button {
					continue
				}
				next := index + 1
				if event.Key() == tcell.KeyBacktab {
					next = index - 1
				}
				if next < 0 {
					next = len(buttons) - 1
				} else if next >= len(buttons) {
					next = 0
				}
				app.SetFocus(buttons[next])
				return nil
			}
			return event
		case tcell.KeyEscape:
			// Modal.Focus 会把焦点交给内部表单；只检查弹窗控件本身会漏掉这里并反复打开弹窗。
			if confirm.HasFocus() {
				pages.HidePage("confirm-stop")
				app.SetFocus(actionBar)
				return nil
			}
			pages.ShowPage("confirm-stop")
			app.SetFocus(confirm)
			return nil
		case tcell.KeyCtrlC:
			action = HomeActionStop
			stopApplication()
			return nil
		default:
			return event
		}
	})
	if err := app.SetRoot(pages, true).SetFocus(buttons[0]).Run(); err != nil {
		return HomeActionDanmaku, fmt.Errorf("启动直播概览失败: %w", err)
	}
	return action, nil
}

func formatStreamHealth(health streamruntime.Health) string {
	mode := "本地推流"
	switch health.Mode {
	case streamruntime.ModeOBS:
		mode = "OBS"
	case streamruntime.ModeFFmpegTest:
		mode = "FFmpeg 测试源"
	}
	state := "已停止"
	color := mutedColor.String()
	if health.Reconnecting {
		state = "正在重连"
		color = "#d68a4b"
	} else if health.Active {
		state = "推流正常"
		color = accentColor.String()
	}
	if health.LastError != "" {
		if health.Reconnecting {
			return fmt.Sprintf("[%s]%s%s[-] · %s", color, mode, state, tview.Escape(health.LastError))
		}
		return fmt.Sprintf("[%s]%s异常[-] · %s", errorColor.String(), mode, tview.Escape(health.LastError))
	}
	parts := []string{fmt.Sprintf("[%s]%s %s[-]", color, mode, state)}
	if health.Active && health.BitrateKbps > 0 {
		parts = append(parts, fmt.Sprintf("%.0f kbps", health.BitrateKbps))
	}
	if health.Active && health.FPS > 0 {
		parts = append(parts, fmt.Sprintf("%.1f FPS", health.FPS))
	}
	if health.Active && health.SkippedFrames > 0 {
		dropped := fmt.Sprintf("掉帧 %d 帧", health.SkippedFrames)
		if health.TotalFrames > 0 {
			dropped += fmt.Sprintf("（%.2f%%）", float64(health.SkippedFrames)*100/float64(health.TotalFrames))
		}
		parts = append(parts, dropped)
	}
	if health.Active && health.CPUPercent > 0 {
		parts = append(parts, fmt.Sprintf("CPU %.1f%%", health.CPUPercent))
	}
	return strings.Join(parts, " · ")
}

func liveInfoSummary(roomID string, settings api.LiveSettings, areas []api.LiveArea, snapshot *api.RoomSnapshot) string {
	return liveInfoSummaryWithStats(roomID, settings, areas, snapshot, nil)
}

func liveInfoSummaryWithStats(roomID string, settings api.LiveSettings, areas []api.LiveArea, snapshot *api.RoomSnapshot, sessionStats *api.LiveSessionStats) string {
	labelColor := mutedColor.String()
	if strings.TrimSpace(roomID) == "" && snapshot != nil {
		roomID = snapshot.RoomID
	}
	areaName := areaNameForID(settings.AreaID, areas)
	if areaName == "" && snapshot != nil {
		areaName = strings.TrimSpace(snapshot.AreaName)
		if parent := strings.TrimSpace(snapshot.ParentAreaName); parent != "" && areaName != "" {
			areaName = parent + " / " + areaName
		}
	}
	if areaName == "" {
		areaName = "暂未获取"
	}
	cover := "使用房间默认封面"
	if strings.TrimSpace(settings.CoverPath) != "" || (snapshot != nil && strings.TrimSpace(snapshot.Cover) != "") {
		cover = "已设置（已上传）"
	}
	lines := []string{
		fmt.Sprintf("[%s]房间号[-]　%s", labelColor, summaryValue(roomID, "暂未获取")),
		fmt.Sprintf("[%s]标题[-]　%s", labelColor, summaryValue(settings.Title, "未设置")),
		fmt.Sprintf("[%s]简介[-]　%s", labelColor, summaryValue(settings.Description, "暂无简介")),
		fmt.Sprintf("[%s]标签[-]　%s", labelColor, summaryValue(settings.Tags, "暂无标签")),
		fmt.Sprintf("[%s]分区[-]　%s", labelColor, tview.Escape(areaName)),
		fmt.Sprintf("[%s]封面[-]　%s", labelColor, cover),
	}
	if sessionStats != nil {
		giftSummary := "暂未收到"
		if sessionStats.GiftCount > 0 {
			giftSummary = fmt.Sprintf("%d 次 / 共 %d 个", sessionStats.GiftEvents, sessionStats.GiftCount)
		}
		lines = append(lines, fmt.Sprintf("[%s]本场礼物[-]　%s", labelColor, giftSummary))
	}
	if snapshot != nil {
		if sessionStats != nil && sessionStats.PopularityKnown {
			lines = append(lines, fmt.Sprintf("[%s]当前人气[-]　%s", labelColor, formatMetric(sessionStats.Popularity, true, "")))
		} else if snapshot.OnlineKnown {
			lines = append(lines, fmt.Sprintf("[%s]当前人气[-]　%s", labelColor, formatMetric(snapshot.Online, true, "")))
		}
		if snapshot.WatchedKnown {
			lines = append(lines, fmt.Sprintf("[%s]累计观看[-]　%s", labelColor, formatMetric(snapshot.Watched, true, "")))
		}
	}
	return strings.Join(lines, "\n")
}

func areaNameForID(id string, areas []api.LiveArea) string {
	id = strings.TrimSpace(id)
	for _, area := range areas {
		if strings.TrimSpace(area.ID) != id {
			continue
		}
		name := strings.TrimSpace(area.Name)
		if parent := strings.TrimSpace(area.ParentName); parent != "" && name != "" {
			return parent + " / " + name
		}
		return name
	}
	return ""
}

func formatMetric(value int64, known bool, fallback string) string {
	if !known || value < 0 {
		return fallback
	}
	return strconv.FormatInt(value, 10)
}

func formatLiveDuration(duration time.Duration) string {
	if duration < 0 {
		duration = 0
	}
	totalSeconds := int64(duration / time.Second)
	hours := totalSeconds / 3600
	minutes := (totalSeconds % 3600) / 60
	seconds := totalSeconds % 60
	return fmt.Sprintf("%02d:%02d:%02d", hours, minutes, seconds)
}

func summaryValue(value, fallback string) string {
	value = strings.TrimSpace(strings.Join(strings.Fields(value), " "))
	if value == "" {
		return fallback
	}
	return tview.Escape(value)
}

func newHomeActionButton(label string, selected func()) *tview.Button {
	return tview.NewButton(label).
		SetSelectedFunc(selected).
		SetStyle(tcell.StyleDefault.
			Background(accentColor).
			Foreground(buttonTextColor)).
		SetActivatedStyle(tcell.StyleDefault.
			Background(accentActiveColor).
			Foreground(buttonActiveTextColor).
			Bold(true))
}

func centeredActionBar(buttons []*tview.Button) *tview.Flex {
	bar := tview.NewFlex()
	bar.SetDirection(tview.FlexColumn)
	bar.SetBackgroundColor(panelColor)

	if len(buttons) == 0 {
		return bar
	}

	buttonWidth := 0
	for _, button := range buttons {
		// 最长标签两侧各保留一个显示单元格的空隙。
		// 这里比 tview.Button 默认四格边距更紧凑，避免操作区占据概览过多空间。
		if width := tview.TaggedStringWidth(button.GetLabel()) + 2; width > buttonWidth {
			buttonWidth = width
		}
	}

	bar.AddItem(nil, 0, 1, false)
	for index, button := range buttons {
		bar.AddItem(button, buttonWidth, 0, index == 0)
		if index < len(buttons)-1 {
			bar.AddItem(nil, 1, 0, false)
		}
	}
	bar.AddItem(nil, 0, 1, false)
	return bar
}
