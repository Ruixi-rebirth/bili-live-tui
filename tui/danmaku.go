package tui

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
	"unicode"

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
	applyTheme()
	app := tview.NewApplication().EnableMouse(true).EnablePaste(true).SetTitle("bili-live-tui")

	chat := tview.NewTextView()
	chat.SetScrollable(true)
	chat.SetMaxLines(danmakuHistoryLimit)
	chat.SetDynamicColors(true)
	chat.SetWordWrap(true)
	chat.SetBackgroundColor(panelColor)
	chat.SetBorder(true)
	chat.SetBorderColor(tview.Styles.BorderColor)
	// 工作区标题已经命名页面；清空弹幕框标题，避免“弹幕互动”重复显示，同时保留边框。
	chat.SetTitle("")
	chat.SetTitleColor(tview.Styles.TitleColor)
	initialSessionSnapshot := session.snapshot()
	renderDanmakuHistory(chat, initialSessionSnapshot.history, initialSessionSnapshot.placeholder)

	status := tview.NewTextView()
	status.SetDynamicColors(true)
	status.SetTextColor(mutedColor)
	status.SetBackgroundColor(panelColor)
	status.SetText(formatDanmakuSessionStatus(initialSessionSnapshot))
	onlineRank := tview.NewTextView()
	onlineRank.SetScrollable(true)
	onlineRank.SetDynamicColors(true)
	onlineRank.SetWordWrap(false)
	onlineRank.SetBackgroundColor(panelColor)
	onlineRank.SetBorder(true)
	onlineRank.SetBorderColor(tview.Styles.BorderColor)
	onlineRank.SetTitleColor(tview.Styles.TitleColor)
	renderOnlineRank(onlineRank, initialSessionSnapshot)
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

	reply := tview.NewInputField().
		SetLabel("").
		SetPlaceholder("").
		SetFieldWidth(0).
		SetLabelColor(tview.Styles.SecondaryTextColor).
		SetFieldStyle(tcell.StyleDefault.
			Background(tview.Styles.ContrastBackgroundColor).
			Foreground(tview.Styles.PrimaryTextColor)).
		SetAcceptanceFunc(tview.InputFieldMaxLength(80))
	reply.SetBackgroundColor(panelColor).SetBorder(true).SetBorderColor(accentColor)
	reply.SetText(initialSessionSnapshot.draft)
	reply.SetChangedFunc(session.SetDraft)

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
	clearChat := func() {
		session.ClearHistory()
		sentCount = 0
		sendStatus.SetText("已清空本地弹幕记录。")
	}
	sending := false
	send := func() {
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
			err := client.SendDanmaku(requestCtx, roomID, sessdata, biliJCT, message)
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
		pageFooter("Enter发送　Alt+H/Ctrl+H概览　Ctrl+L清空　Ctrl+U删输入　Esc下播　Ctrl+C退出"),
	)
	pages.AddPage("main", root, true, true)
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
					renderOnlineRank(onlineRank, snapshot)
					if snapshot.historyRevision != renderedHistoryRevision {
						renderedHistoryRevision = updateDanmakuHistory(chat, snapshot, renderedHistoryRevision)
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
		switch {
		case matchesControlShortcut(event, tcell.KeyCtrlH, 'h') || matchesModifiedRuneShortcut(event, tcell.ModAlt, 'h'):
			navigation = NavigationHome
			stopApplication()
			return nil
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
			// 输入框聚焦时拦截 Enter，发送消息后不意外跳到下一个按钮。
			if reply.HasFocus() {
				send()
				return nil
			}
			return event
		case tcell.KeyEscape:
			if confirm.HasFocus() {
				pages.HidePage("confirm-stop")
				if previousFocus != nil {
					app.SetFocus(previousFocus)
				} else {
					app.SetFocus(reply)
				}
				return nil
			}
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

func appendDanmakuEvent(chat *tview.TextView, event api.DanmakuEvent, prependLineBreak bool) {
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
	line := fmt.Sprintf("%s[%s]%s[-] [%s]%s[-]：%s", separator, mutedColor.String(), stamp.Format("15:04:05"), prefixColor.String(), tview.Escape(username), tview.Escape(strings.TrimSpace(message.Text)))
	if message.MedalName != "" {
		medal := message.MedalName
		if message.MedalLevel > 0 {
			medal = fmt.Sprintf("%s %d", medal, message.MedalLevel)
		}
		line = fmt.Sprintf("%s[%s]%s[-] [%s][%s][-] [%s]%s[-]：%s", separator, mutedColor.String(), stamp.Format("15:04:05"), mutedColor.String(), tview.Escape(medal), prefixColor.String(), tview.Escape(username), tview.Escape(strings.TrimSpace(message.Text)))
	}
	_, _ = chat.Write([]byte(line))
	chat.ScrollToEnd()
}

func renderOnlineRank(view *tview.TextView, snapshot liveDanmakuSnapshot) {
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
		fmt.Fprintf(&content, "[%s]#%-2d[-] %s", mutedColor.String(), rank, tview.Escape(member.Username))
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

func renderDanmakuHistory(chat *tview.TextView, history []api.DanmakuEvent, placeholder string) {
	chat.SetText("")
	if len(history) == 0 {
		chat.SetText(placeholder)
		return
	}
	for index, event := range history {
		appendDanmakuEvent(chat, event, index > 0)
	}
	chat.ScrollToEnd()
}

// updateDanmakuHistory 只追加上次绘制后新增的事件。
// 清空历史或修订号不连续时退回完整渲染，保证页面状态仍然正确。
func updateDanmakuHistory(chat *tview.TextView, snapshot liveDanmakuSnapshot, renderedRevision uint64) uint64 {
	if len(snapshot.history) == 0 {
		chat.SetText(snapshot.placeholder)
		chat.ScrollToBeginning()
		return snapshot.historyRevision
	}
	if snapshot.historyRevision < renderedRevision {
		renderDanmakuHistory(chat, snapshot.history, snapshot.placeholder)
		return snapshot.historyRevision
	}
	added := snapshot.historyRevision - renderedRevision
	if added == 0 {
		return renderedRevision
	}
	if added > uint64(len(snapshot.history)) {
		renderDanmakuHistory(chat, snapshot.history, snapshot.placeholder)
		return snapshot.historyRevision
	}

	start := len(snapshot.history) - int(added)
	if start == 0 {
		chat.SetText("")
	}
	hasPrevious := start > 0
	for _, event := range snapshot.history[start:] {
		appendDanmakuEvent(chat, event, hasPrevious)
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
