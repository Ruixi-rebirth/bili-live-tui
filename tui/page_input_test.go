package tui

import (
	"testing"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

func TestUserManagementInputStaysOnTopPage(t *testing.T) {
	pages := tview.NewPages()
	var closed []string
	quit := false
	cardInput := 0
	handlers := pageInputHandlers{
		"user-card": func(event *tcell.EventKey) *tcell.EventKey {
			cardInput++
			return nil
		},
	}
	pages.AddPage("user-card", tview.NewBox(), true, true)
	for _, name := range []string{"warning-dialog", "confirm-stop", "management-confirm"} {
		name := name
		pages.AddPage(name, tview.NewBox(), true, true)
		handlers[name] = modalInputHandler(func() {
			closed = append(closed, name)
			pages.HidePage(name)
		}, func() { quit = true })
	}
	for _, want := range []string{"management-confirm", "confirm-stop", "warning-dialog"} {
		for _, key := range []tcell.Key{tcell.KeyTab, tcell.KeyBacktab, tcell.KeyLeft, tcell.KeyRight, tcell.KeyEnter} {
			event := tcell.NewEventKey(key, 0, tcell.ModNone)
			got, handled := handlers.capture(pages, event)
			if !handled || got != event || cardInput != 0 {
				t.Fatalf("%s: key %v reached underlying card or was swallowed", want, key)
			}
		}
		got, handled := handlers.capture(pages, tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone))
		if !handled || got != nil || closed[len(closed)-1] != want {
			t.Fatalf("Escape must close only %s: %v", want, closed)
		}
	}
	if cardInput != 0 || quit {
		t.Fatalf("underlying input=%d quit=%v", cardInput, quit)
	}
	handlers.capture(pages, tcell.NewEventKey(tcell.KeyTab, 0, tcell.ModNone))
	if cardInput != 1 {
		t.Fatal("card should regain keyboard input when child popups close")
	}
}

func TestPageInputDoesNotInterceptOtherPages(t *testing.T) {
	pages := tview.NewPages().AddPage("main", tview.NewBox(), true, true)
	handlers := pageInputHandlers{"popup": modalInputHandler(func() { t.Fatal("unexpected close") }, func() { t.Fatal("unexpected quit") })}
	event := tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone)
	got, handled := handlers.capture(pages, event)
	if handled || got != event {
		t.Fatal("unrelated pages must retain their input handler")
	}
}
