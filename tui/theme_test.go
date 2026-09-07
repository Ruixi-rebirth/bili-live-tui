package tui

import (
	"testing"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

type transparentTestPrimitive struct{ *tview.Box }

func (primitive *transparentTestPrimitive) Draw(tcell.Screen) {}

func TestFloatingOverlayCentersWithoutClearingBackground(t *testing.T) {
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	screen.SetSize(40, 20)
	screen.SetContent(0, 0, 'x', nil, tcell.StyleDefault)

	content := tview.NewBox()
	overlay := newFloatingOverlay(content, 20, 8)
	overlay.SetRect(0, 0, 40, 20)
	overlay.Draw(screen)

	if x, y, width, height := content.GetRect(); x != 10 || y != 6 || width != 20 || height != 8 {
		t.Fatalf("content rect = (%d, %d, %d, %d), want (10, 6, 20, 8)", x, y, width, height)
	}
	if mainRune, _, _, _ := screen.GetContent(0, 0); mainRune != 'x' {
		t.Fatalf("background rune = %q, want it left unchanged", mainRune)
	}
}

func TestFloatingOverlayFitsSmallTerminal(t *testing.T) {
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	screen.SetSize(18, 7)

	content := tview.NewBox()
	overlay := newFloatingOverlay(content, 88, 11)
	overlay.SetRect(0, 0, 18, 7)
	overlay.Draw(screen)

	if x, y, width, height := content.GetRect(); x != 2 || y != 1 || width != 14 || height != 5 {
		t.Fatalf("content rect = (%d, %d, %d, %d), want (2, 1, 14, 5)", x, y, width, height)
	}
}

func TestFloatingOverlayOpaqueBackgroundCoversUnderlyingText(t *testing.T) {
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	screen.SetSize(30, 12)
	for row := 0; row < 12; row++ {
		for column := 0; column < 30; column++ {
			screen.SetContent(column, row, 'x', nil, tcell.StyleDefault)
		}
	}

	content := &transparentTestPrimitive{Box: tview.NewBox()}
	overlay := newFloatingOverlay(content, 16, 6).SetOpaqueBackground(tcell.ColorRed)
	overlay.SetRect(0, 0, 30, 12)
	overlay.Draw(screen)

	if outside, _, _, _ := screen.GetContent(0, 0); outside != 'x' {
		t.Fatalf("outside background = %q, want unchanged", outside)
	}
	inside, _, style, _ := screen.GetContent(10, 5)
	_, background, _ := style.Decompose()
	if inside != ' ' || background != tcell.ColorRed {
		t.Fatalf("opaque background = rune %q color %v", inside, background)
	}
	for _, column := range []int{6, 23} {
		halo, _, haloStyle, _ := screen.GetContent(column, 5)
		_, haloBackground, _ := haloStyle.Decompose()
		if halo != ' ' || haloBackground != tcell.ColorRed {
			t.Fatalf("opaque full-width-character guard at column %d = rune %q color %v", column, halo, haloBackground)
		}
	}
}
