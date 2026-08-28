package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"bili-live-tui/internal/api"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// roomManagerDependencies 明确工作区与弹幕页之间的边界。
// 页面状态、分页、搜索及焦点均由工作区自己持有，网络结果通过 QueueUI 提交。
type roomManagerDependencies struct {
	Context                   context.Context
	App                       *tview.Application
	Pages                     *tview.Pages
	Client                    *api.Client
	RoomID, Sessdata, BiliJCT string
	QueueUI                   func(func())
	Capabilities              func() api.RoomManagementCapabilities
	RefreshCapabilities       func(func(error))
	Status                    *tview.TextView
	ReturnToChat              func()
	Quit                      func()
}

type roomManagerUserOperation int

const (
	roomManagerUserAddAdmin roomManagerUserOperation = iota
	roomManagerUserMute
	roomManagerUserBlacklist
)

type roomManagerTab struct {
	label string
	load  func()
}

type roomManagerWorkspace struct {
	deps roomManagerDependencies

	navigation   *tview.List
	section      *tview.TextView
	notice       *tview.TextView
	empty        *tview.TextView
	emptyCenter  *tview.Flex
	errorText    *tview.TextView
	errorCenter  *tview.Flex
	table        *tview.Table
	tableLayout  *tview.Grid
	tableActions map[int]func()
	content      *tview.Pages
	contentPanel *tview.Flex
	actionBar    *buttonGrid

	confirmModal       *confirmModal
	input              *inputDialog
	inputPreviousFocus tview.Primitive

	backButton          *tview.Button
	retryButton         *tview.Button
	addAdminButton      *tview.Button
	muteUserButton      *tview.Button
	blacklistUserButton *tview.Button
	closeSilentButton   *tview.Button
	searchAgainButton   *tview.Button
	flowBackButton      *tview.Button
	prevButton          *tview.Button
	nextButton          *tview.Button
	currentButtons      []*tview.Button

	visible               bool
	confirmVisible        bool
	inputVisible          bool
	loading               bool
	generation            uint64
	focusContentAfterLoad bool
	baseActionMode        string
	flowSearchable        bool
	canPrevPage           bool
	canNextPage           bool

	reload                 func()
	retryAction            func()
	flowBack               func()
	pendingAction          func(context.Context) error
	pendingLabel           string
	triggerCloseRoomSilent func()

	tabs        []roomManagerTab
	tabIndex    int
	navSyncing  bool
	adminPage   int
	mutedPage   int
	blacklistPage int
	adminTotalPages int
	mutedTotalPages int
	blacklistTotalPages int

	searchOp           roomManagerUserOperation
	searchQuery        string
	searchResults      []api.RoomUserSearchResult
	adminLevels        map[string]int
	seniorAdminKnown   bool
	seniorAdminEnabled bool
	seniorAdminLoading bool
	capabilities       api.RoomManagementCapabilities
}

