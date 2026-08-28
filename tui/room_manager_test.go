package tui

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"bili-live-tui/internal/api"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

type managementTestTransport func(*http.Request) (*http.Response, error)

func (f managementTestTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestRoomManagerSearchPopupOwnsFocus(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	updates := make(chan func(), 16)
	client := api.NewClient(&http.Client{Transport: managementTestTransport(func(req *http.Request) (*http.Response, error) {
		body := `{"code":0,"data":{"data":[],"page":{"page":1,"total_page":1}}}`
		if strings.Contains(req.URL.Path, "senior_switch") {
			body = `{"code":0,"data":{"status":0}}`
		}
		if strings.Contains(req.URL.Path, "search_user") {
			body = `{"code":0,"data":{"items":[{"uid":123,"uname":"测试用户"}]}}`
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})})
	app := tview.NewApplication()
	pages := tview.NewPages().AddPage("main", tview.NewBox(), true, true)
	workspace := newRoomManagerWorkspace(roomManagerDependencies{
		Context: ctx, App: app, Pages: pages, Client: client,
		RoomID: "1", Sessdata: "sess", BiliJCT: "jct",
		QueueUI: func(f func()) {
			select {
			case updates <- f:
			case <-ctx.Done():
			}
		},
		Capabilities: func() api.RoomManagementCapabilities {
			return api.RoomManagementCapabilities{IsAnchor: true, AnchorID: "1", UserID: "1"}
		},
		RefreshCapabilities: func(done func(error)) { done(nil) },
		Status:              tview.NewTextView(), ReturnToChat: func() {}, Quit: func() {},
	})
	workspace.open()
	// 空列表加载后，从栏目向右应聚焦“添加房管”。
	deadline := time.After(time.Second)
	for {
		select {
		case update := <-updates:
			update()
			workspace.capture(tcell.NewEventKey(tcell.KeyRight, 0, tcell.ModNone))
			if button, ok := app.GetFocus().(*tview.Button); ok && button.GetLabel() == "添加房管" {
				goto loaded
			}
		case <-deadline:
			t.Fatal("room manager did not load")
		}
	}
loaded:
	app.GetFocus().InputHandler()(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone), func(p tview.Primitive) { app.SetFocus(p) })
	if front, _ := pages.GetFrontPage(); front != "room-manager-input" {
		t.Fatalf("front page = %q", front)
	}
	field, ok := app.GetFocus().(*tview.InputField)
	if !ok {
		t.Fatalf("search did not focus input: %T", app.GetFocus())
	}
	field.SetText("测试用户")
	workspace.capture(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	deadline = time.After(time.Second)
	for {
		select {
		case update := <-updates:
			update()
			if table, ok := app.GetFocus().(*tview.Table); ok && table.GetRowCount() > 1 {
				return
			}
		case <-deadline:
			t.Fatalf("search results did not receive focus: %T", app.GetFocus())
		}
	}
}
