package dap

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"
)

const (
	defaultEventQueueSize     = 64
	maxConcurrentRejections   = 4
	secondClientReplyDeadline = 2 * time.Second
)

// Server owns the DAP listener and permits at most one active frontend. The
// debugger backend remains behind Session; neither the transport nor its
// clients have access to the MULTI bridge.
type Server struct {
	listener net.Listener
	backend  Backend
	errors   chan error
	rejects  chan struct{}

	mu     sync.Mutex
	active *clientConnection
	closed bool
}

// Listen creates a DAP listener on a numeric loopback address. Host names,
// wildcard addresses, and non-loopback interfaces are deliberately rejected:
// the DAP server is a local frontend boundary.
func Listen(address string, backend Backend) (*Server, error) {
	if err := validateLoopbackAddress(address); err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, fmt.Errorf("dap: listen %q: %w", address, err)
	}
	server, err := NewServer(listener, backend)
	if err != nil {
		_ = listener.Close()
		return nil, err
	}
	return server, nil
}

// NewServer takes ownership of a pre-created numeric loopback TCP listener.
// It is useful where a caller needs to reserve an ephemeral port first.
func NewServer(listener net.Listener, backend Backend) (*Server, error) {
	if listener == nil {
		return nil, errors.New("dap: listener is required")
	}
	if backend == nil {
		return nil, errors.New("dap: backend is required")
	}
	if err := validateLoopbackListener(listener); err != nil {
		return nil, err
	}
	return &Server{
		listener: listener,
		backend:  backend,
		errors:   make(chan error, defaultEventQueueSize),
		rejects:  make(chan struct{}, maxConcurrentRejections),
	}, nil
}

// Addr returns the listener address for the IDE launch configuration.
func (s *Server) Addr() net.Addr { return s.listener.Addr() }

// Close stops accepting new DAP clients. It does not command the target or
// tear down an already attached frontend, so a later Terminate event can still
// reach that frontend during daemon failure handling.
func (s *Server) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	return s.listener.Close()
}

// Errors reports client-scoped transport failures without stopping the
// listener. The bounded channel is never closed: a Server may still have a
// client goroutine draining after Serve returns, and diagnostics must never
// block that goroutine.
func (s *Server) Errors() <-chan error { return s.errors }

// Serve accepts clients until the context is cancelled or the listener is
// closed. A failed client session never tears down the listening server.
func (s *Server) Serve(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	stopAccept := context.AfterFunc(ctx, func() { _ = s.listener.Close() })
	defer stopAccept()

	for {
		connection, err := s.listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("dap: accept client: %w", err)
		}
		if !s.claim(connection) {
			if !s.reject(connection) {
				_ = connection.Close()
			}
			continue
		}
		go func(connection net.Conn) {
			if err := s.serveClaimed(ctx, connection); err != nil && ctx.Err() == nil {
				s.reportError(err)
			}
		}(connection)
	}
}

// ServeConnection gives one already-authenticated loopback connection to the
// normal DAP single-frontend machinery. It is used by the control-plane proxy
// upgrade, avoiding a second dial to a mutable DAP address after control
// authentication has completed.
func (s *Server) ServeConnection(ctx context.Context, connection net.Conn) error {
	if connection == nil {
		return errors.New("dap: connection is required")
	}
	if !isLoopbackConnection(connection) {
		return errors.New("dap: connection is not loopback")
	}
	if !s.claim(connection) {
		s.mu.Lock()
		closed := s.closed
		s.mu.Unlock()
		if closed {
			_ = connection.Close()
			return errors.New("dap: server is closed")
		}
		rejectSecondClient(connection)
		return nil
	}
	return s.serveClaimed(ctx, connection)
}

func (s *Server) reject(connection net.Conn) bool {
	select {
	case s.rejects <- struct{}{}:
		go func() {
			defer func() { <-s.rejects }()
			rejectSecondClient(connection)
		}()
		return true
	default:
		return false
	}
}

func rejectSecondClient(connection net.Conn) {
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(secondClientReplyDeadline))
	request, err := NewDecoder(connection).Decode()
	if err != nil {
		return
	}
	success := false
	response := Envelope{
		Seq: 1, Type: TypeResponse, RequestSeq: request.Seq, Command: request.Command,
		Success: &success, Message: "another DAP client is already active",
	}
	_ = NewEncoder(connection).Encode(response)
}

// PublishTargetState queues a canonical target transition for the currently
// attached frontend. It returns false when no session is active or its bounded
// delivery queue is full. A client that cannot accept a complete canonical
// event stream is disconnected; callers should retain canonical state so a
// later attach can obtain a fresh snapshot.
func (s *Server) PublishTargetState(state TargetState) bool {
	return s.offer(outboundInput{kind: inputTargetState, state: state})
}

// PublishOutput queues a DAP output event for the currently attached
// frontend. It is delivered by the connection's single writer, never through
// this process's stdout. False means there is no active client or its bounded
// event queue was full.
func (s *Server) PublishOutput(output OutputBody) bool {
	if output.Output == "" {
		return false
	}
	return s.offer(outboundInput{kind: inputOutput, output: output})
}

// Terminate tells the currently active DAP session that the debugger session
// is unrecoverably lost. It deliberately does not issue a target command.
func (s *Server) Terminate() bool {
	return s.offer(outboundInput{kind: inputTerminate})
}

func (s *Server) offer(input outboundInput) bool {
	s.mu.Lock()
	active := s.active
	s.mu.Unlock()
	if active == nil {
		return false
	}
	if active.offer(input) {
		return true
	}
	active.close()
	s.reportError(errors.New("dap: active client could not accept an asynchronous event"))
	return false
}

func (s *Server) reportError(err error) {
	select {
	case s.errors <- err:
	default:
	}
}

func (s *Server) claim(connection net.Conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.active != nil {
		return false
	}
	s.active = newClientConnection(connection)
	return true
}

func isLoopbackConnection(connection net.Conn) bool {
	address, ok := connection.RemoteAddr().(*net.TCPAddr)
	return ok && address.IP != nil && address.IP.IsLoopback()
}

func (s *Server) release(client *clientConnection) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active == client {
		s.active = nil
	}
}

func (s *Server) serveClaimed(ctx context.Context, connection net.Conn) error {
	s.mu.Lock()
	client := s.active
	s.mu.Unlock()
	if client == nil || client.connection != connection {
		return errors.New("dap: connection was not claimed")
	}
	defer s.release(client)
	return client.serve(ctx, s.backend)
}

func validateLoopbackAddress(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("dap: invalid listen address %q: %w", address, err)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("dap: listen address %q is not a loopback IP literal", address)
	}
	return nil
}

func validateLoopbackListener(listener net.Listener) error {
	tcpAddress, ok := listener.Addr().(*net.TCPAddr)
	if !ok || tcpAddress.IP == nil || !tcpAddress.IP.IsLoopback() {
		return fmt.Errorf("dap: listener %q is not bound to a loopback IP literal", listener.Addr())
	}
	return nil
}
