package tui

import (
	"strings"
	"testing"

	"bili-live-tui/internal/api"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

func TestLiveEditFieldsFitAvailableSpace(t *testing.T) {
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	screen.SetSize(120, 40)
	page := newLiveEditPage(tview.NewApplication(), api.LiveSettings{}, nil, nil, nil)
	page.root.SetRect(0, 0, 120, 40)
	page.root.Draw(screen)
	_, top, _, height := page.form.GetInnerRect()
	for i := 0; i < page.form.GetFormItemCount(); i++ {
		item := page.form.GetFormItem(i)
		_, y, _, h := item.GetRect()
		if y < top || y+h > top+height {
			t.Fatalf("field %q outside form: y=%d h=%d, form y=%d h=%d", item.GetLabel(), y, h, top, height)
		}
	}
}

func TestHomeLayoutStableOnFirstDraw(t *testing.T) {
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	screen.SetSize(120, 40)
	status := tview.NewTextView()
	status.SetBorder(true)
	text := strings.Repeat("直播概览", 15)
	status.SetText(text)
	panel := tview.NewFlex().SetDirection(tview.FlexRow).AddItem(status, 0, 1, true)
	flex := tview.NewFlex().SetDirection(tview.FlexRow).AddItem(panel, 0, 1, true)
	body := &responsiveHomeBody{Flex: flex, panel: panel, status: status, getStatusText: func() string { return text }}
	body.SetRect(0, 0, 100, 35)
	body.Draw(screen)
	_, firstY, _, firstHeight := status.GetRect()
	body.Draw(screen)
	_, nextY, _, nextHeight := status.GetRect()
	if firstY != nextY || firstHeight != nextHeight {
		t.Fatalf("layout changed between frames: (%d, %d) -> (%d, %d)", firstY, firstHeight, nextY, nextHeight)
	}
}
