package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"bili-live-tui/internal/api"
	"github.com/rivo/tview"
)

func (w *roomManagerWorkspace) loadAdmins() {
	generation := w.showLoading("房管")
	w.reload = w.loadAdmins
	requestedPage := w.adminPage
	if !w.seniorAdminKnown && !w.seniorAdminLoading && strings.TrimSpace(w.capabilities.AnchorID) != "" {
		w.seniorAdminLoading = true
		go func(anchorID string) {
			requestCtx, cancelRequest := context.WithTimeout(w.deps.Context, 6*time.Second)
			defer cancelRequest()
			status, err := w.deps.Client.GetRoomAdminSeniorStatus(requestCtx, anchorID, w.deps.Sessdata, w.deps.BiliJCT)
			w.deps.QueueUI(func() {
				w.seniorAdminLoading = false
				if err == nil {
					w.seniorAdminKnown = true
					w.seniorAdminEnabled = status > 0
				}
			})
		}(w.capabilities.AnchorID)
	}
	go func() {
		requestCtx, cancelRequest := context.WithTimeout(w.deps.Context, 10*time.Second)
		defer cancelRequest()
		result, err := w.deps.Client.GetRoomAdmins(requestCtx, requestedPage, w.deps.Sessdata, w.deps.BiliJCT)
		w.deps.QueueUI(func() {
			if err != nil {
				w.showError(generation, "房管", err, w.loadAdmins)
				return
			}
			if !w.visible || generation != w.generation {
				return
			}
			w.showTableView()
			w.table.Clear()
			clear(w.tableActions)
			clear(w.adminLevels)
			w.table.SetCell(0, 0, roomManagerTableHeaderCell("用户", 32, 2))
			w.table.SetCell(0, 1, roomManagerTableHeaderCell("身份", 10, 0))
			w.table.SetCell(0, 2, roomManagerTableHeaderCell("任命时间", 20, 1))
			w.table.SetCell(0, 3, roomManagerTableHeaderCell("操作", 8, 0).SetAlign(tview.AlignCenter))
			w.adminTotalPages = result.TotalPages
			if w.adminTotalPages < 1 {
				w.adminTotalPages = 1
			}
			adminCount := fmt.Sprintf("本页 %d 人", len(result.Items))
			if result.MaxCount > 0 {
				adminCount += fmt.Sprintf(" · 容量上限 %d 人", result.MaxCount)
			}
			adminCount += fmt.Sprintf(" · 第 %d/%d 页", w.adminPage, w.adminTotalPages)
			w.section.SetText(fmt.Sprintf("[%s]%s[-]", mutedColor.String(), adminCount))
			w.canPrevPage = w.adminPage > 1
			w.canNextPage = w.adminPage < w.adminTotalPages
			w.updateActionBar(w.baseActionMode)
			if len(result.Items) == 0 && w.adminPage == 1 {
				w.showEmptyView("暂无房管")
				w.restoreFocusAfterLoad()
				return
			}
			for index, admin := range result.Items {
				admin := admin
				row := index + 1
				if admin.LevelKnown {
					w.adminLevels[admin.UserID] = admin.Level
				}
				action := func() {
					w.showAdminChoices(api.RoomUserSearchResult{UserID: admin.UserID, Username: admin.Username, AdminLevel: admin.Level, AdminLevelKnown: admin.LevelKnown}, false)
				}
				level := "身份未知"
				if admin.LevelKnown && admin.Level == 1 {
					level = "普通房管"
				} else if admin.LevelKnown && admin.Level == 2 {
					level = "高级房管"
				}
				w.tableActions[row] = action
				w.table.SetCell(row, 0, roomManagerTableTextCell(formatRoomManagerTableUser(admin.Username, admin.UserID), 32, 2))
				w.table.SetCell(row, 1, roomManagerTableTextCell(level, 10, 0))
				w.table.SetCell(row, 2, roomManagerTableMutedCell(fallbackDanmakuManagementText(admin.AppointedAt, "时间未知"), 20, 1))
				w.table.SetCell(row, 3, roomManagerTableActionCell("管理", accentActiveColor, action))
			}
			if len(result.Items) == 0 {
				w.showEmptyView("本页没有记录")
				w.restoreFocusAfterLoad()
				return
			}
			w.table.Select(1, 0).ScrollToBeginning()
			w.restoreFocusAfterLoad()
		})
	}()
}

