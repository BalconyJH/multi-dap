package daemon

import (
	"context"
	"encoding/base64"
	"math"
	"strings"
	"testing"

	"github.com/Tacrolimus/multi-dap/internal/core/actor"
	"github.com/Tacrolimus/multi-dap/internal/core/inspection"
	"github.com/Tacrolimus/multi-dap/internal/dap"
	"github.com/Tacrolimus/multi-dap/internal/multi"
)

type m5FakeCore struct {
	*fakeCore
	readRequest actor.MemoryReadRequest
	readData    []byte
	readErr     error
	disRequest  actor.DisassemblyRequest
	disData     []multi.Instruction
	disErr      error
}

func newM5FakeCore() *m5FakeCore {
	return &m5FakeCore{fakeCore: newFakeCore(), readData: []byte{1, 2, 3}}
}

func (c *m5FakeCore) ReadMemory(_ context.Context, request actor.MemoryReadRequest) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.readRequest = request
	return append([]byte(nil), c.readData...), c.readErr
}

func (c *m5FakeCore) Disassemble(_ context.Context, request actor.DisassemblyRequest) ([]multi.Instruction, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.disRequest = request
	return append([]multi.Instruction(nil), c.disData...), c.disErr
}

func boundM5Backend(t *testing.T, defaultCore *uint64) (*Backend, *m5FakeCore) {
	t.Helper()
	core := newM5FakeCore()
	backend, err := NewBackendWithOptions(core, BackendOptions{DefaultInspectionCore: defaultCore})
	if err != nil {
		t.Fatal(err)
	}
	if err := backend.BindPublisher(&fakePublisher{accept: true}); err != nil {
		t.Fatal(err)
	}
	attach(t, backend)
	t.Cleanup(backend.Close)
	return backend, core
}

func TestM5OpaqueMemoryReferenceRoundTripsStrictly(t *testing.T) {
	value := formatMemoryReference(4, 19, 0xAbCd)
	if value != "multi-dap:core:4:stop:19:0xabcd" {
		t.Fatalf("format = %q", value)
	}
	parsed, err := parseMemoryReference(value)
	if err != nil || parsed.core != 4 || parsed.stopEpoch == nil || *parsed.stopEpoch != 19 || parsed.address != 0xabcd {
		t.Fatalf("parse = %#v, %v", parsed, err)
	}
	for _, malformed := range []string{
		"multi-dap:core:04:stop:19:0xabcd", "multi-dap:core:4:stop:019:0xabcd",
		"multi-dap:core:4:stop:19:ABCD", "multi-dap:core:4:stop:19:0x",
		"multi-dap:core:4:stop:19:0xabcd:extra",
	} {
		if _, err := parseMemoryReference(malformed); err == nil {
			t.Fatalf("malformed reference accepted: %q", malformed)
		}
	}
}

func TestM5ReadMemoryRoutesExplicitDefaultAndPreservesEpoch(t *testing.T) {
	defaultCore := uint64(1)
	backend, core := boundM5Backend(t, &defaultCore)
	result, err := backend.Execute(context.Background(), dap.Action{Kind: dap.ActionReadMemory, ReadMemory: dap.ReadMemoryArguments{
		MemoryReference: "0x100", Offset: 2, Count: 3,
	}})
	if err != nil || result.ReadMemory.Address != "0x102" || result.ReadMemory.Data != base64.StdEncoding.EncodeToString([]byte{1, 2, 3}) {
		t.Fatalf("read = %#v, %v", result.ReadMemory, err)
	}
	core.mu.Lock()
	request := core.readRequest
	core.mu.Unlock()
	if request.CoreID != 1 || request.Address != 0x102 || request.Count != 3 || request.ExpectedStopEpoch != nil {
		t.Fatalf("numeric request = %#v", request)
	}

	_, err = backend.Execute(context.Background(), dap.Action{Kind: dap.ActionReadMemory, ReadMemory: dap.ReadMemoryArguments{
		MemoryReference: formatMemoryReference(0, 7, 0x20), Count: 3,
	}})
	if err != nil {
		t.Fatal(err)
	}
	core.mu.Lock()
	request = core.readRequest
	core.mu.Unlock()
	if request.CoreID != 0 || request.ExpectedStopEpoch == nil || *request.ExpectedStopEpoch != 7 {
		t.Fatalf("opaque request = %#v", request)
	}
}

