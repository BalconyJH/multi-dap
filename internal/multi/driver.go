package multi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ErrProtocol marks a result that violates the typed bridge contract. It is
// distinct from a valid RemoteError such as MULTI refusing one command.
var ErrProtocol = errors.New("multi: bridge result violates protocol")

// Caller is the transport boundary used by Driver. bridge.Client satisfies it;
// keeping the interface here prevents MULTI semantics from leaking into the
// bridge transport package.
type Caller interface {
	Call(ctx context.Context, method string, params, result any) error
}

// Driver is the typed M1 facade over bridge.py's deliberately small method
// allowlist. It is the only Go layer that knows the bridge response shapes.
type Driver struct {
	caller Caller
}

// NewDriver constructs a typed MULTI driver over caller.
func NewDriver(caller Caller) (*Driver, error) {
	if caller == nil {
		return nil, errors.New("multi: nil bridge caller")
	}
	return &Driver{caller: caller}, nil
}

// OpenMode selects one of the two non-interchangeable session acquisition
// mechanisms. Cold is the only mode that may create a program or connect a
// target. Warm only binds a pre-existing program window in the service router
// already selected by the bridge launcher.
type OpenMode string

const (
	OpenModeCold OpenMode = "cold"
	OpenModeWarm OpenMode = "warm"
)

// ColdPreparation selects a documented preparation action after a cold
// DebugProgram/ConnectToTarget sequence. The empty value performs no action.
type ColdPreparation string

const (
	ColdPreparationNone                   ColdPreparation = ""
	ColdPreparationAlreadyPresentNoVerify ColdPreparation = "already_present_no_verify"
)

// OpenRequest identifies the typed MULTI session acquisition request. Cold
// fields are retained as direct fields because callers already construct this
// value; PrimaryELF is the sole warm-binding identity. Mode defaults to cold
// for the original API's behavior.
type OpenRequest struct {
	Mode OpenMode

	Project         string
	Connection      string
	SetupScript     string
	SetupScriptArgs string
	MultiLog        string
	Preparation     ColdPreparation

	PrimaryELF string
}

// Open either creates a bridge-owned MULTI debug window (cold) or binds the
// one exact pre-existing program window selected by primary_elf (warm).
func (d *Driver) Open(ctx context.Context, request OpenRequest) error {
	mode := request.Mode
	if mode == "" {
		mode = OpenModeCold
	}
	switch mode {
	case OpenModeCold:
		return d.openCold(ctx, request)
	case OpenModeWarm:
		return d.openWarm(ctx, request)
	default:
		return fmt.Errorf("multi: unknown open mode %q", mode)
	}
}

func (d *Driver) openCold(ctx context.Context, request OpenRequest) error {
	if request.PrimaryELF != "" {
		return errors.New("multi: cold open does not accept warm primary ELF")
	}
	if strings.TrimSpace(request.Project) == "" {
		return errors.New("multi: open project is required")
	}
	if strings.TrimSpace(request.Connection) == "" {
		return errors.New("multi: open connection is required")
	}
	switch request.Preparation {
	case ColdPreparationNone, ColdPreparationAlreadyPresentNoVerify:
	default:
		return fmt.Errorf("multi: unknown cold preparation %q", request.Preparation)
	}
	params := map[string]string{
		"mode":       string(OpenModeCold),
		"project":    request.Project,
		"connection": request.Connection,
	}
	if request.SetupScript != "" {
		params["setup_script"] = request.SetupScript
	}
	if request.SetupScriptArgs != "" {
		params["setup_script_args"] = request.SetupScriptArgs
	}
	if request.MultiLog != "" {
		params["multi_log"] = request.MultiLog
	}
	if request.Preparation != ColdPreparationNone {
		params["preparation"] = string(request.Preparation)
	}
	return d.callConfirmation(ctx, "open", params, "opened")
}