func (w *roomManagerWorkspace) loadMutedUsers() {
	generation := w.showLoading("禁言名单")
	w.reload = w.loadMutedUsers
	requestedPage := w.mutedPage
	go func() {
		requestCtx, cancelRequest := context.WithTimeout(w.deps.Context, 10*time.Second)
		defer cancelRequest()
		result, err := w.deps.Client.GetMutedRoomUsers(requestCtx, w.deps.RoomID, requestedPage, w.deps.Sessdata, w.deps.BiliJCT)
		w.deps.QueueUI(func() {
			if err != nil {
				w.showError(generation, "禁言名单", err, w.loadMutedUsers)
				return
			}
			if !w.visible || generation != w.generation {
				return
			}
			w.showTableView()
			w.table.Clear()
			clear(w.tableActions)
			w.table.SetCell(0, 0, roomManagerTableHeaderCell("用户", 30, 2))
			w.table.SetCell(0, 1, roomManagerTableHeaderCell("到期时间", 20, 1))
			w.table.SetCell(0, 2, roomManagerTableHeaderCell("操作者", 14, 1))
			w.table.SetCell(0, 3, roomManagerTableHeaderCell("操作", 8, 0).SetAlign(tview.AlignCenter))
			w.mutedTotalPages = result.TotalPages
			if w.mutedTotalPages < 1 {
				w.mutedTotalPages = 1
			}
			sectionText := fmt.Sprintf("第 %d/%d 页", w.mutedPage, w.mutedTotalPages)
			if result.Total > 0 {
				sectionText = fmt.Sprintf("共 %d 人 · %s", result.Total, sectionText)
			}
			w.section.SetText(fmt.Sprintf("[%s]%s[-]", mutedColor.String(), sectionText))
			w.canPrevPage = w.mutedPage > 1
			w.canNextPage = w.mutedPage < w.mutedTotalPages
			w.updateActionBar(w.baseActionMode)
			if len(result.Items) == 0 && w.mutedPage == 1 {
				w.showEmptyView("暂无被禁言用户")
				w.restoreFocusAfterLoad()
				return
			}
			for index, item := range result.Items {
				item := item
				row := index + 1
				targetLevel, targetLevelKnown := w.adminLevels[item.UserID]
				canMute := w.capabilities.CanMuteUser(targetLevel, targetLevelKnown)
				var action func()
				if canMute {
					action = func() {
						w.openConfirm("解除禁言", "确定解除 "+displayDanmakuManagedUser(item.Username, item.UserID)+" 的禁言吗？", func(actionCtx context.Context) error {
							return w.deps.Client.UnmuteRoomUser(actionCtx, w.deps.RoomID, item.UserID, w.deps.Sessdata, w.deps.BiliJCT)
						})
					}
					w.tableActions[row] = action
				}
				operator := "执行人未知"
				if item.OperatorIsAnchor {
					operator = "主播"
				} else if strings.TrimSpace(item.OperatorName) != "" {
					operator = item.OperatorName
				}
				w.table.SetCell(row, 0, roomManagerTableTextCell(formatRoomManagerTableUser(item.Username, item.UserID), 30, 2))
				w.table.SetCell(row, 1, roomManagerTableMutedCell(fallbackDanmakuManagementText(item.ExpiresAt, "上游未提供"), 20, 1))
				w.table.SetCell(row, 2, roomManagerTableMutedCell(tview.Escape(operator), 14, 1))
				if canMute {
					w.table.SetCell(row, 3, roomManagerTableActionCell("解除", errorColor, action))
				} else {
					w.table.SetCell(row, 3, roomManagerTableMutedCell("不可操作", 8, 0).SetAlign(tview.AlignCenter))
				}
			}
			if len(result.Items) == 0 {
				w.showEmptyView("本页没有记录")
				w.restoreFocusAfterLoad()
				return
			}
			w.table.Select(1, 0).ScrollToBeginning()
			w.restoreFocusAfterLoad()
		})
	}()
}

