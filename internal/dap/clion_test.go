package dap

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestCLion2026AttachTransportContract(t *testing.T) {
	serverSide, clientSide := net.Pipe()
	client := newClientConnection(serverSide)
	backend := &fakeBackend{result: ActionResult{Threads: []Thread{{ID: 1, Name: "Core 0"}}}}
	done := make(chan error, 1)
	go func() { done <- client.serve(context.Background(), backend) }()

	encoder := NewEncoder(clientSide)
	decoder := NewDecoder(clientSide)
	writeRequest(t, encoder, request(1, "initialize", string(readCLionFixture(t, "clion-2026.2-initialize.json"))))
	initialized := readFrame(t, decoder)
	if initialized.Type != TypeResponse || initialized.Command != "initialize" || initialized.Success == nil || !*initialized.Success {
		t.Fatalf("initialize response = %#v", initialized)
	}
	var capabilities Capabilities
	if err := json.Unmarshal(initialized.Body, &capabilities); err != nil {
		t.Fatal(err)
	}
	if !capabilities.SupportsConfigurationDoneRequest {
		t.Fatalf("capabilities = %#v", capabilities)
	}
	var capabilityWire map[string]json.RawMessage
	if err := json.Unmarshal(initialized.Body, &capabilityWire); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"exceptionBreakpointFilters", "supportsExceptionFilterOptions", "supportsExceptionOptions"} {
		if _, ok := capabilityWire[field]; ok {
			t.Fatalf("initialize response advertises unwired exception breakpoint capability %q: %s", field, initialized.Body)
		}
	}

	writeRequest(t, encoder, request(2, "attach", string(readCLionFixture(t, "clion-2026.2-attach.json"))))
	if got := readFrame(t, decoder); got.Type != TypeEvent || got.Event != "initialized" {
		t.Fatalf("attach event = %#v", got)
	}
	beforeExceptionBreakpoints := len(backend.actions)
	writeRequest(t, encoder, request(3, "setExceptionBreakpoints", `{"filters":[],"filterOptions":[],"exceptionOptions":[]}`))
	if got := readFrame(t, decoder); got.Type != TypeResponse || got.Command != "setExceptionBreakpoints" || got.RequestSeq != 3 || got.Success == nil || !*got.Success {
		t.Fatalf("setExceptionBreakpoints response = %#v", got)
	}
	if len(backend.actions) != beforeExceptionBreakpoints {
		t.Fatalf("empty exception breakpoint configuration reached backend: %#v", backend.actions[beforeExceptionBreakpoints:])
	}
	writeRequest(t, encoder, request(4, "configurationDone", `{}`))
	for _, want := range []struct {
		command    string
		requestSeq int64
	}{
		{"configurationDone", 4},
		{"attach", 2},
	} {
		got := readFrame(t, decoder)
		if got.Type != TypeResponse || got.Command != want.command || got.RequestSeq != want.requestSeq || got.Success == nil || !*got.Success {
			t.Fatalf("configuration response for %s = %#v", want.command, got)
		}
	}

	writeRequest(t, encoder, request(5, "threads", `{}`))
	if got := readFrame(t, decoder); got.Type != TypeResponse || got.Command != "threads" || got.Success == nil || !*got.Success {
		t.Fatalf("threads response = %#v", got)
	}
	for _, operation := range []struct {
		sequence  int64
		command   string
		arguments string
	}{
		{6, "next", `{"threadId":1,"granularity":"statement"}`},
		{7, "stepIn", `{"threadId":1,"granularity":"statement"}`},
		{8, "continue", `{"threadId":1}`},
		{9, "pause", `{"threadId":1}`},
	} {
		writeRequest(t, encoder, request(operation.sequence, operation.command, operation.arguments))
		if got := readFrame(t, decoder); got.Type != TypeResponse || got.Command != operation.command || got.Success == nil || !*got.Success {
			t.Fatalf("%s response = %#v", operation.command, got)
		}
	}
	writeRequest(t, encoder, request(10, "disconnect", string(readCLionFixture(t, "clion-2026.2-disconnect.json"))))
	if got := readFrame(t, decoder); got.Type != TypeResponse || got.Command != "disconnect" || got.Success == nil || !*got.Success {
		t.Fatalf("disconnect response = %#v", got)
	}

	if err := clientSide.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("serve after CLion disconnect = %v", err)
	}
	want := []ActionKind{
		ActionAttach, ActionConfigurationDone, ActionThreads,
		ActionNext, ActionStepIn, ActionContinue, ActionPause,
		ActionDisconnect,
	}
	if len(backend.actions) != len(want) {
		t.Fatalf("backend actions = %#v, want %v", backend.actions, want)
	}
	for index, kind := range want {
		if backend.actions[index].Kind != kind {
			t.Fatalf("backend action %d = %v, want %v", index, backend.actions[index].Kind, kind)
		}
	}
	if initialize := backend.actions[0].Initialize; initialize.ClientID != "clion" || initialize.ClientName != "CLion" || initialize.AdapterID != "clion.dap.debugger" || !initialize.SupportsVariablePaging || !initialize.SupportsVariableType {
		t.Fatalf("CLion initialize arguments = %#v", initialize)
	}
	for index, arguments := range []StepArguments{backend.actions[3].Next, backend.actions[4].StepIn} {
		if arguments.ThreadID != 1 || arguments.Granularity != "statement" || arguments.SingleThread {
			t.Fatalf("CLion step arguments %d = %#v", index, arguments)
		}
	}
}

func readCLionFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}
