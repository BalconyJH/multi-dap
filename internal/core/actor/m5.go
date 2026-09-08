package actor

import (
	"context"
	"errors"
	"fmt"

	"github.com/Tacrolimus/multi-dap/internal/bridge"
	"github.com/Tacrolimus/multi-dap/internal/core/session"
	"github.com/Tacrolimus/multi-dap/internal/multi"
)

const (
	maxM5MemoryBytes        = 64 * 1024
	maxM5DisassembleEntries = 8192
)

var ErrM5Unavailable = errors.New("actor: memory and disassembly inspection is unavailable")

// M5Runner is the bridge-bound, read-only backend seam for Memory View and
// Disassembly. Actor supplies the client so all target I/O remains serialized
// by its executor and topology binding happens only during Open.
type M5Runner interface {
	ReadMemory(context.Context, *bridge.Client, int, uint64, uint64) ([]byte, error)
	Disassemble(context.Context, *bridge.Client, int, uint64, int64, uint32) ([]multi.Instruction, error)
}

// MemoryReadRequest names a physical configured core explicitly: addresses
// never imply an ownership route in a multicore target. ExpectedStopEpoch is
// optional for clients which hold an opaque stop-bound reference.
type MemoryReadRequest struct {
	CoreID            uint64
	Address           uint64
	Count             uint64
	ExpectedStopEpoch *uint64
}

// DisassemblyRequest is the bounded, core-qualified disassembly operation.
// Address offset validation belongs at the protocol boundary; Actor only
// protects target state, immutable topology routing, and transport ordering.
type DisassemblyRequest struct {
	CoreID            uint64
	Address           uint64
	InstructionOffset int64
	InstructionCount  uint32
	ExpectedStopEpoch *uint64
}

// ReadMemory performs a bounded read against a configured core while the
// actor's canonical snapshot remains at the same stopped epoch.
func (a *Actor) ReadMemory(ctx context.Context, request MemoryReadRequest) ([]byte, error) {
	request.ExpectedStopEpoch = copyStopEpoch(request.ExpectedStopEpoch)
	ch := make(chan reply[[]byte], 1)
	if err := a.send(ctx, readMemoryCommand{request: request, reply: ch}); err != nil {
		return nil, err
	}
	return await(ctx, a.done, ch, []byte(nil))
}

// Disassemble performs a bounded instruction query against a configured core
// while the actor's canonical snapshot remains at the same stopped epoch.
func (a *Actor) Disassemble(ctx context.Context, request DisassemblyRequest) ([]multi.Instruction, error) {
	request.ExpectedStopEpoch = copyStopEpoch(request.ExpectedStopEpoch)
	ch := make(chan reply[[]multi.Instruction], 1)
	if err := a.send(ctx, disassembleCommand{request: request, reply: ch}); err != nil {
		return nil, err
	}
	return await(ctx, a.done, ch, []multi.Instruction(nil))
}

func copyStopEpoch(epoch *uint64) *uint64 {
	if epoch == nil {
		return nil
	}
	copy := *epoch
	return &copy
}

type readMemoryCommand struct {
	request MemoryReadRequest
	reply   chan reply[[]byte]
}

func (c readMemoryCommand) apply(r *runtime) {
	stopEpoch, core, err := r.m5Request(c.request.CoreID, c.request.ExpectedStopEpoch)
	if err != nil {
		respond(c.reply, nil, err)
		return
	}
	if c.request.Count == 0 || c.request.Count > maxM5MemoryBytes {
		respond(c.reply, nil, fmt.Errorf("actor: memory byte count must be in 1..%d", maxM5MemoryBytes))
		return
	}
	r.enqueue(r.m5ReadMemoryWork(stopEpoch, core, c.request, c.reply))
}

type disassembleCommand struct {
	request DisassemblyRequest
	reply   chan reply[[]multi.Instruction]
}