func (w *roomManagerWorkspace) loadBlacklistedUsers() {
	generation := w.showLoading("直播间黑名单")
	w.reload = w.loadBlacklistedUsers
	requestedPage, anchorID := w.blacklistPage, w.capabilities.AnchorID
	go func() {
		requestCtx, cancelRequest := context.WithTimeout(w.deps.Context, 10*time.Second)
		defer cancelRequest()
		result, err := w.deps.Client.GetRoomBlacklist(requestCtx, anchorID, requestedPage, 50, w.deps.Sessdata, w.deps.BiliJCT)
		w.deps.QueueUI(func() {
			if err != nil {
				w.showError(generation, "直播间黑名单", err, w.loadBlacklistedUsers)
				return
			}
			if !w.visible || generation != w.generation {
				return
			}
			w.showTableView()
			w.table.Clear()
			clear(w.tableActions)
			w.table.SetCell(0, 0, roomManagerTableHeaderCell("用户", 34, 2))
			w.table.SetCell(0, 1, roomManagerTableHeaderCell("加入时间", 22, 1))
			w.table.SetCell(0, 2, roomManagerTableHeaderCell("操作", 8, 0).SetAlign(tview.AlignCenter))
			w.blacklistTotalPages = result.TotalPages
			if w.blacklistTotalPages < 1 && result.Total > 0 {
				w.blacklistTotalPages = (result.Total + 49) / 50
			}
			if w.blacklistTotalPages < 1 {
				w.blacklistTotalPages = 1
			}
			sectionText := fmt.Sprintf("第 %d/%d 页", w.blacklistPage, w.blacklistTotalPages)
			if result.Total > 0 {
				sectionText = fmt.Sprintf("共 %d 人 · %s", result.Total, sectionText)
			}
			w.section.SetText(fmt.Sprintf("[%s]%s[-]", mutedColor.String(), sectionText))
			w.canPrevPage = w.blacklistPage > 1
			w.canNextPage = len(result.Items) == 50 || result.Total > w.blacklistPage*50 || result.TotalPages > w.blacklistPage
			w.updateActionBar(w.baseActionMode)
			if len(result.Items) == 0 && w.blacklistPage == 1 {
				w.showEmptyView("黑名单为空")
				w.restoreFocusAfterLoad()
				return
			}
			for index, item := range result.Items {
				item := item
				row := index + 1
				action := func() {
					w.openConfirm("移出黑名单", "确定将 "+displayDanmakuManagedUser(item.Username, item.UserID)+" 移出直播间黑名单吗？", func(actionCtx context.Context) error {
						return w.deps.Client.UnblacklistRoomUser(actionCtx, anchorID, item.UserID, w.deps.Sessdata, w.deps.BiliJCT)
					})
				}
				w.tableActions[row] = action
				w.table.SetCell(row, 0, roomManagerTableTextCell(formatRoomManagerTableUser(item.Username, item.UserID), 34, 2))
				w.table.SetCell(row, 1, roomManagerTableMutedCell(fallbackDanmakuManagementText(item.CreatedAt, "时间未知"), 22, 1))
				w.table.SetCell(row, 2, roomManagerTableActionCell("移出", errorColor, action))
			}
			if len(result.Items) == 0 {
				w.showEmptyView("本页没有记录")
				w.restoreFocusAfterLoad()
				return
			}
			w.table.Select(1, 0).ScrollToBeginning()
			w.restoreFocusAfterLoad()
		})
	}()
}

