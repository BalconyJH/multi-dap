package dap

import (
	"context"
	"encoding/json"
	"testing"
)

type fakeBackend struct {
	actions []Action
	result  ActionResult
	err     error
}

type capableBackend struct {
	fakeBackend
	capabilities Capabilities
}

func (b *capableBackend) DAPCapabilities() Capabilities { return b.capabilities }

func (b *fakeBackend) Execute(_ context.Context, action Action) (ActionResult, error) {
	b.actions = append(b.actions, action)
	return b.result, b.err
}

func request(seq int64, command, arguments string) Envelope {
	return Envelope{Seq: seq, Type: TypeRequest, Command: command, Arguments: json.RawMessage(arguments)}
}

func TestAttachLifecycleOrdering(t *testing.T) {
	backend := &fakeBackend{result: ActionResult{State: TargetState{
		Execution: TargetStopped,
		Stopped:   StoppedBody{Reason: "breakpoint", ThreadID: 1},
	}}}
	session := NewSession(backend)

	initialize := session.Handle(context.Background(), request(1, "initialize", `{"adapterID":"multi-dap","pathFormat":"path","linesStartAt1":true,"columnsStartAt1":true}`))
	if len(initialize) != 1 || initialize[0].Type != TypeResponse || initialize[0].Command != "initialize" || !*initialize[0].Success {
		t.Fatalf("initialize messages = %#v", initialize)
	}
	var caps Capabilities
	if err := json.Unmarshal(initialize[0].Body, &caps); err != nil {
		t.Fatal(err)
	}
	if !caps.SupportsConfigurationDoneRequest || caps.SupportsSingleThreadExecutionRequests {
		t.Fatalf("capabilities = %#v", caps)
	}
	if arguments, ok := session.InitializeArguments(); !ok || arguments.PathFormat != "path" || !arguments.LinesStartAt1 || !arguments.ColumnsStartAt1 {
		t.Fatalf("negotiated client settings = (%#v, %v)", arguments, ok)
	}

	attach := session.Handle(context.Background(), request(2, "attach", `{}`))
	if len(attach) != 1 || attach[0].Type != TypeEvent || attach[0].Event != "initialized" {
		t.Fatalf("attach messages = %#v", attach)
	}
	if got := session.PublishTargetState(TargetState{Execution: TargetRunning, Continued: ContinuedBody{ThreadID: 1}}); got != nil {
		t.Fatalf("configuration churn escaped as %#v", got)
	}

	done := session.Handle(context.Background(), request(3, "configurationDone", `{}`))
	if len(done) != 2 {
		t.Fatalf("configurationDone emitted %d messages, want 2: %#v", len(done), done)
	}
	if done[0].Type != TypeResponse || done[0].Command != "configurationDone" || done[1].Type != TypeResponse || done[1].Command != "attach" {
		t.Fatalf("wrong lifecycle ordering: %#v", done)
	}
	stopped := session.PublishTargetState(backend.result.State)
	if len(stopped) != 1 || stopped[0].Type != TypeEvent || stopped[0].Event != "stopped" {
		t.Fatalf("initial stopped event = %#v", stopped)
	}
	if len(backend.actions) != 2 || backend.actions[0].Kind != ActionAttach || backend.actions[1].Kind != ActionConfigurationDone {
		t.Fatalf("backend actions = %#v", backend.actions)
	}
}

func TestSessionRejectsOutOfOrderAndUnknownRequests(t *testing.T) {
	for _, test := range []struct {
		name    string
		prepare func(*Session)
		request Envelope
	}{
		{"attach before initialize", func(*Session) {}, request(1, "attach", `{}`)},
		{"configuration before attach", func(s *Session) { initializeSession(t, s) }, request(2, "configurationDone", `{}`)},
		{"threads before attach", func(s *Session) { initializeSession(t, s) }, request(2, "threads", `{}`)},
		{"unknown", func(s *Session) { initializeSession(t, s) }, request(2, "launch", `{}`)},
		{"initialize twice", func(s *Session) { initializeSession(t, s) }, request(2, "initialize", `{"adapterID":"multi-dap"}`)},
	} {
		t.Run(test.name, func(t *testing.T) {
			session := NewSession(&fakeBackend{})
			test.prepare(session)
			messages := session.Handle(context.Background(), test.request)
			if len(messages) != 1 || messages[0].Success == nil || *messages[0].Success {
				t.Fatalf("messages = %#v, want one failure response", messages)
			}
		})
	}
}

