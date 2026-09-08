package inspection

import (
	"context"
	"errors"
	"math"
	"reflect"
	"testing"

	"github.com/Tacrolimus/multi-dap/internal/core/handle"
	"github.com/Tacrolimus/multi-dap/internal/core/source"
)

type mapperFunc func(string) (source.Identity, error)

func (f mapperFunc) MapDebugPath(path string) (source.Identity, error) { return f(path) }

type fakeBackend struct {
	frames    map[CoreID][]RawFrame
	scopes    map[uint64][]RawScope
	values    RawValuePage
	children  RawValuePage
	evaluated RawValue
	err       error

	gotCore    CoreID
	gotFrame   uint64
	gotLocator string
	gotPage    Page
	gotFormat  Format
	gotExpr    string
	stackCalls int
	valueCalls int
	childCalls int
}

func (b *fakeBackend) Stack(_ context.Context, core CoreID) ([]RawFrame, error) {
	b.stackCalls++
	return b.frames[core], b.err
}
func (b *fakeBackend) Scopes(_ context.Context, core CoreID, frame uint64) ([]RawScope, error) {
	b.gotCore, b.gotFrame = core, frame
	return b.scopes[frame], b.err
}
func (b *fakeBackend) Variables(_ context.Context, core CoreID, _ ScopeLocator, page Page, format Format) (RawValuePage, error) {
	b.valueCalls++
	b.gotCore, b.gotPage, b.gotFormat = core, page, format
	return b.values, b.err
}
func (b *fakeBackend) Children(_ context.Context, core CoreID, locator ValueLocator, page Page, format Format) (RawValuePage, error) {
	b.childCalls++
	b.gotCore, b.gotLocator, b.gotPage, b.gotFormat = core, locator.Expression(), page, format
	return b.children, b.err
}
func (b *fakeBackend) Evaluate(_ context.Context, core CoreID, frame uint64, expression Expression, format Format) (RawValue, error) {
	b.gotCore, b.gotFrame, b.gotExpr, b.gotFormat = core, frame, expression.Text(), format
	return b.evaluated, b.err
}

