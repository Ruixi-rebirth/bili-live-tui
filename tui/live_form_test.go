package tui

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bili-live-tui/internal/api"
	streamruntime "bili-live-tui/internal/stream"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

func TestNormalizeTags(t *testing.T) {
	if got, want := normalizeTags("游戏，聊天, 游戏;;音乐；"), "游戏,聊天,音乐"; got != want {
		t.Fatalf("normalizeTags() = %q, want %q", got, want)
	}
}

func TestResponsiveLiveFormDensity(t *testing.T) {
	for _, test := range []struct {
		height, padding, rows int
	}{
		{18, 0, 1},
		{24, 0, 2},
		{30, 1, 3},
	} {
		padding, rows := responsiveLiveFormDensity(test.height)
		if padding != test.padding || rows != test.rows {
			t.Fatalf("responsiveLiveFormDensity(%d) = (%d,%d), want (%d,%d)", test.height, padding, rows, test.padding, test.rows)
		}
	}
}

func TestPreferredLiveFormHeightCollapsesOBSGroup(t *testing.T) {
	form, state := newLiveFormWithOptions(nil, nil, "开播信息", true)
	withOBS := preferredLiveFormHeight(form, 1)
	state.streamMode.SetCurrentOption(1)
	withoutOBS := preferredLiveFormHeight(form, 1)
	if got, want := withOBS-withoutOBS, state.obsGroup.GetFieldHeight()+1; got != want {
		t.Fatalf("OBS group reserved %d rows after removal, want %d", got, want)
	}
}

func TestFormClippedTextAreaDoesNotDrawPastInnerBorder(t *testing.T) {
	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	screen.SetSize(20, 10)

	form := tview.NewForm()
	form.SetBorder(true)
	form.SetRect(0, 1, 20, 6)
	field := tview.NewTextArea().SetText("11111\n22222\n33333", false).SetSize(3, 0)
	item := clipTextAreaToForm(form, field)
	item.SetRect(1, 4, 18, 3)
	for x := 0; x < 20; x++ {
		screen.SetContent(x, 6, 'X', nil, tcell.StyleDefault)
	}

	item.Draw(screen)
	for x := 0; x < 20; x++ {
		mainc, _, _, _ := screen.GetContent(x, 6)
		if mainc != 'X' {
			t.Fatalf("text area drew past form border at x=%d: %q", x, mainc)
		}
	}
}

func TestValidateCoverInput(t *testing.T) {
	t.Run("empty is allowed", func(t *testing.T) {
		if err := validateCoverInput("", true); err != nil {
			t.Fatalf("empty cover = %v", err)
		}
	})
	t.Run("empty is rejected without an existing cover", func(t *testing.T) {
		if err := validateCoverInput("", false); err == nil {
			t.Fatal("empty first cover accepted")
		}
	})
	for _, value := range []string{"https://example.com/cover.jpg", "http://cdn.example/cover.webp"} {
		if err := validateCoverInput(value, false); err != nil {
			t.Fatalf("URL %q rejected: %v", value, err)
		}
	}
	t.Run("local image", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "cover.PNG")
		file, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(file, image.NewRGBA(image.Rect(0, 0, 640, 360))); err != nil {
			file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		if err := validateCoverInput(path, false); err != nil {
			t.Fatalf("local image rejected: %v", err)
		}
	})
	t.Run("unsupported extension", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "cover.gif")
		if err := os.WriteFile(path, []byte("image"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := validateCoverInput(path, false); err == nil {
			t.Fatal("unsupported extension accepted")
		}
	})
}