func TestContinuePauseThreadsAndDisconnect(t *testing.T) {
	backend := &fakeBackend{result: ActionResult{Threads: []Thread{{ID: 1, Name: "Core 0"}}}}
	session := attachedSession(t, backend)

	threads := session.Handle(context.Background(), request(4, "threads", `{}`))
	if len(threads) != 1 || !*threads[0].Success {
		t.Fatalf("threads = %#v", threads)
	}
	continued := session.Handle(context.Background(), request(5, "continue", `{"threadId":1}`))
	if len(continued) != 1 || !*continued[0].Success {
		t.Fatalf("continue = %#v", continued)
	}
	var continuedBody ContinueBody
	if err := json.Unmarshal(continued[0].Body, &continuedBody); err != nil || continuedBody.AllThreadsContinued {
		t.Fatalf("continue body = %#v, %v", continuedBody, err)
	}
	paused := session.Handle(context.Background(), request(6, "pause", `{"threadId":1}`))
	if len(paused) != 1 || !*paused[0].Success {
		t.Fatalf("pause = %#v", paused)
	}
	disconnected := session.Handle(context.Background(), request(7, "disconnect", `{}`))
	if len(disconnected) != 1 || !*disconnected[0].Success || session.Lifecycle().Phase != Disconnected {
		t.Fatalf("disconnect = %#v, lifecycle=%#v", disconnected, session.Lifecycle())
	}
	want := []ActionKind{ActionAttach, ActionConfigurationDone, ActionThreads, ActionContinue, ActionPause, ActionDisconnect}
	if len(backend.actions) != len(want) {
		t.Fatalf("actions = %#v", backend.actions)
	}
	for i, kind := range want {
		if backend.actions[i].Kind != kind {
			t.Fatalf("action %d = %v, want %v", i, backend.actions[i].Kind, kind)
		}
	}
}

func TestContinueResponseUsesOnlyBackendExecutionTruth(t *testing.T) {
	backend := &fakeBackend{result: ActionResult{
		Threads:   []Thread{{ID: 1, Name: "Core 0"}},
		Execution: ExecutionResult{AllThreadsContinued: true},
	}}
	session := attachedSession(t, backend)

	continued := session.Handle(context.Background(), request(4, "continue", `{"threadId":1}`))
	if len(continued) != 1 || continued[0].Success == nil || !*continued[0].Success {
		t.Fatalf("continue = %#v", continued)
	}
	var body ContinueBody
	if err := json.Unmarshal(continued[0].Body, &body); err != nil {
		t.Fatalf("decode continue body: %v", err)
	}
	if !body.AllThreadsContinued {
		t.Fatalf("continue body lost backend truth: %#v", body)
	}
}

func TestTargetMessagesPreserveAllThreadsFlags(t *testing.T) {
	session := NewSession(&fakeBackend{})
	running := session.targetMessages(TargetState{Execution: TargetRunning, Continued: ContinuedBody{ThreadID: 1}})
	if len(running) != 1 {
		t.Fatalf("running messages = %#v", running)
	}
	var continued ContinuedBody
	if err := json.Unmarshal(running[0].Body, &continued); err != nil || continued.AllThreadsContinued {
		t.Fatalf("continued body = %#v, %v", continued, err)
	}
	stopped := session.targetMessages(TargetState{Execution: TargetStopped, Stopped: StoppedBody{Reason: "pause"}})
	if len(stopped) != 1 {
		t.Fatalf("stopped messages = %#v", stopped)
	}
	var stoppedBody StoppedBody
	if err := json.Unmarshal(stopped[0].Body, &stoppedBody); err != nil || stoppedBody.AllThreadsStopped {
		t.Fatalf("stopped body = %#v, %v", stoppedBody, err)
	}
}

