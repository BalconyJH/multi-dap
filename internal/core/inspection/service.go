package inspection

import (
	"context"
	"fmt"
	"math"

	"github.com/Tacrolimus/multi-dap/internal/core/handle"
)

type frameValue struct{ index uint64 }

type scopeValue struct {
	locator ScopeLocator
}

type variableValue struct {
	locator ValueLocator
}

// Stack returns frames for core in one current stopped epoch. Frame IDs are
// allocated only for this page and can never be reused after invalidation.
func (s *Service) Stack(ctx context.Context, stop StopContext, core CoreID, page Page) (Stack, error) {
	if err := validateRequest(stop, page, FormatDefault); err != nil {
		return Stack{}, err
	}
	frames, err := s.backend.Stack(ctx, core)
	if err != nil {
		return Stack{}, fmt.Errorf("inspection: stack core=%d: %w", core, err)
	}
	for _, raw := range frames {
		if err := validateRawFrame(raw); err != nil {
			return Stack{}, err
		}
	}
	start, end := pageBounds(page, len(frames))
	result := Stack{Frames: make([]Frame, 0, end-start), Total: len(frames)}
	for _, raw := range frames[start:end] {
		frame, err := s.makeFrame(stop, core, raw)
		if err != nil {
			return Stack{}, err
		}
		result.Frames = append(result.Frames, frame)
	}
	return result, nil
}

// Scopes returns lazy roots for a frame. The frame reference determines its
// core and backend index; callers cannot select another core accidentally.
func (s *Service) Scopes(ctx context.Context, stop StopContext, frameID int32) ([]Scope, error) {
	h, frame, err := s.resolve(frameID, handle.KindFrame, stop)
	if err != nil {
		return nil, err
	}
	rawScopes, err := s.backend.Scopes(ctx, h.Core, frame.index)
	if err != nil {
		return nil, fmt.Errorf("inspection: scopes core=%d frame=%d: %w", h.Core, frame.index, err)
	}
	for _, raw := range rawScopes {
		if err := validateRawScope(raw); err != nil {
			return nil, err
		}
	}
	result := make([]Scope, 0, len(rawScopes))
	for _, raw := range rawScopes {
		id, err := s.handles.Alloc(handle.KindScope, h.Core, stop.StopEpoch, scopeValue{locator: raw.Locator})
		if err != nil {
			return nil, fmt.Errorf("inspection: allocate scope: %w", err)
		}
		result = append(result, Scope{Name: raw.Name, Kind: raw.Kind, VariablesReference: id, Expensive: raw.Expensive})
	}
	return result, nil
}

// Variables resolves a scope or expandable variable reference lazily. It
// never asks the backend for an empty page and validates every backend page
// before allocating any child reference.
func (s *Service) Variables(ctx context.Context, stop StopContext, reference int32, page Page, format Format) (VariablePage, error) {
	if err := validatePage(page); err != nil {
		return VariablePage{}, err
	}
	if err := validateFormat(format); err != nil {
		return VariablePage{}, err
	}
	h, err := s.handles.Resolve(reference, stop.StopEpoch, stop.Stopped)
	if err != nil {
		return VariablePage{}, stale(reference, err)
	}
	if h.Kind != handle.KindScope && h.Kind != handle.KindVariable {
		return VariablePage{}, fmt.Errorf("%w: reference %d is not a variable root", ErrInvalidReference, reference)
	}
	if page.Count == 0 {
		return VariablePage{Total: -1}, nil
	}
	var raw RawValuePage
	switch h.Kind {
	case handle.KindScope:
		value, ok := h.Value.(scopeValue)
		if !ok {
			return VariablePage{}, fmt.Errorf("%w: scope handle payload", ErrBackendContract)
		}
		raw, err = s.backend.Variables(ctx, h.Core, value.locator, page, format)
	case handle.KindVariable:
		value, ok := h.Value.(variableValue)
		if !ok {
			return VariablePage{}, fmt.Errorf("%w: variable handle payload", ErrBackendContract)
		}
		raw, err = s.backend.Children(ctx, h.Core, value.locator, page, format)
	}
	if err != nil {
		return VariablePage{}, fmt.Errorf("inspection: variables core=%d reference=%d: %w", h.Core, reference, err)
	}
	if err := validateRawPage(raw); err != nil {
		return VariablePage{}, err
	}
	// Backends return complete snapshots, even when their native protocol has
	// no paging support. Validate the whole snapshot before applying the
	// frontend page so malformed values outside that page cannot leave partial
	// handles or a seemingly valid incomplete view behind.
	for _, value := range raw.Values {
		if err := validateRawValue(value); err != nil {
			return VariablePage{}, err
		}
		if _, err := locatorFor(h.Kind, h.Value, value.Name, value.Access); err != nil {
			return VariablePage{}, err
		}
	}
	start, end := pageBounds(page, len(raw.Values))
	result := VariablePage{Variables: make([]Variable, 0, end-start), Total: raw.Total}
	for i := start; i < end; i++ {
		value := raw.Values[i]
		locator, err := locatorFor(h.Kind, h.Value, value.Name, value.Access)
		if err != nil {
			return VariablePage{}, err
		}
		variable, err := s.makeVariable(stop, h.Core, value, locator)
		if err != nil {
			return VariablePage{}, err
		}
		result.Variables = append(result.Variables, variable)
	}
	return result, nil
}

