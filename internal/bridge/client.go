// Package bridge owns the Go-side connection to bridge.py. It deliberately
// contains transport lifetime policy, but no MULTI or DAP semantics.
package bridge

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Tacrolimus/multi-dap/internal/mbp"
)

const (
	// ProtocolVersion is the only MBP version this client accepts.
	ProtocolVersion = 1

	// MaxHandshakeMessageSize bounds a peer-advertised frame limit. It keeps a
	// malformed handshake from turning later frame decoding into an unbounded
	// memory commitment.
	MaxHandshakeMessageSize = 16 << 20
)

var (
	// ErrPoisoned is returned after a deadline or a transport/protocol failure.
	// Such a connection must never be reused: an old MULTI call can still finish.
	ErrPoisoned = errors.New("bridge: connection is poisoned")
	// ErrNonLoopbackAddress rejects MBP endpoints outside the architectural
	// loopback-only boundary from architecture.md §7.6.
	ErrNonLoopbackAddress = errors.New("bridge: MBP endpoint must be a loopback IP address")
)

// RemoteError is a valid MBP error response. It does not poison the
// connection: the request and response completed with a valid envelope.
type RemoteError struct {
	Kind     string
	Message  string
	Raw      string
	RawLossy bool
}

func (e *RemoteError) Error() string {
	return fmt.Sprintf("bridge: remote %s: %s", e.Kind, e.Message)
}

// Client is a single-flight MBP client. Calls are serialized because MBP has
// one outstanding request/response stream, even if callers arrive concurrently.
type Client struct {
	conn   net.Conn
	reader *bufio.Reader
	max    int

	callMu sync.Mutex
	nextID int64

	poisoned atomic.Bool
}

// DialLoopback dials a literal loopback TCP address and validates its
// handshake. Host names are intentionally rejected: accepting a name would
// make the loopback guarantee depend on name resolution at connection time.
func DialLoopback(ctx context.Context, address string) (*Client, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateLoopbackAddress(address); err != nil {
		return nil, err
	}

	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, fmt.Errorf("bridge: dial %s: %w", address, err)
	}
	client, err := newClientContext(ctx, conn)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	return client, nil
}

// newClientContext applies ctx while the peer's mandatory handshake is read.
// DialContext only covers the TCP dial; without this guard a spawned bridge
// which accepts but never handshakes could make startup wait forever.
func newClientContext(ctx context.Context, conn net.Conn) (*Client, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			return nil, err
		}
	}
	finished := make(chan struct{})
	var watcher sync.WaitGroup
	if ctx.Done() != nil {
		watcher.Add(1)
		go func() {
			defer watcher.Done()
			select {
			case <-ctx.Done():
				_ = conn.SetDeadline(time.Now())
			case <-finished:
			}
		}()
	}
	client, err := NewClient(conn)
	close(finished)
	watcher.Wait()
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return nil, contextErr
		}
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		_ = client.Close()
		return nil, err
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		_ = client.Close()
		return nil, err
	}
	return client, nil
}

// NewClient validates an already-connected loopback connection and consumes
// its required handshake. Non-TCP conns are accepted only to permit in-process
// transports such as net.Pipe in tests; production entry points use
// DialLoopback.
func NewClient(conn net.Conn) (*Client, error) {
	if conn == nil {
		return nil, errors.New("bridge: nil connection")
	}
	if err := validateConnectionLoopback(conn); err != nil {
		return nil, err
	}

	reader := bufio.NewReader(conn)
	handshake, err := readHandshake(reader)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	return &Client{conn: conn, reader: reader, max: handshake.MaxMessageSize}, nil
}

// Close permanently poisons the client and interrupts an in-flight call. It
// is safe to call while Call is blocked; net.Conn permits concurrent Close.
func (c *Client) Close() error {
	c.poisoned.Store(true)
	return c.conn.Close()
}

// Poisoned reports whether this connection can no longer carry another call.
func (c *Client) Poisoned() bool { return c.poisoned.Load() }

