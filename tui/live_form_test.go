package tui

import (
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
		if err := validateCoverInput(""); err != nil {
			t.Fatalf("empty cover = %v", err)
		}
	})
	for _, value := range []string{"https://example.com/cover.jpg", "http://cdn.example/cover.webp"} {
		if err := validateCoverInput(value); err != nil {
			t.Fatalf("URL %q rejected: %v", value, err)
		}
	}
	t.Run("local image", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "cover.PNG")
		if err := os.WriteFile(path, []byte("image"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := validateCoverInput(path); err != nil {
			t.Fatalf("local image rejected: %v", err)
		}
	})
	t.Run("unsupported extension", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "cover.gif")
		if err := os.WriteFile(path, []byte("image"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := validateCoverInput(path); err == nil {
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
	form, state := newLiveForm([]api.LiveArea{{ID: "376", Name: "单机游戏"}})
	settings := state.settings()
	if settings.StreamMode != streamruntime.ModeOBS {
		t.Fatalf("default stream mode = %q, want OBS", settings.StreamMode)
	}
	if settings.Orientation != api.OrientationLandscape {
		t.Fatalf("default orientation = %q, want landscape", settings.Orientation)
	}
	if form.GetFormItemByLabel("OBS WebSocket 密码") == nil {
		t.Fatal("OBS password field is missing in OBS mode")
	}
	state.streamMode.SetCurrentOption(1)
	selected := state.settings()
	if selected.StreamMode != streamruntime.ModeFFmpegTest {
		t.Fatalf("selected stream mode = %q, want FFmpeg test", selected.StreamMode)
	}
	if selected.OBSPassword != "" {
		t.Fatalf("FFmpeg settings retained OBS password %q", selected.OBSPassword)
	}
	if form.GetFormItemByLabel("OBS WebSocket 密码") != nil {
		t.Fatal("OBS password field is visible in FFmpeg mode")
	}
	state.streamMode.SetCurrentOption(0)
	if form.GetFormItemByLabel("OBS WebSocket 密码") == nil {
		t.Fatal("OBS password field was not restored after switching back to OBS")
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
	if width, height := mascotWidth(), mascotHeight(); width == 0 || height == 0 {
		t.Fatalf("rabbit has invalid dimensions %dx%d", width, height)
	}
}
