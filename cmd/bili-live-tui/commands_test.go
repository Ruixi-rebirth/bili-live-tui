package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"bili-live-tui/internal/api"
	"github.com/spf13/cobra"
)

func TestCommandsRejectUnexpectedArgumentsBeforeRunning(t *testing.T) {
	for _, args := range [][]string{{"statuz"}, {"status", "extra"}, {"stop", "extra"}, {"logout", "extra"}} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			root := newRootCmd()
			called := false
			run := func(*cobra.Command, []string) error { called = true; return nil }
			root.RunE = run
			for _, cmd := range root.Commands() {
				cmd.RunE = run
			}
			root.SetArgs(args)
			if err := root.ExecuteContext(context.Background()); err == nil {
				t.Fatal("unexpected arguments were accepted")
			}
			if called {
				t.Fatal("command ran before rejecting arguments")
			}
		})
	}
}

func TestCommandActionsReportUpstreamFailure(t *testing.T) {
	for _, name := range []string{"status", "stop"} {
		for _, failed := range []bool{false, true} {
			t.Run(name+"_"+map[bool]string{false: "success", true: "failure"}[failed], func(t *testing.T) {
				client := api.NewClient(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					body := `{"code":0,"data":{"room_info":{"room_id":1,"title":"测试直播间","live_status":0}}}`
					if failed {
						body = `{"code":-101,"message":"账号未登录"}`
					}
					return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
				})})
				var err error
				if name == "status" {
					err = handleStatusAction(context.Background(), client, "1")
				} else {
					err = handleStopAction(context.Background(), client, "1", "test-token", nil)
				}
				if (err != nil) != failed {
					t.Fatalf("error = %v, want failure = %v", err, failed)
				}
				if failed && !strings.Contains(err.Error(), "账号未登录") {
					t.Fatalf("upstream failure reason was lost: %v", err)
				}
			})
		}
	}
}

func TestStopActionRespectsCommandCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	client := api.NewClient(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		cancel()
		if !errors.Is(req.Context().Err(), context.Canceled) {
			t.Fatal("stop request is not derived from command context")
		}
		return nil, req.Context().Err()
	})})
	defer cancel()
	if err := handleStopAction(ctx, client, "1", "test-token", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation was not preserved: %v", err)
	}
}

func TestOnceInitializationDoesNotCreateDiagnostics(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "windows" {
		t.Skip("cache directory redirection in this test uses XDG_CACHE_HOME or LOCALAPPDATA")
	}
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)
	t.Setenv("LOCALAPPDATA", cache)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := initAppContext(ctx, false); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancelled login, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(cache, "bili-live-tui")); !os.IsNotExist(err) {
		t.Fatalf("temporary session created a diagnostics directory: %v", err)
	}
}