func newService(t *testing.T, backend *fakeBackend) (*Service, *handle.Store) {
	t.Helper()
	store := handle.NewStore()
	service, err := New(backend, store, mapperFunc(func(path string) (source.Identity, error) {
		return source.Identity{ClientPath: "C:/workspace/" + path, DebugPath: path, Key: source.Canonicalize(path)}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	return service, store
}

func TestStackFrameIdentityAndSourceMapping(t *testing.T) {
	backend := &fakeBackend{frames: map[CoreID][]RawFrame{
		0: {{Index: 7, Name: "main()", DebugPath: "D:/build/app.c", Line: 86, Column: 2}, {Index: 8, Name: "worker()"}},
		1: {{Index: 7, Name: "main()", DebugPath: "D:/build/app.c", Line: 86, Column: 2}},
	}}
	service, _ := newService(t, backend)
	stop := StopContext{Stopped: true, StopEpoch: 17}

	first, err := service.Stack(context.Background(), stop, 0, Page{Start: 0, Count: 1})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Stack(context.Background(), stop, 1, Page{Start: 0, Count: 4})
	if err != nil {
		t.Fatal(err)
	}
	if first.Total != 2 || len(first.Frames) != 1 || first.Frames[0].Source == nil || first.Frames[0].Source.Identity.DebugPath != "D:/build/app.c" {
		t.Fatalf("first stack = %#v", first)
	}
	if second.Total != 1 || first.Frames[0].ID == second.Frames[0].ID {
		t.Fatalf("frame ids must be distinct core-scoped handles: first=%#v second=%#v", first, second)
	}
	if got, err := service.Scopes(context.Background(), stop, first.Frames[0].ID); err != nil || len(got) != 0 || backend.gotCore != 0 || backend.gotFrame != 7 {
		t.Fatalf("Scopes(first) = (%#v, %v), backend=(core=%d frame=%d)", got, err, backend.gotCore, backend.gotFrame)
	}
}

func TestStackInstructionAddressRetainsCoreAndStopEpoch(t *testing.T) {
	backend := &fakeBackend{frames: map[CoreID][]RawFrame{4: {
		{Index: 0, Name: "selected", HasInstructionAddress: true, InstructionAddress: 0},
		{Index: 1, Name: "caller"},
	}}}
	service, _ := newService(t, backend)
	stack, err := service.Stack(context.Background(), StopContext{Stopped: true, StopEpoch: 23}, 4, Page{Count: 2})
	if err != nil {
		t.Fatal(err)
	}
	if got := stack.Frames[0]; !got.HasInstructionAddress || got.InstructionAddress != (AddressReference{Address: 0, Core: 4, StopEpoch: 23}) {
		t.Fatalf("selected frame = %#v", got)
	}
	if got := stack.Frames[1]; got.HasInstructionAddress || got.InstructionAddress != (AddressReference{}) {
		t.Fatalf("unselected frame = %#v", got)
	}
}

func TestScopesVariablesChildrenAndEvaluate(t *testing.T) {
	backend := &fakeBackend{
		frames: map[CoreID][]RawFrame{2: {{Index: 3, Name: "worker()"}}},
		scopes: map[uint64][]RawScope{3: {{Name: "Locals", Kind: ScopeLocals, Locator: "locals:3"}}},
		values: RawValuePage{Total: 1, Values: []RawValue{{
			Name: "buffer", Value: "{...}", Type: "unsigned char[2]", Access: Access{Kind: AccessRoot}, HasChildren: true,
			NamedChildren: 0, IndexedChildren: 2, Availability: AvailabilityAvailable,
		}}},
		children: RawValuePage{Total: 2, Values: []RawValue{
			{Name: "[0]", Value: "42", Type: "unsigned char", Access: Access{Kind: AccessIndex, Index: 0}, NamedChildren: 0, IndexedChildren: 0, Availability: AvailabilityNotInitialized},
			{Name: "[1]", Value: "43", Type: "unsigned char", Access: Access{Kind: AccessIndex, Index: 1}, NamedChildren: 0, IndexedChildren: 0, Availability: AvailabilityAvailable},
		}},
		evaluated: RawValue{Name: "counter + 1", Value: "43", Type: "int", HasChildren: true, NamedChildren: -1, IndexedChildren: -1},
	}
	service, _ := newService(t, backend)
	stop := StopContext{Stopped: true, StopEpoch: 21}
	stack, err := service.Stack(context.Background(), stop, 2, Page{Count: 8})
	if err != nil {
		t.Fatal(err)
	}
	scopes, err := service.Scopes(context.Background(), stop, stack.Frames[0].ID)
	if err != nil || len(scopes) != 1 || scopes[0].VariablesReference == 0 {
		t.Fatalf("Scopes() = (%#v, %v)", scopes, err)
	}
	values, err := service.Variables(context.Background(), stop, scopes[0].VariablesReference, Page{Start: 0, Count: 4}, FormatHexadecimal)
	if err != nil {
		t.Fatal(err)
	}
	if backend.gotCore != 2 || backend.gotFormat != FormatHexadecimal || len(values.Variables) != 1 {
		t.Fatalf("Variables() = %#v, backend core=%d format=%d", values, backend.gotCore, backend.gotFormat)
	}
	buffer := values.Variables[0]
	if buffer.VariablesReference == 0 || buffer.IndexedChildren != 2 || buffer.ChildKind != ChildNamed || buffer.EvaluateName != "buffer" {
		t.Fatalf("buffer = %#v", buffer)
	}
	children, err := service.Variables(context.Background(), stop, buffer.VariablesReference, Page{Start: 0, Count: 1}, FormatDefault)
	if err != nil {
		t.Fatal(err)
	}
	if backend.gotLocator != "buffer" || len(children.Variables) != 1 || children.Variables[0].Availability != AvailabilityNotInitialized || children.Variables[0].ChildKind != ChildIndexed || children.Variables[0].EvaluateName != "(buffer)[0]" {
		t.Fatalf("children = %#v, locator=%q", children, backend.gotLocator)
	}
	expression, err := NewExpression("counter + 1")
	if err != nil {
		t.Fatal(err)
	}
	evaluated, err := service.Evaluate(context.Background(), stop, stack.Frames[0].ID, expression, FormatDecimal)
	if err != nil {
		t.Fatal(err)
	}
	if backend.gotCore != 2 || backend.gotFrame != 3 || backend.gotExpr != "counter + 1" || evaluated.VariablesReference == 0 || evaluated.NamedChildren != -1 {
		t.Fatalf("Evaluate() = %#v, backend=(core=%d frame=%d expr=%q)", evaluated, backend.gotCore, backend.gotFrame, backend.gotExpr)
	}
}

func TestReferencesAreInvalidatedByStateAndEpoch(t *testing.T) {
	backend := &fakeBackend{frames: map[CoreID][]RawFrame{0: {{Index: 0, Name: "main"}}}}
	service, store := newService(t, backend)
	stop := StopContext{Stopped: true, StopEpoch: 2}
	stack, err := service.Stack(context.Background(), stop, 0, Page{Count: 1})
	if err != nil {
		t.Fatal(err)
	}
	for _, current := range []StopContext{{Stopped: false, StopEpoch: 2}, {Stopped: true, StopEpoch: 3}} {
		if _, err := service.Scopes(context.Background(), current, stack.Frames[0].ID); !errors.Is(err, ErrInvalidReference) || !errors.Is(err, handle.ErrStale) {
			t.Fatalf("Scopes(%#v) error = %v, want invalid stale reference", current, err)
		}
	}
	store.DropStopBound()
	if _, err := service.Scopes(context.Background(), stop, stack.Frames[0].ID); !errors.Is(err, handle.ErrStale) {
		t.Fatalf("Scopes after DropStopBound error = %v, want stale", err)
	}
}

func TestPaginationValidationAndBackendContracts(t *testing.T) {
	tests := []struct {
		name    string
		page    Page
		raw     RawValuePage
		wantErr error
	}{
		{name: "negative start", page: Page{Start: -1, Count: 1}, wantErr: ErrInvalidPage},
		{name: "negative count", page: Page{Count: -1}, wantErr: ErrInvalidPage},
		{name: "overflow", page: Page{Start: math.MaxInt, Count: 1}, wantErr: ErrInvalidPage},
		{name: "incomplete snapshot", page: Page{Count: 1}, raw: RawValuePage{Total: 2, Values: []RawValue{{Name: "a", Access: Access{Kind: AccessRoot}}}}, wantErr: ErrBackendContract},
		{name: "inconsistent empty snapshot", page: Page{Start: 3, Count: 1}, raw: RawValuePage{Total: 2}, wantErr: ErrBackendContract},
		{name: "unknown nonempty snapshot", page: Page{Count: 1}, raw: RawValuePage{Total: -1, Values: []RawValue{{Name: "a", Access: Access{Kind: AccessRoot}}}}, wantErr: ErrBackendContract},
		{name: "scalar unknown child count", page: Page{Count: 1}, raw: RawValuePage{Total: 1, Values: []RawValue{{Name: "a", Access: Access{Kind: AccessRoot}, NamedChildren: -1, IndexedChildren: 0}}}, wantErr: ErrBackendContract},
		{name: "expandable empty child count", page: Page{Count: 1}, raw: RawValuePage{Total: 1, Values: []RawValue{{Name: "a", Access: Access{Kind: AccessRoot}, HasChildren: true}}}, wantErr: ErrBackendContract},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			backend := &fakeBackend{frames: map[CoreID][]RawFrame{0: {{Index: 0, Name: "main"}}}, scopes: map[uint64][]RawScope{0: {{Name: "Locals", Locator: "locals"}}}, values: test.raw}
			service, _ := newService(t, backend)
			stop := StopContext{Stopped: true, StopEpoch: 1}
			stack, err := service.Stack(context.Background(), stop, 0, Page{Count: 1})
			if err != nil {
				t.Fatal(err)
			}
			scopes, err := service.Scopes(context.Background(), stop, stack.Frames[0].ID)
			if err != nil {
				t.Fatal(err)
			}
			_, err = service.Variables(context.Background(), stop, scopes[0].VariablesReference, test.page, FormatDefault)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("Variables(%#v) error = %v, want %v", test.page, err, test.wantErr)
			}
		})
	}
}

