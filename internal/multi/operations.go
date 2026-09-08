package multi

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"
)

// ErrUnsupported marks an operation deliberately unavailable on the current
// bridge contract. It is not a MULTI refusal: callers can use errors.Is to
// avoid treating it as a target or transport failure.
var ErrUnsupported = errors.New("multi: operation unsupported by verified bridge contract")

// Capability identifies a driver operation whose spelling and result shape
// need their own hardware evidence. M0 verified the non-blocking Step and
// Next Python calls; the remaining M4/M5 primitives still fail closed.
type Capability uint8

const (
	CapabilityStep Capability = iota
	CapabilityNext
	CapabilityFinish
	CapabilityRunTo
	CapabilityReadMemory
	CapabilityWriteMemory
	CapabilityRegisters
	CapabilityDisassembly
)

func (c Capability) String() string {
	switch c {
	case CapabilityStep:
		return "step"
	case CapabilityNext:
		return "next"
	case CapabilityFinish:
		return "finish"
	case CapabilityRunTo:
		return "run_to"
	case CapabilityReadMemory:
		return "read_memory"
	case CapabilityWriteMemory:
		return "write_memory"
	case CapabilityRegisters:
		return "registers"
	case CapabilityDisassembly:
		return "disassembly"
	default:
		return fmt.Sprintf("unknown_capability(%d)", c)
	}
}

// Supports reports whether this Driver can issue a capability under the
// currently verified bridge method table. It has no optimistic defaults.
func (d *Driver) Supports(capability Capability) bool {
	switch capability {
	case CapabilityStep, CapabilityNext:
		return true
	default:
		return false
	}
}

func unsupported(capability Capability) error {
	return fmt.Errorf("%w: %s", ErrUnsupported, capability)
}

// Stack returns the current stack through the documented normalized calls
// form. Its source and no-source output grammars are pinned by hardware data.
func (d *Driver) Stack(ctx context.Context) ([]StackFrame, error) {
	result, err := d.runCommands(ctx, normalizedCallsCommand)
	if err != nil {
		return nil, err
	}
	if result.RawLossy {
		return nil, protocolError("stack", errors.New("stack text is lossy"))
	}
	frames, err := ParseStack(result.Raw)
	if err != nil {
		return nil, fmt.Errorf("multi: stack text: %w", err)
	}
	return frames, nil
}

// CurrentLocals returns locals in MULTI's currently selected frame.
//
// MULTI-NOSTRUCT: M0-4 established that locals are available only through
// "l" text; its output is pinned by testdata/locals.txt. Selecting a non-zero
// frame yielded out-of-scope values in M0, so this method intentionally does
// not pretend to provide arbitrary-frame locals.
func (d *Driver) CurrentLocals(ctx context.Context) ([]NamedValue, error) {
	result, err := d.runCommands(ctx, "l")
	if err != nil {
		return nil, err
	}
	if result.RawLossy {
		return nil, protocolError("locals", errors.New("locals text is lossy"))
	}
	values, err := ParseLocals(result.Raw)
	if err != nil {
		return nil, fmt.Errorf("multi: locals text: %w", err)
	}
	return values, nil
}

// Evaluate evaluates one expression in MULTI's currently selected frame.
// The expression is deliberately not quoted: MULTI's C-expression grammar is
// the locator contract. New command separators are rejected before composing
// the single verified "print <expr>" command.
//
// MULTI-NOSTRUCT: M0-4 established that scalar evaluation is available only
// through print text; its output is pinned by testdata/value.txt.
func (d *Driver) Evaluate(ctx context.Context, expression string) (NamedValue, error) {
	if err := validateExpression(expression); err != nil {
		return NamedValue{}, err
	}
	result, err := d.runCommands(ctx, "print "+expression)
	if err != nil {
		return NamedValue{}, err
	}
	if result.RawLossy {
		return NamedValue{}, protocolError("evaluate", errors.New("value text is lossy"))
	}
	value, err := ParseValue(result.Raw)
	if err != nil {
		return NamedValue{}, fmt.Errorf("multi: evaluate text: %w", err)
	}
	return value, nil
}

