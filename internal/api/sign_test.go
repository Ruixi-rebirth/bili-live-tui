package api

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"testing"
)

func legacyGenerateSign(params url.Values) string {
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

func TestGenerateSignMatchesLegacy(t *testing.T) {
	testCases := []url.Values{
		{},
		{"appkey": []string{AppKey}},
		{"appkey": []string{AppKey}, "ts": []string{"1700000000"}},
		{"z": []string{"last"}, "a": []string{"first"}, "m": []string{"middle value with spaces & symbols"}},
		{"auth_code": []string{"abc123xyz"}, "local_id": []string{"0"}, "ts": []string{"12345678"}},
	}

	for _, tc := range testCases {
		got := GenerateSign(tc)
		want := legacyGenerateSign(tc)
		if got != want {
			t.Fatalf("GenerateSign(%v) = %q, want %q", tc, got, want)
		}
	}
}