func (d *Driver) openWarm(ctx context.Context, request OpenRequest) error {
	if strings.TrimSpace(request.PrimaryELF) == "" {
		return errors.New("multi: warm open primary ELF is required")
	}
	if request.Project != "" || request.Connection != "" || request.SetupScript != "" || request.SetupScriptArgs != "" || request.MultiLog != "" || request.Preparation != ColdPreparationNone {
		return errors.New("multi: warm open does not accept cold connection fields")
	}
	return d.callConfirmation(ctx, "open", map[string]string{
		"mode":        string(OpenModeWarm),
		"primary_elf": request.PrimaryELF,
	}, "opened")
}

// Close disconnects the bridge-owned MULTI session.
func (d *Driver) Close(ctx context.Context) error {
	return d.callConfirmation(ctx, "close", map[string]string{}, "closed")
}

// State is one observation from MULTI. ProcessInfo retains all raw fields
// supplied by GetCurPrInfo while exposing M0's typed accessors.
type State struct {
	Status      Status
	ProcessInfo ProcessInfo
	RawLossy    bool
}

// State reads the current execution state and process dictionary. Unknown
// MULTI status values are rejected instead of becoming an implicit state.
func (d *Driver) State(ctx context.Context) (State, error) {
	var result map[string]json.RawMessage
	if err := d.caller.Call(ctx, "state", map[string]string{}, &result); err != nil {
		return State{}, fmt.Errorf("multi: state: %w", err)
	}
	lossy, err := requireTextFields(result, "status", "process_info")
	if err != nil {
		return State{}, protocolError("state", err)
	}

	status, err := decodeStatus(result["status"])
	if err != nil {
		return State{}, protocolError("state", err)
	}
	processInfo, err := decodeStringMap(result["process_info"])
	if err != nil {
		return State{}, protocolError("state process_info", err)
	}
	return State{Status: status, ProcessInfo: ParseProcessInfo(processInfo), RawLossy: lossy}, nil
}

// Components lists the components registered by MULTI. Component records describe
// MULTI inventory only; configured core identity is established by Topology.
func (d *Driver) Components(ctx context.Context) ([]Component, error) {
	result, err := d.callCommand(ctx, "cores", map[string]string{})
	if err != nil {
		return nil, err
	}
	lossy, err := requireTextFields(result, "components", "status")
	if err != nil {
		return nil, protocolError("cores", err)
	}
	if lossy {
		return nil, protocolError("cores", errors.New("component text is lossy"))
	}
	if err := requireCommandSuccess(result["status"]); err != nil {
		return nil, protocolError("cores", err)
	}
	components, err := decodeString(result["components"])
	if err != nil {
		return nil, protocolError("cores components", err)
	}
	parsed, err := ParseComponents(components)
	if err != nil {
		return nil, protocolError("cores text", err)
	}
	return parsed, nil
}

// Resume requests non-blocking target execution.
func (d *Driver) Resume(ctx context.Context) error {
	return d.callConfirmation(ctx, "resume", map[string]string{}, "accepted")
}

// Halt requests a non-blocking target stop.
func (d *Driver) Halt(ctx context.Context) error {
	return d.callConfirmation(ctx, "halt", map[string]string{}, "accepted")
}

// commandResult remains package-private: only structured MULTI primitives in
// this package may use bridge.py's escape hatch. Debugger Core must never build
// MULTI command strings.
type commandResult struct {
	Raw      string
	RawLossy bool
}

func (d *Driver) runCommands(ctx context.Context, commands string) (commandResult, error) {
	if strings.TrimSpace(commands) == "" {
		return commandResult{}, errors.New("multi: command text is required")
	}
	result, err := d.callCommand(ctx, "run_commands", map[string]string{"commands": commands})
	if err != nil {
		return commandResult{}, err
	}
	lossy, err := requireTextFields(result, "raw", "status")
	if err != nil {
		return commandResult{}, protocolError("run_commands", err)
	}
	if err := requireCommandSuccess(result["status"]); err != nil {
		return commandResult{}, protocolError("run_commands", err)
	}
	raw, err := decodeString(result["raw"])
	if err != nil {
		return commandResult{}, protocolError("run_commands raw", err)
	}
	return commandResult{Raw: raw, RawLossy: lossy}, nil
}