func newRoomManagerWorkspace(deps roomManagerDependencies) *roomManagerWorkspace {
	w := &roomManagerWorkspace{
		deps:                deps,
		tableActions:        make(map[int]func()),
		adminLevels:         make(map[string]int),
		tabs:                make([]roomManagerTab, 0, 4),
		searchResults:       make([]api.RoomUserSearchResult, 0),
		adminPage:           1,
		mutedPage:           1,
		blacklistPage:       1,
		adminTotalPages:     1,
		mutedTotalPages:     1,
		blacklistTotalPages: 1,
		baseActionMode:      "default",
		capabilities:        deps.Capabilities(),
	}

	w.navigation = newDanmakuManagementList(" 栏目 ")
	w.navigation.ShowSecondaryText(false)
	w.navigation.SetHighlightFullLine(true)
	w.navigation.SetSelectedFocusOnly(noColor)
	w.navigation.SetBorderPadding(1, 0, 1, 1)

	w.section = tview.NewTextView().SetDynamicColors(true).SetTextAlign(tview.AlignCenter)
	w.section.SetBackgroundColor(panelColor)
	w.section.SetTextColor(tview.Styles.SecondaryTextColor)

	w.notice = tview.NewTextView().SetDynamicColors(true).SetTextAlign(tview.AlignCenter)
	w.notice.SetBackgroundColor(panelColor)

	w.empty = tview.NewTextView().SetDynamicColors(true).SetTextAlign(tview.AlignCenter)
	w.empty.SetBackgroundColor(panelColor)

	w.emptyCenter = tview.NewFlex().SetDirection(tview.FlexRow)
	w.emptyCenter.SetBackgroundColor(panelColor)
	w.emptyCenter.AddItem(nil, 0, 1, false)
	w.emptyCenter.AddItem(w.empty, 4, 0, false)
	w.emptyCenter.AddItem(nil, 0, 1, false)

	w.errorText = tview.NewTextView().SetDynamicColors(true).SetTextAlign(tview.AlignCenter)
	w.errorText.SetBackgroundColor(panelColor)

	w.errorCenter = tview.NewFlex().SetDirection(tview.FlexRow)
	w.errorCenter.SetBackgroundColor(panelColor)
	w.errorCenter.AddItem(nil, 0, 1, false)
	w.errorCenter.AddItem(w.errorText, 6, 0, false)
	w.errorCenter.AddItem(nil, 0, 1, false)

	w.backButton = newActionButton("返回弹幕", w.close)
	w.retryButton = newActionButton("重新加载", func() {
		if w.retryAction != nil {
			w.focusContentAfterLoad = true
			w.retryAction()
		}
	})
	w.addAdminButton = newActionButton("添加房管", func() { w.openUserSearch(roomManagerUserAddAdmin) })
	w.muteUserButton = newActionButton("添加禁言用户", func() { w.openUserSearch(roomManagerUserMute) })
	w.blacklistUserButton = newActionButton("添加黑名单用户", func() { w.openUserSearch(roomManagerUserBlacklist) })
	w.closeSilentButton = newActionButton("关闭全局禁言", func() {
		if w.triggerCloseRoomSilent != nil {
			w.triggerCloseRoomSilent()
		}
	})
	w.searchAgainButton = newActionButton("重新查找", func() { w.openUserSearch(w.searchOp) })
	w.flowBackButton = newActionButton("返回列表", func() {
		if w.flowBack != nil {
			w.focusContentAfterLoad = true
			w.flowBack()
		}
	})
	w.prevButton = newActionButton("上一页", func() {
		w.focusContentAfterLoad = true
		w.prevPage()
	})
	w.nextButton = newActionButton("下一页", func() {
		w.focusContentAfterLoad = true
		w.nextPage()
	})

	w.actionBar = newButtonGrid(buttonGridColumns).SetButtons([]*tview.Button{w.backButton})
	w.updateActionBar("default")

	w.table = tview.NewTable()
	w.table.SetBackgroundColor(panelColor)
	w.table.SetBorderPadding(0, 0, 1, 1)
	w.table.SetBorders(false)
	w.table.SetFixed(1, 0)
	w.table.SetSelectable(true, false)
	w.table.SetEvaluateAllRows(true)
	configureTableFocusStyle(w.table)
	w.table.SetSelectedFunc(func(row, _ int) {
		if action := w.tableActions[row]; action != nil {
			action()
		}
	})

	w.tableLayout = tview.NewGrid().SetColumns(0)
	w.tableLayout.SetBackgroundColor(panelColor)
	w.tableLayout.AddItem(w.table, 0, 0, 1, 1, 0, 0, true)

	w.content = tview.NewPages()
	w.content.SetBackgroundColor(panelColor)
	w.content.AddPage("table", w.tableLayout, true, true)
	w.content.AddPage("empty", w.emptyCenter, true, false)
	w.content.AddPage("error", w.errorCenter, true, false)

	w.contentPanel = tview.NewFlex().SetDirection(tview.FlexRow)
	w.contentPanel.SetBackgroundColor(panelColor)
	w.contentPanel.SetBorder(true)
	w.contentPanel.SetBorderColor(tview.Styles.BorderColor)
	w.contentPanel.AddItem(w.section, 1, 0, false)
	w.contentPanel.AddItem(w.notice, 1, 0, false)
	w.contentPanel.AddItem(w.content, 0, 1, true)
	w.contentPanel.AddItem(w.actionBar, w.actionBar.PreferredHeight(), 0, true)
	w.actionBar.AddChangedFunc(func() {
		w.contentPanel.ResizeItem(w.actionBar, max(w.actionBar.PreferredHeight(), 1), 0)
	})

	panel := tview.NewFlex().SetDirection(tview.FlexColumn)
	panel.SetBackgroundColor(panelColor)
	panel.AddItem(w.navigation, 16, 0, true)
	panel.AddItem(nil, 1, 0, false)
	panel.AddItem(w.contentPanel, 0, 1, true)

	page := workspacePage(
		workspaceHeader("房间管理"),
		panel,
		pageFooter("↑/↓ 选择 · ←/→ 切换区域 · Tab 切换焦点 · Enter 操作 · [ / ] 翻页 · Esc 返回"),
	)

	w.confirmModal = newConfirmModal("房管管理确认")
	w.confirmModal.SetDoneFunc(func(buttonIndex int, _ string) {
		if buttonIndex == 1 && w.pendingAction != nil {
			w.runAction()
			return
		}
		w.confirmVisible = false
		w.pendingAction = nil
		w.deps.Pages.HidePage("room-manager-confirm")
		w.deps.Pages.SendToFront("room-manager")
		w.focusContent()
	})

	w.navigation.SetChangedFunc(func(index int, _, _ string, _ rune) {
		if w.navSyncing || !w.visible || w.confirmVisible || w.inputVisible {
			return
		}
		if index >= 0 && index < len(w.tabs) && index != w.tabIndex {
			w.selectTab(index)
		}
	})

	deps.Pages.AddPage("room-manager", page, true, false)
	deps.Pages.AddPage("room-manager-confirm", w.confirmModal, true, false)
	return w
}

