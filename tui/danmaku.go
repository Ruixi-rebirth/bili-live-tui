package tui

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"bili-live-tui/internal/api"
	streamruntime "bili-live-tui/internal/stream"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

func RunDanmaku(
	ctx context.Context,
	session *LiveDanmakuSession,
	client *api.Client,
	roomID, sessdata, biliJCT string,
	healthLoader func() streamruntime.Health,
	overviewOpts ...DanmakuOverviewOptions,
) (Navigation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	applyTheme()
	app := tview.NewApplication().EnableMouse(true).EnablePaste(true).SetTitle("bili-live-tui")

	chat := tview.NewTextView()
	chat.SetScrollable(true)
	chat.SetMaxLines(danmakuHistoryLimit)
	chat.SetDynamicColors(true)
	chat.SetRegions(true)
	chat.SetWordWrap(true)
	setDanmakuChatColors(chat)
	chat.SetBorder(true)
	chat.SetBorderColor(tview.Styles.BorderColor)
	// 工作区标题已经命名页面，清空弹幕框标题，避免“弹幕互动”重复显示，同时保留边框。
	chat.SetTitle("")
	chat.SetTitleColor(tview.Styles.TitleColor)
	initialSessionSnapshot := session.snapshot()
	userRegions := newDanmakuUserRegionRegistry()
	renderDanmakuHistory(chat, initialSessionSnapshot.history, userRegions)

	status := tview.NewTextView()
	status.SetDynamicColors(true)
	status.SetTextColor(mutedColor)
	status.SetBackgroundColor(panelColor)
	status.SetText(formatDanmakuSessionStatus(initialSessionSnapshot))
	onlineRank := tview.NewTextView()
	onlineRank.SetScrollable(true)
	onlineRank.SetDynamicColors(true)
	onlineRank.SetRegions(true)
	onlineRank.SetWordWrap(false)
	onlineRank.SetBackgroundColor(panelColor)
	onlineRank.SetBorder(true)
	onlineRank.SetBorderColor(tview.Styles.BorderColor)
	onlineRank.SetTitleColor(tview.Styles.TitleColor)
	activeRankTab := rankTabAudience
	onlineRankUserRegions := newDanmakuUserRegionRegistry()
	renderOnlineRank(onlineRank, initialSessionSnapshot, activeRankTab, onlineRankUserRegions)
	sendStatus := tview.NewTextView()
	sendStatus.SetDynamicColors(true)
	sendStatus.SetTextColor(mutedColor)
	sendStatus.SetBackgroundColor(panelColor)
	streamStatus := tview.NewTextView()
	streamStatus.SetDynamicColors(true)
	streamStatus.SetTextColor(mutedColor)
	streamStatus.SetBackgroundColor(panelColor)
	if healthLoader != nil {
		streamStatus.SetText(formatStreamHealth(healthLoader()))
	}

	var danmakuMaxLength atomic.Int64
	danmakuMaxLength.Store(api.DefaultDanmakuMaxLength)
	limitReady := false
	limitFallback := false
	managementCapabilities := api.RoomManagementCapabilities{}
	managementCapabilitiesReady := false
	managementCapabilitiesErr := error(nil)
	managementCapabilitiesLoading := false
	managementCapabilitiesWaiters := make([]func(error), 0, 2)
	userAdminLevelOverrides := make(map[string]int)
	var refreshManagementCapabilitiesUI func()
	var refreshManagementCapabilities func(func(error))
	var reply *tview.InputField
	reply = tview.NewInputField().
		SetLabel("").
		SetPlaceholder("输入弹幕… (Enter 发送 · Ctrl+U 清空 · Ctrl+L 清屏 · Esc 下播)").
		SetPlaceholderTextColor(mutedColor).
		SetFieldWidth(0).
		SetLabelColor(tview.Styles.SecondaryTextColor).
		SetFieldStyle(tcell.StyleDefault.
			Background(tview.Styles.ContrastBackgroundColor).
			Foreground(tview.Styles.PrimaryTextColor)).
		SetAcceptanceFunc(func(text string, _ rune) bool {
			return acceptsDanmakuInput(reply, text, int(danmakuMaxLength.Load()))
		})
	reply.SetBackgroundColor(panelColor).SetBorder(true).SetBorderColor(accentColor)
	reply.SetTitleAlign(tview.AlignRight)
	reply.SetText(initialSessionSnapshot.draft)
	updateReplyCounter := func() {
		updateDanmakuReplyCounter(reply, int(danmakuMaxLength.Load()), limitReady, limitFallback)
	}
	updateReplyCounter()
	reply.SetChangedFunc(func(text string) {
		session.SetDraft(text)
		updateReplyCounter()
	})

	navigation := NavigationQuit
	sentCount := 0
	type pendingDanmakuSend struct {
		id              uint64
		count           int
		message         string
		startRevision   uint64
		requestAccepted bool
		streamConfirmed bool
	}
	var nextDanmakuSendID uint64
	pendingSends := make(map[uint64]*pendingDanmakuSend)
	var uiOpen atomic.Bool
	uiOpen.Store(true)
	streamCtx, cancelStream := context.WithCancel(ctx)
	uiUpdates := make(chan func(), 256)
	stopApplication := func() {
		uiOpen.Store(false)
		cancelStream()
		app.Stop()
	}
	queueUI := func(update func()) {
		if !uiOpen.Load() {
			return
		}
		select {
		case uiUpdates <- update:
		case <-streamCtx.Done():
		}
	}
	go func() {
		requestCtx, cancelRequest := context.WithTimeout(streamCtx, 8*time.Second)
		defer cancelRequest()
		limit, err := client.GetDanmakuMaxLength(requestCtx, roomID, sessdata, biliJCT)
		queueUI(func() {
			limitReady = true
			if err != nil {
				limitFallback = true
				danmakuMaxLength.Store(api.DefaultDanmakuMaxLength)
				if strings.TrimSpace(sendStatus.GetText(true)) == "" {
					sendStatus.SetText(fmt.Sprintf("未能获取弹幕字数上限，暂按 %d 字处理。", api.DefaultDanmakuMaxLength))
				}
			} else {
				limitFallback = false
				danmakuMaxLength.Store(int64(limit))
				if strings.HasPrefix(sendStatus.GetText(true), "正在获取弹幕字数上限") {
					sendStatus.SetText("")
				}
			}
			updateReplyCounter()
		})
	}()
	go func() {
		for {
			select {
			case <-streamCtx.Done():
				return
			case update := <-uiUpdates:
				if !uiOpen.Load() {
					continue
				}
				app.QueueUpdateDraw(func() {
					if uiOpen.Load() {
						update()
					}
				})
			}
		}
	}()
	refreshManagementCapabilities = func(after func(error)) {
		if after != nil {
			managementCapabilitiesWaiters = append(managementCapabilitiesWaiters, after)
		}
		if managementCapabilitiesLoading {
			return
		}
		managementCapabilitiesLoading = true
		go func() {
			requestCtx, cancelRequest := context.WithTimeout(streamCtx, 10*time.Second)
			defer cancelRequest()
			capabilities, err := client.GetRoomManagementCapabilities(requestCtx, roomID, sessdata, biliJCT)
			queueUI(func() {
				managementCapabilities = capabilities
				managementCapabilitiesReady = true
				managementCapabilitiesErr = err
				managementCapabilitiesLoading = false
				if refreshManagementCapabilitiesUI != nil {
					refreshManagementCapabilitiesUI()
				}
				waiters := managementCapabilitiesWaiters
				managementCapabilitiesWaiters = nil
				for _, waiter := range waiters {
					waiter(err)
				}
			})
		}()
	}
	refreshManagementCapabilities(nil)
	clearChat := func() {
		session.ClearHistory()
		sentCount = 0
		sendStatus.SetText("已清空本地弹幕记录。")
	}
	danmakuSender := api.NewDanmakuSender(client, roomID, sessdata, biliJCT)
	sending := false
	send := func() {
		if sending {
			sendStatus.SetText("正在发送上一条弹幕，请稍候……")
			return
		}
		message := strings.TrimSpace(reply.GetText())
		if message == "" {
			sendStatus.SetText("内容不能为空。")
			return
		}
		sending = true
		nextDanmakuSendID++
		submission := &pendingDanmakuSend{
			id:            nextDanmakuSendID,
			message:       message,
			startRevision: session.snapshot().historyRevision,
		}
		pendingSends[submission.id] = submission
		sendStatus.SetText("正在发送弹幕……")
		go func(send *pendingDanmakuSend) {
			requestCtx, cancelRequest := context.WithTimeout(streamCtx, 8*time.Second)
			defer cancelRequest()
			err := danmakuSender.Send(requestCtx, send.message, int(danmakuMaxLength.Load()))
			queueUI(func() {
				sending = false
				pending, exists := pendingSends[send.id]
				if !exists {
					return
				}
				if err != nil {
					if pending.streamConfirmed {
						if strings.TrimSpace(reply.GetText()) == send.message {
							reply.SetText("")
						}
						sentCount++
						delete(pendingSends, send.id)
						sendStatus.SetText(formatDanmakuSendConfirmedStatus(sentCount))
						return
					}
					delete(pendingSends, send.id)
					if api.IsDanmakuDeliveryUnknown(err) {
						sendStatus.SetText("发送结果未确认，内容仍在输入框中；请先查看直播间，避免重复发送。")
						return
					}
					if api.IsDanmakuProhibited(err) {
						sendStatus.SetText("弹幕未发送成功，内容仍在输入框中，可能包含屏蔽词。")
						return
					}
					sendStatus.SetText("发送失败，内容仍在输入框中：" + tview.Escape(err.Error()))
					return
				}
				if strings.TrimSpace(reply.GetText()) == send.message {
					reply.SetText("")
				}
				sentCount++
				pending.count = sentCount
				send.requestAccepted = true
				if pending.streamConfirmed {
					delete(pendingSends, send.id)
					sendStatus.SetText(formatDanmakuSendConfirmedStatus(pending.count))
					return
				}
				snapshot := session.snapshot()
				connected := snapshot.connection.connected()
				sendStatus.SetText(formatDanmakuSendAcceptedStatus(pending.count, connected))
				go func(sendID uint64) {
					timer := time.NewTimer(10 * time.Second)
					defer timer.Stop()
					select {
					case <-streamCtx.Done():
						return
					case <-timer.C:
						queueUI(func() {
							p, ok := pendingSends[sendID]
							if !ok || p.streamConfirmed {
								return
							}
							delete(pendingSends, sendID)
							if sendID == nextDanmakuSendID {
								snap := session.snapshot()
								conn := snap.connection.connected()
								sendStatus.SetText(formatDanmakuSendTimeoutStatus(p.count, conn))
							}
						})
					}
				}(send.id)
			})
		}(submission)
	}
	pages := tview.NewPages()
	previousFocus := tview.Primitive(reply)
	userCard, userCardContent, userCardActions := newDanmakuUserCardPanel()
	const userCardWidth = 45
	userCardOverlay := newFloatingOverlay(userCard, userCardWidth, 12).SetOpaqueBackground(panelColor)
	setUserCardText := func(text string) {
		userCardContent.SetText(text)
		userCardOverlay.SetPreferredSize(userCardWidth, danmakuUserCardHeight(text, userCardWidth))
	}
	userCardVisible := false
	selectedUserID := ""
	selectedUserMessage := api.DanmakuMessage{}
	userProfileCache := make(map[string]api.UserProfile)
	userProfileLoading := make(map[string]bool)
	userCardFocusIndex := 0
	focusUserCardAction := func(index int) {
		buttonCount := userCardActions.GetButtonCount()
		if buttonCount == 0 {
			return
		}
		index = (index%buttonCount + buttonCount) % buttonCount
		userCardFocusIndex = index
		userCardActions.SetFocus(index)
		// ClearButtons 会移除 Application 当前聚焦的旧 Button。每次重建
		// 按钮后都重新委派焦点，否则新按钮看得见却收不到键盘事件。
		app.SetFocus(userCardActions)
	}
	closeUserCard := func() {
		userCardVisible = false
		selectedUserID = ""
		selectedUserMessage = api.DanmakuMessage{}
		pages.HidePage("user-card")
		if previousFocus != nil {
			app.SetFocus(previousFocus)
		} else {
			app.SetFocus(reply)
		}
	}
	userCardActions.SetCancelFunc(closeUserCard)

	managementMenu := newDanmakuManagementList(" 用户管理 ")
	managementMenuOverlay := newFloatingOverlay(managementMenu, 46, 9).SetOpaqueBackground(panelColor)
	muteDurationMenu := newDanmakuManagementList(" 选择禁言时长 ")
	muteDurationOverlay := newFloatingOverlay(muteDurationMenu, 38, 10).SetOpaqueBackground(panelColor)
	managementConfirm := styleModal(tview.NewModal())
	managementVisible := false
	muteDurationVisible := false
	managementConfirmVisible := false
	closeManagementMenu := func() {
		managementVisible = false
		pages.HidePage("user-management")
		pages.SendToFront("user-card")
		focusUserCardAction(userCardFocusIndex)
	}
	closeMuteDuration := func() {
		muteDurationVisible = false
		pages.HidePage("mute-duration")
		pages.SendToFront("user-management")
		app.SetFocus(managementMenu)
	}
	var pendingManagementAction func(context.Context) error
	var pendingManagementSuccess func()
	managementActionLabel := ""
	closeManagementConfirm := func() {
		managementConfirmVisible = false
		pendingManagementAction = nil
		pendingManagementSuccess = nil
		pages.HidePage("management-confirm")
		pages.SendToFront("user-management")
		app.SetFocus(managementMenu)
	}
	runManagementAction := func() {
		action := pendingManagementAction
		onSuccess := pendingManagementSuccess
		label := managementActionLabel
		managementConfirmVisible = false
		managementVisible = false
		muteDurationVisible = false
		pendingManagementAction = nil
		pendingManagementSuccess = nil
		pages.HidePage("management-confirm")
		pages.HidePage("mute-duration")
		pages.HidePage("user-management")
		closeUserCard()
		sendStatus.SetText("正在" + label + "……")
		go func() {
			requestCtx, cancelRequest := context.WithTimeout(streamCtx, 10*time.Second)
			defer cancelRequest()
			err := action(requestCtx)
			queueUI(func() {
				if err != nil {
					sendStatus.SetText(label + "失败：" + tview.Escape(err.Error()))
					return
				}
				if onSuccess != nil {
					onSuccess()
				}
				sendStatus.SetText(label + "成功。")
			})
		}()
	}
	managementConfirm.SetDoneFunc(func(buttonIndex int, _ string) {
		if buttonIndex == 1 && pendingManagementAction != nil {
			runManagementAction()
			return
		}
		closeManagementConfirm()
	})
	openManagementConfirm := func(label, warning string, action func(context.Context) error, onSuccess ...func()) {
		managementActionLabel = label
		pendingManagementAction = action
		pendingManagementSuccess = nil
		if len(onSuccess) > 0 {
			pendingManagementSuccess = onSuccess[0]
		}
		// “取消”始终排在第一位并获得默认焦点，处罚操作不会因一次回车误触。
		managementConfirm.ClearButtons().SetText(warning).AddButtons([]string{"取消", "确认"})
		managementConfirmVisible = true
		pages.ShowPage("management-confirm")
		pages.SendToFront("management-confirm")
		app.SetFocus(managementConfirm)
	}
	var openUserManagement func(api.DanmakuMessage, *api.UserProfile)
	openUserManagement = func(message api.DanmakuMessage, profile *api.UserProfile) {
		managementMenu.Clear()
		targetAdminLevel, targetAdminLevelKnown := danmakuTargetAdminStatus(message)
		targetAdminLevelOverridden := false
		if level, ok := userAdminLevelOverrides[strings.TrimSpace(message.UserID)]; ok {
			targetAdminLevel = level
			targetAdminLevelKnown = true
			targetAdminLevelOverridden = true
		}
		username := strings.TrimSpace(message.Username)
		if username == "" {
			username = message.UserID
		}
		addAction := func(label, detail string, selected func()) {
			managementMenu.AddItem(label, detail, 0, selected)
		}
		if managementCapabilities.CanMuteUser(targetAdminLevel, targetAdminLevelKnown) {
			addAction("禁言", "选择 2 小时至永久，或仅本场直播", func() {
				muteDurationMenu.Clear()
				durations := []struct {
					label    string
					duration api.RoomUserMuteDuration
				}{
					{"仅本场直播", api.RoomMuteThisLive},
					{"2 小时", api.RoomMuteTwoHours},
					{"4 小时", api.RoomMuteFourHours},
					{"24 小时", api.RoomMuteOneDay},
					{"7 天", api.RoomMuteSevenDays},
					{"永久", api.RoomMutePermanent},
				}
				for _, item := range durations {
					item := item
					muteDurationMenu.AddItem(item.label, "", 0, func() {
						muteDurationVisible = false
						pages.HidePage("mute-duration")
						openManagementConfirm("禁言 "+username, "确定要禁言 "+username+"（"+item.label+"）吗？", func(requestCtx context.Context) error {
							return client.MuteRoomUser(requestCtx, roomID, message.UserID, message.Text, item.duration, sessdata, biliJCT)
						})
					})
				}
				muteDurationMenu.AddItem("返回", "", 0, closeMuteDuration)
				muteDurationMenu.SetCurrentItem(muteDurationMenu.GetItemCount() - 1)
				muteDurationVisible = true
				pages.ShowPage("mute-duration")
				pages.SendToFront("mute-duration")
				app.SetFocus(muteDurationMenu)
			})
		}
		if managementCapabilities.CanBlacklistUser(targetAdminLevel, targetAdminLevelKnown) && strings.TrimSpace(managementCapabilities.AnchorID) != "" {
			addAction("添加至黑名单", "添加到当前直播间黑名单", func() {
				openManagementConfirm("拉黑 "+username, "确定将 "+username+" 添加到直播间黑名单吗？", func(requestCtx context.Context) error {
					return client.BlacklistRoomUser(requestCtx, managementCapabilities.AnchorID, message.UserID, sessdata, biliJCT)
				})
			})
		}
		if managementCapabilities.IsAnchor {
			targetIsAdmin := message.IsAdmin
			if targetAdminLevelOverridden {
				targetIsAdmin = targetAdminLevel > 0
			}
			if targetIsAdmin {
				addAction("撤销房管", "撤销该用户的直播间管理权限", func() {
					openManagementConfirm("撤销房管 "+username, "确定撤销 "+username+" 的房管权限吗？", func(requestCtx context.Context) error {
						return client.DismissRoomAdmin(requestCtx, message.UserID, sessdata, biliJCT)
					}, func() { userAdminLevelOverrides[strings.TrimSpace(message.UserID)] = 0 })
				})
			} else {
				addAction("设为房管", "授予普通房管身份", func() {
					openManagementConfirm("任命房管 "+username, "确定任命 "+username+" 为房管吗？", func(requestCtx context.Context) error {
						return client.AppointRoomAdmin(requestCtx, message.UserID, 1, sessdata, biliJCT)
					}, func() { userAdminLevelOverrides[strings.TrimSpace(message.UserID)] = 1 })
				})
			}
		}
		managementMenu.AddItem("返回用户资料", "", 0, closeManagementMenu)
		managementMenu.SetCurrentItem(managementMenu.GetItemCount() - 1)
		managementMenuOverlay.SetPreferredSize(46, min(max(managementMenu.GetItemCount()*2+3, 7), 14))
		managementVisible = true
		pages.ShowPage("user-management")
		pages.SendToFront("user-management")
		app.SetFocus(managementMenu)
	}

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
	roomManagerInput := newDanmakuManagementForm("")
	roomManagerInput.SetItemPadding(1)
	roomManagerInputCard := newFloatingOverlay(roomManagerInput, 56, 9)
	var roomManagerPrevPage func()
	var roomManagerNextPage func()
	roomManagerBackButton := newActionButton("返回弹幕", nil)
	roomManagerRetryButton := newActionButton("重新加载", nil)
	roomManagerAddAdminButton := newActionButton("添加房管", nil)
	roomManagerMuteUserButton := newActionButton("禁言用户", nil)
	roomManagerBlacklistUserButton := newActionButton("添加黑名单用户", nil)
	roomManagerAddKeywordButton := newActionButton("添加屏蔽词", nil)
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
	roomManagerActionBar := centeredActionBar([]*tview.Button{roomManagerBackButton})
	updateRoomManagerActionBar := func(mode string) {
		previousFocus := app.GetFocus()
		wasActionButton := previousFocus == roomManagerBackButton ||
			previousFocus == roomManagerRetryButton ||
			previousFocus == roomManagerAddAdminButton ||
			previousFocus == roomManagerMuteUserButton ||
			previousFocus == roomManagerBlacklistUserButton ||
			previousFocus == roomManagerAddKeywordButton ||
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
			case "keyword":
				roomManagerCurrentButtons = append(roomManagerCurrentButtons, roomManagerAddKeywordButton)
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
		populateCenteredActionBar(roomManagerActionBar, roomManagerCurrentButtons)
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
	roomManagerContent.AddPage("input", roomManagerInputCard, true, false)
	roomManagerContent.AddPage("empty", roomManagerEmptyCenter, true, false)
	roomManagerContent.AddPage("error", roomManagerErrorCenter, true, false)
	roomManagerContentPanel := tview.NewFlex().SetDirection(tview.FlexRow)
	roomManagerContentPanel.SetBackgroundColor(panelColor)
	roomManagerContentPanel.SetBorder(true)
	roomManagerContentPanel.SetBorderColor(tview.Styles.BorderColor)
	roomManagerContentPanel.AddItem(roomManagerSection, 1, 0, false)
	roomManagerContentPanel.AddItem(roomManagerNotice, 1, 0, false)
	roomManagerContentPanel.AddItem(roomManagerContent, 0, 1, true)
	roomManagerContentPanel.AddItem(roomManagerActionBar, 1, 0, true)
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
	roomManagerConfirm := styleModal(tview.NewModal())
	roomManagerVisible := false
	roomManagerConfirmVisible := false
	roomManagerInputVisible := false
	roomManagerGeneration := uint64(0)
	type roomManagerTab struct {
		label string
		load  func()
	}
	roomManagerTabsAvailable := make([]roomManagerTab, 0, 5)
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
	var loadShieldKeywords func()
	var loadRoomSilent func()
	var selectRoomManagerTab func(int)
	var roomManagerReload func()
	var roomManagerRetryAction func()
	var triggerAddRoomAdmin func()
	var triggerMuteRoomUser func()
	var triggerBlacklistRoomUser func()
	var triggerAddShieldKeyword func()
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
	var renderRoomManagerSearchResults func()
	var showRoomManagerAdminChoices func(api.RoomUserSearchResult, bool)
	var showRoomManagerMuteChoices func(api.RoomUserSearchResult)
	var searchRoomManagerUsers func(roomManagerUserOperation, string)
	var openRoomManagerUserSearch func(roomManagerUserOperation)
	focusRoomManagerContent := func() {
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
		// 房间管理是独立工作区，不与弹幕页叠加绘制。切回主页面时也用
		// SwitchToPage 恢复唯一可见页，避免宽字符残留在下一帧。
		pages.SwitchToPage("main")
		app.SetFocus(reply)
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
	roomManagerAddKeywordButton.SetSelectedFunc(func() {
		if triggerAddShieldKeyword != nil {
			triggerAddShieldKeyword()
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
		roomManagerInputVisible = false
		roomManagerRetryAction = retry
		roomManagerSection.SetText("[" + errorColor.String() + "]请求异常[-]")
		setRoomManagerNotice("请求失败："+compactDanmakuManagementError(err), true)
		errCard := fmt.Sprintf("[%s::b]%s 加载失败[-:-:-]\n\n[%s]%s[-]",
			errorColor.String(),
			tview.Escape(title),
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
	roomManagerSectionBeforeInput := ""
	roomManagerPageBeforeInput := "table"
	closeRoomManagerInput := func() {
		roomManagerInputVisible = false
		roomManagerContent.SwitchToPage(roomManagerPageBeforeInput)
		roomManagerSection.SetText(roomManagerSectionBeforeInput)
		setRoomManagerNotice("", false)
		focusRoomManagerContent()
	}
	roomManagerInput.SetCancelFunc(closeRoomManagerInput)
	openRoomManagerInput := func(title, label, initial string, maxLength int, submitLabel string, validate func(string) error, submitted func(string)) {
		roomManagerInput.Clear(true)
		roomManagerSectionBeforeInput = roomManagerSection.GetText(false)
		roomManagerPageBeforeInput, _ = roomManagerContent.GetFrontPage()
		roomManagerSection.SetText("[" + mutedColor.String() + "]填写后确认操作[-]")
		setRoomManagerNotice("", false)
		roomManagerInput.SetTitle(" " + title + " ")
		roomManagerInput.AddInputField(label, initial, maxLength, nil, func(string) {
			setRoomManagerNotice("", false)
		})
		if strings.TrimSpace(submitLabel) == "" {
			submitLabel = "保存"
		}
		roomManagerInput.AddButton(submitLabel, func() {
			value := roomManagerInput.GetFormItem(0).(*tview.InputField).GetText()
			if validate != nil {
				if err := validate(value); err != nil {
					setRoomManagerNotice(err.Error(), true)
					roomManagerInput.SetFocus(0)
					return
				}
			}
			closeRoomManagerInput()
			submitted(value)
		})
		roomManagerInput.AddButton("取消", closeRoomManagerInput)
		roomManagerInputVisible = true
		roomManagerContent.SwitchToPage("input")
		app.SetFocus(roomManagerInput)
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
			title = "禁言用户"
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
		case "屏蔽词":
			roomManagerBaseActionMode = "keyword"
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
			result, err := client.GetRoomAdmins(requestCtx, roomAdminPage, sessdata, biliJCT)
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
		go func() {
			requestCtx, cancelRequest := context.WithTimeout(streamCtx, 10*time.Second)
			defer cancelRequest()
			result, err := client.GetMutedRoomUsers(requestCtx, roomID, mutedUserPage, sessdata, biliJCT)
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
					roomManagerTable.SetCell(row, 1, roomManagerTableMutedCell(fallbackDanmakuManagementText(item.ExpiresAt, "永久或未知"), 20, 1))
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
		go func() {
			requestCtx, cancelRequest := context.WithTimeout(streamCtx, 10*time.Second)
			defer cancelRequest()
			result, err := client.GetRoomBlacklist(requestCtx, managementCapabilities.AnchorID, blacklistPage, 50, sessdata, biliJCT)
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
							return client.UnblacklistRoomUser(actionCtx, managementCapabilities.AnchorID, item.UserID, sessdata, biliJCT)
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
	loadShieldKeywords = func() {
		generation := showRoomManagerLoading("屏蔽词")
		roomManagerReload = loadShieldKeywords
		go func() {
			requestCtx, cancelRequest := context.WithTimeout(streamCtx, 10*time.Second)
			defer cancelRequest()
			state, err := client.GetRoomShieldKeywords(requestCtx, roomID, sessdata, biliJCT)
			queueUI(func() {
				if err != nil {
					showRoomManagerError(generation, "屏蔽词", err, loadShieldKeywords)
					return
				}
				if !roomManagerVisible || generation != roomManagerGeneration {
					return
				}
				showRoomManagerTableView()
				roomManagerTable.Clear()
				clear(roomManagerTableActions)
				roomManagerTable.SetCell(0, 0, roomManagerTableHeaderCell("屏蔽词", 48, 1))
				roomManagerTable.SetCell(0, 1, roomManagerTableHeaderCell("操作", 8, 0).SetAlign(tview.AlignCenter))
				count := fmt.Sprintf("%d", len(state.Keywords))
				if state.MaxCount > 0 {
					count = fmt.Sprintf("%d/%d", len(state.Keywords), state.MaxCount)
				}
				canAddKeyword := state.MaxCount <= 0 || len(state.Keywords) < state.MaxCount
				if canAddKeyword {
					roomManagerSection.SetText(fmt.Sprintf("[%s]已使用 %s[-]", mutedColor.String(), count))
					triggerAddShieldKeyword = func() {
						openRoomManagerInput("添加屏蔽词", "屏蔽词 ", "", 0, "保存", func(keyword string) error {
							if strings.TrimSpace(keyword) == "" {
								return fmt.Errorf("屏蔽词不能为空。")
							}
							return nil
						}, func(keyword string) {
							pendingRoomManagerLabel = "添加屏蔽词"
							pendingRoomManagerAction = func(actionCtx context.Context) error {
								return client.AddRoomShieldKeyword(actionCtx, roomID, keyword, sessdata, biliJCT)
							}
							runRoomManagerAction()
						})
					}
					roomManagerBaseActionMode = "keyword"
				} else {
					roomManagerSection.SetText(fmt.Sprintf("[%s]已使用 %s · 已达上限[-]", mutedColor.String(), count))
					triggerAddShieldKeyword = nil
					roomManagerBaseActionMode = "default"
				}
				updateRoomManagerActionBar(roomManagerBaseActionMode)
				if len(state.Keywords) == 0 {
					showRoomManagerEmptyView("暂无屏蔽词")
					restoreRoomManagerFocusAfterLoad()
					return
				}
				for index, keyword := range state.Keywords {
					keyword := keyword
					row := index + 1
					action := func() {
						openRoomManagerConfirm("删除屏蔽词", "确定删除屏蔽词“"+keyword+"”吗？", func(actionCtx context.Context) error {
							return client.DeleteRoomShieldKeyword(actionCtx, roomID, keyword, sessdata, biliJCT)
						})
					}
					roomManagerTableActions[row] = action
					roomManagerTable.SetCell(row, 0, roomManagerTableTextCell(tview.Escape(keyword), 48, 1))
					roomManagerTable.SetCell(row, 1, roomManagerTableActionCell("删除", errorColor, action))
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
	mentionUser := func(message api.DanmakuMessage) {
		username := strings.TrimSpace(message.Username)
		if username == "" {
			return
		}
		text, ok := prependDanmakuMention(reply.GetText(), username, int(danmakuMaxLength.Load()))
		if !ok {
			sendStatus.SetText("@ 用户后将超过当前弹幕字数上限，未修改输入内容。")
			closeUserCard()
			return
		}
		reply.SetText(text)
		closeUserCard()
	}
	var configureUserCardActions func(api.DanmakuMessage, *api.UserProfile)
	configureUserCardActions = func(message api.DanmakuMessage, profile *api.UserProfile) {
		userCardActions.ClearButtons()
		if canFollowDanmakuUser(message, profile) {
			userCardActions.AddButton("关注", func() {
				// 请求通常很快，但仍禁用一次以避免鼠标连点产生重复操作。
				// 按钮文字保持不变，不用短暂的“关注中…”扰动布局。
				userCardActions.GetButton(0).SetDisabled(true)
				focusUserCardAction(userCardActions.GetButtonCount() - 1)
				profileBeforeFollow := *profile
				go func(clicked api.DanmakuMessage) {
					requestCtx, cancelRequest := context.WithTimeout(streamCtx, 8*time.Second)
					defer cancelRequest()
					followErr := client.SetUserFollowing(requestCtx, clicked.UserID, sessdata, biliJCT, true)
					queueUI(func() {
						if followErr == nil {
							profileBeforeFollow.IsFollowing = true
							userProfileCache[clicked.UserID] = profileBeforeFollow
						}
						if !userCardVisible || selectedUserID != clicked.UserID {
							return
						}
						if followErr != nil {
							text := formatDanmakuUserCard(clicked, &profileBeforeFollow, false, nil)
							text += fmt.Sprintf("\n\n[%s]关注失败：%s[-]", errorColor.String(), tview.Escape(followErr.Error()))
							setUserCardText(text)
							configureUserCardActions(clicked, &profileBeforeFollow)
							return
						}
						text := formatDanmakuUserCard(clicked, &profileBeforeFollow, false, nil)
						text += fmt.Sprintf("\n\n[%s]关注成功[-]", accentActiveColor.String())
						setUserCardText(text)
						configureUserCardActions(clicked, &profileBeforeFollow)
					})
				}(message)
			})
		}
		if profile != nil && !profile.IsSelf && strings.TrimSpace(message.Username) != "" {
			userCardActions.AddButton("@TA", func() { mentionUser(message) })
		}
		if managementCapabilitiesReady && managementCapabilitiesErr == nil && canManageDanmakuUser(managementCapabilities, message, profile) {
			userCardActions.AddButton("管理", func() {
				sendStatus.SetText("正在确认房间管理权限……")
				refreshManagementCapabilities(func(err error) {
					if err != nil {
						sendStatus.SetText("确认房间管理权限失败：" + tview.Escape(err.Error()))
						return
					}
					if !userCardVisible || selectedUserID != strings.TrimSpace(message.UserID) {
						return
					}
					if !canManageDanmakuUser(managementCapabilities, message, profile) {
						sendStatus.SetText("当前账号已无权管理该用户。")
						configureUserCardActions(message, profile)
						return
					}
					sendStatus.SetText("")
					openUserManagement(message, profile)
				})
			})
		}
		userCardActions.AddButton("关闭", closeUserCard)
		// 打开资料卡以及异步刷新按钮后，默认都停在最安全的“关闭”上。
		focusUserCardAction(userCardActions.GetButtonCount() - 1)
	}
	openUserCard := func(message api.DanmakuMessage) {
		previousFocus = app.GetFocus()
		userID := strings.TrimSpace(message.UserID)
		selectedUserID = userID
		selectedUserMessage = message
		userCardVisible = true
		if profile, ok := userProfileCache[userID]; ok {
			setUserCardText(formatDanmakuUserCard(message, &profile, false, nil))
			configureUserCardActions(message, &profile)
		} else {
			setUserCardText(formatDanmakuUserCard(message, nil, userID != "", nil))
			configureUserCardActions(message, nil)
		}
		pages.ShowPage("user-card")
		pages.SendToFront("user-card")
		app.SetFocus(userCardActions)
		if userID == "" || userProfileLoading[userID] {
			return
		}
		if _, ok := userProfileCache[userID]; ok {
			return
		}
		userProfileLoading[userID] = true
		go func(clicked api.DanmakuMessage) {
			requestCtx, cancelRequest := context.WithTimeout(streamCtx, 8*time.Second)
			defer cancelRequest()
			profile, queryErr := client.GetUserProfile(requestCtx, clicked.UserID, sessdata, biliJCT)
			queueUI(func() {
				delete(userProfileLoading, clicked.UserID)
				if queryErr == nil {
					userProfileCache[clicked.UserID] = profile
				}
				if userCardVisible && selectedUserID == clicked.UserID {
					profileView := profilePointer(profile, queryErr)
					setUserCardText(formatDanmakuUserCard(clicked, profileView, false, queryErr))
					configureUserCardActions(clicked, profileView)
				}
			})
		}(message)
	}
	refreshManagementCapabilitiesUI = func() {
		if !userCardVisible || selectedUserID == "" {
			return
		}
		if profile, ok := userProfileCache[selectedUserID]; ok {
			configureUserCardActions(selectedUserMessage, &profile)
		}
	}
	chat.SetHighlightedFunc(func(added, _, _ []string) {
		if len(added) == 0 {
			return
		}
		message, ok := userRegions.Lookup(added[0])
		chat.Highlight()
		if ok {
			openUserCard(message)
		}
	})
	onlineRank.SetHighlightedFunc(func(added, _, _ []string) {
		if len(added) == 0 {
			return
		}
		region := added[0]
		onlineRank.Highlight()
		switch region {
		case "tab_audience":
			activeRankTab = rankTabAudience
			renderOnlineRank(onlineRank, session.snapshot(), activeRankTab, onlineRankUserRegions)
		case "tab_guard":
			activeRankTab = rankTabGuard
			renderOnlineRank(onlineRank, session.snapshot(), activeRankTab, onlineRankUserRegions)
		default:
			if message, ok := onlineRankUserRegions.Lookup(region); ok {
				openUserCard(message)
			}
		}
	})
	confirm := styleModal(tview.NewModal()).
		SetText("确定下播并退出吗？").
		AddButtons([]string{"取消", "下播并退出"})
	confirm.SetDoneFunc(func(buttonIndex int, _ string) {
		if buttonIndex == 1 {
			navigation = NavigationQuit
			stopApplication()
			return
		}
		pages.HidePage("confirm-stop")
		if previousFocus != nil {
			app.SetFocus(previousFocus)
		} else {
			app.SetFocus(reply)
		}
	})
	openStopConfirm := func() {
		previousFocus = app.GetFocus()
		pages.ShowPage("confirm-stop")
		app.SetFocus(confirm)
	}

	var openOverview func()
	toolButtons := []*tview.Button{
		newActionButton("直播概览", func() {
			if openOverview != nil {
				openOverview()
			}
		}),
		newActionButton("房间管理", openRoomManager),
	}
	toolBar := centeredActionBar(toolButtons)
	toolBar.SetBackgroundColor(panelColor)

	mainFocusables := []tview.Primitive{chat, onlineRank, reply}
	for _, button := range toolButtons {
		mainFocusables = append(mainFocusables, button)
	}
	cycleMainFocus := func(backward bool) {
		current := app.GetFocus()
		next := 0
		if backward {
			next = len(mainFocusables) - 1
		}
		for index, primitive := range mainFocusables {
			if current != primitive {
				continue
			}
			if backward {
				next = (index - 1 + len(mainFocusables)) % len(mainFocusables)
			} else {
				next = (index + 1) % len(mainFocusables)
			}
			break
		}
		app.SetFocus(mainFocusables[next])
	}
	chat.SetFocusFunc(func() {
		setFocusBorder(chat.Box, true)
	})
	chat.SetBlurFunc(func() {
		setFocusBorder(chat.Box, false)
	})
	onlineRank.SetFocusFunc(func() {
		setFocusBorder(onlineRank.Box, true)
	})
	onlineRank.SetBlurFunc(func() {
		setFocusBorder(onlineRank.Box, false)
	})
	reply.SetFocusFunc(func() {
		setFocusBorder(reply.Box, true)
	})
	reply.SetBlurFunc(func() {
		setFocusBorder(reply.Box, false)
	})

	body := tview.NewFlex()
	body.SetDirection(tview.FlexRow)
	body.SetBackgroundColor(panelColor)
	activity := tview.NewFlex()
	activity.SetDirection(tview.FlexColumn)
	activity.SetBackgroundColor(panelColor)
	activity.AddItem(chat, 0, 1, true)
	activity.AddItem(onlineRank, 32, 0, false)
	body.AddItem(activity, 0, 1, true)
	body.AddItem(status, 1, 0, false)
	body.AddItem(sendStatus, 1, 0, false)
	if healthLoader != nil {
		body.AddItem(streamStatus, 1, 0, false)
	}
	body.AddItem(reply, 3, 0, true)
	body.AddItem(toolBar, 1, 0, false)
	root := workspacePage(
		workspaceHeader("弹幕互动"),
		body,
		nil,
	)
	pages.AddPage("main", root, true, true)
	pages.AddPage("user-card", userCardOverlay, true, false)
	pages.AddPage("user-management", managementMenuOverlay, true, false)
	pages.AddPage("mute-duration", muteDurationOverlay, true, false)
	pages.AddPage("management-confirm", managementConfirm, true, false)
	pages.AddPage("room-manager", roomManagerPage, true, false)
	pages.AddPage("room-manager-confirm", roomManagerConfirm, true, false)
	pages.AddPage("confirm-stop", confirm, true, false)

	var homeWS *homeWorkspaceComponents
	var overviewVisible bool
	openOverview = func() {
		if homeWS == nil {
			return
		}
		overviewVisible = true
		homeWS.setStatusText()
		pages.SwitchToPage("home")
		if len(homeWS.buttons) > 0 {
			app.SetFocus(homeWS.buttons[0])
		}
	}
	closeOverview := func() {
		if homeWS != nil && homeWS.isEditing != nil && homeWS.isEditing() {
			homeWS.cancelEditing()
		}
		overviewVisible = false
		pages.SwitchToPage("main")
		app.SetFocus(reply)
	}
	if len(overviewOpts) > 0 {
		opts := overviewOpts[0]
		if strings.TrimSpace(opts.RoomID) == "" {
			opts.RoomID = roomID
		}
		if opts.HealthLoader == nil {
			opts.HealthLoader = healthLoader
		}
		homeWS = newHomeWorkspace(
			ctx,
			app,
			pages,
			"home",
			opts,
			session.Stats,
			closeOverview,
			openStopConfirm,
		)
		defer homeWS.stopRefresh()
		pages.AddPage("home", homeWS.root, true, false)
	}
	wideRankLayout := true
	rankRows := 7
	app.SetBeforeDrawFunc(func(screen tcell.Screen) bool {
		width, height := screen.Size()
		wide := width >= 92
		rows := 7
		if height < 22 {
			rows = 5
		}
		if wide != wideRankLayout || (!wide && rows != rankRows) {
			if wide {
				activity.SetDirection(tview.FlexColumn)
				activity.ResizeItem(onlineRank, 32, 0)
			} else {
				activity.SetDirection(tview.FlexRow)
				activity.ResizeItem(onlineRank, rows, 0)
			}
			wideRankLayout = wide
			rankRows = rows
		}
		return false
	})
	updates, unsubscribe := session.subscribe()
	// 将更新与上方实际绘制的快照比较。subscribe 始终安排一次刷新，
	// 因此页面挂载期间收到的消息不会被误认为已经显示。
	renderedHistoryRevision := initialSessionSnapshot.historyRevision
	go func() {
		for {
			select {
			case <-streamCtx.Done():
				return
			case _, ok := <-updates:
				if !ok {
					return
				}
				snapshot := session.snapshot()
				queueUI(func() {
					status.SetText(formatDanmakuSessionStatus(snapshot))
					renderOnlineRank(onlineRank, snapshot, activeRankTab, onlineRankUserRegions)
					for id, pending := range pendingSends {
						if danmakuSnapshotConfirmsSend(snapshot, pending.startRevision, pending.message, managementCapabilities.UserID) {
							pending.streamConfirmed = true
							if pending.requestAccepted {
								delete(pendingSends, id)
								sendStatus.SetText(formatDanmakuSendConfirmedStatus(pending.count))
							}
						}
					}
					if snapshot.historyRevision != renderedHistoryRevision {
						renderedHistoryRevision = updateDanmakuHistory(chat, snapshot, renderedHistoryRevision, userRegions)
					} else if len(snapshot.history) == 0 && chat.GetText(true) != "" {
						chat.SetText("")
					}
				})
			}
		}
	}()
	if healthLoader != nil {
		go func() {
			ticker := time.NewTicker(2 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-streamCtx.Done():
					return
				case <-ticker.C:
					health := healthLoader()
					queueUI(func() { streamStatus.SetText(formatStreamHealth(health)) })
				}
			}
		}()
	}
	viewDone := make(chan struct{})
	defer close(viewDone)
	go func() {
		select {
		case <-ctx.Done():
			stopApplication()
		case <-viewDone:
		}
	}()

	app.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if overviewVisible {
			if homeWS != nil && homeWS.isEditing != nil && homeWS.isEditing() {
				return event
			}
			if confirm.HasFocus() {
				if event.Key() == tcell.KeyEscape {
					pages.HidePage("confirm-stop")
					if homeWS != nil && len(homeWS.buttons) > 0 {
						app.SetFocus(homeWS.buttons[len(homeWS.buttons)-1])
					}
					return nil
				}
				return event
			}
			if pages.HasPage(executablePathPageName) && event.Key() != tcell.KeyCtrlC {
				return event
			}
			if navigateHomeWorkspace(app, homeWS, event) {
				return nil
			}
			switch event.Key() {
			case tcell.KeyEscape:
				closeOverview()
				return nil
			case tcell.KeyCtrlC:
				navigation = NavigationQuit
				stopApplication()
				return nil
			}
			return event
		}
		if userCardVisible {
			switch event.Key() {
			case tcell.KeyEscape:
				closeUserCard()
				return nil
			case tcell.KeyLeft, tcell.KeyBacktab:
				if _, buttonIndex := userCardActions.GetFocusedItemIndex(); buttonIndex >= 0 {
					userCardFocusIndex = buttonIndex
				}
				focusUserCardAction(userCardFocusIndex - 1)
				return nil
			case tcell.KeyRight, tcell.KeyTab:
				if _, buttonIndex := userCardActions.GetFocusedItemIndex(); buttonIndex >= 0 {
					userCardFocusIndex = buttonIndex
				}
				focusUserCardAction(userCardFocusIndex + 1)
				return nil
			case tcell.KeyCtrlC:
				navigation = NavigationQuit
				stopApplication()
				return nil
			default:
				return event
			}
		}
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
				navigation = NavigationQuit
				stopApplication()
				return nil
			default:
				return event
			}
		}
		if roomManagerInputVisible {
			switch {
			case event.Key() == tcell.KeyEscape:
				closeRoomManagerInput()
				return nil
			case matchesControlShortcut(event, tcell.KeyCtrlU, 'u'):
				if input, ok := roomManagerInput.GetFormItem(0).(*tview.InputField); ok {
					input.SetText("")
				}
				return nil
			case event.Key() == tcell.KeyCtrlC:
				navigation = NavigationQuit
				stopApplication()
				return nil
			default:
				return event
			}
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
			case event.Key() == tcell.KeyUp, event.Key() == tcell.KeyDown:
				if isActionBarFocused() {
					focusRoomManagerContent()
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
			case event.Key() == tcell.KeyRune && (event.Rune() == 'a' || event.Rune() == 'A'):
				if len(roomManagerTabsAvailable) > 0 && roomManagerTabIndex < len(roomManagerTabsAvailable) && roomManagerTabsAvailable[roomManagerTabIndex].label == "屏蔽词" {
					if triggerAddShieldKeyword != nil {
						triggerAddShieldKeyword()
						return nil
					}
				}
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
				navigation = NavigationQuit
				stopApplication()
				return nil
			default:
				return event
			}
		}
		if managementConfirmVisible {
			switch event.Key() {
			case tcell.KeyEscape:
				closeManagementConfirm()
				return nil
			case tcell.KeyCtrlC:
				navigation = NavigationQuit
				stopApplication()
				return nil
			default:
				return event
			}
		}
		if muteDurationVisible {
			switch event.Key() {
			case tcell.KeyEscape:
				closeMuteDuration()
				return nil
			case tcell.KeyCtrlC:
				navigation = NavigationQuit
				stopApplication()
				return nil
			default:
				return event
			}
		}
		if managementVisible {
			switch event.Key() {
			case tcell.KeyEscape:
				closeManagementMenu()
				return nil
			case tcell.KeyCtrlC:
				navigation = NavigationQuit
				stopApplication()
				return nil
			default:
				return event
			}
		}
		if confirm.HasFocus() {
			switch event.Key() {
			case tcell.KeyEscape:
				pages.HidePage("confirm-stop")
				if previousFocus != nil {
					app.SetFocus(previousFocus)
				} else {
					app.SetFocus(reply)
				}
				return nil
			case tcell.KeyCtrlC:
				navigation = NavigationQuit
				stopApplication()
				return nil
			default:
				return event
			}
		}
		if event.Key() == tcell.KeyTab || event.Key() == tcell.KeyBacktab {
			cycleMainFocus(event.Key() == tcell.KeyBacktab)
			return nil
		}
		if event.Key() == tcell.KeyLeft || event.Key() == tcell.KeyRight {
			for index, button := range toolButtons {
				if !button.HasFocus() {
					continue
				}
				next := index + 1
				if event.Key() == tcell.KeyLeft {
					next = index - 1
				}
				next = (next + len(toolButtons)) % len(toolButtons)
				app.SetFocus(toolButtons[next])
				return nil
			}
		}
		if onlineRank.HasFocus() {
			switch event.Key() {
			case tcell.KeyLeft:
				activeRankTab = rankTabAudience
				renderOnlineRank(onlineRank, session.snapshot(), activeRankTab, onlineRankUserRegions)
				return nil
			case tcell.KeyRight:
				activeRankTab = rankTabGuard
				renderOnlineRank(onlineRank, session.snapshot(), activeRankTab, onlineRankUserRegions)
				return nil
			}
		}

		switch {
		case matchesControlShortcut(event, tcell.KeyCtrlL, 'l'):
			clearChat()
			return nil
		case matchesControlShortcut(event, tcell.KeyCtrlU, 'u'):
			reply.SetText("")
			sendStatus.SetText("已清除输入内容。")
			return nil
		case matchesControlShortcut(event, tcell.KeyCtrlC, 'c'):
			stopApplication()
			return nil
		}
		switch event.Key() {
		case tcell.KeyEnter:
			// 输入框聚焦时拦截 Enter，发送消息后不意外跳到下一个控件。
			if reply.HasFocus() {
				send()
				return nil
			}
			return event
		case tcell.KeyEscape:
			openStopConfirm()
			return nil
		default:
			return event
		}
	})
	if err := app.SetRoot(pages, true).SetFocus(reply).Run(); err != nil {
		uiOpen.Store(false)
		cancelStream()
		unsubscribe()
		return NavigationQuit, fmt.Errorf("启动弹幕界面失败: %w", err)
	}
	uiOpen.Store(false)
	cancelStream()
	unsubscribe()
	return navigation, nil
}

func setDanmakuChatColors(chat *tview.TextView) {
	if noColor {
		chat.SetBackgroundColor(tcell.ColorDefault)
		chat.SetTextColor(tcell.ColorDefault)
		return
	}
	chat.SetBackgroundColor(panelColor)
	chat.SetTextColor(tview.Styles.PrimaryTextColor)
}

func acceptsDanmakuInput(field *tview.InputField, proposed string, maxLength int) bool {
	proposedLength := utf8.RuneCountInString(proposed)
	if proposedLength <= maxLength {
		return true
	}
	// 上游额度可能在页面打开后才返回。旧草稿已经超长时仍须允许逐字删除，
	// 否则 acceptance handler 会连退格键产生的文本也拒绝掉。
	return field != nil && proposedLength < utf8.RuneCountInString(field.GetText())
}

func updateDanmakuReplyCounter(field *tview.InputField, maxLength int, ready, fallback bool) {
	length := utf8.RuneCountInString(field.GetText())
	if !ready {
		field.SetTitle(fmt.Sprintf(" %d/… ", length))
		return
	}
	if fallback {
		field.SetTitle(fmt.Sprintf(" %d/%d 默认 ", length, maxLength))
		return
	}
	field.SetTitle(fmt.Sprintf(" %d/%d ", length, maxLength))
}

func newDanmakuManagementList(title string) *tview.List {
	list := tview.NewList()
	list.SetBackgroundColor(panelColor)
	list.SetMainTextColor(tview.Styles.PrimaryTextColor)
	list.SetSecondaryTextColor(mutedColor)
	list.SetSelectedStyle(selectedItemStyle())
	list.SetBorder(true)
	list.SetBorderColor(tview.Styles.BorderColor)
	list.SetTitle(title)
	list.SetTitleColor(tview.Styles.TitleColor)
	list.SetBorderPadding(0, 0, 1, 1)
	return list
}

func newDanmakuManagementForm(title string) *tview.Form {
	form := tview.NewForm()
	form.SetBackgroundColor(panelColor)
	form.SetFieldBackgroundColor(tview.Styles.ContrastBackgroundColor)
	form.SetFieldTextColor(tview.Styles.PrimaryTextColor)
	form.SetLabelColor(tview.Styles.SecondaryTextColor)
	form.SetButtonsAlign(tview.AlignCenter)
	form.SetButtonStyle(actionButtonStyle(false))
	form.SetButtonActivatedStyle(actionButtonStyle(true))
	form.SetBorder(true)
	form.SetBorderColor(tview.Styles.BorderColor)
	form.SetTitle(title)
	form.SetTitleColor(tview.Styles.TitleColor)
	form.SetBorderPadding(1, 0, 1, 1)
	return form
}

func compactDanmakuManagementError(err error) string {
	if err == nil {
		return "未知错误"
	}
	detail := []rune(strings.Join(strings.Fields(err.Error()), " "))
	if len(detail) > 70 {
		detail = append(detail[:70], '…')
	}
	return tview.Escape(string(detail))
}

func formatRoomManagerTableUser(username, userID string) string {
	username = strings.TrimSpace(username)
	userID = strings.TrimSpace(userID)
	switch {
	case username != "" && userID != "":
		return tview.Escape(username) + " · UID " + tview.Escape(userID)
	case username != "":
		return tview.Escape(username)
	case userID != "":
		return "UID " + tview.Escape(userID)
	default:
		return "未知用户"
	}
}

func roomManagerTableHeaderCell(text string, maxWidth, expansion int) *tview.TableCell {
	return tview.NewTableCell(text).
		SetTextColor(tview.Styles.SecondaryTextColor).
		SetAttributes(tcell.AttrBold).
		SetMaxWidth(maxWidth).
		SetExpansion(expansion).
		SetSelectable(false)
}

func roomManagerTableTextCell(text string, maxWidth, expansion int) *tview.TableCell {
	return tview.NewTableCell(text).
		SetTextColor(tview.Styles.PrimaryTextColor).
		SetMaxWidth(maxWidth).
		SetExpansion(expansion)
}

func roomManagerTableMutedCell(text string, maxWidth, expansion int) *tview.TableCell {
	return tview.NewTableCell(text).
		SetTextColor(mutedColor).
		SetMaxWidth(maxWidth).
		SetExpansion(expansion)
}

func roomManagerTableActionCell(label string, color tcell.Color, action func()) *tview.TableCell {
	cell := tview.NewTableCell(label).
		SetTextColor(color).
		SetAttributes(tcell.AttrBold).
		SetAlign(tview.AlignCenter).
		SetMaxWidth(8)
	cell.SetClickedFunc(func() bool {
		if action != nil {
			action()
		}
		return true
	})
	return cell
}

func displayDanmakuManagedUser(username, userID string) string {
	username = strings.TrimSpace(username)
	userID = strings.TrimSpace(userID)
	switch {
	case username != "" && userID != "":
		return tview.Escape(username) + "  UID " + tview.Escape(userID)
	case username != "":
		return tview.Escape(username)
	case userID != "":
		return "UID " + tview.Escape(userID)
	default:
		return "未知用户"
	}
}

func fallbackDanmakuManagementText(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return tview.Escape(value)
}

func managementTimestamp(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	return " · 任命于 " + tview.Escape(value)
}

func parseDanmakuManagementLevel(value string) (int, error) {
	level, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || level < 1 {
		return 0, fmt.Errorf("等级必须是正整数。")
	}
	return level, nil
}

func formatRoomSilentState(state api.RoomSilentState) string {
	label := map[string]string{
		api.RoomSilentAll:       "全员",
		api.RoomSilentNonFans:   "非本房粉丝",
		api.RoomSilentWealth:    fmt.Sprintf("荣耀等级低于 %d", state.Level),
		api.RoomSilentMedal:     fmt.Sprintf("粉丝勋章低于 %d", state.Level),
		api.RoomSilentNonMember: "除房管以外的观众",
	}[state.Audience]
	if label == "" {
		label = fallbackDanmakuManagementText(state.Audience, "未知身份")
	}
	if state.RemainingSeconds > 0 {
		label += fmt.Sprintf(" · 剩余 %s", (time.Duration(state.RemainingSeconds) * time.Second).Round(time.Second))
	} else {
		label += " · 持续至手动关闭"
	}
	return label
}

func danmakuTargetAdminStatus(message api.DanmakuMessage) (level int, known bool) {
	if message.IsAdmin {
		// 消息只表明“是房管”，没有给出普通或高级等级。数值 2 仅作为
		// 保守上界，known=false 会阻止非主播据此开放越级操作。
		return 2, false
	}
	return 0, true
}

func canManageDanmakuUser(capabilities api.RoomManagementCapabilities, message api.DanmakuMessage, profile *api.UserProfile) bool {
	userID := strings.TrimSpace(message.UserID)
	if userID == "" || userID == strings.TrimSpace(capabilities.UserID) || message.IsMystery || profile != nil && profile.IsSelf {
		return false
	}
	targetAdminLevel, targetAdminLevelKnown := danmakuTargetAdminStatus(message)
	return capabilities.CanMuteUser(targetAdminLevel, targetAdminLevelKnown) ||
		capabilities.CanBlacklistUser(targetAdminLevel, targetAdminLevelKnown) ||
		capabilities.IsAnchor
}

func matchesControlShortcut(event *tcell.EventKey, legacyKey tcell.Key, letter rune) bool {
	if event == nil {
		return false
	}
	if event.Key() == legacyKey {
		return true
	}
	// 某些终端会把控制字节作为 KeyRune 传递，不能只依赖 tcell 的别名键。
	if event.Key() == tcell.KeyRune && event.Rune() == rune(legacyKey) {
		return true
	}
	return matchesModifiedRuneShortcut(event, tcell.ModCtrl, letter)
}

func matchesModifiedRuneShortcut(event *tcell.EventKey, modifier tcell.ModMask, letter rune) bool {
	return event != nil &&
		event.Key() == tcell.KeyRune &&
		event.Modifiers()&modifier != 0 &&
		strings.EqualFold(string(event.Rune()), string(letter))
}

func formatDanmakuSessionStatus(snapshot liveDanmakuSnapshot) string {
	state := snapshot.connection
	endpoint := tview.Escape(formatDanmakuEndpoint(state.endpoint))
	switch state.phase {
	case danmakuConnectionConnecting:
		if state.attempt <= 1 {
			return "正在连接弹幕服务……"
		}
		return fmt.Sprintf("正在连接弹幕服务（第 %d 次）……", state.attempt)
	case danmakuConnectionAuthenticating:
		if endpoint == "" {
			return "已建立通道，正在等待服务器确认……"
		}
		return "已建立通道，正在等待服务器确认 · 节点 " + endpoint
	case danmakuConnectionConnected:
		status := "弹幕已连接"
		if endpoint != "" {
			status += " · 节点 " + endpoint
		}
		if snapshot.onlineKnown {
			status += fmt.Sprintf(" · 当前人气 %d", snapshot.online)
		}
		return status
	case danmakuConnectionRetrying:
		status := "弹幕连接未确认"
		if state.confirmed {
			status = "弹幕连接中断"
		} else if endpoint == "" {
			status = "弹幕连接失败"
		}
		details := make([]string, 0, 3)
		if !state.confirmed && endpoint == "" {
			details = append(details, fmt.Sprintf("第 %d 次", state.attempt))
		}
		if state.lastError != "" {
			details = append(details, tview.Escape(state.lastError))
		}
		if endpoint != "" {
			details = append(details, "节点 "+endpoint)
		}
		if len(details) > 0 {
			status += "（" + strings.Join(details, "，") + "）"
		}
		if state.confirmed {
			return status + "，" + formatDanmakuRetryDelay(state.retryDelay) + "后自动重连……"
		}
		return status + "，" + formatDanmakuRetryDelay(state.retryDelay) + "后重试……"
	default:
		return "弹幕连接状态未知"
	}
}

func formatDanmakuSendAcceptedStatus(count int, connected bool) string {
	if count <= 0 {
		if !connected {
			return "弹幕已发送（弹幕服务正在连接，暂未收到实时回显）。"
		}
		return "弹幕已发送，正在等待直播间显示……"
	}
	if !connected {
		return fmt.Sprintf("第 %d 条弹幕已发送（弹幕服务正在连接，暂未收到实时回显）。", count)
	}
	return fmt.Sprintf("第 %d 条弹幕已发送，正在等待直播间显示……", count)
}

func formatDanmakuSendTimeoutStatus(count int, connected bool) string {
	if count <= 0 {
		if !connected {
			return "弹幕已发送（弹幕服务未连接，未收到实时回显）。"
		}
		return "弹幕已发送（若直播间未显示，可能被 B 站审核过滤）。"
	}
	if !connected {
		return fmt.Sprintf("第 %d 条弹幕已发送（弹幕服务未连接，未收到实时回显）。", count)
	}
	return fmt.Sprintf("第 %d 条弹幕已发送（若直播间未显示，可能被 B 站审核过滤）。", count)
}

func formatDanmakuSendConfirmedStatus(count int) string {
	if count <= 0 {
		return "弹幕已在直播间显示。"
	}
	return fmt.Sprintf("第 %d 条弹幕已在直播间显示。", count)
}

func newDanmakuUserCardPanel() (*tview.Flex, *tview.TextView, *tview.Form) {
	content := tview.NewTextView()
	content.SetDynamicColors(true)
	content.SetTextAlign(tview.AlignLeft)
	content.SetWordWrap(true)
	content.SetScrollable(true)
	content.SetBackgroundColor(panelColor)
	content.SetTextColor(tview.Styles.PrimaryTextColor)
	content.SetBorderPadding(1, 0, 2, 2)

	actions := tview.NewForm()
	actions.SetBackgroundColor(panelColor)
	actions.SetButtonsAlign(tview.AlignCenter)
	actions.SetBorderPadding(0, 0, 0, 0)
	actions.SetButtonStyle(actionButtonStyle(false))
	actions.SetButtonActivatedStyle(actionButtonStyle(true))

	panel := tview.NewFlex().SetDirection(tview.FlexRow)
	panel.SetBackgroundColor(panelColor)
	panel.SetBorder(true)
	panel.SetBorderColor(tview.Styles.BorderColor)
	panel.SetTitle(" 用户资料 ")
	panel.SetTitleColor(tview.Styles.TitleColor)
	panel.AddItem(content, 0, 1, false)
	// 显式把留白放在按钮上方。不能直接给 Form 两行，否则它会把
	// 多出来的一行留到按钮下方。
	actionArea := tview.NewFlex().SetDirection(tview.FlexRow)
	actionArea.SetBackgroundColor(panelColor)
	actionArea.AddItem(nil, 1, 0, false)
	actionArea.AddItem(actions, 1, 0, true)
	panel.AddItem(actionArea, 2, 0, true)
	return panel, content, actions
}

type danmakuUserRegionRegistry struct {
	next  uint64
	items map[string]api.DanmakuMessage
	order []string
}

func newDanmakuUserRegionRegistry() *danmakuUserRegionRegistry {
	return &danmakuUserRegionRegistry{items: make(map[string]api.DanmakuMessage)}
}

func (registry *danmakuUserRegionRegistry) Reset() {
	if registry == nil {
		return
	}
	clear(registry.items)
	registry.order = registry.order[:0]
}

func (registry *danmakuUserRegionRegistry) Register(message api.DanmakuMessage) string {
	if registry == nil {
		return ""
	}
	registry.next++
	id := fmt.Sprintf("danmaku-user-%d", registry.next)
	registry.items[id] = message
	registry.order = append(registry.order, id)
	// TextView 最多保留 danmakuHistoryLimit 行，多留一倍余量可覆盖换行文本，
	// 同时避免长时间直播让点击区域索引无限增长。
	if len(registry.order) > danmakuHistoryLimit*2 {
		oldest := registry.order[0]
		registry.order = registry.order[1:]
		delete(registry.items, oldest)
	}
	return id
}

func (registry *danmakuUserRegionRegistry) Lookup(id string) (api.DanmakuMessage, bool) {
	if registry == nil {
		return api.DanmakuMessage{}, false
	}
	message, ok := registry.items[id]
	return message, ok
}

func appendDanmakuEvent(chat *tview.TextView, event api.DanmakuEvent, prependLineBreak bool, registries ...*danmakuUserRegionRegistry) {
	if event.Kind == api.DanmakuEventOnline || event.Kind == api.DanmakuEventConnected {
		return
	}
	message := event.Message
	username := strings.TrimSpace(message.Username)
	if username == "" {
		username = "匿名用户"
	}
	prefixColor := accentColor
	switch event.Kind {
	case api.DanmakuEventGift:
		prefixColor = tcell.NewHexColor(0xd68a4b)
	case api.DanmakuEventSuperChat:
		prefixColor = tcell.NewHexColor(0xd97706)
	case api.DanmakuEventGuard:
		prefixColor = accentActiveColor
	case api.DanmakuEventWarning:
		prefixColor = errorColor
	case api.DanmakuEventSystem:
		prefixColor = mutedColor
	}
	stamp := message.Timestamp
	if stamp.IsZero() {
		stamp = time.Now()
	}
	separator := ""
	if prependLineBreak {
		separator = "\n"
	}
	styledText := func(text string, color tcell.Color, bold bool) string {
		if noColor {
			if bold {
				return "[::b]" + text + "[-:-:-]"
			}
			return text
		}
		if bold {
			return fmt.Sprintf("[%s::b]%s[-:-:-]", color.String(), text)
		}
		return fmt.Sprintf("[%s]%s[-]", color.String(), text)
	}
	stampText := styledText(stamp.Format("15:04:05"), mutedColor, false)
	usernameText := tview.Escape(username)
	if event.Kind != api.DanmakuEventWarning {
		usernameText = styledText(usernameText, prefixColor, true)
	}
	if len(registries) > 0 && registries[0] != nil && event.Kind != api.DanmakuEventWarning {
		if regionID := registries[0].Register(message); regionID != "" {
			usernameText = fmt.Sprintf("[\"%s\"]%s[\"\"]", regionID, usernameText)
		}
	}

	var line string
	switch event.Kind {
	case api.DanmakuEventSuperChat:
		priceLabel := fmt.Sprintf("醒目留言 ¥%d", message.Price)
		if message.Price <= 0 {
			priceLabel = "醒目留言"
		}
		line = fmt.Sprintf("%s%s %s %s：%s", separator, stampText, styledText("["+priceLabel+"]", prefixColor, true), usernameText, tview.Escape(strings.TrimSpace(message.Text)))
	case api.DanmakuEventGuard:
		line = fmt.Sprintf("%s%s %s %s %s", separator, stampText, styledText("[大航海]", prefixColor, true), usernameText, tview.Escape(strings.TrimSpace(message.Text)))
	case api.DanmakuEventWarning:
		line = fmt.Sprintf("%s%s %s", separator, stampText, styledText("⚠ [超管警告] "+tview.Escape(strings.TrimSpace(message.Text)), errorColor, true))
	case api.DanmakuEventGift:
		line = fmt.Sprintf("%s%s %s %s %s", separator, stampText, styledText("[礼物]", prefixColor, true), usernameText, tview.Escape(strings.TrimSpace(message.Text)))
	default:
		line = fmt.Sprintf("%s%s %s：%s", separator, stampText, usernameText, tview.Escape(strings.TrimSpace(message.Text)))
	}
	_, _ = chat.Write([]byte(line))
	chat.ScrollToEnd()
}

func profilePointer(profile api.UserProfile, err error) *api.UserProfile {
	if err != nil {
		return nil
	}
	return &profile
}

func canFollowDanmakuUser(message api.DanmakuMessage, profile *api.UserProfile) bool {
	return profile != nil &&
		!profile.IsSelf &&
		!profile.IsFollowing &&
		strings.TrimSpace(message.UserID) != ""
}

func prependDanmakuMention(draft, username string, maxLength int) (string, bool) {
	username = strings.TrimSpace(username)
	if username == "" {
		return draft, false
	}
	mention := "@" + username + " "
	if strings.HasPrefix(draft, mention) {
		return draft, true
	}
	text := mention + draft
	return text, maxLength <= 0 || utf8.RuneCountInString(text) <= maxLength
}

func danmakuUserCardHeight(text string, width int) int {
	// 浮窗边框占 2 列，正文左右各留 2 列，底部按钮、上下留白和边框共 5 行。
	contentWidth := max(width-6, 1)
	rows := 0
	for _, line := range strings.Split(text, "\n") {
		lineWidth := tview.TaggedStringWidth(line)
		rows += max(1, (lineWidth+contentWidth-1)/contentWidth)
	}
	return min(max(rows+5, 9), 24)
}

func formatDanmakuUserCard(message api.DanmakuMessage, profile *api.UserProfile, loading bool, queryErr error) string {
	username := strings.TrimSpace(message.Username)
	if username == "" {
		username = "匿名用户"
	}
	userID := strings.TrimSpace(message.UserID)
	header := fmt.Sprintf("[%s::b]%s[-:-:-]", accentColor.String(), tview.Escape(username))
	if userID != "" {
		header += fmt.Sprintf("  [%s]UID %s[-]", mutedColor.String(), tview.Escape(userID))
	}
	lines := []string{header}
	roomDetails := make([]string, 0, 3)
	identities := make([]string, 0, 3)
	if message.IsAdmin {
		identities = append(identities, "房管")
	}
	if message.IsMystery {
		identities = append(identities, "神秘人")
	}
	if guard := onlineGuardLabel(message.GuardLevel); guard != "" {
		identities = append(identities, guard)
	}
	if len(identities) > 0 {
		roomDetails = append(roomDetails, fmt.Sprintf("[%s]身份[-]  %s", mutedColor.String(), strings.Join(identities, " · ")))
	}
	if medal := strings.TrimSpace(message.MedalName); medal != "" {
		if message.MedalLevel > 0 {
			medal += fmt.Sprintf(" Lv.%d", message.MedalLevel)
		}
		roomDetails = append(roomDetails, fmt.Sprintf("[%s]粉丝牌[-]  %s", mutedColor.String(), tview.Escape(medal)))
	}
	levels := make([]string, 0, 2)
	if message.UserLevel > 0 {
		levels = append(levels, fmt.Sprintf("弹幕 UL %d", message.UserLevel))
	}
	if message.WealthLevel > 0 {
		levels = append(levels, fmt.Sprintf("财富 Lv.%d", message.WealthLevel))
	}
	if len(levels) > 0 {
		roomDetails = append(roomDetails, fmt.Sprintf("[%s]等级[-]  %s", mutedColor.String(), strings.Join(levels, " · ")))
	}
	if len(roomDetails) > 0 {
		lines = append(lines, "", fmt.Sprintf("[%s::b]直播间身份[-:-:-]", tview.Styles.TitleColor.String()))
		lines = append(lines, roomDetails...)
	}
	lines = append(lines, "", fmt.Sprintf("[%s::b]B 站公开资料[-:-:-]", tview.Styles.TitleColor.String()))
	switch {
	case loading:
		lines = append(lines, fmt.Sprintf("[%s]正在查询，仅在点击用户名时发起请求……[-]", mutedColor.String()))
	case queryErr != nil:
		detail := []rune(strings.TrimSpace(queryErr.Error()))
		if len(detail) > 100 {
			detail = append(detail[:100], '…')
		}
		lines = append(lines,
			fmt.Sprintf("[%s]公开资料暂不可用，已保留弹幕包信息[-]", errorColor.String()),
			fmt.Sprintf("[%s]%s[-]", mutedColor.String(), tview.Escape(string(detail))),
		)
	case profile != nil:
		if name := strings.TrimSpace(profile.Username); name != "" && name != username {
			lines = append(lines, fmt.Sprintf("[%s]主页[-]  %s", mutedColor.String(), tview.Escape(name)))
		}
		account := make([]string, 0, 2)
		if profile.Level > 0 {
			account = append(account, fmt.Sprintf("等级 Lv.%d", profile.Level))
		}
		if profile.VIP {
			label := strings.TrimSpace(profile.VIPLabel)
			if label == "" {
				label = "大会员"
			}
			account = append(account, tview.Escape(label))
		}
		if len(account) > 0 {
			lines = append(lines, fmt.Sprintf("[%s]账号[-]  %s", mutedColor.String(), strings.Join(account, " · ")))
		}
		lines = append(lines,
			fmt.Sprintf("[%s]数据[-]  关注 %d · 粉丝 %d", mutedColor.String(), profile.Following, profile.Followers),
			fmt.Sprintf("[%s]投稿[-]  视频 %d · 专栏 %d", mutedColor.String(), profile.ArchiveCount, profile.ArticleCount),
		)
		if !profile.IsSelf {
			relation := "未关注"
			if profile.IsFollowing {
				relation = "已关注"
			}
			lines = append(lines, fmt.Sprintf("[%s]关系[-]  %s", mutedColor.String(), relation))
		}
		if official := strings.TrimSpace(profile.Official); official != "" {
			lines = append(lines, fmt.Sprintf("[%s]认证[-]  %s", mutedColor.String(), tview.Escape(official)))
		}
		if signature := strings.Join(strings.Fields(profile.Signature), " "); signature != "" {
			lines = append(lines, fmt.Sprintf("[%s]签名[-]  %s", mutedColor.String(), tview.Escape(signature)))
		}
	default:
		lines = append(lines, fmt.Sprintf("[%s]弹幕包未提供可查询的 UID[-]", mutedColor.String()))
	}
	return strings.Join(lines, "\n")
}

type rankTabMode int

const (
	rankTabAudience rankTabMode = 0
	rankTabGuard    rankTabMode = 1
)

func renderOnlineRank(view *tview.TextView, snapshot liveDanmakuSnapshot, mode rankTabMode, registries ...*danmakuUserRegionRegistry) {
	var registry *danmakuUserRegionRegistry
	if len(registries) > 0 {
		registry = registries[0]
		registry.Reset()
	}

	audienceCount := ""
	if snapshot.viewerKnown {
		audienceCount = fmt.Sprintf(" %d", snapshot.viewerOnline)
	}
	guardCount := ""
	if snapshot.guardKnown {
		guardCount = fmt.Sprintf(" %d", snapshot.guardTotal)
	}

	var title string
	if mode == rankTabGuard {
		title = " 大航海 "
		if snapshot.guardKnown {
			title = fmt.Sprintf(" 大航海 · 共 %d 人 ", snapshot.guardTotal)
		}
	} else {
		title = " 房间观众 "
		if snapshot.viewerKnown {
			title = fmt.Sprintf(" 房间观众 · 在线 %d 人 ", snapshot.viewerOnline)
		}
	}
	view.SetTitle(title)

	var content strings.Builder
	// 顶部 Tab 标签（支持鼠标点击切换）
	if mode == rankTabAudience {
		fmt.Fprintf(&content, "[\"tab_audience\"][%s::b][● 观众%s][-:-:-][\"\"] [\"tab_guard\"][%s][○ 大航海%s][-:-:-][\"\"]\n", accentColor.String(), audienceCount, mutedColor.String(), guardCount)
	} else {
		fmt.Fprintf(&content, "[\"tab_audience\"][%s][○ 观众%s][-:-:-][\"\"] [\"tab_guard\"][%s::b][● 大航海%s][-:-:-][\"\"]\n", mutedColor.String(), audienceCount, accentColor.String(), guardCount)
	}

	if mode == rankTabGuard {
		if !snapshot.guardKnown && len(snapshot.guardMembers) == 0 {
			if snapshot.guardError != "" {
				content.WriteString("\n大航海信息暂不可用，正在重试……")
			} else {
				content.WriteString("\n正在获取大航海列表……")
			}
			view.SetText(content.String())
			return
		}
		if len(snapshot.guardMembers) == 0 {
			content.WriteString("\n当前直播间暂无大航海成员。")
			view.SetText(content.String())
			return
		}
		for index, member := range snapshot.guardMembers {
			content.WriteByte('\n')
			rank := member.Rank
			if rank <= 0 {
				rank = index + 1
			}
			username := tview.Escape(member.Username)
			if strings.TrimSpace(member.UserID) != "" && registry != nil {
				message := api.DanmakuMessage{
					UserID:     member.UserID,
					Username:   member.Username,
					GuardLevel: member.GuardLevel,
				}
				if regionID := registry.Register(message); regionID != "" {
					username = fmt.Sprintf("[\"%s\"][%s::b]%s[-:-:-][\"\"]", regionID, accentColor.String(), username)
				}
			}
			guardColor := accentColor
			if member.GuardLevel == 1 {
				guardColor = themeColor(tcell.NewHexColor(0xd97706)) // 总督
			} else if member.GuardLevel == 2 {
				guardColor = accentActiveColor // 提督
			}
			fmt.Fprintf(&content, "[%s]#%-2d[-] %s", mutedColor.String(), rank, username)
			if guard := onlineGuardLabel(member.GuardLevel); guard != "" {
				fmt.Fprintf(&content, " [%s]%s[-]", guardColor.String(), guard)
			}
			if member.IsAlive {
				fmt.Fprintf(&content, " [%s]在线[-]", accentColor.String())
			}
		}
		if snapshot.guardError != "" {
			fmt.Fprintf(&content, "\n\n[%s]大航海刷新失败，暂时显示上次结果。[-] ", mutedColor.String())
		}
	} else {
		if !snapshot.viewerKnown && len(snapshot.onlineRank) == 0 {
			if snapshot.onlineRankError != "" {
				content.WriteString("\n在线信息暂不可用，正在自动重试……")
			} else {
				content.WriteString("\n正在获取在线人数……")
			}
			view.SetText(content.String())
			return
		}
		if len(snapshot.onlineRank) == 0 {
			content.WriteString("\n当前高能榜暂无成员。")
			view.SetText(content.String())
			return
		}
		for index, member := range snapshot.onlineRank {
			content.WriteByte('\n')
			rank := member.Rank
			if rank <= 0 {
				rank = index + 1
			}
			username := tview.Escape(member.Username)
			if strings.TrimSpace(member.UserID) != "" && registry != nil {
				message := api.DanmakuMessage{
					UserID:     member.UserID,
					Username:   member.Username,
					GuardLevel: member.GuardLevel,
				}
				if regionID := registry.Register(message); regionID != "" {
					username = fmt.Sprintf("[\"%s\"][%s::b]%s[-:-:-][\"\"]", regionID, accentColor.String(), username)
				}
			}
			fmt.Fprintf(&content, "[%s]#%-2d[-] %s", mutedColor.String(), rank, username)
			if guard := onlineGuardLabel(member.GuardLevel); guard != "" {
				fmt.Fprintf(&content, " [%s]%s[-]", accentColor.String(), guard)
			}
			if member.Score > 0 {
				fmt.Fprintf(&content, " [%s]%d[-]", mutedColor.String(), member.Score)
			}
		}
		if snapshot.onlineRankError != "" {
			fmt.Fprintf(&content, "\n\n[%s]高能榜刷新失败，暂时显示上次结果。[-]", mutedColor.String())
		}
	}
	view.SetText(content.String())
	view.ScrollToBeginning()
}

func onlineGuardLabel(level int) string {
	switch level {
	case 1:
		return "总督"
	case 2:
		return "提督"
	case 3:
		return "舰长"
	default:
		return ""
	}
}

func renderDanmakuHistory(chat *tview.TextView, history []api.DanmakuEvent, registries ...*danmakuUserRegionRegistry) {
	var registry *danmakuUserRegionRegistry
	if len(registries) > 0 {
		registry = registries[0]
		registry.Reset()
	}
	chat.SetText("")
	if len(history) == 0 {
		return
	}
	for index, event := range history {
		appendDanmakuEvent(chat, event, index > 0, registry)
	}
	chat.ScrollToEnd()
}

// updateDanmakuHistory 只追加上次绘制后新增的事件。
// 清空历史或修订号不连续时退回完整渲染，保证页面状态仍然正确。
func updateDanmakuHistory(chat *tview.TextView, snapshot liveDanmakuSnapshot, renderedRevision uint64, registries ...*danmakuUserRegionRegistry) uint64 {
	var registry *danmakuUserRegionRegistry
	if len(registries) > 0 {
		registry = registries[0]
	}
	if len(snapshot.history) == 0 {
		if registry != nil {
			registry.Reset()
		}
		chat.SetText("")
		chat.ScrollToBeginning()
		return snapshot.historyRevision
	}
	if snapshot.historyRevision < renderedRevision {
		renderDanmakuHistory(chat, snapshot.history, registry)
		return snapshot.historyRevision
	}
	added := snapshot.historyRevision - renderedRevision
	if added == 0 {
		return renderedRevision
	}
	if added > uint64(len(snapshot.history)) {
		renderDanmakuHistory(chat, snapshot.history, registry)
		return snapshot.historyRevision
	}

	// 检查当前聊天框实际已渲染的行数。若历史长度与增量不吻合（例如存在连击原地修改已有消息），
	// 则完整重绘历史，确保连击数字在原地跳动更新，不产生重复多余的追加行。
	currentRendered := 0
	if txt := chat.GetText(true); txt != "" {
		currentRendered = strings.Count(txt, "\n") + 1
	}
	if len(snapshot.history) != currentRendered+int(added) {
		renderDanmakuHistory(chat, snapshot.history, registry)
		return snapshot.historyRevision
	}

	start := len(snapshot.history) - int(added)
	if start == 0 {
		chat.SetText("")
	}
	hasPrevious := start > 0
	for _, event := range snapshot.history[start:] {
		appendDanmakuEvent(chat, event, hasPrevious, registry)
		hasPrevious = true
	}
	return snapshot.historyRevision
}

// danmakuSnapshotConfirmsSend 只检查提交之后新增的普通弹幕。已知当前账号
// UID 时必须一致，旧格式消息没有 UID 时再以文本回显作为兼容确认。
func danmakuSnapshotConfirmsSend(snapshot liveDanmakuSnapshot, afterRevision uint64, message, currentUserID string) bool {
	message = strings.TrimSpace(message)
	if message == "" || snapshot.historyRevision <= afterRevision || len(snapshot.history) == 0 {
		return false
	}
	added := snapshot.historyRevision - afterRevision
	if added > uint64(len(snapshot.history)) {
		added = uint64(len(snapshot.history))
	}
	currentUserID = strings.TrimSpace(currentUserID)
	for index := len(snapshot.history) - int(added); index < len(snapshot.history); index++ {
		event := snapshot.history[index]
		if event.Kind != api.DanmakuEventMessage || strings.TrimSpace(event.Message.Text) != message {
			continue
		}
		eventUserID := strings.TrimSpace(event.Message.UserID)
		if currentUserID == "" || eventUserID == "" || eventUserID == currentUserID {
			return true
		}
	}
	return false
}

func formatDanmakuRetryDelay(delay time.Duration) string {
	return fmt.Sprintf("%d 秒", int(delay/time.Second))
}

func formatDanmakuEndpoint(endpoint string) string {
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err == nil && parsed.Host != "" {
		return parsed.Host
	}
	return strings.TrimSpace(endpoint)
}