func (w *roomManagerWorkspace) loadRoomSilent() {
	w.triggerCloseRoomSilent = nil
	w.baseActionMode = "default"
	generation := w.showLoading("全局禁言")
	w.reload = w.loadRoomSilent
	go func() {
		requestCtx, cancelRequest := context.WithTimeout(w.deps.Context, 10*time.Second)
		defer cancelRequest()
		state, err := w.deps.Client.GetRoomSilentState(requestCtx, w.deps.RoomID, w.deps.Sessdata, w.deps.BiliJCT)
		w.deps.QueueUI(func() {
			if err != nil {
				w.showError(generation, "全局禁言", err, w.loadRoomSilent)
				return
			}
			if !w.visible || generation != w.generation {
				return
			}
			w.showTableView()
			w.table.Clear()
			clear(w.tableActions)
			w.canPrevPage = false
			w.canNextPage = false
			if state.Enabled {
				w.section.SetText(fmt.Sprintf("[%s::b]已开启[-:-:-]", accentActiveColor.String()))
				w.triggerCloseRoomSilent = func() {
					w.openConfirm("关闭全局禁言", "确定关闭直播间全局禁言吗？", func(actionCtx context.Context) error {
						return w.deps.Client.SetRoomSilentState(actionCtx, w.deps.RoomID, api.RoomSilentOff, 1, 0, w.deps.Sessdata, w.deps.BiliJCT)
					})
				}
				w.baseActionMode = "silent-active"
				w.showEmptyView(formatRoomSilentState(state))
				w.updateActionBar(w.baseActionMode)
				w.restoreFocusAfterLoad()
				return
			}
			w.section.SetText(fmt.Sprintf("[%s]当前未开启[-]", mutedColor.String()))
			w.table.SetCell(0, 0, roomManagerTableHeaderCell("规则", 22, 1))
			w.table.SetCell(0, 1, roomManagerTableHeaderCell("影响范围", 46, 2))
			w.table.SetCell(0, 2, roomManagerTableHeaderCell("操作", 8, 0).SetAlign(tview.AlignCenter))
			addSilentChoice := func(row int, label, detail, actionLabel, audience string, level int) {
				action := func() {
					w.openConfirm("开启全局禁言", "确定开启“"+label+"”全局禁言吗？", func(actionCtx context.Context) error {
						return w.deps.Client.SetRoomSilentState(actionCtx, w.deps.RoomID, audience, level, 0, w.deps.Sessdata, w.deps.BiliJCT)
					})
				}
				w.tableActions[row] = action
				w.table.SetCell(row, 0, roomManagerTableTextCell(label, 22, 1))
				w.table.SetCell(row, 1, roomManagerTableMutedCell(detail, 46, 2))
				w.table.SetCell(row, 2, roomManagerTableActionCell(actionLabel, accentActiveColor, action))
			}
			addSilentChoice(1, "全员禁言", "除主播和房管外，所有观众均不可发言", "开启", api.RoomSilentAll, 1)
			addSilentChoice(2, "仅粉丝发言", "未关注本直播间的观众不可发言", "开启", api.RoomSilentNonFans, 1)
			wealthAction := func() {
				w.openInput("荣耀等级禁言", "等级 ", "1", 0, "保存", func(value string) error {
					_, err := parseDanmakuManagementLevel(value)
					return err
				}, func(value string) {
					level, _ := parseDanmakuManagementLevel(value)
					w.openConfirm("开启全局禁言", fmt.Sprintf("确定禁止荣耀等级低于 %d 的用户发言吗？", level), func(actionCtx context.Context) error {
						return w.deps.Client.SetRoomSilentState(actionCtx, w.deps.RoomID, api.RoomSilentWealth, level, 0, w.deps.Sessdata, w.deps.BiliJCT)
					})
				})
			}
			w.tableActions[3] = wealthAction
			w.table.SetCell(3, 0, roomManagerTableTextCell("荣耀等级限制", 22, 1))
			w.table.SetCell(3, 1, roomManagerTableMutedCell("低于所填荣耀等级的用户不可发言", 46, 2))
			w.table.SetCell(3, 2, roomManagerTableActionCell("配置", accentActiveColor, wealthAction))
			medalAction := func() {
				w.openInput("粉丝勋章禁言", "等级 ", "1", 0, "保存", func(value string) error {
					_, err := parseDanmakuManagementLevel(value)
					return err
				}, func(value string) {
					level, _ := parseDanmakuManagementLevel(value)
					w.openConfirm("开启全局禁言", fmt.Sprintf("确定禁止粉丝牌等级低于 %d 的用户发言吗？", level), func(actionCtx context.Context) error {
						return w.deps.Client.SetRoomSilentState(actionCtx, w.deps.RoomID, api.RoomSilentMedal, level, 0, w.deps.Sessdata, w.deps.BiliJCT)
					})
				})
			}
			w.tableActions[4] = medalAction
			w.table.SetCell(4, 0, roomManagerTableTextCell("粉丝勋章限制", 22, 1))
			w.table.SetCell(4, 1, roomManagerTableMutedCell("无勋章或低于所填等级的用户不可发言", 46, 2))
			w.table.SetCell(4, 2, roomManagerTableActionCell("配置", accentActiveColor, medalAction))
			addSilentChoice(5, "除房管以外的观众", "仅主播和房管可以发言", "开启", api.RoomSilentNonMember, 1)
			w.baseActionMode = "default"
			w.updateActionBar(w.baseActionMode)
			w.table.Select(1, 0).ScrollToBeginning()
			w.restoreFocusAfterLoad()
		})
	}()
}