func (w *roomManagerWorkspace) open() {
	w.deps.Status.SetText("正在获取房间管理权限，请稍候……")
	w.deps.RefreshCapabilities(func(err error) {
		w.capabilities = w.deps.Capabilities()
		if err != nil {
			w.deps.Status.SetText("获取房间管理权限失败：" + tview.Escape(err.Error()))
			return
		}
		if !w.capabilities.IsAnchor && !w.capabilities.IsAdmin {
			w.deps.Status.SetText("当前账号不是本直播间的主播或房管。")
			return
		}
		w.deps.Status.SetText("")
		w.visible = true
		w.showRoot()
		w.deps.Pages.SwitchToPage("room-manager")
		w.deps.App.SetFocus(w.navigation)
	})
}

func (w *roomManagerWorkspace) close() {
	w.loading = false
	w.generation++
	w.visible = false
	w.confirmVisible = false
	w.inputVisible = false
	w.retryAction = nil
	w.flowBack = nil
	w.flowSearchable = false
	w.focusContentAfterLoad = false
	w.pendingAction = nil
	w.setNotice("", false)
	w.deps.Pages.HidePage("room-manager-confirm")
	w.deps.Pages.RemovePage("room-manager-input")
	w.deps.Pages.SwitchToPage("main")
	w.deps.ReturnToChat()
}

func (w *roomManagerWorkspace) isActionBarFocused() bool {
	for _, btn := range w.currentButtons {
		if btn != nil && btn.HasFocus() {
			return true
		}
	}
	return false
}

func (w *roomManagerWorkspace) focusedButtonIndex() int {
	for i, btn := range w.currentButtons {
		if btn != nil && btn.HasFocus() {
			return i
		}
	}
	return -1
}

