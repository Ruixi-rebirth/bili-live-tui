package tui

import (
	"context"
	"errors"
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

// 测试显式执行 UI 队列，避免用睡眠猜测网络回调和页面切换的先后顺序。
func newManagementTestWorkspace(t *testing.T) (*roomManagerWorkspace, <-chan func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	updates := make(chan func(), 16)
	app := tview.NewApplication()
	pages := tview.NewPages().AddPage("main", tview.NewBox(), true, true)
	w := newRoomManagerWorkspace(roomManagerDependencies{
		Context: ctx, App: app, Pages: pages,
		Capabilities: func() api.RoomManagementCapabilities { return api.RoomManagementCapabilities{} },
		QueueUI: func(f func()) {
			select {
			case updates <- f:
			case <-ctx.Done():
			}
		},
		Status: tview.NewTextView().SetDynamicColors(true), ReturnToChat: func() {}, Quit: func() {},
	})
	w.visible = true
	pages.SwitchToPage("room-manager")
	app.SetFocus(w.navigation)
	return w, updates
}

func applyManagementTestUpdate(t *testing.T, updates <-chan func()) {
	t.Helper()
	select {
	case update := <-updates:
		update()
	case <-time.After(3 * time.Second):
		t.Fatal("room manager did not submit a UI update")
	}
}

func TestRoomManagerLoadingDoesNotDependOnCopy(t *testing.T) {
	w, _ := newManagementTestWorkspace(t)
	w.showLoading()
	if !w.loading {
		t.Fatal("loading state was not set")
	}
	w.showEmptyView("正在加载……")
	if w.loading {
		t.Fatal("display text was interpreted as a loading state")
	}
	w.focusContent()
	if w.deps.App.GetFocus() != w.backButton {
		t.Fatal("empty state did not allow focusing the return button")
	}
}

func TestRoomManagerTabBehaviorDoesNotDependOnLabel(t *testing.T) {
	w, _ := newManagementTestWorkspace(t)
	page, loads := 7, 0
	w.tabs = []roomManagerTab{{label: "任意新名称", actionMode: "keyword", page: &page, load: func() { loads++ }}}
	w.navigation.AddItem(w.tabs[0].label, "", 0, nil)
	w.selectTab(0)
	if page != 1 || loads != 1 || w.baseActionMode != "keyword" || w.currentButtons[0] != w.addKeywordButton {
		t.Fatalf("tab metadata was not applied: page=%d loads=%d mode=%q", page, loads, w.baseActionMode)
	}
	w.canNextPage = true
	w.nextPage()
	if page != 2 || loads != 2 {
		t.Fatalf("pagination depends on label: page=%d loads=%d", page, loads)
	}
	w.canPrevPage = true
	w.prevPage()
	if page != 1 || loads != 3 {
		t.Fatalf("previous page failed: page=%d loads=%d", page, loads)
	}
	w.prevPage()
	w.canNextPage = false
	w.nextPage()
	w.canNextPage = true
	w.loading = true
	w.nextPage()
	if page != 1 || loads != 3 {
		t.Fatal("out-of-range or loading pagination was accepted")
	}
}

func TestRoomManagerActionReportsResultAfterNavigation(t *testing.T) {
	for _, location := range []string{"another_tab", "chat"} {
		for _, result := range []string{"success", "failure"} {
			t.Run(location+"_"+result, func(t *testing.T) {
				w, updates := newManagementTestWorkspace(t)
				w.pendingLabel = "添加禁言用户"
				w.pendingAction = func(context.Context) error {
					if result == "failure" {
						return errors.New("服务器拒绝请求")
					}
					return nil
				}
				w.reload = func() { t.Fatal("old operation reloaded a page after navigation") }
				w.runAction()
				if location == "chat" {
					w.close()
				} else {
					w.showLoading()
					w.setNotice("", false)
				}
				focused := tview.NewInputField()
				w.deps.App.SetFocus(focused)
				applyManagementTestUpdate(t, updates)
				if w.deps.App.GetFocus() != focused {
					t.Fatal("completed operation stole focus")
				}
				message := w.notice.GetText(true)
				if location == "chat" {
					message = w.deps.Status.GetText(true)
				}
				want := "添加禁言用户成功"
				if result == "failure" {
					want = "添加禁言用户失败：服务器拒绝请求"
				}
				if message != want {
					t.Fatalf("operation feedback = %q, want %q", message, want)
				}
			})
		}
	}
}

func TestRoomManagerKeywordsEscapeTerminalMarkup(t *testing.T) {
	w, updates := newManagementTestWorkspace(t)
	w.deps.RoomID = "1"
	w.deps.Sessdata = "test-session"
	w.deps.BiliJCT = "test-csrf"
	w.deps.Client = api.NewClient(&http.Client{Transport: managementTestTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"code":0,"data":{"keyword_list":[{"keyword":"[red]测试"}]}}`))}, nil
	})})
	w.loadKeywords()
	applyManagementTestUpdate(t, updates)
	if got := w.table.GetCell(1, 0).Text; got != tview.Escape("[red]测试") {
		t.Fatalf("keyword is interpreted as terminal markup: %q", got)
	}
}

func TestRoomManagerErrorNoticeEscapesOnlyOnce(t *testing.T) {
	w, _ := newManagementTestWorkspace(t)
	generation := w.showLoading()
	w.showError(generation, "加载", errors.New("[red]错误[-]"), nil)
	if got := w.notice.GetText(true); got != "加载 失败：[red]错误[-]" {
		t.Fatalf("error notice contains extra escape markers: %q", got)
	}
}