// Call sends exactly one request and waits for its matching response. A
// context cancellation, read/write failure, or malformed/unmatched response
// poisons the connection so no subsequent request can observe stale traffic.
func (c *Client) Call(ctx context.Context, method string, params, result any) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if method == "" {
		return errors.New("bridge: method is required")
	}
	if c.Poisoned() {
		return ErrPoisoned
	}

	var rawParams json.RawMessage
	if params != nil {
		encoded, err := json.Marshal(params)
		if err != nil {
			return fmt.Errorf("bridge: encode params: %w", err)
		}
		rawParams = encoded
	}

	c.callMu.Lock()
	defer c.callMu.Unlock()
	if c.Poisoned() {
		return ErrPoisoned
	}

	requestID := c.nextID + 1
	c.nextID = requestID
	request := mbp.Request{ID: requestID, Method: method, Params: rawParams}
	if err := c.setContextDeadline(ctx); err != nil {
		c.poison()
		return fmt.Errorf("%w: set deadline: %v", ErrPoisoned, err)
	}
	finished := make(chan struct{})
	interruptDone := make(chan struct{})
	go func() {
		defer close(interruptDone)
		c.interruptOnCancel(ctx, finished)
	}()
	defer func() {
		close(finished)
		<-interruptDone
		_ = c.conn.SetDeadline(time.Time{})
	}()

	if err := writeRequest(c.conn, request); err != nil {
		c.poison()
		if contextErr := callContextError(ctx); contextErr != nil {
			return contextErr
		}
		return fmt.Errorf("%w: write request: %v", ErrPoisoned, err)
	}

	response, err := c.readResponse()
	if err != nil {
		c.poison()
		if contextErr := callContextError(ctx); contextErr != nil {
			return contextErr
		}
		return fmt.Errorf("%w: read response: %v", ErrPoisoned, err)
	}
	if !validResponse(response, requestID) {
		c.poison()
		return fmt.Errorf("%w: protocol error: expected response for request %d", ErrPoisoned, requestID)
	}
	if !response.OK {
		return &RemoteError{
			Kind: response.Error.Kind, Message: response.Error.Message,
			Raw: response.Error.Raw, RawLossy: response.Error.RawLossy,
		}
	}
	if result == nil {
		return nil
	}
	if err := json.Unmarshal(response.Result, result); err != nil {
		c.poison()
		return fmt.Errorf("%w: decode result: %v", ErrPoisoned, err)
	}
	return nil
}

func (c *Client) readResponse() (*mbp.Response, error) {
	line, err := readBoundedLine(c.reader, c.max)
	if err != nil {
		return nil, err
	}
	line = bytesWithoutNewline(line)
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(line, &fields); err != nil {
		return nil, fmt.Errorf("malformed response: %w", err)
	}
	if _, ok := fields["id"]; !ok {
		return nil, errors.New("response is missing id")
	}
	okRaw, ok := fields["ok"]
	if !ok {
		return nil, errors.New("response is missing ok")
	}
	var success bool
	if err := json.Unmarshal(okRaw, &success); err != nil {
		return nil, errors.New("response ok must be boolean")
	}
	if success {
		if len(fields) != 3 || fields["result"] == nil || fields["error"] != nil {
			return nil, errors.New("successful response must contain only id, ok, and result")
		}
	} else {
		errorRaw := fields["error"]
		if len(fields) != 3 || errorRaw == nil || fields["result"] != nil {
			return nil, errors.New("error response must contain only id, ok, and error")
		}
		if err := validateErrorEnvelope(errorRaw); err != nil {
			return nil, err
		}
	}
	var response mbp.Response
	if err := json.Unmarshal(line, &response); err != nil {
		return nil, fmt.Errorf("malformed response: %w", err)
	}
	return &response, nil
}

func validateErrorEnvelope(raw json.RawMessage) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return fmt.Errorf("malformed error envelope: %w", err)
	}
	if len(fields) < 3 || len(fields) > 4 || fields["kind"] == nil || fields["message"] == nil || fields["raw"] == nil {
		return errors.New("error envelope must contain kind, message, raw, and optional raw_lossy")
	}
	for name := range fields {
		if name != "kind" && name != "message" && name != "raw" && name != "raw_lossy" {
			return errors.New("error envelope contains an unknown field")
		}
	}
	var envelope mbp.Error
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("malformed error envelope: %w", err)
	}
	if envelope.Kind == "" || envelope.Message == "" {
		return errors.New("error envelope kind and message are required")
	}
	return nil
}

func callContextError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
		return context.DeadlineExceeded
	}
	return nil
}

func (c *Client) poison() {
	c.poisoned.Store(true)
	_ = c.conn.Close()
}

func (c *Client) setContextDeadline(ctx context.Context) error {
	if deadline, ok := ctx.Deadline(); ok {
		return c.conn.SetDeadline(deadline)
	}
	return c.conn.SetDeadline(time.Time{})
}

