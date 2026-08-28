package api

import (
	"crypto/md5"
	"encoding/hex"
	"net/url"
)

const (
	AppKey    = "4409e2ce8ffd12b8"
	AppSecret = "59b43e04ad6965f34319062b478f83dd"
)

// GenerateSign 生成 B 站 APP API 的签名
func GenerateSign(params url.Values) string {
	hash := md5.Sum([]byte(params.Encode() + AppSecret))
	return hex.EncodeToString(hash[:])
}
