//go:build !windows

package daemon

import (
	"context"
	"strings"
	"testing"

	"github.com/Tacrolimus/multi-dap/internal/bridge"
	"github.com/Tacrolimus/multi-dap/internal/config"
)

func TestRuntimeRejectsNativeNonWindowsWarmIdentityBeforeOpen(t *testing.T) {
	// A native temporary file exercises warm identity validation, but cannot
	// supply the Windows program identity required by the production resolver.
	fake := newRuntimeFakeBridge(t)
	options := testWarmRuntimeOptions(t)
	var launched bridge.LaunchSpec
	_, err := startWith(context.Background(), options, func(_ context.Context, spec bridge.LaunchSpec) (bridgeProcess, error) {
		launched = spec
		return fake, nil
	})
	if err == nil || !strings.Contains(err.Error(), "configured core 0 ELF: must be a Windows drive or UNC absolute path") {
		t.Fatalf("native non-Windows warm identity error = %v", err)
	}
	if launched.PythonVersion != config.PythonVersion2 || launched.SessionMode != bridge.SessionModeWarm ||
		launched.ServiceRouter == nil || launched.ServiceRouter.Host != options.Warm.ServiceRouterHost ||
		launched.ServiceRouter.Port != options.Warm.ServiceRouterPort {
		t.Fatalf("warm launch identity = %#v", launched)
	}
	if _, opened := fake.requestParams("open"); opened {
		t.Fatal("unsupported program identity reached the MULTI open request")
	}
	select {
	case <-fake.Done():
	default:
		t.Fatal("rejected startup did not close its owned fake bridge")
	}
}
