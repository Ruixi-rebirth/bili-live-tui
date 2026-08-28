package tui

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"bili-live-tui/internal/api"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

func newDanmakuUserCardPanel() (*tview.Flex, *tview.TextView, *buttonGrid) {
	content := tview.NewTextView()
	content.SetDynamicColors(true)
	content.SetTextAlign(tview.AlignLeft)
	content.SetWordWrap(true)
	content.SetScrollable(true)
	content.SetBackgroundColor(panelColor)
	content.SetTextColor(tview.Styles.PrimaryTextColor)
	content.SetBorderPadding(1, 0, 2, 2)

	actions := newButtonGrid(buttonGridColumns)

	panel := tview.NewFlex().SetDirection(tview.FlexRow)
	panel.SetBackgroundColor(panelColor)
	panel.SetBorder(true)
	panel.SetBorderColor(tview.Styles.BorderColor)
	panel.SetTitle(" 用户资料 ")
	panel.SetTitleColor(tview.Styles.TitleColor)
	panel.AddItem(content, 0, 1, false)
	// 按钮上方保留一行，让操作区与资料正文保持清晰间距。
	actionArea := tview.NewFlex().SetDirection(tview.FlexRow)
	actionArea.SetBackgroundColor(panelColor)
	actionArea.AddItem(nil, 1, 0, false)
	actionArea.AddItem(actions, 0, 1, true)
	panel.AddItem(actionArea, 1, 0, true)
	actions.AddChangedFunc(func() {
		panel.ResizeItem(actionArea, actions.PreferredHeight()+1, 0)
	})
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

func sanitizeDanmakuText(text string) string {
	text = strings.ReplaceAll(text, "\r\n", " ")
	text = strings.ReplaceAll(text, "\r", " ")
	text = strings.ReplaceAll(text, "\n", " ")
	text = strings.ReplaceAll(text, "\t", " ")

	var b strings.Builder
	for _, r := range text {
		if r < 0x20 && r != ' ' {
			continue
		}
		if r == '\u200b' || r == '\ufeff' || r == '\u200e' || r == '\u200f' {
			continue
		}
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
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
	if prependLineBreak || (chat != nil && chat.GetText(false) != "") {
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

	cleanText := sanitizeDanmakuText(message.Text)
	var line string
	switch event.Kind {
	case api.DanmakuEventSuperChat:
		priceLabel := fmt.Sprintf("醒目留言 ¥%d", message.Price)
		if message.Price <= 0 {
			priceLabel = "醒目留言"
		}
		line = fmt.Sprintf("%s%s %s %s：%s", separator, stampText, styledText("["+priceLabel+"]", prefixColor, true), usernameText, tview.Escape(cleanText))
	case api.DanmakuEventGuard:
		line = fmt.Sprintf("%s%s %s %s %s", separator, stampText, styledText("[大航海]", prefixColor, true), usernameText, tview.Escape(cleanText))
	case api.DanmakuEventWarning:
		line = fmt.Sprintf("%s%s %s", separator, stampText, styledText("⚠ [超管警告] "+tview.Escape(cleanText), errorColor, true))
	case api.DanmakuEventGift:
		line = fmt.Sprintf("%s%s %s %s %s", separator, stampText, styledText("[礼物]", prefixColor, true), usernameText, tview.Escape(cleanText))
	default:
		line = fmt.Sprintf("%s%s %s：%s", separator, stampText, usernameText, tview.Escape(cleanText))
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

func canChangeDanmakuUserRelation(message api.DanmakuMessage, profile *api.UserProfile) bool {
	return profile != nil &&
		!profile.IsSelf &&
		strings.TrimSpace(message.UserID) != ""
}

type roomAdminLevelChoice struct {
	label string
	level int
}

func availableRoomAdminLevelChoices(seniorKnown, seniorEnabled bool) []roomAdminLevelChoice {
	choices := []roomAdminLevelChoice{{label: "普通房管", level: 1}}
	if seniorKnown && seniorEnabled {
		choices = append(choices, roomAdminLevelChoice{label: "高级房管", level: 2})
	}
	return choices
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

func danmakuUserCardHeight(text string, width int, actionHeights ...int) int {
	// 浮窗边框占两列，正文左右各留两列。
	contentWidth := max(width-6, 1)
	rows := 0
	for _, line := range strings.Split(text, "\n") {
		lineWidth := tview.TaggedStringWidth(line)
		rows += max(1, (lineWidth+contentWidth-1)/contentWidth)
	}
	actionHeight := 1
	if len(actionHeights) > 0 {
		actionHeight = max(actionHeights[0], 1)
	}
	return min(max(rows+actionHeight+4, 9), 24)
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
	if cleanText := sanitizeDanmakuText(message.Text); cleanText != "" {
		lines = append(lines, fmt.Sprintf("[%s]发言[-]  “%s”", mutedColor.String(), tview.Escape(cleanText)))
	}
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
