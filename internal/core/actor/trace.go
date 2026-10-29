package actor

import (
	"context"
	"errors"
	"fmt"

	"github.com/Tacrolimus/multi-dap/internal/bridge"
	"github.com/Tacrolimus/multi-dap/internal/core/session"
	"github.com/Tacrolimus/multi-dap/internal/tracecontrol"
)

// TraceService is an actor-owned, independent native-helper lifetime. Execute
// must observe cancellation; Invalidate must synchronously fence old work and
// return without waiting for native I/O. Neither method may reenter Actor.
type TraceService interface {
	Execute(context.Context, string, tracecontrol.Request) (tracecontrol.Result, error)
	Invalidate(string, string)
	Snapshot() tracecontrol.Status
	Enabled() bool
}

// TraceEnabled reports the configured capability, not current native health.
// The value is immutable after construction; no helper is contacted.
func (a *Actor) TraceEnabled() bool { return a != nil && a.traceEnabled }

// Trace admits one typed operation under the current control lease. Independent
// helper I/O remains ordered against other actor work without occupying MBP's
// sole worker. A trace failure never invents a target execution transition.
func (a *Actor) Trace(ctx context.Context, owner session.ControllerID, request tracecontrol.Request) (tracecontrol.Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ch := make(chan reply[tracecontrol.Result], 1)
	if err := a.send(ctx, traceCommand{
		ctx: ctx, owner: owner, request: tracecontrol.CloneRequest(request), reply: ch,
	}); err != nil {
		return tracecontrol.Result{}, err
	}
	return await(ctx, a.done, ch, tracecontrol.Result{})
}

// TraceSnapshot reads only local lifecycle/receipt state. The current lease
// owner may use it even after native trace work requires reconciliation.
func (a *Actor) TraceSnapshot(ctx context.Context, owner session.ControllerID) (tracecontrol.Status, error) {
	ch := make(chan reply[tracecontrol.Status], 1)
	if err := a.send(ctx, traceSnapshotCommand{owner: owner, reply: ch}); err != nil {
		return tracecontrol.Status{}, err
	}
	return await(ctx, a.done, ch, tracecontrol.Status{})
}

type traceCommand struct {
	ctx     context.Context
	owner   session.ControllerID
	request tracecontrol.Request
	reply   chan reply[tracecontrol.Result]
}

func (c traceCommand) apply(r *runtime) {
	if err := r.traceRequest(c.ctx, c.owner, c.request); err != nil {
		respond(c.reply, tracecontrol.Result{}, err)
		return
	}
	epoch := r.controlGeneration
	started := false
	deadline := deadlineRPC
	if c.request.Op == tracecontrol.OpOpen {
		deadline = deadlineTraceBootstrap
	}
	r.enqueue(rpcWork{
		kind: opTrace, parent: c.ctx, deadline: deadline,
		expectedGeneration: r.executor.Generation(), isolatedOwner: c.owner,
		validate: func(r *runtime) error {
			if !r.traceOwnerCurrent(c.owner, epoch) {
				return tracecontrol.ErrStaleOperation
			}
			if err := r.traceRequest(c.ctx, c.owner, c.request); err != nil {
				return err
			}
			started = true
			return nil
		},
		isolated: func(ctx context.Context) (any, error) {
			return r.opts.Trace.Execute(ctx, string(c.owner), c.request)
		},
		done: func(r *runtime, value any, err error) {
			// The same textual owner may have released and reacquired control.
			// It must not inherit work admitted by the preceding lease.
			if !r.traceOwnerCurrent(c.owner, epoch) {
				respond(c.reply, tracecontrol.Result{}, tracecontrol.ErrStaleOperation)
				return
			}
			if started {
				switch {
				case errors.Is(err, context.DeadlineExceeded):
					r.blockTrace("trace_timeout")
					r.invalidateTraceOwner(c.owner, "trace_timeout")
				case errors.Is(err, context.Canceled):
					r.blockTrace("trace_cancelled")
					r.invalidateTraceOwner(c.owner, "trace_cancelled")
				case errors.Is(err, ErrBridgeStaleCompletion):
					r.blockTrace("actor_faulted")
					r.invalidateTraceOwner(c.owner, "actor_faulted")
				}
			}
			if r.faulted {
				respond(c.reply, tracecontrol.Result{}, ErrReconciliationNeeded)
				return
			}
			result, ok := value.(tracecontrol.Result)
			if !ok && err == nil {
				r.blockTrace("actor_faulted")
				r.invalidateTraceOwner(c.owner, "actor_faulted")
				err = errors.New("actor: native trace result has wrong type")
			}
			if err != nil {
				result.Batch = nil
				if r.traceBlocked {
					result.Status = r.traceSnapshot()
					result.Receipt = result.Status.LastReceipt
				}
				err = traceOperationError{operation: c.request.Op, cause: err}
			}
			respond(c.reply, result, err)
		},
	})
}

