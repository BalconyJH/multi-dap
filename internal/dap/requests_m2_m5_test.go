package dap

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

type actionBackend struct {
	actions []Action
	call    func(Action) (ActionResult, error)
}

func (b *actionBackend) Execute(_ context.Context, action Action) (ActionResult, error) {
	b.actions = append(b.actions, action)
	if b.call == nil {
		return ActionResult{}, nil
	}
	return b.call(action)
}

func TestSetBreakpointsIsAvailableDuringConfiguration(t *testing.T) {
	backend := &actionBackend{call: func(action Action) (ActionResult, error) {
		if action.Kind != ActionSetBreakpoints {
			return ActionResult{}, nil
		}
		return ActionResult{Breakpoints: []Breakpoint{
			{ID: 7, Verified: true, Line: 10},
			{ID: 8, Verified: false, Line: 20, Message: "not present on every core"},
		}}, nil
	}}
	session := NewSession(backend)
	initializeSession(t, session)
	if got := session.Handle(context.Background(), request(2, "attach", `{}`)); len(got) != 1 || got[0].Event != "initialized" {
		t.Fatalf("attach = %#v", got)
	}

	messages := session.Handle(context.Background(), request(3, "setBreakpoints", `{
		"source":{"name":"main.c","path":"C:\\workspace\\main.c"},
		"breakpoints":[{"line":10},{"line":20}]
	}`))
	if len(messages) != 1 || messages[0].Success == nil || !*messages[0].Success {
		t.Fatalf("setBreakpoints = %#v", messages)
	}
	var body SetBreakpointsBody
	if err := json.Unmarshal(messages[0].Body, &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Breakpoints) != 2 || body.Breakpoints[0].ID != 7 || body.Breakpoints[1].Verified {
		t.Fatalf("body = %#v", body)
	}
	if len(backend.actions) != 2 || backend.actions[0].Kind != ActionAttach || backend.actions[1].Kind != ActionSetBreakpoints {
		t.Fatalf("actions = %#v", backend.actions)
	}
	if got := backend.actions[1].SetBreakpoints.Source.Path; got != `C:\workspace\main.c` {
		t.Fatalf("source path = %q", got)
	}
}

func TestSetBreakpointsRejectsUnimplementedSemanticsAndBadBackendShape(t *testing.T) {
	for _, test := range []struct {
		name      string
		arguments string
	}{
		{"source reference", `{"source":{"sourceReference":1},"breakpoints":[]}`},
		{"modified source", `{"source":{"path":"main.c"},"sourceModified":true,"breakpoints":[]}`},
		{"condition", `{"source":{"path":"main.c"},"breakpoints":[{"line":1,"condition":"x"}]}`},
		{"hit condition", `{"source":{"path":"main.c"},"breakpoints":[{"line":1,"hitCondition":"2"}]}`},
		{"log point", `{"source":{"path":"main.c"},"breakpoints":[{"line":1,"logMessage":"x"}]}`},
		{"mode", `{"source":{"path":"main.c"},"breakpoints":[{"line":1,"mode":"x"}]}`},
		{"mixed legacy lines", `{"source":{"path":"main.c"},"breakpoints":[{"line":1}],"lines":[1]}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			backend := &actionBackend{}
			session := attachedSession(t, backend)
			before := len(backend.actions)
			messages := session.Handle(context.Background(), request(4, "setBreakpoints", test.arguments))
			assertFailure(t, messages)
			if len(backend.actions) != before {
				t.Fatalf("invalid request reached backend: %#v", backend.actions[before:])
			}
		})
	}

	backend := &actionBackend{call: func(action Action) (ActionResult, error) {
		if action.Kind == ActionSetBreakpoints {
			return ActionResult{}, nil
		}
		return ActionResult{}, nil
	}}
	session := attachedSession(t, backend)
	assertFailure(t, session.Handle(context.Background(), request(4, "setBreakpoints", `{"source":{"path":"main.c"},"breakpoints":[{"line":1}]}`)))
}

func TestSetExceptionBreakpointsEmptyConfigurationIsANoOp(t *testing.T) {
	for _, test := range []struct {
		name      string
		arguments string
	}{
		{"missing arguments", ""},
		{"empty object", `{}`},
		{"empty filters", `{"filters":[]}`},
		{"empty filter options", `{"filterOptions":[]}`},
		{"empty exception options", `{"exceptionOptions":[]}`},
		{"all empty", `{"filters":[],"filterOptions":[],"exceptionOptions":[]}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			backend := &actionBackend{}
			session := attachedSession(t, backend)
			before := len(backend.actions)
			messages := session.Handle(context.Background(), request(4, "setExceptionBreakpoints", test.arguments))
			if len(messages) != 1 || messages[0].Success == nil || !*messages[0].Success {
				t.Fatalf("setExceptionBreakpoints(%s) = %#v", test.arguments, messages)
			}
			if len(backend.actions) != before {
				t.Fatalf("empty exception breakpoint configuration reached backend: %#v", backend.actions[before:])
			}
		})
	}
}

