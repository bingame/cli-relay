package safeio

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func CheckPath(path string) error {
	p, e := filepath.Abs(path)
	if e != nil {
		return e
	}
	for {
		info, err := os.Lstat(p)
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("拒绝符号链接路径: %s", p)
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		next := filepath.Dir(p)
		if next == p {
			break
		}
		p = next
	}
	return nil
}
func EnsureDir(path string) error {
	if err := CheckPath(path); err != nil {
		return err
	}
	if st, err := os.Stat(path); err == nil {
		if !st.IsDir() {
			return fmt.Errorf("路径不是目录: %s", path)
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	return protect(path, true)
}

// EnsurePrivateDir 仅用于 Relay 自己管理的数据根目录。
func EnsurePrivateDir(path string) error {
	if e := EnsureDir(path); e != nil {
		return e
	}
	return protect(path, true)
}
func WriteFile(path string, data []byte, mode os.FileMode) error {
	if err := CheckPath(path); err != nil {
		return err
	}
	if err := EnsureDir(filepath.Dir(path)); err != nil {
		return err
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".relay-write-*")
	if e != nil {
		return e
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if e = f.Chmod(mode); e != nil {
		f.Close()
		return e
	}
	if e = protect(tmp, false); e != nil {
		f.Close()
		return e
	}
	if _, e = f.Write(data); e != nil {
		f.Close()
		return e
	}
	if e = f.Sync(); e != nil {
		f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	return os.Rename(tmp, path)
}
func ReadRegular(path string, maxBytes int64) ([]byte, error) {
	if err := CheckPath(path); err != nil {
		return nil, err
	}
	f, e := os.Open(path)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	st, e := f.Stat()
	if e != nil {
		return nil, e
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("必须是普通文件")
	}
	current, e := os.Lstat(path)
	if e != nil {
		return nil, e
	}
	if current.Mode()&os.ModeSymlink != 0 || !os.SameFile(st, current) {
		return nil, fmt.Errorf("文件在读取时发生替换")
	}
	if st.Size() > maxBytes {
		return nil, fmt.Errorf("文件超出大小限制")
	}
	b, e := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if int64(len(b)) > maxBytes {
		return nil, fmt.Errorf("文件超出大小限制")
	}
	return b, e
}