func (r *runtime) traceRequest(ctx context.Context, owner session.ControllerID, request tracecontrol.Request) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !r.opened {
		return ErrNotOpen
	}
	if r.faulted {
		return ErrReconciliationNeeded
	}
	if r.opts.Trace == nil || !r.opts.Trace.Enabled() {
		return tracecontrol.ErrUnavailable
	}
	if err := r.reducer.RequireControl(owner); err != nil {
		return err
	}
	if r.reducer.Snapshot().Configuring {
		return errors.New("actor: native trace requires completed frontend configuration")
	}
	if r.traceBlocked {
		return tracecontrol.ErrReconciliationRequired
	}
	status := r.opts.Trace.Snapshot()
	if status.NeedsReconciliation {
		r.blockTrace("actor_faulted")
		return tracecontrol.ErrReconciliationRequired
	}
	if err := tracecontrol.ValidateRequest(request); err != nil {
		return err
	}
	if request.Op != tracecontrol.OpOpen && request.Handle != status.Handle {
		return tracecontrol.ErrStaleHandle
	}
	if request.Op == tracecontrol.OpOpen && request.Open.Source.CoreID != nil &&
		!r.hasConfiguredCore(uint64(*request.Open.Source.CoreID)) {
		return errors.New("actor: native trace declaration references an unconfigured core")
	}
	return nil
}

// Preserve machine-readable failure causes without forwarding a provider's
// diagnostic text into the DAP boundary.
type traceOperationError struct {
	operation tracecontrol.Operation
	cause     error
}

func (e traceOperationError) Error() string {
	return fmt.Sprintf("actor: native trace %s failed", e.operation)
}

func (e traceOperationError) Unwrap() error { return e.cause }

func (r *runtime) traceOwnerCurrent(owner session.ControllerID, epoch uint64) bool {
	return owner != "" && epoch == r.controlGeneration && r.reducer.Snapshot().LeaseOwner == owner
}

type traceSnapshotCommand struct {
	owner session.ControllerID
	reply chan reply[tracecontrol.Status]
}

func (c traceSnapshotCommand) apply(r *runtime) {
	if r.opts.Trace == nil || !r.opts.Trace.Enabled() {
		respond(c.reply, tracecontrol.Status{}, tracecontrol.ErrUnavailable)
		return
	}
	if err := r.reducer.RequireControl(c.owner); err != nil {
		respond(c.reply, tracecontrol.Status{}, err)
		return
	}
	respond(c.reply, r.traceSnapshot(), nil)
}

func (r *runtime) blockTrace(reason string) {
	if !r.traceBlocked {
		r.traceBlocked = true
		r.traceBlockReason = reason
	}
}

func (r *runtime) traceSnapshot() tracecontrol.Status {
	status := r.opts.Trace.Snapshot()
	if r.traceBlocked && !status.NeedsReconciliation {
		// The independent runner may have returned to idle just as Actor's
		// deadline won. Keep the actor's admission latch visible without
		// overwriting a more specific receipt already recorded by the service.
		status.State, status.NeedsReconciliation = "faulted", true
		status.LastReceipt = tracecontrol.Receipt{
			Operation: "invalidate", Outcome: "abandoned", Reason: r.traceBlockReason,
			NativeStateUncertain: true, HelperAbortRequested: true, CollectionMayContinue: true,
		}
	}
	return status
}

func (r *runtime) invalidateTraceOwner(owner session.ControllerID, reason string) {
	if r.opts.Trace == nil {
		return
	}
	r.opts.Trace.Invalidate(string(owner), reason)
	in := r.inflight
	if in != nil && in.work.isolated != nil &&
		(owner == "" || in.work.isolatedOwner == owner) {
		r.blockTrace(reason)
		in.cancelIsolated()
	}
	if r.opts.Trace.Snapshot().NeedsReconciliation {
		r.blockTrace(reason)
	}
}

func (r *runtime) dispatchIsolated(token uint64, work rpcWork, ctx context.Context, generation bridge.BridgeGeneration) {
	if err := ctx.Err(); err != nil {
		r.deliver(completion{token: token, generation: generation, err: err})
		return
	}
	completed := make(chan reply[any], 1)
	go func() {
		value, err := work.isolated(ctx)
		completed <- reply[any]{value: value, err: err}
	}()
	select {
	case result := <-completed:
		// A backend which ignores cancellation cannot turn an expired
		// operation into a successful actor result.
		if err := ctx.Err(); err != nil {
			result = reply[any]{err: err}
		}
		r.deliver(completion{token: token, generation: generation, result: result.value, err: result.err})
	case <-ctx.Done():
		r.deliver(completion{token: token, generation: generation, err: ctx.Err()})
	case <-r.done:
	}
}
