package tui

import (
	"errors"

	"bili-live-tui/internal/api"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// ErrLiveEditUnchanged 表示用户保存了未发生变化的资料。
var ErrLiveEditUnchanged = errors.New("直播资料没有变化")

type liveEditPage struct {
	root      tview.Primitive
	body      *responsiveLiveEditBody
	form      *tview.Form
	buttons   []*tview.Button
	setStatus func(string, bool)
	cancel    func()
}

// responsiveLiveEditBody 只调整当前编辑页的布局，避免占用 Application
// 唯一的 BeforeDraw 回调。弹幕页也使用该回调处理自身布局，覆盖它会导致
// 关闭编辑页后终端缩放失效。
type responsiveLiveEditBody struct {
	*tview.Flex
	form        *tview.Form
	description *tview.TextArea
}

func (body *responsiveLiveEditBody) Draw(screen tcell.Screen) {
	_, height := screen.Size()
	padding, rows := responsiveLiveFormDensity(height)
	body.form.SetItemPadding(padding)
	body.description.SetSize(rows, 0)
	body.Flex.Draw(screen)
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
	save := func() {
		settings := state.settings()
		if err := settings.Validate(); err != nil {
			setStatus(err.Error(), true)
			return
		}
		if err := validateCoverInput(settings.CoverPath, state.hasExistingCover); err != nil {
			setStatus(err.Error(), true)
			return
		}
		if onSave != nil {
			onSave(settings)
		}
	}
	cancel := func() {
		if onCancel != nil {
			onCancel()
		}
	}
	saveButton := newActionButton("保存修改", save).
		SetStyle(actionButtonStyle(false).Bold(true))
	cancelButton := newActionButton("取消修改", cancel)
	buttons := centeredActionBar([]*tview.Button{saveButton, cancelButton})
	buttons.SetBackgroundColor(tview.Styles.PrimitiveBackgroundColor)
	form.SetCancelFunc(cancel)

	flex := tview.NewFlex()
	flex.SetDirection(tview.FlexRow)
	flex.SetBackgroundColor(tview.Styles.PrimitiveBackgroundColor)
	flex.AddItem(form, 0, 1, true)
	flex.AddItem(nil, 1, 0, false)
	flex.AddItem(buttons, 1, 0, true)
	flex.AddItem(status, 2, 0, false)
	body := &responsiveLiveEditBody{Flex: flex, form: form, description: state.description}
	body.SetInputCapture(liveEditInputCapture(app, form, state, saveButton, cancelButton, cancel))
	root := wideFormPage(
		pageHeader("修改直播资料", "保存后会立即同步到直播间"),
		body,
		pageFooter("Tab 切换　Enter 确认　Ctrl+U 清空当前项　Esc/Ctrl+C 放弃修改　支持鼠标点击"),
	)
	return &liveEditPage{
		root:      root,
		body:      body,
		form:      form,
		buttons:   []*tview.Button{saveButton, cancelButton},
		setStatus: setStatus,
		cancel:    onCancel,
	}
}

func liveEditInputCapture(app *tview.Application, form *tview.Form, state *liveFormState, saveButton, cancelButton *tview.Button, cancel func()) func(*tcell.EventKey) *tcell.EventKey {
	focusForm := func(index int) {
		form.SetFocus(index)
		app.SetFocus(form)
	}
	return func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEscape || event.Key() == tcell.KeyCtrlC {
			if cancel != nil {
				cancel()
			}
			return nil
		}
		if saveButton.HasFocus() {
			switch event.Key() {
			case tcell.KeyLeft, tcell.KeyRight, tcell.KeyTab:
				app.SetFocus(cancelButton)
				return nil
			case tcell.KeyUp, tcell.KeyBacktab:
				focusForm(form.GetFormItemCount() - 1)
				return nil
			}
		}
		if cancelButton.HasFocus() {
			switch event.Key() {
			case tcell.KeyLeft, tcell.KeyRight, tcell.KeyBacktab:
				app.SetFocus(saveButton)
				return nil
			case tcell.KeyTab:
				focusForm(0)
				return nil
			case tcell.KeyUp:
				focusForm(form.GetFormItemCount() - 1)
				return nil
			}
		}
		if state.title.HasFocus() && event.Key() == tcell.KeyBacktab {
			app.SetFocus(cancelButton)
			return nil
		}
		if state.orientation.HasFocus() && event.Key() == tcell.KeyTab {
			app.SetFocus(saveButton)
			return nil
		}
		return event
	}
}