func TestReducerSuppressesConfigurationChurn(t *testing.T) {
	state := NewLifecycle()
	var err error
	state, _, err = ReduceLifecycle(state, LifecycleEvent{Kind: LifecycleInitialize, RequestSeq: 1})
	if err != nil {
		t.Fatal(err)
	}
	state, _, err = ReduceLifecycle(state, LifecycleEvent{Kind: LifecycleAttach, RequestSeq: 2})
	if err != nil {
		t.Fatal(err)
	}
	_, effects, err := ReduceLifecycle(state, LifecycleEvent{Kind: LifecycleTargetChanged})
	if err != nil || len(effects) != 0 {
		t.Fatalf("target churn = (%#v, %v), want no effects", effects, err)
	}
}

func TestTerminateEmitsEvent(t *testing.T) {
	session := NewSession(&fakeBackend{})
	if messages := session.Terminate(); messages != nil {
		t.Fatalf("Terminate() before initialize = %#v, want no protocol event", messages)
	}
	initializeSession(t, session)
	messages := session.Terminate()
	if len(messages) != 1 || messages[0].Type != TypeEvent || messages[0].Event != "terminated" {
		t.Fatalf("Terminate() = %#v", messages)
	}
}

func TestPublishOutputRequiresAttachedSessionAndNonEmptyText(t *testing.T) {
	session := NewSession(&fakeBackend{})
	if messages := session.PublishOutput(OutputBody{Category: "console", Output: "before attach"}); messages != nil {
		t.Fatalf("output before attach = %#v, want no event", messages)
	}
	session = attachedSession(t, &fakeBackend{})
	if messages := session.PublishOutput(OutputBody{Category: "console"}); messages != nil {
		t.Fatalf("empty output = %#v, want no event", messages)
	}
	messages := session.PublishOutput(OutputBody{Category: "console", Output: "SM2 \u6d4b\u8bd5\u901a\u8fc7\\n"})
	if len(messages) != 1 || messages[0].Type != TypeEvent || messages[0].Event != "output" {
		t.Fatalf("output messages = %#v", messages)
	}
	var body OutputBody
	if err := json.Unmarshal(messages[0].Body, &body); err != nil {
		t.Fatal(err)
	}
	if body.Category != "console" || body.Output != "SM2 \u6d4b\u8bd5\u901a\u8fc7\\n" {
		t.Fatalf("output body = %#v", body)
	}
}

func TestInitializeAppliesProtocolDefaults(t *testing.T) {
	session := NewSession(&fakeBackend{})
	initializeSession(t, session)
	arguments, ok := session.InitializeArguments()
	if !ok || arguments.PathFormat != "path" || !arguments.LinesStartAt1 || !arguments.ColumnsStartAt1 {
		t.Fatalf("InitializeArguments() = (%#v, %v), want DAP defaults", arguments, ok)
	}
}

