package tui

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"

	"bili-live-tui/internal/utils"
	"github.com/rivo/tview"
)

type AlreadyLiveAction int

const (
	AlreadyLiveActionStopLive AlreadyLiveAction = iota // 立即下播
	AlreadyLiveActionDanmaku                           // 进入弹幕与房间管理
	AlreadyLiveActionRestart                           // 强制重新开播
	AlreadyLiveActionExit                              // 退出程序
)

// RunAlreadyLiveDialog 当启动时检测到直播间处于开播状态时弹出，提供一键下播、直接进弹幕、重开或退出等选项。
func RunAlreadyLiveDialog(ctx context.Context, roomID, title string, onStopLive func() error) (AlreadyLiveAction, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	applyTheme()
	app := tview.NewApplication().EnableMouse(true).SetTitle("bili-live-tui")

	action := AlreadyLiveActionExit
	modal := styleModal(tview.NewModal())
	modal.SetBackgroundColor(panelColor)

	msg := fmt.Sprintf("⚠️ 检测到您的直播间（%s）当前处于【开播中 🔴】状态！\n标题：%s\n\n可能是上次意外关闭终端未完成下播，或正在其他客户端推流。\n请选择您的操作：",
		tview.Escape(roomID), tview.Escape(title))
	modal.SetText(msg)
	modal.AddButtons([]string{"⏹ 立即下播", "📺 进入弹幕与管理", "🔄 重新开播", "✕ 退出程序"})

	var busy atomic.Bool
	modal.SetDoneFunc(func(buttonIndex int, buttonLabel string) {
		if busy.Load() {
			return
		}
		switch buttonIndex {
		case 0: // 立即下播
			if onStopLive != nil {
				busy.Store(true)
				modal.SetText(fmt.Sprintf("%s\n\n[%s]正在请求下播，请稍候……[-]", msg, accentColor.String()))
				go func() {
					err := onStopLive()
					app.QueueUpdateDraw(func() {
						busy.Store(false)
						if err != nil {
							modal.SetText(fmt.Sprintf("%s\n\n[%s]下播失败：%s[-]", msg, errorColor.String(), tview.Escape(err.Error())))
							return
						}
						action = AlreadyLiveActionStopLive
						app.Stop()
					})
				}()
				return
			}
			action = AlreadyLiveActionStopLive
			app.Stop()
		case 1: // 进入弹幕与管理
			action = AlreadyLiveActionDanmaku
			app.Stop()
		case 2: // 重新开播
			if onStopLive != nil {
				busy.Store(true)
				modal.SetText(fmt.Sprintf("%s\n\n[%s]正在清理旧直播状态，请稍候……[-]", msg, accentColor.String()))
				go func() {
					err := onStopLive()
					app.QueueUpdateDraw(func() {
						busy.Store(false)
						if err != nil {
							modal.SetText(fmt.Sprintf("%s\n\n[%s]清理旧直播状态失败：%s[-]", msg, errorColor.String(), tview.Escape(err.Error())))
							return
						}
						action = AlreadyLiveActionRestart
						app.Stop()
					})
				}()
				return
			}
			action = AlreadyLiveActionRestart
			app.Stop()
		default: // 退出程序
			action = AlreadyLiveActionExit
			app.Stop()
		}
	})
	viewDone := make(chan struct{})
	defer close(viewDone)
	go func() {
		select {
		case <-ctx.Done():
			app.Stop()
		case <-viewDone:
		}
	}()

	if err := app.SetRoot(modal, false).Run(); err != nil {
		return AlreadyLiveActionExit, err
	}
	return action, nil
}

const executablePathPageName = "executable-path"

func executableNotFound(err error) *utils.ExecutableNotFoundError {
	var missing *utils.ExecutableNotFoundError
	if errors.As(err, &missing) {
		return missing
	}
	return nil
}

// showExecutablePathPage 是 OBS、FFmpeg 和 MPV 共用的浮动路径输入框。
func showExecutablePathPage(app *tview.Application, pages *tview.Pages, missing *utils.ExecutableNotFoundError, onConfigured, onCancel func()) {
	field := tview.NewInputField().
		SetLabel("路径").
		SetText(missing.Suggested).
		SetPlaceholder("请输入可执行文件完整路径").
		SetAcceptanceFunc(tview.InputFieldMaxLength(1000))
	message := tview.NewTextView()
	message.SetDynamicColors(true)
	message.SetTextAlign(tview.AlignCenter)
	message.SetWrap(true)
	message.SetBackgroundColor(panelColor)
	message.SetText("[" + mutedColor.String() + "]自动探测失败，请选择可执行文件，保存后下次将自动使用[-]")
	status := tview.NewTextView()
	status.SetDynamicColors(true)
	status.SetTextAlign(tview.AlignCenter)
	status.SetTextColor(mutedColor)
	status.SetBackgroundColor(panelColor)
	form := styleForm(tview.NewForm(), "")
	form.SetBorder(false)
	form.SetItemPadding(0)
	form.AddFormItem(focusedLabelInput(field))
	panel := tview.NewFlex().SetDirection(tview.FlexRow)
	panel.SetBackgroundColor(panelColor)
	panel.SetBorder(true)
	panel.SetBorderColor(tview.Styles.BorderColor)
	panel.SetTitle(" 设置 " + missing.DisplayName + " 路径 ")
	panel.SetTitleColor(tview.Styles.TitleColor)
	panel.AddItem(message, 2, 0, false)
	panel.AddItem(form, 0, 1, true)
	panel.AddItem(status, 0, 0, false)
	overlay := newFloatingOverlay(panel, 88, 7)
	closePage := func(callback func()) {
		pages.RemovePage(executablePathPageName)
		if callback != nil {
			callback()
		}
	}
	form.AddButton("保存并重试", func() {
		if _, err := missing.Configure(field.GetText()); err != nil {
			panel.ResizeItem(status, 2, 0)
			overlay.preferredHeight = 9
			status.SetText("[" + errorColor.String() + "]" + tview.Escape(err.Error()) + "[-]")
			return
		}
		closePage(onConfigured)
	})
	form.AddButton("取消", func() { closePage(onCancel) })
	form.SetCancelFunc(func() { closePage(onCancel) })
	equalizeButtonWidths(form)
	pages.AddPage(executablePathPageName, overlay, true, true)
	form.SetFocus(0)
	app.SetFocus(form)
}