func TestVariablesValidateWholeSnapshotBeforePagingOrHandleAllocation(t *testing.T) {
	backend := &fakeBackend{
		frames: map[CoreID][]RawFrame{0: {{Index: 0, Name: "main"}}},
		scopes: map[uint64][]RawScope{0: {{Name: "Locals", Locator: "locals"}}},
		values: RawValuePage{Total: 2, Values: []RawValue{
			{Name: "first", Access: Access{Kind: AccessRoot}, HasChildren: true, NamedChildren: 1},
			{Name: "later", Access: Access{Kind: AccessMember, Name: "bad; resume"}},
		}},
	}
	service, store := newService(t, backend)
	stop := StopContext{Stopped: true, StopEpoch: 1}
	stack, err := service.Stack(context.Background(), stop, 0, Page{Count: 1})
	if err != nil {
		t.Fatal(err)
	}
	scopes, err := service.Scopes(context.Background(), stop, stack.Frames[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Variables(context.Background(), stop, scopes[0].VariablesReference, Page{Count: 1}, FormatDefault); !errors.Is(err, ErrBackendContract) {
		t.Fatalf("Variables() error = %v, want backend contract", err)
	}
	if _, err := store.Resolve(3, stop.StopEpoch, stop.Stopped); !errors.Is(err, handle.ErrStale) {
		t.Fatalf("invalid snapshot allocated a variable handle: %v", err)
	}

	backend.values = RawValuePage{Total: 1, Values: []RawValue{{Name: "first", Access: Access{Kind: AccessRoot}, HasChildren: true, NamedChildren: 1}}}
	page, err := service.Variables(context.Background(), stop, scopes[0].VariablesReference, Page{Count: 1}, FormatDefault)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Variables) != 1 || page.Variables[0].VariablesReference != 3 {
		t.Fatalf("valid retry = %#v, want first variable handle 3", page)
	}
}

func TestStackAndScopesValidateWholeSnapshotBeforeAllocatingReferences(t *testing.T) {
	backend := &fakeBackend{
		frames: map[CoreID][]RawFrame{0: {
			{Index: 0, Name: "main"},
			{Index: 1, Name: ""},
		}},
	}
	service, store := newService(t, backend)
	stop := StopContext{Stopped: true, StopEpoch: 1}
	if _, err := service.Stack(context.Background(), stop, 0, Page{Count: 1}); !errors.Is(err, ErrBackendContract) {
		t.Fatalf("Stack() error = %v, want backend contract", err)
	}
	if _, err := store.Resolve(1, stop.StopEpoch, stop.Stopped); !errors.Is(err, handle.ErrStale) {
		t.Fatalf("invalid stack allocated a frame handle: %v", err)
	}

	backend.frames[0] = []RawFrame{{Index: 0, Name: "main"}}
	backend.scopes = map[uint64][]RawScope{0: {
		{Name: "Locals", Locator: "locals"},
		{Name: "Broken", Locator: ""},
	}}
	stack, err := service.Stack(context.Background(), stop, 0, Page{Count: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Scopes(context.Background(), stop, stack.Frames[0].ID); !errors.Is(err, ErrBackendContract) {
		t.Fatalf("Scopes() error = %v, want backend contract", err)
	}
	if _, err := store.Resolve(2, stop.StopEpoch, stop.Stopped); !errors.Is(err, handle.ErrStale) {
		t.Fatalf("invalid scopes allocated a scope handle: %v", err)
	}
}

func TestVariablesPagesCompleteBackendSnapshot(t *testing.T) {
	backend := &fakeBackend{
		frames: map[CoreID][]RawFrame{0: {{Index: 0, Name: "main"}}},
		scopes: map[uint64][]RawScope{0: {{Name: "Locals", Locator: "locals"}}},
		values: RawValuePage{Total: 3, Values: []RawValue{
			{Name: "first", Access: Access{Kind: AccessRoot}},
			{Name: "second", Access: Access{Kind: AccessRoot}},
			{Name: "third", Access: Access{Kind: AccessRoot}},
		}},
	}
	service, _ := newService(t, backend)
	stop := StopContext{Stopped: true, StopEpoch: 1}
	stack, err := service.Stack(context.Background(), stop, 0, Page{Count: 1})
	if err != nil {
		t.Fatal(err)
	}
	scopes, err := service.Scopes(context.Background(), stop, stack.Frames[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	page, err := service.Variables(context.Background(), stop, scopes[0].VariablesReference, Page{Start: 1, Count: 1}, FormatDefault)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 3 || len(page.Variables) != 1 || page.Variables[0].Name != "second" || backend.gotPage != (Page{Start: 1, Count: 1}) {
		t.Fatalf("Variables() = %#v, backend page=%#v", page, backend.gotPage)
	}
}

func TestZeroVariablePageIsLazyButStillValidatesReference(t *testing.T) {
	backend := &fakeBackend{frames: map[CoreID][]RawFrame{0: {{Index: 0, Name: "main"}}}, scopes: map[uint64][]RawScope{0: {{Name: "Locals", Locator: "locals"}}}}
	service, store := newService(t, backend)
	stop := StopContext{Stopped: true, StopEpoch: 1}
	stack, err := service.Stack(context.Background(), stop, 0, Page{Count: 1})
	if err != nil {
		t.Fatal(err)
	}
	scopes, err := service.Scopes(context.Background(), stop, stack.Frames[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	page, err := service.Variables(context.Background(), stop, scopes[0].VariablesReference, Page{}, FormatDefault)
	if err != nil || page.Total != -1 || len(page.Variables) != 0 || backend.valueCalls != 0 {
		t.Fatalf("zero Variables() = (%#v, %v), calls=%d", page, err, backend.valueCalls)
	}
	if _, err := service.Variables(context.Background(), stop, stack.Frames[0].ID, Page{}, FormatDefault); !errors.Is(err, ErrInvalidReference) {
		t.Fatalf("zero Variables() frame reference error = %v, want invalid reference", err)
	}
	store.DropStopBound()
	if _, err := service.Variables(context.Background(), stop, scopes[0].VariablesReference, Page{}, FormatDefault); !errors.Is(err, handle.ErrStale) {
		t.Fatalf("zero Variables() stale reference error = %v", err)
	}
}

func TestBackendErrorsRemainInspectable(t *testing.T) {
	want := errors.New("backend unavailable")
	backend := &fakeBackend{frames: map[CoreID][]RawFrame{0: {{Index: 0, Name: "main"}}}, err: want}
	service, _ := newService(t, backend)
	_, err := service.Stack(context.Background(), StopContext{Stopped: true, StopEpoch: 1}, 0, Page{Count: 1})
	if !errors.Is(err, want) {
		t.Fatalf("Stack() error = %v, want wrapping %v", err, want)
	}
}

func TestExpressionAndLocatorConstructionRejectUnsafeSyntax(t *testing.T) {
	for _, text := range []string{"", "value\nnext", "value\n", "\rvalue", "value; next", "\x00", "value\vnext", "value\u0085next"} {
		if _, err := NewExpression(text); !errors.Is(err, ErrInvalidExpression) {
			t.Fatalf("NewExpression(%q) error = %v", text, err)
		}
	}
	if expression, err := NewExpression("  counter + 1  "); err != nil || expression.Text() != "counter + 1" {
		t.Fatalf("NewExpression(trimmed) = (%q, %v)", expression.Text(), err)
	}
	root, err := rootLocator("ptr")
	if err != nil {
		t.Fatal(err)
	}
	arrayRoot, err := rootLocator("buffer[0002]")
	if err != nil || arrayRoot.Expression() != "buffer[0002]" {
		t.Fatalf("rootLocator(root array) = (%q, %v)", arrayRoot.Expression(), err)
	}
	arrayChild, err := childLocator(arrayRoot, Access{Kind: AccessIndex, Index: 1})
	if err != nil || arrayChild.Expression() != "(buffer[0002])[1]" {
		t.Fatalf("childLocator(root array) = (%q, %v)", arrayChild.Expression(), err)
	}
	for _, name := range []string{
		"buffer[]", "buffer[-1]", "buffer[+1]", "buffer[0x1]", "buffer[1.0]",
		"buffer[1][2]", "buffer[18446744073709551616]", "buffer[\u0661]", "buffer[1]; resume",
	} {
		if _, err := rootLocator(name); !errors.Is(err, ErrBackendContract) {
			t.Fatalf("unsafe root %q error = %v", name, err)
		}
	}
	for _, test := range []struct {
		access Access
		want   string
	}{
		{Access{Kind: AccessMember, Name: "field"}, "(ptr).field"},
		{Access{Kind: AccessMember, Name: "field", Index: 0, HasIndex: true}, "(ptr).field[0]"},
		{Access{Kind: AccessPointerMember, Name: "field"}, "(ptr)->field"},
		{Access{Kind: AccessIndex, Index: 9}, "(ptr)[9]"},
		{Access{Kind: AccessDereference}, "*(ptr)"},
	} {
		got, err := childLocator(root, test.access)
		if err != nil || got.Expression() != test.want {
			t.Fatalf("childLocator(%#v) = (%q, %v), want %q", test.access, got.Expression(), err, test.want)
		}
	}
	for _, name := range []string{"x); resume", "field\nnext", "field\u03bb"} {
		if _, err := childLocator(root, Access{Kind: AccessMember, Name: name}); !errors.Is(err, ErrBackendContract) {
			t.Fatalf("unsafe member %q error = %v", name, err)
		}
	}
	for _, access := range []Access{
		{Kind: AccessMember, Name: "field", Index: 1},
		{Kind: AccessPointerMember, Name: "field", Index: 1},
		{Kind: AccessIndex, HasIndex: true},
		{Kind: AccessDereference, HasIndex: true},
	} {
		if _, err := childLocator(root, access); !errors.Is(err, ErrBackendContract) {
			t.Fatalf("inconsistent access %#v error = %v", access, err)
		}
	}
}

func TestNewRequiresAllBoundaries(t *testing.T) {
	backend := &fakeBackend{}
	store := handle.NewStore()
	for _, test := range []struct {
		backend Backend
		handles *handle.Store
		mapper  SourceMapper
	}{
		{nil, store, mapperFunc(func(string) (source.Identity, error) { return source.Identity{}, nil })},
		{backend, nil, mapperFunc(func(string) (source.Identity, error) { return source.Identity{}, nil })},
		{backend, store, nil},
	} {
		if got, err := New(test.backend, test.handles, test.mapper); got != nil || err == nil {
			t.Fatalf("New(%#v) = (%#v, %v), want error", test, got, err)
		}
	}
}

func TestRawValueContractDoesNotMutateBackendValues(t *testing.T) {
	raw := RawValue{Name: "x", Access: Access{Kind: AccessRoot}, NamedChildren: 0, IndexedChildren: 0}
	backend := &fakeBackend{frames: map[CoreID][]RawFrame{0: {{Index: 0, Name: "main"}}}, scopes: map[uint64][]RawScope{0: {{Name: "Locals", Locator: "locals"}}}, values: RawValuePage{Total: 1, Values: []RawValue{raw}}}
	service, _ := newService(t, backend)
	stop := StopContext{Stopped: true, StopEpoch: 1}
	stack, _ := service.Stack(context.Background(), stop, 0, Page{Count: 1})
	scopes, _ := service.Scopes(context.Background(), stop, stack.Frames[0].ID)
	if _, err := service.Variables(context.Background(), stop, scopes[0].VariablesReference, Page{Count: 1}, FormatDefault); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(raw, backend.values.Values[0]) {
		t.Fatalf("service mutated backend value: got %#v, want %#v", backend.values.Values[0], raw)
	}
}
