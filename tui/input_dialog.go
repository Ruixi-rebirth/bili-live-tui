package tui

import (
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// inputDialog 是单字段操作的公共浮窗，不占用下面工作区的状态栏或按钮。
type inputDialog struct {
	root            *floatingOverlay
	field           *tview.InputField
	confirm, cancel *tview.Button
	status          *tview.TextView
	capture         func(*tcell.EventKey) *tcell.EventKey
}

func newInputDialog(app *tview.Application, title, label, initial, submitLabel string, validate func(string) error, submit func(string), cancel func()) *inputDialog {
	field := tview.NewInputField().SetText(initial).SetFieldWidth(0)
	field.SetFieldBackgroundColor(formFieldColor).SetFieldTextColor(tview.Styles.PrimaryTextColor)
	labelView := tview.NewTextView().SetText(strings.TrimSpace(label))
	labelView.SetBackgroundColor(panelColor)
	labelView.SetTextColor(tview.Styles.SecondaryTextColor)
	status := tview.NewTextView().SetDynamicColors(true).SetWrap(true).SetWordWrap(false)
	status.SetBackgroundColor(panelColor)
	status.SetTextColor(errorColor)
	panel := tview.NewFlex().SetDirection(tview.FlexRow)
	panel.SetBorder(true).SetTitle(" "+tview.Escape(title)+" ").SetBorderPadding(1, 0, 2, 2)
	panel.SetBackgroundColor(panelColor).SetBorderColor(tview.Styles.BorderColor).SetTitleColor(tview.Styles.TitleColor)
	dialog := &inputDialog{field: field, status: status}
	dialog.root = newFloatingOverlay(panel, 52, 7).SetOpaqueBackground(panelColor)
	clearError := func() {
		status.SetText("")
		panel.ResizeItem(status, 0, 0)
		dialog.root.SetPreferredSize(52, 7)
	}
	field.SetChangedFunc(func(string) { clearError() })
	confirm := func() {
		value := strings.TrimSpace(field.GetText())
		if validate != nil {
			if err := validate(value); err != nil {
				status.SetText(tview.Escape(err.Error()))
				panel.ResizeItem(status, 2, 0)
				dialog.root.SetPreferredSize(52, 9)
				app.SetFocus(field)
				return
			}
		}
		submit(value)
	}
	dialog.confirm = newActionButton(submitLabel, confirm)
	dialog.cancel = newActionButton("取消", cancel)
	panel.AddItem(labelView, 1, 0, false)
	panel.AddItem(field, 1, 0, true)
	panel.AddItem(status, 0, 0, false)
	panel.AddItem(nil, 1, 0, false)
	panel.AddItem(centeredActionBar([]*tview.Button{dialog.confirm, dialog.cancel}), 1, 0, true)
	focusables := []tview.Primitive{field, dialog.confirm, dialog.cancel}
	dialog.capture = func(event *tcell.EventKey) *tcell.EventKey {
		switch event.Key() {
		case tcell.KeyEscape:
			cancel()
			return nil
		case tcell.KeyCtrlU:
			if field.HasFocus() {
				field.SetText("")
				return nil
			}
		case tcell.KeyEnter:
			if field.HasFocus() {
				confirm()
				return nil
			}
		case tcell.KeyTab, tcell.KeyBacktab:
			delta := 1
			if event.Key() == tcell.KeyBacktab {
				delta = -1
			}
			for i, item := range focusables {
				if item.HasFocus() {
					app.SetFocus(focusables[(i+delta+len(focusables))%len(focusables)])
					return nil
				}
			}
			app.SetFocus(field)
			return nil
		case tcell.KeyLeft, tcell.KeyRight:
			if dialog.confirm.HasFocus() {
				app.SetFocus(dialog.cancel)
				return nil
			}
			if dialog.cancel.HasFocus() {
				app.SetFocus(dialog.confirm)
				return nil
			}
		case tcell.KeyUp:
			if !field.HasFocus() {
				app.SetFocus(field)
				return nil
			}
		case tcell.KeyDown:
			if field.HasFocus() {
				app.SetFocus(dialog.confirm)
				return nil
			}
		}
		return event
	}
	return dialog
}
