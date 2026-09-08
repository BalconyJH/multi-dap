package dap

import (
	"context"
	"encoding/base64"
)

const (
	maxMemoryRequest = 1 << 20
	// CLion's native DAP frontend may prefetch 32 instructions for every row
	// requested by its public Disassembly view (up to 256 rows).
	maxDisassembleRequest = 8192
)

func (s *Session) readMemory(ctx context.Context, request Envelope) []Envelope {
	if s.lifecycle.Phase != Attached {
		return []Envelope{s.failure(request, "readMemory requires an attached client")}
	}
	var arguments ReadMemoryArguments
	if err := decodeArguments(request, &arguments); err != nil {
		return []Envelope{s.failure(request, err.Error())}
	}
	if arguments.MemoryReference == "" || arguments.Count == 0 || arguments.Count > maxMemoryRequest {
		return []Envelope{s.failure(request, "readMemory requires a reference and a count in 1..1048576")}
	}
	result, err := s.execute(ctx, Action{Kind: ActionReadMemory, ReadMemory: arguments})
	if err != nil {
		return []Envelope{s.failure(request, err.Error())}
	}
	return []Envelope{s.success(request, result.ReadMemory)}
}

func (s *Session) writeMemory(ctx context.Context, request Envelope) []Envelope {
	if s.lifecycle.Phase != Attached {
		return []Envelope{s.failure(request, "writeMemory requires an attached client")}
	}
	var arguments WriteMemoryArguments
	if err := decodeArguments(request, &arguments); err != nil {
		return []Envelope{s.failure(request, err.Error())}
	}
	data, err := base64.StdEncoding.DecodeString(arguments.Data)
	if arguments.MemoryReference == "" || err != nil || len(data) == 0 || len(data) > maxMemoryRequest {
		return []Envelope{s.failure(request, "writeMemory requires a reference and 1..1048576 base64-decoded bytes")}
	}
	result, err := s.execute(ctx, Action{Kind: ActionWriteMemory, WriteMemory: arguments})
	if err != nil {
		return []Envelope{s.failure(request, err.Error())}
	}
	return []Envelope{s.success(request, result.WriteMemory)}
}

func (s *Session) disassemble(ctx context.Context, request Envelope) []Envelope {
	if s.lifecycle.Phase != Attached {
		return []Envelope{s.failure(request, "disassemble requires an attached client")}
	}
	var arguments DisassembleArguments
	if err := decodeArguments(request, &arguments); err != nil {
		return []Envelope{s.failure(request, err.Error())}
	}
	if arguments.MemoryReference == "" || arguments.InstructionCount == 0 || arguments.InstructionCount > maxDisassembleRequest {
		return []Envelope{s.failure(request, "disassemble requires a reference and an instructionCount in 1..8192")}
	}
	result, err := s.execute(ctx, Action{Kind: ActionDisassemble, Disassemble: arguments})
	if err != nil {
		return []Envelope{s.failure(request, err.Error())}
	}
	if len(result.Disassembly) != int(arguments.InstructionCount) {
		return []Envelope{s.failure(request, "debugger backend returned the wrong instruction count")}
	}
	return []Envelope{s.success(request, DisassembleBody{Instructions: result.Disassembly})}
}
