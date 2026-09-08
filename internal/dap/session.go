package dap

import (
	"context"
	"encoding/json"
	"fmt"
)

// Session is an in-process DAP command dispatcher. It has no listener and no
// socket ownership; callers supply decoded envelopes and write returned ones.
type Session struct {
	backend             Backend
	backendAttached     bool
	lifecycle           Lifecycle
	nextSeq             int64
	initializeArguments InitializeArguments
	hasInitialize       bool
}

func NewSession(backend Backend) *Session {
	return &Session{backend: backend, lifecycle: NewLifecycle(), nextSeq: 1}
}

func (s *Session) Lifecycle() Lifecycle { return s.lifecycle }

// Close releases backend state acquired for this frontend after an abrupt
// transport loss. It is idempotent and emits no DAP messages. An explicit
// disconnect clears the same ownership, so the backend is called exactly
// once in the ordinary case.
func (s *Session) Close(ctx context.Context) error {
	if !s.backendAttached {
		return nil
	}
	if _, err := s.execute(ctx, Action{Kind: ActionDisconnect}); err != nil {
		return err
	}
	s.backendAttached = false
	s.lifecycle.Phase = Disconnected
	return nil
}

// InitializeArguments returns the client's negotiated path and line/column
// conventions. Debugger Core owns path canonicalization, but it needs these
// unmodified frontend conventions at that boundary.
func (s *Session) InitializeArguments() (InitializeArguments, bool) {
	return s.initializeArguments, s.hasInitialize
}

// Handle applies one client request. Protocol-level rejection is represented
// as a normal DAP failure response, so a client can recover without losing its
// transport connection.
func (s *Session) Handle(ctx context.Context, request Envelope) []Envelope {
	if request.Type != TypeRequest {
		return []Envelope{s.failure(request, "expected a request envelope")}
	}
	switch request.Command {
	case "initialize":
		return s.initialize(request)
	case "attach":
		return s.attach(ctx, request)
	case "configurationDone":
		return s.configurationDone(ctx, request)
	case "setBreakpoints":
		return s.setBreakpoints(ctx, request)
	case "setExceptionBreakpoints":
		return s.setExceptionBreakpoints(request)
	case "threads":
		return s.threads(ctx, request)
	case "stackTrace":
		return s.stackTrace(ctx, request)
	case "scopes":
		return s.scopes(ctx, request)
	case "variables":
		return s.variables(ctx, request)
	case "evaluate":
		return s.evaluate(ctx, request)
	case "continue":
		return s.continueRequest(ctx, request)
	case "pause":
		return s.pause(ctx, request)
	case "next":
		return s.step(ctx, request, ActionNext)
	case "stepIn":
		return s.step(ctx, request, ActionStepIn)
	case "stepOut":
		return s.step(ctx, request, ActionStepOut)
	case "readMemory":
		return s.readMemory(ctx, request)
	case "writeMemory":
		return s.writeMemory(ctx, request)
	case "disassemble":
		return s.disassemble(ctx, request)
	case "disconnect":
		return s.disconnect(ctx, request)
	default:
		return []Envelope{s.failure(request, "unsupported command: "+request.Command)}
	}
}

// PublishTargetState is called by the owning Debugger Core integration after a
// canonical target transition. It suppresses all configuration-window churn.
func (s *Session) PublishTargetState(state TargetState) []Envelope {
	next, effects, err := ReduceLifecycle(s.lifecycle, LifecycleEvent{Kind: LifecycleTargetChanged})
	if err != nil {
		return nil
	}
	s.lifecycle = next
	if len(effects) == 0 {
		return nil
	}
	return s.targetMessages(state)
}

// PublishOutput publishes debugger output only after the client has completed
// its attach configuration. Empty output is suppressed because it carries no
// observable console content and is not a valid DAP output event payload.
func (s *Session) PublishOutput(output OutputBody) []Envelope {
	if s.lifecycle.Phase != Attached || output.Output == "" {
		return nil
	}
	return []Envelope{s.event("output", output)}
}

// Terminate reports an unrecoverable session loss (for example bridge death)
// to the client. Disconnect is intentionally different: it detaches without
// changing target execution and therefore does not emit terminated.
func (s *Session) Terminate() []Envelope {
	if s.lifecycle.Phase == AwaitInitialize || s.lifecycle.Phase == Disconnected {
		return nil
	}
	s.lifecycle.Phase = Disconnected
	return []Envelope{s.event("terminated", nil)}
}