func (w *roomManagerWorkspace) openUserSearch(operation roomManagerUserOperation) {
	title := "查找用户"
	switch operation {
	case roomManagerUserAddAdmin:
		title = "添加房管"
	case roomManagerUserMute:
		title = "添加禁言用户"
	case roomManagerUserBlacklist:
		title = "添加黑名单用户"
	}
	w.openInput(title, "UID 或用户名 ", "", 50, "查找", func(value string) error {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("请输入 UID 或用户名。")
		}
		return nil
	}, func(value string) {
		w.searchUsers(operation, value)
	})
}

func (w *roomManagerWorkspace) searchUsers(operation roomManagerUserOperation, query string) {
	query = strings.TrimSpace(query)
	w.searchOp = operation
	w.searchQuery = query
	w.focusContentAfterLoad = true
	generation := w.showLoading("查找用户")
	w.section.SetText(fmt.Sprintf("[%s]正在查找 %s……[-]", mutedColor.String(), tview.Escape(query)))
	w.setFlowBack("返回列表", w.returnToList, false)
	go func() {
		requestCtx, cancelRequest := context.WithTimeout(w.deps.Context, 10*time.Second)
		defer cancelRequest()
		results, err := w.deps.Client.SearchRoomUsers(requestCtx, query, w.deps.Sessdata, w.deps.BiliJCT)
		w.deps.QueueUI(func() {
			if !w.visible || generation != w.generation {
				return
			}
			if err != nil {
				w.section.SetText(fmt.Sprintf("[%s]查找失败[-]", errorColor.String()))
				w.setNotice(compactDanmakuManagementError(err), true)
				w.showEmptyView("未能完成用户查找")
				w.setFlowBack("返回列表", w.returnToList, true)
				w.restoreFocusAfterLoad()
				return
			}
			w.searchResults = results
			w.renderSearchResults()
		})
	}()
}

