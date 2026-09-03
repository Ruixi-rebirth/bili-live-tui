package tui

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"bili-live-tui/internal/api"
	streamruntime "bili-live-tui/internal/stream"
	"github.com/gabriel-vasile/mimetype"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

const maxCoverSourceBytes int64 = 64 * 1024 * 1024

type liveFormState struct {
	title                *tview.InputField
	description          *tview.TextArea
	announcement         *tview.TextArea
	tags                 *tview.InputField
	area                 *areaField
	cover                *tview.InputField
	streamMode           *autoOpenDropDown
	orientation          *autoOpenDropDown
	obsPassword          *tview.InputField
	tagIDsJSON           string
	hasExistingCover     bool
	streamOptionsVisible bool
	initialStreamMode    string
}

// focusedLabelItem 让表单只突出显示当前获得焦点的字段标签。
type focusedLabelItem struct {
	tview.FormItem
}

// formClippedTextArea 把多行字段限制在表单内边框中。
// tview.Form 滚动时可能把只显示一部分的字段放到边框之外。
type formClippedTextArea struct {
	tview.FormItem
	field *tview.TextArea
	form  *tview.Form
}

func (item *formClippedTextArea) Draw(screen tcell.Screen) {
	x, y, width, height := item.field.GetRect()
	clipX, clipY, clipWidth, clipHeight := item.form.GetInnerRect()
	left := max(x, clipX)
	top := max(y, clipY)
	right := min(x+width, clipX+clipWidth)
	bottom := min(y+height, clipY+clipHeight)
	if left >= right || top >= bottom {
		return
	}
	item.field.SetRect(left, top, right-left, bottom-top)
	item.FormItem.Draw(screen)
	item.field.SetRect(x, y, width, height)
}

func clipTextAreaToForm(form *tview.Form, field *tview.TextArea) tview.FormItem {
	return &formClippedTextArea{
		FormItem: focusedLabelTextArea(field),
		field:    field,
		form:     form,
	}
}

func (i *focusedLabelItem) SetFormAttributes(labelWidth int, labelColor, bgColor, fieldTextColor, fieldBgColor tcell.Color) tview.FormItem {
	var dropDown tview.FormItem
	switch i.FormItem.(type) {
	case *tview.InputField, *tview.TextArea:
		if i.HasFocus() {
			fieldBgColor = formFieldFocusColor
		} else {
			fieldBgColor = formFieldColor
		}
	case *tview.DropDown, *autoOpenDropDown:
		fieldBgColor = formSelectColor
		dropDown = i.FormItem
	}
	i.FormItem.SetFormAttributes(labelWidth, labelColor, bgColor, fieldTextColor, fieldBgColor)
	// 保持下拉框关闭时的文字颜色与展开列表一致。
	// Form.SetFormAttributes 会把关闭状态恢复为通用输入框颜色，因此这里重新设置。
	switch field := dropDown.(type) {
	case *tview.DropDown:
		field.SetFieldTextColor(autocompleteTextColor)
	case *autoOpenDropDown:
		field.SetFieldTextColor(autocompleteTextColor)
	}
	styleLabel(i.FormItem, i.HasFocus())
	return i
}

func focusedLabelInput(field *tview.InputField) tview.FormItem {
	return &focusedLabelItem{FormItem: field}
}

func focusedLabelTextArea(field *tview.TextArea) tview.FormItem {
	return &focusedLabelItem{FormItem: field}
}

func focusedLabelDropDown(field tview.FormItem) tview.FormItem {
	return &focusedLabelItem{FormItem: field}
}

func styleLabel(item tview.FormItem, focused bool) {
	var style tcell.Style
	switch field := item.(type) {
	case *tview.InputField:
		style = field.GetLabelStyle()
		field.SetLabelStyle(style.Foreground(labelColorForFocus(focused)).Bold(focused))
	case *tview.TextArea:
		style = field.GetLabelStyle()
		field.SetLabelStyle(style.Foreground(labelColorForFocus(focused)).Bold(focused))
	case *tview.DropDown:
		field.SetLabelStyle(tcell.StyleDefault.Foreground(labelColorForFocus(focused)).Bold(focused))
	case *autoOpenDropDown:
		field.SetLabelStyle(tcell.StyleDefault.Foreground(labelColorForFocus(focused)).Bold(focused))
	}
}

