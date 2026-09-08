package daemon

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/Tacrolimus/multi-dap/internal/core/actor"
	"github.com/Tacrolimus/multi-dap/internal/core/breakpoint"
	"github.com/Tacrolimus/multi-dap/internal/core/session"
	"github.com/Tacrolimus/multi-dap/internal/core/source"
	"github.com/Tacrolimus/multi-dap/internal/core/stop"
	"github.com/Tacrolimus/multi-dap/internal/dap"
	"github.com/Tacrolimus/multi-dap/internal/multi"
)

// TestDAPE2EConfigurationBarrierAndFailClosedOptionalRequests covers the
// daemon-to-TCP path, rather than only the individual DAP and daemon units.
// In particular, a stop observed by the actor must not be visible before the
// held attach response. It also distinguishes the supported default M4
// stepping operations from unavailable M4/M5 requests.
func TestDAPE2EConfigurationBarrierAndFailClosedOptionalRequests(t *testing.T) {
	core := newFakeCore()
	core.threads = core.threads[:1]
	path := `C:\workspace\main.c`
	identity := source.Identity{ClientPath: path, DebugPath: `C:/workspace/main.c`, Key: `c:/workspace/main.c`}
	core.breakResult = breakpoint.ReplaceResult{Breakpoints: []breakpoint.Logical{{
		DAPID: 61, Source: identity, Line: 5, Verified: true,
	}}}

	backend, err := NewBackend(core)
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	server, err := dap.Listen("127.0.0.1:0", backend)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	if err := backend.BindPublisher(server); err != nil {
		t.Fatal(err)
	}

	serveContext, stopServing := context.WithCancel(context.Background())
	defer stopServing()
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(serveContext) }()

	connection, err := net.Dial("tcp", server.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	encoder := dap.NewEncoder(connection)
	decoder := dap.NewDecoder(connection)

	writeDAPRequest(t, encoder, 1, "initialize", `{"adapterID":"multi-dap"}`)
	initialized := readDAPMessage(t, connection, decoder)
	if initialized.Type != dap.TypeResponse || initialized.Command != "initialize" || initialized.Success == nil || !*initialized.Success {
		t.Fatalf("initialize response = %#v", initialized)
	}
	var capabilities dap.Capabilities
	if err := json.Unmarshal(initialized.Body, &capabilities); err != nil {
		t.Fatalf("decode initialize capabilities: %v", err)
	}
	if !capabilities.SupportsConfigurationDoneRequest || capabilities.SupportsSteppingGranularity || capabilities.SupportsReadMemoryRequest || capabilities.SupportsWriteMemoryRequest || capabilities.SupportsDisassembleRequest || capabilities.SupportsDelayedStackTraceLoading {
		t.Fatalf("default runtime capabilities = %#v", capabilities)
	}
	var capabilityWire map[string]json.RawMessage
	if err := json.Unmarshal(initialized.Body, &capabilityWire); err != nil {
		t.Fatalf("decode initialize capability wire body: %v", err)
	}
	for _, field := range []string{"supportsVariablePaging", "supportsVariableType"} {
		if _, ok := capabilityWire[field]; ok {
			t.Fatalf("initialize response illegally includes client capability %q: %s", field, initialized.Body)
		}
	}

	writeDAPRequest(t, encoder, 2, "attach", `{}`)
	if attached := readDAPMessage(t, connection, decoder); attached.Type != dap.TypeEvent || attached.Event != "initialized" {
		t.Fatalf("attach output = %#v, want initialized event", attached)
	}
	writeDAPRequest(t, encoder, 3, "setBreakpoints", `{"source":{"path":"C:\\workspace\\main.c"},"breakpoints":[{"line":5}]}`)
	breakpoints := readDAPMessage(t, connection, decoder)
	if breakpoints.Type != dap.TypeResponse || breakpoints.Success == nil || !*breakpoints.Success {
		t.Fatalf("setBreakpoints response = %#v", breakpoints)
	}
	var breakpointBody dap.SetBreakpointsBody
	if err := json.Unmarshal(breakpoints.Body, &breakpointBody); err != nil {
		t.Fatalf("decode breakpoints: %v", err)
	}
	if len(breakpointBody.Breakpoints) != 1 || breakpointBody.Breakpoints[0].ID != 61 || !breakpointBody.Breakpoints[0].Verified {
		t.Fatalf("breakpoints = %#v", breakpointBody)
	}

	writeDAPRequest(t, encoder, 4, "configurationDone", `{}`)
	for _, want := range []string{"configurationDone", "attach"} {
		message := readDAPMessage(t, connection, decoder)
		if message.Type != dap.TypeResponse || message.Command != want || message.Success == nil || !*message.Success {
			t.Fatalf("configuration barrier output = %#v, want successful %s response", message, want)
		}
	}

	core.mu.Lock()
	owner := core.attachOwners[0]
	core.mu.Unlock()
	core.emit(owner, actor.Event{
		Kind:             actor.EventStopped,
		ThreadID:         1,
		Snapshot:         session.Snapshot{State: session.TargetStopped, StopEpoch: 8},
		Reason:           stop.ReasonBreakpoint,
		HitBreakpointIDs: []int{61},
	})
	stopped := readDAPMessage(t, connection, decoder)
	if stopped.Type != dap.TypeEvent || stopped.Event != "stopped" {
		t.Fatalf("stop event = %#v", stopped)
	}
	var stoppedBody dap.StoppedBody
	if err := json.Unmarshal(stopped.Body, &stoppedBody); err != nil {
		t.Fatalf("decode stopped event: %v", err)
	}
	if stoppedBody.Reason != "breakpoint" || stoppedBody.ThreadID != 1 || len(stoppedBody.HitBreakpointIDs) != 1 || stoppedBody.HitBreakpointIDs[0] != 61 {
		t.Fatalf("stopped body = %#v", stoppedBody)
	}

	for _, request := range []struct {
		seq     int64
		command string
	}{
		{5, "next"},
		{6, "stepIn"},
	} {
		writeDAPRequest(t, encoder, request.seq, request.command, `{"threadId":1,"granularity":"statement"}`)
		response := readDAPMessage(t, connection, decoder)
		if response.Type != dap.TypeResponse || response.Command != request.command || response.Success == nil || !*response.Success {
			t.Fatalf("%s response = %#v", request.command, response)
		}
	}
	core.mu.Lock()
	stepped := len(core.executions) == 2 && core.executions[0].owner == owner && core.executions[0].request == (actor.ExecutionRequest{CoreID: 0, Operation: multi.ExecutionNext}) && core.executions[1].owner == owner && core.executions[1].request == (actor.ExecutionRequest{CoreID: 0, Operation: multi.ExecutionStepIn})
	core.mu.Unlock()
	if !stepped {
		t.Fatal("M4 commands did not reach the leased actor frontend")
	}

	for _, request := range []struct {
		seq       int64
		command   string
		arguments string
		message   string
	}{
		{7, "stepOut", `{"threadId":1,"granularity":"statement"}`, "unsupported DAP action"},
		{8, "readMemory", `{"memoryReference":"core:0:0x1000","count":1}`, "inspection is unavailable"},
	} {
		writeDAPRequest(t, encoder, request.seq, request.command, request.arguments)
		response := readDAPMessage(t, connection, decoder)
		if response.Type != dap.TypeResponse || response.Command != request.command || response.Success == nil || *response.Success || !strings.Contains(response.Message, request.message) {
			t.Fatalf("%s fail-closed response = %#v", request.command, response)
		}
	}

	writeDAPRequest(t, encoder, 9, "disconnect", `{}`)
	if disconnected := readDAPMessage(t, connection, decoder); disconnected.Type != dap.TypeResponse || disconnected.Command != "disconnect" || disconnected.Success == nil || !*disconnected.Success {
		t.Fatalf("disconnect response = %#v", disconnected)
	}

	stopServing()
	if err := <-serveDone; err != nil {
		t.Fatalf("DAP Serve() = %v", err)
	}
}

