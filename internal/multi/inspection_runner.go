package multi

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"

	"github.com/Tacrolimus/multi-dap/internal/bridge"
	"github.com/Tacrolimus/multi-dap/internal/core/inspection"
)

const (
	topFrameLocals        = inspection.ScopeLocator("multi:locals:top-frame")
	programCounterCommand = "print /x $pc"
)

// InspectionRunner is the production MULTI implementation of the Actor's
// inspection seam. It uses the immutable configured-core topology established
// by Actor.Open and never re-enumerates MULTI components during inspection.
//
// M0-4 only supports calls, l, and print with parser-stable output. In
// particular, selecting a non-zero frame made every local out of scope. Child
// inspection is limited to the one-level aggregate print grammar pinned by
// testdata/aggregate.txt; all other shapes fail closed.
type InspectionRunner struct {
	mu       sync.Mutex
	topology *Topology
	bound    bool
}

var _ interface {
	Stack(context.Context, *bridge.Client, inspection.CoreID) ([]inspection.RawFrame, error)
	Scopes(context.Context, *bridge.Client, inspection.CoreID, uint64) ([]inspection.RawScope, error)
	Variables(context.Context, *bridge.Client, inspection.CoreID, inspection.ScopeLocator, inspection.Page, inspection.Format) (inspection.RawValuePage, error)
	Children(context.Context, *bridge.Client, inspection.CoreID, inspection.ValueLocator, inspection.Page, inspection.Format) (inspection.RawValuePage, error)
	Evaluate(context.Context, *bridge.Client, inspection.CoreID, uint64, inspection.Expression, inspection.Format) (inspection.RawValue, error)
} = (*InspectionRunner)(nil)

// NewInspectionRunner returns a topology-bound runner. The zero-argument form
// is useful before Actor.Open; BindMULTITopology supplies the verified map.
func NewInspectionRunner(topologies ...*Topology) *InspectionRunner {
	var topology *Topology
	if len(topologies) == 1 {
		topology = topologies[0]
	}
	return &InspectionRunner{topology: topology, bound: topology != nil}
}

