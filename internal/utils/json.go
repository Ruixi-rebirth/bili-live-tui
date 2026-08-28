package utils

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// WriteJSONAtomically 先在同目录写入 0600 权限的临时文件，落盘并关闭成功后再重命名覆盖目标。
// 这样可防止程序在写入期间崩溃时将原有文件截断为空文件。
func WriteJSONAtomically(path string, value any) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
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

// ReadJSON 打开指定的 JSON 文件，防御性设置 0600 权限并解析到 out 目标。
func ReadJSON(path string, out any) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := file.Chmod(0o600); err != nil {
		return err
	}
	return json.NewDecoder(file).Decode(out)
}