func validateExpression(expression string) error {
	if strings.TrimSpace(expression) == "" {
		return errors.New("multi: expression is required")
	}
	for _, r := range expression {
		if r == 0 || r == '\r' || r == '\n' || r == ';' || unicode.IsControl(r) {
			return errors.New("multi: expression contains an unsafe command separator")
		}
	}
	return nil
}

// HaltInfo reads MULTI's observed textual halt cause. The actor must still
// corroborate it with State.ProcessInfo as required by architecture.md §6.3.
// MULTI-NOSTRUCT: M0-9 established that this information is available only
// through "H" text; its output is pinned by testdata/halt_breakpoint.txt.
func (d *Driver) HaltInfo(ctx context.Context) (HaltInfo, error) {
	result, err := d.runCommands(ctx, "H")
	if err != nil {
		return HaltInfo{}, err
	}
	if result.RawLossy {
		return HaltInfo{}, protocolError("halt info", errors.New("halt text is lossy"))
	}
	info, err := ParseHaltInfo(result.Raw)
	if err != nil {
		return HaltInfo{}, protocolError("halt info text", err)
	}
	return info, nil
}

// Processes lists MULTI process slots. They are not DAP threads.
// MULTI-NOSTRUCT: the M0 multicore capture established "P" text; its output
// is pinned by testdata/processes.txt.
func (d *Driver) Processes(ctx context.Context) ([]Process, error) {
	result, err := d.runCommands(ctx, "P")
	if err != nil {
		return nil, err
	}
	if result.RawLossy {
		return nil, protocolError("processes", errors.New("process text is lossy"))
	}
	processes, err := ParseProcesses(result.Raw)
	if err != nil {
		return nil, protocolError("processes text", err)
	}
	return processes, nil
}

// Breakpoints lists physical MULTI breakpoints. DAP identity mapping remains
// Debugger Core's responsibility.
// MULTI-NOSTRUCT: M0-5 established "B" text; its output is pinned by
// testdata/breakpoints.txt.
func (d *Driver) Breakpoints(ctx context.Context) ([]Breakpoint, error) {
	result, err := d.runCommands(ctx, "B")
	if err != nil {
		return nil, err
	}
	if result.RawLossy {
		return nil, protocolError("breakpoints", errors.New("breakpoint text is lossy"))
	}
	breakpoints, err := ParseBreakpoints(result.Raw)
	if err != nil {
		return nil, protocolError("breakpoints text", err)
	}
	return breakpoints, nil
}

// StepIn requests one non-blocking source-level step into the current call.
// M0 captured the Step(block, printOutput, stepIntoFunc) Python surface and
// verified its step result. The bridge fixes block=0, retaining wait policy in
// Go as architecture.md requires.
func (d *Driver) StepIn(ctx context.Context) error {
	return d.callConfirmation(ctx, "step_in", map[string]string{}, "accepted")
}

// Step is retained as the M1 name for StepIn until callers migrate.
func (d *Driver) Step(ctx context.Context) error { return d.StepIn(ctx) }

// Next requests one non-blocking source-level step over the current call.
func (d *Driver) Next(ctx context.Context) error {
	return d.callConfirmation(ctx, "next", map[string]string{}, "accepted")
}

// Finish and RunTo name the remainder of the M4 control surface without
// guessing a text-command spelling or extending bridge.py beyond evidence.
func (d *Driver) Finish(context.Context) error        { return unsupported(CapabilityFinish) }
func (d *Driver) RunTo(context.Context, uint64) error { return unsupported(CapabilityRunTo) }

// Register is reserved for the verified parser that will back Registers.
// M0 observed "l r", but did not capture a sanitized output contract.
type Register struct {
	Name     string
	Value    string
	Children []Register
}

// Instruction is reserved for the verified parser that will back Disassemble.
type Instruction struct {
	Address uint64
	Bytes   []byte
	Text    string
}

func (d *Driver) ReadMemory(context.Context, uint64, uint64) ([]byte, error) {
	return nil, unsupported(CapabilityReadMemory)
}

func (d *Driver) WriteMemory(context.Context, uint64, []byte) error {
	return unsupported(CapabilityWriteMemory)
}

func (d *Driver) Registers(context.Context) ([]Register, error) {
	return nil, unsupported(CapabilityRegisters)
}

func (d *Driver) Disassemble(context.Context, uint64, uint64) ([]Instruction, error) {
	return nil, unsupported(CapabilityDisassembly)
}
