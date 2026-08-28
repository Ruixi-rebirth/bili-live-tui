package tui

import (
	"context"
	"fmt"
	"time"

	"github.com/rivo/tview"
)

func (w *roomManagerWorkspace) loadKeywords() {
	w.baseActionMode = "keyword"
	generation := w.showLoading("屏蔽词")
	w.reload = w.loadKeywords
	go func() {
		ctx, cancel := context.WithTimeout(w.deps.Context, 10*time.Second)
		defer cancel()
		result, err := w.deps.Client.GetRoomShieldKeywords(ctx, w.deps.RoomID, w.deps.Sessdata, w.deps.BiliJCT)
		w.deps.QueueUI(func() {
			if !w.visible || generation != w.generation {
				return
			}
			if err != nil {
				w.showError(generation, "屏蔽词", err, w.loadKeywords)
				return
			}
			w.showTableView()
			w.table.Clear()
			clear(w.tableActions)
			count := fmt.Sprintf("已添加 %d 个", len(result.Keywords))
			if result.MaxCount > 0 {
				count = fmt.Sprintf("已添加 %d/%d 个", len(result.Keywords), result.MaxCount)
			}
			w.section.SetText(count + " · 当前账号的观看屏蔽词")
			w.table.SetCell(0, 0, roomManagerTableHeaderCell("屏蔽词", 34, 2))
			w.table.SetCell(0, 1, roomManagerTableHeaderCell("操作", 8, 0).SetAlign(tview.AlignCenter))
			for index, keyword := range result.Keywords {
				action := func() {
					w.openConfirm("删除屏蔽词", "确定删除“"+tview.Escape(keyword)+"”吗？", func(ctx context.Context) error {
						return w.deps.Client.DeleteRoomShieldKeyword(ctx, w.deps.RoomID, keyword, w.deps.Sessdata, w.deps.BiliJCT)
					})
				}
				row := index + 1
				w.tableActions[row] = action
				w.table.SetCell(row, 0, roomManagerTableTextCell(keyword, 34, 2))
				w.table.SetCell(row, 1, roomManagerTableActionCell("删除", errorColor, action))
			}
			if len(result.Keywords) == 0 {
				w.showEmptyView("暂无屏蔽词，可点击下方按钮添加")
			} else {
				w.table.Select(1, 0).ScrollToBeginning()
			}
			w.restoreFocusAfterLoad()
		})
	}()
}
