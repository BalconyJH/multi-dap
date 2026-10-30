//go:build windows

package daemon

import (
	"context"
	"testing"

	"github.com/Tacrolimus/multi-dap/internal/bridge"
	"github.com/Tacrolimus/multi-dap/internal/config"
	"github.com/Tacrolimus/multi-dap/internal/multi"
)

func TestRuntimeWarmSessionUsesOneAtomicLaunchAndOpenIdentity(t *testing.T) {
	fake := newRuntimeFakeBridge(t)
	options := testWarmRuntimeOptions(t)
	primaryELF := options.Warm.PrimaryELF
	fake.setResult("cores", map[string]any{"components": "fixture.component.5 (debugger.name.core0)\n", "status": 1})
	fake.setCommandResult("route fixture.component.5 P", map[string]any{"status": 1, "raw": "     # PID        PPID       Status        CBEFITDHR Name and Arguments\n>>   0 0x1 N/A        Stopped       011111101 " + primaryELF + "\n"})
	var launched bridge.LaunchSpec
	runtime, err := startWith(context.Background(), options, func(_ context.Context, spec bridge.LaunchSpec) (bridgeProcess, error) {
		launched = spec
		return fake, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtime.Close)
	if launched.PythonVersion != config.PythonVersion2 || launched.SessionMode != bridge.SessionModeWarm || launched.ServiceRouter == nil || launched.ServiceRouter.Host != "127.0.0.1" || launched.ServiceRouter.Port != 40123 {
		t.Fatalf("warm launch = %#v", launched)
	}
	params, ok := fake.requestParams("open")
	if !ok {
		t.Fatal("warm open request was not observed")
	}
	if params["mode"] != string(multi.OpenModeWarm) || params["primary_elf"] != primaryELF || len(params) != 2 {
		t.Fatalf("warm open params = %#v", params)
	}
}
