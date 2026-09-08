package actor

import (
	"context"
	"errors"

	"github.com/Tacrolimus/multi-dap/internal/bridge"
	"github.com/Tacrolimus/multi-dap/internal/multi"
)

var ErrConsoleUnavailable = errors.New("actor: console forwarding is disabled")

// ResetConsole clears the bridge's incremental console cursor so the next
// bounded read replays the current MULTI panes. It is intentionally an actor
// operation: a prior console read must complete first, and subsequent polling
// must observe the reset in executor order.
func (a *Actor) ResetConsole(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	ch := make(chan reply[struct{}], 1)
	if err := a.send(ctx, resetConsoleCommand{reply: ch}); err != nil {
		return err
	}
	_, err := await(ctx, a.done, ch, struct{}{})
	return err
}

type resetConsoleCommand struct{ reply chan reply[struct{}] }

func (c resetConsoleCommand) apply(r *runtime) {
	if !r.opened {
		respond(c.reply, struct{}{}, ErrNotOpen)
		return
	}
	if r.faulted {
		respond(c.reply, struct{}{}, ErrReconciliationNeeded)
		return
	}
	if !r.opts.ConsoleEnabled {
		respond(c.reply, struct{}{}, ErrConsoleUnavailable)
		return
	}
	r.enqueue(r.resetConsoleWork(c.reply))
}

func (r *runtime) resetConsoleWork(reply chan reply[struct{}]) rpcWork {
	return rpcWork{kind: opConsole, run: func(ctx context.Context, client *bridge.Client) (any, error) {
		d, err := multi.NewDriver(client)
		if err != nil {
			return nil, err
		}
		return nil, d.ResetConsole(ctx)
	}, done: func(r *runtime, _ any, err error) {
		if err == nil {
			r.consoleDisabled = false
			r.consoleFailureReported = false
			r.consoleFailurePending = false
			r.consoleFailureTargets = nil
		}
		respond(reply, struct{}{}, err)
	}}
}