func TestInitializeAdvertisesOnlyWiredBackendCapabilities(t *testing.T) {
	backend := &capableBackend{capabilities: Capabilities{
		SupportsConditionalBreakpoints:   true,
		SupportsEvaluateForHovers:        true,
		SupportsValueFormattingOptions:   true,
		SupportsReadMemoryRequest:        true,
		SupportsWriteMemoryRequest:       true,
		SupportsDisassembleRequest:       true,
		SupportsDelayedStackTraceLoading: true,
		SupportsSteppingGranularity:      true,
	}}
	session := NewSession(backend)
	messages := session.Handle(context.Background(), request(1, "initialize", `{"adapterID":"multi-dap"}`))
	if len(messages) != 1 || messages[0].Success == nil || !*messages[0].Success {
		t.Fatalf("initialize = %#v", messages)
	}
	var got Capabilities
	if err := json.Unmarshal(messages[0].Body, &got); err != nil {
		t.Fatal(err)
	}
	if !got.SupportsConfigurationDoneRequest || got.SupportsSingleThreadExecutionRequests || !got.SupportsConditionalBreakpoints || !got.SupportsDelayedStackTraceLoading {
		t.Fatalf("capabilities = %#v", got)
	}
	if got.SupportsEvaluateForHovers || got.SupportsValueFormattingOptions || !got.SupportsReadMemoryRequest || got.SupportsWriteMemoryRequest || !got.SupportsDisassembleRequest || got.SupportsSteppingGranularity {
		t.Fatalf("unsupported capabilities escaped: %#v", got)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(messages[0].Body, &wire); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"supportsVariablePaging", "supportsVariableType"} {
		if _, ok := wire[field]; ok {
			t.Fatalf("initialize response illegally includes client capability %q: %s", field, messages[0].Body)
		}
	}
}

func TestDisconnectDuringConfigurationReleasesBackend(t *testing.T) {
	backend := &fakeBackend{}
	session := NewSession(backend)
	initializeSession(t, session)
	if messages := session.Handle(context.Background(), request(2, "attach", `{}`)); len(messages) != 1 || messages[0].Event != "initialized" {
		t.Fatalf("attach messages = %#v", messages)
	}
	messages := session.Handle(context.Background(), request(3, "disconnect", `{}`))
	if len(messages) != 1 || messages[0].Success == nil || !*messages[0].Success || session.Lifecycle().Phase != Disconnected {
		t.Fatalf("disconnect messages = %#v, lifecycle=%#v", messages, session.Lifecycle())
	}
	want := []ActionKind{ActionAttach, ActionDisconnect}
	if len(backend.actions) != len(want) {
		t.Fatalf("backend actions = %#v, want %v", backend.actions, want)
	}
	for index, kind := range want {
		if backend.actions[index].Kind != kind {
			t.Fatalf("backend action %d = %v, want %v", index, backend.actions[index].Kind, kind)
		}
	}
}

func TestDisconnectRejectsUnadvertisedTargetMutationArguments(t *testing.T) {
	for _, arguments := range []string{
		`{"terminateDebuggee":true}`,
		`{"suspendDebuggee":true}`,
	} {
		backend := &fakeBackend{}
		session := attachedSession(t, backend)
		messages := session.Handle(context.Background(), request(4, "disconnect", arguments))
		if len(messages) != 1 || messages[0].Success == nil || *messages[0].Success {
			t.Fatalf("disconnect(%s) = %#v, want one failure response", arguments, messages)
		}
		if session.Lifecycle().Phase != Attached {
			t.Fatalf("disconnect(%s) changed lifecycle to %v", arguments, session.Lifecycle().Phase)
		}
	}
}

func TestDisconnectAcceptsExplicitFalseMutationFlags(t *testing.T) {
	for _, arguments := range []string{
		`{"terminateDebuggee":false}`,
		`{"suspendDebuggee":false}`,
		`{"terminateDebuggee":false,"suspendDebuggee":false}`,
	} {
		backend := &fakeBackend{}
		session := attachedSession(t, backend)
		messages := session.Handle(context.Background(), request(4, "disconnect", arguments))
		if len(messages) != 1 || messages[0].Success == nil || !*messages[0].Success {
			t.Fatalf("disconnect(%s) = %#v, want one success response", arguments, messages)
		}
		if session.Lifecycle().Phase != Disconnected {
			t.Fatalf("disconnect(%s) changed lifecycle to %v", arguments, session.Lifecycle().Phase)
		}
		if got := backend.actions[len(backend.actions)-1]; got.Kind != ActionDisconnect {
			t.Fatalf("disconnect(%s) last action = %#v", arguments, got)
		}
	}
}

