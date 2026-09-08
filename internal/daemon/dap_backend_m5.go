package daemon

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/Tacrolimus/multi-dap/internal/core/actor"
	"github.com/Tacrolimus/multi-dap/internal/dap"
	"github.com/Tacrolimus/multi-dap/internal/multi"
)

// m5Core is optional so existing Core test doubles and non-M5 embeddings keep
// their narrow interface. A backend only routes memory inspection when its
// concrete actor explicitly implements this read-only seam.
type m5Core interface {
	ReadMemory(context.Context, actor.MemoryReadRequest) ([]byte, error)
	Disassemble(context.Context, actor.DisassemblyRequest) ([]multi.Instruction, error)
}

type memoryReference struct {
	core      uint64
	stopEpoch *uint64
	address   uint64
}

func formatMemoryReference(core, stopEpoch, address uint64) string {
	return fmt.Sprintf("multi-dap:core:%d:stop:%d:0x%x", core, stopEpoch, address)
}

func parseMemoryReference(value string) (memoryReference, error) {
	const prefix = "multi-dap:core:"
	if !strings.HasPrefix(value, prefix) {
		return memoryReference{}, errors.New("daemon: memory reference is not a multi-dap opaque reference")
	}
	rest := strings.TrimPrefix(value, prefix)
	parts := strings.Split(rest, ":")
	if len(parts) != 4 || parts[1] != "stop" || !strings.HasPrefix(parts[3], "0x") || len(parts[3]) == 2 {
		return memoryReference{}, errors.New("daemon: malformed multi-dap memory reference")
	}
	core, err := parseCanonicalDecimal(parts[0])
	if err != nil {
		return memoryReference{}, fmt.Errorf("daemon: malformed memory-reference core: %w", err)
	}
	epoch, err := parseCanonicalDecimal(parts[2])
	if err != nil {
		return memoryReference{}, fmt.Errorf("daemon: malformed memory-reference stop epoch: %w", err)
	}
	address, err := strconv.ParseUint(parts[3][2:], 16, 64)
	if err != nil {
		return memoryReference{}, fmt.Errorf("daemon: malformed memory-reference address: %w", err)
	}
	if value != formatMemoryReference(core, epoch, address) {
		return memoryReference{}, errors.New("daemon: multi-dap memory reference is not canonical")
	}
	return memoryReference{core: core, stopEpoch: &epoch, address: address}, nil
}

func parseCanonicalDecimal(value string) (uint64, error) {
	if value == "" || (len(value) > 1 && value[0] == '0') {
		return 0, errors.New("must be a canonical unsigned decimal")
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return 0, errors.New("must be a canonical unsigned decimal")
		}
	}
	return strconv.ParseUint(value, 10, 64)
}

func parseNumericMemoryReference(value string) (uint64, error) {
	if !strings.HasPrefix(value, "0x") || len(value) == 2 {
		return 0, errors.New("daemon: numeric memory reference must be 0x followed by hexadecimal digits")
	}
	return strconv.ParseUint(value[2:], 16, 64)
}

func checkedAddressOffset(address uint64, offset int64) (uint64, error) {
	if offset >= 0 {
		magnitude := uint64(offset)
		if magnitude > math.MaxUint64-address {
			return 0, errors.New("daemon: memory address offset overflows")
		}
		return address + magnitude, nil
	}
	magnitude := uint64(-(offset + 1)) + 1
	if magnitude > address {
		return 0, errors.New("daemon: memory address offset underflows")
	}
	return address - magnitude, nil
}

func (b *Backend) m5() (m5Core, error) {
	core, ok := b.core.(m5Core)
	if !ok {
		return nil, errors.New("daemon: memory and disassembly inspection is unavailable")
	}
	return core, nil
}

func (b *Backend) routeMemoryReference(f *frontend, reference string) (memoryReference, error) {
	if strings.HasPrefix(reference, "multi-dap:") {
		return parseMemoryReference(reference)
	}
	address, err := parseNumericMemoryReference(reference)
	if err != nil {
		return memoryReference{}, err
	}
	core, err := b.numericInspectionCore(f)
	if err != nil {
		return memoryReference{}, err
	}
	return memoryReference{core: core, address: address}, nil
}