func TestAreaFieldSelectionAndFallback(t *testing.T) {
	chooser := newAreaField([]api.LiveArea{
		{ID: "376", Name: "单机游戏"},
		{ID: "12", Name: "手游"},
	})
	if got := chooser.id(); got != "376" {
		t.Fatalf("initial area ID = %q, want 376", got)
	}
	if got := chooser.matches("手"); len(got) != 1 || got[0].id != "12" {
		t.Fatalf("area matches = %#v, want the mobile-game area", got)
	}
	chooser.field.SetText("手游")
	if got := chooser.id(); got != "12" {
		t.Fatalf("area label override = %q, want 12", got)
	}
	chooser.field.SetText("999")
	if got := chooser.id(); got != "999" {
		t.Fatalf("numeric area override = %q, want 999", got)
	}

	fallback := newAreaField(nil)
	fallback.field.SetText("376")
	if got := fallback.id(); got != "376" {
		t.Fatalf("fallback area ID = %q, want 376", got)
	}
}

func TestLiveFormPrefillsSettings(t *testing.T) {
	initial := api.LiveSettings{
		Title:        "已有标题",
		Description:  "今天继续练习",
		Announcement: "今晚八点见\n记得关注",
		Tags:         "游戏,聊天",
		AreaID:       "12",
		CoverPath:    "https://i.example/cover.jpg",
		Orientation:  api.OrientationPortrait,
	}
	_, state := newLiveFormWithSettings([]api.LiveArea{
		{ID: "376", Name: "单机游戏"},
		{ID: "12", Name: "手游"},
	}, &initial, "修改直播资料")
	if got := state.settings(); got != initial {
		t.Fatalf("prefilled settings = %#v, want %#v", got, initial)
	}
}

func TestLiveEditPageSavesWithoutStartingAnotherApplication(t *testing.T) {
	initial := api.LiveSettings{
		Title:       "已有标题",
		AreaID:      "12",
		CoverPath:   "https://i.example/cover.jpg",
		Orientation: api.OrientationLandscape,
	}
	var saved api.LiveSettings
	page := newLiveEditPage(tview.NewApplication(), initial, []api.LiveArea{{ID: "12", Name: "手游"}}, func(settings api.LiveSettings) {
		saved = settings
	}, nil)

	page.form.GetButton(0).InputHandler()(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone), func(tview.Primitive) {})
	if saved != initial {
		t.Fatalf("saved settings = %#v, want %#v", saved, initial)
	}
}

func TestLiveEditPageCancelCallback(t *testing.T) {
	cancelled := false
	page := newLiveEditPage(tview.NewApplication(), api.LiveSettings{}, nil, nil, func() {
		cancelled = true
	})

	page.cancel()
	if !cancelled {
		t.Fatal("cancel callback was not called")
	}
}

func TestNewLiveFormDefaultsToOBSAndAllowsTestSource(t *testing.T) {
	form, state := newLiveFormWithOptions([]api.LiveArea{{ID: "376", Name: "单机游戏"}}, nil, "开播信息", true)
	if form.GetButtonCount() != 0 {
		t.Fatal("live settings action buttons must be outside the form border")
	}
	settings := state.settings()
	if settings.StreamMode != streamruntime.ModeOBS {
		t.Fatalf("default stream mode = %q, want OBS", settings.StreamMode)
	}
	if settings.Orientation != api.OrientationLandscape {
		t.Fatalf("default orientation = %q, want landscape", settings.Orientation)
	}
	if formItemIndex(form, state.obsGroup) < 0 {
		t.Fatal("OBS WebSocket group is missing in OBS mode")
	}
	for _, label := range []string{"地址", "端口", "密码"} {
		if state.obsGroup.GetFormItemByLabel(label) == nil {
			t.Fatalf("OBS WebSocket field %q is missing", label)
		}
	}
	if settings.OBSHost != "" {
		t.Fatalf("default OBS host setting = %q, want blank for runtime default", settings.OBSHost)
	}
	if settings.OBSPort != "" {
		t.Fatalf("default OBS port setting = %q, want blank for runtime default", settings.OBSPort)
	}
	state.streamMode.SetCurrentOption(1)
	selected := state.settings()
	if selected.StreamMode != streamruntime.ModeFFmpegTest {
		t.Fatalf("selected stream mode = %q, want FFmpeg test", selected.StreamMode)
	}
	if selected.OBSPassword != "" {
		t.Fatalf("FFmpeg settings retained OBS password %q", selected.OBSPassword)
	}
	if selected.OBSPort != "" {
		t.Fatalf("FFmpeg settings retained OBS port %q", selected.OBSPort)
	}
	if selected.OBSHost != "" {
		t.Fatalf("FFmpeg settings retained OBS host %q", selected.OBSHost)
	}
	if formItemIndex(form, state.obsGroup) >= 0 {
		t.Fatal("OBS WebSocket group is visible in FFmpeg mode")
	}
	state.streamMode.SetCurrentOption(0)
	if formItemIndex(form, state.obsGroup) < 0 {
		t.Fatal("OBS WebSocket group was not restored after switching back to OBS")
	}
}

