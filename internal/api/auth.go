package api

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// 定义二维码状态枚举
type QRStatus int

const (
	QRStatusSuccess QRStatus = 0
	QRStatusExpired QRStatus = 86038
	QRStatusWaiting QRStatus = 86039
	QRStatusScanned QRStatus = 86090
)

// TVQRPollResponse 提取需要用到的认证数据
type TVQRPollResponse struct {
	Code int `json:"code"`
	Data struct {
		AccessToken string `json:"access_token"`
		CookieInfo  struct {
			Cookies []struct {
				Name  string `json:"name"`
				Value string `json:"value"`
			} `json:"cookies"`
		} `json:"cookie_info"`
	} `json:"data"`
	Message string `json:"message"`
}

// GetTVQRCodeContext 获取登录二维码，并允许主流程在收到退出信号时取消请求。
func GetTVQRCodeContext(ctx context.Context) (qrURL string, authCode string, err error) {
	params := url.Values{}
	params.Set("appkey", AppKey)
	params.Set("local_id", "0")
	params.Set("ts", fmt.Sprintf("%d", time.Now().Unix()))
	params.Set("sign", GenerateSign(params))

	reqURL, err := NewClient(nil).endpointByName("GetTVQRCode")
	if err != nil {
		return "", "", err
	}
	var result struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			URL      string `json:"url"`
			AuthCode string `json:"auth_code"`
		} `json:"data"`
	}
	if err := NewClient(nil).postForm(ctx, reqURL, params, &result); err != nil {
		return "", "", fmt.Errorf("获取登录二维码失败: %w", err)
	}

	if result.Code != 0 {
		return "", "", fmt.Errorf("获取登录二维码失败（错误码 %d）：%s", result.Code, responseMessage(result.Message, ""))
	}
	if strings.TrimSpace(result.Data.URL) == "" || strings.TrimSpace(result.Data.AuthCode) == "" {
		return "", "", fmt.Errorf("登录接口未返回有效二维码")
	}
	return result.Data.URL, result.Data.AuthCode, nil
}

// CheckQRStatusContext 执行一次可取消的扫码状态检查。
func CheckQRStatusContext(ctx context.Context, authCode string) (QRStatus, *TVQRPollResponse, error) {
	params := url.Values{}
	params.Set("appkey", AppKey)
	params.Set("auth_code", authCode)
	params.Set("local_id", "0")
	params.Set("ts", fmt.Sprintf("%d", time.Now().Unix()))
	params.Set("sign", GenerateSign(params))

	reqURL, err := NewClient(nil).endpointByName("CheckQRStatus")
	if err != nil {
		return -1, nil, err
	}
	var result TVQRPollResponse
	if err := NewClient(nil).postForm(ctx, reqURL, params, &result); err != nil {
		return -1, nil, fmt.Errorf("检查扫码状态失败: %w", err)
	}

	status := QRStatus(result.Code)
	switch status {
	case QRStatusSuccess, QRStatusExpired, QRStatusWaiting, QRStatusScanned:
		return status, &result, nil
	default:
		return status, &result, fmt.Errorf("检查扫码状态失败（错误码 %d）：%s", result.Code, responseMessage(result.Message, ""))
	}
}
