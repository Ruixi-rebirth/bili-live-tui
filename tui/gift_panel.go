package tui

import (
	"fmt"
	"sort"
	"strings"

	"bili-live-tui/internal/api"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

type giftPanelTab int

const (
	giftTabLog  giftPanelTab = 0
	giftTabRank giftPanelTab = 1
)

type giftLeaderboardEntry struct {
	UserID     string
	Username   string
	GuardLevel int
	TotalCNY   float64
	GiftEvents int
	HasGuard   bool
	HasSC      bool
}

type giftPanel struct {
	container    *tview.Flex
	summaryView  *tview.TextView
	tabBar       *tview.TextView
	table        *tview.Table
	helpView     *tview.TextView
	closeBtn     *tview.Button
	currentTab   giftPanelTab
	initialized  bool
	events       []api.DanmakuEvent
	stats        api.LiveSessionStats
	logRows      []api.DanmakuMessage
	rankRows     []giftLeaderboardEntry
	onSelectUser func(message api.DanmakuMessage)
	onClose      func()
}

func newGiftPanel(onSelectUser func(message api.DanmakuMessage), onClose func()) *giftPanel {
	p := &giftPanel{
		container:    tview.NewFlex().SetDirection(tview.FlexRow),
		summaryView:  tview.NewTextView().SetDynamicColors(true),
		tabBar:       tview.NewTextView().SetDynamicColors(true).SetRegions(true),
		table:        tview.NewTable().SetSelectable(true, false),
		helpView:     tview.NewTextView().SetDynamicColors(true),
		currentTab:   giftTabLog,
		onSelectUser: onSelectUser,
		onClose:      onClose,
	}

	p.container.SetBackgroundColor(panelColor)
	p.container.SetBorder(true)
	p.container.SetTitle(" 🎁 本次直播收礼与收益看板 ")
	p.container.SetTitleColor(tview.Styles.TitleColor)
	p.container.SetBorderColor(tview.Styles.BorderColor)

	p.summaryView.SetBackgroundColor(panelColor)
	p.summaryView.SetBorderPadding(0, 0, 1, 1)

	p.tabBar.SetBackgroundColor(panelColor)

	p.table.SetBackgroundColor(panelColor)
	p.table.SetSelectedStyle(tcell.StyleDefault.
		Background(accentActiveColor).
		Foreground(buttonActiveTextColor).
		Bold(true))

	p.helpView.SetBackgroundColor(panelColor)
	p.helpView.SetTextColor(mutedColor)
	p.helpView.SetText(" [Tab/左右]切换流水/榜单  [↑/↓]选择记录  [Enter]看送礼人")

	p.closeBtn = tview.NewButton(" 关闭 (Esc) ")
	p.closeBtn.SetStyle(tcell.StyleDefault.Background(accentColor).Foreground(buttonTextColor))
	p.closeBtn.SetActivatedStyle(tcell.StyleDefault.Background(accentActiveColor).Foreground(buttonActiveTextColor).Bold(true))
	p.closeBtn.SetSelectedFunc(func() {
		if p.onClose != nil {
			p.onClose()
		}
	})

	bottomBar := tview.NewFlex().SetDirection(tview.FlexColumn)
	bottomBar.SetBackgroundColor(panelColor)
	bottomBar.AddItem(p.helpView, 0, 1, false)
	bottomBar.AddItem(p.closeBtn, 14, 0, true)

	p.table.SetSelectedFunc(func(row, _ int) {
		if row <= 0 {
			return
		}
		index := row - 1
		if p.currentTab == giftTabLog {
			if index >= 0 && index < len(p.logRows) && p.onSelectUser != nil {
				p.onSelectUser(p.logRows[index])
			}
		} else {
			if index >= 0 && index < len(p.rankRows) && p.onSelectUser != nil {
				entry := p.rankRows[index]
				p.onSelectUser(api.DanmakuMessage{
					UserID:     entry.UserID,
					Username:   entry.Username,
					GuardLevel: entry.GuardLevel,
				})
			}
		}
	})

	p.tabBar.SetHighlightedFunc(func(added, _, _ []string) {
		if len(added) == 0 {
			return
		}
		p.tabBar.Highlight()
		switch added[0] {
		case "tab_log":
			if p.currentTab != giftTabLog {
				p.currentTab = giftTabLog
				p.render()
			}
		case "tab_rank":
			if p.currentTab != giftTabRank {
				p.currentTab = giftTabRank
				p.render()
			}
		}
	})

	p.container.AddItem(p.summaryView, 2, 0, false)
	p.container.AddItem(p.tabBar, 1, 0, false)
	p.container.AddItem(p.table, 0, 1, true)
	p.container.AddItem(bottomBar, 1, 0, false)

	p.table.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch event.Key() {
		case tcell.KeyTab, tcell.KeyBacktab:
			if p.currentTab == giftTabLog {
				p.currentTab = giftTabRank
			} else {
				p.currentTab = giftTabLog
			}
			p.render()
			return nil
		case tcell.KeyRight:
			if p.currentTab != giftTabRank {
				p.currentTab = giftTabRank
				p.render()
			}
			return nil
		case tcell.KeyLeft:
			if p.currentTab != giftTabLog {
				p.currentTab = giftTabLog
				p.render()
			}
			return nil
		case tcell.KeyEscape:
			if p.onClose != nil {
				p.onClose()
			}
			return nil
		case tcell.KeyRune:
			if event.Rune() == 'q' || event.Rune() == 'Q' {
				if p.onClose != nil {
					p.onClose()
				}
				return nil
			}
		}
		return event
	})

	p.render()
	return p
}

