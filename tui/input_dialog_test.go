package tui

import (
	"fmt"
	"testing"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

func TestInputDialogFocusValidationAndSubmit(t *testing.T) {
	app := tview.NewApplication()
	var submitted string
	cancelled := false
	dialog := newInputDialog(app, "添加房管", "UID 或用户名", "", "查找", func(value string) error {
		if value == "" {
			return fmt.Errorf("请输入 UID 或用户名")
		}
		return nil
	}, func(value string) { submitted = value }, func() { cancelled = true })
	app.SetFocus(dialog.field)
	press := func(key tcell.Key) { dialog.capture(tcell.NewEventKey(key, 0, tcell.ModNone)) }
	press(tcell.KeyEnter)
	if dialog.status.GetText(true) == "" || !dialog.field.HasFocus() || dialog.root.preferredHeight != 9 {
		t.Fatal("validation must stay inside the dialog")
	}
	dialog.field.SetText(" 123 ")
	if dialog.status.GetText(true) != "" || dialog.root.preferredHeight != 7 {
		t.Fatal("editing must clear validation and compact the dialog")
	}
	press(tcell.KeyTab)
	if !dialog.confirm.HasFocus() {
		t.Fatal("Tab must focus search")
	}
	press(tcell.KeyRight)
	if !dialog.cancel.HasFocus() {
		t.Fatal("Right must focus cancel")
	}
	press(tcell.KeyTab)
	if !dialog.field.HasFocus() {
		t.Fatal("Tab must cycle back to input")
	}
	press(tcell.KeyEnter)
	if submitted != "123" {
		t.Fatalf("submitted = %q", submitted)
	}
	press(tcell.KeyEscape)
	if !cancelled {
		t.Fatal("Escape must cancel")
	}
}

func TestInputDialogFitsAndBlocksUnderlyingMouse(t *testing.T) {
	app := tview.NewApplication()
	dialog := newInputDialog(app, "添加禁言用户", "UID 或用户名", "", "查找", nil, func(string) {}, func() {})
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	screen.SetSize(80, 24)
	dialog.root.SetRect(0, 0, 80, 24)
	dialog.root.Draw(screen)
	x, y, w, h := dialog.root.Primitive.GetRect()
	_, buttonY, _, _ := dialog.cancel.GetRect()
	if w != 52 || h != 7 || buttonY != y+h-2 {
		t.Fatalf("rect %d,%d %dx%d buttonY=%d", x, y, w, h, buttonY)
	}
	consumed, _ := dialog.root.MouseHandler()(tview.MouseLeftClick, tcell.NewEventMouse(0, 0, tcell.Button1, tcell.ModNone), func(p tview.Primitive) { app.SetFocus(p) })
	if !consumed {
		t.Fatal("outside clicks must not reach underlying actions")
	}
}
