package tui

import (
	"context"
	"fmt"
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

type displayOnlyPrimitive struct {
	tview.Primitive
}

func (primitive *displayOnlyPrimitive) InputHandler() func(*tcell.EventKey, func(tview.Primitive)) {
	return nil
}

func (primitive *displayOnlyPrimitive) Focus(func(tview.Primitive)) {}

func (primitive *displayOnlyPrimitive) HasFocus() bool {
	return false
}

func (primitive *displayOnlyPrimitive) MouseHandler() func(tview.MouseAction, *tcell.EventMouse, func(tview.Primitive)) (bool, tview.Primitive) {
	return func(tview.MouseAction, *tcell.EventMouse, func(tview.Primitive)) (bool, tview.Primitive) {
		return false, nil
	}
}

func (primitive *displayOnlyPrimitive) PasteHandler() func(string, func(tview.Primitive)) {
	return nil
}

// DanmakuOverviewOptions 包含在弹幕工作区中内嵌直播概览所需的配置与回调。
type DanmakuOverviewOptions struct {
	StartedAt        time.Time
	RoomID           string
	Settings         *api.LiveSettings
	Areas            []api.LiveArea
	RoomSnapshot     *api.RoomSnapshot
	Notice           string
	LoadRoomSnapshot func() (api.RoomSnapshot, error)
	OnSnapshot       func(api.RoomSnapshot)
	HealthLoader     func() streamruntime.Health
	SaveEdit         func(api.LiveSettings) (api.LiveSettings, error)
	PreviewLive      func() error
}

type homeWorkspaceComponents struct {
	root          tview.Primitive
	actionBar     *tview.Flex
	buttons       []*tview.Button
	setStatusText func()
	isEditing     func() bool
	cancelEditing func()
	stopRefresh   func()
}

func newHomeWorkspace(
	ctx context.Context,
	app *tview.Application,
	pages *tview.Pages,
	returnPageName string,
	opts DanmakuOverviewOptions,
	statsLoader func() api.LiveSessionStats,
	onReturnToDanmaku func(),
	onStopLive func(),
) *homeWorkspaceComponents {
	if ctx == nil {
		ctx = context.Background()
	}
	startedAt := opts.StartedAt
	if startedAt.IsZero() {
		startedAt = time.Now()
	}
	roomID := opts.RoomID
	settings := opts.Settings
	areas := opts.Areas
	currentSnapshot := opts.RoomSnapshot
	notice := opts.Notice
	healthLoader := opts.HealthLoader
	loader := opts.LoadRoomSnapshot
	onSnapshot := opts.OnSnapshot
	saveEdit := opts.SaveEdit
	preview := opts.PreviewLive

	var currentSessionStats *api.LiveSessionStats
	if statsLoader != nil {
		latest := statsLoader()
		currentSessionStats = &latest
	}

	var applicationRunning atomic.Bool
	applicationRunning.Store(true)
	refreshDone := make(chan struct{})
	var stopRefresh sync.Once
	stop := func() {
		applicationRunning.Store(false)
		stopRefresh.Do(func() { close(refreshDone) })
	}

	status := tview.NewTextView()
	status.SetDynamicColors(true)
	status.SetTextAlign(tview.AlignCenter)
	status.SetBackgroundColor(panelColor)
	status.SetBorder(true)
	status.SetBorderColor(tview.Styles.BorderColor)
	status.SetTitle(" ♡ 直播概览 ♡ ")
	status.SetTitleColor(tview.Styles.TitleColor)

	noticeView := tview.NewTextView()
	noticeView.SetDynamicColors(true)
	noticeView.SetTextAlign(tview.AlignCenter)
	noticeView.SetWrap(false)
	noticeView.SetBackgroundColor(panelColor)

	statusDisplay := &displayOnlyPrimitive{Primitive: status}
	noticeDisplay := &displayOnlyPrimitive{Primitive: noticeView}
	roomNotice := ""

	var body *tview.Flex
	setStatusText := func() {
		if statsLoader != nil {
			latest := statsLoader()
			currentSessionStats = &latest
		}
		statusText := fmt.Sprintf("[%s]直播中[-]\n\n开播时间  %s\n\n直播时长  %s", accentColor.String(), startedAt.Format("15:04:05"), formatLiveDuration(time.Since(startedAt)))
		if settings != nil {
			statusText += "\n\n" + liveInfoSummaryWithStats(roomID, *settings, areas, currentSnapshot, currentSessionStats)
		}
		if healthLoader != nil {
			statusText += "\n\n" + formatStreamHealth(healthLoader())
		}
		status.SetText(statusText)
		message := strings.TrimSpace(notice)
		if message == "" {
			message = strings.TrimSpace(roomNotice)
		}
		if message == "" {
			noticeView.SetText("")
		} else {
			noticeView.SetText("[" + mutedColor.String() + "]" + tview.Escape(message) + "[-]")
		}
		if body != nil {
			body.ResizeItem(noticeDisplay, noticeRowHeight(message), 0)
		}
	}

	var actionBar *tview.Flex
	editing := false
	var cancelEdit func()
	var previewBusy atomic.Bool
	var startPreview func()

	buttons := make([]*tview.Button, 0, 4)
	buttons = append(buttons, newActionButton("返回弹幕", func() {
		if onReturnToDanmaku != nil {
			onReturnToDanmaku()
		}
	}))

	var previewBtn *tview.Button
	if preview != nil {
		startPreview = func() {
			if !previewBusy.CompareAndSwap(false, true) {
				return
			}
			notice = "正在等待直播画面……"
			setStatusText()
			go func() {
				err := preview()
				if !applicationRunning.Load() {
					return
				}
				app.QueueUpdateDraw(func() {
					previewBusy.Store(false)
					if err != nil {
						if missing := executableNotFound(err); missing != nil {
							showExecutablePathPage(app, pages, missing, func() {
								if previewBtn != nil {
									app.SetFocus(previewBtn)
								} else {
									app.SetFocus(actionBar)
								}
								startPreview()
							}, func() {
								if previewBtn != nil {
									app.SetFocus(previewBtn)
								} else {
									app.SetFocus(actionBar)
								}
								notice = "尚未设置 " + missing.DisplayName + " 可执行文件路径"
								setStatusText()
							})
							return
						}
						notice = err.Error()
					} else {
						notice = ""
					}
					setStatusText()
				})
			}()
		}
		previewBtn = newActionButton("预览直播", startPreview)
		buttons = append(buttons, previewBtn)
	}

	if saveEdit != nil && settings != nil {
		var editBtn *tview.Button
		editBtn = newActionButton("修改资料", func() {
			var saving atomic.Bool
			var editPage *liveEditPage
			closeEdit := func() {
				if saving.Load() {
					editPage.setStatus("正在保存，请稍候", false)
					return
				}
				editing = false
				cancelEdit = nil
				app.SetBeforeDrawFunc(nil)
				pages.RemovePage("home-edit")
				pages.SwitchToPage(returnPageName)
				if editBtn != nil {
					app.SetFocus(editBtn)
				} else {
					app.SetFocus(actionBar)
				}
			}
			editPage = newLiveEditPage(app, *settings, areas, func(edited api.LiveSettings) {
				if edited == *settings {
					editPage.setStatus(ErrLiveEditUnchanged.Error(), false)
					return
				}
				if !saving.CompareAndSwap(false, true) {
					return
				}
				editPage.setStatus("正在保存直播资料……", false)
				go func() {
					updated, err := saveEdit(edited)
					if !applicationRunning.Load() {
						return
					}
					app.QueueUpdateDraw(func() {
						saving.Store(false)
						if err != nil {
							editPage.setStatus("保存失败："+err.Error(), true)
							return
						}
						*settings = updated
						if currentSnapshot != nil {
							currentSnapshot.Title = updated.Title
							currentSnapshot.Description = updated.Description
							currentSnapshot.Tags = updated.Tags
							currentSnapshot.AreaName = ""
							currentSnapshot.ParentAreaName = ""
							currentSnapshot.Cover = updated.CoverPath
						}
						notice = "直播资料更新请求已提交"
						closeEdit()
						setStatusText()
					})
				}()
			}, closeEdit)
			editing = true
			cancelEdit = closeEdit
			pages.AddAndSwitchToPage("home-edit", editPage.root, true)
			app.SetFocus(editPage.form)
		})
		buttons = append(buttons, editBtn)
	}

	buttons = append(buttons, newActionButton("下播退出", func() {
		if onStopLive != nil {
			onStopLive()
		}
	}))

	actionBar = centeredActionBar(buttons)

	body = tview.NewFlex()
	body.SetDirection(tview.FlexRow)
	body.SetBackgroundColor(panelColor)
	body.AddItem(statusDisplay, 0, 1, false)
	body.AddItem(noticeDisplay, 0, 0, false)
	body.AddItem(actionBar, 1, 0, true)
	setStatusText()

	footerText := "Tab 选择　Enter 执行　Esc / Alt+H 返回弹幕"
	root := centeredPage(
		nil,
		body,
		pageFooter(footerText),
	)

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
					app.QueueUpdateDraw(func() {
						if applicationRunning.Load() {
							setStatusText()
						}
					})
				case <-refreshDone:
					return
				case <-ctx.Done():
					return
				}
			}
		}()
	}

	if loader != nil {
		ticker := time.NewTicker(roomSnapshotRefreshInterval)
		refresh := func() {
			fresh, refreshErr := loader()
			select {
			case <-refreshDone:
				return
			case <-ctx.Done():
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
					roomNotice = "房间实时状态暂不可用：" + refreshErr.Error()
				} else {
					currentSnapshot = &fresh
					if onSnapshot != nil {
						onSnapshot(fresh)
					}
					roomNotice = ""
				}
				setStatusText()
			})
		}
		go func() {
			defer ticker.Stop()
			go refresh()
			for {
				select {
				case <-ticker.C:
					go refresh()
				case <-refreshDone:
					return
				case <-ctx.Done():
					return
				}
			}
		}()
	}

	return &homeWorkspaceComponents{
		root:          root,
		actionBar:     actionBar,
		buttons:       buttons,
		setStatusText: setStatusText,
		isEditing:     func() bool { return editing },
		cancelEditing: func() {
			if cancelEdit != nil {
				cancelEdit()
			}
		},
		stopRefresh: stop,
	}
}

