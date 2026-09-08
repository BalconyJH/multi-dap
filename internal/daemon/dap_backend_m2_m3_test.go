package daemon

import (
	"context"
	"math"
	"sync"
	"testing"

	"github.com/Tacrolimus/multi-dap/internal/core/actor"
	"github.com/Tacrolimus/multi-dap/internal/core/breakpoint"
	"github.com/Tacrolimus/multi-dap/internal/core/inspection"
	"github.com/Tacrolimus/multi-dap/internal/core/source"
	"github.com/Tacrolimus/multi-dap/internal/dap"
)

func TestParallelStacksUsesStandardThreadStackTracePagingWithoutCrossCoreMixing(t *testing.T) {
	backend, core, _ := boundBackend(t)
	core.threads = []actor.Thread{{ID: 1, CoreID: 0, Name: "core0"}, {ID: 5, CoreID: 4, Name: "core4"}}
	core.stackResults = map[int]inspection.Stack{
		1: {Total: 3, Frames: []inspection.Frame{{
			ID: 101, Name: "host_top", Source: &inspection.Source{Identity: source.Identity{ClientPath: `C:\workspace\host.c`}, Line: 11},
		}}},
		5: {Total: 7, Frames: []inspection.Frame{{
			ID: 401, Name: "hsm_top", Source: &inspection.Source{Identity: source.Identity{ClientPath: `C:\workspace\hsm.c`}, Line: 29},
		}}},
	}
	attachWithInitialize(t, backend, dap.InitializeArguments{PathFormat: "path", LinesStartAt1: true, ColumnsStartAt1: true})

	threads, err := backend.Execute(context.Background(), dap.Action{Kind: dap.ActionThreads})
	if err != nil || len(threads.Threads) != 2 || threads.Threads[0] != (dap.Thread{ID: 1, Name: "core0"}) || threads.Threads[1] != (dap.Thread{ID: 5, Name: "core4"}) {
		t.Fatalf("stable configured threads = (%#v, %v)", threads.Threads, err)
	}

	type response struct {
		thread int32
		result dap.ActionResult
		err    error
	}
	requests := []dap.StackTraceArguments{{ThreadID: 1, StartFrame: 1, Levels: 2}, {ThreadID: 5, StartFrame: 3, Levels: 4}}
	responses := make(chan response, len(requests))
	var group sync.WaitGroup
	for _, arguments := range requests {
		group.Add(1)
		go func(arguments dap.StackTraceArguments) {
			defer group.Done()
			result, err := backend.Execute(context.Background(), dap.Action{Kind: dap.ActionStackTrace, StackTrace: arguments})
			responses <- response{thread: arguments.ThreadID, result: result, err: err}
		}(arguments)
	}
	group.Wait()
	close(responses)

	got := make(map[int32]dap.ActionResult, len(requests))
	for response := range responses {
		if response.err != nil {
			t.Fatalf("stackTrace thread %d: %v", response.thread, response.err)
		}
		got[response.thread] = response.result
	}
	host, hsm := got[1], got[5]
	if host.TotalFrames != 3 || len(host.StackFrames) != 1 || host.StackFrames[0].ID <= 0 || host.StackFrames[0].ID != 101 || host.StackFrames[0].Source == nil || host.StackFrames[0].Source.Path != "C:/workspace/host.c" {
		t.Fatalf("host stack = %#v", host)
	}
	if hsm.TotalFrames != 7 || len(hsm.StackFrames) != 1 || hsm.StackFrames[0].ID <= 0 || hsm.StackFrames[0].ID != 401 || hsm.StackFrames[0].Source == nil || hsm.StackFrames[0].Source.Path != "C:/workspace/hsm.c" {
		t.Fatalf("hsm stack = %#v", hsm)
	}
	if host.StackFrames[0].ID == hsm.StackFrames[0].ID {
		t.Fatalf("frame IDs are not globally distinct: host=%d hsm=%d", host.StackFrames[0].ID, hsm.StackFrames[0].ID)
	}

	core.mu.Lock()
	calls := append([]stackCall(nil), core.stackCalls...)
	core.mu.Unlock()
	pages := make(map[int]inspection.Page, len(calls))
	for _, call := range calls {
		pages[call.threadID] = call.page
	}
	if len(pages) != 2 || pages[1] != (inspection.Page{Start: 1, Count: 2}) || pages[5] != (inspection.Page{Start: 3, Count: 4}) {
		t.Fatalf("per-thread stack paging = %#v", pages)
	}
}

