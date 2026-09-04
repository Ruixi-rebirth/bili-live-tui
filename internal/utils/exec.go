package utils

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ErrExecutableNotFound 表示自动探测和用户配置中都没有可用的执行文件。
var ErrExecutableNotFound = errors.New("未找到必需的执行文件")

const executablePathsFileName = "executable-paths.json"

var executablePathsMu sync.Mutex

// ExecutableNotFoundError 携带 TUI 显示路径输入页所需的信息。
type ExecutableNotFoundError struct {
	Key         string
	DisplayName string
	Candidates  []string
	Suggested   string
	Probe       ExecutableProbe
}

// ExecutableProbe 用无副作用的版本命令确认用户没有选错程序。
type ExecutableProbe struct {
	Args                   []string
	OutputContains         string
	UseExecutableDirectory bool
}

func (err *ExecutableNotFoundError) Error() string {
	return fmt.Sprintf("%s：%s", ErrExecutableNotFound, err.DisplayName)
}

func (err *ExecutableNotFoundError) Unwrap() error { return ErrExecutableNotFound }

// Configure 校验并保存用户选择的可执行文件，供后续解析和下次启动使用。
func (err *ExecutableNotFoundError) Configure(path string) (string, error) {
	return ConfigureExecutablePath(err.Key, err.DisplayName, path, err.Probe)
}

// GetExecutablePath 返回首个可用执行文件的绝对路径，并统一处理缺失提示。
func GetExecutablePath(executableNames ...string) (string, error) {
	for _, executableName := range executableNames {
		if path, err := exec.LookPath(executableName); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("%w: [%s]", ErrExecutableNotFound, strings.Join(executableNames, ", "))
}

// ResolveExecutable 优先使用保存的路径，再检查调用方提供的 PATH 名称或默认绝对路径。
// 找不到时返回 *ExecutableNotFoundError，让界面可以统一请求用户选择路径。
func ResolveExecutable(key, displayName string, candidates ...string) (string, error) {
	return ResolveExecutableWithProbe(key, displayName, ExecutableProbe{}, candidates...)
}

// ResolveExecutableWithProbe 在公共解析流程上增加手动路径的程序身份验证。
func ResolveExecutableWithProbe(key, displayName string, probe ExecutableProbe, candidates ...string) (string, error) {
	paths, err := loadExecutablePaths()
	if err != nil {
		return "", fmt.Errorf("读取外部程序路径配置失败: %w", err)
	}
	allCandidates := make([]string, 0, len(candidates)+1)
	if configured := strings.TrimSpace(paths[key]); configured != "" {
		allCandidates = append(allCandidates, configured)
	}
	for _, candidate := range candidates {
		if candidate = strings.TrimSpace(candidate); candidate != "" {
			allCandidates = append(allCandidates, candidate)
		}
	}
	if path, err := GetExecutablePath(allCandidates...); err == nil {
		return path, nil
	}
	return "", &ExecutableNotFoundError{
		Key:         key,
		DisplayName: displayName,
		Candidates:  append([]string(nil), candidates...),
		Suggested:   strings.TrimSpace(paths[key]),
		Probe:       probe,
	}
}

// ConfigureExecutablePath 校验用户输入，并以原子方式保存到用户配置目录。
func ConfigureExecutablePath(key, displayName, value string, probes ...ExecutableProbe) (string, error) {
	value = strings.Trim(strings.TrimSpace(value), `"`)
	if value == "" {
		return "", fmt.Errorf("%s 可执行文件路径不能为空", displayName)
	}
	resolved, err := exec.LookPath(value)
	if err != nil {
		return "", fmt.Errorf("找不到 %s 可执行文件：%s", displayName, value)
	}
	absolute, err := filepath.Abs(resolved)
	if err != nil {
		return "", fmt.Errorf("解析 %s 可执行文件路径失败: %w", displayName, err)
	}
	if len(probes) > 0 {
		if err := probeExecutable(absolute, displayName, probes[0]); err != nil {
			return "", err
		}
	}

	executablePathsMu.Lock()
	defer executablePathsMu.Unlock()
	paths, err := readExecutablePathsUnlocked()
	if err != nil {
		return "", fmt.Errorf("读取外部程序路径配置失败: %w", err)
	}
	paths[key] = absolute
	if err := writeExecutablePathsUnlocked(paths); err != nil {
		return "", fmt.Errorf("保存外部程序路径配置失败: %w", err)
	}
	return absolute, nil
}

func probeExecutable(path, displayName string, probe ExecutableProbe) error {
	if len(probe.Args) == 0 || strings.TrimSpace(probe.OutputContains) == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, path, probe.Args...)
	if probe.UseExecutableDirectory {
		command.Dir = filepath.Dir(path)
	}
	output, err := command.CombinedOutput()
	if ctx.Err() != nil {
		return fmt.Errorf("验证 %s 可执行文件超时，请确认路径是否正确", displayName)
	}
	if err != nil || !strings.Contains(strings.ToLower(string(output)), strings.ToLower(probe.OutputContains)) {
		return fmt.Errorf("所选文件不是有效的 %s 可执行文件：%s", displayName, path)
	}
	return nil
}

func loadExecutablePaths() (map[string]string, error) {
	executablePathsMu.Lock()
	defer executablePathsMu.Unlock()
	return readExecutablePathsUnlocked()
}

func readExecutablePathsUnlocked() (map[string]string, error) {
	path, err := executablePathsFilePath()
	if err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return make(map[string]string), nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	paths := make(map[string]string)
	if err := json.NewDecoder(file).Decode(&paths); err != nil {
		return nil, err
	}
	if paths == nil {
		paths = make(map[string]string)
	}
	return paths, nil
}

func writeExecutablePathsUnlocked(paths map[string]string) error {
	path, err := executablePathsFilePath()
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".executable-paths.tmp-*")
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
	if err := encoder.Encode(paths); err != nil {
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

func executablePathsFilePath() (string, error) {
	configRoot, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	directory := filepath.Join(configRoot, "bili-live-tui")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(directory, executablePathsFileName), nil
}