// RunHome 显示直播概览，并在同一个 TUI 中处理资料编辑和直播预览。
func RunHome(ctx context.Context, startedAt time.Time, roomID string, settings *api.LiveSettings, areas []api.LiveArea, snapshot *api.RoomSnapshot, sessionStats api.LiveSessionStats, notice string, loader func() (api.RoomSnapshot, error), onSnapshot func(api.RoomSnapshot), healthLoader func() streamruntime.Health, saveEdit func(api.LiveSettings) (api.LiveSettings, error), preview func() error, statsLoader func() api.LiveSessionStats) (HomeAction, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	applyTheme()
	app := tview.NewApplication().EnableMouse(true).EnablePaste(true).SetTitle("bili-live-tui")
	pages := tview.NewPages()
	action := HomeActionDanmaku

	stopApplication := func() {
		app.Stop()
	}

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
		app.SetFocus(pages)
	})

	opts := DanmakuOverviewOptions{
		StartedAt:        startedAt,
		RoomID:           roomID,
		Settings:         settings,
		Areas:            areas,
		RoomSnapshot:     snapshot,
		Notice:           notice,
		LoadRoomSnapshot: loader,
		OnSnapshot:       onSnapshot,
		HealthLoader:     healthLoader,
		SaveEdit:         saveEdit,
		PreviewLive:      preview,
	}

	ws := newHomeWorkspace(
		ctx,
		app,
		pages,
		"main",
		opts,
		statsLoader,
		func() {
			action = HomeActionDanmaku
			stopApplication()
		},
		func() {
			pages.ShowPage("confirm-stop")
			app.SetFocus(confirm)
		},
	)
	defer ws.stopRefresh()

	pages.AddPage("main", ws.root, true, true)
	pages.AddPage("confirm-stop", confirm, true, false)

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
		if pages.HasPage(executablePathPageName) && event.Key() != tcell.KeyCtrlC {
			return event
		}
		if ws.isEditing != nil && ws.isEditing() {
			return event
		}
		if confirm.HasFocus() {
			if event.Key() == tcell.KeyEscape {
				pages.HidePage("confirm-stop")
				app.SetFocus(ws.actionBar)
				return nil
			}
			return event
		}
		switch event.Key() {
		case tcell.KeyTab, tcell.KeyBacktab, tcell.KeyRight, tcell.KeyLeft:
			isPrev := event.Key() == tcell.KeyBacktab || event.Key() == tcell.KeyLeft
			for index, button := range ws.buttons {
				if app.GetFocus() != button {
					continue
				}
				next := index + 1
				if isPrev {
					next = index - 1
				}
				if next < 0 {
					next = len(ws.buttons) - 1
				} else if next >= len(ws.buttons) {
					next = 0
				}
				app.SetFocus(ws.buttons[next])
				return nil
			}
			if len(ws.buttons) > 0 {
				next := 0
				if isPrev {
					next = len(ws.buttons) - 1
				}
				app.SetFocus(ws.buttons[next])
			}
			return nil
		case tcell.KeyEscape:
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

	if err := app.SetRoot(pages, true).SetFocus(ws.buttons[0]).Run(); err != nil {
		return HomeActionDanmaku, fmt.Errorf("启动直播概览失败: %w", err)
	}
	return action, nil
}

func noticeRowHeight(message string) int {
	if strings.TrimSpace(message) == "" {
		return 0
	}
	return 1
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
		if strings.Contains(health.LastError, "正在确认") {
			state = "正在确认"
		}
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
			lines = append(lines, fmt.Sprintf("[%s]当前人气[-]　%d", labelColor, sessionStats.Popularity))
		} else if snapshot.OnlineKnown {
			lines = append(lines, fmt.Sprintf("[%s]当前人气[-]　%d", labelColor, snapshot.Online))
		}
		if snapshot.WatchedKnown {
			lines = append(lines, fmt.Sprintf("[%s]累计观看[-]　%d", labelColor, snapshot.Watched))
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

func newActionButton(label string, selected func()) *tview.Button {
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

func populateCenteredActionBar(bar *tview.Flex, buttons []*tview.Button) {
	bar.Clear()
	if len(buttons) == 0 {
		return
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
}

func centeredActionBar(buttons []*tview.Button) *tview.Flex {
	bar := tview.NewFlex()
	bar.SetDirection(tview.FlexColumn)
	bar.SetBackgroundColor(panelColor)
	populateCenteredActionBar(bar, buttons)
	return bar
}
