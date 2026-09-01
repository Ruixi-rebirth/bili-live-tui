package tui

import (
	"strings"

	"github.com/rivo/tview"
)

const rabbitArt = `   (\_/)
  (｡•ㅅ•｡)
  / づ♡ \`

func cuteMascot() *tview.TextView {
	mascot := tview.NewTextView()
	mascot.SetDynamicColors(true)
	mascot.SetTextAlign(tview.AlignCenter)
	mascot.SetTextColor(mutedColor)
	mascot.SetBackgroundColor(tview.Styles.PrimitiveBackgroundColor)
	mascot.SetText(colorMascotLines(strings.Split(rabbitArt, "\n")))
	return mascot
}

func mascotWidth() int {
	width := 0
	for _, line := range strings.Split(rabbitArt, "\n") {
		if current := tview.TaggedStringWidth(line); current > width {
			width = current
		}
	}
	return width
}

func colorMascotLines(lines []string) string {
	colored := make([]string, len(lines))
	for i, line := range lines {
		color := mutedColor
		if i == 0 || i == len(lines)-1 {
			color = accentColor
		} else if i == 1 {
			color = tview.Styles.TitleColor
		} else {
			color = tview.Styles.PrimaryTextColor
		}
		colored[i] = "[" + color.String() + "]" + tview.Escape(line) + "[-]"
	}
	return strings.Join(colored, "\n")
}
