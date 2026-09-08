// Package inspection implements the frontend-neutral suspended-state model
// for stack frames, scopes, variables, and expression evaluation.
//
// It owns DAP-style reference identity, but has no DAP, bridge, process, GUI,
// or MULTI command-text dependency. A future actor calls Service while it owns
// the session serialization boundary and supplies the typed Backend below.
package inspection

import (
	"context"
	"errors"

	"github.com/Tacrolimus/multi-dap/internal/core/handle"
	"github.com/Tacrolimus/multi-dap/internal/core/source"
)

var (
	// ErrInvalidReference means a frame, scope, or variable reference is not
	// valid in the supplied suspended world. It always wraps handle.ErrStale
	// when invalidation or an epoch mismatch is the cause.
	ErrInvalidReference  = errors.New("inspection: invalid suspended-state reference")
	ErrInvalidPage       = errors.New("inspection: invalid page")
	ErrInvalidExpression = errors.New("inspection: invalid expression")
	ErrBackendContract   = errors.New("inspection: backend contract violation")
)

// CoreID identifies one target address space. It is deliberately the same
// identity carried by the shared handle store.
type CoreID = handle.CoreID

// StopContext is the session-global suspended world in which an inspection
// request executes. The actor must take it from one current session snapshot
// and serialize the backend call with state transitions. Every reference is
// resolved against both fields; CoreID is carried separately on each handle.
type StopContext struct {
	Stopped   bool
	StopEpoch uint64
}

// Page is a zero-based, bounded window. Count zero is valid. Variable
// requests treat it as an empty page without a backend call; negative values
// and an overflowing Start+Count are rejected.
type Page struct {
	Start int
	Count int
}

// Format is a presentation request. It contains no frontend vocabulary; a
// backend may map it to the debugger's native formatting primitive.
type Format uint8

const (
	FormatDefault Format = iota
	FormatHexadecimal
	FormatDecimal
	FormatBinary
)

// SourceMapper is the only source-path boundary used by inspection. MULTI's
// debug path is passed through unchanged; the mapper owns all rewriting and
// canonical comparison according to architecture.md §6.6.
type SourceMapper interface {
	MapDebugPath(path string) (source.Identity, error)
}

// Source is a source location already mapped into Debugger Core's canonical
// identity model. Line and Column intentionally retain debugger numbering;
// frontend-base conversion belongs above this package.
type Source struct {
	Identity source.Identity
	Line     int64
	Column   int64
}

// AddressReference identifies one address in a particular stopped target
// address space. Frontends may encode it however they need, but must retain
// the core and stop epoch rather than treating an address alone as stable
// identity in a multicore session.
type AddressReference struct {
	Address   uint64
	Core      CoreID
	StopEpoch uint64
}

// Frame is a suspended-state stack frame. ID is a handle ID, never a raw
// backend frame index, so a frame from another core or stop cannot resolve.
type Frame struct {
	ID                    int32
	Name                  string
	Source                *Source
	BackendIndex          uint64
	HasInstructionAddress bool
	InstructionAddress    AddressReference
}

// Stack is one lazily requested page of frames. Total is the complete stack
// depth observed for this request.
type Stack struct {
	Frames []Frame
	Total  int
}

// ScopeKind identifies a debugger scope without importing DAP presentation
// concepts. The initial M3 backend exposes locals; globals and registers are
// represented now so their later implementation does not change references.
type ScopeKind uint8

const (
	ScopeLocals ScopeKind = iota
	ScopeGlobals
	ScopeRegisters
)

// Scope is a lazy variable root associated with exactly one frame.
type Scope struct {
	Name               string
	Kind               ScopeKind
	VariablesReference int32
	Expensive          bool
}

// Availability preserves the observed liveness state without leaking MULTI's
// textual annotations into frontends.
type Availability uint8

const (
	AvailabilityAvailable Availability = iota
	AvailabilityNotInitialized
	AvailabilityDead
	AvailabilityUnknown
)

// Variable is a presentation-neutral scalar or expandable value. A zero
// VariablesReference means that the value has no known child traversal.
// Child counts use -1 when the backend cannot know them without expansion.
type Variable struct {
	Name string
	// EvaluateName is the validated expression path a frontend can use to
	// re-evaluate this value (for example, a live watch or inline hint).
	// It is derived from the opaque locator, never from display text.
	EvaluateName       string
	Value              string
	Type               string
	VariablesReference int32
	NamedChildren      int
	IndexedChildren    int
	ChildKind          ChildKind
	Availability       Availability
	Hidden             bool
}

// ChildKind classifies a value in its immediate parent container. It is
// retained from the backend access metadata so frontends can apply DAP's
// named/indexed child filter before their paging window. A scope's direct
// values and member accesses are named; only a direct index access is indexed.
type ChildKind uint8

