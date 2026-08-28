package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"bili-live-tui/internal/api"
	"bili-live-tui/internal/config"
)

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
			if isRemoteCoverURL(cover) {
				edited.CoverPath = coverURL
			}
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
		if err := json.Unmarshal([]byte(settings.TagIDsJSON), &tagIDs); err != nil {
			return fmt.Errorf("直播标签编号缓存损坏，无法安全同步标签: %w", err)
		}
	}
	if tagIDs == nil {
		tagIDs = make(map[string]string)
	}
	// 在第一笔写请求之前检查所有删除目标，不能跳过缺失编号却报告保存成功。
	for _, tag := range oldTags {
		if _, keep := newSet[tag]; !keep && strings.TrimSpace(tagIDs[tag]) == "" {
			return fmt.Errorf("缺少标签 %q 的编号，无法删除，请在 B 站直播中心移除该标签", tag)
		}
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
