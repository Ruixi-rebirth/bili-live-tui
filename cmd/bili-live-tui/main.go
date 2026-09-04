package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"bili-live-tui/internal/api"
	"bili-live-tui/internal/config"
	"bili-live-tui/internal/diagnostics"
	"bili-live-tui/internal/ffmpeg"
	"bili-live-tui/internal/obs"
	streamruntime "bili-live-tui/internal/stream"
	"bili-live-tui/tui"

	"github.com/mdp/qrterminal/v3"
)

func main() {
	diagnosticLog, _ := diagnostics.Open()
	if diagnosticLog != nil {
		defer diagnosticLog.Close()
		diagnosticLog.Printf("程序启动")
	}
	signalCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	ctx, cancel := context.WithCancel(signalCtx)
	defer cancel()

	client := api.NewClient(nil)
	auth, err := config.LoadAuth()
	if err != nil {
		auth, err = performLogin(ctx)
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				diagnosticLog.Printf("扫码登录失败: %v", err)
				fmt.Fprintf(os.Stderr, "登录失败: %v\n", err)
			}
			return
		}
	}

	roomID, err := client.GetMyRoomID(ctx, auth.SESSDATA)
	if err != nil && isAuthenticationError(err) {
		fmt.Println("登录凭证已失效，请重新扫码登录")
		auth, err = performLogin(ctx)
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				diagnosticLog.Printf("重新登录失败: %v", err)
				fmt.Fprintf(os.Stderr, "重新登录失败: %v\n", err)
			}
			return
		}
		roomID, err = client.GetMyRoomID(ctx, auth.SESSDATA)
	}
	if err != nil {
		diagnosticLog.Printf("获取房间号失败: %v", err)
		fmt.Fprintf(os.Stderr, "获取房间号失败: %v\n", err)
		return
	}

	areas, areaErr := client.GetLiveAreas(ctx, auth.AccessToken)
	if areaErr != nil {
		areas = nil
	}
	savedSettings, settingsLoadErr := config.LoadLiveSettings()
	if settingsLoadErr != nil && !os.IsNotExist(settingsLoadErr) {
		diagnosticLog.Printf("读取上次开播信息失败，将使用默认值: %v", settingsLoadErr)
		savedSettings = nil
	}
	if savedSettings == nil || strings.TrimSpace(savedSettings.CoverPath) == "" {
		if snapshot, snapshotErr := client.GetRoomSnapshot(ctx, roomID); snapshotErr == nil && strings.TrimSpace(snapshot.Cover) != "" {
			if savedSettings == nil {
				savedSettings = &api.LiveSettings{}
			}
			savedSettings.CoverPath = snapshot.Cover
		}
	}
	var liveStream streamruntime.Runtime
	var liveStartedAt time.Time
	var platformLiveStarted atomic.Bool
	previousTags := ""
	if savedSettings != nil {
		previousTags = savedSettings.Tags
	}
	rollbackStartedLive := func(cause error) error {
		stopped, rollbackErr := rollbackLiveStart(client, roomID, auth.AccessToken, cause)
		if stopped {
			platformLiveStarted.Store(false)
		}
		return rollbackErr
	}
	settings, err := tui.RunLiveSettings(ctx, areas, savedSettings, func(liveSettings *api.LiveSettings) error {
		if err := preflightStreamExecutable(*liveSettings); err != nil {
			return err
		}
		existingCover := ""
		if savedSettings != nil {
			existingCover = strings.TrimSpace(savedSettings.CoverPath)
		}
		cover := strings.TrimSpace(liveSettings.CoverPath)
		if cover == "" {
			liveSettings.CoverPath = existingCover
		} else if cover != existingCover || !isRemoteCoverURL(cover) {
			coverURL, uploadErr := uploadCover(ctx, client, roomID, auth.SESSDATA, auth.BiliJCT, cover)
			if uploadErr != nil {
				return uploadErr
			}
			liveSettings.CoverPath = coverURL
			if err := client.UpdatePreLiveCover(ctx, roomID, auth.SESSDATA, auth.BiliJCT, coverURL, liveSettings.Orientation); err != nil {
				return err
			}
		}
		if updateErr := client.UpdateLiveInfoBeforeStart(ctx, roomID, auth.AccessToken, auth.SESSDATA, auth.BiliJCT, *liveSettings); updateErr != nil {
			return updateErr
		}
		if tagErr := syncLiveTags(ctx, client, roomID, auth, previousTags, liveSettings); tagErr != nil {
			return tagErr
		}
		if strings.TrimSpace(liveSettings.Announcement) != "" {
			if err := client.UpdateRoomNews(ctx, roomID, auth.SESSDATA, auth.BiliJCT, liveSettings.Announcement); err != nil {
				return err
			}
		}
		addr, key, startErr := client.StartLive(ctx, roomID, auth.AccessToken, *liveSettings)
		if startErr != nil {
			return startErr
		}
		platformLiveStarted.Store(true)
		if ctx.Err() != nil {
			return rollbackStartedLive(ctx.Err())
		}
		runtime, runtimeErr := newStreamRuntime(*liveSettings)
		if runtimeErr != nil {
			return rollbackStartedLive(runtimeErr)
		}
		streamErr := runtime.Start(addr, key)
		if streamErr != nil {
			return rollbackStartedLive(streamErr)
		}
		// 从这里开始即使取消路径中的首次 Stop 失败，主流程也能再次尝试清理。
		liveStream = runtime
		if ctx.Err() != nil {
			cause := ctx.Err()
			if stopErr := runtime.Stop(); stopErr != nil {
				cause = fmt.Errorf("%w；停止刚启动的本地推流失败：%v", cause, stopErr)
			}
			return rollbackStartedLive(cause)
		}
		liveStartedAt = time.Now()
		diagnosticLog.Printf("直播启动成功 room=%s mode=%s", roomID, liveSettings.StreamMode)
		return nil
	})
	if err != nil {
		if liveStream != nil {
			if stopErr := liveStream.Stop(); stopErr != nil {
				diagnosticLog.Printf("取消开播时停止本地推流失败: %v", stopErr)
			}
		}
		if platformLiveStarted.Load() {
			stopCtx, cancelStop := context.WithTimeout(context.Background(), 10*time.Second)
			stopErr := client.StopLive(stopCtx, roomID, auth.AccessToken)
			cancelStop()
			if stopErr != nil {
				diagnosticLog.Printf("取消开播时调用下播接口失败: %v", stopErr)
				fmt.Fprintln(os.Stderr, "自动下播失败，请到 B 站直播中心确认状态")
			} else {
				platformLiveStarted.Store(false)
			}
		}
		if !errors.Is(err, tui.ErrLiveSettingsCancelled) && !errors.Is(err, context.Canceled) {
			diagnosticLog.Printf("开播设置流程失败: %v", err)
			fmt.Fprintf(os.Stderr, "读取开播信息失败: %v\n", err)
		}
		return
	}
	if saveErr := config.SaveLiveSettings(settings); saveErr != nil {
		diagnosticLog.Printf("保存下次开播默认值失败: %v", saveErr)
	}
	// 资料、开播和所选本地推流全部成功后才进入弹幕页，避免短暂回到终端外壳。

	// OBS/FFmpeg 通过 RTMP 连接维持直播。
	// B 站移动端心跳接口用于观众观看任务，不属于主播推流会话，不能在这里调用。
	danmakuSession := tui.NewLiveDanmakuSession(ctx, client, roomID, auth.SESSDATA, auth.BiliJCT)
	previewer := &livePreviewer{}
	previewLive := func() error {
		return previewer.Start(ctx, client, roomID, auth.SESSDATA, auth.BiliJCT)
	}
	var outputEndedUnexpectedly atomic.Bool
	go watchStreamOutput(ctx, liveStream.Done(), func() {
		outputEndedUnexpectedly.Store(true)
		cancel()
	})

	// 开播成功后在弹幕页和直播概览之间切换。
	// 页面本身不再输出控制台日志，所有交互都在 TUI 内完成。
	homeNotice := ""
	var roomSnapshot *api.RoomSnapshot
	loadRoomSnapshot := func() (api.RoomSnapshot, error) {
		snapshot, err := client.GetRoomSnapshot(ctx, roomID)
		if err != nil {
			return api.RoomSnapshot{}, err
		}
		// 房间接口只作为弹幕心跳尚未返回数据时的临时兜底，不覆盖会话中的最新人气。
		if online, known := danmakuSession.Popularity(); known {
			snapshot.Online = online
			snapshot.OnlineKnown = true
		}
		return snapshot, nil
	}
	stopRequested := false
	streamHealth := func() streamruntime.Health {
		if liveStream == nil {
			return streamruntime.Health{}
		}
		return liveStream.Health()
	}
	for {
		navigation, err := tui.RunDanmaku(ctx, danmakuSession, client, roomID, auth.SESSDATA, auth.BiliJCT, streamHealth)
		if err != nil {
			diagnosticLog.Printf("弹幕界面异常: %v", err)
			fmt.Fprintf(os.Stderr, "弹幕界面异常: %v\n", err)
			break
		}
		if navigation != tui.NavigationHome {
			break
		}

		saveEdit := func(edited api.LiveSettings) (api.LiveSettings, error) {
			updated, saveErr := saveLiveSettings(ctx, client, roomID, auth, settings, edited)
			if saveErr != nil {
				return api.LiveSettings{}, saveErr
			}
			if saveErr := config.SaveLiveSettings(updated); saveErr != nil {
				diagnosticLog.Printf("保存下次开播默认值失败: %v", saveErr)
			}
			return updated, nil
		}
		action, err := tui.RunHome(ctx, liveStartedAt, roomID, &settings, areas, roomSnapshot, danmakuSession.Stats(), homeNotice, loadRoomSnapshot, func(fresh api.RoomSnapshot) {
			roomSnapshot = &fresh
		}, streamHealth, saveEdit, previewLive, danmakuSession.Stats)
		homeNotice = ""
		if err != nil {
			diagnosticLog.Printf("直播概览异常: %v", err)
			fmt.Fprintf(os.Stderr, "直播概览异常: %v\n", err)
			stopRequested = true
			break
		}
		if action == tui.HomeActionStop {
			stopRequested = true
		}
		if stopRequested {
			break
		}
	}

	danmakuSession.Close()
	cancel()
	if outputEndedUnexpectedly.Load() {
		detail := strings.TrimSpace(liveStream.Health().LastError)
		if detail == "" {
			detail = "未返回详细原因"
		}
		diagnosticLog.Printf("本地推流意外停止，触发自动下播: %s", detail)
		fmt.Fprintf(os.Stderr, "本地推流已意外停止（%s），正在自动结束 B 站直播\n", detail)
	}

	if liveStream != nil {
		if err := liveStream.Stop(); err != nil {
			diagnosticLog.Printf("停止本地推流失败: %v", err)
			fmt.Fprintf(os.Stderr, "停止本地推流遇到问题: %v\n", err)
		}
	}

	stopCtx, cancelStop := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelStop()
	if err := client.StopLive(stopCtx, roomID, auth.AccessToken); err != nil {
		diagnosticLog.Printf("调用下播接口失败: %v", err)
		fmt.Fprintf(os.Stderr, "调用下播接口失败: %v\n", err)
	} else {
		diagnosticLog.Printf("直播已安全结束 room=%s", roomID)
	}
}

