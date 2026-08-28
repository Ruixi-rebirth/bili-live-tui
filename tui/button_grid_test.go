package tui

import (
	"fmt"
	"testing"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

func TestButtonGridUsesAtMostThreeColumns(t *testing.T) {
	applyTheme()
	for _, buttonCount := range []int{1, 3, 4, 7} {
		t.Run(fmt.Sprintf("%d_buttons", buttonCount), func(t *testing.T) {
			grid := newButtonGrid(buttonGridColumns)
			for index := 0; index < buttonCount; index++ {
				grid.AddButton(fmt.Sprintf("按钮%d", index+1), nil)
			}
			wantRows := (buttonCount + buttonGridColumns - 1) / buttonGridColumns
			if grid.RowCount() != wantRows {
				t.Fatalf("RowCount() = %d, want %d", grid.RowCount(), wantRows)
			}

			screen := tcell.NewSimulationScreen("UTF-8")
			if err := screen.Init(); err != nil {
				t.Fatal(err)
			}
			defer screen.Fini()
			wantHeight := wantRows*2 - 1
			if grid.PreferredHeight() != wantHeight {
				t.Fatalf("PreferredHeight() = %d, want %d", grid.PreferredHeight(), wantHeight)
			}
			screen.SetSize(60, wantHeight)
			grid.SetRect(0, 0, 60, wantHeight)
			grid.Draw(screen)

			rowCounts := make(map[int]int)
			for index := 0; index < buttonCount; index++ {
				_, row, _, height := grid.GetButton(index).GetRect()
				if height != 1 {
					t.Fatalf("button %d height = %d, want 1", index, height)
				}
				if wantRow := (index / buttonGridColumns) * 2; row != wantRow {
					t.Fatalf("button %d row = %d, want %d", index, row, wantRow)
				}
				rowCounts[row]++
			}
			for row, count := range rowCounts {
				if count > buttonGridColumns {
					t.Fatalf("row %d contains %d buttons", row, count)
				}
			}
		})
	}
}

func TestButtonGridKeyboardMovesAcrossRows(t *testing.T) {
	applyTheme()
	grid := newButtonGrid(buttonGridColumns)
	for index := 0; index < 7; index++ {
		grid.AddButton(fmt.Sprintf("按钮%d", index+1), nil)
	}
	app := tview.NewApplication()
	app.SetFocus(grid)

	press := func(key tcell.Key) {
		t.Helper()
		focused := app.GetFocus()
		focused.InputHandler()(tcell.NewEventKey(key, 0, tcell.ModNone), func(primitive tview.Primitive) {
			app.SetFocus(primitive)
		})
	}
	assertFocused := func(want int) {
		t.Helper()
		if app.GetFocus() != grid.GetButton(want) {
			t.Fatalf("focused button is not %d", want)
		}
	}

	assertFocused(0)
	press(tcell.KeyDown)
	assertFocused(3)
	press(tcell.KeyDown)
	assertFocused(6)
	press(tcell.KeyUp)
	assertFocused(3)
	press(tcell.KeyRight)
	assertFocused(4)
	press(tcell.KeyTab)
	assertFocused(5)
	press(tcell.KeyBacktab)
	assertFocused(4)
}