func (p *giftPanel) Update(snapshot liveDanmakuSnapshot) {
	// 如果已初始化且礼物数据和统计没有任何变化，跳过重绘，避免频繁弹幕导致界面反复重建卡死
	if p.initialized && len(p.events) == len(snapshot.gifts) && p.stats == snapshot.stats {
		return
	}
	p.initialized = true
	p.events = append(p.events[:0], snapshot.gifts...)
	p.stats = snapshot.stats
	p.render()
}

func (p *giftPanel) buildRankRows() {
	userMap := make(map[string]*giftLeaderboardEntry)
	for _, ev := range p.events {
		uid := strings.TrimSpace(ev.Message.UserID)
		if uid == "" {
			uid = strings.TrimSpace(ev.Message.Username)
		}
		if uid == "" {
			continue
		}
		entry, ok := userMap[uid]
		if !ok {
			entry = &giftLeaderboardEntry{
				UserID:     ev.Message.UserID,
				Username:   ev.Message.Username,
				GuardLevel: ev.Message.GuardLevel,
			}
			userMap[uid] = entry
		}
		if ev.Message.GuardLevel > 0 && (entry.GuardLevel == 0 || ev.Message.GuardLevel < entry.GuardLevel) {
			entry.GuardLevel = ev.Message.GuardLevel
		}
		entry.GiftEvents++
		switch ev.Kind {
		case api.DanmakuEventSuperChat:
			entry.TotalCNY += float64(ev.Message.Price)
			entry.HasSC = true
		case api.DanmakuEventGuard:
			entry.HasGuard = true
			if ev.Message.Price > 0 {
				entry.TotalCNY += float64(ev.Message.Price)
			} else {
				switch ev.Message.GuardLevel {
				case 1:
					entry.TotalCNY += 19998
				case 2:
					entry.TotalCNY += 1998
				default:
					entry.TotalCNY += 198
				}
			}
		default:
			if ev.Message.GiftCoinType == "gold" {
				entry.TotalCNY += float64(ev.Message.GiftTotalCoin) / 1000.0
			}
		}
	}

	list := make([]giftLeaderboardEntry, 0, len(userMap))
	for _, v := range userMap {
		list = append(list, *v)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].TotalCNY != list[j].TotalCNY {
			return list[i].TotalCNY > list[j].TotalCNY
		}
		return list[i].GiftEvents > list[j].GiftEvents
	})
	p.rankRows = list
}