func labelColorForFocus(focused bool) tcell.Color {
	if focused {
		return accentActiveColor
	}
	return tview.Styles.SecondaryTextColor
}

// newLiveFormWithSettings 创建开播前和直播中编辑资料共用的表单。
// 两个页面共用一套表单定义，确保校验和显示方式一致。
func newLiveFormWithSettings(areas []api.LiveArea, initial *api.LiveSettings, title string) (*tview.Form, *liveFormState) {
	return newLiveFormWithOptions(areas, initial, title, false)
}

func newLiveFormWithOptions(areas []api.LiveArea, initial *api.LiveSettings, title string, showStreamOptions bool) (*tview.Form, *liveFormState) {
	hasExistingCover := initial != nil && strings.TrimSpace(initial.CoverPath) != ""
	coverPlaceholder := "图片路径或 URL"
	if hasExistingCover {
		coverPlaceholder = "留空沿用已上传封面，或填写新图片"
	}
	state := &liveFormState{
		hasExistingCover:     hasExistingCover,
		streamOptionsVisible: showStreamOptions,
		title: tview.NewInputField().
			SetLabel("直播标题").
			SetText("我的直播").
			SetPlaceholder("例如：今晚一起玩游戏"),
		description: tview.NewTextArea().
			SetLabel("直播简介").
			SetPlaceholder("可选：介绍一下今天的直播内容").
			SetSize(3, 0),
		announcement: tview.NewTextArea().
			SetLabel("直播公告").
			SetPlaceholder("可选：显示在直播间公告区域").
			SetSize(2, 0),
		tags: tview.NewInputField().
			SetLabel("直播标签").
			SetPlaceholder("可选，例如：游戏，聊天，音乐"),
		area: newAreaField(areas),
		cover: tview.NewInputField().
			SetLabel("直播封面").
			SetPlaceholder(coverPlaceholder).
			SetAcceptanceFunc(tview.InputFieldMaxLength(500)),
		streamMode:  newStreamModeDropDown(),
		orientation: newOrientationDropDown(),
		obsPassword: tview.NewInputField().
			SetLabel("OBS WebSocket 密码").
			SetPlaceholder("OBS 未启用密码时留空").
			SetMaskCharacter('*').
			SetAcceptanceFunc(tview.InputFieldMaxLength(200)),
	}
	if initial != nil {
		state.initialStreamMode = initial.StreamMode
		if strings.TrimSpace(initial.Title) != "" {
			state.title.SetText(initial.Title)
		}
		state.description.SetText(initial.Description, false)
		state.announcement.SetText(initial.Announcement, false)
		state.tags.SetText(initial.Tags)
		state.cover.SetText(initial.CoverPath)
		state.area.setID(initial.AreaID)
		state.obsPassword.SetText(initial.OBSPassword)
		state.tagIDsJSON = initial.TagIDsJSON
		if initial.StreamMode == streamruntime.ModeFFmpegTest {
			state.streamMode.SetCurrentOption(1)
		}
		if initial.Orientation == api.OrientationPortrait {
			state.orientation.SetCurrentOption(1)
		}
	}

	form := styleForm(tview.NewForm(), title)
	form.AddFormItem(focusedLabelInput(state.title)).
		AddFormItem(clipTextAreaToForm(form, state.description)).
		AddFormItem(clipTextAreaToForm(form, state.announcement)).
		AddFormItem(focusedLabelInput(state.tags)).
		AddFormItem(focusedLabelInput(state.area.field)).
		AddFormItem(focusedLabelInput(state.cover)).
		AddFormItem(focusedLabelDropDown(state.orientation))
	if showStreamOptions {
		form.AddFormItem(focusedLabelDropDown(state.streamMode))
		syncOBSOptions := func(index int) {
			passwordIndex := form.GetFormItemIndex(state.obsPassword.GetLabel())
			if index == 1 {
				if passwordIndex >= 0 {
					form.RemoveFormItem(passwordIndex)
				}
				return
			}
			if passwordIndex < 0 {
				form.AddFormItem(focusedLabelInput(state.obsPassword))
			}
		}
		state.streamMode.SetSelectedFunc(func(_ string, index int) {
			syncOBSOptions(index)
		})
		index, _ := state.streamMode.GetCurrentOption()
		syncOBSOptions(index)
	}
	return form, state
}