func (w *roomManagerWorkspace) renderSearchResults() {
	w.showTableView()
	w.table.Clear()
	clear(w.tableActions)
	w.section.SetText(fmt.Sprintf("[%s::b]搜索结果[-:-:-] [%s]· %s[-]", tview.Styles.PrimaryTextColor.String(), mutedColor.String(), tview.Escape(w.searchQuery)))
	w.setNotice("请核对用户名和 UID 后再操作。", false)
	w.table.SetCell(0, 0, roomManagerTableHeaderCell("用户", 32, 2))
	w.table.SetCell(0, 1, roomManagerTableHeaderCell("UID", 20, 1))
	w.table.SetCell(0, 2, roomManagerTableHeaderCell("状态", 12, 0))
	w.table.SetCell(0, 3, roomManagerTableHeaderCell("操作", 8, 0).SetAlign(tview.AlignCenter))
	w.setFlowBack("返回列表", w.returnToList, true)
	if len(w.searchResults) == 0 {
		w.showEmptyView("未找到匹配用户")
		w.updateActionBar("flow")
		w.restoreFocusAfterLoad()
		return
	}
	for index, result := range w.searchResults {
		result := result
		row := index + 1
		currentLevel := result.AdminLevel
		currentLevelKnown := result.AdminLevelKnown
		if knownLevel, ok := w.adminLevels[result.UserID]; ok {
			currentLevel = knownLevel
			currentLevelKnown = true
			result.AdminLevel = knownLevel
			result.AdminLevelKnown = true
		}
		statusText := "可操作"
		actionLabel := "选择"
		canOperate := true
		switch {
		case result.UserID == strings.TrimSpace(w.capabilities.UserID):
			statusText, canOperate = "当前账号", false
		case result.UserID == strings.TrimSpace(w.capabilities.AnchorID):
			statusText, canOperate = "主播", false
		case w.searchOp == roomManagerUserMute && !w.capabilities.CanMuteUser(currentLevel, currentLevelKnown):
			if !currentLevelKnown && !w.capabilities.IsAnchor {
				statusText = "身份未知"
			} else {
				statusText = "无权禁言"
			}
			canOperate = false
		case w.searchOp == roomManagerUserBlacklist && strings.TrimSpace(w.capabilities.AnchorID) == "":
			statusText, canOperate = "主播未知", false
		case w.searchOp == roomManagerUserBlacklist && !w.capabilities.CanBlacklistUser(currentLevel, currentLevelKnown):
			if !currentLevelKnown && !w.capabilities.IsAnchor {
				statusText = "身份未知"
			} else {
				statusText = "无权拉黑"
			}
			canOperate = false
		case w.searchOp == roomManagerUserAddAdmin && currentLevelKnown && currentLevel == 1:
			statusText, actionLabel = "普通房管", "管理"
		case w.searchOp == roomManagerUserAddAdmin && currentLevelKnown && currentLevel == 2:
			statusText, actionLabel = "高级房管", "管理"
		}
		var action func()
		if canOperate {
			action = func() {
				switch w.searchOp {
				case roomManagerUserAddAdmin:
					w.showAdminChoices(result, true)
				case roomManagerUserMute:
					w.showMuteChoices(result)
				case roomManagerUserBlacklist:
					w.openConfirm("添加黑名单用户", "确定将 "+displayDanmakuManagedUser(result.Username, result.UserID)+" 添加到直播间黑名单吗？", func(actionCtx context.Context) error {
						return w.deps.Client.BlacklistRoomUser(actionCtx, w.capabilities.AnchorID, result.UserID, w.deps.Sessdata, w.deps.BiliJCT)
					})
				}
			}
			w.tableActions[row] = action
		}
		w.table.SetCell(row, 0, roomManagerTableTextCell(fallbackDanmakuManagementText(result.Username, "用户名未知"), 32, 2))
		w.table.SetCell(row, 1, roomManagerTableMutedCell(tview.Escape(result.UserID), 20, 1))
		w.table.SetCell(row, 2, roomManagerTableMutedCell(statusText, 12, 0))
		if canOperate {
			w.table.SetCell(row, 3, roomManagerTableActionCell(actionLabel, accentActiveColor, action))
		} else {
			w.table.SetCell(row, 3, roomManagerTableMutedCell("不可操作", 8, 0).SetAlign(tview.AlignCenter))
		}
	}
	w.table.Select(1, 0).ScrollToBeginning()
	w.restoreFocusAfterLoad()
}