func (w *roomManagerWorkspace) updateActionBar(mode string) {
	previousFocus := w.deps.App.GetFocus()
	wasActionButton := previousFocus == w.backButton ||
		previousFocus == w.retryButton ||
		previousFocus == w.addAdminButton ||
		previousFocus == w.muteUserButton ||
		previousFocus == w.blacklistUserButton ||
		previousFocus == w.closeSilentButton ||
		previousFocus == w.searchAgainButton ||
		previousFocus == w.flowBackButton ||
		previousFocus == w.prevButton ||
		previousFocus == w.nextButton

	w.currentButtons = w.currentButtons[:0]
	if mode == "error" {
		w.currentButtons = []*tview.Button{w.retryButton, w.backButton}
	} else {
		switch mode {
		case "admin":
			w.currentButtons = append(w.currentButtons, w.addAdminButton)
		case "mute":
			w.currentButtons = append(w.currentButtons, w.muteUserButton)
		case "blacklist":
			w.currentButtons = append(w.currentButtons, w.blacklistUserButton)
		case "silent-active":
			w.currentButtons = append(w.currentButtons, w.closeSilentButton)
		case "flow":
			if w.flowSearchable {
				w.currentButtons = append(w.currentButtons, w.searchAgainButton)
			}
			w.currentButtons = append(w.currentButtons, w.flowBackButton)
		}
		if w.canPrevPage {
			w.currentButtons = append(w.currentButtons, w.prevButton)
		}
		if w.canNextPage {
			w.currentButtons = append(w.currentButtons, w.nextButton)
		}
		w.currentButtons = append(w.currentButtons, w.backButton)
	}
	w.actionBar.SetButtons(w.currentButtons)
	if wasActionButton {
		focusStillVisible := false
		for _, button := range w.currentButtons {
			if previousFocus == button {
				focusStillVisible = true
				break
			}
		}
		if !focusStillVisible {
			w.deps.App.SetFocus(w.navigation)
		}
	}
}

func (w *roomManagerWorkspace) focusContent() {
	if w.loading {
		w.deps.App.SetFocus(w.navigation)
		return
	}
	frontPage, _ := w.content.GetFrontPage()
	switch {
	case frontPage == "table" && w.table.GetRowCount() > 1:
		w.deps.App.SetFocus(w.table)
	case frontPage == "error" && w.retryAction != nil:
		w.deps.App.SetFocus(w.retryButton)
	case len(w.currentButtons) > 0:
		w.deps.App.SetFocus(w.currentButtons[0])
	default:
		w.deps.App.SetFocus(w.navigation)
	}
}

func (w *roomManagerWorkspace) restoreFocusAfterLoad() {
	if !w.focusContentAfterLoad {
		return
	}
	w.focusContentAfterLoad = false
	w.focusContent()
}

func (w *roomManagerWorkspace) setNotice(message string, failed bool) {
	message = strings.TrimSpace(message)
	if message == "" {
		w.notice.SetText("")
		return
	}
	msgRunes := []rune(strings.Join(strings.Fields(message), " "))
	if len(msgRunes) > 60 {
		message = string(msgRunes[:60]) + "…"
	} else {
		message = string(msgRunes)
	}
	color := accentActiveColor
	if failed {
		color = errorColor
	}
	w.notice.SetText("[" + color.String() + "]" + tview.Escape(message) + "[-]")
}

func (w *roomManagerWorkspace) showTableView() {
	w.loading = false
	w.inputVisible = false
	w.retryAction = nil
	retryWasFocused := w.deps.App.GetFocus() == w.retryButton
	w.updateActionBar(w.baseActionMode)
	w.content.SwitchToPage("table")
	if retryWasFocused {
		w.deps.App.SetFocus(w.navigation)
	}
}

func (w *roomManagerWorkspace) showEmptyView(message string) {
	w.loading = (message == "正在加载……")
	w.inputVisible = false
	emptyContent := fmt.Sprintf("[%s::b]%s[-:-:-]",
		tview.Styles.PrimaryTextColor.String(),
		tview.Escape(message),
	)
	w.empty.SetText(emptyContent)
	w.content.SwitchToPage("empty")
	if w.deps.App.GetFocus() == w.table {
		w.deps.App.SetFocus(w.navigation)
	}
}

func (w *roomManagerWorkspace) showLoading(_ string) uint64 {
	w.generation++
	generation := w.generation
	w.retryAction = nil
	w.canPrevPage = false
	w.canNextPage = false
	w.updateActionBar("default")
	w.section.SetText("[" + mutedColor.String() + "]正在加载……[-]")
	w.showEmptyView("正在加载……")
	return generation
}