func watchStreamOutput(ctx context.Context, done <-chan struct{}, onUnexpectedStop func()) {
	select {
	case <-done:
		if ctx.Err() == nil && onUnexpectedStop != nil {
			onUnexpectedStop()
		}
	case <-ctx.Done():
	}
}

func isAuthenticationError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{"未登录", "登录失效", "登录过期", "token错误", "token 错误", "-101", "65530"} {
		if strings.Contains(message, strings.ToLower(marker)) {
			return true
		}
	}
	return false
}

func rollbackLiveStart(client *api.Client, roomID, accessToken string, cause error) (bool, error) {
	rollbackCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := client.StopLive(rollbackCtx, roomID, accessToken); err != nil {
		return false, fmt.Errorf("%w；本地推流启动失败后自动下播也失败：%v，请到 B 站直播中心确认状态", cause, err)
	}
	return true, cause
}

func newStreamRuntime(settings api.LiveSettings) (streamruntime.Runtime, error) {
	switch strings.TrimSpace(settings.StreamMode) {
	case "", streamruntime.ModeOBS:
		return obs.NewRuntime(settings.OBSPassword), nil
	case streamruntime.ModeFFmpegTest:
		return ffmpeg.NewTestRuntime(settings.Orientation), nil
	default:
		return nil, fmt.Errorf("不支持的推流方式: %s", settings.StreamMode)
	}
}

