package api

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

const (
	AppKey    = "4409e2ce8ffd12b8"
	AppSecret = "59b43e04ad6965f34319062b478f83dd"
)

// GenerateSign 生成 B 站 APP API 的签名
func GenerateSign(params url.Values) string {
	var keys []string
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var sortedParams []string
	for _, k := range keys {
		sortedParams = append(sortedParams, fmt.Sprintf("%s=%s", k, url.QueryEscape(params.Get(k))))
	}
	queryStr := strings.Join(sortedParams, "&")
	hash := md5.Sum([]byte(queryStr + AppSecret))
	return hex.EncodeToString(hash[:])
}