func (w *roomManagerWorkspace) showError(generation uint64, title string, err error, retry func()) {
	if !w.visible || generation != w.generation {
		return
	}
	w.loading = false
	w.inputVisible = false
	w.retryAction = retry
	w.section.SetText("[" + errorColor.String() + "]请求异常[-]")
	failSuffix := "失败"
	if strings.HasSuffix(title, "列表") || title == "查找用户" || title == "搜索用户" {
		failSuffix = "加载失败"
	}
	cardTitle := fmt.Sprintf("%s %s", title, failSuffix)
	if strings.HasSuffix(title, "失败") {
		cardTitle = title
	}
	w.setNotice(cardTitle+"："+compactDanmakuManagementError(err), true)
	errCard := fmt.Sprintf("[%s::b]%s[-:-:-]\n\n[%s]%s[-]",
		errorColor.String(),
		tview.Escape(cardTitle),
		tview.Styles.PrimaryTextColor.String(),
		compactDanmakuManagementError(err),
	)
	w.errorText.SetText(errCard)
	w.content.SwitchToPage("error")
	w.updateActionBar("error")
	w.focusContentAfterLoad = false
	w.deps.App.SetFocus(w.retryButton)
}

func (w *roomManagerWorkspace) runAction() {
	action := w.pendingAction
	label := w.pendingLabel
	w.pendingAction = nil
	w.confirmVisible = false
	w.deps.Pages.HidePage("room-manager-confirm")
	w.focusContentAfterLoad = true
	w.deps.App.SetFocus(w.navigation)
	generation := w.showLoading(label)
	w.setNotice("正在"+label+"……", false)
	go func() {
		requestCtx, cancelRequest := context.WithTimeout(w.deps.Context, 10*time.Second)
		defer cancelRequest()
		err := action(requestCtx)
		w.deps.QueueUI(func() {
			if !w.visible || generation != w.generation {
				return
			}
			if err != nil {
				w.setNotice(label+"失败："+err.Error(), true)
				w.showError(generation, label, err, w.reload)
				return
			}
			w.setNotice(label+"成功。", false)
			if w.reload != nil {
				w.reload()
			} else {
				w.showRoot()
			}
		})
	}()
}

func (w *roomManagerWorkspace) openConfirm(label, warning string, action func(context.Context) error) {
	w.pendingLabel = label
	w.pendingAction = action
	w.confirmModal.ClearButtons().SetText(warning).AddButtons([]string{"取消", "确认"})
	w.confirmVisible = true
	w.deps.Pages.ShowPage("room-manager-confirm")
	w.deps.Pages.SendToFront("room-manager-confirm")
	w.deps.App.SetFocus(w.confirmModal)
}

func (w *roomManagerWorkspace) closeInput() {
	w.inputVisible = false
	w.deps.Pages.RemovePage("room-manager-input")
	if w.inputPreviousFocus != nil {
		w.deps.App.SetFocus(w.inputPreviousFocus)
	} else {
		w.focusContent()
	}
}

func (w *roomManagerWorkspace) openInput(title, label, initial string, _ int, submitLabel string, validate func(string) error, submitted func(string)) {
	w.inputPreviousFocus = w.deps.App.GetFocus()
	w.input = newInputDialog(w.deps.App, title, label, initial, submitLabel, validate, func(value string) {
		w.closeInput()
		submitted(value)
	}, w.closeInput)
	w.inputVisible = true
	w.deps.Pages.AddPage("room-manager-input", w.input.root, true, true)
	w.deps.App.SetFocus(w.input.field)
}

func (w *roomManagerWorkspace) setFlowBack(label string, action func(), searchable bool) {
	w.flowBackButton.SetLabel(label)
	w.flowBack = action
	w.flowSearchable = searchable
	w.canPrevPage = false
	w.canNextPage = false
	w.updateActionBar("flow")
}