// Evaluate evaluates a validated expression in a frame's core and selected
// backend frame. Its result can itself be traversed lazily when expandable.
func (s *Service) Evaluate(ctx context.Context, stop StopContext, frameID int32, expression Expression, format Format) (Variable, error) {
	if expression.text == "" {
		return Variable{}, fmt.Errorf("%w: expression was not constructed", ErrInvalidExpression)
	}
	if err := validateFormat(format); err != nil {
		return Variable{}, err
	}
	h, frame, err := s.resolve(frameID, handle.KindFrame, stop)
	if err != nil {
		return Variable{}, err
	}
	raw, err := s.backend.Evaluate(ctx, h.Core, frame.index, expression, format)
	if err != nil {
		return Variable{}, fmt.Errorf("inspection: evaluate core=%d frame=%d: %w", h.Core, frame.index, err)
	}
	return s.makeVariable(stop, h.Core, raw, evaluatedLocator(expression))
}

func (s *Service) makeFrame(stop StopContext, core CoreID, raw RawFrame) (Frame, error) {
	if raw.Name == "" {
		return Frame{}, fmt.Errorf("%w: frame name is empty", ErrBackendContract)
	}
	frame := Frame{Name: raw.Name, BackendIndex: raw.Index}
	if raw.HasInstructionAddress {
		frame.HasInstructionAddress = true
		frame.InstructionAddress = AddressReference{
			Address: raw.InstructionAddress, Core: core, StopEpoch: stop.StopEpoch,
		}
	}
	if raw.DebugPath != "" {
		identity, err := s.mapper.MapDebugPath(raw.DebugPath)
		if err != nil {
			return Frame{}, fmt.Errorf("inspection: map source %q: %w", raw.DebugPath, err)
		}
		frame.Source = &Source{Identity: identity, Line: raw.Line, Column: raw.Column}
	}
	id, err := s.handles.Alloc(handle.KindFrame, core, stop.StopEpoch, frameValue{index: raw.Index})
	if err != nil {
		return Frame{}, fmt.Errorf("inspection: allocate frame: %w", err)
	}
	frame.ID = id
	return frame, nil
}

func (s *Service) makeVariable(stop StopContext, core CoreID, raw RawValue, locator ValueLocator) (Variable, error) {
	if raw.Name == "" {
		return Variable{}, fmt.Errorf("%w: variable name is empty", ErrBackendContract)
	}
	if err := validateRawValue(raw); err != nil {
		return Variable{}, err
	}
	result := Variable{
		Name: raw.Name, EvaluateName: locator.Expression(), Value: raw.Value, Type: raw.Type,
		NamedChildren: raw.NamedChildren, IndexedChildren: raw.IndexedChildren,
		ChildKind: childKind(raw.Access), Availability: raw.Availability, Hidden: raw.Hidden,
	}
	if !raw.HasChildren {
		return result, nil
	}
	id, err := s.handles.Alloc(handle.KindVariable, core, stop.StopEpoch, variableValue{locator: locator})
	if err != nil {
		return Variable{}, fmt.Errorf("inspection: allocate variable: %w", err)
	}
	result.VariablesReference = id
	return result, nil
}

func childKind(access Access) ChildKind {
	if access.Kind == AccessIndex {
		return ChildIndexed
	}
	return ChildNamed
}

func (s *Service) resolve(id int32, kind handle.Kind, stop StopContext) (handle.Handle, frameValue, error) {
	h, err := s.handles.Resolve(id, stop.StopEpoch, stop.Stopped)
	if err != nil {
		return handle.Handle{}, frameValue{}, stale(id, err)
	}
	if h.Kind != kind {
		return handle.Handle{}, frameValue{}, fmt.Errorf("%w: reference %d has kind %d", ErrInvalidReference, id, h.Kind)
	}
	value, ok := h.Value.(frameValue)
	if !ok {
		return handle.Handle{}, frameValue{}, fmt.Errorf("%w: frame handle payload", ErrBackendContract)
	}
	return h, value, nil
}

