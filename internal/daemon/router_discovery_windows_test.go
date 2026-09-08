//go:build windows

package daemon

import (
	"errors"
	"math/bits"
	"reflect"
	"testing"

	"github.com/Tacrolimus/multi-dap/internal/bridge"
)

func TestVerifiedRouterListenersRequiresLoopbackAndExactImage(t *testing.T) {
	const port = 40123
	rows := []listenerRow{
		{address: 0x0100007f, port: uint32(bits.ReverseBytes16(port)), pid: 1},
		{address: 0x0100007f, port: uint32(bits.ReverseBytes16(port)), pid: 1},
		{address: 0x0100007f, port: uint32(bits.ReverseBytes16(40124)), pid: 2},
		{address: 0x010200c0, port: uint32(bits.ReverseBytes16(40125)), pid: 3},
	}
	images := map[uint32]string{1: `C:\MULTI\svc_router.exe`, 2: `C:\other\svc_router.exe`}
	got := verifiedRouterListeners(rows, `C:\MULTI\svc_router.exe`, func(pid uint32) (string, error) {
		if image, ok := images[pid]; ok {
			return image, nil
		}
		return "", errors.New("unreadable")
	})
	want := []bridge.ServiceRouter{{Host: "127.0.0.1", Port: port}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("verifiedRouterListeners() = %#v, want %#v", got, want)
	}
}
