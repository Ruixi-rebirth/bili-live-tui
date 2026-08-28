package tui

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"

	"bili-live-tui/internal/api"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// ErrLiveSettingsCancelled 用于区分用户主动放弃开播和设置/网络错误。
var ErrLiveSettingsCancelled = errors.New("已取消设置开播信息")

// RunLiveSettings 在调用方执行真实开播流程时保持设置页面可见。
// 回调可以规范化设置（例如把本地封面路径替换为上传后的地址），成功后返回更新值。
// 将网络操作放在本次 TUI 生命周期内，避免设置页和弹幕页之间闪回终端外壳。
func RunLiveSettings(ctx context.Context, areas []api.LiveArea, initial *api.LiveSettings, submit func(*api.LiveSettings) error) (api.LiveSettings, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	applyTheme()
	app := tview.NewApplication().EnableMouse(true).EnablePaste(true).SetTitle("bili-live-tui")

	form, state := newLiveFormWithOptions(areas, initial, "开播信息", true)
	form.SetBorderPadding(0, 0, 1, 1)
	status := tview.NewTextView()
	status.SetDynamicColors(true)
	status.SetTextColor(mutedColor)
	status.SetTextAlign(tview.AlignCenter)
	status.SetWrap(true)
	status.SetWordWrap(false)
	status.SetBackgroundColor(tview.Styles.PrimitiveBackgroundColor)

	var result api.LiveSettings
	cancelled := false
	var busy atomic.Bool
	setStatus := func(message string, isError bool) {
		color := mutedColor
		if isError {
			color = errorColor
		}
		status.SetText("[" + color.String() + "]" + tview.Escape(message) + "[-]")
	}

	pages := tview.NewPages()
	cancelLive := func() {
		if busy.Load() {
			cancelled = true
			setStatus("正在结束当前启动步骤，随后取消开播……", false)
			return
		}
		cancelled = true
		app.Stop()
	}
	var startSubmit func(api.LiveSettings)
	startSubmit = func(settings api.LiveSettings) {
		if submit == nil {
			result = settings
			app.Stop()
			return
		}
		if !busy.CompareAndSwap(false, true) {
			return
		}
		setStatus("正在准备直播，请稍候……", false)
		go func() {
			err := submit(&settings)
			app.QueueUpdateDraw(func() {
				busy.Store(false)
				if cancelled || ctx.Err() != nil {
					app.Stop()
					return
				}
				if err != nil {
					if missing := executableNotFound(err); missing != nil {
						showExecutablePathPage(app, pages, missing, func() {
							app.SetFocus(form)
							startSubmit(settings)
						}, func() {
							app.SetFocus(form)
							setStatus("尚未设置 "+missing.DisplayName+" 可执行文件路径", true)
						})
						return
					}
					setStatus("启动直播失败："+err.Error(), true)
					return
				}
				result = settings
				app.Stop()
			})
		}()
	}
	startLive := func() {
		settings := state.settings()
		if err := settings.Validate(); err != nil {
			setStatus(err.Error(), true)
			return
		}
		if err := validateCoverInput(settings.CoverPath, state.hasExistingCover); err != nil {
			setStatus(err.Error(), true)
			return
		}
		startSubmit(settings)
	}
	startButton := newActionButton("开始直播", startLive).
		SetStyle(actionButtonStyle(false).Bold(true))
	cancelButton := newActionButton("取消开播", cancelLive)
	buttons := centeredActionBar([]*tview.Button{startButton, cancelButton})
	buttons.SetBackgroundColor(tview.Styles.PrimitiveBackgroundColor)

	// 配置、操作和状态各自独立：长错误只在底部状态区换行，不会撑开表单或推动按钮。
	body := tview.NewFlex()
	body.SetDirection(tview.FlexRow)
	body.SetBackgroundColor(tview.Styles.PrimitiveBackgroundColor)
	body.AddItem(form, 1, 0, true)
	body.AddItem(nil, 1, 0, false)
	body.AddItem(buttons, 1, 0, true)
	body.AddItem(status, 3, 0, false)
	configureStartLiveForm(app, body, form, state.description)
	root := tallWideFormPage(
		pageHeader("设置开播信息", "填写房间资料，确认后立即开始直播"),
		body,
		pageFooter("Tab 切换　Enter 确认　Ctrl+U 清空当前项　Esc/Ctrl+C 取消开播　支持鼠标点击"),
	)
	pages.AddPage("main", root, true, true)

	form.SetCancelFunc(cancelLive)
	startButton.SetExitFunc(func(key tcell.Key) {
		switch key {
		case tcell.KeyTab:
			app.SetFocus(cancelButton)
		case tcell.KeyBacktab:
			focusLastLiveFormItem(app, form, state)
		case tcell.KeyEscape:
			cancelLive()
		}
	})
	cancelButton.SetExitFunc(func(key tcell.Key) {
		switch key {
		case tcell.KeyTab:
			form.SetFocus(0)
			app.SetFocus(form)
		case tcell.KeyBacktab:
			app.SetFocus(startButton)
		case tcell.KeyEscape:
			cancelLive()
		}
	})

	app.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyCtrlC {
			cancelLive()
			return nil
		}
		if event.Key() == tcell.KeyBacktab && state.title.HasFocus() {
			app.SetFocus(cancelButton)
			return nil
		}
		if event.Key() == tcell.KeyTab || event.Key() == tcell.KeyEnter {
			if state.obsPassword.HasFocus() || (formItemIndex(form, state.obsGroup) < 0 && state.streamMode.HasFocus() && event.Key() == tcell.KeyTab) {
				app.SetFocus(startButton)
				return nil
			}
		}
		if startButton.HasFocus() {
			switch event.Key() {
			case tcell.KeyRight, tcell.KeyLeft:
				app.SetFocus(cancelButton)
				return nil
			case tcell.KeyUp:
				focusLastLiveFormItem(app, form, state)
				return nil
			}
		}
		if cancelButton.HasFocus() {
			switch event.Key() {
			case tcell.KeyLeft, tcell.KeyRight:
				app.SetFocus(startButton)
				return nil
			case tcell.KeyUp:
				focusLastLiveFormItem(app, form, state)
				return nil
			}
		}
		return event
	})
	viewDone := make(chan struct{})
	defer close(viewDone)
	go func() {
		select {
		case <-ctx.Done():
			// 提交回调可能正在配置 OBS。等待它观察到 ctx 或完成当前有界操作，
			// 再由 UI 更新回调停止应用，避免主流程提前清理。
			if !busy.Load() {
				app.Stop()
			}
		case <-viewDone:
		}
	}()

	if err := app.SetRoot(pages, true).SetFocus(form).Run(); err != nil {
		return api.LiveSettings{}, fmt.Errorf("启动设置界面失败: %w", err)
	}
	if ctx.Err() != nil {
		return api.LiveSettings{}, ErrLiveSettingsCancelled
	}
	if cancelled {
		return api.LiveSettings{}, ErrLiveSettingsCancelled
	}
	return result, nil
}