func (r *InspectionRunner) BindMULTITopology(topology *Topology) error {
	if r == nil {
		return errors.New("multi: nil inspection runner")
	}
	if topology == nil {
		return errors.New("multi: inspection runner requires topology")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.bound {
		return errors.New("multi: inspection runner topology is already bound")
	}
	r.topology = topology
	r.bound = true
	return nil
}

// Stack obtains the complete stack for one routed core. Paging is performed
// by inspection.Service on this complete, parser-verified snapshot.
func (r *InspectionRunner) Stack(ctx context.Context, client *bridge.Client, core inspection.CoreID) ([]inspection.RawFrame, error) {
	driver, err := r.driver(client)
	if err != nil {
		return nil, err
	}
	return r.stack(ctx, driver, core)
}

func (r *InspectionRunner) stack(ctx context.Context, driver *Driver, core inspection.CoreID) ([]inspection.RawFrame, error) {
	result, err := routedInspectionCommand(ctx, r.topology, driver, core, normalizedCallsCommand)
	if err != nil {
		return nil, fmt.Errorf("multi: routed stack: %w", err)
	}
	frames, err := ParseStack(result.Raw)
	if err != nil {
		return nil, fmt.Errorf("multi: routed stack text: %w", err)
	}
	pcResult, err := routedInspectionCommand(ctx, r.topology, driver, core, programCounterCommand)
	if err != nil {
		return nil, fmt.Errorf("multi: routed program counter: %w", err)
	}
	pc, err := ParseProgramCounter(pcResult.Raw)
	if err != nil {
		return nil, fmt.Errorf("multi: routed program counter text: %w", err)
	}
	out := make([]inspection.RawFrame, 0, len(frames))
	for _, frame := range frames {
		raw := inspection.RawFrame{
			Index: frame.Index, Name: frame.Function, DebugPath: frame.Path, Line: frame.Line, Column: frame.Column,
		}
		if frame.Selected {
			raw.HasInstructionAddress = true
			raw.InstructionAddress = pc
		}
		out = append(out, raw)
	}
	return out, nil
}

// Scopes exposes only locals on the selected top frame. M0-4 showed that e N
// followed by l is not trustworthy for N != 0, so other frame indices cannot
// be represented correctly yet.
func (r *InspectionRunner) Scopes(ctx context.Context, client *bridge.Client, core inspection.CoreID, frame uint64) ([]inspection.RawScope, error) {
	if frame != 0 {
		return nil, unsupportedInspection("non-top-frame scopes")
	}
	driver, err := r.driver(client)
	if err != nil {
		return nil, err
	}
	if _, err := r.topology.verifiedRoute(ctx, driver, int(core)); err != nil {
		return nil, err
	}
	return []inspection.RawScope{{Name: "Locals", Kind: inspection.ScopeLocals, Locator: topFrameLocals}}, nil
}

// Variables lists a complete top-frame local snapshot. Service performs the
// requested frontend pagination after this parser-verified snapshot; MULTI
// has no native page cursor to emulate.
func (r *InspectionRunner) Variables(ctx context.Context, client *bridge.Client, core inspection.CoreID, locator inspection.ScopeLocator, page inspection.Page, format inspection.Format) (inspection.RawValuePage, error) {
	if locator != topFrameLocals {
		return inspection.RawValuePage{}, unsupportedInspection("unknown scope locator")
	}
	if err := validateInspectionPage(page); err != nil {
		return inspection.RawValuePage{}, err
	}
	if err := validateInspectionFormat(format); err != nil {
		return inspection.RawValuePage{}, err
	}
	if page.Count == 0 {
		return inspection.RawValuePage{Total: -1}, nil
	}
	driver, err := r.driver(client)
	if err != nil {
		return inspection.RawValuePage{}, err
	}
	result, err := routedInspectionCommand(ctx, r.topology, driver, core, "l")
	if err != nil {
		return inspection.RawValuePage{}, fmt.Errorf("multi: routed locals: %w", err)
	}
	values, err := ParseLocals(result.Raw)
	if err != nil {
		return inspection.RawValuePage{}, fmt.Errorf("multi: routed locals text: %w", err)
	}
	out := make([]inspection.RawValue, 0, len(values))
	for _, value := range values {
		out = append(out, rawValue(value))
	}
	return inspection.RawValuePage{Values: out, Total: len(out)}, nil
}

// Children prints the opaque locator and parses only M0-4's one-level
// aggregate grammar. It returns the complete observed snapshot; Service
// applies the frontend page. The locator is validated before command assembly
// so a display name can never become injected MULTI syntax.
//
// MULTI-NOSTRUCT: MULTI 7.1.6d exposes aggregate children only as `print`
// text. Output contract pinned by golden fixture testdata/aggregate.txt.
func (r *InspectionRunner) Children(ctx context.Context, client *bridge.Client, core inspection.CoreID, locator inspection.ValueLocator, page inspection.Page, format inspection.Format) (inspection.RawValuePage, error) {
	if err := validateInspectionPage(page); err != nil {
		return inspection.RawValuePage{}, err
	}
	if err := validateInspectionFormat(format); err != nil {
		return inspection.RawValuePage{}, err
	}
	if err := validateExpression(locator.Expression()); err != nil {
		return inspection.RawValuePage{}, err
	}
	if page.Count == 0 {
		return inspection.RawValuePage{Total: -1}, nil
	}
	driver, err := r.driver(client)
	if err != nil {
		return inspection.RawValuePage{}, err
	}
	result, err := routedInspectionCommand(ctx, r.topology, driver, core, "print "+locator.Expression())
	if err != nil {
		return inspection.RawValuePage{}, fmt.Errorf("multi: routed variable children: %w", err)
	}
	aggregate, err := ParseAggregate(result.Raw)
	if err != nil {
		return inspection.RawValuePage{}, fmt.Errorf("multi: routed aggregate text: %w", err)
	}
	out := make([]inspection.RawValue, 0, len(aggregate.Children))
	for _, child := range aggregate.Children {
		out = append(out, rawAggregateChild(child))
	}
	return inspection.RawValuePage{Values: out, Total: len(out)}, nil
}

// Evaluate runs print in the selected top frame. Non-default formats are
// deliberately unavailable: M0 observed format letters but did not capture a
// stable output contract for any of them.
func (r *InspectionRunner) Evaluate(ctx context.Context, client *bridge.Client, core inspection.CoreID, frame uint64, expression inspection.Expression, format inspection.Format) (inspection.RawValue, error) {
	if frame != 0 {
		return inspection.RawValue{}, unsupportedInspection("non-top-frame evaluation")
	}
	if err := validateInspectionFormat(format); err != nil {
		return inspection.RawValue{}, err
	}
	if err := validateExpression(expression.Text()); err != nil {
		return inspection.RawValue{}, err
	}
	driver, err := r.driver(client)
	if err != nil {
		return inspection.RawValue{}, err
	}
	result, err := routedInspectionCommand(ctx, r.topology, driver, core, "print "+expression.Text())
	if err != nil {
		return inspection.RawValue{}, fmt.Errorf("multi: routed evaluate: %w", err)
	}
	value, valueErr := ParseValue(result.Raw)
	if valueErr == nil {
		return rawValue(value), nil
	}
	aggregate, aggregateErr := ParseAggregate(result.Raw)
	if aggregateErr != nil {
		return inspection.RawValue{}, fmt.Errorf("multi: routed evaluate text: %w", aggregateErr)
	}
	return inspection.RawValue{
		Name: expression.Text(), Value: aggregate.Display, Access: inspection.Access{Kind: inspection.AccessRoot},
		HasChildren: true, NamedChildren: -1, IndexedChildren: -1,
	}, nil
}

func (r *InspectionRunner) driver(client *bridge.Client) (*Driver, error) {
	if r == nil {
		return nil, errors.New("multi: inspection runner is not initialized")
	}
	r.mu.Lock()
	bound := r.bound
	r.mu.Unlock()
	if !bound {
		return nil, errors.New("multi: inspection runner is not initialized")
	}
	if client == nil {
		return nil, errors.New("multi: inspection bridge client is required")
	}
	return NewDriver(client)
}

func routedInspectionCommand(ctx context.Context, topology *Topology, driver *Driver, core inspection.CoreID, command string) (commandResult, error) {
	if core < 0 {
		return commandResult{}, errors.New("multi: inspection core must not be negative")
	}
	result, err := topology.routed(ctx, driver, int(core), command)
	if err != nil {
		return commandResult{}, err
	}
	if result.RawLossy {
		return commandResult{}, protocolError("routed inspection command", errors.New("MULTI command output is lossy"))
	}
	return result, nil
}

func validateInspectionPage(page inspection.Page) error {
	if page.Start < 0 || page.Count < 0 || page.Count > math.MaxInt-page.Start {
		return fmt.Errorf("%w: start=%d count=%d", inspection.ErrInvalidPage, page.Start, page.Count)
	}
	return nil
}

func validateInspectionFormat(format inspection.Format) error {
	if format != inspection.FormatDefault {
		return unsupportedInspection("inspection format")
	}
	return nil
}

func rawValue(value NamedValue) inspection.RawValue {
	children := childCount(value.HasChildren)
	return inspection.RawValue{
		Name: value.Name, Value: value.Display, Access: inspection.Access{Kind: inspection.AccessRoot},
		HasChildren: value.HasChildren, NamedChildren: children, IndexedChildren: children,
		Availability: inspectionAvailability(value.State), Hidden: value.Hidden,
	}
}

func rawAggregateChild(child AggregateChild) inspection.RawValue {
	return inspection.RawValue{
		Name: child.Name, Value: child.Display,
		Access:      inspection.Access{Kind: inspection.AccessMember, Name: child.Member, Index: child.Index, HasIndex: child.HasIndex},
		HasChildren: child.HasChildren, NamedChildren: childCount(child.HasChildren), IndexedChildren: childCount(child.HasChildren),
	}
}

func childCount(hasChildren bool) int {
	if hasChildren {
		return -1
	}
	return 0
}

func inspectionAvailability(state ValueState) inspection.Availability {
	switch state {
	case ValueAvailable:
		return inspection.AvailabilityAvailable
	case ValueNotInitialized:
		return inspection.AvailabilityNotInitialized
	case ValueDead:
		return inspection.AvailabilityDead
	default:
		return inspection.AvailabilityUnknown
	}
}

func unsupportedInspection(operation string) error {
	return fmt.Errorf("%w: %s", ErrUnsupported, operation)
}