func preflightStreamExecutable(settings api.LiveSettings) error {
	switch strings.TrimSpace(settings.StreamMode) {
	case "", streamruntime.ModeOBS:
		return obs.Preflight()
	case streamruntime.ModeFFmpegTest:
		_, err := ffmpeg.ExecutablePath()
		return err
	default:
		return fmt.Errorf("不支持的推流方式: %s", settings.StreamMode)
	}
}

func uploadCover(ctx context.Context, client *api.Client, roomID, sessdata, biliJCT, cover string) (string, error) {
	if isRemoteCoverURL(cover) {
		uploaded, uploadErr := client.UploadRoomCoverURL(ctx, roomID, sessdata, biliJCT, cover)
		if uploadErr == nil {
			return uploaded, nil
		}
		return "", uploadErr
	}
	return client.UploadRoomCover(ctx, roomID, sessdata, biliJCT, cover)
}

func isRemoteCoverURL(cover string) bool {
	parsed, err := url.ParseRequestURI(strings.TrimSpace(cover))
	return err == nil && (strings.EqualFold(parsed.Scheme, "http") || strings.EqualFold(parsed.Scheme, "https")) && parsed.Host != ""
}

func saveLiveSettings(ctx context.Context, client *api.Client, roomID string, auth *config.AuthData, current, edited api.LiveSettings) (api.LiveSettings, error) {
	// 用户只修改文字资料时，已经上传过的封面地址保持不变。
	// 新的本地文件和第三方链接沿用首次设置时的上传流程。
	if strings.TrimSpace(edited.CoverPath) == "" && strings.TrimSpace(current.CoverPath) != "" {
		edited.CoverPath = current.CoverPath
	}
	if strings.TrimSpace(edited.CoverPath) != strings.TrimSpace(current.CoverPath) {
		if cover := strings.TrimSpace(edited.CoverPath); cover != "" {
			coverURL, err := uploadCover(ctx, client, roomID, auth.SESSDATA, auth.BiliJCT, cover)
			if err != nil {
				return api.LiveSettings{}, err
			}
			edited.CoverPath = coverURL
			if err := client.UpdatePreLiveCover(ctx, roomID, auth.SESSDATA, auth.BiliJCT, coverURL, edited.Orientation); err != nil {
				return api.LiveSettings{}, err
			}
		}
	}
	if err := client.UpdateLiveInfoWithCookie(ctx, roomID, auth.AccessToken, auth.SESSDATA, auth.BiliJCT, edited); err != nil {
		return api.LiveSettings{}, err
	}
	if err := syncLiveTags(ctx, client, roomID, auth, current.Tags, &edited); err != nil {
		return api.LiveSettings{}, err
	}
	if strings.TrimSpace(edited.Announcement) != "" || strings.TrimSpace(current.Announcement) != "" {
		if err := client.UpdateRoomNews(ctx, roomID, auth.SESSDATA, auth.BiliJCT, edited.Announcement); err != nil {
			return api.LiveSettings{}, err
		}
	}
	return edited, nil
}

