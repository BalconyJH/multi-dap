package control

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"
)

const controlDeadline = 2 * time.Second

// Server is one daemon's local control listener. Listener binding and token
// generation occur before the record is published, so no record ever points
// at a listener that was not successfully reserved.
type Server struct {
	listener net.Listener
	record   Record
	status   func(context.Context) (Status, error)
	shutdown func()
	proxy    func(context.Context, net.Conn) error

	closeOnce sync.Once
	mu        sync.RWMutex
}

type ServerOptions struct {
	ProbeID string
	// ConfigDigest is private disk metadata. It is deliberately absent from
	// Status and the control protocol.
	ConfigDigest string
	DAPAddress   string
	Status       func(context.Context) (Status, error)
	Shutdown     func()
	// Proxy receives the already authenticated control connection after its
	// NDJSON acknowledgement. It must serve raw DAP bytes on that same socket;
	// dialing DAPAddress again would reintroduce a listener-rebind race.
	Proxy func(context.Context, net.Conn) error
}

func NewServer(options ServerOptions) (*Server, error) {
	if options.ProbeID == "" || !validConfigDigest(options.ConfigDigest) || options.Status == nil || options.Shutdown == nil {
		return nil, errors.New("control: probe ID, configuration digest, status, and shutdown are required")
	}
	listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		return nil, fmt.Errorf("control: listen on loopback: %w", err)
	}
	token, err := randomToken()
	if err != nil {
		_ = listener.Close()
		return nil, err
	}
	instance, err := randomToken()
	if err != nil {
		_ = listener.Close()
		return nil, err
	}
	server := &Server{
		listener: listener,
		record: Record{
			Version: ProtocolVersion, ProbeID: options.ProbeID, ConfigDigest: options.ConfigDigest, InstanceID: instance, PID: os.Getpid(),
			ControlAddress: listener.Addr().String(), Token: token,
		},
		status: options.Status, shutdown: options.Shutdown, proxy: options.Proxy,
	}
	if options.DAPAddress != "" {
		if err := server.SetDAPAddress(options.DAPAddress); err != nil {
			_ = listener.Close()
			return nil, err
		}
	}
	return server, nil
}