func (s *liveFormState) settings() api.LiveSettings {
	areaID := s.area.id()
	streamMode := streamruntime.ModeOBS
	if !s.streamOptionsVisible {
		streamMode = s.initialStreamMode
	} else if index, _ := s.streamMode.GetCurrentOption(); index == 1 {
		streamMode = streamruntime.ModeFFmpegTest
	}
	obsPassword := s.obsPassword.GetText()
	if s.streamOptionsVisible && streamMode != streamruntime.ModeOBS {
		obsPassword = ""
	}
	orientation := api.OrientationLandscape
	if index, _ := s.orientation.GetCurrentOption(); index == 1 {
		orientation = api.OrientationPortrait
	}
	return api.LiveSettings{
		Title:        strings.TrimSpace(s.title.GetText()),
		Description:  strings.TrimSpace(s.description.GetText()),
		Announcement: strings.TrimSpace(s.announcement.GetText()),
		Tags:         normalizeTags(s.tags.GetText()),
		AreaID:       areaID,
		CoverPath:    normalizeCoverPath(s.cover.GetText()),
		StreamMode:   streamMode,
		OBSPassword:  obsPassword,
		Orientation:  orientation,
		TagIDsJSON:   s.tagIDsJSON,
	}
}

// autoOpenDropDown 是常规的终端选择器：Tab 聚焦后立即打开选项，
// 用户可直接用上下方向键和 Enter 选择，不需要先按退格键。
// 列表颜色与上方分区自动补全弹窗保持一致。
type autoOpenDropDown struct {
	*tview.DropDown
}

func newStreamModeDropDown() *autoOpenDropDown {
	return newStyledDropDown("推流方式", []string{"OBS Studio（推荐）", "FFmpeg 测试画面"})
}

func newOrientationDropDown() *autoOpenDropDown {
	return newStyledDropDown("推流方向", []string{"横屏", "竖屏"})
}

func newStyledDropDown(label string, options []string) *autoOpenDropDown {
	dropDown := tview.NewDropDown().
		SetLabel(label).
		SetOptions(options, nil).
		SetCurrentOption(0).
		SetFieldTextColor(autocompleteTextColor).
		SetListStyles(
			tcell.StyleDefault.Foreground(autocompleteTextColor).Background(autocompleteColor),
			tcell.StyleDefault.Foreground(autocompleteSelectedTextColor).Background(autocompleteSelectedColor),
		)
	return &autoOpenDropDown{DropDown: dropDown}
}

func (d *autoOpenDropDown) Focus(delegate func(tview.Primitive)) {
	d.DropDown.Focus(delegate)
	if d.IsOpen() {
		return
	}
	if handler := d.DropDown.InputHandler(); handler != nil {
		// 空选择前缀上的退格键不会改变当前选项，但可以打开 tview 列表。
		handler(tcell.NewEventKey(tcell.KeyBackspace, 0, tcell.ModNone), delegate)
	}
}

// validateCoverInput 在关闭设置页面前执行本地可完成的检查。
// 远程地址在提交阶段下载，本地文件在表单内提前校验。
// 这样可以尽早给出明确错误，避免房间资料已修改后才失败。
func validateCoverInput(value string, hasExistingCover bool) error {
	value = strings.TrimSpace(value)
	if value == "" {
		if hasExistingCover {
			return nil
		}
		return fmt.Errorf("首次开播请设置直播封面")
	}
	if parsed, err := url.ParseRequestURI(value); err == nil &&
		(strings.EqualFold(parsed.Scheme, "http") || strings.EqualFold(parsed.Scheme, "https")) {
		if parsed.Host == "" {
			return fmt.Errorf("直播封面 URL 缺少域名")
		}
		return nil
	}

	path := normalizeCoverPath(value)
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("找不到直播封面文件：%s", path)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("直播封面必须是图片文件")
	}
	if info.Size() > maxCoverSourceBytes {
		return fmt.Errorf("直播封面源文件不能超过 64 MB")
	}
	detectedMIME, err := mimetype.DetectFile(path)
	if err != nil {
		return fmt.Errorf("识别直播封面失败: %w", err)
	}
	if detectedMIME.Is("image/jpeg") || detectedMIME.Is("image/png") || detectedMIME.Is("image/webp") {
		return nil
	}
	return fmt.Errorf("直播封面实际格式不受支持：%s", detectedMIME.String())
}

