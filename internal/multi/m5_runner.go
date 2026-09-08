package multi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/Tacrolimus/multi-dap/internal/bridge"
)

const (
	maxM5MemoryBytes        = 64 * 1024
	maxM5DisassembleEntries = 8192
	maxM5ParsedInstructions = maxM5DisassembleEntries + 1
	maxM5DisassembleLines   = 32 * 1024
	maxM5DisassembleBytes   = 48*1024 + rh850MaximumInstruction
	rh850MaximumInstruction = 6
)

var m5DisassemblyLine = regexp.MustCompile(`^(?:0[xX])?([0-9a-fA-F]+)\t([^\r\n]+)$`)

// M5Runner owns the core-qualified, read-only M5 inspection contract. It is
// deliberately topology-bound: an address alone never chooses a core.
type M5Runner struct {
	mu       sync.Mutex
	topology *Topology
	bound    bool
}

func NewM5Runner(topologies ...*Topology) *M5Runner {
	var topology *Topology
	if len(topologies) == 1 {
		topology = topologies[0]
	}
	return &M5Runner{topology: topology, bound: topology != nil}
}

func (r *M5Runner) BindMULTITopology(topology *Topology) error {
	if r == nil {
		return errors.New("multi: nil M5 runner")
	}
	if topology == nil {
		return errors.New("multi: M5 runner requires topology")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.bound {
		return errors.New("multi: M5 runner topology is already bound")
	}
	r.topology = topology
	r.bound = true
	return nil
}

func (r *M5Runner) driver(client *bridge.Client) (*Driver, *Topology, error) {
	if r == nil {
		return nil, nil, errors.New("multi: M5 runner is not initialized")
	}
	r.mu.Lock()
	topology, bound := r.topology, r.bound
	r.mu.Unlock()
	if !bound || topology == nil {
		return nil, nil, errors.New("multi: M5 runner is not initialized")
	}
	if client == nil {
		return nil, nil, errors.New("multi: M5 bridge client is required")
	}
	driver, err := NewDriver(client)
	if err != nil {
		return nil, nil, err
	}
	return driver, topology, nil
}

// ReadMemory reads exactly byteCount bytes from a configured, stopped core.
func (r *M5Runner) ReadMemory(ctx context.Context, client *bridge.Client, core int, address, byteCount uint64) ([]byte, error) {
	if byteCount == 0 || byteCount > maxM5MemoryBytes {
		return nil, fmt.Errorf("multi: memory byte count must be in 1..%d", maxM5MemoryBytes)
	}
	driver, topology, err := r.driver(client)
	if err != nil {
		return nil, err
	}
	route, err := stoppedM5Route(ctx, topology, driver, core)
	if err != nil {
		return nil, err
	}
	return driver.memoryRead(ctx, route.component, address, byteCount)
}

// Disassemble returns exactly count consecutive instructions for a
// configured, stopped core. RH850 instructions are at most six bytes, so the
// bounded byte request is sufficient to obtain the requested count.
func (r *M5Runner) Disassemble(ctx context.Context, client *bridge.Client, core int, address uint64, instructionOffset int64, count uint32) ([]Instruction, error) {
	// DAP defines this as an instruction count, not a byte offset. MULTI's
	// text output alone cannot safely locate an instruction before address,
	// so fail closed until that capability has a hardware-pinned contract.
	if instructionOffset != 0 {
		return nil, fmt.Errorf("%w: disassembly instruction offsets", ErrUnsupported)
	}
	instructionCount := uint64(count)
	if instructionCount == 0 || instructionCount > maxM5DisassembleEntries {
		return nil, fmt.Errorf("multi: disassembly instruction count must be in 1..%d", maxM5DisassembleEntries)
	}
	// Decode one sentinel instruction beyond the DAP window. Its address gives
	// the exact byte width of the final returned instruction.
	decodeCount := instructionCount + 1
	if decodeCount > math.MaxUint64/rh850MaximumInstruction {
		return nil, errors.New("multi: disassembly byte count overflows")
	}
	byteCount := decodeCount * rh850MaximumInstruction
	if byteCount > maxM5DisassembleBytes {
		return nil, errors.New("multi: disassembly byte count exceeds bridge limit")
	}
	if byteCount > math.MaxUint64-address {
		return nil, errors.New("multi: disassembly address range overflows")
	}
	driver, topology, err := r.driver(client)
	if err != nil {
		return nil, err
	}
	route, err := stoppedM5Route(ctx, topology, driver, core)
	if err != nil {
		return nil, err
	}
	raw, err := driver.disassemble(ctx, route.component, address, byteCount)
	if err != nil {
		return nil, err
	}
	instructions, err := parseM5Disassembly(raw, address, int(decodeCount))
	if err != nil {
		return nil, fmt.Errorf("multi: disassembly text: %w", err)
	}
	span := instructions[len(instructions)-1].Address - address
	if span == 0 || span > maxM5MemoryBytes {
		return nil, errors.New("multi: disassembly opcode span is invalid")
	}
	opcodes, err := driver.memoryRead(ctx, route.component, address, span)
	if err != nil {
		return nil, fmt.Errorf("multi: disassembly opcode bytes: %w", err)
	}
	return attachM5InstructionBytes(instructions, address, opcodes, int(instructionCount))
}

func attachM5InstructionBytes(instructions []Instruction, address uint64, opcodes []byte, count int) ([]Instruction, error) {
	if count <= 0 || len(instructions) < count+1 {
		return nil, errors.New("multi: disassembly opcode window is incomplete")
	}
	for index := 0; index < count; index++ {
		begin := instructions[index].Address - address
		end := instructions[index+1].Address - address
		width := end - begin
		if width != 2 && width != 4 && width != 6 || end > uint64(len(opcodes)) {
			return nil, errors.New("multi: disassembly contains an invalid RH850 instruction width")
		}
		instructions[index].Bytes = append([]byte(nil), opcodes[begin:end]...)
	}
	return instructions[:count], nil
}

func addSignedAddressOffset(address uint64, offset int64) (uint64, error) {
	if offset >= 0 {
		value := uint64(offset)
		if value > math.MaxUint64-address {
			return 0, errors.New("multi: disassembly address offset overflows")
		}
		return address + value, nil
	}
	// Avoid negating MinInt64: -(offset + 1) is representable.
	magnitude := uint64(-(offset + 1)) + 1
	if magnitude > address {
		return 0, errors.New("multi: disassembly address offset underflows")
	}
	return address - magnitude, nil
}

func stoppedM5Route(ctx context.Context, topology *Topology, driver *Driver, core int) (topologyRoute, error) {
	if core < 0 {
		return topologyRoute{}, errors.New("multi: M5 core must not be negative")
	}
	route, err := topology.verifiedRoute(ctx, driver, core)
	if err != nil {
		return topologyRoute{}, err
	}
	process, err := routedSelectedProcess(ctx, driver, route.component)
	if err != nil {
		return topologyRoute{}, err
	}
	if process.Status != StatusStopped {
		return topologyRoute{}, fmt.Errorf("multi: M5 core %d must be stopped", core)
	}
	return route, nil
}

func (d *Driver) memoryRead(ctx context.Context, component ComponentID, address, byteCount uint64) ([]byte, error) {
	var result map[string]json.RawMessage
	if err := d.caller.Call(ctx, "memory_read", map[string]any{
		"component": string(component), "address": address, "byte_count": byteCount,
	}, &result); err != nil {
		return nil, fmt.Errorf("multi: memory_read: %w", err)
	}
	if err := requireFields(result, "data", "byte_count", "status"); err != nil {
		return nil, protocolError("memory_read", err)
	}
	if err := requireCommandSuccess(result["status"]); err != nil {
		return nil, protocolError("memory_read", err)
	}
	var returned uint64
	if err := json.Unmarshal(result["byte_count"], &returned); err != nil || returned != byteCount {
		return nil, protocolError("memory_read", errors.New("byte_count is invalid"))
	}
	encoded, err := decodeString(result["data"])
	if err != nil {
		return nil, protocolError("memory_read data", err)
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || uint64(len(data)) != byteCount {
		return nil, protocolError("memory_read data", errors.New("base64 data length is invalid"))
	}
	return data, nil
}

func (d *Driver) disassemble(ctx context.Context, component ComponentID, address, byteCount uint64) (string, error) {
	var result map[string]json.RawMessage
	if err := d.caller.Call(ctx, "disassemble", map[string]any{
		"component": string(component), "address": address, "byte_count": byteCount,
	}, &result); err != nil {
		return "", fmt.Errorf("multi: disassemble: %w", err)
	}
	lossy, err := requireTextFields(result, "raw", "status")
	if err != nil {
		return "", protocolError("disassemble", err)
	}
	if lossy {
		return "", protocolError("disassemble", errors.New("disassembly text is lossy"))
	}
	if err := requireCommandSuccess(result["status"]); err != nil {
		return "", protocolError("disassemble", err)
	}
	raw, err := decodeString(result["raw"])
	if err != nil {
		return "", protocolError("disassemble raw", err)
	}
	if len(raw) > 1<<20 {
		return "", protocolError("disassemble raw", errors.New("output exceeds size limit"))
	}
	return raw, nil
}

func parseM5Disassembly(raw string, address uint64, count int) ([]Instruction, error) {
	if count <= 0 || count > maxM5ParsedInstructions {
		return nil, errors.New("instruction count is invalid")
	}
	lines := strings.Split(strings.TrimSuffix(raw, "\n"), "\n")
	if len(lines) < count || len(lines) > maxM5DisassembleLines {
		return nil, errors.New("instruction line count is invalid")
	}
	out := make([]Instruction, 0, count)
	var previous uint64
	for index, line := range lines {
		if len(line) > 16*1024 {
			return nil, errors.New("instruction line exceeds size limit")
		}
		match := m5DisassemblyLine.FindStringSubmatch(line)
		if match == nil {
			return nil, errors.New("instruction line is malformed")
		}
		value, err := strconv.ParseUint(match[1], 16, 64)
		if err != nil || strings.TrimSpace(match[2]) == "" {
			return nil, errors.New("instruction line is malformed")
		}
		if index == 0 && value != address {
			return nil, errors.New("first instruction address does not match request")
		}
		if index > 0 && value <= previous {
			return nil, errors.New("instruction addresses are not strictly increasing")
		}
		previous = value
		if len(out) < count {
			out = append(out, Instruction{Address: value, Text: match[2]})
		}
	}
	if len(out) != count {
		return nil, errors.New("insufficient instructions")
	}
	return out, nil
}