func (w *roomManagerWorkspace) returnToList() {
	w.flowBack = nil
	w.flowSearchable = false
	w.focusContentAfterLoad = true
	w.setNotice("", false)
	if w.reload != nil {
		w.reload()
	}
}

func (w *roomManagerWorkspace) prevPage() {
	if !w.canPrevPage || len(w.tabs) == 0 || w.tabIndex >= len(w.tabs) {
		return
	}
	switch w.tabs[w.tabIndex].label {
	case "房管":
		if w.adminPage > 1 {
			w.focusContentAfterLoad = true
			w.adminPage--
			w.loadAdmins()
		}
	case "禁言":
		if w.mutedPage > 1 {
			w.focusContentAfterLoad = true
			w.mutedPage--
			w.loadMutedUsers()
		}
	case "黑名单":
		if w.blacklistPage > 1 {
			w.focusContentAfterLoad = true
			w.blacklistPage--
			w.loadBlacklistedUsers()
		}
	}
}

func (w *roomManagerWorkspace) nextPage() {
	if !w.canNextPage || len(w.tabs) == 0 || w.tabIndex >= len(w.tabs) {
		return
	}
	switch w.tabs[w.tabIndex].label {
	case "房管":
		if w.adminPage < w.adminTotalPages {
			w.focusContentAfterLoad = true
			w.adminPage++
			w.loadAdmins()
		}
	case "禁言":
		if w.mutedPage < w.mutedTotalPages {
			w.focusContentAfterLoad = true
			w.mutedPage++
			w.loadMutedUsers()
		}
	case "黑名单":
		if w.canNextPage {
			w.focusContentAfterLoad = true
			w.blacklistPage++
			w.loadBlacklistedUsers()
		}
	}
}

func (w *roomManagerWorkspace) showRoot() {
	w.tabs = w.tabs[:0]
	if w.capabilities.IsAnchor {
		w.tabs = append(w.tabs, roomManagerTab{label: "房管", load: func() {
			w.adminPage = 1
			w.loadAdmins()
		}})
	}
	if w.capabilities.CanMute(0) {
		w.tabs = append(w.tabs, roomManagerTab{label: "禁言", load: func() {
			w.mutedPage = 1
			w.loadMutedUsers()
		}})
	}
	if w.capabilities.CanBlacklist(0) && w.capabilities.AnchorID != "" {
		w.tabs = append(w.tabs, roomManagerTab{label: "黑名单", load: func() {
			w.blacklistPage = 1
			w.loadBlacklistedUsers()
		}})
	}
	if w.capabilities.IsAnchor {
		w.tabs = append(w.tabs, roomManagerTab{label: "全局禁言", load: w.loadRoomSilent})
	}
	if w.tabIndex >= len(w.tabs) {
		w.tabIndex = 0
	}
	w.navSyncing = true
	w.navigation.Clear()
	for _, tab := range w.tabs {
		w.navigation.AddItem(tab.label, "", 0, func() {
			w.focusContent()
		})
	}
	w.navigation.SetCurrentItem(w.tabIndex)
	w.navSyncing = false
	w.selectTab(w.tabIndex)
}

func (w *roomManagerWorkspace) selectTab(index int) {
	if len(w.tabs) == 0 {
		w.close()
		return
	}
	index = (index%len(w.tabs) + len(w.tabs)) % len(w.tabs)
	w.tabIndex = index
	w.navSyncing = true
	w.navigation.SetCurrentItem(index)
	w.navSyncing = false
	tab := w.tabs[index]
	switch tab.label {
	case "房管":
		w.baseActionMode = "admin"
	case "禁言":
		w.baseActionMode = "mute"
	case "黑名单":
		w.baseActionMode = "blacklist"
	default:
		w.baseActionMode = "default"
	}
	w.canPrevPage = false
	w.canNextPage = false
	w.flowSearchable = false
	w.focusContentAfterLoad = false
	w.showTableView()
	w.setNotice("", false)
	w.reload = tab.load
	tab.load()
}

