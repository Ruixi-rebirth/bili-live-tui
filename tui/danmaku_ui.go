package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"bili-live-tui/internal/api"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

func setDanmakuChatColors(chat *tview.TextView) {
	if noColor {
		chat.SetBackgroundColor(tcell.ColorDefault)
		chat.SetTextColor(tcell.ColorDefault)
		return
	}
	chat.SetBackgroundColor(panelColor)
	chat.SetTextColor(tview.Styles.PrimaryTextColor)
}

func cycleFocus(app *tview.Application, focusables []tview.Primitive, backward bool) {
	if app == nil || len(focusables) == 0 {
		return
	}
	current := app.GetFocus()
	currentIndex := -1
	for index, primitive := range focusables {
		if primitive == current {
			currentIndex = index
			break
		}
	}
	if currentIndex < 0 {
		for index, primitive := range focusables {
			if primitive.HasFocus() {
				currentIndex = index
				break
			}
		}
	}
	next := 0
	if currentIndex >= 0 {
		next = (currentIndex + 1) % len(focusables)
	}
	if backward {
		next = len(focusables) - 1
		if currentIndex >= 0 {
			next = (currentIndex - 1 + len(focusables)) % len(focusables)
		}
	}
	app.SetFocus(focusables[next])
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