func TestSetExceptionBreakpointsFailsClosed(t *testing.T) {
	for _, arguments := range []string{
		`{"filters":["all"]}`,
		`{"filterOptions":[{"filter":"all"}]}`,
		`{"exceptionOptions":[{"path":[],"breakMode":"always"}]}`,
		`{"filters":null}`,
		`{"unknownSemanticField":true}`,
		`{"filterOptions":[{"filter":"all","unknownSemanticField":true}]}`,
	} {
		t.Run(arguments, func(t *testing.T) {
			backend := &actionBackend{}
			session := attachedSession(t, backend)
			before := len(backend.actions)
			assertFailure(t, session.Handle(context.Background(), request(4, "setExceptionBreakpoints", arguments)))
			if len(backend.actions) != before {
				t.Fatalf("exception breakpoint request reached backend: %#v", backend.actions[before:])
			}
		})
	}
}

func TestSetExceptionBreakpointsRejectsConfigurationDuringAttachWindow(t *testing.T) {
	backend := &actionBackend{}
	session := NewSession(backend)
	initializeSession(t, session)
	if got := session.Handle(context.Background(), request(2, "attach", `{}`)); len(got) != 1 || got[0].Event != "initialized" {
		t.Fatalf("attach = %#v", got)
	}

	before := len(backend.actions)
	assertFailure(t, session.Handle(context.Background(), request(3, "setExceptionBreakpoints", `{"filters":["all"]}`)))
	if len(backend.actions) != before {
		t.Fatalf("exception breakpoint request reached backend: %#v", backend.actions[before:])
	}

	if got := session.Handle(context.Background(), request(4, "configurationDone", `{}`)); len(got) != 2 || got[0].Command != "configurationDone" || got[1].Command != "attach" {
		t.Fatalf("configurationDone = %#v", got)
	}
	if len(backend.actions) != before+1 || backend.actions[before].Kind != ActionConfigurationDone {
		t.Fatalf("backend actions after configuration = %#v", backend.actions)
	}
}

