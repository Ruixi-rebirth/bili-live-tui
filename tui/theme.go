package tui

import (
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// floatingOverlay keeps the current page visible and only draws its content in
// a centered rectangle. Pages still gives the overlay the full terminal rect so
// it is automatically re-centered after a terminal resize.
type floatingOverlay struct {
	tview.Primitive
	x, y, width, height int
	preferredWidth      int
	preferredHeight     int
}

func newFloatingOverlay(content tview.Primitive, width, height int) *floatingOverlay {
	return &floatingOverlay{
		Primitive:       content,
		preferredWidth:  width,
		preferredHeight: height,
	}
}

func (overlay *floatingOverlay) SetRect(x, y, width, height int) {
	overlay.x = x
	overlay.y = y
	overlay.width = width
	overlay.height = height
}

func (overlay *floatingOverlay) GetRect() (int, int, int, int) {
	return overlay.x, overlay.y, overlay.width, overlay.height
}

func (overlay *floatingOverlay) Draw(screen tcell.Screen) {
	width := min(overlay.preferredWidth, overlay.width)
	height := min(overlay.preferredHeight, overlay.height)
	if overlay.width > 4 {
		width = min(width, overlay.width-4)
	}
	if overlay.height > 2 {
		height = min(height, overlay.height-2)
	}
	x := overlay.x + (overlay.width-width)/2
	y := overlay.y + (overlay.height-height)/2
	overlay.Primitive.SetRect(x, y, max(width, 1), max(height, 1))
	overlay.Primitive.Draw(screen)
}

func applyTheme() {
	tview.Styles = tview.Theme{
		PrimitiveBackgroundColor:    tcell.NewHexColor(0xfff1f5),
		ContrastBackgroundColor:     tcell.NewHexColor(0xfff9fb),
		MoreContrastBackgroundColor: tcell.NewHexColor(0xffdce8),
		BorderColor:                 tcell.NewHexColor(0xe1a4b7),
		TitleColor:                  tcell.NewHexColor(0xb65f7b),
		GraphicsColor:               tcell.NewHexColor(0xe58da7),
		PrimaryTextColor:            tcell.NewHexColor(0x57414b),
		SecondaryTextColor:          tcell.NewHexColor(0x7d626d),
		TertiaryTextColor:           tcell.NewHexColor(0xa78996),
		InverseTextColor:            tcell.NewHexColor(0xfff9fb),
		ContrastSecondaryTextColor:  tcell.NewHexColor(0x987585),
	}
}

var (
	accentColor                   = tcell.NewHexColor(0xe98eaa)
	accentActiveColor             = tcell.NewHexColor(0xc6537d)
	buttonTextColor               = tcell.NewHexColor(0x5c3948)
	buttonActiveTextColor         = tcell.NewHexColor(0xfff9fb)
	mutedColor                    = tcell.NewHexColor(0xa27f90)
	errorColor                    = tcell.NewHexColor(0xd65e78)
	panelColor                    = tcell.NewHexColor(0xfff7fa)
	formFieldColor                = tcell.NewHexColor(0xe9e4ea)
	formFieldFocusColor           = tcell.NewHexColor(0xd8cbd9)
	formSelectColor               = tcell.NewHexColor(0xf3eef3)
	autocompleteColor             = tcell.NewHexColor(0xffeef4)
	autocompleteSelectedColor     = tcell.NewHexColor(0xffc7d9)
	autocompleteTextColor         = tcell.NewHexColor(0x684c5a)
	autocompleteSelectedTextColor = tcell.NewHexColor(0x4f3544)
)

func pageHeader(title, subtitle string) *tview.TextView {
	header := tview.NewTextView()
	header.SetDynamicColors(true)
	header.SetTextAlign(tview.AlignCenter)
	header.SetBackgroundColor(tview.Styles.PrimitiveBackgroundColor)
	header.SetText("[::b][" + accentColor.String() + "]♡[-] " + title + " [" + accentColor.String() + "]♡[-][::-]\n[" + mutedColor.String() + "]" + subtitle + "[-]")
	return header
}

func pageFooter(text string) *tview.TextView {
	footer := tview.NewTextView()
	footer.SetDynamicColors(true)
	footer.SetTextAlign(tview.AlignCenter)
	footer.SetText("[" + mutedColor.String() + "]" + text + "[-]")
	return footer
}

func workspaceHeader(title string) *tview.TextView {
	header := tview.NewTextView()
	header.SetDynamicColors(true)
	header.SetTextAlign(tview.AlignCenter)
	header.SetBackgroundColor(tview.Styles.PrimitiveBackgroundColor)
	header.SetText("[::b][" + accentColor.String() + "]" + tview.Escape(title) + "[-][::-]")
	return header
}

func centeredPage(header, body, footer tview.Primitive) tview.Primitive {
	return centeredPageWithGrid(header, body, footer, -1, -3, -1)
}

func wideFormPage(header, body, footer tview.Primitive) tview.Primitive {
	return centeredPageWithGrid(header, body, footer, -1, -8, -1)
}

func centeredPageWithGrid(header, body, footer tview.Primitive, top, middle, bottom int) tview.Primitive {
	center := tview.NewGrid()
	center.SetRows(top, middle, bottom)
	center.SetColumns(-1, -5, -1)
	center.SetBackgroundColor(tview.Styles.PrimitiveBackgroundColor)
	center.AddItem(cuteMascot(), 1, 0, 1, 1, 0, mascotWidth(), false)
	center.AddItem(body, 1, 1, 1, 1, 0, 0, true)
	center.AddItem(cuteMascot(), 1, 2, 1, 1, 0, mascotWidth(), false)

	root := tview.NewFlex()
	root.SetDirection(tview.FlexRow)
	root.SetBackgroundColor(tview.Styles.PrimitiveBackgroundColor)
	if header != nil {
		root.AddItem(header, 3, 0, false)
	}
	root.AddItem(center, 0, 1, true)
	root.AddItem(footer, 1, 0, false)
	return root
}

func workspacePage(header, body, footer tview.Primitive) tview.Primitive {
	root := tview.NewFlex()
	root.SetDirection(tview.FlexRow)
	root.SetBackgroundColor(tview.Styles.PrimitiveBackgroundColor)
	root.AddItem(header, 1, 0, false)
	root.AddItem(body, 0, 1, true)
	if footer != nil {
		root.AddItem(footer, 1, 0, false)
	}
	return root
}

func styleForm(form *tview.Form, title string) *tview.Form {
	form.SetBackgroundColor(panelColor)
	form.SetBorder(true)
	form.SetBorderColor(tview.Styles.BorderColor)
	form.SetTitle(" " + title + " ")
	form.SetTitleColor(tview.Styles.TitleColor)
	form.SetItemPadding(1)
	form.SetButtonsAlign(tview.AlignCenter)
	form.SetLabelColor(tview.Styles.SecondaryTextColor)
	form.SetFieldBackgroundColor(formFieldColor)
	form.SetFieldTextColor(tview.Styles.PrimaryTextColor)
	form.SetButtonStyle(tcell.StyleDefault.
		Background(accentColor).
		Foreground(buttonTextColor))
	form.SetButtonActivatedStyle(tcell.StyleDefault.
		Background(accentActiveColor).
		Foreground(buttonActiveTextColor).
		Bold(true))
	return form
}

func styleModal(modal *tview.Modal) *tview.Modal {
	// Modal.SetBackgroundColor 只设置内部框体，嵌入的 Box 负责覆盖下方页面的区域。
	// 两者都显式设置，避免弹窗边框区域透出弹幕内容。
	modal.Box.SetBackgroundColor(panelColor)
	modal.Box.SetBorderColor(tview.Styles.BorderColor)
	return modal.
		SetBackgroundColor(panelColor).
		SetTextColor(tview.Styles.PrimaryTextColor).
		SetButtonStyle(tcell.StyleDefault.
			Background(accentColor).
			Foreground(buttonTextColor)).
		SetButtonActivatedStyle(tcell.StyleDefault.
			Background(accentActiveColor).
			Foreground(buttonActiveTextColor).
			Bold(true))
}

func equalizeButtonWidths(form *tview.Form) {
	count := form.GetButtonCount()
	if count < 2 {
		return
	}
	maxWidth := 0
	widths := make([]int, count)
	for i := 0; i < count; i++ {
		label := form.GetButton(i).GetLabel()
		widths[i] = tview.TaggedStringWidth(label)
		if widths[i] > maxWidth {
			maxWidth = widths[i]
		}
	}
	for i := 0; i < count; i++ {
		padding := maxWidth - widths[i]
		left := padding / 2
		right := padding - left
		button := form.GetButton(i)
		button.SetLabel(strings.Repeat(" ", left) + button.GetLabel() + strings.Repeat(" ", right))
	}
}