func TestM5NumericReferenceIsOnlyAllowedForExplicitOrSingleCore(t *testing.T) {
	multiCore, _ := boundM5Backend(t, nil)
	_, err := multiCore.Execute(context.Background(), dap.Action{Kind: dap.ActionReadMemory, ReadMemory: dap.ReadMemoryArguments{MemoryReference: "0x20", Count: 3}})
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("multi-core numeric request = %v, want ambiguity", err)
	}

	// A frontend's topology is immutable after attach; establish it before attach.
	core := newM5FakeCore()
	core.threads = core.threads[:1]
	backend, err := NewBackend(core)
	if err != nil {
		t.Fatal(err)
	}
	if err := backend.BindPublisher(&fakePublisher{accept: true}); err != nil {
		t.Fatal(err)
	}
	attach(t, backend)
	t.Cleanup(backend.Close)
	if _, err := backend.Execute(context.Background(), dap.Action{Kind: dap.ActionReadMemory, ReadMemory: dap.ReadMemoryArguments{MemoryReference: "0x20", Count: 3}}); err != nil {
		t.Fatalf("single-core numeric request: %v", err)
	}
	core.mu.Lock()
	request := core.readRequest
	core.mu.Unlock()
	if request.CoreID != 0 {
		t.Fatalf("single-core route = %#v", request)
	}
}

func TestM5OffsetsAreCheckedAndDisassemblyMapsInstructions(t *testing.T) {
	defaultCore := uint64(0)
	backend, core := boundM5Backend(t, &defaultCore)
	for _, arguments := range []dap.ReadMemoryArguments{
		{MemoryReference: "0xffffffffffffffff", Offset: 1, Count: 3},
		{MemoryReference: "0x0", Offset: -1, Count: 3},
	} {
		if _, err := backend.Execute(context.Background(), dap.Action{Kind: dap.ActionReadMemory, ReadMemory: arguments}); err == nil {
			t.Fatalf("unchecked read offset accepted: %#v", arguments)
		}
	}
	core.mu.Lock()
	core.disData = []multi.Instruction{{Address: 0x102, Bytes: []byte{0x12, 0x34}, Text: "nop"}, {Address: 0x104, Text: "jr 0x10"}}
	core.mu.Unlock()
	result, err := backend.Execute(context.Background(), dap.Action{Kind: dap.ActionDisassemble, Disassemble: dap.DisassembleArguments{
		MemoryReference: "0x100", Offset: 2, InstructionOffset: 0, InstructionCount: 2,
	}})
	if err != nil || len(result.Disassembly) != 2 || result.Disassembly[0].Address != "0x102" || result.Disassembly[0].InstructionBytes != "1234" || result.Disassembly[1].InstructionBytes != "" || result.Disassembly[1].Instruction != "jr 0x10" {
		t.Fatalf("disassembly = %#v, %v", result.Disassembly, err)
	}
	core.mu.Lock()
	request := core.disRequest
	core.mu.Unlock()
	if request.Address != 0x102 || request.CoreID != 0 || request.InstructionOffset != 0 {
		t.Fatalf("disassembly request = %#v", request)
	}
	if _, err := backend.Execute(context.Background(), dap.Action{Kind: dap.ActionDisassemble, Disassemble: dap.DisassembleArguments{MemoryReference: "0xffffffffffffffff", Offset: 1, InstructionCount: 2}}); err == nil {
		t.Fatal("overflowing disassembly offset accepted")
	}
}

func TestM5StackFramesExposeStopBoundInstructionReference(t *testing.T) {
	backend, core := boundM5Backend(t, nil)
	core.threads = core.threads[:1]
	core.stackResult = inspection.Stack{Frames: []inspection.Frame{{
		ID: 1, Name: "main", HasInstructionAddress: true,
		InstructionAddress: inspection.AddressReference{Core: 0, StopEpoch: 8, Address: 0x400},
	}}}
	result, err := backend.Execute(context.Background(), dap.Action{Kind: dap.ActionStackTrace, StackTrace: dap.StackTraceArguments{ThreadID: 1}})
	if err != nil || len(result.StackFrames) != 1 || result.StackFrames[0].InstructionPointerReference != formatMemoryReference(0, 8, 0x400) {
		t.Fatalf("frames = %#v, %v", result.StackFrames, err)
	}
}

func TestCheckedAddressOffsetMinInt(t *testing.T) {
	if _, err := checkedAddressOffset(uint64(math.MaxInt64), math.MinInt64); err == nil {
		t.Fatal("MinInt64 underflow accepted")
	}
}