func (c disassembleCommand) apply(r *runtime) {
	stopEpoch, core, err := r.m5Request(c.request.CoreID, c.request.ExpectedStopEpoch)
	if err != nil {
		respond(c.reply, nil, err)
		return
	}
	if c.request.InstructionCount == 0 || c.request.InstructionCount > maxM5DisassembleEntries {
		respond(c.reply, nil, fmt.Errorf("actor: disassembly instruction count must be in 1..%d", maxM5DisassembleEntries))
		return
	}
	r.enqueue(r.m5DisassembleWork(stopEpoch, core, c.request, c.reply))
}

func (r *runtime) m5Request(coreID uint64, expected *uint64) (uint64, int, error) {
	if !r.opened {
		return 0, 0, ErrNotOpen
	}
	if r.faulted {
		return 0, 0, ErrReconciliationNeeded
	}
	if r.opts.M5 == nil {
		return 0, 0, ErrM5Unavailable
	}
	snapshot := r.reducer.Snapshot()
	if snapshot.State != session.TargetStopped {
		return 0, 0, errors.New("actor: target must be stopped for memory and disassembly inspection")
	}
	stopEpoch := uint64(snapshot.StopEpoch)
	if expected != nil && *expected != stopEpoch {
		return 0, 0, fmt.Errorf("actor: stale stop epoch %d (current %d)", *expected, stopEpoch)
	}
	for _, thread := range r.threads {
		if thread.CoreID == coreID {
			if coreID > uint64(maxInt()) {
				return 0, 0, fmt.Errorf("actor: configured core %d is outside platform range", coreID)
			}
			return stopEpoch, int(coreID), nil
		}
	}
	return 0, 0, fmt.Errorf("actor: unknown configured core %d", coreID)
}

func (r *runtime) m5ReadMemoryWork(stopEpoch uint64, core int, request MemoryReadRequest, reply chan reply[[]byte]) rpcWork {
	return rpcWork{kind: opInspection, validate: r.stoppedEpochGuard(stopEpoch), run: func(ctx context.Context, client *bridge.Client) (any, error) {
		return r.opts.M5.ReadMemory(ctx, client, core, request.Address, request.Count)
	}, done: func(r *runtime, value any, err error) {
		if !r.sameStoppedEpoch(stopEpoch) {
			err = ErrInspectionStale
			value = nil
		}
		data, ok := value.([]byte)
		if !ok && err == nil {
			err = errors.New("actor: memory read result has wrong type")
		}
		respond(reply, append([]byte(nil), data...), err)
	}}
}

func (r *runtime) m5DisassembleWork(stopEpoch uint64, core int, request DisassemblyRequest, reply chan reply[[]multi.Instruction]) rpcWork {
	return rpcWork{kind: opInspection, validate: r.stoppedEpochGuard(stopEpoch), run: func(ctx context.Context, client *bridge.Client) (any, error) {
		return r.opts.M5.Disassemble(ctx, client, core, request.Address, request.InstructionOffset, request.InstructionCount)
	}, done: func(r *runtime, value any, err error) {
		if !r.sameStoppedEpoch(stopEpoch) {
			err = ErrInspectionStale
			value = nil
		}
		instructions, ok := value.([]multi.Instruction)
		if !ok && err == nil {
			err = errors.New("actor: disassembly result has wrong type")
		}
		respond(reply, cloneInstructions(instructions), err)
	}}
}

func (r *runtime) sameStoppedEpoch(stopEpoch uint64) bool {
	snapshot := r.reducer.Snapshot()
	return snapshot.State == session.TargetStopped && uint64(snapshot.StopEpoch) == stopEpoch
}

func cloneInstructions(in []multi.Instruction) []multi.Instruction {
	if in == nil {
		return nil
	}
	out := make([]multi.Instruction, len(in))
	copy(out, in)
	for i := range out {
		out[i].Bytes = append([]byte(nil), out[i].Bytes...)
	}
	return out
}