func syncLiveTags(ctx context.Context, client *api.Client, roomID string, auth *config.AuthData, previous string, settings *api.LiveSettings) error {
	if settings == nil {
		return nil
	}
	oldTags := splitLiveTags(previous)
	newTags := splitLiveTags(settings.Tags)
	oldSet := make(map[string]struct{}, len(oldTags))
	newSet := make(map[string]struct{}, len(newTags))
	for _, tag := range oldTags {
		oldSet[tag] = struct{}{}
	}
	for _, tag := range newTags {
		newSet[tag] = struct{}{}
	}
	tagIDs := make(map[string]string)
	if strings.TrimSpace(settings.TagIDsJSON) != "" {
		_ = json.Unmarshal([]byte(settings.TagIDsJSON), &tagIDs)
	}
	for _, tag := range oldTags {
		if _, exists := newSet[tag]; exists {
			continue
		}
		tagID := strings.TrimSpace(tagIDs[tag])
		if tagID == "" {
			continue
		}
		if err := client.DeleteLiveTag(ctx, roomID, auth.SESSDATA, auth.BiliJCT, tagID); err != nil {
			return fmt.Errorf("删除直播标签 %q 失败: %w", tag, err)
		}
		delete(tagIDs, tag)
	}
	for _, tag := range newTags {
		if _, exists := oldSet[tag]; exists {
			continue
		}
		tagID, err := client.AddLiveTag(ctx, roomID, auth.SESSDATA, auth.BiliJCT, tag)
		if err != nil {
			return fmt.Errorf("新增直播标签 %q 失败: %w", tag, err)
		}
		if strings.TrimSpace(tagID) != "" {
			tagIDs[tag] = tagID
		}
	}
	if len(tagIDs) == 0 {
		settings.TagIDsJSON = ""
	} else if data, err := json.Marshal(tagIDs); err == nil {
		settings.TagIDsJSON = string(data)
	}
	return nil
}

