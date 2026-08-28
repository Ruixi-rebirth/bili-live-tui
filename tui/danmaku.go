package tui

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

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
	updateStreamHealthStatus := func(health streamruntime.Health) {
		text := formatStreamHealth(health)
		if text == "" {
			text = fmt.Sprintf("[%s]未接管推流进程[-]", mutedColor.String())
		}
		streamStatus.SetText(text)
	}
	if healthLoader != nil {
		updateStreamHealthStatus(healthLoader())
	} else {
		streamStatus.SetText(fmt.Sprintf("[%s]未接管推流进程[-]", mutedColor.String()))
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
	roomAdminSeniorKnown := false
	roomAdminSeniorEnabled := false
	roomAdminSeniorLoading := false
	roomAdminSeniorWaiters := make([]func(error), 0, 2)
	userAdminLevelOverrides := make(map[string]int)
	var refreshManagementCapabilitiesUI func()
	var refreshManagementCapabilities func(func(error))
	var refreshRoomAdminSeniorStatus func(func(error))
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

	navigation := NavigationDetach
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
	requestStop := func() {
		navigation = NavigationQuit
		stopApplication()
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
	refreshRoomAdminSeniorStatus = func(after func(error)) {
		if roomAdminSeniorKnown {
			if after != nil {
				after(nil)
			}
			return
		}
		if after != nil {
			roomAdminSeniorWaiters = append(roomAdminSeniorWaiters, after)
		}
		if roomAdminSeniorLoading {
			return
		}
		anchorID := strings.TrimSpace(managementCapabilities.AnchorID)
		if anchorID == "" {
			err := fmt.Errorf("未获取到主播 UID")
			waiters := roomAdminSeniorWaiters
			roomAdminSeniorWaiters = nil
			for _, waiter := range waiters {
				waiter(err)
			}
			return
		}
		roomAdminSeniorLoading = true
		go func() {
			requestCtx, cancelRequest := context.WithTimeout(streamCtx, 6*time.Second)
			defer cancelRequest()
			status, err := client.GetRoomAdminSeniorStatus(requestCtx, anchorID, sessdata, biliJCT)
			queueUI(func() {
				roomAdminSeniorLoading = false
				if err == nil {
					roomAdminSeniorKnown = true
					roomAdminSeniorEnabled = status > 0
				}
				waiters := roomAdminSeniorWaiters
				roomAdminSeniorWaiters = nil
				for _, waiter := range waiters {
					waiter(err)
				}
			})
		}()
	}
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
				if err == nil && capabilities.IsAnchor {
					refreshRoomAdminSeniorStatus(nil)
				}
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
						sendStatus.SetText("这条弹幕被 B 站拦截了。")
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
	userCardText := ""
	resizeUserCard := func() {
		userCardOverlay.SetPreferredSize(userCardWidth, danmakuUserCardHeight(userCardText, userCardWidth, userCardActions.PreferredHeight()))
	}
	userCardActions.AddChangedFunc(resizeUserCard)
	setUserCardText := func(text string) {
		userCardText = text
		userCardContent.SetText(text)
		resizeUserCard()
	}
	userCardVisible := false
	selectedUserID := ""
	selectedUserMessage := api.DanmakuMessage{}
	userProfileCache := make(map[string]api.UserProfile)
	userProfileLoading := make(map[string]bool)
	focusUserCardAction := func(index int) {
		buttonCount := userCardActions.GetButtonCount()
		if buttonCount == 0 {
			return
		}
		index = (index%buttonCount + buttonCount) % buttonCount
		userCardActions.SetFocus(index)
		// ClearButtons 会移除 Application 当前聚焦的旧 Button。每次重建
		// 按钮后都重新委派焦点，否则新按钮看得见却收不到键盘事件。
		if front, _ := pages.GetFrontPage(); front == "user-card" {
			app.SetFocus(userCardActions)
		}
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

	managementConfirm := newConfirmModal("用户管理确认")
	var pendingManagementAction func(context.Context) error
	var pendingManagementSuccess func()
	managementActionLabel := ""
	closeManagementConfirm := func() {
		pendingManagementAction = nil
		pendingManagementSuccess = nil
		pages.HidePage("management-confirm")
		pages.SendToFront("user-card")
		focusUserCardAction(userCardActions.currentIndex())
	}
	runManagementAction := func() {
		action := pendingManagementAction
		onSuccess := pendingManagementSuccess
		label := managementActionLabel
		pendingManagementAction = nil
		pendingManagementSuccess = nil
		pages.HidePage("management-confirm")
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
	openManagementConfirm := func(label, warning, confirmBtn string, action func(context.Context) error, onSuccess ...func()) {
		if confirmBtn == "" {
			confirmBtn = "确认"
		}
		managementActionLabel = label
		pendingManagementAction = action
		pendingManagementSuccess = nil
		if len(onSuccess) > 0 {
			pendingManagementSuccess = onSuccess[0]
		}
		// “取消”始终排在第一位并获得默认焦点，处罚操作不会因一次回车误触。
		managementConfirm.ClearButtons().SetText(warning).AddButtons([]string{"取消", confirmBtn})
		managementConfirm.SetDoneFunc(func(buttonIndex int, _ string) {
			if buttonIndex == 1 && pendingManagementAction != nil {
				runManagementAction()
				return
			}
			closeManagementConfirm()
		})
		pages.ShowPage("management-confirm")
		pages.SendToFront("management-confirm")
		app.SetFocus(managementConfirm)
	}
	openMuteConfirm := func(username, userID, danmakuText string) {
		durations := []struct {
			label    string
			duration api.RoomUserMuteDuration
		}{
			{"本场", api.RoomMuteThisLive},
			{"2小时", api.RoomMuteTwoHours},
			{"4小时", api.RoomMuteFourHours},
			{"1天", api.RoomMuteOneDay},
			{"7天", api.RoomMuteSevenDays},
			{"永久", api.RoomMutePermanent},
		}
		buttonLabels := []string{"取消"}
		for _, item := range durations {
			buttonLabels = append(buttonLabels, item.label)
		}
		warning := fmt.Sprintf("确定禁言 %s 吗？请选择禁言时长：", username)
		managementConfirm.ClearButtons().SetText(warning).AddButtons(buttonLabels)
		managementConfirm.SetDoneFunc(func(buttonIndex int, _ string) {
			if buttonIndex > 0 && buttonIndex <= len(durations) {
				choice := durations[buttonIndex-1]
				managementActionLabel = fmt.Sprintf("禁言 %s（%s）", username, choice.label)
				pendingManagementAction = func(requestCtx context.Context) error {
					return client.MuteRoomUser(requestCtx, roomID, userID, danmakuText, choice.duration, sessdata, biliJCT)
				}
				runManagementAction()
				return
			}
			closeManagementConfirm()
		})
		pages.ShowPage("management-confirm")
		pages.SendToFront("management-confirm")
		app.SetFocus(managementConfirm)
	}
	openRoomAdminChoices := func(username string, message api.DanmakuMessage) {
		showChoices := func(statusErr error) {
			if !userCardVisible || selectedUserID != strings.TrimSpace(message.UserID) {
				return
			}
			choices := availableRoomAdminLevelChoices(roomAdminSeniorKnown, roomAdminSeniorEnabled)
			labels := []string{"取消"}
			for _, choice := range choices {
				labels = append(labels, choice.label)
			}
			prompt := "选择要授予 " + username + " 的房管身份。"
			if statusErr != nil {
				prompt += "\n\n高级房管状态暂不可用。"
			}
			managementConfirm.ClearButtons().SetText(prompt).AddButtons(labels)
			managementConfirm.SetDoneFunc(func(buttonIndex int, _ string) {
				if buttonIndex <= 0 || buttonIndex > len(choices) {
					closeManagementConfirm()
					return
				}
				choice := choices[buttonIndex-1]
				level, levelLabel := choice.level, choice.label
				openManagementConfirm("任命"+levelLabel+" "+username, "确定将 "+username+" 设为"+levelLabel+"吗？", "确认任命", func(requestCtx context.Context) error {
					return client.AppointRoomAdmin(requestCtx, message.UserID, level, sessdata, biliJCT)
				}, func() { userAdminLevelOverrides[strings.TrimSpace(message.UserID)] = level })
			})
			pages.ShowPage("management-confirm")
			pages.SendToFront("management-confirm")
			app.SetFocus(managementConfirm)
		}
		if roomAdminSeniorKnown {
			showChoices(nil)
			return
		}
		sendStatus.SetText("正在获取可用的房管类型……")
		refreshRoomAdminSeniorStatus(func(err error) {
			sendStatus.SetText("")
			showChoices(err)
		})
	}

	roomManager := newRoomManagerWorkspace(roomManagerDependencies{
		Context: streamCtx, App: app, Pages: pages, Client: client,
		RoomID: roomID, Sessdata: sessdata, BiliJCT: biliJCT,
		QueueUI: queueUI, Status: sendStatus,
		Capabilities:        func() api.RoomManagementCapabilities { return managementCapabilities },
		RefreshCapabilities: refreshManagementCapabilities,
		ReturnToChat:        func() { app.SetFocus(reply) },
		Quit:                func() { navigation = NavigationQuit; stopApplication() },
	})
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
		if canChangeDanmakuUserRelation(message, profile) {
			targetFollowing := !profile.IsFollowing
			actionLabel := "关注"
			if !targetFollowing {
				actionLabel = "取消关注"
			}
			userCardActions.AddButton(actionLabel, func() {
				// 请求通常很快，但仍禁用一次以避免鼠标连点产生重复操作。
				// 按钮文字保持不变，不用临时状态扰动布局。
				userCardActions.GetButton(0).SetDisabled(true)
				focusUserCardAction(userCardActions.GetButtonCount() - 1)
				profileBeforeChange := *profile
				go func(clicked api.DanmakuMessage) {
					requestCtx, cancelRequest := context.WithTimeout(streamCtx, 8*time.Second)
					defer cancelRequest()
					relationErr := client.SetUserFollowing(requestCtx, clicked.UserID, sessdata, biliJCT, targetFollowing)
					queueUI(func() {
						if relationErr == nil {
							profileBeforeChange.IsFollowing = targetFollowing
							userProfileCache[clicked.UserID] = profileBeforeChange
						}
						if !userCardVisible || selectedUserID != clicked.UserID {
							return
						}
						if relationErr != nil {
							text := formatDanmakuUserCard(clicked, &profileBeforeChange, false, nil)
							text += fmt.Sprintf("\n\n[%s]%s失败：%s[-]", errorColor.String(), actionLabel, tview.Escape(relationErr.Error()))
							setUserCardText(text)
							configureUserCardActions(clicked, &profileBeforeChange)
							return
						}
						text := formatDanmakuUserCard(clicked, &profileBeforeChange, false, nil)
						text += fmt.Sprintf("\n\n[%s]%s成功[-]", accentActiveColor.String(), actionLabel)
						setUserCardText(text)
						configureUserCardActions(clicked, &profileBeforeChange)
					})
				}(message)
			})
		}
		if profile != nil && !profile.IsSelf && strings.TrimSpace(message.Username) != "" {
			userCardActions.AddButton("@TA", func() { mentionUser(message) })
		}
		if managementCapabilitiesReady && managementCapabilitiesErr == nil && canManageDanmakuUser(managementCapabilities, message, profile) {
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
			if managementCapabilities.CanMuteUser(targetAdminLevel, targetAdminLevelKnown) {
				userCardActions.AddButton("警告", func() {
					warnText := fmt.Sprintf("@%s 请注意直播间言论规范，违规将被禁言处理！", username)
					openManagementConfirm("警告 "+username, fmt.Sprintf("确定向公屏发送针对 %s 的合规警告吗？\n\n[%s]%s[-]", username, tview.Styles.SecondaryTextColor.String(), warnText), "发送警告", func(requestCtx context.Context) error {
						return danmakuSender.Send(requestCtx, warnText, int(danmakuMaxLength.Load()))
					})
				})
				userCardActions.AddButton("禁言", func() {
					openMuteConfirm(username, message.UserID, message.Text)
				})
			}
			if managementCapabilities.CanBlacklistUser(targetAdminLevel, targetAdminLevelKnown) && strings.TrimSpace(managementCapabilities.AnchorID) != "" {
				userCardActions.AddButton("拉黑", func() {
					openManagementConfirm("拉黑 "+username, "确定将 "+username+" 添加到直播间黑名单吗？", "确认拉黑", func(requestCtx context.Context) error {
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
					userCardActions.AddButton("撤销房管", func() {
						openManagementConfirm("撤销房管 "+username, "确定撤销 "+username+" 的房管权限吗？", "确认撤销", func(requestCtx context.Context) error {
							return client.DismissRoomAdmin(requestCtx, message.UserID, sessdata, biliJCT)
						}, func() { userAdminLevelOverrides[strings.TrimSpace(message.UserID)] = 0 })
					})
				} else {
					userCardActions.AddButton("设为房管", func() {
						openRoomAdminChoices(username, message)
					})
				}
			}
		}
		userCardActions.AddButton("关闭", closeUserCard)
		// 打开资料卡以及异步刷新按钮后，默认停在最安全的“关闭”上。
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
			if activeRankTab != rankTabAudience {
				activeRankTab = rankTabAudience
				renderOnlineRank(onlineRank, session.snapshot(), activeRankTab, onlineRankUserRegions)
				onlineRank.ScrollToBeginning()
			}
		case "tab_guard":
			if activeRankTab != rankTabGuard {
				activeRankTab = rankTabGuard
				renderOnlineRank(onlineRank, session.snapshot(), activeRankTab, onlineRankUserRegions)
				onlineRank.ScrollToBeginning()
			}
		default:
			if message, ok := onlineRankUserRegions.Lookup(region); ok {
				openUserCard(message)
			}
		}
	})
	var homeWS *homeWorkspaceComponents
	var overviewVisible bool
	confirm := newConfirmModal("下播确认").
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
		} else if overviewVisible && homeWS != nil && len(homeWS.buttons) > 0 {
			app.SetFocus(homeWS.buttons[len(homeWS.buttons)-1])
		} else {
			app.SetFocus(reply)
		}
	})
	openStopConfirm := func() {
		previousFocus = app.GetFocus()
		pages.ShowPage("confirm-stop")
		pages.SendToFront("confirm-stop")
		app.SetFocus(confirm)
	}

	warningModal := newConfirmModal("平台超管警告")
	warningModalVisible := false
	showSuperAdminWarning := func(event api.DanmakuEvent) {
		isCutOff := event.Command == "CUT_OFF"
		title := "平台超管警告"
		if isCutOff {
			title = "直播已被超管切断"
		}
		warningModal.panel.SetTitle(" ⚠️ " + title + " ⚠️ ")
		warningModal.panel.SetBorderColor(errorColor)
		warningModal.ClearButtons()
		if isCutOff {
			warningModal.SetText(fmt.Sprintf("[%s::b]平台超管已强制切断/关闭当前直播！[-:-:-]\n\n[%s]原因/提示：[-]%s\n\n请立即检查直播规范，尽快停止推流并处理违规内容。", errorColor.String(), tview.Styles.PrimaryTextColor.String(), tview.Escape(event.Message.Text)))
			warningModal.AddButtons([]string{"下播并退出", "关闭提示"})
			warningModal.SetDoneFunc(func(buttonIndex int, _ string) {
				pages.HidePage("warning-dialog")
				warningModalVisible = false
				if buttonIndex == 0 {
					navigation = NavigationQuit
					stopApplication()
					return
				}
				if previousFocus != nil {
					app.SetFocus(previousFocus)
				} else {
					app.SetFocus(reply)
				}
			})
		} else {
			warningModal.SetText(fmt.Sprintf("[%s::b]收到来自 B 站官方超管的合规警告！[-:-:-]\n\n[%s]警告说明：[-]%s\n\n请立即整改直播音视频或弹幕内容，避免直播间被切断或封禁！", errorColor.String(), tview.Styles.PrimaryTextColor.String(), tview.Escape(event.Message.Text)))
			warningModal.AddButtons([]string{"我已知晓并整改"})
			warningModal.SetDoneFunc(func(_ int, _ string) {
				pages.HidePage("warning-dialog")
				warningModalVisible = false
				if previousFocus != nil {
					app.SetFocus(previousFocus)
				} else {
					app.SetFocus(reply)
				}
			})
		}
		sendStatus.SetText(fmt.Sprintf("[%s::b]⚠ [超管警告] %s[-:-:-]", errorColor.String(), tview.Escape(event.Message.Text)))
		previousFocus = app.GetFocus()
		warningModalVisible = true
		pages.ShowPage("warning-dialog")
		pages.SendToFront("warning-dialog")
		app.SetFocus(warningModal)
	}

	var openOverview func()
	toolButtons := []*tview.Button{
		newActionButton("直播概览", func() {
			if openOverview != nil {
				openOverview()
			}
		}),
		newActionButton("房间管理", roomManager.open),
	}
	toolBar := centeredActionBar(toolButtons)
	toolBar.SetBackgroundColor(panelColor)

	mainFocusables := []tview.Primitive{reply, toolButtons[0], toolButtons[1], chat, onlineRank}
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
	pages.AddPage("management-confirm", managementConfirm, true, false)
	pages.AddPage("confirm-stop", confirm, true, false)
	pages.AddPage("warning-dialog", warningModal, true, false)

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
					if snapshot.historyRevision > renderedHistoryRevision {
						addedCount := int(snapshot.historyRevision - renderedHistoryRevision)
						if addedCount > len(snapshot.history) {
							addedCount = len(snapshot.history)
						}
						startIdx := len(snapshot.history) - addedCount
						for _, ev := range snapshot.history[startIdx:] {
							if ev.Kind == api.DanmakuEventWarning {
								showSuperAdminWarning(ev)
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
					queueUI(func() { updateStreamHealthStatus(health) })
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

	userPopupInput := pageInputHandlers{
		"user-card": func(event *tcell.EventKey) *tcell.EventKey {
			switch event.Key() {
			case tcell.KeyEscape:
				closeUserCard()
				return nil
			case tcell.KeyLeft, tcell.KeyBacktab:
				focusUserCardAction(userCardActions.currentIndex() - 1)
				return nil
			case tcell.KeyRight, tcell.KeyTab:
				focusUserCardAction(userCardActions.currentIndex() + 1)
				return nil
			case tcell.KeyUp:
				userCardActions.moveVertical(-1)
				return nil
			case tcell.KeyDown:
				userCardActions.moveVertical(1)
				return nil
			case tcell.KeyCtrlC:
				navigation = NavigationQuit
				stopApplication()
				return nil
			default:
				return event
			}
		},
		"management-confirm": modalInputHandler(closeManagementConfirm, requestStop),
	}

	app.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if routed, handled := userPopupInput.capture(pages, event); handled {
			return routed
		}
		frontPage, _ := pages.GetFrontPage()
		if frontPage == "room-manager" || frontPage == "room-manager-confirm" || frontPage == "room-manager-input" {
			return roomManager.capture(event)
		}
		if overviewVisible {
			if homeWS != nil && homeWS.isEditing != nil && homeWS.isEditing() {
				return event
			}
			if warningModalVisible {
				switch event.Key() {
				case tcell.KeyEscape:
					pages.HidePage("warning-dialog")
					warningModalVisible = false
					if previousFocus != nil {
						app.SetFocus(previousFocus)
					} else if homeWS != nil && len(homeWS.buttons) > 0 {
						app.SetFocus(homeWS.buttons[len(homeWS.buttons)-1])
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
			if confirm.HasFocus() {
				switch event.Key() {
				case tcell.KeyEscape:
					pages.HidePage("confirm-stop")
					if previousFocus != nil {
						app.SetFocus(previousFocus)
					} else if homeWS != nil && len(homeWS.buttons) > 0 {
						app.SetFocus(homeWS.buttons[len(homeWS.buttons)-1])
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
		if warningModalVisible {
			switch event.Key() {
			case tcell.KeyEscape:
				pages.HidePage("warning-dialog")
				warningModalVisible = false
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
			cycleFocus(app, mainFocusables, event.Key() == tcell.KeyBacktab)
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
				if activeRankTab != rankTabAudience {
					activeRankTab = rankTabAudience
					renderOnlineRank(onlineRank, session.snapshot(), activeRankTab, onlineRankUserRegions)
					onlineRank.ScrollToBeginning()
				}
				return nil
			case tcell.KeyRight:
				if activeRankTab != rankTabGuard {
					activeRankTab = rankTabGuard
					renderOnlineRank(onlineRank, session.snapshot(), activeRankTab, onlineRankUserRegions)
					onlineRank.ScrollToBeginning()
				}
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
			requestStop()
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
		return NavigationDetach, fmt.Errorf("启动弹幕界面失败: %w", err)
	}
	uiOpen.Store(false)
	cancelStream()
	unsubscribe()
	return navigation, nil
}
