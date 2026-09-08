// Package dap implements the Debug Adapter Protocol frontend boundary. It is
// deliberately independent of Debugger Core, the MULTI Driver, and the bridge:
// it translates DAP envelopes and lifecycle ordering into typed backend actions.
package dap

import (
	"context"
	"encoding/json"
)

type MessageType string

const (
	TypeRequest  MessageType = "request"
	TypeResponse MessageType = "response"
	TypeEvent    MessageType = "event"
)

// Envelope is DAP's common JSON envelope. Its arguments and body intentionally
// remain raw at the codec boundary; command dispatch owns typed decoding.
type Envelope struct {
	Seq        int64           `json:"seq"`
	Type       MessageType     `json:"type"`
	Command    string          `json:"command,omitempty"`
	RequestSeq int64           `json:"request_seq,omitempty"`
	Success    *bool           `json:"success,omitempty"`
	Message    string          `json:"message,omitempty"`
	Event      string          `json:"event,omitempty"`
	Arguments  json.RawMessage `json:"arguments,omitempty"`
	Body       json.RawMessage `json:"body,omitempty"`
}

type InitializeArguments struct {
	ClientID               string `json:"clientID,omitempty"`
	ClientName             string `json:"clientName,omitempty"`
	AdapterID              string `json:"adapterID"`
	Locale                 string `json:"locale,omitempty"`
	PathFormat             string `json:"pathFormat,omitempty"`
	LinesStartAt1          bool   `json:"linesStartAt1"`
	ColumnsStartAt1        bool   `json:"columnsStartAt1"`
	SupportsVariablePaging bool   `json:"supportsVariablePaging,omitempty"`
	SupportsVariableType   bool   `json:"supportsVariableType,omitempty"`
}

type Capabilities struct {
	SupportsConfigurationDoneRequest      bool `json:"supportsConfigurationDoneRequest"`
	SupportsSingleThreadExecutionRequests bool `json:"supportsSingleThreadExecutionRequests"`
	SupportsConditionalBreakpoints        bool `json:"supportsConditionalBreakpoints"`
	SupportsHitConditionalBreakpoints     bool `json:"supportsHitConditionalBreakpoints"`
	SupportsLogPoints                     bool `json:"supportsLogPoints"`
	SupportsEvaluateForHovers             bool `json:"supportsEvaluateForHovers"`
	SupportsValueFormattingOptions        bool `json:"supportsValueFormattingOptions"`
	SupportsReadMemoryRequest             bool `json:"supportsReadMemoryRequest"`
	SupportsWriteMemoryRequest            bool `json:"supportsWriteMemoryRequest"`
	SupportsDisassembleRequest            bool `json:"supportsDisassembleRequest"`
	// SupportsDelayedStackTraceLoading permits clients such as CLion's
	// Parallel Stacks view to page each thread independently with startFrame,
	// levels, and totalFrames.
	SupportsDelayedStackTraceLoading bool `json:"supportsDelayedStackTraceLoading"`
	SupportsSteppingGranularity      bool `json:"supportsSteppingGranularity"`
}

// AttachArguments retains DAP 1.71's restart token without imposing any
// debugger-specific attach configuration on the frontend.
type AttachArguments struct {
	Restart json.RawMessage `json:"__restart,omitempty"`
}

type ContinueArguments struct {
	ThreadID     int32 `json:"threadId"`
	SingleThread bool  `json:"singleThread,omitempty"`
}

type PauseArguments struct {
	ThreadID int32 `json:"threadId"`
}

type DisconnectArguments struct {
	Restart           bool  `json:"restart,omitempty"`
	TerminateDebuggee *bool `json:"terminateDebuggee,omitempty"`
	SuspendDebuggee   *bool `json:"suspendDebuggee,omitempty"`
}

type Thread struct {
	ID   int32  `json:"id"`
	Name string `json:"name"`
}

type ThreadsBody struct {
	Threads []Thread `json:"threads"`
}

type ContinueBody struct {
	AllThreadsContinued bool `json:"allThreadsContinued"`
}

type ContinuedBody struct {
	ThreadID            int32 `json:"threadId"`
	AllThreadsContinued bool  `json:"allThreadsContinued"`
}