func (d *Driver) callConfirmation(ctx context.Context, method string, params any, key string) error {
	var result map[string]json.RawMessage
	if err := d.caller.Call(ctx, method, params, &result); err != nil {
		return fmt.Errorf("multi: %s: %w", method, err)
	}
	if err := requireFields(result, key); err != nil {
		return protocolError(method, err)
	}
	var confirmed bool
	if err := json.Unmarshal(result[key], &confirmed); err != nil || !confirmed {
		if err != nil {
			return protocolError(method, fmt.Errorf("%s must be true: %w", key, err))
		}
		return protocolError(method, fmt.Errorf("%s must be true", key))
	}
	return nil
}

func protocolError(operation string, err error) error {
	return fmt.Errorf("%w: %s result: %v", ErrProtocol, operation, err)
}

func (d *Driver) callCommand(ctx context.Context, method string, params any) (map[string]json.RawMessage, error) {
	var result map[string]json.RawMessage
	if err := d.caller.Call(ctx, method, params, &result); err != nil {
		return nil, fmt.Errorf("multi: %s: %w", method, err)
	}
	return result, nil
}

func requireFields(fields map[string]json.RawMessage, names ...string) error {
	if len(fields) != len(names) {
		return errors.New("contains unknown or missing fields")
	}
	for _, name := range names {
		if _, ok := fields[name]; !ok {
			return fmt.Errorf("is missing %q", name)
		}
	}
	return nil
}

// requireTextFields accepts bridge.py's optional raw_lossy marker while
// retaining exact-field validation for every typed text response.
func requireTextFields(fields map[string]json.RawMessage, names ...string) (bool, error) {
	want := len(names)
	rawLossy, hasRawLossy := fields["raw_lossy"]
	if hasRawLossy {
		want++
	}
	if len(fields) != want {
		return false, errors.New("contains unknown or missing fields")
	}
	for _, name := range names {
		if _, ok := fields[name]; !ok {
			return false, fmt.Errorf("is missing %q", name)
		}
	}
	if !hasRawLossy {
		return false, nil
	}
	var lossy bool
	if err := json.Unmarshal(rawLossy, &lossy); err != nil {
		return false, fmt.Errorf("raw_lossy must be a boolean: %w", err)
	}
	return lossy, nil
}

func decodeStatus(raw json.RawMessage) (Status, error) {
	var number json.Number
	if err := json.Unmarshal(raw, &number); err != nil {
		return 0, fmt.Errorf("status must be an integer: %w", err)
	}
	value, err := strconv.ParseInt(number.String(), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("status must be an integer: %w", err)
	}
	status := Status(value)
	if status < StatusNil || status > StatusZombie {
		return 0, fmt.Errorf("unknown status %d", value)
	}
	return status, nil
}

func decodeStringMap(raw json.RawMessage) (map[string]string, error) {
	var values map[string]json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil || values == nil {
		if err != nil {
			return nil, err
		}
		return nil, errors.New("must be an object")
	}
	decoded := make(map[string]string, len(values))
	for key, value := range values {
		stringValue, err := decodeString(value)
		if err != nil {
			return nil, fmt.Errorf("field %q must be a string: %w", key, err)
		}
		decoded[key] = stringValue
	}
	return decoded, nil
}

func decodeString(raw json.RawMessage) (string, error) {
	if len(raw) < 2 || raw[0] != '"' {
		return "", errors.New("must be a string")
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", err
	}
	return value, nil
}

func requireCommandSuccess(raw json.RawMessage) error {
	var status json.Number
	if err := json.Unmarshal(raw, &status); err != nil {
		return fmt.Errorf("status must be integer 1: %w", err)
	}
	if status.String() != "1" {
		return errors.New("status must be integer 1")
	}
	return nil
}
