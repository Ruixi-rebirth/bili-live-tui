package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"bili-live-tui/internal/api"
	"bili-live-tui/internal/utils"
)

const (
	AppName              = "bili-live-tui"
	AuthFileName         = "auth.json"
	LiveSettingsFileName = "live-settings.json"
)

type AuthData struct {
	AccessToken string `json:"access_token"`
	SESSDATA    string `json:"SESSDATA"`
	BiliJCT     string `json:"bili_jct"`
}

func (auth AuthData) Validate() error {
	if strings.TrimSpace(auth.AccessToken) == "" || strings.TrimSpace(auth.SESSDATA) == "" || strings.TrimSpace(auth.BiliJCT) == "" {
		return fmt.Errorf("登录凭证不完整")
	}
	return nil
}

// getConfigPath 获取配置文件的完整路径，并确保目录存在
func getConfigPath(fileName string) (string, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}

	appConfigDir := filepath.Join(configDir, AppName)

	if err := os.MkdirAll(appConfigDir, 0o700); err != nil {
		return "", err
	}
	if err := os.Chmod(appConfigDir, 0o700); err != nil {
		return "", err
	}

	return filepath.Join(appConfigDir, fileName), nil
}

// ExtractAuthData 从登录轮询响应中提取并校验凭证数据。
func ExtractAuthData(pollData *api.TVQRPollResponse) (*AuthData, error) {
	if pollData == nil {
		return nil, fmt.Errorf("登录响应为空")
	}
	authData := &AuthData{
		AccessToken: pollData.Data.AccessToken,
	}

	for _, c := range pollData.Data.CookieInfo.Cookies {
		switch c.Name {
		case "SESSDATA":
			authData.SESSDATA = c.Value
		case "bili_jct":
			authData.BiliJCT = c.Value
		}
	}
	if err := authData.Validate(); err != nil {
		return nil, fmt.Errorf("登录响应不完整: %w", err)
	}
	return authData, nil
}

// SaveAuth 保存登录成功后的凭证
func SaveAuth(pollData *api.TVQRPollResponse) error {
	authData, err := ExtractAuthData(pollData)
	if err != nil {
		return err
	}

	configPath, err := getConfigPath(AuthFileName)
	if err != nil {
		return err
	}

	return writeJSONAtomically(configPath, authData)
}

// LoadAuth 读取本地凭证，用于启动时检查是否已登录
func LoadAuth() (*AuthData, error) {
	configPath, err := getConfigPath(AuthFileName)
	if err != nil {
		return nil, err
	}

	var auth AuthData
	if err := utils.ReadJSON(configPath, &auth); err != nil {
		return nil, err
	}
	if err := auth.Validate(); err != nil {
		return nil, err
	}
	return &auth, nil
}

// RemoveAuth 删除本地保存的登录凭据。若凭证文件存在且删除成功返回 true，若原本就不存在返回 false。
func RemoveAuth() (bool, error) {
	configPath, err := getConfigPath(AuthFileName)
	if err != nil {
		return false, err
	}
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		return false, nil
	}
	if err := os.Remove(configPath); err != nil {
		return false, err
	}
	return true, nil
}

// SaveLiveSettings 只保存最近一次成功开播的配置。
// 文件保持私有，因为其中可能包含 OBS WebSocket 密码；LiveSettings 从不包含 B 站推流码。
func SaveLiveSettings(settings api.LiveSettings) error {
	configPath, err := getConfigPath(LiveSettingsFileName)
	if err != nil {
		return err
	}
	return writeJSONAtomically(configPath, settings)
}

// writeJSONAtomically 先在同目录写入私有临时文件，落盘成功后再替换目标。
// 这样即使进程在保存期间异常退出，已有配置也不会被 O_TRUNC 截成空文件。
func writeJSONAtomically(path string, value any) error {
	return utils.WriteJSONAtomically(path, value)
}

// LoadLiveSettings 读取最近一次成功的开播配置，用于下次启动时回填表单。
func LoadLiveSettings() (*api.LiveSettings, error) {
	configPath, err := getConfigPath(LiveSettingsFileName)
	if err != nil {
		return nil, err
	}

	var settings api.LiveSettings
	if err := utils.ReadJSON(configPath, &settings); err != nil {
		return nil, err
	}
	if err := settings.Validate(); err != nil {
		return nil, fmt.Errorf("保存的开播信息无效: %w", err)
	}
	return &settings, nil
}