func (p *giftPanel) render() {
	p.buildRankRows()

	// 1. 计算总收益（折合人民币）
	// 1000 金瓜子 = 10 电池 = 1 元
	giftCNY := float64(p.stats.GiftGoldCoin) / 1000.0
	scCNY := float64(p.stats.SuperChatPrice)
	guardCNY := 0.0
	for _, ev := range p.events {
		if ev.Kind == api.DanmakuEventGuard {
			if ev.Message.Price > 0 {
				guardCNY += float64(ev.Message.Price)
			} else {
				switch ev.Message.GuardLevel {
				case 1:
					guardCNY += 19998
				case 2:
					guardCNY += 1998
				default:
					guardCNY += 198
				}
			}
		}
	}
	totalCNY := giftCNY + scCNY + guardCNY

	var summary strings.Builder
	fmt.Fprintf(&summary, " 💰 [#f59e0b::b]总收益折合: ¥%.2f[-:-:-]    🎁 [#38bdf8]礼物打赏: ¥%.2f[-:-:-] (%d次/%d件)\n",
		totalCNY, giftCNY, p.stats.GiftEvents, p.stats.GiftCount)
	fmt.Fprintf(&summary, " 💬 [#facc15]醒目留言: ¥%.2f[-:-:-] (%d条)     ⚓ [#c084fc]大航海登船: ¥%.2f[-:-:-] (%d位)",
		scCNY, p.stats.SuperChatCount, guardCNY, p.stats.GuardCount)
	p.summaryView.SetText(summary.String())

	// 2. 标签栏
	tabLogCount := len(p.events)
	tabRankCount := len(p.rankRows)
	logTitle := fmt.Sprintf(" ⚡ 实时明细流水 (%d) ", tabLogCount)
	rankTitle := fmt.Sprintf(" 🏆 贡献榜单汇总 (%d) ", tabRankCount)
	if p.currentTab == giftTabLog {
		p.tabBar.SetText(fmt.Sprintf(" [\"tab_log\"][#38bdf8::b][●%s][-:-:-][\"\"]  [\"tab_rank\"][#94a3b8][○%s][-:-:-][\"\"]",
			logTitle, rankTitle))
	} else {
		p.tabBar.SetText(fmt.Sprintf(" [\"tab_log\"][#94a3b8][○%s][-:-:-][\"\"]  [\"tab_rank\"][#38bdf8::b][●%s][-:-:-][\"\"]",
			logTitle, rankTitle))
	}

	// 3. 表格内容（保持原选择行）
	selRow, selCol := p.table.GetSelection()
	p.table.Clear()
	if p.currentTab == giftTabLog {
		p.renderLogTable()
	} else {
		p.renderRankTable()
	}
	rowCount := p.table.GetRowCount()
	if selRow > 0 && rowCount > 1 {
		if selRow >= rowCount {
			selRow = rowCount - 1
		}
		p.table.Select(selRow, selCol)
	} else if rowCount > 1 {
		p.table.Select(1, 0)
	}
}

func (p *giftPanel) renderLogTable() {
	headers := []struct {
		name      string
		align     int
		expansion int
	}{
		{" 时间", tview.AlignLeft, 0},
		{"送礼人", tview.AlignLeft, 0},
		{"打赏与留言内容", tview.AlignLeft, 2},
		{"价值估算 ", tview.AlignRight, 0},
	}
	for col, h := range headers {
		cell := tview.NewTableCell(h.name).
			SetTextColor(mutedColor).
			SetSelectable(false).
			SetAlign(h.align).
			SetExpansion(h.expansion).
			SetAttributes(tcell.AttrBold)
		p.table.SetCell(0, col, cell)
	}

	p.logRows = p.logRows[:0]
	// 倒序展示（最新在最前）
	row := 1
	for i := len(p.events) - 1; i >= 0; i-- {
		ev := p.events[i]
		msg := ev.Message
		p.logRows = append(p.logRows, msg)

		timeStr := " " + msg.Timestamp.Format("15:04:05")
		username := msg.Username
		if guard := onlineGuardLabel(msg.GuardLevel); guard != "" {
			username = fmt.Sprintf("[%s] %s", guard, username)
		}

		var contentStr, valStr string

		switch ev.Kind {
		case api.DanmakuEventSuperChat:
			text := strings.TrimSpace(msg.Text)
			if text != "" {
				contentStr = fmt.Sprintf("[#facc15::b][SC醒目留言][-:-:-] %s", tview.Escape(text))
			} else {
				contentStr = "[#facc15::b][SC醒目留言][-:-:-]"
			}
			valStr = fmt.Sprintf("¥%d ", msg.Price)
		case api.DanmakuEventGuard:
			contentStr = fmt.Sprintf("[#c084fc::b][大航海][-:-:-] 登船 %s (%d个月)", tview.Escape(msg.GiftName), msg.GiftCount)
			if msg.Price > 0 {
				valStr = fmt.Sprintf("¥%d ", msg.Price)
			} else {
				switch msg.GuardLevel {
				case 1:
					valStr = "¥19998 "
				case 2:
					valStr = "¥1998 "
				default:
					valStr = "¥198 "
				}
			}
		default: // Gift
			comboStr := ""
			if msg.GiftCombo > 1 {
				comboStr = fmt.Sprintf(" [连击x%d]", msg.GiftCombo)
			}
			contentStr = fmt.Sprintf("[#38bdf8::b][礼物][-:-:-] %s ×%d%s", tview.Escape(msg.GiftName), msg.GiftCount, comboStr)
			if msg.GiftCoinType == "gold" {
				valStr = fmt.Sprintf("¥%.2f ", float64(msg.GiftTotalCoin)/1000.0)
			} else {
				valStr = fmt.Sprintf("%d银瓜子 ", msg.GiftTotalCoin)
			}
		}

		p.table.SetCell(row, 0, tview.NewTableCell(timeStr).SetTextColor(mutedColor))
		p.table.SetCell(row, 1, tview.NewTableCell(username).SetTextColor(accentColor).SetAttributes(tcell.AttrBold))
		p.table.SetCell(row, 2, tview.NewTableCell(contentStr).SetExpansion(2))
		p.table.SetCell(row, 3, tview.NewTableCell(valStr).SetAlign(tview.AlignRight).SetTextColor(tcell.NewHexColor(0x22c55e)).SetAttributes(tcell.AttrBold))
		row++
	}

	if len(p.events) == 0 {
		p.table.SetCell(1, 0, tview.NewTableCell("").SetSelectable(false))
		p.table.SetCell(1, 1, tview.NewTableCell("  ✨ 本次直播暂未收到打赏记录").
			SetTextColor(mutedColor).
			SetSelectable(false))
		p.table.SetCell(1, 2, tview.NewTableCell("礼物与醒目留言将在此处实时滚动显示").
			SetTextColor(mutedColor).
			SetSelectable(false))
		p.table.SetCell(1, 3, tview.NewTableCell("").SetSelectable(false))
	}
}

