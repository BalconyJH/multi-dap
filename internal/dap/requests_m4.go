package dap

import "context"

func (s *Session) step(ctx context.Context, request Envelope, kind ActionKind) []Envelope {
	if s.lifecycle.Phase != Attached {
		return []Envelope{s.failure(request, request.Command+" requires an attached client")}
	}
	var arguments StepArguments
	if err := decodeArguments(request, &arguments); err != nil {
		return []Envelope{s.failure(request, err.Error())}
	}
	if arguments.ThreadID <= 0 {
		return []Envelope{s.failure(request, request.Command+" requires a positive threadId")}
	}
	if arguments.SingleThread {
		return []Envelope{s.failure(request, "single-thread execution is unsupported for the freeze group")}
	}
	if arguments.Granularity != "" && arguments.Granularity != "statement" {
		return []Envelope{s.failure(request, "only statement stepping granularity is supported")}
	}
	action := Action{Kind: kind}
	switch kind {
	case ActionNext:
		action.Next = arguments
	case ActionStepIn:
		action.StepIn = arguments
	case ActionStepOut:
		action.StepOut = arguments
	default:
		return []Envelope{s.failure(request, "invalid stepping operation")}
	}
	if _, err := s.execute(ctx, action); err != nil {
		return []Envelope{s.failure(request, err.Error())}
	}
	return []Envelope{s.success(request, nil)}
}