func TestInspectionRequestsUseTypedActions(t *testing.T) {
	backend := &actionBackend{call: func(action Action) (ActionResult, error) {
		switch action.Kind {
		case ActionStackTrace:
			return ActionResult{StackFrames: []StackFrame{{ID: 11, Name: "main", Line: 42, Column: 1}}, TotalFrames: 3}, nil
		case ActionScopes:
			return ActionResult{Scopes: []Scope{{Name: "Locals", VariablesReference: 12}}}, nil
		case ActionVariables:
			return ActionResult{Variables: []Variable{{Name: "counter", Value: "7", Type: "int"}}}, nil
		case ActionEvaluate:
			return ActionResult{Evaluation: EvaluateBody{Result: "8", Type: "int"}}, nil
		default:
			return ActionResult{}, nil
		}
	}}
	session := attachedSessionWithInitialize(t, backend, `{"adapterID":"multi-dap","supportsVariablePaging":true,"supportsVariableType":true}`)

	requests := []Envelope{
		request(4, "stackTrace", `{"threadId":1,"startFrame":1,"levels":2}`),
		request(5, "scopes", `{"frameId":11}`),
		request(6, "variables", `{"variablesReference":12,"filter":"named","start":1,"count":2,"format":{"hex":true}}`),
		request(7, "evaluate", `{"expression":"counter + 1","frameId":11,"context":"watch","format":{"hex":true}}`),
	}
	for _, request := range requests {
		messages := session.Handle(context.Background(), request)
		if len(messages) != 1 || messages[0].Success == nil || !*messages[0].Success {
			t.Fatalf("%s = %#v", request.Command, messages)
		}
	}
	want := []ActionKind{ActionAttach, ActionConfigurationDone, ActionStackTrace, ActionScopes, ActionVariables, ActionEvaluate}
	if len(backend.actions) != len(want) {
		t.Fatalf("actions = %#v", backend.actions)
	}
	for index, kind := range want {
		if backend.actions[index].Kind != kind {
			t.Fatalf("action %d = %v, want %v", index, backend.actions[index].Kind, kind)
		}
	}
	if got := backend.actions[4].Variables; got.Filter != "named" || !got.Format.Hex || got.Start != 1 || got.Count != 2 {
		t.Fatalf("variables action = %#v", got)
	}
}

func TestVariablesEmptySnapshotEncodesAsJSONList(t *testing.T) {
	backend := &actionBackend{call: func(action Action) (ActionResult, error) {
		if action.Kind == ActionVariables {
			return ActionResult{}, nil
		}
		return ActionResult{}, nil
	}}
	session := attachedSession(t, backend)
	messages := session.Handle(context.Background(), request(4, "variables", `{"variablesReference":12}`))
	if len(messages) != 1 || messages[0].Success == nil || !*messages[0].Success {
		t.Fatalf("variables = %#v", messages)
	}
	var wire struct {
		Variables json.RawMessage `json:"variables"`
	}
	if err := json.Unmarshal(messages[0].Body, &wire); err != nil {
		t.Fatal(err)
	}
	if got := string(wire.Variables); got != "[]" {
		t.Fatalf("variables wire = %s, want []", messages[0].Body)
	}
}

