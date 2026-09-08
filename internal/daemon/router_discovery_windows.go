//go:build windows

package daemon

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math/bits"
	"net/netip"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"github.com/Tacrolimus/multi-dap/internal/bridge"
)

const (
	afInet                   = 2
	tcpTableOwnerPIDListener = 3
	errorInsufficientBuffer  = syscall.Errno(122)
	processQueryLimitedInfo  = 0x1000
)

var (
	iphlpapi                   = syscall.NewLazyDLL("iphlpapi.dll")
	routerKernel32             = syscall.NewLazyDLL("kernel32.dll")
	getExtendedTCPTable        = iphlpapi.NewProc("GetExtendedTcpTable")
	routerOpenProcess          = routerKernel32.NewProc("OpenProcess")
	routerCloseProcess         = routerKernel32.NewProc("CloseHandle")
	queryFullProcessImageNameW = routerKernel32.NewProc("QueryFullProcessImageNameW")
)

// discoverWarmRouters returns every IPv4 loopback listener whose owning
// process image is exactly the configured installation's svc_router.exe.
func discoverWarmRouters(ctx context.Context, installation string) ([]bridge.ServiceRouter, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	table, err := ownerPIDListenerTable()
	if err != nil {
		return nil, err
	}
	want := filepath.Clean(filepath.Join(installation, "svc_router.exe"))
	return verifiedRouterListeners(table, want, processImage), nil
}

func verifiedRouterListeners(table []listenerRow, want string, imageForPID func(uint32) (string, error)) []bridge.ServiceRouter {
	seen := make(map[string]struct{})
	result := make([]bridge.ServiceRouter, 0)
	for _, row := range table {
		ip := netip.AddrFrom4([4]byte{byte(row.address), byte(row.address >> 8), byte(row.address >> 16), byte(row.address >> 24)})
		if !ip.IsLoopback() {
			continue
		}
		image, err := imageForPID(row.pid)
		if err != nil || !strings.EqualFold(filepath.Clean(image), want) {
			continue
		}
		port := int(bits.ReverseBytes16(uint16(row.port)))
		if port < 1 || port > 65535 {
			continue
		}
		candidate := bridge.ServiceRouter{Host: ip.String(), Port: port}
		key := candidate.Host + ":" + fmt.Sprint(candidate.Port)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, candidate)
	}
	return result
}

type listenerRow struct{ address, port, pid uint32 }

func ownerPIDListenerTable() ([]listenerRow, error) {
	var size uint32
	result, _, callErr := getExtendedTCPTable.Call(0, uintptr(unsafe.Pointer(&size)), 0, afInet, tcpTableOwnerPIDListener, 0)
	if result != uintptr(errorInsufficientBuffer) {
		return nil, fmt.Errorf("daemon: size service-router listener table: %w", callErr)
	}
	buffer := make([]byte, size)
	result, _, callErr = getExtendedTCPTable.Call(uintptr(unsafe.Pointer(&buffer[0])), uintptr(unsafe.Pointer(&size)), 0, afInet, tcpTableOwnerPIDListener, 0)
	if result != 0 {
		return nil, fmt.Errorf("daemon: read service-router listener table: %w", callErr)
	}
	if len(buffer) < 4 {
		return nil, errors.New("daemon: invalid service-router listener table")
	}
	count := int(binary.LittleEndian.Uint32(buffer[:4]))
	const rowSize = 24
	if count < 0 || count > (len(buffer)-4)/rowSize {
		return nil, errors.New("daemon: invalid service-router listener count")
	}
	rows := make([]listenerRow, 0, count)
	for index := 0; index < count; index++ {
		offset := 4 + index*rowSize
		rows = append(rows, listenerRow{
			address: binary.LittleEndian.Uint32(buffer[offset+4:]),
			port:    binary.LittleEndian.Uint32(buffer[offset+8:]),
			pid:     binary.LittleEndian.Uint32(buffer[offset+20:]),
		})
	}
	return rows, nil
}

func processImage(pid uint32) (string, error) {
	handle, _, callErr := routerOpenProcess.Call(processQueryLimitedInfo, 0, uintptr(pid))
	if handle == 0 {
		return "", callErr
	}
	defer routerCloseProcess.Call(handle)
	buffer := make([]uint16, 32768)
	size := uint32(len(buffer))
	result, _, callErr := queryFullProcessImageNameW.Call(handle, 0, uintptr(unsafe.Pointer(&buffer[0])), uintptr(unsafe.Pointer(&size)))
	if result == 0 {
		return "", callErr
	}
	return syscall.UTF16ToString(buffer[:size]), nil
}