func focusLastLiveFormItem(app *tview.Application, form *tview.Form, state *liveFormState) {
	if formItemIndex(form, state.obsGroup) >= 0 {
		app.SetFocus(state.obsPassword)
		return
	}
	app.SetFocus(state.streamMode)
}

// configureStartLiveForm 在较矮终端中压缩简介和字段间距。
// 多行字段的边框裁剪由 formClippedTextArea 统一处理。
func configureStartLiveForm(app *tview.Application, body *tview.Flex, form *tview.Form, description *tview.TextArea) {
	app.SetBeforeDrawFunc(func(screen tcell.Screen) bool {
		_, height := screen.Size()
		padding, rows := responsiveLiveFormDensity(height)
		form.SetItemPadding(padding)
		description.SetSize(rows, 0)

		// 开播页中央区域占垂直权重的 16/18；再留出框外间隔、按钮和状态区。
		available := max((height-4)*16/18-5, 1)
		body.ResizeItem(form, min(preferredLiveFormHeight(form, padding), available), 0)
		return false
	})
}

func preferredLiveFormHeight(form *tview.Form, padding int) int {
	height := 2 // 开播表单仅保留上下边框，不再额外留垂直内边距。
	for index := 0; index < form.GetFormItemCount(); index++ {
		height += form.GetFormItem(index).GetFieldHeight()
	}
	if count := form.GetFormItemCount(); count > 1 {
		height += (count - 1) * padding
	}
	return height
}

func responsiveLiveFormDensity(height int) (padding, descriptionRows int) {
	switch {
	case height < 22:
		return 0, 1
	case height < 30:
		return 0, 2
	default:
		return 1, 3
	}
}

type Navigation int

const (
	NavigationDetach Navigation = iota // 界面关闭或失败，不代表请求停止已有直播。
	NavigationQuit                     // 用户明确请求下播。
)