func TestInspectionResponsesFollowClientCapabilities(t *testing.T) {
	backend := &actionBackend{call: func(action Action) (ActionResult, error) {
		switch action.Kind {
		case ActionVariables:
			return ActionResult{Variables: []Variable{{
				Name: "items", Value: "{...}", Type: "ItemList", NamedVariables: 2, IndexedVariables: 3,
			}}}, nil
		case ActionEvaluate:
			return ActionResult{Evaluation: EvaluateBody{
				Result: "{...}", Type: "ItemList", NamedVariables: 2, IndexedVariables: 3,
			}}, nil
		default:
			return ActionResult{}, nil
		}
	}}

	for _, test := range []struct {
		name       string
		initialize string
		wantPaging bool
		wantType   bool
	}{
		{"paging and type", `{"adapterID":"multi-dap","supportsVariablePaging":true,"supportsVariableType":true}`, true, true},
		{"paging only", `{"adapterID":"multi-dap","supportsVariablePaging":true,"supportsVariableType":false}`, true, false},
		{"type only", `{"adapterID":"multi-dap","supportsVariablePaging":false,"supportsVariableType":true}`, false, true},
		{"neither", `{"adapterID":"multi-dap","supportsVariablePaging":false,"supportsVariableType":false}`, false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			backend.actions = nil
			session := NewSession(backend)
			if messages := session.Handle(context.Background(), request(1, "initialize", test.initialize)); len(messages) != 1 || messages[0].Success == nil || !*messages[0].Success {
				t.Fatalf("initialize = %#v", messages)
			}
			if messages := session.Handle(context.Background(), request(2, "attach", `{}`)); len(messages) != 1 || messages[0].Event != "initialized" {
				t.Fatalf("attach = %#v", messages)
			}
			if messages := session.Handle(context.Background(), request(3, "configurationDone", `{}`)); len(messages) != 2 {
				t.Fatalf("configurationDone = %#v", messages)
			}

			variables := session.Handle(context.Background(), request(4, "variables", `{"variablesReference":12,"start":1,"count":2}`))
			if len(variables) != 1 || variables[0].Success == nil || !*variables[0].Success {
				t.Fatalf("variables = %#v", variables)
			}
			var variablesBody VariablesBody
			if err := json.Unmarshal(variables[0].Body, &variablesBody); err != nil {
				t.Fatal(err)
			}
			variable := variablesBody.Variables[0]
			if (variable.Type != "") != test.wantType || (variable.NamedVariables != 0 || variable.IndexedVariables != 0) != test.wantPaging {
				t.Fatalf("variables body = %#v, want type=%v paging=%v", variable, test.wantType, test.wantPaging)
			}
			var variableWire struct {
				Variables []map[string]json.RawMessage `json:"variables"`
			}
			if err := json.Unmarshal(variables[0].Body, &variableWire); err != nil {
				t.Fatal(err)
			}
			for _, field := range []string{"type", "namedVariables", "indexedVariables"} {
				_, present := variableWire.Variables[0][field]
				wantPresent := (field == "type" && test.wantType) || (field != "type" && test.wantPaging)
				if present != wantPresent {
					t.Fatalf("variables wire field %q presence=%v, want type=%v paging=%v: %s", field, present, test.wantType, test.wantPaging, variables[0].Body)
				}
			}
			variableAction := backend.actions[len(backend.actions)-1].Variables
			if test.wantPaging {
				if variableAction.Start != 1 || variableAction.Count != 2 {
					t.Fatalf("paged request was not forwarded: %#v", variableAction)
				}
			} else if variableAction.Start != 0 || variableAction.Count != 0 {
				t.Fatalf("unnegotiated paging was not ignored: %#v", variableAction)
			}

			evaluation := session.Handle(context.Background(), request(5, "evaluate", `{"expression":"items","frameId":11,"context":"watch"}`))
			if len(evaluation) != 1 || evaluation[0].Success == nil || !*evaluation[0].Success {
				t.Fatalf("evaluate = %#v", evaluation)
			}
			var evaluationBody EvaluateBody
			if err := json.Unmarshal(evaluation[0].Body, &evaluationBody); err != nil {
				t.Fatal(err)
			}
			if (evaluationBody.Type != "") != test.wantType || (evaluationBody.NamedVariables != 0 || evaluationBody.IndexedVariables != 0) != test.wantPaging {
				t.Fatalf("evaluate body = %#v, want type=%v paging=%v", evaluationBody, test.wantType, test.wantPaging)
			}
			var evaluationWire map[string]json.RawMessage
			if err := json.Unmarshal(evaluation[0].Body, &evaluationWire); err != nil {
				t.Fatal(err)
			}
			for _, field := range []string{"type", "namedVariables", "indexedVariables"} {
				_, present := evaluationWire[field]
				wantPresent := (field == "type" && test.wantType) || (field != "type" && test.wantPaging)
				if present != wantPresent {
					t.Fatalf("evaluate wire field %q presence=%v, want type=%v paging=%v: %s", field, present, test.wantType, test.wantPaging, evaluation[0].Body)
				}
			}
		})
	}
}

func TestHoverEvaluationFailsClosed(t *testing.T) {
	backend := &actionBackend{}
	session := attachedSession(t, backend)
	before := len(backend.actions)
	assertFailure(t, session.Handle(context.Background(), request(4, "evaluate", `{"expression":"value","frameId":1,"context":"hover"}`)))
	if len(backend.actions) != before {
		t.Fatalf("hover evaluation reached backend: %#v", backend.actions[before:])
	}
}

