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

type roomManagerWorkspace struct {
	open    func()
	capture func(*tcell.EventKey) *tcell.EventKey
}

func newRoomManagerWorkspace(deps roomManagerDependencies) *roomManagerWorkspace {
	app, pages, client := deps.App, deps.Pages, deps.Client
	streamCtx := deps.Context
	roomID, sessdata, biliJCT := deps.RoomID, deps.Sessdata, deps.BiliJCT
	queueUI, sendStatus := deps.QueueUI, deps.Status
	refreshManagementCapabilities := deps.RefreshCapabilities
	managementCapabilities := deps.Capabilities()
	roomManagerNavigation := newDanmakuManagementList(" 栏目 ")
	roomManagerNavigation.ShowSecondaryText(false)
	roomManagerNavigation.SetHighlightFullLine(true)
	roomManagerNavigation.SetSelectedFocusOnly(noColor)
	roomManagerNavigation.SetBorderPadding(1, 0, 1, 1)
	roomManagerSection := tview.NewTextView().SetDynamicColors(true).SetTextAlign(tview.AlignCenter)
	roomManagerSection.SetBackgroundColor(panelColor)
	roomManagerSection.SetTextColor(tview.Styles.SecondaryTextColor)
	roomManagerNotice := tview.NewTextView().SetDynamicColors(true).SetTextAlign(tview.AlignCenter)
	roomManagerNotice.SetBackgroundColor(panelColor)
	roomManagerEmpty := tview.NewTextView().SetDynamicColors(true).SetTextAlign(tview.AlignCenter)
	roomManagerEmpty.SetBackgroundColor(panelColor)
	roomManagerEmptyCenter := tview.NewFlex().SetDirection(tview.FlexRow)
	roomManagerEmptyCenter.SetBackgroundColor(panelColor)
	roomManagerEmptyCenter.AddItem(nil, 0, 1, false)
	roomManagerEmptyCenter.AddItem(roomManagerEmpty, 4, 0, false)
	roomManagerEmptyCenter.AddItem(nil, 0, 1, false)
	roomManagerErrorText := tview.NewTextView().SetDynamicColors(true).SetTextAlign(tview.AlignCenter)
	roomManagerErrorText.SetBackgroundColor(panelColor)
	roomManagerErrorCenter := tview.NewFlex().SetDirection(tview.FlexRow)
	roomManagerErrorCenter.SetBackgroundColor(panelColor)
	roomManagerErrorCenter.AddItem(nil, 0, 1, false)
	roomManagerErrorCenter.AddItem(roomManagerErrorText, 6, 0, false)
	roomManagerErrorCenter.AddItem(nil, 0, 1, false)
	var roomManagerInput *inputDialog
	var roomManagerPrevPage func()
	var roomManagerNextPage func()
	roomManagerBackButton := newActionButton("返回弹幕", nil)
	roomManagerRetryButton := newActionButton("重新加载", nil)
	roomManagerAddAdminButton := newActionButton("添加房管", nil)
	roomManagerMuteUserButton := newActionButton("添加禁言用户", nil)
	roomManagerBlacklistUserButton := newActionButton("添加黑名单用户", nil)
	roomManagerCloseSilentButton := newActionButton("关闭全局禁言", nil)
	roomManagerSearchAgainButton := newActionButton("重新查找", nil)
	roomManagerFlowBackButton := newActionButton("返回列表", nil)
	roomManagerPrevButton := newActionButton("上一页", nil)
	roomManagerNextButton := newActionButton("下一页", nil)
	roomManagerCanPrevPage := false
	roomManagerCanNextPage := false
	var roomManagerCurrentButtons []*tview.Button
	roomManagerBaseActionMode := "default"
	roomManagerFlowSearchable := false
	roomManagerActionBar := newButtonGrid(buttonGridColumns).SetButtons([]*tview.Button{roomManagerBackButton})
	updateRoomManagerActionBar := func(mode string) {
		previousFocus := app.GetFocus()
		wasActionButton := previousFocus == roomManagerBackButton ||
			previousFocus == roomManagerRetryButton ||
			previousFocus == roomManagerAddAdminButton ||
			previousFocus == roomManagerMuteUserButton ||
			previousFocus == roomManagerBlacklistUserButton ||
			previousFocus == roomManagerCloseSilentButton ||
			previousFocus == roomManagerSearchAgainButton ||
			previousFocus == roomManagerFlowBackButton ||
			previousFocus == roomManagerPrevButton ||
			previousFocus == roomManagerNextButton
		roomManagerCurrentButtons = roomManagerCurrentButtons[:0]
		if mode == "error" {
			roomManagerCurrentButtons = []*tview.Button{roomManagerRetryButton, roomManagerBackButton}
		} else {
			switch mode {
			case "admin":
				roomManagerCurrentButtons = append(roomManagerCurrentButtons, roomManagerAddAdminButton)
			case "mute":
				roomManagerCurrentButtons = append(roomManagerCurrentButtons, roomManagerMuteUserButton)
			case "blacklist":
				roomManagerCurrentButtons = append(roomManagerCurrentButtons, roomManagerBlacklistUserButton)
			case "silent-active":
				roomManagerCurrentButtons = append(roomManagerCurrentButtons, roomManagerCloseSilentButton)
			case "flow":
				if roomManagerFlowSearchable {
					roomManagerCurrentButtons = append(roomManagerCurrentButtons, roomManagerSearchAgainButton)
				}
				roomManagerCurrentButtons = append(roomManagerCurrentButtons, roomManagerFlowBackButton)
			}
			if roomManagerCanPrevPage {
				roomManagerCurrentButtons = append(roomManagerCurrentButtons, roomManagerPrevButton)
			}
			if roomManagerCanNextPage {
				roomManagerCurrentButtons = append(roomManagerCurrentButtons, roomManagerNextButton)
			}
			roomManagerCurrentButtons = append(roomManagerCurrentButtons, roomManagerBackButton)
		}
		roomManagerActionBar.SetButtons(roomManagerCurrentButtons)
		if wasActionButton {
			focusStillVisible := false
			for _, button := range roomManagerCurrentButtons {
				if previousFocus == button {
					focusStillVisible = true
					break
				}
			}
			if !focusStillVisible {
				app.SetFocus(roomManagerNavigation)
			}
		}
	}
	updateRoomManagerActionBar("default")
	isActionBarFocused := func() bool {
		for _, btn := range roomManagerCurrentButtons {
			if btn != nil && btn.HasFocus() {
				return true
			}
		}
		return false
	}
	focusedButtonIndex := func() int {
		for i, btn := range roomManagerCurrentButtons {
			if btn != nil && btn.HasFocus() {
				return i
			}
		}
		return -1
	}
	roomManagerTable := tview.NewTable()
	roomManagerTable.SetBackgroundColor(panelColor)
	roomManagerTable.SetBorderPadding(0, 0, 1, 1)
	roomManagerTable.SetBorders(false)
	roomManagerTable.SetFixed(1, 0)
	roomManagerTable.SetSelectable(true, false)
	roomManagerTable.SetEvaluateAllRows(true)
	configureTableFocusStyle(roomManagerTable)
	roomManagerTableActions := make(map[int]func())
	roomManagerTable.SetSelectedFunc(func(row, _ int) {
		if action := roomManagerTableActions[row]; action != nil {
			action()
		}
	})
	roomManagerTableLayout := tview.NewGrid().SetColumns(0)
	roomManagerTableLayout.SetBackgroundColor(panelColor)
	roomManagerTableLayout.AddItem(roomManagerTable, 0, 0, 1, 1, 0, 0, true)
	roomManagerContent := tview.NewPages()
	roomManagerContent.SetBackgroundColor(panelColor)
	roomManagerContent.AddPage("table", roomManagerTableLayout, true, true)
	roomManagerContent.AddPage("empty", roomManagerEmptyCenter, true, false)
	roomManagerContent.AddPage("error", roomManagerErrorCenter, true, false)
	roomManagerContentPanel := tview.NewFlex().SetDirection(tview.FlexRow)
	roomManagerContentPanel.SetBackgroundColor(panelColor)
	roomManagerContentPanel.SetBorder(true)
	roomManagerContentPanel.SetBorderColor(tview.Styles.BorderColor)
	roomManagerContentPanel.AddItem(roomManagerSection, 1, 0, false)
	roomManagerContentPanel.AddItem(roomManagerNotice, 1, 0, false)
	roomManagerContentPanel.AddItem(roomManagerContent, 0, 1, true)
	roomManagerContentPanel.AddItem(roomManagerActionBar, roomManagerActionBar.PreferredHeight(), 0, true)
	roomManagerActionBar.AddChangedFunc(func() {
		roomManagerContentPanel.ResizeItem(roomManagerActionBar, max(roomManagerActionBar.PreferredHeight(), 1), 0)
	})
	roomManagerPanel := tview.NewFlex().SetDirection(tview.FlexColumn)
	roomManagerPanel.SetBackgroundColor(panelColor)
	roomManagerPanel.AddItem(roomManagerNavigation, 16, 0, true)
	roomManagerPanel.AddItem(nil, 1, 0, false)
	roomManagerPanel.AddItem(roomManagerContentPanel, 0, 1, true)
	roomManagerPageFooter := pageFooter("↑/↓ 选择 · ←/→ 切换区域 · Tab 切换焦点 · Enter 操作 · [ / ] 翻页 · Esc 返回")
	roomManagerPage := workspacePage(
		workspaceHeader("房间管理"),
		roomManagerPanel,
		roomManagerPageFooter,
	)
	roomManagerConfirm := newConfirmModal("房管管理确认")
	roomManagerVisible := false
	roomManagerConfirmVisible := false
	roomManagerInputVisible := false
	roomManagerGeneration := uint64(0)
	type roomManagerTab struct {
		label string
		load  func()
	}
	roomManagerTabsAvailable := make([]roomManagerTab, 0, 4)
	roomManagerTabIndex := 0
	roomManagerNavigationSyncing := false
	roomAdminPage := 1
	mutedUserPage := 1
	blacklistPage := 1
	roomAdminTotalPages := 1
	blacklistTotalPages := 1
	mutedTotalPages := 1
	var showRoomManagerRoot func()
	var loadRoomAdmins func()
	var loadMutedUsers func()
	var loadBlacklistedUsers func()
	var loadRoomSilent func()
	var selectRoomManagerTab func(int)
	var roomManagerReload func()
	var roomManagerRetryAction func()
	var triggerAddRoomAdmin func()
	var triggerMuteRoomUser func()
	var triggerBlacklistRoomUser func()
	var triggerCloseRoomSilent func()
	var roomManagerFlowBack func()
	var pendingRoomManagerAction func(context.Context) error
	pendingRoomManagerLabel := ""
	roomManagerFocusContentAfterLoad := false
	type roomManagerUserOperation int
	const (
		roomManagerUserAddAdmin roomManagerUserOperation = iota
		roomManagerUserMute
		roomManagerUserBlacklist
	)
	roomManagerSearchOperation := roomManagerUserAddAdmin
	roomManagerSearchQuery := ""
	roomManagerSearchResults := make([]api.RoomUserSearchResult, 0)
	roomManagerAdminLevels := make(map[string]int)
	roomManagerSeniorAdminKnown := false
	roomManagerSeniorAdminEnabled := false
	roomManagerSeniorAdminLoading := false
	roomManagerLoading := false
	var renderRoomManagerSearchResults func()
	var showRoomManagerAdminChoices func(api.RoomUserSearchResult, bool)
	var showRoomManagerMuteChoices func(api.RoomUserSearchResult)
	var searchRoomManagerUsers func(roomManagerUserOperation, string)
	var openRoomManagerUserSearch func(roomManagerUserOperation)
	focusRoomManagerContent := func() {
		if roomManagerLoading {
			app.SetFocus(roomManagerNavigation)
			return
		}
		frontPage, _ := roomManagerContent.GetFrontPage()
		switch {
		case frontPage == "table" && roomManagerTable.GetRowCount() > 1:
			app.SetFocus(roomManagerTable)
		case frontPage == "error" && roomManagerRetryAction != nil:
			app.SetFocus(roomManagerRetryButton)
		case len(roomManagerCurrentButtons) > 0:
			app.SetFocus(roomManagerCurrentButtons[0])
		default:
			app.SetFocus(roomManagerNavigation)
		}
	}
	restoreRoomManagerFocusAfterLoad := func() {
		if !roomManagerFocusContentAfterLoad {
			return
		}
		roomManagerFocusContentAfterLoad = false
		focusRoomManagerContent()
	}
	setRoomManagerNotice := func(message string, failed bool) {
		message = strings.TrimSpace(message)
		if message == "" {
			roomManagerNotice.SetText("")
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
		roomManagerNotice.SetText("[" + color.String() + "]" + tview.Escape(message) + "[-]")
	}
	showRoomManagerTableView := func() {
		roomManagerLoading = false
		roomManagerInputVisible = false
		roomManagerRetryAction = nil
		retryWasFocused := app.GetFocus() == roomManagerRetryButton
		updateRoomManagerActionBar(roomManagerBaseActionMode)
		roomManagerContent.SwitchToPage("table")
		if retryWasFocused {
			app.SetFocus(roomManagerNavigation)
		}
	}
	showRoomManagerEmptyView := func(message string) {
		roomManagerLoading = (message == "正在加载……")
		roomManagerInputVisible = false
		emptyContent := fmt.Sprintf("[%s::b]%s[-:-:-]",
			tview.Styles.PrimaryTextColor.String(),
			tview.Escape(message),
		)
		roomManagerEmpty.SetText(emptyContent)
		roomManagerContent.SwitchToPage("empty")
		if app.GetFocus() == roomManagerTable {
			app.SetFocus(roomManagerNavigation)
		}
	}
	closeRoomManager := func() {
		roomManagerLoading = false
		roomManagerGeneration++
		roomManagerVisible = false
		roomManagerConfirmVisible = false
		roomManagerInputVisible = false
		roomManagerRetryAction = nil
		roomManagerFlowBack = nil
		roomManagerFlowSearchable = false
		roomManagerFocusContentAfterLoad = false
		pendingRoomManagerAction = nil
		setRoomManagerNotice("", false)
		pages.HidePage("room-manager-confirm")
		pages.RemovePage("room-manager-input")
		// 房间管理是独立工作区，不与弹幕页叠加绘制。切回主页面时也用
		// SwitchToPage 恢复唯一可见页，避免宽字符残留在下一帧。
		pages.SwitchToPage("main")
		deps.ReturnToChat()
	}
	roomManagerBackButton.SetSelectedFunc(closeRoomManager)
	roomManagerRetryButton.SetSelectedFunc(func() {
		if roomManagerRetryAction != nil {
			roomManagerFocusContentAfterLoad = true
			roomManagerRetryAction()
		}
	})
	roomManagerAddAdminButton.SetSelectedFunc(func() {
		if triggerAddRoomAdmin != nil {
			triggerAddRoomAdmin()
		}
	})
	roomManagerMuteUserButton.SetSelectedFunc(func() {
		if triggerMuteRoomUser != nil {
			triggerMuteRoomUser()
		}
	})
	roomManagerBlacklistUserButton.SetSelectedFunc(func() {
		if triggerBlacklistRoomUser != nil {
			triggerBlacklistRoomUser()
		}
	})
	roomManagerFlowBackButton.SetSelectedFunc(func() {
		if roomManagerFlowBack != nil {
			roomManagerFocusContentAfterLoad = true
			roomManagerFlowBack()
		}
	})
	roomManagerCloseSilentButton.SetSelectedFunc(func() {
		if triggerCloseRoomSilent != nil {
			triggerCloseRoomSilent()
		}
	})
	roomManagerSearchAgainButton.SetSelectedFunc(func() {
		if openRoomManagerUserSearch != nil {
			openRoomManagerUserSearch(roomManagerSearchOperation)
		}
	})
	roomManagerPrevButton.SetSelectedFunc(func() {
		if roomManagerPrevPage != nil {
			roomManagerFocusContentAfterLoad = true
			roomManagerPrevPage()
		}
	})
	roomManagerNextButton.SetSelectedFunc(func() {
		if roomManagerNextPage != nil {
			roomManagerFocusContentAfterLoad = true
			roomManagerNextPage()
		}
	})
	showRoomManagerLoading := func(_ string) uint64 {
		roomManagerGeneration++
		generation := roomManagerGeneration
		roomManagerRetryAction = nil
		roomManagerCanPrevPage = false
		roomManagerCanNextPage = false
		updateRoomManagerActionBar("default")
		roomManagerSection.SetText("[" + mutedColor.String() + "]正在加载……[-]")
		showRoomManagerEmptyView("正在加载……")
		return generation
	}
	showRoomManagerError := func(generation uint64, title string, err error, retry func()) {
		if !roomManagerVisible || generation != roomManagerGeneration {
			return
		}
		roomManagerLoading = false
		roomManagerInputVisible = false
		roomManagerRetryAction = retry
		roomManagerSection.SetText("[" + errorColor.String() + "]请求异常[-]")
		failSuffix := "失败"
		if strings.HasSuffix(title, "列表") || title == "查找用户" || title == "搜索用户" {
			failSuffix = "加载失败"
		}
		cardTitle := fmt.Sprintf("%s %s", title, failSuffix)
		if strings.HasSuffix(title, "失败") {
			cardTitle = title
		}
		setRoomManagerNotice(cardTitle+"："+compactDanmakuManagementError(err), true)
		errCard := fmt.Sprintf("[%s::b]%s[-:-:-]\n\n[%s]%s[-]",
			errorColor.String(),
			tview.Escape(cardTitle),
			tview.Styles.PrimaryTextColor.String(),
			compactDanmakuManagementError(err),
		)
		roomManagerErrorText.SetText(errCard)
		roomManagerContent.SwitchToPage("error")
		updateRoomManagerActionBar("error")
		roomManagerFocusContentAfterLoad = false
		app.SetFocus(roomManagerRetryButton)
	}
	runRoomManagerAction := func() {
		action := pendingRoomManagerAction
		label := pendingRoomManagerLabel
		pendingRoomManagerAction = nil
		roomManagerConfirmVisible = false
		pages.HidePage("room-manager-confirm")
		roomManagerFocusContentAfterLoad = true
		app.SetFocus(roomManagerNavigation)
		generation := showRoomManagerLoading(label)
		setRoomManagerNotice("正在"+label+"……", false)
		go func() {
			requestCtx, cancelRequest := context.WithTimeout(streamCtx, 10*time.Second)
			defer cancelRequest()
			err := action(requestCtx)
			queueUI(func() {
				if !roomManagerVisible || generation != roomManagerGeneration {
					return
				}
				if err != nil {
					setRoomManagerNotice(label+"失败："+err.Error(), true)
					showRoomManagerError(generation, label, err, roomManagerReload)
					return
				}
				setRoomManagerNotice(label+"成功。", false)
				if roomManagerReload != nil {
					roomManagerReload()
				} else {
					showRoomManagerRoot()
				}
			})
		}()
	}
	roomManagerConfirm.SetDoneFunc(func(buttonIndex int, _ string) {
		if buttonIndex == 1 && pendingRoomManagerAction != nil {
			runRoomManagerAction()
			return
		}
		roomManagerConfirmVisible = false
		pendingRoomManagerAction = nil
		pages.HidePage("room-manager-confirm")
		pages.SendToFront("room-manager")
		focusRoomManagerContent()
	})
	openRoomManagerConfirm := func(label, warning string, action func(context.Context) error) {
		pendingRoomManagerLabel = label
		pendingRoomManagerAction = action
		roomManagerConfirm.ClearButtons().SetText(warning).AddButtons([]string{"取消", "确认"})
		roomManagerConfirmVisible = true
		pages.ShowPage("room-manager-confirm")
		pages.SendToFront("room-manager-confirm")
		app.SetFocus(roomManagerConfirm)
	}
	var inputPreviousFocus tview.Primitive
	closeRoomManagerInput := func() {
		roomManagerInputVisible = false
		pages.RemovePage("room-manager-input")
		if inputPreviousFocus != nil {
			app.SetFocus(inputPreviousFocus)
		} else {
			focusRoomManagerContent()
		}
	}
	openRoomManagerInput := func(title, label, initial string, _ int, submitLabel string, validate func(string) error, submitted func(string)) {
		inputPreviousFocus = app.GetFocus()
		roomManagerInput = newInputDialog(app, title, label, initial, submitLabel, validate, func(value string) {
			closeRoomManagerInput()
			submitted(value)
		}, closeRoomManagerInput)
		roomManagerInputVisible = true
		pages.AddPage("room-manager-input", roomManagerInput.root, true, true)
		app.SetFocus(roomManagerInput.field)
	}
	returnToRoomManagerList := func() {
		roomManagerFlowBack = nil
		roomManagerFlowSearchable = false
		roomManagerFocusContentAfterLoad = true
		setRoomManagerNotice("", false)
		if roomManagerReload != nil {
			roomManagerReload()
		}
	}
	setRoomManagerFlowBack := func(label string, action func(), searchable bool) {
		roomManagerFlowBackButton.SetLabel(label)
		roomManagerFlowBack = action
		roomManagerFlowSearchable = searchable
		roomManagerCanPrevPage = false
		roomManagerCanNextPage = false
		updateRoomManagerActionBar("flow")
	}
	showRoomManagerAdminChoices = func(target api.RoomUserSearchResult, fromSearch bool) {
		showRoomManagerTableView()
		roomManagerTable.Clear()
		clear(roomManagerTableActions)
		currentLevel := target.AdminLevel
		currentLevelKnown := target.AdminLevelKnown
		if knownLevel, ok := roomManagerAdminLevels[target.UserID]; ok {
			currentLevel = knownLevel
			currentLevelKnown = true
		}
		roomManagerSection.SetText(fmt.Sprintf("[%s::b]管理房管[-:-:-] [%s]· %s[-]", tview.Styles.PrimaryTextColor.String(), mutedColor.String(), displayDanmakuManagedUser(target.Username, target.UserID)))
		setRoomManagerNotice("", false)
		roomManagerTable.SetCell(0, 0, roomManagerTableHeaderCell("房管身份", 18, 1))
		roomManagerTable.SetCell(0, 1, roomManagerTableHeaderCell("说明", 42, 2))
		roomManagerTable.SetCell(0, 2, roomManagerTableHeaderCell("操作", 8, 0).SetAlign(tview.AlignCenter))
		addLevel := func(row, level int, label, detail string) {
			roomManagerTable.SetCell(row, 0, roomManagerTableTextCell(label, 18, 1))
			roomManagerTable.SetCell(row, 1, roomManagerTableMutedCell(detail, 42, 2))
			if currentLevelKnown && currentLevel == level {
				roomManagerTable.SetCell(row, 2, roomManagerTableMutedCell("当前", 8, 0).SetAlign(tview.AlignCenter))
				return
			}
			actionLabel := "任命"
			if currentLevelKnown && currentLevel > 0 {
				actionLabel = "调整"
			}
			action := func() {
				openRoomManagerConfirm(actionLabel+"房管", "确定将 "+displayDanmakuManagedUser(target.Username, target.UserID)+" 设为"+label+"吗？", func(actionCtx context.Context) error {
					return client.AppointRoomAdmin(actionCtx, target.UserID, level, sessdata, biliJCT)
				})
			}
			roomManagerTableActions[row] = action
			roomManagerTable.SetCell(row, 2, roomManagerTableActionCell(actionLabel, accentActiveColor, action))
		}
		row := 1
		addLevel(row, 1, "普通房管", "授予普通房管身份")
		row++
		if roomManagerSeniorAdminEnabled || currentLevel == 2 {
			addLevel(row, 2, "高级房管", "授予高级房管身份")
			row++
		} else if !roomManagerSeniorAdminKnown {
			setRoomManagerNotice("未能确认高级房管功能，暂只提供普通房管。", false)
		}
		if (currentLevelKnown && currentLevel > 0) || !fromSearch {
			revokeRow := row
			action := func() {
				openRoomManagerConfirm("撤销房管", "确定撤销 "+displayDanmakuManagedUser(target.Username, target.UserID)+" 的房管权限吗？", func(actionCtx context.Context) error {
					return client.DismissRoomAdmin(actionCtx, target.UserID, sessdata, biliJCT)
				})
			}
			roomManagerTableActions[revokeRow] = action
			roomManagerTable.SetCell(revokeRow, 0, roomManagerTableTextCell("撤销房管", 18, 1))
			roomManagerTable.SetCell(revokeRow, 1, roomManagerTableMutedCell("移除该用户的直播间管理权限", 42, 2))
			roomManagerTable.SetCell(revokeRow, 2, roomManagerTableActionCell("撤销", errorColor, action))
		}
		if fromSearch {
			setRoomManagerFlowBack("返回搜索结果", renderRoomManagerSearchResults, false)
		} else {
			setRoomManagerFlowBack("返回房管列表", returnToRoomManagerList, false)
		}
		roomManagerTable.Select(1, 0).ScrollToBeginning()
	}
	showRoomManagerMuteChoices = func(target api.RoomUserSearchResult) {
		showRoomManagerTableView()
		roomManagerTable.Clear()
		clear(roomManagerTableActions)
		roomManagerSection.SetText(fmt.Sprintf("[%s::b]禁言用户[-:-:-] [%s]· %s[-]", tview.Styles.PrimaryTextColor.String(), mutedColor.String(), displayDanmakuManagedUser(target.Username, target.UserID)))
		setRoomManagerNotice("", false)
		roomManagerTable.SetCell(0, 0, roomManagerTableHeaderCell("禁言时长", 18, 1))
		roomManagerTable.SetCell(0, 1, roomManagerTableHeaderCell("说明", 42, 2))
		roomManagerTable.SetCell(0, 2, roomManagerTableHeaderCell("操作", 8, 0).SetAlign(tview.AlignCenter))
		durations := []struct {
			label    string
			detail   string
			duration api.RoomUserMuteDuration
		}{
			{"仅本场直播", "本次直播结束后自动解除", api.RoomMuteThisLive},
			{"2 小时", "禁言 2 小时", api.RoomMuteTwoHours},
			{"4 小时", "禁言 4 小时", api.RoomMuteFourHours},
			{"24 小时", "禁言 1 天", api.RoomMuteOneDay},
			{"7 天", "禁言 7 天", api.RoomMuteSevenDays},
			{"永久", "直到手动解除禁言", api.RoomMutePermanent},
		}
		for index, item := range durations {
			item := item
			row := index + 1
			action := func() {
				openRoomManagerConfirm("禁言用户", "确定禁言 "+displayDanmakuManagedUser(target.Username, target.UserID)+"（"+item.label+"）吗？", func(actionCtx context.Context) error {
					return client.MuteRoomUser(actionCtx, roomID, target.UserID, "", item.duration, sessdata, biliJCT)
				})
			}
			roomManagerTableActions[row] = action
			roomManagerTable.SetCell(row, 0, roomManagerTableTextCell(item.label, 18, 1))
			roomManagerTable.SetCell(row, 1, roomManagerTableMutedCell(item.detail, 42, 2))
			roomManagerTable.SetCell(row, 2, roomManagerTableActionCell("选择", accentActiveColor, action))
		}
		setRoomManagerFlowBack("返回搜索结果", renderRoomManagerSearchResults, false)
		roomManagerTable.Select(1, 0).ScrollToBeginning()
	}
	renderRoomManagerSearchResults = func() {
		showRoomManagerTableView()
		roomManagerTable.Clear()
		clear(roomManagerTableActions)
		roomManagerSection.SetText(fmt.Sprintf("[%s::b]搜索结果[-:-:-] [%s]· %s[-]", tview.Styles.PrimaryTextColor.String(), mutedColor.String(), tview.Escape(roomManagerSearchQuery)))
		setRoomManagerNotice("请核对用户名和 UID 后再操作。", false)
		roomManagerTable.SetCell(0, 0, roomManagerTableHeaderCell("用户", 32, 2))
		roomManagerTable.SetCell(0, 1, roomManagerTableHeaderCell("UID", 20, 1))
		roomManagerTable.SetCell(0, 2, roomManagerTableHeaderCell("状态", 12, 0))
		roomManagerTable.SetCell(0, 3, roomManagerTableHeaderCell("操作", 8, 0).SetAlign(tview.AlignCenter))
		setRoomManagerFlowBack("返回列表", returnToRoomManagerList, true)
		if len(roomManagerSearchResults) == 0 {
			showRoomManagerEmptyView("未找到匹配用户")
			updateRoomManagerActionBar("flow")
			restoreRoomManagerFocusAfterLoad()
			return
		}
		for index, result := range roomManagerSearchResults {
			result := result
			row := index + 1
			currentLevel := result.AdminLevel
			currentLevelKnown := result.AdminLevelKnown
			if knownLevel, ok := roomManagerAdminLevels[result.UserID]; ok {
				currentLevel = knownLevel
				currentLevelKnown = true
				result.AdminLevel = knownLevel
				result.AdminLevelKnown = true
			}
			statusText := "可操作"
			actionLabel := "选择"
			canOperate := true
			switch {
			case result.UserID == strings.TrimSpace(managementCapabilities.UserID):
				statusText, canOperate = "当前账号", false
			case result.UserID == strings.TrimSpace(managementCapabilities.AnchorID):
				statusText, canOperate = "主播", false
			case roomManagerSearchOperation == roomManagerUserMute && !managementCapabilities.CanMuteUser(currentLevel, currentLevelKnown):
				if !currentLevelKnown && !managementCapabilities.IsAnchor {
					statusText = "身份未知"
				} else {
					statusText = "无权禁言"
				}
				canOperate = false
			case roomManagerSearchOperation == roomManagerUserBlacklist && strings.TrimSpace(managementCapabilities.AnchorID) == "":
				statusText, canOperate = "主播未知", false
			case roomManagerSearchOperation == roomManagerUserBlacklist && !managementCapabilities.CanBlacklistUser(currentLevel, currentLevelKnown):
				if !currentLevelKnown && !managementCapabilities.IsAnchor {
					statusText = "身份未知"
				} else {
					statusText = "无权拉黑"
				}
				canOperate = false
			case roomManagerSearchOperation == roomManagerUserAddAdmin && currentLevelKnown && currentLevel == 1:
				statusText, actionLabel = "普通房管", "管理"
			case roomManagerSearchOperation == roomManagerUserAddAdmin && currentLevelKnown && currentLevel == 2:
				statusText, actionLabel = "高级房管", "管理"
			}
			var action func()
			if canOperate {
				action = func() {
					switch roomManagerSearchOperation {
					case roomManagerUserAddAdmin:
						showRoomManagerAdminChoices(result, true)
					case roomManagerUserMute:
						showRoomManagerMuteChoices(result)
					case roomManagerUserBlacklist:
						openRoomManagerConfirm("添加黑名单用户", "确定将 "+displayDanmakuManagedUser(result.Username, result.UserID)+" 添加到直播间黑名单吗？", func(actionCtx context.Context) error {
							return client.BlacklistRoomUser(actionCtx, managementCapabilities.AnchorID, result.UserID, sessdata, biliJCT)
						})
					}
				}
				roomManagerTableActions[row] = action
			}
			roomManagerTable.SetCell(row, 0, roomManagerTableTextCell(fallbackDanmakuManagementText(result.Username, "用户名未知"), 32, 2))
			roomManagerTable.SetCell(row, 1, roomManagerTableMutedCell(tview.Escape(result.UserID), 20, 1))
			roomManagerTable.SetCell(row, 2, roomManagerTableMutedCell(statusText, 12, 0))
			if canOperate {
				roomManagerTable.SetCell(row, 3, roomManagerTableActionCell(actionLabel, accentActiveColor, action))
			} else {
				roomManagerTable.SetCell(row, 3, roomManagerTableMutedCell("不可操作", 8, 0).SetAlign(tview.AlignCenter))
			}
		}
		roomManagerTable.Select(1, 0).ScrollToBeginning()
		restoreRoomManagerFocusAfterLoad()
	}
	searchRoomManagerUsers = func(operation roomManagerUserOperation, query string) {
		query = strings.TrimSpace(query)
		roomManagerSearchOperation = operation
		roomManagerSearchQuery = query
		roomManagerFocusContentAfterLoad = true
		generation := showRoomManagerLoading("查找用户")
		roomManagerSection.SetText(fmt.Sprintf("[%s]正在查找 %s……[-]", mutedColor.String(), tview.Escape(query)))
		setRoomManagerFlowBack("返回列表", returnToRoomManagerList, false)
		go func() {
			requestCtx, cancelRequest := context.WithTimeout(streamCtx, 10*time.Second)
			defer cancelRequest()
			results, err := client.SearchRoomUsers(requestCtx, query, sessdata, biliJCT)
			queueUI(func() {
				if !roomManagerVisible || generation != roomManagerGeneration {
					return
				}
				if err != nil {
					roomManagerSection.SetText(fmt.Sprintf("[%s]查找失败[-]", errorColor.String()))
					setRoomManagerNotice(compactDanmakuManagementError(err), true)
					showRoomManagerEmptyView("未能完成用户查找")
					setRoomManagerFlowBack("返回列表", returnToRoomManagerList, true)
					restoreRoomManagerFocusAfterLoad()
					return
				}
				roomManagerSearchResults = results
				renderRoomManagerSearchResults()
			})
		}()
	}
	openRoomManagerUserSearch = func(operation roomManagerUserOperation) {
		title := "查找用户"
		switch operation {
		case roomManagerUserAddAdmin:
			title = "添加房管"
		case roomManagerUserMute:
			title = "添加禁言用户"
		case roomManagerUserBlacklist:
			title = "添加黑名单用户"
		}
		openRoomManagerInput(title, "UID 或用户名 ", "", 50, "查找", func(value string) error {
			if strings.TrimSpace(value) == "" {
				return fmt.Errorf("请输入 UID 或用户名。")
			}
			return nil
		}, func(value string) {
			searchRoomManagerUsers(operation, value)
		})
	}
	triggerAddRoomAdmin = func() { openRoomManagerUserSearch(roomManagerUserAddAdmin) }
	triggerMuteRoomUser = func() { openRoomManagerUserSearch(roomManagerUserMute) }
	triggerBlacklistRoomUser = func() { openRoomManagerUserSearch(roomManagerUserBlacklist) }
	roomManagerPrevPage = func() {
		if !roomManagerCanPrevPage {
			return
		}
		if len(roomManagerTabsAvailable) == 0 || roomManagerTabIndex >= len(roomManagerTabsAvailable) {
			return
		}
		switch roomManagerTabsAvailable[roomManagerTabIndex].label {
		case "房管":
			if roomAdminPage > 1 {
				roomManagerFocusContentAfterLoad = true
				roomAdminPage--
				loadRoomAdmins()
			}
		case "禁言":
			if mutedUserPage > 1 {
				roomManagerFocusContentAfterLoad = true
				mutedUserPage--
				loadMutedUsers()
			}
		case "黑名单":
			if blacklistPage > 1 {
				roomManagerFocusContentAfterLoad = true
				blacklistPage--
				loadBlacklistedUsers()
			}
		}
	}
	roomManagerNextPage = func() {
		if !roomManagerCanNextPage {
			return
		}
		if len(roomManagerTabsAvailable) == 0 || roomManagerTabIndex >= len(roomManagerTabsAvailable) {
			return
		}
		switch roomManagerTabsAvailable[roomManagerTabIndex].label {
		case "房管":
			if roomAdminPage < roomAdminTotalPages {
				roomManagerFocusContentAfterLoad = true
				roomAdminPage++
				loadRoomAdmins()
			}
		case "禁言":
			if mutedUserPage < mutedTotalPages {
				roomManagerFocusContentAfterLoad = true
				mutedUserPage++
				loadMutedUsers()
			}
		case "黑名单":
			if roomManagerCanNextPage {
				roomManagerFocusContentAfterLoad = true
				blacklistPage++
				loadBlacklistedUsers()
			}
		}
	}
	showRoomManagerRoot = func() {
		roomManagerTabsAvailable = roomManagerTabsAvailable[:0]
		if managementCapabilities.IsAnchor {
			roomManagerTabsAvailable = append(roomManagerTabsAvailable, roomManagerTab{label: "房管", load: func() {
				roomAdminPage = 1
				loadRoomAdmins()
			}})
		}
		if managementCapabilities.CanMute(0) {
			roomManagerTabsAvailable = append(roomManagerTabsAvailable, roomManagerTab{label: "禁言", load: func() {
				mutedUserPage = 1
				loadMutedUsers()
			}})
		}
		if managementCapabilities.CanBlacklist(0) && managementCapabilities.AnchorID != "" {
			roomManagerTabsAvailable = append(roomManagerTabsAvailable, roomManagerTab{label: "黑名单", load: func() {
				blacklistPage = 1
				loadBlacklistedUsers()
			}})
		}
		if managementCapabilities.IsAnchor {
			roomManagerTabsAvailable = append(roomManagerTabsAvailable, roomManagerTab{label: "全局禁言", load: loadRoomSilent})
		}
		if roomManagerTabIndex >= len(roomManagerTabsAvailable) {
			roomManagerTabIndex = 0
		}
		roomManagerNavigationSyncing = true
		roomManagerNavigation.Clear()
		for _, tab := range roomManagerTabsAvailable {
			roomManagerNavigation.AddItem(tab.label, "", 0, func() {
				focusRoomManagerContent()
			})
		}
		roomManagerNavigation.SetCurrentItem(roomManagerTabIndex)
		roomManagerNavigationSyncing = false
		selectRoomManagerTab(roomManagerTabIndex)
	}
	selectRoomManagerTab = func(index int) {
		if len(roomManagerTabsAvailable) == 0 {
			closeRoomManager()
			return
		}
		index = (index%len(roomManagerTabsAvailable) + len(roomManagerTabsAvailable)) % len(roomManagerTabsAvailable)
		roomManagerTabIndex = index
		roomManagerNavigationSyncing = true
		roomManagerNavigation.SetCurrentItem(index)
		roomManagerNavigationSyncing = false
		tab := roomManagerTabsAvailable[index]
		switch tab.label {
		case "房管":
			roomManagerBaseActionMode = "admin"
		case "禁言":
			roomManagerBaseActionMode = "mute"
		case "黑名单":
			roomManagerBaseActionMode = "blacklist"
		default:
			roomManagerBaseActionMode = "default"
		}
		roomManagerCanPrevPage = false
		roomManagerCanNextPage = false
		roomManagerFlowSearchable = false
		roomManagerFocusContentAfterLoad = false
		showRoomManagerTableView()
		setRoomManagerNotice("", false)
		roomManagerReload = tab.load
		tab.load()
	}
	roomManagerNavigation.SetChangedFunc(func(index int, _, _ string, _ rune) {
		if roomManagerNavigationSyncing || !roomManagerVisible || roomManagerConfirmVisible || roomManagerInputVisible {
			return
		}
		if index >= 0 && index < len(roomManagerTabsAvailable) && index != roomManagerTabIndex {
			selectRoomManagerTab(index)
		}
	})
	loadRoomAdmins = func() {
		generation := showRoomManagerLoading("房管")
		roomManagerReload = loadRoomAdmins
		requestedPage := roomAdminPage
		if !roomManagerSeniorAdminKnown && !roomManagerSeniorAdminLoading && strings.TrimSpace(managementCapabilities.AnchorID) != "" {
			roomManagerSeniorAdminLoading = true
			go func(anchorID string) {
				requestCtx, cancelRequest := context.WithTimeout(streamCtx, 6*time.Second)
				defer cancelRequest()
				status, err := client.GetRoomAdminSeniorStatus(requestCtx, anchorID, sessdata, biliJCT)
				queueUI(func() {
					roomManagerSeniorAdminLoading = false
					if err == nil {
						roomManagerSeniorAdminKnown = true
						roomManagerSeniorAdminEnabled = status > 0
					}
				})
			}(managementCapabilities.AnchorID)
		}
		go func() {
			requestCtx, cancelRequest := context.WithTimeout(streamCtx, 10*time.Second)
			defer cancelRequest()
			result, err := client.GetRoomAdmins(requestCtx, requestedPage, sessdata, biliJCT)
			queueUI(func() {
				if err != nil {
					showRoomManagerError(generation, "房管", err, loadRoomAdmins)
					return
				}
				if !roomManagerVisible || generation != roomManagerGeneration {
					return
				}
				showRoomManagerTableView()
				roomManagerTable.Clear()
				clear(roomManagerTableActions)
				clear(roomManagerAdminLevels)
				roomManagerTable.SetCell(0, 0, roomManagerTableHeaderCell("用户", 32, 2))
				roomManagerTable.SetCell(0, 1, roomManagerTableHeaderCell("身份", 10, 0))
				roomManagerTable.SetCell(0, 2, roomManagerTableHeaderCell("任命时间", 20, 1))
				roomManagerTable.SetCell(0, 3, roomManagerTableHeaderCell("操作", 8, 0).SetAlign(tview.AlignCenter))
				roomAdminTotalPages = result.TotalPages
				if roomAdminTotalPages < 1 {
					roomAdminTotalPages = 1
				}
				adminCount := fmt.Sprintf("本页 %d 人", len(result.Items))
				if result.MaxCount > 0 {
					adminCount += fmt.Sprintf(" · 容量上限 %d 人", result.MaxCount)
				}
				adminCount += fmt.Sprintf(" · 第 %d/%d 页", roomAdminPage, roomAdminTotalPages)
				roomManagerSection.SetText(fmt.Sprintf("[%s]%s[-]", mutedColor.String(), adminCount))
				roomManagerCanPrevPage = roomAdminPage > 1
				roomManagerCanNextPage = roomAdminPage < roomAdminTotalPages
				updateRoomManagerActionBar(roomManagerBaseActionMode)
				if len(result.Items) == 0 && roomAdminPage == 1 {
					showRoomManagerEmptyView("暂无房管")
					restoreRoomManagerFocusAfterLoad()
					return
				}
				for index, admin := range result.Items {
					admin := admin
					row := index + 1
					if admin.LevelKnown {
						roomManagerAdminLevels[admin.UserID] = admin.Level
					}
					action := func() {
						showRoomManagerAdminChoices(api.RoomUserSearchResult{UserID: admin.UserID, Username: admin.Username, AdminLevel: admin.Level, AdminLevelKnown: admin.LevelKnown}, false)
					}
					level := "身份未知"
					if admin.LevelKnown && admin.Level == 1 {
						level = "普通房管"
					} else if admin.LevelKnown && admin.Level == 2 {
						level = "高级房管"
					}
					roomManagerTableActions[row] = action
					roomManagerTable.SetCell(row, 0, roomManagerTableTextCell(formatRoomManagerTableUser(admin.Username, admin.UserID), 32, 2))
					roomManagerTable.SetCell(row, 1, roomManagerTableTextCell(level, 10, 0))
					roomManagerTable.SetCell(row, 2, roomManagerTableMutedCell(fallbackDanmakuManagementText(admin.AppointedAt, "时间未知"), 20, 1))
					roomManagerTable.SetCell(row, 3, roomManagerTableActionCell("管理", accentActiveColor, action))
				}
				if len(result.Items) == 0 {
					showRoomManagerEmptyView("本页没有记录")
					restoreRoomManagerFocusAfterLoad()
					return
				}
				roomManagerTable.Select(1, 0).ScrollToBeginning()
				restoreRoomManagerFocusAfterLoad()
			})
		}()
	}
	loadMutedUsers = func() {
		generation := showRoomManagerLoading("禁言名单")
		roomManagerReload = loadMutedUsers
		// 页码归 UI 线程所有。请求启动前取快照，避免切换栏目时并发读取。
		requestedPage := mutedUserPage
		go func() {
			requestCtx, cancelRequest := context.WithTimeout(streamCtx, 10*time.Second)
			defer cancelRequest()
			result, err := client.GetMutedRoomUsers(requestCtx, roomID, requestedPage, sessdata, biliJCT)
			queueUI(func() {
				if err != nil {
					showRoomManagerError(generation, "禁言名单", err, loadMutedUsers)
					return
				}
				if !roomManagerVisible || generation != roomManagerGeneration {
					return
				}
				showRoomManagerTableView()
				roomManagerTable.Clear()
				clear(roomManagerTableActions)
				roomManagerTable.SetCell(0, 0, roomManagerTableHeaderCell("用户", 30, 2))
				roomManagerTable.SetCell(0, 1, roomManagerTableHeaderCell("到期时间", 20, 1))
				roomManagerTable.SetCell(0, 2, roomManagerTableHeaderCell("操作者", 14, 1))
				roomManagerTable.SetCell(0, 3, roomManagerTableHeaderCell("操作", 8, 0).SetAlign(tview.AlignCenter))
				mutedTotalPages = result.TotalPages
				if mutedTotalPages < 1 {
					mutedTotalPages = 1
				}
				sectionText := fmt.Sprintf("第 %d/%d 页", mutedUserPage, mutedTotalPages)
				if result.Total > 0 {
					sectionText = fmt.Sprintf("共 %d 人 · %s", result.Total, sectionText)
				}
				roomManagerSection.SetText(fmt.Sprintf("[%s]%s[-]", mutedColor.String(), sectionText))
				roomManagerCanPrevPage = mutedUserPage > 1
				roomManagerCanNextPage = mutedUserPage < mutedTotalPages
				updateRoomManagerActionBar(roomManagerBaseActionMode)
				if len(result.Items) == 0 && mutedUserPage == 1 {
					showRoomManagerEmptyView("暂无被禁言用户")
					restoreRoomManagerFocusAfterLoad()
					return
				}
				for index, item := range result.Items {
					item := item
					row := index + 1
					targetLevel, targetLevelKnown := roomManagerAdminLevels[item.UserID]
					canMute := managementCapabilities.CanMuteUser(targetLevel, targetLevelKnown)
					var action func()
					if canMute {
						action = func() {
							openRoomManagerConfirm("解除禁言", "确定解除 "+displayDanmakuManagedUser(item.Username, item.UserID)+" 的禁言吗？", func(actionCtx context.Context) error {
								return client.UnmuteRoomUser(actionCtx, roomID, item.UserID, sessdata, biliJCT)
							})
						}
						roomManagerTableActions[row] = action
					}
					operator := "执行人未知"
					if item.OperatorIsAnchor {
						operator = "主播"
					} else if strings.TrimSpace(item.OperatorName) != "" {
						operator = item.OperatorName
					}
					roomManagerTable.SetCell(row, 0, roomManagerTableTextCell(formatRoomManagerTableUser(item.Username, item.UserID), 30, 2))
					roomManagerTable.SetCell(row, 1, roomManagerTableMutedCell(fallbackDanmakuManagementText(item.ExpiresAt, "上游未提供"), 20, 1))
					roomManagerTable.SetCell(row, 2, roomManagerTableMutedCell(tview.Escape(operator), 14, 1))
					if canMute {
						roomManagerTable.SetCell(row, 3, roomManagerTableActionCell("解除", errorColor, action))
					} else {
						roomManagerTable.SetCell(row, 3, roomManagerTableMutedCell("不可操作", 8, 0).SetAlign(tview.AlignCenter))
					}
				}
				if len(result.Items) == 0 {
					showRoomManagerEmptyView("本页没有记录")
					restoreRoomManagerFocusAfterLoad()
					return
				}
				roomManagerTable.Select(1, 0).ScrollToBeginning()
				restoreRoomManagerFocusAfterLoad()
			})
		}()
	}
	loadBlacklistedUsers = func() {
		generation := showRoomManagerLoading("直播间黑名单")
		roomManagerReload = loadBlacklistedUsers
		requestedPage, anchorID := blacklistPage, managementCapabilities.AnchorID
		go func() {
			requestCtx, cancelRequest := context.WithTimeout(streamCtx, 10*time.Second)
			defer cancelRequest()
			result, err := client.GetRoomBlacklist(requestCtx, anchorID, requestedPage, 50, sessdata, biliJCT)
			queueUI(func() {
				if err != nil {
					showRoomManagerError(generation, "直播间黑名单", err, loadBlacklistedUsers)
					return
				}
				if !roomManagerVisible || generation != roomManagerGeneration {
					return
				}
				showRoomManagerTableView()
				roomManagerTable.Clear()
				clear(roomManagerTableActions)
				roomManagerTable.SetCell(0, 0, roomManagerTableHeaderCell("用户", 34, 2))
				roomManagerTable.SetCell(0, 1, roomManagerTableHeaderCell("加入时间", 22, 1))
				roomManagerTable.SetCell(0, 2, roomManagerTableHeaderCell("操作", 8, 0).SetAlign(tview.AlignCenter))
				blacklistTotalPages = result.TotalPages
				if blacklistTotalPages < 1 && result.Total > 0 {
					blacklistTotalPages = (result.Total + 49) / 50
				}
				if blacklistTotalPages < 1 {
					blacklistTotalPages = 1
				}
				sectionText := fmt.Sprintf("第 %d/%d 页", blacklistPage, blacklistTotalPages)
				if result.Total > 0 {
					sectionText = fmt.Sprintf("共 %d 人 · %s", result.Total, sectionText)
				}
				roomManagerSection.SetText(fmt.Sprintf("[%s]%s[-]", mutedColor.String(), sectionText))
				roomManagerCanPrevPage = blacklistPage > 1
				roomManagerCanNextPage = len(result.Items) == 50 || result.Total > blacklistPage*50 || result.TotalPages > blacklistPage
				updateRoomManagerActionBar(roomManagerBaseActionMode)
				if len(result.Items) == 0 && blacklistPage == 1 {
					showRoomManagerEmptyView("黑名单为空")
					restoreRoomManagerFocusAfterLoad()
					return
				}
				for index, item := range result.Items {
					item := item
					row := index + 1
					action := func() {
						openRoomManagerConfirm("移出黑名单", "确定将 "+displayDanmakuManagedUser(item.Username, item.UserID)+" 移出直播间黑名单吗？", func(actionCtx context.Context) error {
							return client.UnblacklistRoomUser(actionCtx, anchorID, item.UserID, sessdata, biliJCT)
						})
					}
					roomManagerTableActions[row] = action
					roomManagerTable.SetCell(row, 0, roomManagerTableTextCell(formatRoomManagerTableUser(item.Username, item.UserID), 34, 2))
					roomManagerTable.SetCell(row, 1, roomManagerTableMutedCell(fallbackDanmakuManagementText(item.CreatedAt, "时间未知"), 22, 1))
					roomManagerTable.SetCell(row, 2, roomManagerTableActionCell("移出", errorColor, action))
				}
				if len(result.Items) == 0 {
					showRoomManagerEmptyView("本页没有记录")
					restoreRoomManagerFocusAfterLoad()
					return
				}
				roomManagerTable.Select(1, 0).ScrollToBeginning()
				restoreRoomManagerFocusAfterLoad()
			})
		}()
	}
	loadRoomSilent = func() {
		triggerCloseRoomSilent = nil
		roomManagerBaseActionMode = "default"
		generation := showRoomManagerLoading("全局禁言")
		roomManagerReload = loadRoomSilent
		go func() {
			requestCtx, cancelRequest := context.WithTimeout(streamCtx, 10*time.Second)
			defer cancelRequest()
			state, err := client.GetRoomSilentState(requestCtx, roomID, sessdata, biliJCT)
			queueUI(func() {
				if err != nil {
					showRoomManagerError(generation, "全局禁言", err, loadRoomSilent)
					return
				}
				if !roomManagerVisible || generation != roomManagerGeneration {
					return
				}
				showRoomManagerTableView()
				roomManagerTable.Clear()
				clear(roomManagerTableActions)
				roomManagerCanPrevPage = false
				roomManagerCanNextPage = false
				if state.Enabled {
					roomManagerSection.SetText(fmt.Sprintf("[%s::b]已开启[-:-:-]", accentActiveColor.String()))
					triggerCloseRoomSilent = func() {
						openRoomManagerConfirm("关闭全局禁言", "确定关闭直播间全局禁言吗？", func(actionCtx context.Context) error {
							return client.SetRoomSilentState(actionCtx, roomID, api.RoomSilentOff, 1, 0, sessdata, biliJCT)
						})
					}
					roomManagerBaseActionMode = "silent-active"
					showRoomManagerEmptyView(formatRoomSilentState(state))
					updateRoomManagerActionBar(roomManagerBaseActionMode)
					restoreRoomManagerFocusAfterLoad()
					return
				} else {
					roomManagerSection.SetText(fmt.Sprintf("[%s]当前未开启[-]", mutedColor.String()))
					roomManagerTable.SetCell(0, 0, roomManagerTableHeaderCell("规则", 22, 1))
					roomManagerTable.SetCell(0, 1, roomManagerTableHeaderCell("影响范围", 46, 2))
					roomManagerTable.SetCell(0, 2, roomManagerTableHeaderCell("操作", 8, 0).SetAlign(tview.AlignCenter))
					addSilentChoice := func(row int, label, detail, actionLabel, audience string, level int) {
						action := func() {
							openRoomManagerConfirm("开启全局禁言", "确定开启“"+label+"”全局禁言吗？", func(actionCtx context.Context) error {
								return client.SetRoomSilentState(actionCtx, roomID, audience, level, 0, sessdata, biliJCT)
							})
						}
						roomManagerTableActions[row] = action
						roomManagerTable.SetCell(row, 0, roomManagerTableTextCell(label, 22, 1))
						roomManagerTable.SetCell(row, 1, roomManagerTableMutedCell(detail, 46, 2))
						roomManagerTable.SetCell(row, 2, roomManagerTableActionCell(actionLabel, accentActiveColor, action))
					}
					addSilentChoice(1, "全员禁言", "除主播和房管外，所有观众均不可发言", "开启", api.RoomSilentAll, 1)
					addSilentChoice(2, "仅粉丝发言", "未关注本直播间的观众不可发言", "开启", api.RoomSilentNonFans, 1)
					wealthAction := func() {
						openRoomManagerInput("荣耀等级禁言", "等级 ", "1", 0, "保存", func(value string) error {
							_, err := parseDanmakuManagementLevel(value)
							return err
						}, func(value string) {
							level, _ := parseDanmakuManagementLevel(value)
							openRoomManagerConfirm("开启全局禁言", fmt.Sprintf("确定禁止荣耀等级低于 %d 的用户发言吗？", level), func(actionCtx context.Context) error {
								return client.SetRoomSilentState(actionCtx, roomID, api.RoomSilentWealth, level, 0, sessdata, biliJCT)
							})
						})
					}
					roomManagerTableActions[3] = wealthAction
					roomManagerTable.SetCell(3, 0, roomManagerTableTextCell("荣耀等级限制", 22, 1))
					roomManagerTable.SetCell(3, 1, roomManagerTableMutedCell("低于所填荣耀等级的用户不可发言", 46, 2))
					roomManagerTable.SetCell(3, 2, roomManagerTableActionCell("配置", accentActiveColor, wealthAction))
					medalAction := func() {
						openRoomManagerInput("粉丝勋章禁言", "等级 ", "1", 0, "保存", func(value string) error {
							_, err := parseDanmakuManagementLevel(value)
							return err
						}, func(value string) {
							level, _ := parseDanmakuManagementLevel(value)
							openRoomManagerConfirm("开启全局禁言", fmt.Sprintf("确定禁止粉丝牌等级低于 %d 的用户发言吗？", level), func(actionCtx context.Context) error {
								return client.SetRoomSilentState(actionCtx, roomID, api.RoomSilentMedal, level, 0, sessdata, biliJCT)
							})
						})
					}
					roomManagerTableActions[4] = medalAction
					roomManagerTable.SetCell(4, 0, roomManagerTableTextCell("粉丝勋章限制", 22, 1))
					roomManagerTable.SetCell(4, 1, roomManagerTableMutedCell("无勋章或低于所填等级的用户不可发言", 46, 2))
					roomManagerTable.SetCell(4, 2, roomManagerTableActionCell("配置", accentActiveColor, medalAction))
					addSilentChoice(5, "除房管以外的观众", "仅主播和房管可以发言", "开启", api.RoomSilentNonMember, 1)
				}
				roomManagerBaseActionMode = "default"
				updateRoomManagerActionBar(roomManagerBaseActionMode)
				roomManagerTable.Select(1, 0).ScrollToBeginning()
				restoreRoomManagerFocusAfterLoad()
			})
		}()
	}
	openRoomManager := func() {
		sendStatus.SetText("正在获取房间管理权限，请稍候……")
		refreshManagementCapabilities(func(err error) {
			managementCapabilities = deps.Capabilities()
			if err != nil {
				sendStatus.SetText("获取房间管理权限失败：" + tview.Escape(err.Error()))
				return
			}
			if !managementCapabilities.IsAnchor && !managementCapabilities.IsAdmin {
				sendStatus.SetText("当前账号不是本直播间的主播或房管。")
				return
			}
			sendStatus.SetText("")
			roomManagerVisible = true
			showRoomManagerRoot()
			// 使用互斥页面切换而非透明叠加。这样弹幕页不会参与本帧绘制，
			// 全角字符的延伸单元格也不可能透进房间管理工作区。
			pages.SwitchToPage("room-manager")
			app.SetFocus(roomManagerNavigation)
		})
	}

	pages.AddPage("room-manager", roomManagerPage, true, false)
	pages.AddPage("room-manager-confirm", roomManagerConfirm, true, false)
	capture := func(event *tcell.EventKey) *tcell.EventKey {
		if roomManagerConfirmVisible {
			switch event.Key() {
			case tcell.KeyEscape:
				roomManagerConfirmVisible = false
				pendingRoomManagerAction = nil
				pages.HidePage("room-manager-confirm")
				pages.SendToFront("room-manager")
				focusRoomManagerContent()
				return nil
			case tcell.KeyCtrlC:
				deps.Quit()
				return nil
			default:
				return event
			}
		}
		if roomManagerInputVisible {
			if event.Key() == tcell.KeyCtrlC {
				deps.Quit()
				return nil
			}
			return roomManagerInput.capture(event)
		}
		if roomManagerVisible {
			switch {
			case event.Key() == tcell.KeyEscape:
				closeRoomManager()
				return nil
			case event.Key() == tcell.KeyLeft:
				if isActionBarFocused() {
					idx := focusedButtonIndex()
					if idx > 0 {
						app.SetFocus(roomManagerCurrentButtons[idx-1])
						return nil
					}
				}
				if !roomManagerNavigation.HasFocus() {
					app.SetFocus(roomManagerNavigation)
				}
				return nil
			case event.Key() == tcell.KeyRight:
				if roomManagerNavigation.HasFocus() {
					focusRoomManagerContent()
					return nil
				}
				if isActionBarFocused() {
					idx := focusedButtonIndex()
					if idx >= 0 && idx < len(roomManagerCurrentButtons)-1 {
						app.SetFocus(roomManagerCurrentButtons[idx+1])
					}
				}
				return nil
			case event.Key() == tcell.KeyUp:
				if isActionBarFocused() {
					idx := focusedButtonIndex()
					if idx >= buttonGridColumns {
						app.SetFocus(roomManagerCurrentButtons[idx-buttonGridColumns])
						return nil
					}
					focusRoomManagerContent()
					return nil
				}
				return event
			case event.Key() == tcell.KeyDown:
				if isActionBarFocused() {
					idx := focusedButtonIndex()
					if idx >= 0 && idx+buttonGridColumns < len(roomManagerCurrentButtons) {
						app.SetFocus(roomManagerCurrentButtons[idx+buttonGridColumns])
					}
					return nil
				}
				return event
			case event.Key() == tcell.KeyTab:
				if roomManagerNavigation.HasFocus() {
					focusRoomManagerContent()
					return nil
				}
				if isActionBarFocused() {
					idx := focusedButtonIndex()
					if idx >= 0 && idx < len(roomManagerCurrentButtons)-1 {
						app.SetFocus(roomManagerCurrentButtons[idx+1])
					} else {
						app.SetFocus(roomManagerNavigation)
					}
					return nil
				}
				// 内容列表之后进入操作栏，最后一个按钮再回到左侧栏目。
				if len(roomManagerCurrentButtons) > 0 {
					app.SetFocus(roomManagerCurrentButtons[0])
					return nil
				}
				app.SetFocus(roomManagerNavigation)
				return nil
			case event.Key() == tcell.KeyBacktab:
				if roomManagerNavigation.HasFocus() {
					if len(roomManagerCurrentButtons) > 0 {
						app.SetFocus(roomManagerCurrentButtons[len(roomManagerCurrentButtons)-1])
					}
					return nil
				}
				if isActionBarFocused() {
					idx := focusedButtonIndex()
					if idx > 0 {
						app.SetFocus(roomManagerCurrentButtons[idx-1])
					} else {
						frontPage, _ := roomManagerContent.GetFrontPage()
						if frontPage == "table" && roomManagerTable.GetRowCount() > 1 {
							app.SetFocus(roomManagerTable)
						} else {
							app.SetFocus(roomManagerNavigation)
						}
					}
					return nil
				}
				app.SetFocus(roomManagerNavigation)
				return nil
			case event.Key() == tcell.KeyRune && event.Rune() == '[':
				if roomManagerPrevPage != nil {
					roomManagerPrevPage()
					return nil
				}
			case event.Key() == tcell.KeyRune && event.Rune() == ']':
				if roomManagerNextPage != nil {
					roomManagerNextPage()
					return nil
				}
			case event.Key() == tcell.KeyPgUp:
				if roomManagerPrevPage != nil {
					roomManagerPrevPage()
					return nil
				}
			case event.Key() == tcell.KeyPgDn:
				if roomManagerNextPage != nil {
					roomManagerNextPage()
					return nil
				}
			case event.Key() == tcell.KeyCtrlC:
				deps.Quit()
				return nil
			default:
				return event
			}
		}
		return event
	}
	return &roomManagerWorkspace{open: openRoomManager, capture: capture}
}