func (w *roomManagerWorkspace) showAdminChoices(target api.RoomUserSearchResult, fromSearch bool) {
	w.showTableView()
	w.table.Clear()
	clear(w.tableActions)
	currentLevel := target.AdminLevel
	currentLevelKnown := target.AdminLevelKnown
	if knownLevel, ok := w.adminLevels[target.UserID]; ok {
		currentLevel = knownLevel
		currentLevelKnown = true
	}
	w.section.SetText(fmt.Sprintf("[%s::b]管理房管[-:-:-] [%s]· %s[-]", tview.Styles.PrimaryTextColor.String(), mutedColor.String(), displayDanmakuManagedUser(target.Username, target.UserID)))
	w.setNotice("", false)
	w.table.SetCell(0, 0, roomManagerTableHeaderCell("房管身份", 18, 1))
	w.table.SetCell(0, 1, roomManagerTableHeaderCell("说明", 42, 2))
	w.table.SetCell(0, 2, roomManagerTableHeaderCell("操作", 8, 0).SetAlign(tview.AlignCenter))
	addLevel := func(row, level int, label, detail string) {
		w.table.SetCell(row, 0, roomManagerTableTextCell(label, 18, 1))
		w.table.SetCell(row, 1, roomManagerTableMutedCell(detail, 42, 2))
		if currentLevelKnown && currentLevel == level {
			w.table.SetCell(row, 2, roomManagerTableMutedCell("当前", 8, 0).SetAlign(tview.AlignCenter))
			return
		}
		actionLabel := "任命"
		if currentLevelKnown && currentLevel > 0 {
			actionLabel = "调整"
		}
		action := func() {
			w.openConfirm(actionLabel+"房管", "确定将 "+displayDanmakuManagedUser(target.Username, target.UserID)+" 设为"+label+"吗？", func(actionCtx context.Context) error {
				return w.deps.Client.AppointRoomAdmin(actionCtx, target.UserID, level, w.deps.Sessdata, w.deps.BiliJCT)
			})
		}
		w.tableActions[row] = action
		w.table.SetCell(row, 2, roomManagerTableActionCell(actionLabel, accentActiveColor, action))
	}
	row := 1
	addLevel(row, 1, "普通房管", "授予普通房管身份")
	row++
	if w.seniorAdminEnabled || currentLevel == 2 {
		addLevel(row, 2, "高级房管", "授予高级房管身份")
		row++
	} else if !w.seniorAdminKnown {
		w.setNotice("未能确认高级房管功能，暂只提供普通房管。", false)
	}
	if (currentLevelKnown && currentLevel > 0) || !fromSearch {
		revokeRow := row
		action := func() {
			w.openConfirm("撤销房管", "确定撤销 "+displayDanmakuManagedUser(target.Username, target.UserID)+" 的房管权限吗？", func(actionCtx context.Context) error {
				return w.deps.Client.DismissRoomAdmin(actionCtx, target.UserID, w.deps.Sessdata, w.deps.BiliJCT)
			})
		}
		w.tableActions[revokeRow] = action
		w.table.SetCell(revokeRow, 0, roomManagerTableTextCell("撤销房管", 18, 1))
		w.table.SetCell(revokeRow, 1, roomManagerTableMutedCell("移除该用户的直播间管理权限", 42, 2))
		w.table.SetCell(revokeRow, 2, roomManagerTableActionCell("撤销", errorColor, action))
	}
	if fromSearch {
		w.setFlowBack("返回搜索结果", w.renderSearchResults, false)
	} else {
		w.setFlowBack("返回房管列表", w.returnToList, false)
	}
	w.table.Select(1, 0).ScrollToBeginning()
}

func (w *roomManagerWorkspace) showMuteChoices(target api.RoomUserSearchResult) {
	w.showTableView()
	w.table.Clear()
	clear(w.tableActions)
	w.section.SetText(fmt.Sprintf("[%s::b]禁言用户[-:-:-] [%s]· %s[-]", tview.Styles.PrimaryTextColor.String(), mutedColor.String(), displayDanmakuManagedUser(target.Username, target.UserID)))
	w.setNotice("", false)
	w.table.SetCell(0, 0, roomManagerTableHeaderCell("禁言时长", 18, 1))
	w.table.SetCell(0, 1, roomManagerTableHeaderCell("说明", 42, 2))
	w.table.SetCell(0, 2, roomManagerTableHeaderCell("操作", 8, 0).SetAlign(tview.AlignCenter))
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
			w.openConfirm("禁言用户", "确定禁言 "+displayDanmakuManagedUser(target.Username, target.UserID)+"（"+item.label+"）吗？", func(actionCtx context.Context) error {
				return w.deps.Client.MuteRoomUser(actionCtx, w.deps.RoomID, target.UserID, "", item.duration, w.deps.Sessdata, w.deps.BiliJCT)
			})
		}
		w.tableActions[row] = action
		w.table.SetCell(row, 0, roomManagerTableTextCell(item.label, 18, 1))
		w.table.SetCell(row, 1, roomManagerTableMutedCell(item.detail, 42, 2))
		w.table.SetCell(row, 2, roomManagerTableActionCell("选择", accentActiveColor, action))
	}
	w.setFlowBack("返回搜索结果", w.renderSearchResults, false)
	w.table.Select(1, 0).ScrollToBeginning()
}