// SetDAPAddress finalizes a reserved control listener after the DAP listener
// has selected its port. It may be called exactly once before Serve, so the
// published identity cannot change beneath control clients.
func (s *Server) SetDAPAddress(address string) error {
	if s == nil || s.listener == nil {
		return errors.New("control: server is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.record.DAPAddress != "" {
		return errors.New("control: DAP address is already set")
	}
	candidate := s.record
	candidate.DAPAddress = address
	if err := validateRecord(candidate, candidate.ProbeID); err != nil {
		return err
	}
	s.record = candidate
	return nil
}

func randomToken() (string, error) {
	data := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, data); err != nil {
		return "", fmt.Errorf("control: generate token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func (s *Server) Record() Record {
	if s == nil {
		return Record{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.record
}

func (s *Server) Close() error {
	if s == nil || s.listener == nil {
		return nil
	}
	var err error
	s.closeOnce.Do(func() { err = s.listener.Close() })
	return err
}

// Serve accepts one bounded request per connection. The short read/write
// deadline prevents a local peer from retaining handler goroutines forever.
func (s *Server) Serve(ctx context.Context) error {
	if s == nil || s.listener == nil {
		return errors.New("control: server is closed")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	record := s.Record()
	if err := validateRecord(record, record.ProbeID); err != nil {
		return errors.New("control: server DAP address is not set")
	}
	stop := context.AfterFunc(ctx, func() { _ = s.Close() })
	defer stop()
	for {
		connection, err := s.listener.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("control: accept connection: %w", err)
		}
		go s.handle(ctx, connection)
	}
}

func (s *Server) handle(ctx context.Context, connection net.Conn) {
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(controlDeadline))
	line, err := readLine(connection, MaxMessageSize)
	if err != nil {
		return
	}
	req, err := decodeRequest(line)
	if err != nil {
		_ = writeResponse(connection, response{Version: ProtocolVersion, OK: false, Error: &errorReply{Code: "malformed", Message: "invalid control request"}})
		return
	}
	record := s.Record()
	if subtle.ConstantTimeCompare([]byte(req.Token), []byte(record.Token)) != 1 {
		_ = writeResponse(connection, response{Version: ProtocolVersion, OK: false, Error: &errorReply{Code: "unauthorized", Message: "invalid control token"}})
		return
	}
	switch req.Method {
	case MethodStatus:
		statusCtx, cancel := context.WithTimeout(context.Background(), controlDeadline)
		status, err := s.status(statusCtx)
		cancel()
		if err != nil {
			_ = writeResponse(connection, response{Version: ProtocolVersion, OK: false, Error: &errorReply{Code: "unavailable", Message: "daemon status is unavailable"}})
			return
		}
		_ = writeResponse(connection, response{Version: ProtocolVersion, OK: true, Status: &status})
	case MethodShutdown:
		if err := writeResponse(connection, response{Version: ProtocolVersion, OK: true}); err != nil {
			return
		}
		// The acknowledgement is committed before closing the listener or
		// stopping Runtime, so the caller never has to infer shutdown from EOF.
		s.shutdown()
		_ = s.Close()
	case MethodProxy:
		if s.proxy == nil {
			_ = writeResponse(connection, response{Version: ProtocolVersion, OK: false, Error: &errorReply{Code: "unavailable", Message: "daemon proxy is unavailable"}})
			return
		}
		// This acknowledgement is the exact framing boundary: the client must
		// not send raw DAP bytes until it has consumed it, so readLine's local
		// buffer cannot consume any upgraded traffic.
		if err := writeResponse(connection, response{Version: ProtocolVersion, OK: true}); err != nil {
			return
		}
		if err := connection.SetDeadline(time.Time{}); err != nil {
			return
		}
		_ = s.proxy(ctx, connection)
	}
}

func readLine(reader io.Reader, limit int) ([]byte, error) {
	buffered := bufio.NewReader(reader)
	var line []byte
	for {
		chunk, err := buffered.ReadSlice('\n')
		line = append(line, chunk...)
		if len(line) > limit {
			return nil, errors.New("control: message exceeds size limit")
		}
		switch err {
		case nil:
			line = line[:len(line)-1]
			if len(line) > 0 && line[len(line)-1] == '\r' {
				line = line[:len(line)-1]
			}
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

func writeResponse(writer io.Writer, result response) error {
	data, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("control: encode response: %w", err)
	}
	if len(data)+1 > MaxMessageSize {
		return errors.New("control: response exceeds size limit")
	}
	_, err = writer.Write(append(data, '\n'))
	return err
}

// Call authenticates a single request using record and returns status. The
// caller must load the record from its private store; a caller never supplies
// an address or token independently.
func Call(ctx context.Context, record Record, method string) (Status, error) {
	if err := validateRecord(record, record.ProbeID); err != nil {
		return Status{}, err
	}
	if method != MethodStatus && method != MethodShutdown {
		return Status{}, fmt.Errorf("control: unsupported method %q", method)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	dialer := net.Dialer{}
	connection, err := dialer.DialContext(ctx, "tcp", record.ControlAddress)
	if err != nil {
		return Status{}, fmt.Errorf("%w: %v", ErrStaleRecord, err)
	}
	defer connection.Close()
	deadline := time.Now().Add(controlDeadline)
	if limit, ok := ctx.Deadline(); ok && limit.Before(deadline) {
		deadline = limit
	}
	_ = connection.SetDeadline(deadline)
	request, _ := json.Marshal(struct {
		Version int    `json:"version"`
		Method  string `json:"method"`
		Token   string `json:"token"`
	}{ProtocolVersion, method, record.Token})
	if _, err := connection.Write(append(request, '\n')); err != nil {
		return Status{}, fmt.Errorf("%w: %v", ErrStaleRecord, err)
	}
	line, err := readLine(connection, MaxMessageSize)
	if err != nil {
		return Status{}, fmt.Errorf("%w: %v", ErrStaleRecord, err)
	}
	result, err := decodeResponse(line)
	if err != nil {
		return Status{}, fmt.Errorf("%w: %v", ErrStaleRecord, err)
	}
	if !result.OK {
		if result.Error != nil && result.Error.Code == "unauthorized" {
			return Status{}, fmt.Errorf("%w: token rejected", ErrStaleRecord)
		}
		return Status{}, fmt.Errorf("control: daemon rejected %s: %s", method, result.Error.Message)
	}
	if method == MethodStatus && result.Status == nil {
		return Status{}, fmt.Errorf("%w: missing status", ErrStaleRecord)
	}
	status := dereferenceStatus(result.Status)
	if method == MethodStatus && (status.ProbeID != record.ProbeID || status.PID != record.PID || status.DAPAddress != record.DAPAddress) {
		return Status{}, fmt.Errorf("%w: daemon identity does not match record", ErrStaleRecord)
	}
	return status, nil
}

var ErrStaleRecord = errors.New("control: stale daemon record")

func dereferenceStatus(status *Status) Status {
	if status == nil {
		return Status{}
	}
	return *status
}

func decodeResponse(line []byte) (response, error) {
	fields, err := strictObject(line, map[string]struct{}{"version": {}, "ok": {}, "status": {}, "error": {}})
	if err != nil {
		return response{}, err
	}
	if _, ok := fields["version"]; !ok {
		return response{}, fmt.Errorf("%w: missing version", ErrMalformed)
	}
	if _, ok := fields["ok"]; !ok {
		return response{}, fmt.Errorf("%w: missing ok", ErrMalformed)
	}
	var result response
	if err := json.Unmarshal(line, &result); err != nil {
		return response{}, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	if result.Version != ProtocolVersion {
		return response{}, fmt.Errorf("%w: unsupported protocol version", ErrMalformed)
	}
	if result.OK && result.Error != nil || !result.OK && result.Error == nil {
		return response{}, fmt.Errorf("%w: invalid response shape", ErrMalformed)
	}
	if !result.OK && (len(fields) != 3 || result.Status != nil) {
		return response{}, fmt.Errorf("%w: invalid error response shape", ErrMalformed)
	}
	if result.OK && len(fields) != 2 && len(fields) != 3 {
		return response{}, fmt.Errorf("%w: invalid success response shape", ErrMalformed)
	}
	if result.Error != nil && (result.Error.Code == "" || result.Error.Message == "") {
		return response{}, fmt.Errorf("%w: invalid error", ErrMalformed)
	}
	if raw, ok := fields["error"]; ok {
		errorFields, err := strictObject(raw, map[string]struct{}{"code": {}, "message": {}})
		if err != nil || len(errorFields) != 2 || result.Error == nil {
			return response{}, fmt.Errorf("%w: invalid error", ErrMalformed)
		}
	}
	if raw, ok := fields["status"]; ok {
		statusFields, err := strictObject(raw, map[string]struct{}{
			"probe_id": {}, "pid": {}, "dap_address": {}, "target_state": {}, "execution_epoch": {}, "stop_epoch": {},
			"dap_owned_breakpoints": {}, "pending_breakpoints": {}, "orphaned_breakpoints": {},
		})
		if err != nil || (len(statusFields) != 6 && len(statusFields) != 9) || result.Status == nil || result.Status.ProbeID == "" || result.Status.PID <= 0 || result.Status.DAPAddress == "" || result.Status.TargetState == "" || result.Status.DAPOwned < 0 || result.Status.Pending < 0 || result.Status.Orphaned < 0 {
			return response{}, fmt.Errorf("%w: invalid status", ErrMalformed)
		}
	}
	return result, nil
}