func (c *Client) interruptOnCancel(ctx context.Context, finished <-chan struct{}) {
	select {
	case <-ctx.Done():
		// A cancellation with no deadline still has to wake a blocking Read.
		// Closing is necessary afterwards anyway, so this deadline is permanent
		// from the caller's point of view.
		_ = c.conn.SetDeadline(time.Now())
	case <-finished:
	}
}

func validResponse(response *mbp.Response, requestID int64) bool {
	if response == nil || response.ID != requestID {
		return false
	}
	if response.OK {
		return response.Result != nil && response.Error == nil
	}
	return response.Result == nil && response.Error != nil && response.Error.Kind != "" && response.Error.Message != ""
}

func writeRequest(conn net.Conn, request mbp.Request) error {
	encoded, err := json.Marshal(request)
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	for len(encoded) != 0 {
		n, err := conn.Write(encoded)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		encoded = encoded[n:]
	}
	return nil
}

func readHandshake(reader *bufio.Reader) (mbp.Handshake, error) {
	line, err := readBoundedLine(reader, mbp.DefaultMaxFrameSize)
	if err != nil {
		return mbp.Handshake{}, fmt.Errorf("bridge: read handshake: %w", err)
	}
	line = bytesWithoutNewline(line)

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(line, &fields); err != nil {
		return mbp.Handshake{}, fmt.Errorf("bridge: malformed handshake: %w", err)
	}
	if len(fields) != 4 {
		return mbp.Handshake{}, errors.New("bridge: handshake has unknown or missing fields")
	}
	for _, name := range []string{"protocol_version", "bridge_version", "max_message_size", "encoding"} {
		if _, ok := fields[name]; !ok {
			return mbp.Handshake{}, fmt.Errorf("bridge: handshake missing %q", name)
		}
	}
	var handshake mbp.Handshake
	if err := json.Unmarshal(line, &handshake); err != nil {
		return mbp.Handshake{}, fmt.Errorf("bridge: malformed handshake: %w", err)
	}
	if handshake.ProtocolVersion != ProtocolVersion {
		return mbp.Handshake{}, fmt.Errorf("bridge: unsupported protocol version %d", handshake.ProtocolVersion)
	}
	if handshake.Encoding != "utf-8" {
		return mbp.Handshake{}, fmt.Errorf("bridge: unsupported handshake encoding %q", handshake.Encoding)
	}
	if strings.TrimSpace(handshake.BridgeVersion) == "" {
		return mbp.Handshake{}, errors.New("bridge: handshake bridge_version is required")
	}
	if handshake.MaxMessageSize <= 0 || handshake.MaxMessageSize > MaxHandshakeMessageSize {
		return mbp.Handshake{}, fmt.Errorf("bridge: unreasonable max_message_size %d", handshake.MaxMessageSize)
	}
	return handshake, nil
}

func readBoundedLine(reader *bufio.Reader, maximum int) ([]byte, error) {
	var line []byte
	for {
		chunk, err := reader.ReadSlice('\n')
		line = append(line, chunk...)
		if len(line) > maximum {
			return nil, errors.New("handshake exceeds maximum frame size")
		}
		switch err {
		case nil:
			return line, nil
		case bufio.ErrBufferFull:
			continue
		case io.EOF:
			return nil, io.ErrUnexpectedEOF
		default:
			return nil, err
		}
	}
}

func bytesWithoutNewline(line []byte) []byte {
	line = line[:len(line)-1]
	if len(line) > 0 && line[len(line)-1] == '\r' {
		return line[:len(line)-1]
	}
	return line
}

func validateLoopbackAddress(address string) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("bridge: invalid MBP address %q: %w", address, err)
	}
	if _, err := strconv.ParseUint(port, 10, 16); err != nil {
		return fmt.Errorf("bridge: invalid MBP port %q: %w", port, err)
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !ip.IsLoopback() {
		return ErrNonLoopbackAddress
	}
	return nil
}

func validateConnectionLoopback(conn net.Conn) error {
	switch address := conn.RemoteAddr().(type) {
	case *net.TCPAddr:
		if address.IP == nil || !address.IP.IsLoopback() {
			return ErrNonLoopbackAddress
		}
	case *net.UDPAddr:
		if address.IP == nil || !address.IP.IsLoopback() {
			return ErrNonLoopbackAddress
		}
	}
	return nil
}
