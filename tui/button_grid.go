package tui

import (
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

const buttonGridColumns = 3

// buttonGrid 将操作按钮按固定列数排列，并统一处理跨行焦点移动。
type buttonGrid struct {
	*tview.Flex
	buttons   []*tview.Button
	columns   int
	focused   int
	focus     func(tview.Primitive)
	cancel    func()
	onChanged []func()
}

func newButtonGrid(columns int) *buttonGrid {
	if columns < 1 {
		columns = 1
	}
	grid := &buttonGrid{
		Flex:    tview.NewFlex().SetDirection(tview.FlexRow),
		columns: columns,
		focused: -1,
	}
	grid.SetBackgroundColor(panelColor)
	return grid
}

func (grid *buttonGrid) AddButton(label string, selected func()) *buttonGrid {
	button := newActionButton(label, selected)
	grid.buttons = append(grid.buttons, button)
	grid.configureButton(button, len(grid.buttons)-1)
	if grid.focused < 0 {
		grid.focused = 0
	}
	grid.rebuild()
	return grid
}

func (grid *buttonGrid) SetButtons(buttons []*tview.Button) *buttonGrid {
	grid.buttons = append(grid.buttons[:0], buttons...)
	grid.focused = -1
	for index, button := range grid.buttons {
		grid.configureButton(button, index)
		if button.HasFocus() {
			grid.focused = index
		}
	}
	if grid.focused < 0 && len(grid.buttons) > 0 {
		grid.focused = 0
	}
	grid.rebuild()
	return grid
}

func (grid *buttonGrid) configureButton(button *tview.Button, index int) {
	button.SetFocusFunc(func() { grid.focused = index })
	button.SetExitFunc(func(key tcell.Key) {
		switch key {
		case tcell.KeyTab:
			grid.moveHorizontal(1)
		case tcell.KeyBacktab:
			grid.moveHorizontal(-1)
		case tcell.KeyEscape:
			if grid.cancel != nil {
				grid.cancel()
			}
		}
	})
	button.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch event.Key() {
		case tcell.KeyLeft:
			grid.moveHorizontal(-1)
			return nil
		case tcell.KeyRight:
			grid.moveHorizontal(1)
			return nil
		case tcell.KeyUp:
			grid.moveVertical(-1)
			return nil
		case tcell.KeyDown:
			grid.moveVertical(1)
			return nil
		default:
			return event
		}
	})
}

func (grid *buttonGrid) ClearButtons() *buttonGrid {
	grid.buttons = nil
	grid.focused = -1
	grid.rebuild()
	return grid
}

func (grid *buttonGrid) GetButtonCount() int {
	return len(grid.buttons)
}

func (grid *buttonGrid) GetButton(index int) *tview.Button {
	if index < 0 || index >= len(grid.buttons) {
		return nil
	}
	return grid.buttons[index]
}

func (grid *buttonGrid) GetFocusedItemIndex() (formItem, button int) {
	for index, item := range grid.buttons {
		if item.HasFocus() {
			return -1, index
		}
	}
	return -1, -1
}

func (grid *buttonGrid) SetFocus(index int) *buttonGrid {
	if len(grid.buttons) == 0 {
		grid.focused = -1
		return grid
	}
	index = (index%len(grid.buttons) + len(grid.buttons)) % len(grid.buttons)
	grid.focused = index
	if grid.focus != nil {
		grid.focus(grid.buttons[index])
		return grid
	}
	for _, button := range grid.buttons {
		button.Blur()
	}
	var focus func(tview.Primitive)
	focus = func(primitive tview.Primitive) { primitive.Focus(focus) }
	focus(grid.buttons[index])
	return grid
}

func (grid *buttonGrid) SetCancelFunc(cancel func()) *buttonGrid {
	grid.cancel = cancel
	return grid
}

func (grid *buttonGrid) AddChangedFunc(changed func()) *buttonGrid {
	if changed != nil {
		grid.onChanged = append(grid.onChanged, changed)
	}
	return grid
}

func (grid *buttonGrid) RowCount() int {
	if len(grid.buttons) == 0 {
		return 0
	}
	return (len(grid.buttons) + grid.columns - 1) / grid.columns
}

func (grid *buttonGrid) PreferredHeight() int {
	rows := grid.RowCount()
	if rows == 0 {
		return 0
	}
	return rows*2 - 1
}

func (grid *buttonGrid) PreferredWidth() int {
	if len(grid.buttons) == 0 {
		return 0
	}
	buttonWidth := grid.buttonWidth()
	columns := min(len(grid.buttons), grid.columns)
	return columns*buttonWidth + columns - 1
}

func (grid *buttonGrid) Focus(delegate func(tview.Primitive)) {
	if len(grid.buttons) == 0 || delegate == nil {
		grid.Flex.Focus(delegate)
		return
	}
	grid.focus = delegate
	if _, current := grid.GetFocusedItemIndex(); current >= 0 {
		grid.focused = current
	}
	if grid.focused < 0 || grid.focused >= len(grid.buttons) {
		grid.focused = 0
	}
	delegate(grid.buttons[grid.focused])
}

func (grid *buttonGrid) moveHorizontal(offset int) {
	if len(grid.buttons) == 0 {
		return
	}
	index := grid.currentIndex()
	grid.SetFocus(index + offset)
}

func (grid *buttonGrid) moveVertical(direction int) {
	if len(grid.buttons) == 0 {
		return
	}
	index := grid.currentIndex()
	target := index + direction*grid.columns
	if target >= len(grid.buttons) {
		target = index % grid.columns
	}
	if target < 0 {
		target = ((len(grid.buttons)-1)/grid.columns)*grid.columns + index%grid.columns
		if target >= len(grid.buttons) {
			target -= grid.columns
		}
	}
	grid.SetFocus(target)
}

func (grid *buttonGrid) currentIndex() int {
	if _, index := grid.GetFocusedItemIndex(); index >= 0 {
		return index
	}
	if grid.focused >= 0 && grid.focused < len(grid.buttons) {
		return grid.focused
	}
	return 0
}

func (grid *buttonGrid) rebuild() {
	grid.Clear()
	buttonWidth := grid.buttonWidth()
	for start := 0; start < len(grid.buttons); start += grid.columns {
		if start > 0 {
			grid.AddItem(nil, 1, 0, false)
		}
		end := min(start+grid.columns, len(grid.buttons))
		row := tview.NewFlex().SetDirection(tview.FlexColumn)
		row.SetBackgroundColor(panelColor)
		row.AddItem(nil, 0, 1, false)
		for index := start; index < end; index++ {
			row.AddItem(grid.buttons[index], buttonWidth, 0, true)
			if index+1 < end {
				row.AddItem(nil, 1, 0, false)
			}
		}
		row.AddItem(nil, 0, 1, false)
		grid.AddItem(row, 1, 0, true)
	}
	for _, changed := range grid.onChanged {
		changed()
	}
}

func (grid *buttonGrid) buttonWidth() int {
	width := 1
	for _, button := range grid.buttons {
		width = max(width, tview.TaggedStringWidth(button.GetLabel())+2)
	}
	return width
}
