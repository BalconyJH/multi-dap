package dap

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
)

type inputKind uint8

const (
	inputTargetState inputKind = iota
	inputOutput
	inputTerminate
)

type outboundInput struct {
	kind   inputKind
	state  TargetState
	output OutputBody
}

type inbound struct {
	request Envelope
	err     error
}

// clientConnection contains all state that outlives a single reader call. Its
// serving goroutine is the only writer and the only owner of Session.
type clientConnection struct {
	connection net.Conn
	events     chan outboundInput
	done       chan struct{}
	doneOnce   sync.Once
}

func newClientConnection(connection net.Conn) *clientConnection {
	return &clientConnection{
		connection: connection,
		events:     make(chan outboundInput, defaultEventQueueSize),
		done:       make(chan struct{}),
	}
}

func (c *clientConnection) close() {
	c.doneOnce.Do(func() {
		close(c.done)
		_ = c.connection.Close()
	})
}

func (c *clientConnection) offer(input outboundInput) bool {
	select {
	case <-c.done:
		return false
	default:
	}
	select {
	case <-c.done:
		return false
	case c.events <- input:
		return true
	default:
		return false
	}
}

// serve has one reader goroutine and performs every Session call and wire
// write in this goroutine. This prevents response/event frame interleaving and
// keeps Session's lifecycle state race-free.
func (c *clientConnection) serve(parent context.Context, backend Backend) (serveErr error) {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	stopClose := context.AfterFunc(ctx, func() { _ = c.connection.Close() })

	inboundMessages := make(chan inbound, 1)
	var reader sync.WaitGroup
	reader.Add(1)
	go func() {
		defer reader.Done()
		defer close(inboundMessages)
		decoder := NewDecoder(c.connection)
		for {
			message, err := decoder.Decode()
			entry := inbound{request: message, err: err}
			select {
			case inboundMessages <- entry:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	defer func() {
		cancel()
		stopClose()
		c.close()
		reader.Wait()
	}()

	session := NewSession(backend)
	defer func() {
		if err := session.Close(context.WithoutCancel(parent)); err != nil && serveErr == nil {
			serveErr = fmt.Errorf("dap: detach backend after client loss: %w", err)
		}
	}()
	encoder := NewEncoder(c.connection)
	for {
		select {
		case <-ctx.Done():
			return nil
		case entry, ok := <-inboundMessages:
			if !ok {
				return nil
			}
			if entry.err != nil {
				if errors.Is(entry.err, io.EOF) {
					return nil
				}
				return fmt.Errorf("dap: decode client frame: %w", entry.err)
			}
			if err := encodeAll(encoder, session.Handle(ctx, entry.request)); err != nil {
				return err
			}
			if session.Lifecycle().Phase == Disconnected {
				return nil
			}
		case input := <-c.events:
			var messages []Envelope
			switch input.kind {
			case inputTargetState:
				messages = session.PublishTargetState(input.state)
			case inputOutput:
				messages = session.PublishOutput(input.output)
			case inputTerminate:
				messages = session.Terminate()
			default:
				return fmt.Errorf("dap: unknown asynchronous input %d", input.kind)
			}
			if err := encodeAll(encoder, messages); err != nil {
				return err
			}
			if session.Lifecycle().Phase == Disconnected {
				return nil
			}
		}
	}
}

func encodeAll(encoder *Encoder, messages []Envelope) error {
	for _, message := range messages {
		if err := encoder.Encode(message); err != nil {
			return fmt.Errorf("dap: encode server frame: %w", err)
		}
	}
	return nil
}