func TestEvaluateWithoutFrameIDReachesBackend(t *testing.T) {
	backend := &actionBackend{call: func(action Action) (ActionResult, error) {
		switch action.Kind {
		case ActionAttach, ActionConfigurationDone:
			return ActionResult{}, nil
		case ActionEvaluate:
			if action.Evaluate.FrameID != 0 || action.Evaluate.Context != "repl" {
				t.Fatalf("evaluate arguments = %#v", action.Evaluate)
			}
			return ActionResult{Evaluation: EvaluateBody{Result: "8"}}, nil
		default:
			t.Fatalf("action = %v, want evaluate", action.Kind)
		}
		return ActionResult{}, nil
	}}
	session := attachedSession(t, backend)
	messages := session.Handle(context.Background(), request(4, "evaluate", `{"expression":"counter + 1","context":"repl"}`))
	if len(messages) != 1 || messages[0].Success == nil || !*messages[0].Success {
		t.Fatalf("evaluate = %#v", messages)
	}
}

func TestInspectionValidationFailsBeforeBackend(t *testing.T) {
	for _, test := range []struct {
		command   string
		arguments string
	}{
		{"stackTrace", `{"threadId":0}`},
		{"scopes", `{"frameId":0}`},
		{"variables", `{"variablesReference":1,"filter":"both"}`},
		{"evaluate", `{"expression":"x","frameId":-1}`},
		{"evaluate", `{"expression":"x","frameId":1,"context":"unknown"}`},
	} {
		backend := &actionBackend{}
		session := attachedSession(t, backend)
		before := len(backend.actions)
		assertFailure(t, session.Handle(context.Background(), request(4, test.command, test.arguments)))
		if len(backend.actions) != before {
			t.Fatalf("%s reached backend", test.command)
		}
	}
}

func TestSteppingRequestsAndValidation(t *testing.T) {
	backend := &actionBackend{}
	session := attachedSession(t, backend)
	for seq, command := range []string{"next", "stepIn", "stepOut"} {
		messages := session.Handle(context.Background(), request(int64(seq+4), command, `{"threadId":1,"granularity":"statement"}`))
		if len(messages) != 1 || messages[0].Success == nil || !*messages[0].Success {
			t.Fatalf("%s = %#v", command, messages)
		}
	}
	want := []ActionKind{ActionNext, ActionStepIn, ActionStepOut}
	for index, kind := range want {
		if got := backend.actions[index+2].Kind; got != kind {
			t.Fatalf("stepping action %d = %v, want %v", index, got, kind)
		}
	}

	for _, arguments := range []string{
		`{"threadId":0}`,
		`{"threadId":1,"singleThread":true}`,
		`{"threadId":1,"granularity":"instruction"}`,
	} {
		before := len(backend.actions)
		assertFailure(t, session.Handle(context.Background(), request(9, "next", arguments)))
		if len(backend.actions) != before {
			t.Fatal("invalid stepping request reached backend")
		}
	}
}

func TestSteppingIgnoresUnknownDAPArgumentsButEnforcesKnownConstraints(t *testing.T) {
	backend := &actionBackend{}
	session := attachedSession(t, backend)
	before := len(backend.actions)
	messages := session.Handle(context.Background(), request(4, "next", `{
		"threadId":1,
		"granularity":"statement",
		"singleThread":false,
		"futureClientField":{"opaque":true}
	}`))
	if len(messages) != 1 || messages[0].Success == nil || !*messages[0].Success {
		t.Fatalf("next with unknown DAP argument = %#v", messages)
	}
	if len(backend.actions) != before+1 || backend.actions[before].Kind != ActionNext {
		t.Fatalf("actions = %#v", backend.actions[before:])
	}
	arguments := backend.actions[before].Next
	if arguments.ThreadID != 1 || arguments.SingleThread || arguments.Granularity != "statement" {
		t.Fatalf("typed next arguments = %#v", arguments)
	}

	for _, arguments := range []string{
		`{"threadId":1,"singleThread":true,"futureClientField":true}`,
		`{"threadId":1,"granularity":"instruction","futureClientField":true}`,
	} {
		before := len(backend.actions)
		assertFailure(t, session.Handle(context.Background(), request(5, "stepIn", arguments)))
		if len(backend.actions) != before {
			t.Fatalf("constrained stepIn reached backend: %#v", backend.actions[before:])
		}
	}
}