func TestFocusLastLiveFormItem(t *testing.T) {
	app := tview.NewApplication()
	form, state := newLiveFormWithOptions(nil, nil, "开播信息", true)
	focusLastLiveFormItem(app, form, state)
	if app.GetFocus() != state.obsPassword {
		t.Fatal("last OBS form item is not the password field")
	}

	state.streamMode.SetCurrentOption(1)
	focusLastLiveFormItem(app, form, state)
	if !state.streamMode.HasFocus() {
		t.Fatal("last FFmpeg form item is not the stream mode selector")
	}
}

func TestLiveFormRestoresCustomOBSEndpointAndCollapsesSavedDefaults(t *testing.T) {
	_, custom := newLiveFormWithOptions(nil, &api.LiveSettings{
		OBSHost: "192.0.2.10",
		OBSPort: "4456",
	}, "开播信息", true)
	if custom.obsHost.GetText() != "192.0.2.10" || custom.obsPort.GetText() != "4456" {
		t.Fatalf("custom OBS endpoint was not restored: %q:%q", custom.obsHost.GetText(), custom.obsPort.GetText())
	}

	_, defaults := newLiveFormWithOptions(nil, &api.LiveSettings{
		OBSHost: "127.0.0.1",
		OBSPort: "4455",
	}, "开播信息", true)
	if defaults.obsHost.GetText() != "" || defaults.obsPort.GetText() != "" {
		t.Fatalf("saved OBS defaults were not collapsed to blanks: %q:%q", defaults.obsHost.GetText(), defaults.obsPort.GetText())
	}
}

func TestOBSWebSocketGroupKeyboardNavigation(t *testing.T) {
	host := tview.NewInputField().SetLabel("地址")
	port := tview.NewInputField().SetLabel("端口")
	password := tview.NewInputField().SetLabel("密码").SetMaskCharacter('*')
	group := newOBSWebSocketGroup(host, port, password)
	var focused tview.Primitive
	group.Focus(func(primitive tview.Primitive) { focused = primitive })
	if focused != group.items[0] {
		t.Fatal("OBS group did not initially focus the host field")
	}
	group.finishItem(0, tcell.KeyTab)
	if focused != group.items[1] {
		t.Fatal("Tab did not move from host to port")
	}
	group.finishItem(1, tcell.KeyTab)
	if focused != group.items[2] {
		t.Fatal("Tab did not move from port to password")
	}
	exited := false
	group.SetFinishedFunc(func(key tcell.Key) { exited = key == tcell.KeyTab })
	group.finishItem(2, tcell.KeyTab)
	if !exited {
		t.Fatal("Tab on password did not leave the OBS group")
	}
}

func TestOBSWebSocketGroupDrawsBorderAndMasksPassword(t *testing.T) {
	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	screen.SetSize(60, 7)

	group := newOBSWebSocketGroup(
		tview.NewInputField().SetLabel("地址").SetText("127.0.0.1"),
		tview.NewInputField().SetLabel("端口").SetText("4455"),
		tview.NewInputField().SetLabel("密码").SetText("secret").SetMaskCharacter('*'),
	)
	group.SetRect(0, 0, 60, group.GetFieldHeight())
	group.Draw(screen)

	var rendered strings.Builder
	for y := 0; y < 7; y++ {
		for x := 0; x < 60; x++ {
			mainc, _, _, _ := screen.GetContent(x, y)
			rendered.WriteRune(mainc)
		}
		rendered.WriteByte('\n')
	}
	text := rendered.String()
	if !strings.Contains(text, "OBS WebSocket") {
		t.Fatalf("group title was not drawn:\n%s", text)
	}
	for _, plaintext := range []string{"127.0.0.1", "4455", "secret"} {
		if strings.Contains(text, plaintext) {
			t.Fatalf("OBS connection value %q was drawn in plaintext:\n%s", plaintext, text)
		}
	}
	if !strings.Contains(text, "******") {
		t.Fatalf("OBS connection values were not masked:\n%s", text)
	}
}

