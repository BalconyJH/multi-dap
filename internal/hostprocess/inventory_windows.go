//go:build windows

package hostprocess

import (
	"context"
	"errors"
	"io"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"
)

const queryLimitedInformation = 0x1000

var queryFullProcessImageName = syscall.NewLazyDLL("kernel32.dll").NewProc("QueryFullProcessImageNameW")

// Collect takes one advisory snapshot without opening MULTI, querying modules,
// requesting debug privileges, or modifying any process. The deadline is
// checked between bounded kernel calls; it cannot cancel a kernel call.
func Collect(ctx context.Context, installation string) Report {
	if ctx == nil {
		ctx = context.Background()
	}
	bounded, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return collectWith(bounded, installation, openWindowsSnapshot)
}

type windowsSnapshot struct {
	handle  syscall.Handle
	started bool
	cutoff  uint64
}

func openWindowsSnapshot() (snapshot, error) {
	cutoff := syscall.NsecToFiletime(time.Now().UnixNano())
	handle, err := syscall.CreateToolhelp32Snapshot(syscall.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	return &windowsSnapshot{handle: handle, cutoff: filetimeValue(cutoff)}, nil
}

func (s *windowsSnapshot) next() (snapshotEntry, error) {
	entry := syscall.ProcessEntry32{}
	entry.Size = uint32(unsafe.Sizeof(entry))
	var err error
	if s.started {
		err = syscall.Process32Next(s.handle, &entry)
	} else {
		s.started = true
		err = syscall.Process32First(s.handle, &entry)
	}
	if err == syscall.ERROR_NO_MORE_FILES {
		return snapshotEntry{}, io.EOF
	}
	if err != nil {
		return snapshotEntry{}, err
	}
	name, valid := strictUTF16(entry.ExeFile[:], true)
	if !valid {
		name = ""
	}
	return snapshotEntry{pid: entry.ProcessID, parentPID: entry.ParentProcessID, name: name}, nil
}

func (s *windowsSnapshot) image(entry snapshotEntry) (string, error) {
	handle, err := syscall.OpenProcess(queryLimitedInformation, false, entry.pid)
	if err != nil {
		return "", err
	}
	defer syscall.CloseHandle(handle)
	var created, exited, kernel, user syscall.Filetime
	if err := syscall.GetProcessTimes(handle, &created, &exited, &kernel, &user); err != nil {
		return "", err
	}
	// A process born after collection began cannot safely inherit the snapshot's
	// name or parent PID. This also rejects PID reuse during collection.
	if filetimeValue(created) == 0 || filetimeValue(created) > s.cutoff {
		return "", errors.New("process identity changed during snapshot")
	}
	buffer := make([]uint16, maxPathUnits)
	size := uint32(len(buffer))
	result, _, err := queryFullProcessImageName.Call(uintptr(handle), 0,
		uintptr(unsafe.Pointer(&buffer[0])), uintptr(unsafe.Pointer(&size)))
	if result == 0 {
		return "", err
	}
	if size == 0 || size >= uint32(len(buffer)) || buffer[size] != 0 {
		return "", errors.New("invalid process image length")
	}
	path, valid := strictUTF16(buffer[:size], false)
	if !valid {
		return "", errors.New("invalid process image text")
	}
	return path, nil
}

func (s *windowsSnapshot) close() error {
	return syscall.CloseHandle(s.handle)
}

func filetimeValue(value syscall.Filetime) uint64 {
	return uint64(value.HighDateTime)<<32 | uint64(value.LowDateTime)
}

func strictUTF16(value []uint16, terminated bool) (string, bool) {
	end := len(value)
	for index, unit := range value {
		if unit == 0 {
			if !terminated {
				return "", false
			}
			end = index
			break
		}
	}
	if terminated && end == len(value) {
		return "", false
	}
	value = value[:end]
	for index := 0; index < len(value); index++ {
		unit := value[index]
		if unit >= 0xD800 && unit <= 0xDBFF {
			index++
			if index == len(value) || value[index] < 0xDC00 || value[index] > 0xDFFF {
				return "", false
			}
		} else if unit >= 0xDC00 && unit <= 0xDFFF {
			return "", false
		}
	}
	return string(utf16.Decode(value)), true
}