func (p *giftPanel) renderRankTable() {
	headers := []struct {
		name      string
		align     int
		expansion int
	}{
		{" 排名", tview.AlignLeft, 0},
		{"送礼人", tview.AlignLeft, 0},
		{"打赏概况", tview.AlignLeft, 2},
		{"累计价值折合 ", tview.AlignRight, 0},
	}
	for col, h := range headers {
		cell := tview.NewTableCell(h.name).
			SetTextColor(mutedColor).
			SetSelectable(false).
			SetAlign(h.align).
			SetExpansion(h.expansion).
			SetAttributes(tcell.AttrBold)
		p.table.SetCell(0, col, cell)
	}

	row := 1
	for idx, item := range p.rankRows {
		rankStr := fmt.Sprintf(" #%d", idx+1)
		rankColor := tcell.ColorWhite
		switch idx {
		case 0:
			rankStr = " 🥇 #1"
			rankColor = tcell.NewHexColor(0xf59e0b) // 金
		case 1:
			rankStr = " 🥈 #2"
			rankColor = tcell.NewHexColor(0xcbd5e1) // 银
		case 2:
			rankStr = " 🥉 #3"
			rankColor = tcell.NewHexColor(0xd97706) // 铜
		}

		username := item.Username
		if guard := onlineGuardLabel(item.GuardLevel); guard != "" {
			username = fmt.Sprintf("[%s] %s", guard, username)
		}
		valStr := fmt.Sprintf("¥%.2f ", item.TotalCNY)

		overview := fmt.Sprintf("打赏共 %d 次", item.GiftEvents)
		if item.HasGuard {
			overview += " · [#c084fc][大航海][-]"
		}
		if item.HasSC {
			overview += " · [#facc15][SC留言][-]"
		}

		p.table.SetCell(row, 0, tview.NewTableCell(rankStr).SetTextColor(rankColor).SetAttributes(tcell.AttrBold))
		p.table.SetCell(row, 1, tview.NewTableCell(username).SetTextColor(accentColor).SetAttributes(tcell.AttrBold))
		p.table.SetCell(row, 2, tview.NewTableCell(overview).SetExpansion(2).SetTextColor(tview.Styles.PrimaryTextColor))
		p.table.SetCell(row, 3, tview.NewTableCell(valStr).SetAlign(tview.AlignRight).SetTextColor(tcell.NewHexColor(0x22c55e)).SetAttributes(tcell.AttrBold))
		row++
	}

	if len(p.rankRows) == 0 {
		p.table.SetCell(1, 0, tview.NewTableCell("").SetSelectable(false))
		p.table.SetCell(1, 1, tview.NewTableCell("  🏆 本次直播暂无送礼观众").
			SetTextColor(mutedColor).
			SetSelectable(false))
		p.table.SetCell(1, 2, tview.NewTableCell("收到打赏后将在此处按总金额展示贡献榜单").
			SetTextColor(mutedColor).
			SetSelectable(false))
		p.table.SetCell(1, 3, tview.NewTableCell("").SetSelectable(false))
	}
}
