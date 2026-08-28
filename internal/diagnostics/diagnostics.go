package diagnostics

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
)

var secretPattern = regexp.MustCompile(`(?i)(SESSDATA|bili_jct|access_key|auth_code|key)=([^&;\s]+)`)

const maxLogBytes int64 = 2 * 1024 * 1024

// Logger 写入简短的本地生命周期和错误日志，不包含终端控制序列或认证值。
// 它与 TUI 分离，即使终端渲染失败也能留下诊断信息。
type Logger struct {
	file   *os.File
	logger *log.Logger
	Path   string
}

func Open() (*Logger, error) {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return nil, err
	}
	directory := filepath.Join(cacheDir, "bili-live-tui")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(directory, "app.log")
	flags := os.O_CREATE | os.O_WRONLY | os.O_APPEND
	if info, statErr := os.Stat(path); statErr == nil && info.Size() >= maxLogBytes {
		// 诊断日志有大小上限。重新开始写入比让长期重连会话填满缓存更安全。
		flags = os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	} else if statErr != nil && !os.IsNotExist(statErr) {
		return nil, statErr
	}
	file, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		return nil, err
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, err
	}
	return &Logger{file: file, logger: log.New(file, "", log.Ldate|log.Ltime|log.Lmicroseconds), Path: path}, nil
}

func (l *Logger) Printf(format string, args ...any) {
	if l == nil || l.logger == nil {
		return
	}
	l.logger.Print(Sanitize(fmt.Sprintf(format, args...)))
}

func (l *Logger) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	return l.file.Close()
}

func Sanitize(message string) string {
	return secretPattern.ReplaceAllString(message, "$1=[REDACTED]")
}
