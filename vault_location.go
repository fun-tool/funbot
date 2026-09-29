package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func systemRoot(executable, workingDir string) string {
	for _, part := range strings.FieldsFunc(filepath.ToSlash(executable), func(r rune) bool { return r == '/' }) {
		if strings.HasPrefix(part, "go-build") || strings.HasSuffix(executable, ".test") {
			return workingDir
		}
	}
	return filepath.Dir(executable)
}

func defaultKeyfile() string {
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Join(systemRoot(exe, cwd), ".data", "keys.json")
}

func initializeVaultLocation(path string) error {
	if path == "" {
		return fmt.Errorf("无法确定系统数据目录，请用 -keys 指定密钥路径")
	}
	if err := privateParent(path); err != nil {
		return err
	}
	return hideVaultDirectory(filepath.Dir(path))
}

func lockSystem(keyfile string) (func(), error) {
	path := filepath.Join(filepath.Dir(keyfile), ".instance.lock")
	f, err := openPrivate(path, os.O_CREATE|os.O_RDWR)
	if err != nil {
		return nil, err
	}
	ok, err := tryFileLock(f)
	if err != nil || !ok {
		f.Close()
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("此数据目录已有程序运行，请使用原页面或其他系统文件夹")
	}
	return func() { unlockFile(f); f.Close() }, nil
}