func TestCLionAttachProfileLifecycle(t *testing.T) {
	backend := &fakeBackend{}
	session := NewSession(backend)
	initialize := `{"clientID":"clion","clientName":"CLion","adapterID":"clion.dap.debugger","pathFormat":"path","locale":"en","supportsVariableType":true,"supportsVariablePaging":true,"linesStartAt1":true,"columnsStartAt1":true,"supportsRunInTerminalRequest":true}`
	if messages := session.Handle(context.Background(), request(1, "initialize", initialize)); len(messages) != 1 || messages[0].Success == nil || !*messages[0].Success {
		t.Fatalf("initialize = %#v", messages)
	}
	if messages := session.Handle(context.Background(), request(2, "attach", `{"request":"attach"}`)); len(messages) != 1 || messages[0].Event != "initialized" {
		t.Fatalf("attach = %#v", messages)
	}
	if messages := session.Handle(context.Background(), request(3, "configurationDone", `{}`)); len(messages) != 2 || messages[0].Command != "configurationDone" || messages[1].Command != "attach" {
		t.Fatalf("configurationDone = %#v", messages)
	}
	if len(backend.actions) != 2 || backend.actions[0].Kind != ActionAttach || backend.actions[1].Kind != ActionConfigurationDone {
		t.Fatalf("backend actions = %#v", backend.actions)
	}
	if initialize := backend.actions[0].Initialize; initialize.ClientID != "clion" || initialize.ClientName != "CLion" || initialize.AdapterID != "clion.dap.debugger" || !initialize.SupportsVariableType || !initialize.SupportsVariablePaging {
		t.Fatalf("CLion initialize capabilities were not preserved: %#v", backend.actions[0].Initialize)
	}
	disconnect := session.Handle(context.Background(), request(4, "disconnect", `{"terminateDebuggee":false}`))
	if len(disconnect) != 1 || disconnect[0].Success == nil || !*disconnect[0].Success {
		t.Fatalf("CLion disconnect = %#v", disconnect)
	}
}

func TestCloseDetachesBackendAfterAbruptClientLoss(t *testing.T) {
	backend := &fakeBackend{}
	session := attachedSession(t, backend)
	if err := session.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := session.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []ActionKind{ActionAttach, ActionConfigurationDone, ActionDisconnect}
	if len(backend.actions) != len(want) {
		t.Fatalf("actions = %#v, want %#v", backend.actions, want)
	}
	for index, kind := range want {
		if backend.actions[index].Kind != kind {
			t.Fatalf("action %d = %v, want %v", index, backend.actions[index].Kind, kind)
		}
	}
}

func initializeSession(t *testing.T, session *Session) {
	t.Helper()
	messages := session.Handle(context.Background(), request(1, "initialize", `{"adapterID":"multi-dap"}`))
	if len(messages) != 1 || messages[0].Success == nil || !*messages[0].Success {
		t.Fatalf("initialize failed: %#v", messages)
	}
}

func attachedSession(t *testing.T, backend Backend) *Session {
	return attachedSessionWithInitialize(t, backend, `{"adapterID":"multi-dap"}`)
}

func attachedSessionWithInitialize(t *testing.T, backend Backend, initialize string) *Session {
	t.Helper()
	session := NewSession(backend)
	messages := session.Handle(context.Background(), request(1, "initialize", initialize))
	if len(messages) != 1 || messages[0].Success == nil || !*messages[0].Success {
		t.Fatalf("initialize failed: %#v", messages)
	}
	if messages := session.Handle(context.Background(), request(2, "attach", `{}`)); len(messages) != 1 || messages[0].Event != "initialized" {
		t.Fatalf("attach failed: %#v", messages)
	}
	if messages := session.Handle(context.Background(), request(3, "configurationDone", `{}`)); len(messages) < 2 || messages[1].Command != "attach" {
		t.Fatalf("configurationDone failed: %#v", messages)
	}
	return session
}
