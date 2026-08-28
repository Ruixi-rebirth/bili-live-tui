package tui

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"bili-live-tui/internal/api"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

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
		} else if len(snapshot.guardMembers) == 0 {
			content.WriteString("\n当前直播间暂无大航海成员。")
		} else {
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
				fmt.Fprintf(&content, "\n\n[%s]大航海刷新失败，暂时显示上次结果。[-]", mutedColor.String())
			}
		}
	} else {
		if !snapshot.viewerKnown && len(snapshot.onlineRank) == 0 {
			if snapshot.onlineRankError != "" {
				content.WriteString("\n在线信息暂不可用，正在自动重试……")
			} else {
				content.WriteString("\n正在获取在线人数……")
			}
		} else if len(snapshot.onlineRank) == 0 {
			content.WriteString("\n当前高能榜暂无成员。")
		} else {
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
	}
	row, col := view.GetScrollOffset()
	view.SetText(content.String())
	view.ScrollTo(row, col)
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