func normalizeCoverPath(value string) string {
	value = strings.TrimSpace(value)
	if value == "~" || strings.HasPrefix(value, "~/") || strings.HasPrefix(value, `~\`) {
		if home, err := os.UserHomeDir(); err == nil {
			if value == "~" {
				return home
			}
			return filepath.Join(home, value[2:])
		}
	}
	return value
}

func normalizeTags(value string) string {
	parts := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == '，' || r == ';' || r == '；'
	})
	seen := make(map[string]struct{}, len(parts))
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if _, exists := seen[part]; exists {
			continue
		}
		seen[part] = struct{}{}
		result = append(result, part)
	}
	return strings.Join(result, ",")
}

type areaOption struct {
	label string
	id    string
}

type areaField struct {
	field        *tview.InputField
	options      []areaOption
	selected     string
	selectedText string
}

func (a *areaField) setID(id string) {
	id = strings.TrimSpace(id)
	if id == "" {
		return
	}
	for _, option := range a.options {
		if option.id != id {
			continue
		}
		a.field.SetText(option.label)
		a.selected = option.id
		a.selectedText = option.label
		return
	}
	// 即使最新分区列表中没有服务器返回的分区，也保留这个分区编号。
	// 表单的 id() 方法支持直接接受数字编号。
	a.field.SetText(id)
	a.selected = id
	a.selectedText = id
}

func newAreaField(areas []api.LiveArea) *areaField {
	chooser := &areaField{options: make([]areaOption, 0, len(areas))}
	for _, area := range areas {
		label := strings.TrimSpace(area.Name)
		id := strings.TrimSpace(area.ID)
		if label == "" || id == "" {
			continue
		}
		chooser.options = append(chooser.options, areaOption{label: label, id: id})
	}

	chooser.field = tview.NewInputField().
		SetLabel("直播分区").
		SetPlaceholder("输入关键词搜索分区").
		SetAcceptanceFunc(tview.InputFieldMaxLength(80)).
		SetAutocompleteStyles(
			autocompleteColor,
			tcell.StyleDefault.Foreground(autocompleteTextColor).Background(autocompleteColor),
			tcell.StyleDefault.Foreground(autocompleteSelectedTextColor).Background(autocompleteSelectedColor),
		).
		SetAutocompleteUseTags(false).
		SetAutocompleteFunc(func(query string) []string {
			matches := chooser.matches(query)
			labels := make([]string, len(matches))
			for i := range matches {
				labels[i] = matches[i].label
			}
			return labels
		}).
		SetAutocompletedFunc(func(text string, index int, source int) bool {
			matches := chooser.matches(chooser.field.GetText())
			if index < 0 || index >= len(matches) {
				return true
			}
			if source == tview.AutocompletedNavigate {
				chooser.selected = matches[index].id
				chooser.selectedText = chooser.field.GetText()
				return false
			}
			chooser.field.SetText(matches[index].label)
			chooser.selected = matches[index].id
			chooser.selectedText = matches[index].label
			return true
		})
	chooser.field.SetChangedFunc(func(text string) {
		for _, option := range chooser.options {
			if option.label == text {
				chooser.selected = option.id
				chooser.selectedText = text
				return
			}
		}
		chooser.selected = ""
		chooser.selectedText = ""
	})

	if len(chooser.options) > 0 {
		chooser.field.SetText(chooser.options[0].label)
		chooser.selected = chooser.options[0].id
		chooser.selectedText = chooser.options[0].label
	} else {
		chooser.field.SetPlaceholder("分区列表不可用，可输入分区 ID")
	}
	return chooser
}

func (a *areaField) matches(query string) []areaOption {
	query = strings.ToLower(strings.TrimSpace(query))
	result := make([]areaOption, 0, 10)
	for _, option := range a.options {
		if query != "" && !strings.Contains(strings.ToLower(option.label), query) {
			continue
		}
		result = append(result, option)
		if len(result) == 10 {
			break
		}
	}
	return result
}

func (a *areaField) id() string {
	// 即使服务器提供了分区列表，也允许直接输入数字编号，
	// 以支持新建或地区专属、尚未进入缓存的子分区。
	value := strings.TrimSpace(a.field.GetText())
	for _, option := range a.options {
		if strings.EqualFold(strings.TrimSpace(option.label), value) {
			return option.id
		}
	}
	if id, err := strconv.Atoi(value); err == nil && id > 0 {
		return value
	}
	if a.selected != "" && a.selectedText == a.field.GetText() {
		return a.selected
	}
	return ""
}
