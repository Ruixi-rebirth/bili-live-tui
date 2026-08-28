package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"bili-live-tui/internal/api"
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

// SaveAuth 保存登录成功后的凭证
func SaveAuth(pollData *api.TVQRPollResponse) error {
	if pollData == nil {
		return fmt.Errorf("登录响应为空")
	}
	authData := AuthData{
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
		return fmt.Errorf("登录响应不完整: %w", err)
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
	// 修复旧版本写入的凭证权限；旧版本可能受进程 umask 影响，使其他用户可读。
	if err := os.Chmod(configPath, 0o600); err != nil {
		return nil, err
	}

	file, err := os.Open(configPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var auth AuthData
	if err := json.NewDecoder(file).Decode(&auth); err != nil {
		return nil, err
	}
	if err := auth.Validate(); err != nil {
		return nil, err
	}
	return &auth, nil
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
	temporary, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()

	if err := temporary.Chmod(0o600); err != nil {
		return err
	}
	encoder := json.NewEncoder(temporary)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	committed = true
	return nil
}

// LoadLiveSettings 读取最近一次成功的开播配置，用于下次启动时回填表单。
func LoadLiveSettings() (*api.LiveSettings, error) {
	configPath, err := getConfigPath(LiveSettingsFileName)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(configPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if err := file.Chmod(0o600); err != nil {
		return nil, err
	}
	var settings api.LiveSettings
	if err := json.NewDecoder(file).Decode(&settings); err != nil {
		return nil, err
	}
	if err := settings.Validate(); err != nil {
		return nil, fmt.Errorf("保存的开播信息无效: %w", err)
	}
	return &settings, nil
}