func (s *Session) initialize(request Envelope) []Envelope {
	arguments := InitializeArguments{
		PathFormat:      "path",
		LinesStartAt1:   true,
		ColumnsStartAt1: true,
	}
	if err := decodeArguments(request, &arguments); err != nil {
		return []Envelope{s.failure(request, err.Error())}
	}
	if arguments.AdapterID == "" {
		return []Envelope{s.failure(request, "initialize requires adapterID")}
	}
	if arguments.PathFormat != "path" && arguments.PathFormat != "uri" {
		return []Envelope{s.failure(request, "initialize pathFormat must be path or uri")}
	}
	next, effects, err := ReduceLifecycle(s.lifecycle, LifecycleEvent{Kind: LifecycleInitialize, RequestSeq: request.Seq})
	if err != nil {
		return []Envelope{s.failure(request, err.Error())}
	}
	s.lifecycle = next
	s.initializeArguments = arguments
	s.hasInitialize = true
	return s.messagesForEffects(request, effects, TargetState{})
}

func (s *Session) attach(ctx context.Context, request Envelope) []Envelope {
	var arguments AttachArguments
	if err := decodeArguments(request, &arguments); err != nil {
		return []Envelope{s.failure(request, err.Error())}
	}
	if s.lifecycle.Phase != AwaitAttach {
		return []Envelope{s.failure(request, "attach requires initialize response")}
	}
	if _, err := s.execute(ctx, Action{
		Kind: ActionAttach, Initialize: s.initializeArguments, Attach: arguments,
	}); err != nil {
		return []Envelope{s.failure(request, err.Error())}
	}
	s.backendAttached = true
	next, effects, err := ReduceLifecycle(s.lifecycle, LifecycleEvent{Kind: LifecycleAttach, RequestSeq: request.Seq})
	if err != nil {
		_, _ = s.execute(context.WithoutCancel(ctx), Action{Kind: ActionDisconnect})
		s.backendAttached = false
		return []Envelope{s.failure(request, err.Error())}
	}
	s.lifecycle = next
	return s.messagesForEffects(request, effects, TargetState{})
}

func (s *Session) configurationDone(ctx context.Context, request Envelope) []Envelope {
	if err := requireEmptyArguments(request); err != nil {
		return []Envelope{s.failure(request, err.Error())}
	}
	if s.lifecycle.Phase != Configuring {
		return []Envelope{s.failure(request, "configurationDone requires an attach configuration window")}
	}
	_, err := s.execute(ctx, Action{Kind: ActionConfigurationDone})
	if err != nil {
		return []Envelope{s.failure(request, err.Error())}
	}
	next, effects, err := ReduceLifecycle(s.lifecycle, LifecycleEvent{Kind: LifecycleConfigurationDone, RequestSeq: request.Seq})
	if err != nil {
		return []Envelope{s.failure(request, err.Error())}
	}
	s.lifecycle = next
	return s.messagesForEffects(request, effects, TargetState{})
}

func (s *Session) threads(ctx context.Context, request Envelope) []Envelope {
	if s.lifecycle.Phase != Attached {
		return []Envelope{s.failure(request, "threads requires an attached client")}
	}
	if err := requireEmptyArguments(request); err != nil {
		return []Envelope{s.failure(request, err.Error())}
	}
	result, err := s.execute(ctx, Action{Kind: ActionThreads})
	if err != nil {
		return []Envelope{s.failure(request, err.Error())}
	}
	return []Envelope{s.success(request, ThreadsBody{Threads: result.Threads})}
}

func (s *Session) continueRequest(ctx context.Context, request Envelope) []Envelope {
	if s.lifecycle.Phase != Attached {
		return []Envelope{s.failure(request, "continue requires an attached client")}
	}
	var arguments ContinueArguments
	if err := decodeArguments(request, &arguments); err != nil {
		return []Envelope{s.failure(request, err.Error())}
	}
	if arguments.SingleThread {
		return []Envelope{s.failure(request, "single-thread execution is unsupported")}
	}
	if arguments.ThreadID <= 0 {
		return []Envelope{s.failure(request, "continue requires a positive threadId")}
	}
	result, err := s.execute(ctx, Action{Kind: ActionContinue, Continue: arguments})
	if err != nil {
		return []Envelope{s.failure(request, err.Error())}
	}
	return []Envelope{s.success(request, ContinueBody{AllThreadsContinued: result.Execution.AllThreadsContinued})}
}

func (s *Session) pause(ctx context.Context, request Envelope) []Envelope {
	if s.lifecycle.Phase != Attached {
		return []Envelope{s.failure(request, "pause requires an attached client")}
	}
	var arguments PauseArguments
	if err := decodeArguments(request, &arguments); err != nil {
		return []Envelope{s.failure(request, err.Error())}
	}
	if arguments.ThreadID <= 0 {
		return []Envelope{s.failure(request, "pause requires a positive threadId")}
	}
	if _, err := s.execute(ctx, Action{Kind: ActionPause, Pause: arguments}); err != nil {
		return []Envelope{s.failure(request, err.Error())}
	}
	return []Envelope{s.success(request, nil)}
}

