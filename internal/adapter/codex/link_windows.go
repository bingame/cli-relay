package codex

import (
	"encoding/binary"
	"os"
	"strings"
	"unicode/utf16"

	"golang.org/x/sys/windows"
)

// Junction 无需开发者模式或管理员权限；文件使用同卷硬链接，绝不复制凭据文件。
func linkNativePath(source, destination string, directory bool) error {
	if !directory {
		return os.Link(source, destination)
	}
	if err := os.Mkdir(destination, 0700); err != nil {
		return err
	}
	ok := false
	defer func() {
		if !ok {
			os.Remove(destination)
		}
	}()
	path, err := windows.UTF16PtrFromString(destination)
	if err != nil {
		return err
	}
	h, err := windows.CreateFile(path, windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	target := `\??\` + source
	if strings.HasPrefix(source, `\\`) {
		target = `\??\UNC\` + strings.TrimPrefix(source, `\\`)
	}
	substitute, printName := utf16.Encode([]rune(target)), utf16.Encode([]rune(source))
	buf := make([]byte, 16+2*(len(substitute)+1+len(printName)+1))
	binary.LittleEndian.PutUint32(buf, windows.IO_REPARSE_TAG_MOUNT_POINT)
	binary.LittleEndian.PutUint16(buf[4:], uint16(len(buf)-8))
	binary.LittleEndian.PutUint16(buf[10:], uint16(len(substitute)*2))
	binary.LittleEndian.PutUint16(buf[12:], uint16((len(substitute)+1)*2))
	binary.LittleEndian.PutUint16(buf[14:], uint16(len(printName)*2))
	for i, char := range append(append(substitute, 0), append(printName, 0)...) {
		binary.LittleEndian.PutUint16(buf[16+i*2:], char)
	}
	var returned uint32
	err = windows.DeviceIoControl(h, windows.FSCTL_SET_REPARSE_POINT, &buf[0], uint32(len(buf)), nil, 0, &returned, nil)
	ok = err == nil
	return err
}