func TestDAPE2EAdvertisesDelayedStackTraceLoadingWhenRuntimeWiresIt(t *testing.T) {
	backend, err := NewBackendWithOptions(newFakeCore(), BackendOptions{Capabilities: dap.Capabilities{
		SupportsDelayedStackTraceLoading: true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	server, err := dap.Listen("127.0.0.1:0", backend)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	if err := backend.BindPublisher(server); err != nil {
		t.Fatal(err)
	}

	serveContext, stopServing := context.WithCancel(context.Background())
	defer stopServing()
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(serveContext) }()
	connection, err := net.Dial("tcp", server.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	encoder := dap.NewEncoder(connection)
	decoder := dap.NewDecoder(connection)
	writeDAPRequest(t, encoder, 1, "initialize", `{"adapterID":"multi-dap"}`)
	initialized := readDAPMessage(t, connection, decoder)
	var capabilities dap.Capabilities
	if err := json.Unmarshal(initialized.Body, &capabilities); err != nil {
		t.Fatalf("decode initialize capabilities: %v", err)
	}
	if initialized.Success == nil || !*initialized.Success || !capabilities.SupportsDelayedStackTraceLoading {
		t.Fatalf("delayed-stack initialize response = %#v capabilities=%#v", initialized, capabilities)
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	stopServing()
	if err := <-serveDone; err != nil {
		t.Fatal(err)
	}
}

func writeDAPRequest(t *testing.T, encoder *dap.Encoder, sequence int64, command, arguments string) {
	t.Helper()
	if err := encoder.Encode(dap.Envelope{Seq: sequence, Type: dap.TypeRequest, Command: command, Arguments: json.RawMessage(arguments)}); err != nil {
		t.Fatalf("write %s request: %v", command, err)
	}
}

func readDAPMessage(t *testing.T, connection net.Conn, decoder *dap.Decoder) dap.Envelope {
	t.Helper()
	if err := connection.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("set DAP read deadline: %v", err)
	}
	message, err := decoder.Decode()
	if err != nil {
		t.Fatalf("read DAP message: %v", err)
	}
	return message
}
