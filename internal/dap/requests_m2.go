package dap

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
)

const maxProtocolInteger = int64(9007199254740991)

func (s *Session) setBreakpoints(ctx context.Context, request Envelope) []Envelope {
	if s.lifecycle.Phase != Configuring && s.lifecycle.Phase != Attached {
		return []Envelope{s.failure(request, "setBreakpoints requires an attach configuration or attached client")}
	}
	var arguments SetBreakpointsArguments
	if err := decodeArguments(request, &arguments); err != nil {
		return []Envelope{s.failure(request, err.Error())}
	}
	if arguments.Source.Path == "" || arguments.Source.SourceReference != 0 {
		return []Envelope{s.failure(request, "setBreakpoints requires a path-backed source")}
	}
	if arguments.SourceModified {
		return []Envelope{s.failure(request, "modified source breakpoints are unsupported")}
	}
	if arguments.Breakpoints != nil && len(arguments.Lines) != 0 {
		return []Envelope{s.failure(request, "setBreakpoints cannot mix breakpoints with deprecated lines")}
	}
	if arguments.Breakpoints == nil && len(arguments.Lines) != 0 {
		arguments.Breakpoints = make([]SourceBreakpoint, len(arguments.Lines))
		for index, line := range arguments.Lines {
			arguments.Breakpoints[index] = SourceBreakpoint{Line: line}
		}
	}
	for index, candidate := range arguments.Breakpoints {
		minimum := int64(0)
		if s.initializeArguments.LinesStartAt1 {
			minimum = 1
		}
		if candidate.Line < minimum || candidate.Line > maxProtocolInteger {
			return []Envelope{s.failure(request, fmt.Sprintf("breakpoint %d has an invalid line", index))}
		}
		if candidate.Column < 0 || candidate.Column > maxProtocolInteger {
			return []Envelope{s.failure(request, fmt.Sprintf("breakpoint %d has an invalid column", index))}
		}
		if candidate.Condition != "" || candidate.HitCondition != "" || candidate.LogMessage != "" || candidate.Mode != "" {
			return []Envelope{s.failure(request, "conditional, hit-conditional, log, and mode breakpoints are unsupported")}
		}
	}
	result, err := s.execute(ctx, Action{Kind: ActionSetBreakpoints, SetBreakpoints: arguments})
	if err != nil {
		return []Envelope{s.failure(request, err.Error())}
	}
	if len(result.Breakpoints) != len(arguments.Breakpoints) {
		return []Envelope{s.failure(request, "debugger backend returned the wrong breakpoint count")}
	}
	return []Envelope{s.success(request, SetBreakpointsBody{Breakpoints: result.Breakpoints})}
}

func (s *Session) setExceptionBreakpoints(request Envelope) []Envelope {
	if s.lifecycle.Phase != Configuring && s.lifecycle.Phase != Attached {
		return []Envelope{s.failure(request, "setExceptionBreakpoints requires an attach configuration or attached client")}
	}
	arguments, err := decodeSetExceptionBreakpointsArguments(request)
	if err != nil {
		return []Envelope{s.failure(request, err.Error())}
	}
	if len(arguments.Filters) != 0 || len(arguments.FilterOptions) != 0 || len(arguments.ExceptionOptions) != 0 {
		return []Envelope{s.failure(request, "exception breakpoint configuration is unsupported")}
	}
	// An empty request is a DAP-compatible clear operation. Do not reach the
	// backend: exception breakpoint behavior is intentionally not implemented.
	return []Envelope{s.success(request, nil)}
}

func decodeSetExceptionBreakpointsArguments(request Envelope) (SetExceptionBreakpointsArguments, error) {
	var arguments SetExceptionBreakpointsArguments
	if len(request.Arguments) == 0 {
		return arguments, nil
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(request.Arguments, &fields); err != nil || fields == nil {
		return SetExceptionBreakpointsArguments{}, fmt.Errorf("invalid %s arguments: expected an object", request.Command)
	}
	for name, value := range fields {
		switch name {
		case "filters", "filterOptions", "exceptionOptions":
			if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				return SetExceptionBreakpointsArguments{}, fmt.Errorf("invalid %s arguments: %s must be an array", request.Command, name)
			}
		default:
			return SetExceptionBreakpointsArguments{}, fmt.Errorf("invalid %s arguments: unsupported field %q", request.Command, name)
		}
	}

	decoder := json.NewDecoder(bytes.NewReader(request.Arguments))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&arguments); err != nil {
		return SetExceptionBreakpointsArguments{}, fmt.Errorf("invalid %s arguments: %w", request.Command, err)
	}
	return arguments, nil
}