func (s *Session) disconnect(ctx context.Context, request Envelope) []Envelope {
	var arguments DisconnectArguments
	if err := decodeArguments(request, &arguments); err != nil {
		return []Envelope{s.failure(request, err.Error())}
	}
	if arguments.TerminateDebuggee != nil && *arguments.TerminateDebuggee {
		return []Envelope{s.failure(request, "disconnect terminateDebuggee is unsupported for an attached target")}
	}
	if arguments.SuspendDebuggee != nil && *arguments.SuspendDebuggee {
		return []Envelope{s.failure(request, "disconnect suspendDebuggee is unsupported; target execution is preserved")}
	}
	if s.lifecycle.Phase == Disconnected {
		return []Envelope{s.failure(request, "client is already disconnected")}
	}
	if s.backendAttached {
		if _, err := s.execute(ctx, Action{Kind: ActionDisconnect, Disconnect: arguments}); err != nil {
			return []Envelope{s.failure(request, err.Error())}
		}
		s.backendAttached = false
	}
	next, effects, err := ReduceLifecycle(s.lifecycle, LifecycleEvent{Kind: LifecycleDisconnect, RequestSeq: request.Seq})
	if err != nil {
		return []Envelope{s.failure(request, err.Error())}
	}
	s.lifecycle = next
	return s.messagesForEffects(request, effects, TargetState{})
}

func (s *Session) execute(ctx context.Context, action Action) (ActionResult, error) {
	if s.backend == nil {
		return ActionResult{}, fmt.Errorf("debugger backend is unavailable")
	}
	return s.backend.Execute(ctx, action)
}

func (s *Session) messagesForEffects(request Envelope, effects []LifecycleEffect, state TargetState) []Envelope {
	messages := make([]Envelope, 0, len(effects)+1)
	for _, effect := range effects {
		switch effect.Kind {
		case EffectInitializeResponse:
			messages = append(messages, s.successFor(effect.RequestSeq, "initialize", capabilities(s.backend)))
		case EffectInitialized:
			messages = append(messages, s.event("initialized", nil))
		case EffectConfigurationDoneResponse:
			messages = append(messages, s.successFor(effect.RequestSeq, "configurationDone", nil))
		case EffectAttachResponse:
			messages = append(messages, s.successFor(effect.RequestSeq, "attach", nil))
		case EffectPublishTargetState:
			messages = append(messages, s.targetMessages(state)...)
		case EffectDisconnectResponse:
			messages = append(messages, s.successFor(effect.RequestSeq, "disconnect", nil))
		}
	}
	return messages
}

func (s *Session) targetMessages(state TargetState) []Envelope {
	switch state.Execution {
	case TargetRunning:
		return []Envelope{s.event("continued", state.Continued)}
	case TargetStopped:
		return []Envelope{s.event("stopped", state.Stopped)}
	default:
		return nil
	}
}

func (s *Session) success(request Envelope, body any) Envelope {
	return s.successFor(request.Seq, request.Command, body)
}

func (s *Session) successFor(requestSeq int64, command string, body any) Envelope {
	success := true
	return Envelope{Seq: s.sequence(), Type: TypeResponse, RequestSeq: requestSeq, Command: command, Success: &success, Body: marshalBody(body)}
}

func (s *Session) failure(request Envelope, message string) Envelope {
	success := false
	return Envelope{Seq: s.sequence(), Type: TypeResponse, RequestSeq: request.Seq, Command: request.Command, Success: &success, Message: message}
}

func (s *Session) event(name string, body any) Envelope {
	return Envelope{Seq: s.sequence(), Type: TypeEvent, Event: name, Body: marshalBody(body)}
}

func (s *Session) sequence() int64 {
	seq := s.nextSeq
	s.nextSeq++
	return seq
}

func decodeArguments(request Envelope, destination any) error {
	if len(request.Arguments) == 0 || string(request.Arguments) == "null" {
		return nil
	}
	if err := json.Unmarshal(request.Arguments, destination); err != nil {
		return fmt.Errorf("invalid %s arguments: %w", request.Command, err)
	}
	return nil
}

func requireEmptyArguments(request Envelope) error {
	if len(request.Arguments) == 0 || string(request.Arguments) == "null" {
		return nil
	}
	var arguments map[string]json.RawMessage
	if err := json.Unmarshal(request.Arguments, &arguments); err == nil && len(arguments) == 0 {
		return nil
	}
	return fmt.Errorf("%s does not accept arguments", request.Command)
}

func marshalBody(value any) json.RawMessage {
	if value == nil {
		return nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(fmt.Sprintf("dap: marshal internal response body: %v", err))
	}
	return encoded
}