func TestRunToIsNotAReachableDAPAction(t *testing.T) {
	backend := &actionBackend{}
	session := attachedSession(t, backend)
	before := len(backend.actions)
	assertFailure(t, session.Handle(context.Background(), request(4, "runTo", `{
		"threadId":1,
		"targetId":"0x1000"
	}`)))
	if len(backend.actions) != before {
		t.Fatalf("unsupported runTo reached backend: %#v", backend.actions[before:])
	}
}

func TestMemoryAndDisassemblyRequestsAreBounded(t *testing.T) {
	backend := &actionBackend{call: func(action Action) (ActionResult, error) {
		switch action.Kind {
		case ActionReadMemory:
			return ActionResult{ReadMemory: ReadMemoryBody{Address: "0x1000", Data: "AQI="}}, nil
		case ActionWriteMemory:
			return ActionResult{WriteMemory: WriteMemoryBody{BytesWritten: 2}}, nil
		case ActionDisassemble:
			return ActionResult{Disassembly: []DisassembledInstruction{
				{Address: "0x1000", Instruction: "nop"},
				{Address: "0x1002", Instruction: "nop"},
			}}, nil
		default:
			return ActionResult{}, nil
		}
	}}
	session := attachedSession(t, backend)
	for _, request := range []Envelope{
		request(4, "readMemory", `{"memoryReference":"core:0:0x1000","count":2}`),
		request(5, "writeMemory", `{"memoryReference":"core:0:0x1000","data":"AQI="}`),
		request(6, "disassemble", `{"memoryReference":"core:0:0x1000","instructionCount":2}`),
	} {
		messages := session.Handle(context.Background(), request)
		if len(messages) != 1 || messages[0].Success == nil || !*messages[0].Success {
			t.Fatalf("%s = %#v", request.Command, messages)
		}
	}

	for _, test := range []struct {
		command   string
		arguments string
	}{
		{"readMemory", `{"memoryReference":"x","count":0}`},
		{"readMemory", `{"memoryReference":"x","count":1048577}`},
		{"writeMemory", `{"memoryReference":"x","data":"%%%"}`},
		{"writeMemory", `{"memoryReference":"x","data":""}`},
		{"disassemble", `{"memoryReference":"x","instructionCount":0}`},
		{"disassemble", `{"memoryReference":"x","instructionCount":8193}`},
	} {
		before := len(backend.actions)
		assertFailure(t, session.Handle(context.Background(), request(9, test.command, test.arguments)))
		if len(backend.actions) != before {
			t.Fatalf("invalid %s reached backend", test.command)
		}
	}
}

func TestBackendErrorsRemainDAPFailures(t *testing.T) {
	want := errors.New("capability unavailable")
	backend := &actionBackend{call: func(action Action) (ActionResult, error) {
		if action.Kind == ActionReadMemory {
			return ActionResult{}, want
		}
		return ActionResult{}, nil
	}}
	session := attachedSession(t, backend)
	messages := session.Handle(context.Background(), request(4, "readMemory", `{"memoryReference":"x","count":1}`))
	assertFailure(t, messages)
	if messages[0].Message == "" {
		t.Fatal("backend failure lost its diagnostic")
	}
}

func assertFailure(t *testing.T, messages []Envelope) {
	t.Helper()
	if len(messages) != 1 || messages[0].Success == nil || *messages[0].Success {
		t.Fatalf("messages = %#v, want one failure response", messages)
	}
}