func TestOBSWebSocketGroupShowsDefaultPlaceholders(t *testing.T) {
	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	screen.SetSize(60, 7)

	group := newOBSWebSocketGroup(
		tview.NewInputField().SetLabel("地址").SetPlaceholder("留空默认 127.0.0.1"),
		tview.NewInputField().SetLabel("端口").SetPlaceholder("留空默认 4455"),
		tview.NewInputField().SetLabel("密码"),
	)
	group.SetRect(0, 0, 60, group.GetFieldHeight())
	group.Draw(screen)

	var rendered strings.Builder
	for y := 0; y < 7; y++ {
		for x := 0; x < 60; x++ {
			mainc, _, _, _ := screen.GetContent(x, y)
			rendered.WriteRune(mainc)
		}
		rendered.WriteByte('\n')
	}
	text := rendered.String()
	for _, placeholder := range []string{"127.0.0.1", "4455"} {
		if !strings.Contains(text, placeholder) {
			t.Fatalf("placeholder %q was not drawn:\n%s", placeholder, text)
		}
	}
}

func TestStreamModeDropDownOpensOnFocus(t *testing.T) {
	selector := newStreamModeDropDown()
	selector.Focus(func(tview.Primitive) {})
	if !selector.IsOpen() {
		t.Fatal("stream mode choices did not open on focus")
	}
}

func TestFocusedLabelStyle(t *testing.T) {
	field := tview.NewInputField().SetLabel("直播标题")
	item := focusedLabelInput(field)
	item.SetFormAttributes(8, tview.Styles.SecondaryTextColor, tcell.ColorDefault, tcell.ColorDefault, tcell.ColorDefault)
	focusedColor, _, focusedAttrs := field.GetLabelStyle().Decompose()
	if focusedColor != tview.Styles.SecondaryTextColor || focusedAttrs&tcell.AttrBold != 0 {
		t.Fatalf("unfocused label style = (%v, %v), want secondary color without bold", focusedColor, focusedAttrs)
	}

	field.Focus(func(tview.Primitive) {})
	item.SetFormAttributes(8, tview.Styles.SecondaryTextColor, tcell.ColorDefault, tcell.ColorDefault, tcell.ColorDefault)
	focusedColor, _, focusedAttrs = field.GetLabelStyle().Decompose()
	if focusedColor != accentActiveColor || focusedAttrs&tcell.AttrBold == 0 {
		t.Fatalf("focused label style = (%v, %v), want accent color with bold", focusedColor, focusedAttrs)
	}
}

func TestEqualizeButtonWidths(t *testing.T) {
	form := tview.NewForm().AddButton("发送", nil).AddButton("返回概览", nil).AddButton("退出", nil)
	equalizeButtonWidths(form)
	width := tview.TaggedStringWidth(form.GetButton(0).GetLabel())
	for i := 1; i < form.GetButtonCount(); i++ {
		if got := tview.TaggedStringWidth(form.GetButton(i).GetLabel()); got != width {
			t.Fatalf("button %d width = %d, want %d", i, got, width)
		}
	}
	for i, want := range []string{"发送", "返回概览", "退出"} {
		if got := strings.TrimSpace(form.GetButton(i).GetLabel()); got != want {
			t.Fatalf("button %d label = %q, want %q", i, got, want)
		}
	}
}

func TestMascotDimensions(t *testing.T) {
	if width := mascotWidth(); width == 0 {
		t.Fatal("rabbit has invalid width")
	}
}