func locatorFor(kind handle.Kind, payload any, name string, access Access) (ValueLocator, error) {
	switch kind {
	case handle.KindScope:
		if access.Kind != AccessRoot {
			return ValueLocator{}, fmt.Errorf("%w: scope value is not a root access", ErrBackendContract)
		}
		return rootLocator(name)
	case handle.KindVariable:
		parent, ok := payload.(variableValue)
		if !ok {
			return ValueLocator{}, fmt.Errorf("%w: variable handle payload", ErrBackendContract)
		}
		return childLocator(parent.locator, access)
	default:
		return ValueLocator{}, fmt.Errorf("%w: non-variable handle", ErrBackendContract)
	}
}

func validateRequest(stop StopContext, page Page, format Format) error {
	if err := validateStopped(stop); err != nil {
		return err
	}
	if err := validatePage(page); err != nil {
		return err
	}
	return validateFormat(format)
}

func validateStopped(stop StopContext) error {
	if !stop.Stopped {
		return fmt.Errorf("%w: target is running", ErrInvalidReference)
	}
	return nil
}

func validatePage(page Page) error {
	if page.Start < 0 || page.Count < 0 || page.Count > math.MaxInt-page.Start {
		return fmt.Errorf("%w: start=%d count=%d", ErrInvalidPage, page.Start, page.Count)
	}
	return nil
}

func validateFormat(format Format) error {
	if format > FormatBinary {
		return fmt.Errorf("%w: unsupported format %d", ErrBackendContract, format)
	}
	return nil
}

func validateRawPage(raw RawValuePage) error {
	if raw.Total < -1 {
		return fmt.Errorf("%w: invalid total %d", ErrBackendContract, raw.Total)
	}
	if raw.Total == -1 && len(raw.Values) != 0 {
		return fmt.Errorf("%w: unknown total with non-empty snapshot", ErrBackendContract)
	}
	if raw.Total >= 0 && len(raw.Values) != raw.Total {
		return fmt.Errorf("%w: snapshot contains %d values, total is %d", ErrBackendContract, len(raw.Values), raw.Total)
	}
	return nil
}

func validateRawValue(raw RawValue) error {
	if raw.Name == "" {
		return fmt.Errorf("%w: variable name is empty", ErrBackendContract)
	}
	if raw.NamedChildren < -1 || raw.IndexedChildren < -1 {
		return fmt.Errorf("%w: invalid child count", ErrBackendContract)
	}
	if raw.Availability > AvailabilityUnknown {
		return fmt.Errorf("%w: invalid availability", ErrBackendContract)
	}
	if !raw.HasChildren && (raw.NamedChildren != 0 || raw.IndexedChildren != 0) {
		return fmt.Errorf("%w: scalar child counts must be zero", ErrBackendContract)
	}
	if raw.HasChildren && raw.NamedChildren == 0 && raw.IndexedChildren == 0 {
		return fmt.Errorf("%w: expandable value has no children", ErrBackendContract)
	}
	switch raw.Access.Kind {
	case AccessRoot, AccessDereference:
		if raw.Access.Name != "" || raw.Access.Index != 0 || raw.Access.HasIndex {
			return fmt.Errorf("%w: access contains extra data", ErrBackendContract)
		}
	case AccessMember, AccessPointerMember:
		if raw.Access.Name == "" {
			return fmt.Errorf("%w: member access requires a name", ErrBackendContract)
		}
		if !raw.Access.HasIndex && raw.Access.Index != 0 {
			return fmt.Errorf("%w: member index requires HasIndex", ErrBackendContract)
		}
	case AccessIndex:
		if raw.Access.Name != "" || raw.Access.HasIndex {
			return fmt.Errorf("%w: index access contains a name", ErrBackendContract)
		}
	default:
		return fmt.Errorf("%w: unknown access kind", ErrBackendContract)
	}
	return nil
}

func validateRawFrame(raw RawFrame) error {
	if raw.Name == "" {
		return fmt.Errorf("%w: frame name is empty", ErrBackendContract)
	}
	return nil
}

func validateRawScope(raw RawScope) error {
	if raw.Name == "" || raw.Locator == "" {
		return fmt.Errorf("%w: scope requires name and locator", ErrBackendContract)
	}
	if raw.Kind > ScopeRegisters {
		return fmt.Errorf("%w: unknown scope kind %d", ErrBackendContract, raw.Kind)
	}
	return nil
}

func pageBounds(page Page, total int) (int, int) {
	if page.Start >= total {
		return total, total
	}
	end := page.Start + page.Count
	if end > total {
		end = total
	}
	return page.Start, end
}

func stale(id int32, err error) error {
	return fmt.Errorf("%w: reference %d: %w", ErrInvalidReference, id, err)
}
