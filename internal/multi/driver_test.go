package multi

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

type fakeCall struct {
	method string
	params any
	result any
	err    error
}

type fakeCaller struct {
	calls []fakeCall
}

func (f *fakeCaller) Call(_ context.Context, method string, params, result any) error {
	if len(f.calls) == 0 {
		return errors.New("unexpected call")
	}
	call := f.calls[0]
	f.calls = f.calls[1:]
	if call.method != method {
		return errors.New("unexpected method " + method)
	}
	if !reflect.DeepEqual(call.params, params) {
		return errors.New("unexpected params")
	}
	if call.err != nil {
		return call.err
	}
	encoded, err := json.Marshal(call.result)
	if err != nil {
		return err
	}
	return json.Unmarshal(encoded, result)
}

func newTestDriver(t *testing.T, calls ...fakeCall) (*Driver, *fakeCaller) {
	t.Helper()
	caller := &fakeCaller{calls: calls}
	driver, err := NewDriver(caller)
	if err != nil {
		t.Fatal(err)
	}
	return driver, caller
}

func TestDriverM1Methods(t *testing.T) {
	components := "The currently registered components are:\nfixture-router (debugserver.name.fixture-router)\nfixture-core (debugger.pid.1)\n"
	driver, caller := newTestDriver(t,
		fakeCall{method: "open", params: map[string]string{
			"mode": "cold", "project": "fixture-workspace.ghsmc", "connection": "fixture-link",
			"setup_script": "fixture-setup.py", "setup_script_args": "--fixture", "multi_log": "fixture.log",
		}, result: map[string]any{"opened": true}},
		fakeCall{method: "state", params: map[string]string{}, result: map[string]any{
			"status": 2, "process_info": map[string]any{"stopStamp": "17", "file  ": "fixture_unit.c"}, "raw_lossy": true,
		}},
		fakeCall{method: "cores", params: map[string]string{}, result: map[string]any{"status": 1, "components": components}},
		fakeCall{method: "resume", params: map[string]string{}, result: map[string]any{"accepted": true}},
		fakeCall{method: "halt", params: map[string]string{}, result: map[string]any{"accepted": true}},
		fakeCall{method: "close", params: map[string]string{}, result: map[string]any{"closed": true}},
	)

	if err := driver.Open(context.Background(), OpenRequest{
		Project: "fixture-workspace.ghsmc", Connection: "fixture-link", SetupScript: "fixture-setup.py", SetupScriptArgs: "--fixture", MultiLog: "fixture.log",
	}); err != nil {
		t.Fatal(err)
	}
	state, err := driver.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != StatusStopped {
		t.Fatalf("status = %v", state.Status)
	}
	if !state.RawLossy {
		t.Fatal("state did not preserve the bridge raw_lossy marker")
	}
	if got, ok := state.ProcessInfo.StopStamp(); !ok || got != 17 {
		t.Fatalf("stop stamp = %d, %t", got, ok)
	}
	if got := state.ProcessInfo.File(); got != "fixture_unit.c" {
		t.Fatalf("file = %q", got)
	}
	gotComponents, err := driver.Components(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(gotComponents) != 2 || gotComponents[1].Role != ComponentPIDAlias || gotComponents[1].ProcessID != 1 {
		t.Fatalf("components = %#v", gotComponents)
	}
	if err := driver.Resume(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := driver.Halt(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := driver.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(caller.calls) != 0 {
		t.Fatalf("unconsumed calls: %#v", caller.calls)
	}
}

func TestDriverRejectsMalformedBridgeResults(t *testing.T) {
	tests := []struct {
		name string
		call fakeCall
		run  func(*Driver) error
	}{
		{
			name: "unknown status",
			call: fakeCall{method: "state", params: map[string]string{}, result: map[string]any{"status": 9, "process_info": map[string]any{}}},
			run:  func(d *Driver) error { _, err := d.State(context.Background()); return err },
		},
		{
			name: "fractional status",
			call: fakeCall{method: "state", params: map[string]string{}, result: map[string]any{"status": 2.5, "process_info": map[string]any{}}},
			run:  func(d *Driver) error { _, err := d.State(context.Background()); return err },
		},
		{
			name: "non string process field",
			call: fakeCall{method: "state", params: map[string]string{}, result: map[string]any{"status": 2, "process_info": map[string]any{"stopStamp": 3}}},
			run:  func(d *Driver) error { _, err := d.State(context.Background()); return err },
		},
		{
			name: "unknown state result field",
			call: fakeCall{method: "state", params: map[string]string{}, result: map[string]any{"status": 2, "process_info": map[string]any{}, "other": true}},
			run:  func(d *Driver) error { _, err := d.State(context.Background()); return err },
		},
		{
			name: "non boolean lossiness marker",
			call: fakeCall{method: "state", params: map[string]string{}, result: map[string]any{"status": 2, "process_info": map[string]any{}, "raw_lossy": "yes"}},
			run:  func(d *Driver) error { _, err := d.State(context.Background()); return err },
		},
		{
			name: "resume refused",
			call: fakeCall{method: "resume", params: map[string]string{}, result: map[string]any{"accepted": false}},
			run:  func(d *Driver) error { return d.Resume(context.Background()) },
		},
		{
			name: "halt malformed",
			call: fakeCall{method: "halt", params: map[string]string{}, result: map[string]any{"accepted": true, "extra": true}},
			run:  func(d *Driver) error { return d.Halt(context.Background()) },
		},
		{
			name: "cores command failure",
			call: fakeCall{method: "cores", params: map[string]string{}, result: map[string]any{"status": 0, "components": "fixture-core (debugger.pid.1)"}},
			run:  func(d *Driver) error { _, err := d.Components(context.Background()); return err },
		},
		{
			name: "cores non string payload",
			call: fakeCall{method: "cores", params: map[string]string{}, result: map[string]any{"status": 1, "components": []string{"fixture-core"}}},
			run:  func(d *Driver) error { _, err := d.Components(context.Background()); return err },
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			driver, _ := newTestDriver(t, test.call)
			if err := test.run(driver); !errors.Is(err, ErrProtocol) {
				t.Fatalf("error = %v, want ErrProtocol", err)
			}
		})
	}
}

func TestDriverPropagatesCallerFailure(t *testing.T) {
	want := errors.New("bridge unavailable")
	driver, _ := newTestDriver(t, fakeCall{method: "halt", params: map[string]string{}, err: want})
	if err := driver.Halt(context.Background()); !errors.Is(err, want) {
		t.Fatalf("error = %v, want wrapping %v", err, want)
	}
}

func TestDriverRejectsInvalidOpenRequestAndNilCaller(t *testing.T) {
	if _, err := NewDriver(nil); err == nil {
		t.Fatal("NewDriver(nil) succeeded")
	}
	driver, _ := newTestDriver(t)
	if err := driver.Open(context.Background(), OpenRequest{Connection: "fixture-link"}); err == nil {
		t.Fatal("open without project succeeded")
	}
	if err := driver.Open(context.Background(), OpenRequest{Project: "fixture-workspace"}); err == nil {
		t.Fatal("open without connection succeeded")
	}
}

func TestDriverWarmOpenUsesOnlyPrimaryELF(t *testing.T) {
	driver, caller := newTestDriver(t, fakeCall{
		method: "open",
		params: map[string]string{
			"mode": "warm", "primary_elf": `X:\\fixture\\bin\\primary.elf`,
		},
		result: map[string]any{"opened": true},
	})
	if err := driver.Open(context.Background(), OpenRequest{
		Mode: OpenModeWarm, PrimaryELF: `X:\\fixture\\bin\\primary.elf`,
	}); err != nil {
		t.Fatal(err)
	}
	if len(caller.calls) != 0 {
		t.Fatalf("unconsumed calls: %#v", caller.calls)
	}
}

func TestDriverColdPreparationUsesStableOpenParameter(t *testing.T) {
	driver, caller := newTestDriver(t, fakeCall{
		method: "open",
		params: map[string]string{
			"mode": "cold", "project": "fixture-workspace.ghsmc", "connection": "fixture-link",
			"preparation": "already_present_no_verify",
		},
		result: map[string]any{"opened": true},
	})
	if err := driver.Open(context.Background(), OpenRequest{
		Mode: OpenModeCold, Project: "fixture-workspace.ghsmc", Connection: "fixture-link",
		Preparation: ColdPreparationAlreadyPresentNoVerify,
	}); err != nil {
		t.Fatal(err)
	}
	if len(caller.calls) != 0 {
		t.Fatalf("unconsumed calls: %#v", caller.calls)
	}
}

func TestDriverRejectsMixedOrIncompleteWarmOpen(t *testing.T) {
	driver, _ := newTestDriver(t)
	for _, request := range []OpenRequest{
		{Mode: OpenModeWarm},
		{Mode: OpenModeWarm, PrimaryELF: `X:\\fixture\\bin\\primary.elf`, Connection: "fixture-link"},
		{Mode: OpenModeWarm, PrimaryELF: `X:\\fixture\\bin\\primary.elf`, Preparation: ColdPreparationAlreadyPresentNoVerify},
		{Mode: OpenModeCold, Project: "fixture-workspace", Connection: "fixture-link", PrimaryELF: `X:\\fixture\\bin\\primary.elf`},
		{Mode: OpenModeCold, Project: "fixture-workspace", Connection: "fixture-link", Preparation: "unknown"},
		{Mode: "unknown", PrimaryELF: `X:\\fixture\\bin\\primary.elf`},
	} {
		if err := driver.Open(context.Background(), request); err == nil {
			t.Fatalf("Open(%+v) succeeded", request)
		}
	}
}

func TestDriverRunCommandsIsInternalMechanism(t *testing.T) {
	driver, caller := newTestDriver(t, fakeCall{
		method: "run_commands", params: map[string]string{"commands": "components"},
		result: map[string]any{"status": 1, "raw": "ok", "raw_lossy": true},
	})
	result, err := driver.runCommands(context.Background(), "components")
	if err != nil || result.Raw != "ok" || !result.RawLossy {
		t.Fatalf("runCommands = %#v, %v", result, err)
	}
	if len(caller.calls) != 0 {
		t.Fatalf("unconsumed calls: %#v", caller.calls)
	}
}