func (b *Backend) numericInspectionCore(f *frontend) (uint64, error) {
	if b.defaultInspectionCore != nil {
		if !frontendHasCore(f, *b.defaultInspectionCore) {
			return 0, fmt.Errorf("daemon: configured inspection core %d is not a DAP thread", *b.defaultInspectionCore)
		}
		return *b.defaultInspectionCore, nil
	}
	var only uint64
	seen := make(map[uint64]struct{}, len(f.threads))
	for _, thread := range f.threads {
		seen[thread.CoreID] = struct{}{}
		if len(seen) > 1 {
			return 0, errors.New("daemon: numeric memory reference is ambiguous for a multi-core target; configure inspection.default_core or use an opaque frame reference")
		}
		only = thread.CoreID
	}
	if len(seen) != 1 {
		return 0, errors.New("daemon: no configured core for numeric memory reference")
	}
	return only, nil
}

func frontendHasCore(f *frontend, core uint64) bool {
	for _, thread := range f.threads {
		if thread.CoreID == core {
			return true
		}
	}
	return false
}

func (b *Backend) readMemory(ctx context.Context, arguments dap.ReadMemoryArguments) (dap.ActionResult, error) {
	f, err := b.frontend()
	if err != nil {
		return dap.ActionResult{}, err
	}
	core, err := b.m5()
	if err != nil {
		return dap.ActionResult{}, err
	}
	reference, err := b.routeMemoryReference(f, arguments.MemoryReference)
	if err != nil {
		return dap.ActionResult{}, err
	}
	address, err := checkedAddressOffset(reference.address, arguments.Offset)
	if err != nil {
		return dap.ActionResult{}, err
	}
	data, err := core.ReadMemory(ctx, actor.MemoryReadRequest{CoreID: reference.core, Address: address, Count: arguments.Count, ExpectedStopEpoch: reference.stopEpoch})
	if err != nil {
		return dap.ActionResult{}, fmt.Errorf("daemon: read memory: %w", err)
	}
	if uint64(len(data)) != arguments.Count {
		return dap.ActionResult{}, fmt.Errorf("daemon: memory backend returned %d bytes, want %d", len(data), arguments.Count)
	}
	return dap.ActionResult{ReadMemory: dap.ReadMemoryBody{Address: fmt.Sprintf("0x%x", address), Data: base64.StdEncoding.EncodeToString(data)}}, nil
}

func (b *Backend) disassemble(ctx context.Context, arguments dap.DisassembleArguments) (dap.ActionResult, error) {
	f, err := b.frontend()
	if err != nil {
		return dap.ActionResult{}, err
	}
	core, err := b.m5()
	if err != nil {
		return dap.ActionResult{}, err
	}
	reference, err := b.routeMemoryReference(f, arguments.MemoryReference)
	if err != nil {
		return dap.ActionResult{}, err
	}
	address, err := checkedAddressOffset(reference.address, arguments.Offset)
	if err != nil {
		return dap.ActionResult{}, err
	}
	instructions, err := core.Disassemble(ctx, actor.DisassemblyRequest{CoreID: reference.core, Address: address, InstructionOffset: arguments.InstructionOffset, InstructionCount: arguments.InstructionCount, ExpectedStopEpoch: reference.stopEpoch})
	if err != nil {
		return dap.ActionResult{}, fmt.Errorf("daemon: disassemble: %w", err)
	}
	if len(instructions) != int(arguments.InstructionCount) {
		return dap.ActionResult{}, fmt.Errorf("daemon: disassembly backend returned %d instructions, want %d", len(instructions), arguments.InstructionCount)
	}
	result := make([]dap.DisassembledInstruction, 0, len(instructions))
	for _, instruction := range instructions {
		item := dap.DisassembledInstruction{Address: fmt.Sprintf("0x%x", instruction.Address), Instruction: instruction.Text}
		if len(instruction.Bytes) != 0 {
			item.InstructionBytes = hex.EncodeToString(instruction.Bytes)
		}
		result = append(result, item)
	}
	return dap.ActionResult{Disassembly: result}, nil
}