func attachWithInitialize(t *testing.T, backend *Backend, initialize dap.InitializeArguments) {
	t.Helper()
	if _, err := backend.Execute(context.Background(), dap.Action{Kind: dap.ActionAttach, Initialize: initialize}); err != nil {
		t.Fatal(err)
	}
}

func TestSetBreakpointsConvertsURISourceAndLineAtFrontendBoundary(t *testing.T) {
	backend, core, _ := boundBackend(t)
	initialize := dap.InitializeArguments{PathFormat: "uri", LinesStartAt1: false, ColumnsStartAt1: false}
	attachWithInitialize(t, backend, initialize)

	identity := source.Identity{ClientPath: "file:///C:/workspace/main.c", DebugPath: `C:\workspace\main.c`, Key: `c:/workspace/main.c`}
	core.breakResult = breakpoint.ReplaceResult{Breakpoints: []breakpoint.Logical{
		{DAPID: 7, Source: identity, Line: 1, Verified: true},
		{DAPID: 8, Source: identity, Line: 10, Verified: false, Message: "not present on every core"},
	}}
	result, err := backend.Execute(context.Background(), dap.Action{Kind: dap.ActionSetBreakpoints, SetBreakpoints: dap.SetBreakpointsArguments{
		Source:      dap.Source{Path: "file:///C:/workspace/main.c"},
		Breakpoints: []dap.SourceBreakpoint{{Line: 0}, {Line: 9}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	core.mu.Lock()
	gotIdentity, gotLines := core.breakIdentity, append([]int(nil), core.breakLines...)
	core.mu.Unlock()
	if gotIdentity.ClientPath != "file:///C:/workspace/main.c" || gotIdentity.DebugPath != `C:/workspace/main.c` || gotIdentity.Key != `c:/workspace/main.c` {
		t.Fatalf("identity = %#v", gotIdentity)
	}
	if len(gotLines) != 2 || gotLines[0] != 1 || gotLines[1] != 10 {
		t.Fatalf("canonical lines = %#v", gotLines)
	}
	if len(result.Breakpoints) != 2 || result.Breakpoints[0].Line != 0 || result.Breakpoints[1].Line != 9 || result.Breakpoints[0].Source == nil || result.Breakpoints[0].Source.Path != "file:///C:/workspace/main.c" {
		t.Fatalf("DAP breakpoints = %#v", result.Breakpoints)
	}
}

func TestSetBreakpointsMakesUnknownSourceAnUnverifiedResponse(t *testing.T) {
	backend, core, _ := boundBackend(t)
	attachWithInitialize(t, backend, dap.InitializeArguments{PathFormat: "path", LinesStartAt1: true, ColumnsStartAt1: true})
	core.breakErr = actor.ErrSourceUnavailable
	result, err := backend.Execute(context.Background(), dap.Action{Kind: dap.ActionSetBreakpoints, SetBreakpoints: dap.SetBreakpointsArguments{
		Source:      dap.Source{Path: `C:\workspace\not-in-any-elf.c`},
		Breakpoints: []dap.SourceBreakpoint{{Line: 4}, {Line: 9}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Breakpoints) != 2 || result.Breakpoints[0].Verified || result.Breakpoints[1].Verified || result.Breakpoints[0].Message == "" || result.Breakpoints[1].Line != 9 {
		t.Fatalf("unknown source result = %#v", result.Breakpoints)
	}
}

func TestSetBreakpointsRestoresDAPRequestOrderAndDuplicates(t *testing.T) {
	backend, core, _ := boundBackend(t)
	attachWithInitialize(t, backend, dap.InitializeArguments{PathFormat: "path", LinesStartAt1: true, ColumnsStartAt1: true})
	identity := source.Identity{ClientPath: `C:\workspace\main.c`, DebugPath: `C:\workspace\main.c`, Key: `c:/workspace/main.c`}
	// Store.Replace owns a unique sorted set. The frontend must restore DAP's
	// one-response-per-request-item order at the protocol boundary.
	core.breakResult = breakpoint.ReplaceResult{Breakpoints: []breakpoint.Logical{
		{DAPID: 7, Source: identity, Line: 4, Verified: true},
		{DAPID: 8, Source: identity, Line: 9, Verified: true},
	}}
	result, err := backend.Execute(context.Background(), dap.Action{Kind: dap.ActionSetBreakpoints, SetBreakpoints: dap.SetBreakpointsArguments{
		Source: dap.Source{Path: identity.ClientPath}, Breakpoints: []dap.SourceBreakpoint{{Line: 9}, {Line: 4}, {Line: 9}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Breakpoints) != 3 || result.Breakpoints[0].ID != 8 || result.Breakpoints[1].ID != 7 || result.Breakpoints[2].ID != 8 {
		t.Fatalf("DAP request ordering = %#v", result.Breakpoints)
	}
}

func TestM3ActionsUseStableThreadAndDAPAllPages(t *testing.T) {
	backend, core, _ := boundBackend(t)
	core.threads = core.threads[:1]
	attachWithInitialize(t, backend, dap.InitializeArguments{PathFormat: "path", LinesStartAt1: false, ColumnsStartAt1: false})
	if _, err := backend.Execute(context.Background(), dap.Action{Kind: dap.ActionConfigurationDone}); err != nil {
		t.Fatal(err)
	}
	core.stackResult = inspection.Stack{Total: 2, Frames: []inspection.Frame{{
		ID: 11, Name: "main", Source: &inspection.Source{Identity: source.Identity{ClientPath: `C:\workspace\main.c`}, Line: 42, Column: 3},
	}}}
	core.scopesResult = []inspection.Scope{{Name: "Locals", Kind: inspection.ScopeLocals, VariablesReference: 12}}
	core.varsResult = inspection.VariablePage{Variables: []inspection.Variable{{Name: "counter", EvaluateName: "counter", Value: "7", Type: "int", NamedChildren: -1, IndexedChildren: -1}}}
	core.evalResult = inspection.Variable{Name: "result", Value: "8", Type: "int", VariablesReference: 13, NamedChildren: 2, IndexedChildren: -1}

	stack, err := backend.Execute(context.Background(), dap.Action{Kind: dap.ActionStackTrace, StackTrace: dap.StackTraceArguments{ThreadID: 1, StartFrame: 0, Levels: 0}})
	if err != nil {
		t.Fatal(err)
	}
	if len(stack.StackFrames) != 1 || stack.StackFrames[0].Line != 41 || stack.StackFrames[0].Column != 2 || stack.TotalFrames != 2 {
		t.Fatalf("stack = %#v", stack)
	}
	core.mu.Lock()
	thread, stackPage := core.stackThread, core.stackPage
	core.mu.Unlock()
	if thread != 1 || stackPage.Start != 0 || stackPage.Count != math.MaxInt {
		t.Fatalf("stack request = thread %d page %#v", thread, stackPage)
	}

	scopes, err := backend.Execute(context.Background(), dap.Action{Kind: dap.ActionScopes, Scopes: dap.ScopesArguments{FrameID: 11}})
	if err != nil || len(scopes.Scopes) != 1 || scopes.Scopes[0].PresentationHint != "locals" {
		t.Fatalf("scopes = %#v, %v", scopes, err)
	}
	variables, err := backend.Execute(context.Background(), dap.Action{Kind: dap.ActionVariables, Variables: dap.VariablesArguments{VariablesReference: 12, Start: 1, Count: 0, Format: dap.ValueFormat{Hex: true}}})
	if err != nil || len(variables.Variables) != 1 || variables.Variables[0].Name != "counter" || variables.Variables[0].EvaluateName != "counter" {
		t.Fatalf("variables = %#v, %v", variables, err)
	}
	core.mu.Lock()
	varsReference, varsPage, varsFormat := core.varsReference, core.varsPage, core.varsFormat
	core.mu.Unlock()
	if varsReference != 12 || varsPage.Start != 1 || varsPage.Count != math.MaxInt-1 || varsFormat != inspection.FormatHexadecimal {
		t.Fatalf("variables request = reference %d page %#v format %d", varsReference, varsPage, varsFormat)
	}

	evaluation, err := backend.Execute(context.Background(), dap.Action{Kind: dap.ActionEvaluate, Evaluate: dap.EvaluateArguments{Expression: "counter + 1", FrameID: 11}})
	if err != nil || evaluation.Evaluation.Result != "8" || evaluation.Evaluation.VariablesReference != 13 || evaluation.Evaluation.NamedVariables != 2 {
		t.Fatalf("evaluate = %#v, %v", evaluation, err)
	}
}

func TestEvaluateWithoutFrameUsesPrimaryThreadTopFrame(t *testing.T) {
	backend, core, _ := boundBackend(t)
	core.threads = []actor.Thread{{ID: 5, CoreID: 4, Name: "core4"}, {ID: 1, CoreID: 0, Name: "core0"}}
	attachWithInitialize(t, backend, dap.InitializeArguments{PathFormat: "path", LinesStartAt1: true, ColumnsStartAt1: true})
	core.stackResult = inspection.Stack{Frames: []inspection.Frame{{ID: 41, Name: "top"}}}
	core.evalResult = inspection.Variable{Name: "state", Value: "2"}

	result, err := backend.Execute(context.Background(), dap.Action{Kind: dap.ActionEvaluate, Evaluate: dap.EvaluateArguments{Expression: "$_STATE", Context: "repl"}})
	if err != nil || result.Evaluation.Result != "2" {
		t.Fatalf("evaluate = (%#v, %v)", result, err)
	}
	core.mu.Lock()
	thread, page, frame := core.stackThread, core.stackPage, core.evalFrame
	core.mu.Unlock()
	if thread != 5 || page != (inspection.Page{Count: 1}) || frame != 41 {
		t.Fatalf("default evaluation = thread %d page %#v frame %d", thread, page, frame)
	}
}

func TestM3VariablesFilteringUsesInspectionChildKind(t *testing.T) {
	backend, _, _ := boundBackend(t)
	attachWithInitialize(t, backend, dap.InitializeArguments{PathFormat: "path", LinesStartAt1: true, ColumnsStartAt1: true})
	if _, err := backend.Execute(context.Background(), dap.Action{Kind: dap.ActionConfigurationDone}); err != nil {
		t.Fatal(err)
	}
	core := backend.core.(*fakeCore)
	core.varsResult = inspection.VariablePage{Variables: []inspection.Variable{
		{Name: "field", ChildKind: inspection.ChildNamed},
		{Name: "[0]", ChildKind: inspection.ChildIndexed},
		{Name: "other", ChildKind: inspection.ChildNamed},
		{Name: "[1]", ChildKind: inspection.ChildIndexed},
	}}
	result, err := backend.Execute(context.Background(), dap.Action{Kind: dap.ActionVariables, Variables: dap.VariablesArguments{
		VariablesReference: 1, Filter: "named", Start: 1, Count: 1,
	}})
	if err != nil || len(result.Variables) != 1 || result.Variables[0].Name != "other" {
		t.Fatalf("named filter = (%#v, %v)", result, err)
	}
	core.mu.Lock()
	page := core.varsPage
	core.mu.Unlock()
	if page != (inspection.Page{Count: math.MaxInt}) {
		t.Fatalf("filtered variables core page = %#v", page)
	}
	result, err = backend.Execute(context.Background(), dap.Action{Kind: dap.ActionVariables, Variables: dap.VariablesArguments{
		VariablesReference: 1, Filter: "indexed", Start: 1, Count: 1,
	}})
	if err != nil || len(result.Variables) != 1 || result.Variables[0].Name != "[1]" {
		t.Fatalf("indexed filter = (%#v, %v)", result, err)
	}
	core.varsResult = inspection.VariablePage{Variables: []inspection.Variable{{Name: "invalid", ChildKind: inspection.ChildKind(99)}}}
	if _, err := backend.Execute(context.Background(), dap.Action{Kind: dap.ActionVariables, Variables: dap.VariablesArguments{
		VariablesReference: 1, Filter: "named",
	}}); err == nil {
		t.Fatal("unknown child kind was accepted for a filtered request")
	}
}

func TestM3RejectsUnsupportedExpression(t *testing.T) {
	backend, _, _ := boundBackend(t)
	attachWithInitialize(t, backend, dap.InitializeArguments{PathFormat: "path", LinesStartAt1: true, ColumnsStartAt1: true})
	if _, err := backend.Execute(context.Background(), dap.Action{Kind: dap.ActionConfigurationDone}); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.Execute(context.Background(), dap.Action{Kind: dap.ActionEvaluate, Evaluate: dap.EvaluateArguments{Expression: "x; resume", FrameID: 1}}); err == nil {
		t.Fatal("unsafe expression reached the actor")
	}
}
