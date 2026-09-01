package utils

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// ErrExecutableNotFound 表示系统 PATH 中没有找到所需的执行文件。
var ErrExecutableNotFound = errors.New("未找到必需的执行文件，请确保已正确安装并添加到系统环境变量 (PATH) 中")

// GetExecutablePath 返回首个可用执行文件的绝对路径，并统一处理缺失提示。
func GetExecutablePath(executableNames ...string) (string, error) {
	for _, executableName := range executableNames {
		if path, err := exec.LookPath(executableName); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("%w: [%s]", ErrExecutableNotFound, strings.Join(executableNames, ", "))
}
