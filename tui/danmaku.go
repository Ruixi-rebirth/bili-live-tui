package tui

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"bili-live-tui/internal/api"
	streamruntime "bili-live-tui/internal/stream"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// RunDanmaku 将终端页面挂载到长期弹幕会话上。
// 离开页面不会关闭 WebSocket 或丢弃历史记录。
func RunDanmaku(ctx context.Context, session *LiveDanmakuSession, client *api.Client, roomID, sessdata, biliJCT string, healthLoader func() streamruntime.Health) (Navigation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	previousNoColor := noColor
	noColor = danmakuNoColor
	defer func() { noColor = previousNoColor }()
	applyTheme()
	app := tview.NewApplication().EnableMouse(true).EnablePaste(true).SetTitle("bili-live-tui")

	chat := tview.NewTextView()
	chat.SetScrollable(true)
	chat.SetMaxLines(danmakuHistoryLimit)
	chat.SetDynamicColors(true)
	chat.SetRegions(true)
	chat.SetWordWrap(true)
	chat.SetBackgroundColor(panelColor)
	chat.SetBorder(true)
	chat.SetBorderColor(tview.Styles.BorderColor)
	// 工作区标题已经命名页面；清空弹幕框标题，避免“弹幕互动”重复显示，同时保留边框。
	chat.SetTitle("")
	chat.SetTitleColor(tview.Styles.TitleColor)
	initialSessionSnapshot := session.snapshot()
	userRegions := newDanmakuUserRegionRegistry()
	renderDanmakuHistory(chat, initialSessionSnapshot.history, initialSessionSnapshot.placeholder, userRegions)

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
	onlineRankUserRegions := newDanmakuUserRegionRegistry()
	renderOnlineRank(onlineRank, initialSessionSnapshot, onlineRankUserRegions)
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
	var refreshManagementCapabilitiesUI func()
	roomManagerOpenPending := false
	var openRoomManagerWhenReady func()
	var reply *tview.InputField
	reply = tview.NewInputField().
		SetLabel("").
		SetPlaceholder("").
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
				sendStatus.SetText(fmt.Sprintf("未能获取弹幕字数上限，暂按 %d 字处理。", api.DefaultDanmakuMaxLength))
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
	go func() {
		requestCtx, cancelRequest := context.WithTimeout(streamCtx, 10*time.Second)
		defer cancelRequest()
		capabilities, err := client.GetRoomManagementCapabilities(requestCtx, roomID, sessdata, biliJCT)
		queueUI(func() {
			managementCapabilities = capabilities
			managementCapabilitiesReady = true
			managementCapabilitiesErr = err
			if refreshManagementCapabilitiesUI != nil {
				refreshManagementCapabilitiesUI()
			}
			if roomManagerOpenPending && openRoomManagerWhenReady != nil {
				roomManagerOpenPending = false
				openRoomManagerWhenReady()
			}
		})
	}()
	clearChat := func() {
		session.ClearHistory()
		sentCount = 0
		sendStatus.SetText("已清空本地弹幕记录。")
	}
	sending := false
	send := func() {
		if !limitReady {
			sendStatus.SetText("正在获取弹幕字数上限，请稍候……")
			return
		}
		if sending {
			sendStatus.SetText("上一条弹幕仍在发送，请稍候……")
			return
		}
		message := strings.TrimSpace(reply.GetText())
		if message == "" {
			sendStatus.SetText("内容不能为空。")
			return
		}
		sending = true
		sendStatus.SetText("正在发送弹幕……")
		go func() {
			requestCtx, cancelRequest := context.WithTimeout(streamCtx, 12*time.Second)
			defer cancelRequest()
			err := client.SendDanmakuWithLimit(requestCtx, roomID, sessdata, biliJCT, message, int(danmakuMaxLength.Load()))
			queueUI(func() {
				sending = false
				if err != nil {
					sendStatus.SetText("发送失败，内容已保留：" + tview.Escape(err.Error()))
					return
				}
				// 不要清除请求发送期间用户输入的新草稿，只清除 B 站实际接受的原文本。
				if strings.TrimSpace(reply.GetText()) == message {
					reply.SetText("")
				}
				sentCount++
				// 成功消息会通过 WebSocket 以账号真实用户名返回，不再追加本地“我”行，
				// 否则每条弹幕都会显示两次。
				sendStatus.SetText(fmt.Sprintf("第 %d 条弹幕发送成功。", sentCount))
			})
		}()
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
		app.SetFocus(reply)
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
	managementActionLabel := ""
	closeManagementConfirm := func() {
		managementConfirmVisible = false
		pendingManagementAction = nil
		pages.HidePage("management-confirm")
		pages.SendToFront("user-management")
		app.SetFocus(managementMenu)
	}
	runManagementAction := func() {
		action := pendingManagementAction
		label := managementActionLabel
		managementConfirmVisible = false
		managementVisible = false
		muteDurationVisible = false
		pendingManagementAction = nil
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
	openManagementConfirm := func(label, warning string, action func(context.Context) error) {
		managementActionLabel = label
		pendingManagementAction = action
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
		targetAdminLevel := danmakuTargetAdminLevel(message)
		username := strings.TrimSpace(message.Username)
		if username == "" {
			username = message.UserID
		}
		addAction := func(label, detail string, selected func()) {
			managementMenu.AddItem(label, detail, 0, selected)
		}
		if managementCapabilities.CanMute(targetAdminLevel) {
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
		if managementCapabilities.CanBlacklist(targetAdminLevel) && strings.TrimSpace(managementCapabilities.AnchorID) != "" {
			addAction("加入直播间黑名单", "将无法观看、发言和送礼", func() {
				openManagementConfirm("拉黑 "+username, "确定将 "+username+" 加入直播间黑名单吗？\n\n对方会被移出直播间，无法观看和互动，当前排行及贡献会被清除。", func(requestCtx context.Context) error {
					return client.BlacklistRoomUser(requestCtx, managementCapabilities.AnchorID, message.UserID, sessdata, biliJCT)
				})
			})
		}
		if managementCapabilities.IsAnchor {
			if message.IsAdmin {
				addAction("撤销房管", "撤销该用户的直播间管理权限", func() {
					openManagementConfirm("撤销房管 "+username, "确定撤销 "+username+" 的房管权限吗？", func(requestCtx context.Context) error {
						return client.DismissRoomAdmin(requestCtx, message.UserID, sessdata, biliJCT)
					})
				})
			} else {
				addAction("设为房管", "可禁言用户和设置屏蔽词", func() {
					openManagementConfirm("任命房管 "+username, "确定任命 "+username+" 为房管吗？", func(requestCtx context.Context) error {
						return client.AppointRoomAdmin(requestCtx, message.UserID, 1, sessdata, biliJCT)
					})
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

	roomManagerList := newDanmakuManagementList("")
	roomManagerList.SetBorder(false)
	roomManagerList.SetBorderPadding(0, 0, 1, 1)
	roomManagerList.ShowSecondaryText(false)
	roomManagerList.SetSelectedFocusOnly(true)
	roomManagerTabs := tview.NewTextView().SetDynamicColors(true).SetRegions(true).SetTextAlign(tview.AlignCenter)
	roomManagerTabs.SetBackgroundColor(panelColor)
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
	roomManagerInput := newDanmakuManagementForm("")
	roomManagerInput.SetItemPadding(1)
	roomManagerInputCard := newFloatingOverlay(roomManagerInput, 56, 9)
	roomManagerBackButton := newActionButton("返回弹幕 (Esc)", nil)
	roomManagerActionBar := centeredActionBar([]*tview.Button{roomManagerBackButton})
	roomManagerListLayout := tview.NewGrid().SetColumns(-1, -12, -1)
	roomManagerListLayout.SetBackgroundColor(panelColor)
	roomManagerListLayout.AddItem(roomManagerList, 0, 1, 1, 1, 0, 0, true)
	roomManagerContent := tview.NewPages()
	roomManagerContent.SetBackgroundColor(panelColor)
	roomManagerContent.AddPage("list", roomManagerListLayout, true, true)
	roomManagerContent.AddPage("input", roomManagerInputCard, true, false)
	roomManagerContent.AddPage("empty", roomManagerEmptyCenter, true, false)
	roomManagerPanel := tview.NewFlex().SetDirection(tview.FlexRow)
	roomManagerPanel.SetBackgroundColor(panelColor)
	roomManagerPanel.SetBorder(true)
	roomManagerPanel.SetBorderColor(tview.Styles.BorderColor)
	roomManagerPanel.AddItem(roomManagerTabs, 1, 0, false)
	roomManagerPanel.AddItem(roomManagerSection, 1, 0, false)
	roomManagerPanel.AddItem(roomManagerNotice, 1, 0, false)
	roomManagerPanel.AddItem(roomManagerContent, 0, 1, true)
	roomManagerPanel.AddItem(roomManagerActionBar, 1, 0, true)
	roomManagerPageFooter := pageFooter("1~5 切换栏目 · ←/→ 切换 · ↑/↓ 选择 · Enter 操作 · r 刷新 · [ / ] 翻页 · Esc 返回")
	roomManagerPage := workspacePage(
		workspaceHeader("房间管理工作台"),
		roomManagerPanel,
		roomManagerPageFooter,
	)
	roomManagerConfirm := styleModal(tview.NewModal())
	roomManagerVisible := false
	roomManagerConfirmVisible := false
	roomManagerInputVisible := false
	roomManagerEmptyVisible := false
	roomManagerGeneration := uint64(0)
	type roomManagerTab struct {
		label string
		load  func()
	}
	roomManagerTabsAvailable := make([]roomManagerTab, 0, 5)
	roomManagerTabIndex := 0
	roomAdminPage := 1
	mutedUserPage := 1
	blacklistPage := 1
	roomAdminTotalPages := 1
	blacklistTotalPages := 1
	mutedHasNextPage := false
	var roomManagerPrevPage func()
	var roomManagerNextPage func()
	var showRoomManagerRoot func()
	var loadRoomAdmins func()
	var loadMutedUsers func()
	var loadBlacklistedUsers func()
	var loadShieldKeywords func()
	var loadRoomSilent func()
	var selectRoomManagerTab func(int)
	var roomManagerReload func()
	var pendingRoomManagerAction func(context.Context) error
	pendingRoomManagerLabel := ""
	setRoomManagerNotice := func(message string, failed bool) {
		message = strings.TrimSpace(message)
		if message == "" {
			roomManagerNotice.SetText("")
			return
		}
		color := accentActiveColor
		if failed {
			color = errorColor
		}
		roomManagerNotice.SetText("[" + color.String() + "]" + tview.Escape(message) + "[-]")
	}
	addRoomManagerItem := func(title, detail, action string, shortcut rune, selected func()) {
		roomManagerList.AddItem(formatRoomManagerRow(title, detail, action), "", shortcut, selected)
	}
	showRoomManagerListView := func() {
		roomManagerEmptyVisible = false
		roomManagerInputVisible = false
		roomManagerContent.SwitchToPage("list")
		app.SetFocus(roomManagerList)
	}
	showRoomManagerEmptyView := func(message string) {
		roomManagerEmptyVisible = true
		roomManagerInputVisible = false
		emptyContent := fmt.Sprintf("[%s]•  •  •[-]\n[%s::b]%s[-:-:-]\n[%s]可使用上方标签切换其它栏目，或按 r 刷新[-]",
			accentColor.String(),
			tview.Styles.PrimaryTextColor.String(),
			tview.Escape(message),
			mutedColor.String(),
		)
		roomManagerEmpty.SetText(emptyContent)
		roomManagerContent.SwitchToPage("empty")
		app.SetFocus(roomManagerBackButton)
	}
	closeRoomManager := func() {
		roomManagerGeneration++
		roomManagerVisible = false
		roomManagerConfirmVisible = false
		roomManagerInputVisible = false
		pendingRoomManagerAction = nil
		setRoomManagerNotice("", false)
		pages.HidePage("room-manager-confirm")
		pages.HidePage("room-manager")
		app.SetFocus(reply)
	}
	roomManagerBackButton.SetSelectedFunc(closeRoomManager)
	showRoomManagerLoading := func(title string) uint64 {
		roomManagerGeneration++
		generation := roomManagerGeneration
		roomManagerSection.SetText("[" + mutedColor.String() + "]" + tview.Escape(title) + " · 正在加载[-]")
		showRoomManagerEmptyView("正在加载……")
		return generation
	}
	showRoomManagerError := func(generation uint64, title string, err error, retry func()) {
		if !roomManagerVisible || generation != roomManagerGeneration {
			return
		}
		showRoomManagerListView()
		roomManagerList.Clear()
		roomManagerSection.SetText("[" + errorColor.String() + "]" + tview.Escape(title) + " · 加载失败[-]")
		addRoomManagerItem("加载失败", compactDanmakuManagementError(err), "", 0, nil)
		addRoomManagerItem("重试", "", "重试", 'r', retry)
		roomManagerList.SetCurrentItem(1)
	}
	runRoomManagerAction := func() {
		action := pendingRoomManagerAction
		label := pendingRoomManagerLabel
		pendingRoomManagerAction = nil
		roomManagerConfirmVisible = false
		pages.HidePage("room-manager-confirm")
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
		app.SetFocus(roomManagerList)
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
	closeRoomManagerInput := func() {
		roomManagerInputVisible = false
		roomManagerContent.SwitchToPage("list")
		roomManagerSection.SetText(roomManagerSectionBeforeInput)
		setRoomManagerNotice("", false)
		app.SetFocus(roomManagerList)
	}
	roomManagerInput.SetCancelFunc(closeRoomManagerInput)
	openRoomManagerInput := func(title, label, initial string, maxLength int, validate func(string) error, submitted func(string)) {
		roomManagerInput.Clear(true)
		roomManagerSectionBeforeInput = roomManagerSection.GetText(false)
		roomManagerSection.SetText("[" + tview.Styles.TitleColor.String() + "]" + tview.Escape(title) + "[-]")
		setRoomManagerNotice("", false)
		roomManagerInput.SetTitle(" " + title + " ")
		roomManagerInput.AddInputField(label, initial, maxLength, nil, func(string) {
			setRoomManagerNotice("", false)
		})
		roomManagerInput.AddButton("保存", func() {
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
	roomManagerPrevPage = func() {
		if len(roomManagerTabsAvailable) == 0 || roomManagerTabIndex >= len(roomManagerTabsAvailable) {
			return
		}
		switch roomManagerTabsAvailable[roomManagerTabIndex].label {
		case "房管":
			if roomAdminPage > 1 {
				roomAdminPage--
				loadRoomAdmins()
			}
		case "禁言":
			if mutedUserPage > 1 {
				mutedUserPage--
				loadMutedUsers()
			}
		case "黑名单":
			if blacklistPage > 1 {
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
				roomAdminPage++
				loadRoomAdmins()
			}
		case "禁言":
			if mutedHasNextPage {
				mutedUserPage++
				loadMutedUsers()
			}
		case "黑名单":
			if blacklistPage < blacklistTotalPages {
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
		if managementCapabilities.IsAnchor || managementCapabilities.IsAdmin {
			roomManagerTabsAvailable = append(roomManagerTabsAvailable, roomManagerTab{label: "屏蔽词", load: loadShieldKeywords})
		}
		if managementCapabilities.IsAnchor {
			roomManagerTabsAvailable = append(roomManagerTabsAvailable, roomManagerTab{label: "全局禁言", load: loadRoomSilent})
		}
		if roomManagerTabIndex >= len(roomManagerTabsAvailable) {
			roomManagerTabIndex = 0
		}
		selectRoomManagerTab(roomManagerTabIndex)
	}
	selectRoomManagerTab = func(index int) {
		if len(roomManagerTabsAvailable) == 0 {
			closeRoomManager()
			return
		}
		index = (index%len(roomManagerTabsAvailable) + len(roomManagerTabsAvailable)) % len(roomManagerTabsAvailable)
		roomManagerTabIndex = index
		var tabs strings.Builder
		for tabIndex, tab := range roomManagerTabsAvailable {
			if tabIndex > 0 {
				tabs.WriteString("   ")
			}
			regionID := fmt.Sprintf("room-manager-tab-%d", tabIndex)
			tabNumber := tabIndex + 1
			if tabIndex == index {
				fmt.Fprintf(&tabs, "[\"%s\"][%s:%s:b] %d %s [-:-:-][\"\"]", regionID, buttonActiveTextColor.String(), accentActiveColor.String(), tabNumber, tview.Escape(tab.label))
			} else {
				fmt.Fprintf(&tabs, "[\"%s\"][%s]%d [%s]%s[-:-:-][\"\"]", regionID, accentColor.String(), tabNumber, tview.Styles.PrimaryTextColor.String(), tview.Escape(tab.label))
			}
		}
		roomManagerTabs.SetText(tabs.String())
		roomManagerContent.SwitchToPage("list")
		roomManagerInputVisible = false
		setRoomManagerNotice("", false)
		tab := roomManagerTabsAvailable[index]
		roomManagerReload = tab.load
		tab.load()
		app.SetFocus(roomManagerList)
	}
	roomManagerTabs.SetHighlightedFunc(func(added, _, _ []string) {
		if len(added) == 0 {
			return
		}
		roomManagerTabs.Highlight()
		indexText := strings.TrimPrefix(added[0], "room-manager-tab-")
		index, err := strconv.Atoi(indexText)
		if err == nil && roomManagerVisible && !roomManagerConfirmVisible && !roomManagerInputVisible {
			selectRoomManagerTab(index)
		}
	})
	loadRoomAdmins = func() {
		generation := showRoomManagerLoading("房管")
		roomManagerReload = loadRoomAdmins
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
				showRoomManagerListView()
				roomManagerList.Clear()
				adminCount := fmt.Sprintf("第 %d 页", roomAdminPage)
				if result.MaxCount > 0 {
					adminCount += fmt.Sprintf(" · 最多 %d 人", result.MaxCount)
				}
				roomManagerSection.SetText(fmt.Sprintf("[%s]房管 · %s[-]", mutedColor.String(), adminCount))
				if len(result.Items) == 0 && roomAdminPage == 1 {
					showRoomManagerEmptyView("暂无房管")
					return
				}
				for _, admin := range result.Items {
					admin := admin
					level := "普通房管"
					if admin.Level == 2 {
						level = "高级房管"
					}
					addRoomManagerItem(displayDanmakuManagedUser(admin.Username, admin.UserID), level+managementTimestamp(admin.AppointedAt), "撤销", 0, func() {
						openRoomManagerConfirm("撤销房管", "确定撤销 "+displayDanmakuManagedUser(admin.Username, admin.UserID)+" 的房管权限吗？", func(actionCtx context.Context) error {
							return client.DismissRoomAdmin(actionCtx, admin.UserID, sessdata, biliJCT)
						})
					})
				}
				roomAdminTotalPages = result.TotalPages
				if roomAdminTotalPages < 1 {
					roomAdminTotalPages = 1
				}
				if roomAdminPage > 1 {
					addRoomManagerItem("上一页", "", "", 0, func() {
						roomAdminPage--
						loadRoomAdmins()
					})
				}
				if result.TotalPages > roomAdminPage {
					addRoomManagerItem("下一页", "", "", 0, func() {
						roomAdminPage++
						loadRoomAdmins()
					})
				}
				roomManagerList.SetCurrentItem(0)
			})
		}()
	}
	loadMutedUsers = func() {
		generation := showRoomManagerLoading("禁言名单")
		roomManagerReload = loadMutedUsers
		go func() {
			requestCtx, cancelRequest := context.WithTimeout(streamCtx, 10*time.Second)
			defer cancelRequest()
			items, err := client.GetMutedRoomUsers(requestCtx, roomID, mutedUserPage, sessdata, biliJCT)
			queueUI(func() {
				if err != nil {
					showRoomManagerError(generation, "禁言名单", err, loadMutedUsers)
					return
				}
				if !roomManagerVisible || generation != roomManagerGeneration {
					return
				}
				showRoomManagerListView()
				roomManagerList.Clear()
				roomManagerSection.SetText(fmt.Sprintf("[%s]禁言名单 · 第 %d 页[-]", mutedColor.String(), mutedUserPage))
				if len(items) == 0 && mutedUserPage == 1 {
					showRoomManagerEmptyView("暂无被禁言用户")
					return
				}
				for _, item := range items {
					item := item
					detail := "到期 " + fallbackDanmakuManagementText(item.ExpiresAt, "未知")
					if item.OperatorName != "" {
						detail += " · 操作者 " + item.OperatorName
					}
					if managementCapabilities.CanMute(item.AdminLevel) {
						addRoomManagerItem(displayDanmakuManagedUser(item.Username, item.UserID), detail, "解除", 0, func() {
							openRoomManagerConfirm("解除禁言", "确定解除 "+displayDanmakuManagedUser(item.Username, item.UserID)+" 的禁言吗？", func(actionCtx context.Context) error {
								return client.UnmuteRoomUser(actionCtx, roomID, item.UserID, sessdata, biliJCT)
							})
						})
					} else {
						addRoomManagerItem(displayDanmakuManagedUser(item.Username, item.UserID), detail+" · 无权操作", "", 0, nil)
					}
				}
				mutedHasNextPage = len(items) > 0
				if mutedUserPage > 1 {
					addRoomManagerItem("上一页", "", "", 0, func() {
						mutedUserPage--
						loadMutedUsers()
					})
				}
				if len(items) > 0 {
					addRoomManagerItem("下一页", "", "", 0, func() {
						mutedUserPage++
						loadMutedUsers()
					})
				}
				roomManagerList.SetCurrentItem(0)
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
				showRoomManagerListView()
				roomManagerList.Clear()
				roomManagerSection.SetText(fmt.Sprintf("[%s]直播间黑名单 · 第 %d 页[-]", mutedColor.String(), blacklistPage))
				if len(result.Items) == 0 && blacklistPage == 1 {
					showRoomManagerEmptyView("黑名单为空")
					return
				}
				for _, item := range result.Items {
					item := item
					addRoomManagerItem(displayDanmakuManagedUser(item.Username, item.UserID), "加入 "+fallbackDanmakuManagementText(item.CreatedAt, "时间未知"), "移出", 0, func() {
						openRoomManagerConfirm("移出黑名单", "确定将 "+displayDanmakuManagedUser(item.Username, item.UserID)+" 移出直播间黑名单吗？", func(actionCtx context.Context) error {
							return client.UnblacklistRoomUser(actionCtx, managementCapabilities.AnchorID, item.UserID, sessdata, biliJCT)
						})
					})
				}
				blacklistTotalPages = result.TotalPages
				if blacklistTotalPages < 1 && result.Total > 0 {
					blacklistTotalPages = (result.Total + 49) / 50
				}
				if blacklistTotalPages < 1 {
					blacklistTotalPages = 1
				}
				if blacklistPage > 1 {
					addRoomManagerItem("上一页", "", "", 0, func() {
						blacklistPage--
						loadBlacklistedUsers()
					})
				}
				if len(result.Items) == 50 || result.Total > blacklistPage*50 || result.TotalPages > blacklistPage {
					addRoomManagerItem("下一页", "", "", 0, func() {
						blacklistPage++
						loadBlacklistedUsers()
					})
				}
				roomManagerList.SetCurrentItem(0)
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
				showRoomManagerListView()
				roomManagerList.Clear()
				count := fmt.Sprintf("%d", len(state.Keywords))
				if state.MaxCount > 0 {
					count = fmt.Sprintf("%d/%d", len(state.Keywords), state.MaxCount)
				}
				roomManagerSection.SetText("[" + mutedColor.String() + "]已使用 " + count + "[-]")
				if state.MaxCount <= 0 || len(state.Keywords) < state.MaxCount {
					addRoomManagerItem("添加屏蔽词", "最多 15 个字", "添加", 'a', func() {
						openRoomManagerInput("添加屏蔽词", "屏蔽词 ", "", 15, func(keyword string) error {
							if strings.TrimSpace(keyword) == "" {
								return fmt.Errorf("屏蔽词不能为空。")
							}
							if utf8.RuneCountInString(strings.TrimSpace(keyword)) > 15 {
								return fmt.Errorf("屏蔽词最多 15 个字。")
							}
							return nil
						}, func(keyword string) {
							pendingRoomManagerLabel = "添加屏蔽词"
							pendingRoomManagerAction = func(actionCtx context.Context) error {
								return client.AddRoomShieldKeyword(actionCtx, roomID, keyword, sessdata, biliJCT)
							}
							runRoomManagerAction()
						})
					})
				}
				for _, keyword := range state.Keywords {
					keyword := keyword
					addRoomManagerItem(keyword, "", "删除", 0, func() {
						openRoomManagerConfirm("删除屏蔽词", "确定删除屏蔽词“"+keyword+"”吗？", func(actionCtx context.Context) error {
							return client.DeleteRoomShieldKeyword(actionCtx, roomID, keyword, sessdata, biliJCT)
						})
					})
				}
				roomManagerList.SetCurrentItem(0)
			})
		}()
	}
	loadRoomSilent = func() {
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
				showRoomManagerListView()
				roomManagerList.Clear()
				if state.Enabled {
					roomManagerSection.SetText("[" + accentActiveColor.String() + "]当前已开启 · " + tview.Escape(formatRoomSilentState(state)) + "[-]")
					addRoomManagerItem("关闭全局禁言", "恢复正常发言", "关闭", 0, func() {
						openRoomManagerConfirm("关闭全局禁言", "确定关闭直播间全局禁言吗？", func(actionCtx context.Context) error {
							return client.SetRoomSilentState(actionCtx, roomID, api.RoomSilentOff, 1, 0, sessdata, biliJCT)
						})
					})
				} else {
					roomManagerSection.SetText("[" + mutedColor.String() + "]当前未开启 · 选择一种禁言范围[-]")
					addSilentChoice := func(label, detail, audience string, level int) {
						addRoomManagerItem(label, detail, "设置", 0, func() {
							openRoomManagerConfirm("开启全局禁言", "确定开启“"+label+"”全局禁言吗？", func(actionCtx context.Context) error {
								return client.SetRoomSilentState(actionCtx, roomID, audience, level, 0, sessdata, biliJCT)
							})
						})
					}
					addSilentChoice("全员", "除房管外均不可发言", api.RoomSilentAll, 1)
					addSilentChoice("非本房粉丝", "未关注本直播间的用户不可发言", api.RoomSilentNonFans, 1)
					addRoomManagerItem("荣耀等级", "设置低于该等级的用户不可发言", "设置", 0, func() {
						openRoomManagerInput("荣耀等级禁言", "等级 1-80 ", "1", 2, func(value string) error {
							_, err := parseDanmakuManagementLevel(value, 80)
							return err
						}, func(value string) {
							level, _ := parseDanmakuManagementLevel(value, 80)
							openRoomManagerConfirm("开启全局禁言", fmt.Sprintf("确定禁止荣耀等级低于 %d 的用户发言吗？", level), func(actionCtx context.Context) error {
								return client.SetRoomSilentState(actionCtx, roomID, api.RoomSilentWealth, level, 0, sessdata, biliJCT)
							})
						})
					})
					addRoomManagerItem("粉丝勋章", "设置低于或没有该等级粉丝牌的用户不可发言", "设置", 0, func() {
						openRoomManagerInput("粉丝勋章禁言", "等级 1-120 ", "1", 3, func(value string) error {
							_, err := parseDanmakuManagementLevel(value, 120)
							return err
						}, func(value string) {
							level, _ := parseDanmakuManagementLevel(value, 120)
							openRoomManagerConfirm("开启全局禁言", fmt.Sprintf("确定禁止粉丝牌等级低于 %d 的用户发言吗？", level), func(actionCtx context.Context) error {
								return client.SetRoomSilentState(actionCtx, roomID, api.RoomSilentMedal, level, 0, sessdata, biliJCT)
							})
						})
					})
					addSilentChoice("除房管以外的观众", "仅主播和房管可以发言", api.RoomSilentNonMember, 1)
				}
				roomManagerList.SetCurrentItem(0)
			})
		}()
	}
	openRoomManager := func() {
		switch {
		case !managementCapabilitiesReady:
			roomManagerOpenPending = true
			sendStatus.SetText("正在获取房间管理权限，请稍候……")
			return
		case managementCapabilitiesErr != nil:
			sendStatus.SetText("获取房间管理权限失败：" + tview.Escape(managementCapabilitiesErr.Error()))
			return
		case !managementCapabilities.IsAnchor && !managementCapabilities.IsAdmin:
			sendStatus.SetText("当前账号不是本直播间的主播或房管。")
			return
		}
		roomManagerVisible = true
		showRoomManagerRoot()
		pages.ShowPage("room-manager")
		pages.SendToFront("room-manager")
		app.SetFocus(roomManagerList)
	}
	openRoomManagerWhenReady = openRoomManager
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
			userCardActions.AddButton("管理", func() { openUserManagement(message, profile) })
		}
		userCardActions.AddButton("关闭", closeUserCard)
		// 打开资料卡以及异步刷新按钮后，默认都停在最安全的“关闭”上。
		focusUserCardAction(userCardActions.GetButtonCount() - 1)
	}
	openUserCard := func(message api.DanmakuMessage) {
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
		message, ok := onlineRankUserRegions.Lookup(added[0])
		onlineRank.Highlight()
		if ok {
			openUserCard(message)
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
	// 弹幕工作区不显示操作按钮。Enter 在输入框中发送，Esc/Ctrl+H 仍是全局导航快捷键。
	body.AddItem(reply, 3, 0, true)
	root := workspacePage(
		workspaceHeader("弹幕互动"),
		body,
		pageFooter("点击用户名看资料　Enter发送　Ctrl+M/Alt+M房间管理　Alt+H/Ctrl+H概览　Ctrl+L清空　Ctrl+U删输入　Esc下播　Ctrl+C退出"),
	)
	pages.AddPage("main", root, true, true)
	pages.AddPage("user-card", userCardOverlay, true, false)
	pages.AddPage("user-management", managementMenuOverlay, true, false)
	pages.AddPage("mute-duration", muteDurationOverlay, true, false)
	pages.AddPage("management-confirm", managementConfirm, true, false)
	pages.AddPage("room-manager", roomManagerPage, true, false)
	pages.AddPage("room-manager-confirm", roomManagerConfirm, true, false)
	pages.AddPage("confirm-stop", confirm, true, false)
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
					renderOnlineRank(onlineRank, snapshot, onlineRankUserRegions)
					if snapshot.historyRevision != renderedHistoryRevision {
						renderedHistoryRevision = updateDanmakuHistory(chat, snapshot, renderedHistoryRevision, userRegions)
					} else if len(snapshot.history) == 0 && chat.GetText(true) != snapshot.placeholder {
						chat.SetText(snapshot.placeholder)
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
		if roomManagerConfirmVisible {
			switch event.Key() {
			case tcell.KeyEscape:
				roomManagerConfirmVisible = false
				pendingRoomManagerAction = nil
				pages.HidePage("room-manager-confirm")
				pages.SendToFront("room-manager")
				app.SetFocus(roomManagerList)
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
				selectRoomManagerTab(roomManagerTabIndex - 1)
				return nil
			case event.Key() == tcell.KeyRight:
				selectRoomManagerTab(roomManagerTabIndex + 1)
				return nil
			case event.Key() == tcell.KeyTab || event.Key() == tcell.KeyBacktab:
				if roomManagerBackButton.HasFocus() && !roomManagerEmptyVisible {
					app.SetFocus(roomManagerList)
				} else {
					app.SetFocus(roomManagerBackButton)
				}
				return nil
			case event.Key() == tcell.KeyRune && event.Rune() >= '1' && event.Rune() <= '5':
				tabIdx := int(event.Rune() - '1')
				if tabIdx < len(roomManagerTabsAvailable) {
					selectRoomManagerTab(tabIdx)
					return nil
				}
			case event.Key() == tcell.KeyRune && (event.Rune() == 'r' || event.Rune() == 'R'):
				if roomManagerReload != nil {
					setRoomManagerNotice("正在刷新……", false)
					roomManagerReload()
					return nil
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

		switch {
		case matchesControlShortcut(event, tcell.KeyCtrlH, 'h') || matchesModifiedRuneShortcut(event, tcell.ModAlt, 'h'):
			// 在终端中，Backspace 通常与 Ctrl+H 等价（ASCII 8）。
			// 当输入框获得焦点时，放行该按键用于退格删除，避免误跳回房间概览。
			if reply.HasFocus() {
				if event.Key() == tcell.KeyCtrlH || event.Key() == tcell.KeyBackspace {
					return event
				}
				if event.Key() == tcell.KeyRune && (event.Rune() == '\b' || event.Rune() == 8) {
					return tcell.NewEventKey(tcell.KeyBackspace, 0, tcell.ModNone)
				}
			}
			navigation = NavigationHome
			stopApplication()
			return nil
		case matchesControlShortcut(event, tcell.KeyCtrlL, 'l'):
			clearChat()
			return nil
		case matchesRoomManagementShortcut(event):
			openRoomManager()
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
	list.SetSelectedStyle(tcell.StyleDefault.
		Background(accentActiveColor).
		Foreground(buttonActiveTextColor).
		Bold(true))
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
	form.SetButtonStyle(tcell.StyleDefault.Background(accentColor).Foreground(buttonTextColor))
	form.SetButtonActivatedStyle(tcell.StyleDefault.Background(accentActiveColor).Foreground(buttonActiveTextColor).Bold(true))
	form.SetBorder(true)
	form.SetBorderColor(tview.Styles.BorderColor)
	form.SetTitle(title)
	form.SetTitleColor(tview.Styles.TitleColor)
	form.SetBorderPadding(1, 0, 1, 1)
	return form
}

func formatRoomManagerRow(title, detail, action string) string {
	title = strings.TrimSpace(title)
	detail = strings.TrimSpace(detail)
	action = strings.TrimSpace(action)
	const titleColumnWidth = 22
	padding := 2
	if width := tview.TaggedStringWidth(title); width < titleColumnWidth {
		padding = titleColumnWidth - width
	}
	var row strings.Builder
	row.WriteString(title)
	if detail != "" {
		row.WriteString(strings.Repeat(" ", padding))
		row.WriteString(detail)
	}
	if action != "" {
		row.WriteString("  「")
		row.WriteString(action)
		row.WriteString("」")
	}
	return row.String()
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

func parseDanmakuManagementLevel(value string, maximum int) (int, error) {
	level, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || level < 1 || level > maximum {
		return 0, fmt.Errorf("等级必须是 1 到 %d 之间的整数。", maximum)
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

func danmakuTargetAdminLevel(message api.DanmakuMessage) int {
	if message.IsAdmin {
		// 弹幕包只标识“是房管”，不总是包含普通/高级等级。按最高等级
		// 处理，避免房管之间错误展示越权操作；主播不受此限制。
		return 2
	}
	return 0
}

func canManageDanmakuUser(capabilities api.RoomManagementCapabilities, message api.DanmakuMessage, profile *api.UserProfile) bool {
	userID := strings.TrimSpace(message.UserID)
	if userID == "" || userID == strings.TrimSpace(capabilities.UserID) || message.IsMystery || profile != nil && profile.IsSelf {
		return false
	}
	targetAdminLevel := danmakuTargetAdminLevel(message)
	return capabilities.CanMute(targetAdminLevel) ||
		capabilities.CanBlacklist(targetAdminLevel) ||
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
		unicode.ToLower(event.Rune()) == unicode.ToLower(letter)
}

func matchesRoomManagementShortcut(event *tcell.EventKey) bool {
	// Ctrl+M is only distinguishable from Enter when the terminal reports an
	// extended KeyRune event with the Ctrl modifier. Never match KeyCtrlM here:
	// tcell aliases it to KeyCR/KeyEnter on traditional terminals.
	return matchesModifiedRuneShortcut(event, tcell.ModCtrl, 'm') ||
		matchesModifiedRuneShortcut(event, tcell.ModAlt, 'm')
}

func formatDanmakuSessionStatus(snapshot liveDanmakuSnapshot) string {
	if !snapshot.onlineKnown || !strings.HasPrefix(snapshot.status, "弹幕已连接") {
		return snapshot.status
	}
	status := strings.TrimSuffix(snapshot.status, "，消息会实时显示。")
	for _, marker := range []string{" · 当前人气 ", " · 人气 "} {
		if index := strings.LastIndex(status, marker); index >= 0 {
			status = status[:index]
			break
		}
	}
	return fmt.Sprintf("%s · 当前人气 %d", status, snapshot.online)
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
	actions.SetButtonStyle(tcell.StyleDefault.
		Background(accentColor).
		Foreground(buttonTextColor))
	actions.SetButtonActivatedStyle(tcell.StyleDefault.
		Background(accentActiveColor).
		Foreground(buttonActiveTextColor).
		Bold(true))

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
	// TextView 最多保留 danmakuHistoryLimit 行；多留一倍余量可覆盖换行文本，
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
	usernameText := tview.Escape(username)
	if len(registries) > 0 && registries[0] != nil {
		if regionID := registries[0].Register(message); regionID != "" {
			usernameText = fmt.Sprintf("[\"%s\"][%s::b]%s[-:-:-][\"\"]", regionID, prefixColor.String(), usernameText)
		}
	}
	line := fmt.Sprintf("%s[%s]%s[-] %s：%s", separator, mutedColor.String(), stamp.Format("15:04:05"), usernameText, tview.Escape(strings.TrimSpace(message.Text)))
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
	// 浮窗边框占 2 列，正文左右各留 2 列；底部按钮、上下留白和边框共 5 行。
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

func renderOnlineRank(view *tview.TextView, snapshot liveDanmakuSnapshot, registries ...*danmakuUserRegionRegistry) {
	var registry *danmakuUserRegionRegistry
	if len(registries) > 0 {
		registry = registries[0]
		registry.Reset()
	}
	title := " 在线人数 "
	if snapshot.viewerKnown {
		title = fmt.Sprintf(" 在线 %d 人 ", snapshot.viewerOnline)
	}
	view.SetTitle(title)
	if !snapshot.viewerKnown && len(snapshot.onlineRank) == 0 {
		if snapshot.onlineRankError != "" {
			view.SetText("在线信息暂不可用，正在自动重试……")
		} else {
			view.SetText("正在获取在线人数……")
		}
		return
	}
	if len(snapshot.onlineRank) == 0 {
		view.SetText("当前高能榜暂无成员。")
		return
	}
	var content strings.Builder
	content.WriteString("[::b]高能榜[::-]")
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

func renderDanmakuHistory(chat *tview.TextView, history []api.DanmakuEvent, placeholder string, registries ...*danmakuUserRegionRegistry) {
	var registry *danmakuUserRegionRegistry
	if len(registries) > 0 {
		registry = registries[0]
		registry.Reset()
	}
	chat.SetText("")
	if len(history) == 0 {
		chat.SetText(placeholder)
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
		registry.Reset()
		chat.SetText(snapshot.placeholder)
		chat.ScrollToBeginning()
		return snapshot.historyRevision
	}
	if snapshot.historyRevision < renderedRevision {
		renderDanmakuHistory(chat, snapshot.history, snapshot.placeholder, registry)
		return snapshot.historyRevision
	}
	added := snapshot.historyRevision - renderedRevision
	if added == 0 {
		return renderedRevision
	}
	if added > uint64(len(snapshot.history)) {
		renderDanmakuHistory(chat, snapshot.history, snapshot.placeholder, registry)
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

// danmakuStreamConnection 和 danmakuStreamConnector 让重连循环独立于 WebSocket 实现。
// 这样既便于理解生命周期，也能测试认证响应不会意外终止消息消费循环。
type danmakuStreamConnection interface {
	Events() <-chan api.DanmakuEvent
	Errors() <-chan error
	Close()
}

type danmakuStreamConnector func(context.Context) (danmakuStreamConnection, error)

func runDanmakuStreamWithConnector(ctx context.Context, connect danmakuStreamConnector, queueUI func(func()), handleEvent func(api.DanmakuEvent), status, chat *tview.TextView, done chan<- struct{}) {
	defer close(done)
	// TCP/WebSocket 连接成功不足以说明弹幕已连接，认证可能紧接着失败。
	// 只有收到服务器首个事件后才把会话标记为已建立。
	attempt := 0
	for {
		attempt++
		currentAttempt := attempt
		queueUI(func() { status.SetText(fmt.Sprintf("正在连接弹幕服务（第 %d 次）……", currentAttempt)) })
		stream, err := connect(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			queueUI(func() {
				status.SetText(fmt.Sprintf("弹幕连接失败（第 %d 次），5 秒后重试：%s", currentAttempt, tview.Escape(err.Error())))
				chat.SetText("暂时无法连接弹幕服务器。\n\n" + tview.Escape(err.Error()) + "\n\n正在自动重试……")
			})
			if !waitDanmakuRetry(ctx, 5*time.Second) {
				return
			}
			continue
		}

		queueUI(func() { status.SetText("已建立通道，正在等待服务器确认……") })
		endpoint := ""
		if details, ok := stream.(interface{ Endpoint() string }); ok {
			endpoint = details.Endpoint()
		}
		endpointLabel := tview.Escape(formatDanmakuEndpoint(endpoint))
		connectedStatus := "弹幕已连接，消息会实时显示。"
		if endpointLabel != "" {
			connectedStatus = "弹幕已连接 · 节点 " + endpointLabel
		}
		confirmedConnection := false
		streamEnded := false
		disconnectReason := ""
		events := stream.Events()
		errors := stream.Errors()
		for !streamEnded {
			select {
			case <-ctx.Done():
				stream.Close()
				streamEnded = true
			case event, ok := <-events:
				if !ok {
					events = nil
					streamEnded = errors == nil
					continue
				}
				if !confirmedConnection && event.Kind == api.DanmakuEventConnected {
					confirmedConnection = true
					queueUI(func() {
						if strings.HasPrefix(chat.GetText(true), "正在连接") || strings.HasPrefix(chat.GetText(true), "暂时无法") {
							chat.SetText("")
						}
						// 重连使用与首次连接相同的确认状态文本。
						// WebSocket 拨号成功但认证尚未完成时，不能宣称“已恢复”。
						status.SetText(connectedStatus)
					})
				}
				handleEvent(event)
				if event.Kind == api.DanmakuEventConnected {
					// 认证事件已经更新状态，继续消费当前连接；普通弹幕只会在服务器确认后到达。
					continue
				}
				if event.Kind == api.DanmakuEventOnline {
					queueUI(func() {
						if endpointLabel == "" {
							status.SetText(fmt.Sprintf("弹幕已连接 · 当前人气 %d", event.Online))
						} else {
							status.SetText(fmt.Sprintf("弹幕已连接 · 节点 %s · 当前人气 %d", endpointLabel, event.Online))
						}
					})
				}
			case streamErr, ok := <-errors:
				if ok && streamErr != nil {
					disconnectReason = tview.Escape(streamErr.Error())
					queueUI(func() { status.SetText("弹幕连接异常：" + tview.Escape(streamErr.Error())) })
				} else if !ok {
					errors = nil
					streamEnded = events == nil
				}
			}
		}
		stream.Close()
		if ctx.Err() != nil {
			return
		}
		reason := ""
		if disconnectReason != "" {
			reason = "（" + disconnectReason
			if endpointLabel != "" {
				reason += "；节点 " + endpointLabel
			}
			reason += "）"
		} else if endpointLabel != "" {
			reason = "（节点 " + endpointLabel + "）"
		}
		if confirmedConnection {
			queueUI(func() { status.SetText("弹幕连接中断" + reason + "，5 秒后自动重连……") })
		} else {
			queueUI(func() { status.SetText("弹幕连接未确认" + reason + "，5 秒后重试……") })
		}
		if !waitDanmakuRetry(ctx, 5*time.Second) {
			return
		}
	}
}

func formatDanmakuEndpoint(endpoint string) string {
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err == nil && parsed.Host != "" {
		return parsed.Host
	}
	return strings.TrimSpace(endpoint)
}

func waitDanmakuRetry(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