func splitLiveTags(value string) []string {
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
	return result
}

// performLogin 执行可取消的扫码登录，不在辅助函数中直接终止整个进程。
func performLogin(ctx context.Context) (*config.AuthData, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	fmt.Println("正在请求 TV 端登录二维码...")

	qrURL, authCode, err := api.GetTVQRCodeContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("获取二维码失败: %w", err)
	}

	// 使用半方块字符保持二维码接近正方形，并缩小四周留白。
	qrterminal.GenerateWithConfig(qrURL, qrterminal.Config{
		Level:          qrterminal.L,
		Writer:         os.Stdout,
		HalfBlocks:     true,
		BlackChar:      " ",
		BlackWhiteChar: "▄",
		WhiteBlackChar: "▀",
		WhiteChar:      "█",
		QuietZone:      1,
	})
	fmt.Println("请使用哔哩哔哩手机 APP 扫码登录")

	for {
		status, pollData, err := api.CheckQRStatusContext(ctx, authCode)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			fmt.Println("网络请求异常，重试中...")
			if err := waitForContext(ctx, 3*time.Second); err != nil {
				return nil, err
			}
			continue
		}

		switch status {
		case api.QRStatusSuccess:
			fmt.Println("\n登录成功，已获取 APP Access Token")

			// 提取 AuthData 供主流程继续使用
			var sess, jct string
			for _, c := range pollData.Data.CookieInfo.Cookies {
				if c.Name == "SESSDATA" {
					sess = c.Value
				} else if c.Name == "bili_jct" {
					jct = c.Value
				}
			}
			auth := &config.AuthData{
				AccessToken: pollData.Data.AccessToken,
				SESSDATA:    sess,
				BiliJCT:     jct,
			}
			if err := auth.Validate(); err != nil {
				return nil, fmt.Errorf("登录响应不完整: %w", err)
			}
			if err := config.SaveAuth(pollData); err != nil {
				fmt.Printf("保存登录凭证失败: %v\n", err)
			}
			return auth, nil

		case api.QRStatusExpired:
			return nil, fmt.Errorf("二维码已失效，请重新运行程序")

		case api.QRStatusWaiting:
			// 还在等，保持静默

		case api.QRStatusScanned:
			fmt.Println("已扫码，请在手机端点击确认...")
		}

		if err := waitForContext(ctx, 2*time.Second); err != nil {
			return nil, err
		}
	}
}

func waitForContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
