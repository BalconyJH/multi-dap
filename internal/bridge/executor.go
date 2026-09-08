package bridge

import (
	"context"
	"errors"
	"sync"
)

// BridgeGeneration identifies one lifetime of a bridge connection. It is
// monotonic for an Executor; a completion is always tagged with the lifetime
// of the connection that actually attempted its operation.
type BridgeGeneration uint64

// OperationID identifies an operation within one Executor. IDs never repeat,
// including across bridge replacements, so logs can correlate late replies.
type OperationID uint64

// Completion is delivered exactly once for each accepted operation. Consumers
// must fence it against their current BridgeGeneration before mutating actor
// state; this package deliberately retains old-generation completions instead
// of dropping evidence of a stalled call returning late.
type Completion struct {
	Generation BridgeGeneration
	Operation  OperationID
	Result     any
	Err        error
}

// Operation is one bridge action. It is intentionally a function rather than
// a method name so the executor remains a transport scheduler and does not
// grow a copy of Debugger Core's method table.
type Operation func(context.Context, *Client) (any, error)

var ErrExecutorClosed = errors.New("bridge: executor is closed")

// Executor runs all operations through exactly one worker. Replace and Close
// advance the generation immediately and poison the old client without waiting
// for a blocked operation to return.
type Executor struct {
	mu         sync.Mutex
	admission  sync.RWMutex
	submitters sync.WaitGroup
	client     *Client
	generation BridgeGeneration
	nextID     OperationID
	closed     bool

	jobs       chan queuedOperation
	closing    chan struct{}
	workerDone chan struct{}
}

type queuedOperation struct {
	generation BridgeGeneration
	operation  OperationID
	client     *Client
	ctx        context.Context
	run        Operation
	done       chan Completion
}

// NewExecutor starts the sole worker for client. Generation one denotes the
// initially installed bridge; zero is deliberately never a live generation.
func NewExecutor(client *Client) *Executor {
	e := &Executor{
		client:     client,
		generation: 1,
		// The worker is single-flight; a bounded queue keeps the actor responsive
		// while an operation waits on bridge I/O without making submissions
		// unbounded state.
		jobs:       make(chan queuedOperation, 64),
		closing:    make(chan struct{}),
		workerDone: make(chan struct{}),
	}
	go e.worker()
	return e
}

// Submit queues run and returns a per-operation completion channel. Queueing
// obeys ctx so an actor can abandon an operation before it ever reaches the
// worker. Once accepted, it always produces a Completion, including after a
// Replace or Close.
func (e *Executor) Submit(ctx context.Context, run Operation) (OperationID, <-chan Completion, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if run == nil {
		return 0, nil, errors.New("bridge: nil operation")
	}

	e.admission.RLock()
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		e.admission.RUnlock()
		return 0, nil, ErrExecutorClosed
	}
	e.nextID++
	job := queuedOperation{
		generation: e.generation,
		operation:  e.nextID,
		client:     e.client,
		ctx:        ctx,
		run:        run,
		done:       make(chan Completion, 1),
	}
	e.submitters.Add(1)
	e.mu.Unlock()
	e.admission.RUnlock()
	defer e.submitters.Done()

	// Do not close jobs: Submit can be between its lock release and this
	// select. closing makes that race harmless and lets the sole worker drain
	// every accepted item before it exits.
	select {
	case <-e.closing:
		return 0, nil, ErrExecutorClosed
	default:
	}
	select {
	case e.jobs <- job:
		return job.operation, job.done, nil
	case <-e.closing:
		return 0, nil, ErrExecutorClosed
	case <-ctx.Done():
		return 0, nil, ctx.Err()
	}
}

// Replace installs client for future submissions, advances the generation,
// and closes the prior client. It does not wait for the worker: a completion
// already in flight must retain its old generation for the actor to discard.
// After Close, a replacement is immediately closed and the terminal
// generation is returned unchanged.
func (e *Executor) Replace(client *Client) BridgeGeneration {
	e.mu.Lock()
	if e.closed {
		generation := e.generation
		e.mu.Unlock()
		if client != nil {
			_ = client.Close()
		}
		return generation
	}
	old := e.client
	e.client = client
	e.generation++
	generation := e.generation
	e.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
	return generation
}

// Close prevents further submission, advances the generation, and poisons the
// installed client. Existing and queued operations still report completion
// with their original generation.
func (e *Executor) Close() BridgeGeneration {
	e.admission.Lock()
	e.mu.Lock()
	if e.closed {
		generation := e.generation
		e.mu.Unlock()
		e.admission.Unlock()
		return generation
	}
	e.closed = true
	old := e.client
	e.client = nil
	e.generation++
	generation := e.generation
	close(e.closing)
	e.mu.Unlock()
	e.admission.Unlock()
	if old != nil {
		_ = old.Close()
	}
	return generation
}

// Generation returns the generation used for subsequently submitted work.
func (e *Executor) Generation() BridgeGeneration {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.generation
}

func (e *Executor) worker() {
	defer close(e.workerDone)
	for {
		select {
		case job := <-e.jobs:
			e.run(job)
		case <-e.closing:
			// Close must not wait for a blocked operation. Once it completes,
			// however, all submissions already accepted into the bounded queue
			// retain their exactly-once completion guarantee.
			e.submitters.Wait()
			for {
				select {
				case job := <-e.jobs:
					e.run(job)
				default:
					return
				}
			}
		}
	}
}

func (e *Executor) run(job queuedOperation) {
	result, err := job.run(job.ctx, job.client)
	job.done <- Completion{
		Generation: job.generation,
		Operation:  job.operation,
		Result:     result,
		Err:        err,
	}
	close(job.done)
}
