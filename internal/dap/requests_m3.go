package dap

import (
	"context"
	"fmt"
)

func (s *Session) stackTrace(ctx context.Context, request Envelope) []Envelope {
	if s.lifecycle.Phase != Attached {
		return []Envelope{s.failure(request, "stackTrace requires an attached client")}
	}
	var arguments StackTraceArguments
	if err := decodeArguments(request, &arguments); err != nil {
		return []Envelope{s.failure(request, err.Error())}
	}
	if arguments.ThreadID <= 0 {
		return []Envelope{s.failure(request, "stackTrace requires a positive threadId")}
	}
	result, err := s.execute(ctx, Action{Kind: ActionStackTrace, StackTrace: arguments})
	if err != nil {
		return []Envelope{s.failure(request, err.Error())}
	}
	return []Envelope{s.success(request, StackTraceBody{StackFrames: result.StackFrames, TotalFrames: result.TotalFrames})}
}

func (s *Session) scopes(ctx context.Context, request Envelope) []Envelope {
	if s.lifecycle.Phase != Attached {
		return []Envelope{s.failure(request, "scopes requires an attached client")}
	}
	var arguments ScopesArguments
	if err := decodeArguments(request, &arguments); err != nil {
		return []Envelope{s.failure(request, err.Error())}
	}
	if arguments.FrameID <= 0 {
		return []Envelope{s.failure(request, "scopes requires a positive frameId")}
	}
	result, err := s.execute(ctx, Action{Kind: ActionScopes, Scopes: arguments})
	if err != nil {
		return []Envelope{s.failure(request, err.Error())}
	}
	return []Envelope{s.success(request, ScopesBody{Scopes: result.Scopes})}
}

func (s *Session) variables(ctx context.Context, request Envelope) []Envelope {
	if s.lifecycle.Phase != Attached {
		return []Envelope{s.failure(request, "variables requires an attached client")}
	}
	var arguments VariablesArguments
	if err := decodeArguments(request, &arguments); err != nil {
		return []Envelope{s.failure(request, err.Error())}
	}
	if arguments.VariablesReference <= 0 {
		return []Envelope{s.failure(request, "variables requires a positive variablesReference")}
	}
	if arguments.Filter != "" && arguments.Filter != "named" && arguments.Filter != "indexed" {
		return []Envelope{s.failure(request, "variables filter must be named or indexed")}
	}
	if !s.initializeArguments.SupportsVariablePaging {
		// DAP requires an adapter to return the complete snapshot when the
		// client did not negotiate paging, even if it still sends start/count.
		arguments.Start = 0
		arguments.Count = 0
	}
	result, err := s.execute(ctx, Action{Kind: ActionVariables, Variables: arguments})
	if err != nil {
		return []Envelope{s.failure(request, err.Error())}
	}
	return []Envelope{s.success(request, VariablesBody{Variables: s.variablesForClient(result.Variables)})}
}

func (s *Session) evaluate(ctx context.Context, request Envelope) []Envelope {
	if s.lifecycle.Phase != Attached {
		return []Envelope{s.failure(request, "evaluate requires an attached client")}
	}
	var arguments EvaluateArguments
	if err := decodeArguments(request, &arguments); err != nil {
		return []Envelope{s.failure(request, err.Error())}
	}
	if arguments.Expression == "" {
		return []Envelope{s.failure(request, "evaluate requires a non-empty expression")}
	}
	if arguments.FrameID < 0 {
		return []Envelope{s.failure(request, "evaluate frameId must not be negative")}
	}
	if !validEvaluateContext(arguments.Context) {
		return []Envelope{s.failure(request, fmt.Sprintf("unsupported evaluate context %q", arguments.Context))}
	}
	if arguments.Context == "hover" {
		return []Envelope{s.failure(request, "evaluate context hover is unsupported")}
	}
	result, err := s.execute(ctx, Action{Kind: ActionEvaluate, Evaluate: arguments})
	if err != nil {
		return []Envelope{s.failure(request, err.Error())}
	}
	return []Envelope{s.success(request, s.evaluateForClient(result.Evaluation))}
}

func (s *Session) variablesForClient(values []Variable) []Variable {
	// DAP requires VariablesResponseBody.variables to be an array. A nil Go
	// slice encodes as JSON null, which is not a valid empty variables list.
	result := make([]Variable, len(values))
	copy(result, values)
	for index := range result {
		if !s.initializeArguments.SupportsVariableType {
			result[index].Type = ""
		}
		if !s.initializeArguments.SupportsVariablePaging {
			result[index].NamedVariables = 0
			result[index].IndexedVariables = 0
		}
	}
	return result
}

func (s *Session) evaluateForClient(value EvaluateBody) EvaluateBody {
	if !s.initializeArguments.SupportsVariableType {
		value.Type = ""
	}
	if !s.initializeArguments.SupportsVariablePaging {
		value.NamedVariables = 0
		value.IndexedVariables = 0
	}
	return value
}

func validEvaluateContext(value string) bool {
	switch value {
	case "", "watch", "repl", "hover", "clipboard", "variables":
		return true
	default:
		return false
	}
}