const (
	ChildNamed ChildKind = iota
	ChildIndexed
)

// VariablePage is one lazy variables response. Total is -1 when it is not
// knowable without an eager traversal, otherwise it is the full child count.
type VariablePage struct {
	Variables []Variable
	Total     int
}

// ScopeLocator is opaque backend state selecting a root such as the locals of
// one frame. It is never exposed in a frontend response.
type ScopeLocator string

// ValueLocator is an opaque MULTI expression path. Its text is exposed only
// to Backend implementations; no frontend receives it and callers must not
// infer identity from it. Service constructs child paths with the safe helpers
// in path.go rather than by string concatenation.
type ValueLocator struct{ expression string }

// Expression returns the backend operand. It is intentionally not a command
// string and must never be concatenated into one outside the MULTI driver.
func (l ValueLocator) Expression() string { return l.expression }

// Expression is an evaluation operand supplied by a frontend. It is kept
// separate from ValueLocator because it need not be an identifier path.
type Expression struct{ text string }

// NewExpression validates the structural properties required before an
// expression can cross to a command-oriented backend. It deliberately does
// not attempt to parse C/C++: language acceptance belongs to MULTI.
func NewExpression(text string) (Expression, error) { return newExpression(text) }

// Text returns the validated source expression for a Backend implementation.
func (e Expression) Text() string { return e.text }

// AccessKind describes one safe operation used to derive a child expression
// path from its parent's opaque locator.
type AccessKind uint8

const (
	AccessRoot AccessKind = iota
	AccessMember
	AccessPointerMember
	AccessIndex
	AccessDereference
)

// Access is backend metadata, not an expression fragment. Service validates
// it before deriving a child locator, preventing a parsed display name from
// becoming injected command syntax later in the driver.
type Access struct {
	Kind     AccessKind
	Name     string
	Index    uint64
	HasIndex bool
}

// RawFrame is the backend's parsed stack representation. DebugPath is exactly
// as emitted by MULTI and is mapped only through SourceMapper.
type RawFrame struct {
	Index                 uint64
	Name                  string
	DebugPath             string
	Line                  int64
	Column                int64
	HasInstructionAddress bool
	InstructionAddress    uint64
}

// RawScope is one backend variable root for a frame.
type RawScope struct {
	Name      string
	Kind      ScopeKind
	Locator   ScopeLocator
	Expensive bool
}

// RawValue is a parsed value. AccessRoot is used for a scope's direct values;
// all child values use a non-root Access. HasChildren is explicit because the
// M0 text contract does not always reveal child counts without expanding.
type RawValue struct {
	Name            string
	Value           string
	Type            string
	Access          Access
	HasChildren     bool
	NamedChildren   int
	IndexedChildren int
	Availability    Availability
	Hidden          bool
}

// RawValuePage is a complete backend snapshot. Total is -1 only when the
// backend cannot know the size without an eager traversal; when it is known,
// it must equal len(Values). Service applies frontend paging after validating
// this snapshot, so a command-oriented backend never has to pretend it has a
// native page cursor.
type RawValuePage struct {
	Values []RawValue
	Total  int
}

// Backend is the complete M3 integration seam. Its arguments are typed
// inspection operands, never MULTI command text. Actor later supplies an
// implementation backed by internal/multi and calls it while serialized.
type Backend interface {
	Stack(context.Context, CoreID) ([]RawFrame, error)
	Scopes(context.Context, CoreID, uint64) ([]RawScope, error)
	Variables(context.Context, CoreID, ScopeLocator, Page, Format) (RawValuePage, error)
	Children(context.Context, CoreID, ValueLocator, Page, Format) (RawValuePage, error)
	Evaluate(context.Context, CoreID, uint64, Expression, Format) (RawValue, error)
}

// Service owns only the conversion from backend observations into stop-bound
// handles. It has no goroutine or mutable cache; its shared handle.Store is
// synchronized defensively and normally owned by the session actor.
type Service struct {
	backend Backend
	handles *handle.Store
	mapper  SourceMapper
}

// New constructs an M3 inspection service. All three dependencies are
// required: missing source mapping would make an accidental raw-path escape
// from the core boundary too easy.
func New(backend Backend, handles *handle.Store, mapper SourceMapper) (*Service, error) {
	if backend == nil {
		return nil, errors.New("inspection: backend is required")
	}
	if handles == nil {
		return nil, errors.New("inspection: handle store is required")
	}
	if mapper == nil {
		return nil, errors.New("inspection: source mapper is required")
	}
	return &Service{backend: backend, handles: handles, mapper: mapper}, nil
}