func (w *roomManagerWorkspace) capture(event *tcell.EventKey) *tcell.EventKey {
	if w.confirmVisible {
		switch event.Key() {
		case tcell.KeyEscape:
			w.confirmVisible = false
			w.pendingAction = nil
			w.deps.Pages.HidePage("room-manager-confirm")
			w.deps.Pages.SendToFront("room-manager")
			w.focusContent()
			return nil
		case tcell.KeyCtrlC:
			w.deps.Quit()
			return nil
		default:
			return event
		}
	}
	if w.inputVisible {
		if event.Key() == tcell.KeyCtrlC {
			w.deps.Quit()
			return nil
		}
		return w.input.capture(event)
	}
	if w.visible {
		switch {
		case event.Key() == tcell.KeyEscape:
			w.close()
			return nil
		case event.Key() == tcell.KeyLeft:
			if w.isActionBarFocused() {
				idx := w.focusedButtonIndex()
				if idx > 0 {
					w.deps.App.SetFocus(w.currentButtons[idx-1])
					return nil
				}
			}
			if !w.navigation.HasFocus() {
				w.deps.App.SetFocus(w.navigation)
			}
			return nil
		case event.Key() == tcell.KeyRight:
			if w.navigation.HasFocus() {
				w.focusContent()
				return nil
			}
			if w.isActionBarFocused() {
				idx := w.focusedButtonIndex()
				if idx >= 0 && idx < len(w.currentButtons)-1 {
					w.deps.App.SetFocus(w.currentButtons[idx+1])
				}
			}
			return nil
		case event.Key() == tcell.KeyUp:
			if w.isActionBarFocused() {
				idx := w.focusedButtonIndex()
				if idx >= buttonGridColumns {
					w.deps.App.SetFocus(w.currentButtons[idx-buttonGridColumns])
					return nil
				}
				w.focusContent()
				return nil
			}
			return event
		case event.Key() == tcell.KeyDown:
			if w.isActionBarFocused() {
				idx := w.focusedButtonIndex()
				if idx >= 0 && idx+buttonGridColumns < len(w.currentButtons) {
					w.deps.App.SetFocus(w.currentButtons[idx+buttonGridColumns])
				}
				return nil
			}
			return event
		case event.Key() == tcell.KeyTab:
			if w.navigation.HasFocus() {
				w.focusContent()
				return nil
			}
			if w.isActionBarFocused() {
				idx := w.focusedButtonIndex()
				if idx >= 0 && idx < len(w.currentButtons)-1 {
					w.deps.App.SetFocus(w.currentButtons[idx+1])
				} else {
					w.deps.App.SetFocus(w.navigation)
				}
				return nil
			}
			if len(w.currentButtons) > 0 {
				w.deps.App.SetFocus(w.currentButtons[0])
				return nil
			}
			w.deps.App.SetFocus(w.navigation)
			return nil
		case event.Key() == tcell.KeyBacktab:
			if w.navigation.HasFocus() {
				if len(w.currentButtons) > 0 {
					w.deps.App.SetFocus(w.currentButtons[len(w.currentButtons)-1])
				}
				return nil
			}
			if w.isActionBarFocused() {
				idx := w.focusedButtonIndex()
				if idx > 0 {
					w.deps.App.SetFocus(w.currentButtons[idx-1])
				} else {
					frontPage, _ := w.content.GetFrontPage()
					if frontPage == "table" && w.table.GetRowCount() > 1 {
						w.deps.App.SetFocus(w.table)
					} else {
						w.deps.App.SetFocus(w.navigation)
					}
				}
				return nil
			}
			w.deps.App.SetFocus(w.navigation)
			return nil
		case event.Key() == tcell.KeyRune && event.Rune() == '[':
			w.prevPage()
			return nil
		case event.Key() == tcell.KeyRune && event.Rune() == ']':
			w.nextPage()
			return nil
		case event.Key() == tcell.KeyPgUp:
			w.prevPage()
			return nil
		case event.Key() == tcell.KeyPgDn:
			w.nextPage()
			return nil
		case event.Key() == tcell.KeyCtrlC:
			w.deps.Quit()
			return nil
		default:
			return event
		}
	}
	return event
}