// OutputBody is the payload of DAP's output event. Output is required by the
// protocol; Category is optional and lets clients route console, stdout, and
// stderr text to their appropriate views.
type OutputBody struct {
	Category string `json:"category,omitempty"`
	Output   string `json:"output"`
}

type StoppedBody struct {
	Reason            string  `json:"reason"`
	ThreadID          int32   `json:"threadId,omitempty"`
	AllThreadsStopped bool    `json:"allThreadsStopped"`
	HitBreakpointIDs  []int32 `json:"hitBreakpointIds,omitempty"`
}

type TargetExecution uint8

const (
	TargetUnknown TargetExecution = iota
	TargetRunning
	TargetStopped
)

// TargetState is presentation-ready state supplied by Debugger Core. The DAP
// frontend does not infer a stop reason or assign a thread identity.
type TargetState struct {
	Execution TargetExecution
	Continued ContinuedBody
	Stopped   StoppedBody
}

// ExecutionResult carries only execution facts that DAP responses can state
// immediately. It is populated from a validated execution-domain result; a
// command acknowledgement or a thread-count heuristic must leave it zero.
type ExecutionResult struct {
	AllThreadsContinued bool
	AllThreadsStopped   bool
}

type ActionKind uint8

const (
	ActionAttach ActionKind = iota
	ActionConfigurationDone
	ActionThreads
	ActionSetBreakpoints
	ActionStackTrace
	ActionScopes
	ActionVariables
	ActionEvaluate
	ActionContinue
	ActionPause
	ActionNext
	ActionStepIn
	ActionStepOut
	ActionReadMemory
	ActionWriteMemory
	ActionDisassemble
	ActionDisconnect
)

// Action is the complete vocabulary the frontend presents to Debugger Core.
// It contains no MULTI command text and no bridge concerns.
type Action struct {
	Kind           ActionKind
	Initialize     InitializeArguments
	Attach         AttachArguments
	SetBreakpoints SetBreakpointsArguments
	StackTrace     StackTraceArguments
	Scopes         ScopesArguments
	Variables      VariablesArguments
	Evaluate       EvaluateArguments
	Continue       ContinueArguments
	Pause          PauseArguments
	Next           StepArguments
	StepIn         StepArguments
	StepOut        StepArguments
	ReadMemory     ReadMemoryArguments
	WriteMemory    WriteMemoryArguments
	Disassemble    DisassembleArguments
	Disconnect     DisconnectArguments
}

type ActionResult struct {
	Threads     []Thread
	State       TargetState
	Execution   ExecutionResult
	Breakpoints []Breakpoint
	StackFrames []StackFrame
	TotalFrames int
	Scopes      []Scope
	Variables   []Variable
	Evaluation  EvaluateBody
	ReadMemory  ReadMemoryBody
	WriteMemory WriteMemoryBody
	Disassembly []DisassembledInstruction
}

// Backend is implemented by the session-facing Debugger Core adapter. The
// frontend never reaches the MULTI Driver or bridge directly.
type Backend interface {
	Execute(context.Context, Action) (ActionResult, error)
}

// CapabilityProvider is an optional backend contract. Capabilities must
// describe operations that are wired for this concrete runtime, not merely
// request shapes understood by the DAP parser. This prevents an M1 runtime
// from advertising M3/M5 support that will fail at the actor boundary.
type CapabilityProvider interface {
	DAPCapabilities() Capabilities
}

func capabilities(backend Backend) Capabilities {
	var result Capabilities
	if provider, ok := backend.(CapabilityProvider); ok {
		result = provider.DAPCapabilities()
	}
	// These two values are lifecycle invariants of this frontend, independent
	// of optional target capabilities.
	result.SupportsConfigurationDoneRequest = true
	result.SupportsSingleThreadExecutionRequests = false
	// These request shapes are parsed for protocol compatibility, but the
	// concrete adapter cannot promise their semantics yet. In particular,
	// MULTI C expressions cannot be proven side-effect-free for hover.
	result.SupportsEvaluateForHovers = false
	result.SupportsValueFormattingOptions = false
	// Read-memory and disassembly are advertised only when the concrete
	// backend wires their actor-owned implementations. Keep write-memory
	// fail-closed: this adapter currently provides inspection, not mutation.
	result.SupportsWriteMemoryRequest = false
	result.SupportsSteppingGranularity = false
	return result
}
