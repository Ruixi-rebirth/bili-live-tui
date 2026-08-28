package main

import (
	"context"
	"net/http"
	"testing"

	"bili-live-tui/internal/api"
	"bili-live-tui/internal/config"
)

func TestSyncLiveTagsRejectsInvalidDeletionBeforeWriting(t *testing.T) {
	for _, raw := range []string{`{"known":"1"}`, `{broken`} {
		client := api.NewClient(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			t.Fatal("must validate all targets before a write")
			return nil, nil
		})})
		settings := api.LiveSettings{TagIDsJSON: raw}
		if err := syncLiveTags(context.Background(), client, "1", &config.AuthData{}, "known,missing", &settings); err == nil {
			t.Fatal("missing tag ID or malformed cache reported success")
		}
	}
}
