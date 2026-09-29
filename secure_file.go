package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

const maxVaultBytes = 16 << 20

func privateParent(path string) error {
	dir := filepath.Dir(path)
	_, statErr := os.Lstat(dir)
	created := os.IsNotExist(statErr)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	st, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("密钥目录不能是符号链接：%s", dir)
	}
	return protectDirectory(dir, created)
}

func regularFile(path string) error {
	st, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() {
		return fmt.Errorf("拒绝符号链接或非普通文件：%s", path)
	}
	return nil
}

func readPrivateFile(path string) ([]byte, error) {
	if err := regularFile(path); err != nil {
		return nil, err
	}
	f, err := openPrivate(path, os.O_RDONLY)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxVaultBytes+1))
	if len(b) > maxVaultBytes {
		return nil, fmt.Errorf("密钥文件过大")
	}
	return b, err
}

func atomicPrivateWrite(path string, body []byte) error {
	if err := privateParent(path); err != nil {
		return err
	}
	if err := regularFile(path); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".funbot-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	defer f.Close()
	if err := protectFile(tmp); err != nil {
		return err
	}
	if _, err := f.Write(body); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

func lockVault(path string) (func(), error) {
	if err := privateParent(path); err != nil {
		return nil, err
	}
	f, err := openPrivate(path+".lock", os.O_CREATE|os.O_RDWR)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		ok, err := tryFileLock(f)
		if err != nil {
			f.Close()
			return nil, err
		}
		if ok {
			return func() { unlockFile(f); f.Close() }, nil
		}
		if time.Now().After(deadline) {
			f.Close()
			return nil, fmt.Errorf("密钥文件正在被另一进程使用，请稍后重试")
		}
		time.Sleep(25 * time.Millisecond)
	}
}
