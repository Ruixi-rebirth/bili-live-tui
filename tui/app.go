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

var (
	// ErrLiveEditUnchanged 表示用户保存了未发生变化的资料。
	ErrLiveEditUnchanged = errors.New("直播资料没有变化")
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
	status := tview.NewTextView()
	status.SetDynamicColors(true)
	status.SetTextColor(mutedColor)
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
	// 先创建选择器再组装中心页面，使弹窗关闭后能把焦点准确返回到打开它的表单控件。
	body := tview.NewFlex()
	body.SetDirection(tview.FlexRow)
	body.SetBackgroundColor(tview.Styles.PrimitiveBackgroundColor)
	// 表单占据剩余空间，焦点切换到下方项目时自动垂直滚动。
	body.AddItem(form, 0, 1, true)
	body.AddItem(status, 2, 0, false)
	configureResponsiveLiveForm(app, form, state.description)
	root := wideFormPage(
		pageHeader("设置开播信息", "填写房间资料，确认后立即开始直播"),
		body,
		pageFooter("Tab 切换　Enter 确认　Esc/Ctrl+C 取消开播　支持鼠标点击"),
	)
	pages.AddPage("main", root, true, true)
	cancelLive := func() {
		if busy.Load() {
			cancelled = true
			setStatus("正在结束当前启动步骤，随后取消开播……", false)
			return
		}
		cancelled = true
		app.Stop()
	}
	form.AddButton("开始直播", func() {
		if busy.Load() {
			return
		}
		settings := state.settings()
		if err := settings.Validate(); err != nil {
			setStatus(err.Error(), true)
			return
		}
		if err := validateCoverInput(settings.CoverPath); err != nil {
			setStatus(err.Error(), true)
			return
		}
		if submit == nil {
			result = settings
			app.Stop()
			return
		}
		busy.Store(true)
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
					setStatus("启动直播失败："+err.Error(), true)
					return
				}
				result = settings
				app.Stop()
			})
		}()
	})
	form.GetButton(form.GetButtonCount() - 1).SetLabel("  ▶ 开始直播  ").
		SetStyle(tcell.StyleDefault.Background(accentColor).Foreground(buttonTextColor).Bold(true)).
		SetActivatedStyle(tcell.StyleDefault.Background(accentActiveColor).Foreground(buttonActiveTextColor).Bold(true))
	form.AddButton("✕ 取消开播", cancelLive)
	form.GetButton(form.GetButtonCount() - 1).SetLabel("  ✕ 取消开播  ")
	equalizeButtonWidths(form)

	form.SetCancelFunc(cancelLive)

	app.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyCtrlC {
			cancelLive()
			return nil
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

type liveEditPage struct {
	root      tview.Primitive
	form      *tview.Form
	setStatus func(string, bool)
	cancel    func()
}

func newLiveEditPage(app *tview.Application, initial api.LiveSettings, areas []api.LiveArea, onSave func(api.LiveSettings), onCancel func()) *liveEditPage {
	form, state := newLiveFormWithSettings(areas, &initial, "修改直播资料")
	status := tview.NewTextView()
	status.SetDynamicColors(true)
	status.SetTextColor(mutedColor)
	status.SetBackgroundColor(tview.Styles.PrimitiveBackgroundColor)
	setStatus := func(message string, isError bool) {
		color := mutedColor
		if isError {
			color = errorColor
		}
		status.SetText("[" + color.String() + "]" + tview.Escape(message) + "[-]")
	}
	form.AddButton("保存修改", func() {
		settings := state.settings()
		if err := settings.Validate(); err != nil {
			setStatus(err.Error(), true)
			return
		}
		if err := validateCoverInput(settings.CoverPath); err != nil {
			setStatus(err.Error(), true)
			return
		}
		if onSave != nil {
			onSave(settings)
		}
	})
	form.GetButton(form.GetButtonCount() - 1).SetLabel("  保存修改  ").
		SetStyle(tcell.StyleDefault.Background(accentColor).Foreground(buttonTextColor).Bold(true)).
		SetActivatedStyle(tcell.StyleDefault.Background(accentActiveColor).Foreground(buttonActiveTextColor).Bold(true))
	form.AddButton("取消修改", func() {
		if onCancel != nil {
			onCancel()
		}
	})
	equalizeButtonWidths(form)
	form.SetCancelFunc(func() {
		if onCancel != nil {
			onCancel()
		}
	})

	body := tview.NewFlex()
	body.SetDirection(tview.FlexRow)
	body.SetBackgroundColor(tview.Styles.PrimitiveBackgroundColor)
	body.AddItem(form, 0, 1, true)
	body.AddItem(status, 2, 0, false)
	configureResponsiveLiveForm(app, form, state.description)
	root := wideFormPage(
		pageHeader("修改直播资料", "保存后会立即同步到直播间"),
		body,
		pageFooter("Tab 切换　Enter 确认　Esc/Ctrl+C 放弃修改　支持鼠标点击"),
	)
	return &liveEditPage{root: root, form: form, setStatus: setStatus, cancel: onCancel}
}

// configureResponsiveLiveForm 在较矮终端中压缩简介和字段间距。
// 多行字段的边框裁剪由 formClippedTextArea 统一处理。
func configureResponsiveLiveForm(app *tview.Application, form *tview.Form, description *tview.TextArea) {
	app.SetBeforeDrawFunc(func(screen tcell.Screen) bool {
		_, height := screen.Size()
		padding, rows := responsiveLiveFormDensity(height)
		form.SetItemPadding(padding)
		description.SetSize(rows, 0)
		return false
	})
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
	NavigationQuit Navigation = iota
	NavigationHome
)

// HomeAction 描述直播概览下一步的去向。
// 单独的编辑动作让 Esc 行为明确：只打开或关闭下播确认，不会悄悄切换页面。
type HomeAction int

const (
	HomeActionDanmaku HomeAction = iota
	HomeActionStop
)
