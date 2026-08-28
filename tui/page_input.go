package tui

import (
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// pageInputHandlers 只把输入交给最上层页面。底层仍可见不代表它应接收按键。
// handled=true 时调用方必须直接返回，不能继续执行其他页面的快捷键。
type pageInputHandlers map[string]func(*tcell.EventKey) *tcell.EventKey

func (handlers pageInputHandlers) capture(pages *tview.Pages, event *tcell.EventKey) (*tcell.EventKey, bool) {
	front, _ := pages.GetFrontPage()
	if handler := handlers[front]; handler != nil {
		return handler(event), true
	}
	return event, false
}

func modalInputHandler(closeModal, quit func()) func(*tcell.EventKey) *tcell.EventKey {
	return func(event *tcell.EventKey) *tcell.EventKey {
		switch event.Key() {
		case tcell.KeyEscape:
			closeModal()
			return nil
		case tcell.KeyCtrlC:
			quit()
			return nil
		default:
			return event
		}
	}
}
