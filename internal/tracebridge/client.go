package tracebridge

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	defaultStartupTimeout  = 5 * time.Second
	defaultRPCTimeout      = 3 * time.Second
	defaultShutdownTimeout = 2 * time.Second
	maximumTimeout         = time.Minute
	maxPathBytes           = 4096
	maxInheritedHandles    = 16
)

type Options struct {
	Python              string
	Script              string
	Installation        string
	ExpectedPointerBits int
	ExpectedInfoSize    int
	StartupTimeout      time.Duration
	RPCTimeout          time.Duration
	ShutdownTimeout     time.Duration
	// InheritedHandles are trusted daemon-owned Windows lease duplicates.
	// The caller keeps them open and inheritable until Start returns. The
	// helper retains its inherited handles until actual process termination.
	InheritedHandles []uintptr
}

type pendingCall struct {
	seq       int64
	op        string
	response  chan frame
	delivered bool
	closeAck  bool
}

// Client owns exactly one child process and cannot reconnect after failure.
// Abort invalidates admission immediately without waiting for an RPC or the
// operating system to finish process cleanup. Done proves actual reaping.
type Client struct {
	options Options
	command *exec.Cmd
	owner   processOwner
	input   *os.File
	output  *os.File

	gate           chan struct{}
	mu             sync.Mutex
	pending        *pendingCall
	nextSeq        int64
	opened         bool
	closing        bool
	closed         bool
	poisoned       bool
	processEnded   bool
	exitFailed     bool
	lastGeneration uint64
	failure        error
	cleanupFailure bool

	aborted    chan struct{}
	readerDone chan struct{}
	done       chan struct{}
	abortOnce  sync.Once
	pipesOnce  sync.Once
	writers    sync.WaitGroup
}

// Start launches only the supplied, validated interpreter and helper script.
// The context bounds startup, not the returned process lifetime. No native
// installation or session is opened until an explicit Open call.
// Once a child has been spawned, startup failure returns its poisoned Client
// alongside the error. Callers retain that ownership and use Abort/Wait/Done
// even on failure; a cleanup timeout does not relinquish the child.
func Start(ctx context.Context, options Options) (*Client, error) {
	validated, err := validateOptions(options)
	if err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	startup, cancel := context.WithTimeout(ctx, validated.StartupTimeout)
	defer cancel()
	if err := startup.Err(); err != nil {
		return nil, err
	}
	command := exec.Command(validated.Python, "-I", "-S", "-B", "-u", validated.Script)
	command.Dir = validated.Installation
	configureProcess(command)
	configureInheritance(command, validated.InheritedHandles)
	return startCommand(startup, validated, command)
}

func startCommand(ctx context.Context, options Options, command *exec.Cmd) (*Client, error) {
	owner, err := newProcessOwner()
	if err != nil {
		return nil, &Error{Code: "ownership_failed", cause: ErrTransport}
	}
	return startOwnedCommand(ctx, options, command, owner)
}

func startOwnedCommand(ctx context.Context, options Options, command *exec.Cmd, owner processOwner) (*Client, error) {
	childInput, input, err := os.Pipe()
	if err != nil {
		_ = owner.Close()
		return nil, &Error{Code: "create_input_failed", cause: ErrTransport}
	}
	output, childOutput, err := os.Pipe()
	if err != nil {
		_ = childInput.Close()
		_ = input.Close()
		_ = owner.Close()
		return nil, &Error{Code: "create_output_failed", cause: ErrTransport}
	}
	command.Stdin, command.Stdout, command.Stderr = childInput, childOutput, nil
	client := &Client{
		options: options, command: command, owner: owner, input: input, output: output,
		gate: make(chan struct{}, 1), aborted: make(chan struct{}),
		readerDone: make(chan struct{}), done: make(chan struct{}),
		pending: &pendingCall{op: "ready", response: make(chan frame, 1)},
	}
	client.gate <- struct{}{}
	if err := command.Start(); err != nil {
		_ = childInput.Close()
		_ = childOutput.Close()
		client.closePipes()
		_ = owner.Close()
		return nil, &Error{Code: "start_failed", cause: ErrTransport}
	}
	// Parent-owned pipe ends stay open until this client closes them; Cmd.Wait
	// must not race a final buffered close response by closing StdoutPipe.
	_ = childInput.Close()
	_ = childOutput.Close()
	// Assignment occurs before any Wait/Release goroutine and before startup
	// admission. A failure cannot be hidden by a ready handshake.
	if err := owner.Assign(command.Process); err != nil {
		client.abortWith(&Error{Code: "ownership_assignment_failed", cause: ErrTransport, poisoned: true})
		go client.readLoop()
		go client.waitProcess()
		return client, client.failedStartError(client.abortError())
	}
	go client.readLoop()
	go client.waitProcess()
	greeting := client.pending.response
	select {
	case response := <-greeting:
		if response.event != "ready" || response.ready.PointerBits != options.ExpectedPointerBits {
			client.abortWith(&Error{Code: "invalid_ready", cause: ErrProtocol, poisoned: true})
			return client, client.failedStartError(client.abortError())
		}
		if err := ctx.Err(); err != nil {
			client.abortWith(&Error{Code: "startup_cancelled", cause: err, poisoned: true})
			return client, client.failedStartError(&Error{Code: "startup_cancelled", cause: err, poisoned: true})
		}
		client.mu.Lock()
		unavailable := client.poisoned || client.processEnded
		if !unavailable {
			client.pending = nil
		}
		client.mu.Unlock()
		if unavailable {
			client.Abort()
			return client, client.failedStartError(client.abortError())
		}
		return client, nil
	case <-ctx.Done():
		client.abortWith(&Error{Code: "startup_cancelled", cause: ctx.Err(), poisoned: true})
		return client, client.failedStartError(&Error{Code: "startup_cancelled", cause: ctx.Err(), poisoned: true})
	case <-client.aborted:
		return client, client.failedStartError(client.abortError())
	}
}

func (c *Client) failedStartError(primary error) error {
	ctx, cancel := context.WithTimeout(context.Background(), c.options.ShutdownTimeout)
	defer cancel()
	if err := c.Wait(ctx); err != nil {
		return &Error{
			Code:  "startup_cleanup_unconfirmed",
			cause: errors.Join(primary, ErrCleanupUnconfirmed, err), poisoned: true,
		}
	}
	return primary
}

func (c *Client) Open(ctx context.Context, arguments OpenArgs) (Status, error) {
	if arguments.SessionID == 0 || arguments.SessionID > math.MaxInt32 ||
		!fixedHex(arguments.DLLSHA256, 32) {
		return Status{}, ErrInvalidArguments
	}
	if (arguments.MultiVersion == "8.1.4" && (c.options.ExpectedPointerBits != 64 || c.options.ExpectedInfoSize != 64)) ||
		(arguments.MultiVersion == "7.1.6d" && (c.options.ExpectedPointerBits != 32 || c.options.ExpectedInfoSize != 60)) ||
		(arguments.MultiVersion != "8.1.4" && arguments.MultiVersion != "7.1.6d") {
		return Status{}, ErrInvalidArguments
	}
	installation, err := canonicalDirectory(arguments.Installation)
	if err != nil || !samePath(installation, c.options.Installation) {
		return Status{}, ErrInvalidArguments
	}
	arguments.Installation = c.options.Installation
	arguments.DLLSHA256 = strings.ToLower(arguments.DLLSHA256)
	var result Status
	err = c.call(ctx, "open", arguments, func(data json.RawMessage) (uint64, error) {
		var err error
		result, err = decodeStatus(data, c.options.ExpectedInfoSize)
		return result.Generation, err
	})
	if err != nil {
		return Status{}, err
	}
	return result, err
}

func (c *Client) Status(ctx context.Context) (Status, error) {
	var result Status
	err := c.call(ctx, "status", struct{}{}, func(data json.RawMessage) (uint64, error) {
		var err error
		result, err = decodeStatus(data, c.options.ExpectedInfoSize)
		return result.Generation, err
	})
	if err != nil {
		return Status{}, err
	}
	return result, err
}

func (c *Client) Read(ctx context.Context, arguments ReadArgs) (ReadResult, error) {
	if arguments.Generation == 0 || arguments.Count < 1 || arguments.Count > MaxPacketBatch ||
		uint64(arguments.Count) > math.MaxUint64-arguments.Start {
		return ReadResult{}, ErrInvalidArguments
	}
	var result ReadResult
	err := c.call(ctx, "read", arguments, func(data json.RawMessage) (uint64, error) {
		var err error
		result, err = decodeRead(data, arguments)
		return result.Generation, err
	})
	if err != nil {
		return ReadResult{}, err
	}
	return result, err
}

func (c *Client) StartTrace(ctx context.Context, authorized bool) (ControlResult, error) {
	return c.control(ctx, "start", authorized)
}

func (c *Client) StopTrace(ctx context.Context, authorized bool) (ControlResult, error) {
	return c.control(ctx, "stop", authorized)
}

func (c *Client) control(ctx context.Context, operation string, authorized bool) (ControlResult, error) {
	if !authorized {
		return ControlResult{}, ErrNotAuthorized
	}
	var result ControlResult
	err := c.call(ctx, operation, struct {
		Authorized bool `json:"authorized"`
	}{Authorized: true}, func(data json.RawMessage) (uint64, error) {
		var err error
		result, err = decodeControl(data)
		return result.Generation, err
	})
	if err != nil {
		return ControlResult{}, err
	}
	return result, err
}

// Close explicitly disconnects the helper-owned API connection, then waits
// for the process to exit. It never sends stop_trace or target commands.
func (c *Client) Close(ctx context.Context) error {
	c.mu.Lock()
	alreadyClosed, poisoned := c.closed, c.poisoned
	c.mu.Unlock()
	if poisoned {
		return c.abortError()
	}
	if alreadyClosed {
		return c.waitClosed(ctx)
	}
	if err := c.call(ctx, "close", struct{}{}, func(data json.RawMessage) (uint64, error) {
		return 0, decodeClose(data)
	}); err != nil {
		if errors.Is(err, ErrClosed) {
			c.mu.Lock()
			completed := c.closed && !c.poisoned
			c.mu.Unlock()
			if completed {
				return c.waitClosed(ctx)
			}
		}
		return err
	}
	if err := c.Wait(ctx); err != nil {
		c.abortWith(&Error{Code: "shutdown_cancelled", cause: err, poisoned: true})
		return &Error{Code: "shutdown_cancelled", cause: err, poisoned: true}
	}
	return c.closeOutcome()
}

func (c *Client) waitClosed(ctx context.Context) error {
	if err := c.Wait(ctx); err != nil {
		return err
	}
	return c.closeOutcome()
}

// Reaping alone is not a successful Close: another caller may have invalidated
// the lifetime after the helper's acknowledgement.
func (c *Client) closeOutcome() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.poisoned || c.exitFailed {
		if c.failure != nil {
			return c.failure
		}
		return ErrPoisoned
	}
	return nil
}

// Abort invalidates this lifetime synchronously. Pipe closure and termination
// run independently of an in-flight call; Done/Wait report actual cleanup.
func (c *Client) Abort() {
	c.abortWith(&Error{Code: "aborted", cause: ErrPoisoned, poisoned: true})
}

func (c *Client) abortWith(failure error) {
	c.mu.Lock()
	if !c.poisoned {
		c.poisoned = true
		c.failure = failure
	}
	c.mu.Unlock()
	c.abortOnce.Do(func() {
		close(c.aborted)
		go func() {
			if c.owner != nil {
				_ = c.owner.Close()
			}
			if c.command.Process != nil {
				_ = c.command.Process.Kill()
			}
			c.closePipes()
		}()
	})
}

func (c *Client) abortError() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failure != nil {
		return c.failure
	}
	return ErrPoisoned
}

func (c *Client) Poisoned() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.poisoned
}

func (c *Client) Alive() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.processEnded
}

// Done closes only after process wait, pipe-reader exit and request-writer
// completion. It does not claim any trace or target recovery.
func (c *Client) Done() <-chan struct{} { return c.done }

// Wait observes process cleanup without issuing native operations or changing
// admission. Even a caller with no deadline receives a bounded wait.
func (c *Client) Wait(ctx context.Context) error {
	select {
	case <-c.done:
		return c.cleanupError()
	default:
	}
	if ctx == nil {
		ctx = context.Background()
	}
	wait, cancel := context.WithTimeout(ctx, c.options.ShutdownTimeout)
	defer cancel()
	select {
	case <-c.done:
		return c.cleanupError()
	case <-wait.Done():
		return wait.Err()
	}
}

func (c *Client) cleanupError() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cleanupFailure {
		return ErrCleanupUnconfirmed
	}
	return nil
}

func (c *Client) call(ctx context.Context, operation string, arguments any, validate func(json.RawMessage) (uint64, error)) error {
	if ctx == nil {
		ctx = context.Background()
	}
	rpc, cancel := context.WithTimeout(ctx, c.options.RPCTimeout)
	defer cancel()
	if err := rpc.Err(); err != nil {
		return err
	}
	select {
	case <-rpc.Done():
		return rpc.Err()
	case <-c.aborted:
		return c.abortError()
	case <-c.gate:
	}
	defer func() { c.gate <- struct{}{} }()
	if err := rpc.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	if c.poisoned {
		failure := c.failure
		c.mu.Unlock()
		return failure
	}
	if c.closing || c.closed || c.processEnded {
		c.mu.Unlock()
		return ErrClosed
	}
	if operation == "open" && c.opened {
		c.mu.Unlock()
		return ErrAlreadyOpen
	}
	if operation != "open" && !c.opened {
		c.mu.Unlock()
		return ErrNotOpen
	}
	if c.nextSeq == math.MaxInt64 {
		c.mu.Unlock()
		c.abortWith(&Error{Code: "sequence_exhausted", cause: ErrProtocol, poisoned: true})
		return c.abortError()
	}
	sequence := c.nextSeq + 1
	c.mu.Unlock()
	encoded, err := json.Marshal(struct {
		Version int    `json:"v"`
		Seq     int64  `json:"seq"`
		Op      string `json:"op"`
		Args    any    `json:"args"`
	}{ProtocolVersion, sequence, operation, arguments})
	if err != nil || len(encoded)+1 > MaxRequestBytes {
		return ErrInvalidArguments
	}
	encoded = append(encoded, '\n')
	if err := rpc.Err(); err != nil {
		return err
	}
	pending := &pendingCall{seq: sequence, op: operation, response: make(chan frame, 1)}
	c.mu.Lock()
	if c.poisoned || c.processEnded {
		c.mu.Unlock()
		if !c.Poisoned() {
			c.abortWith(&Error{Code: "process_exited", cause: ErrExited, poisoned: true})
		}
		return c.abortError()
	}
	c.nextSeq = sequence
	c.pending = pending
	if operation == "close" {
		c.closing = true
	}
	c.writers.Add(1)
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		if c.pending == pending {
			c.pending = nil
		}
		c.mu.Unlock()
	}()

	written := make(chan error, 1)
	go func() {
		defer c.writers.Done()
		written <- writeAll(c.input, encoded)
	}()
	select {
	case err := <-written:
		if err != nil {
			c.abortWith(&Error{Code: "write_failed", cause: ErrTransport, poisoned: true})
			return c.callFailure(rpc)
		}
	case <-rpc.Done():
		c.abortWith(&Error{Code: "rpc_cancelled", cause: rpc.Err(), poisoned: true})
		return c.callFailure(rpc)
	case <-c.aborted:
		return c.callFailure(rpc)
	}
	select {
	case response := <-pending.response:
		if err := rpc.Err(); err != nil {
			c.abortWith(&Error{Code: "rpc_cancelled", cause: err, poisoned: true})
			return c.callFailure(rpc)
		}
		if c.Poisoned() {
			return c.abortError()
		}
		if !response.ok {
			if operation == "read" && response.code == "stale_generation" {
				return &RemoteError{Code: response.code, recoverable: true}
			}
			c.abortWith(&RemoteError{Code: response.code})
			return c.abortError()
		}
		generation, err := validate(response.result)
		if err != nil {
			c.abortWith(&Error{Code: "invalid_result", cause: ErrProtocol, poisoned: true})
			return c.abortError()
		}
		c.mu.Lock()
		if c.poisoned {
			c.mu.Unlock()
			return c.abortError()
		}
		if err := rpc.Err(); err != nil {
			c.mu.Unlock()
			c.abortWith(&Error{Code: "rpc_cancelled", cause: err, poisoned: true})
			return c.callFailure(rpc)
		}
		if generation != 0 && generation < c.lastGeneration {
			c.mu.Unlock()
			c.abortWith(&Error{Code: "generation_regressed", cause: ErrProtocol, poisoned: true})
			return c.abortError()
		}
		if generation != 0 {
			c.lastGeneration = generation
		}
		switch operation {
		case "open":
			c.opened = true
		case "close":
			c.closed = true
			c.opened = false
		}
		c.mu.Unlock()
		return nil
	case <-rpc.Done():
		c.abortWith(&Error{Code: "rpc_cancelled", cause: rpc.Err(), poisoned: true})
		return c.callFailure(rpc)
	case <-c.aborted:
		return c.callFailure(rpc)
	}
}

func (c *Client) callFailure(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return &Error{Code: "rpc_cancelled", cause: err, poisoned: true}
	}
	return c.abortError()
}

func (c *Client) readLoop() {
	defer close(c.readerDone)
	reader := bufio.NewReaderSize(c.output, 4096)
	for {
		line, err := readBoundedLine(reader)
		if err != nil {
			c.mu.Lock()
			expectedEOF := errors.Is(err, io.EOF) && c.closing && c.pending != nil && c.pending.closeAck
			if c.closed && errors.Is(err, io.EOF) {
				expectedEOF = true
			}
			poisoned := c.poisoned
			c.mu.Unlock()
			if !expectedEOF && !poisoned {
				cause := ErrTransport
				if errors.Is(err, ErrProtocol) {
					cause = ErrProtocol
				}
				c.abortWith(&Error{Code: "read_failed", cause: cause, poisoned: true})
			}
			return
		}
		message, err := decodeFrame(line)
		if err != nil {
			c.abortWith(&Error{Code: "invalid_frame", cause: ErrProtocol, poisoned: true})
			return
		}
		if message.event == "fatal" {
			c.abortWith(&RemoteError{Code: message.code})
			return
		}
		c.mu.Lock()
		pending := c.pending
		if c.poisoned {
			c.mu.Unlock()
			return
		}
		valid := pending != nil && !pending.delivered
		if valid {
			if pending.seq == 0 {
				valid = message.event == "ready"
			} else {
				valid = message.event == "" && message.seq == pending.seq
			}
		}
		if !valid {
			c.mu.Unlock()
			c.abortWith(&Error{Code: "unexpected_frame", cause: ErrProtocol, poisoned: true})
			return
		}
		pending.delivered = true
		if pending.op == "close" && message.ok && decodeClose(message.result) == nil {
			pending.closeAck = true
		}
		pending.response <- message
		remoteFailure := pending.seq != 0 && !message.ok &&
			!(pending.op == "read" && message.code == "stale_generation")
		c.mu.Unlock()
		if remoteFailure {
			c.abortWith(&RemoteError{Code: message.code})
			return
		}
	}
}

func (c *Client) waitProcess() {
	err := c.command.Wait()
	c.mu.Lock()
	c.processEnded = true
	c.exitFailed = err != nil
	expectedExit := c.closing
	c.mu.Unlock()
	if err != nil || !expectedExit {
		c.abortWith(&Error{Code: "process_exited", cause: ErrExited, poisoned: true})
	}
	// Release the job after the primary child is reaped, before waiting for
	// pipe EOF. Any owned descendant retaining a pipe must not strand cleanup.
	if err := c.owner.Close(); err != nil {
		c.mu.Lock()
		c.cleanupFailure = true
		c.mu.Unlock()
	}
	// processEnded is set under the same lock used before writers.Add, so no
	// new writer can be admitted while cleanup waits for existing writers.
	c.writers.Wait()
	<-c.readerDone
	c.closePipes()
	close(c.done)
}

func (c *Client) closePipes() {
	c.pipesOnce.Do(func() {
		_ = c.input.Close()
		_ = c.output.Close()
	})
}

func readBoundedLine(reader *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		chunk, err := reader.ReadSlice('\n')
		if len(chunk) > MaxResponseBytes-len(line) {
			return nil, ErrProtocol
		}
		line = append(line, chunk...)
		switch err {
		case nil:
			line = line[:len(line)-1]
			if len(line) > 0 && line[len(line)-1] == '\r' {
				line = line[:len(line)-1]
			}
			return line, nil
		case bufio.ErrBufferFull:
		case io.EOF:
			if len(line) != 0 {
				return nil, io.ErrUnexpectedEOF
			}
			return nil, io.EOF
		default:
			return nil, err
		}
	}
}

func writeAll(writer io.Writer, data []byte) error {
	for len(data) != 0 {
		n, err := writer.Write(data)
		if n < 0 || n > len(data) {
			return io.ErrShortWrite
		}
		data = data[n:]
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

func validateOptions(options Options) (Options, error) {
	if (options.ExpectedPointerBits != 32 && options.ExpectedPointerBits != 64) ||
		(options.ExpectedPointerBits == 32 && options.ExpectedInfoSize != 60) ||
		(options.ExpectedPointerBits == 64 && options.ExpectedInfoSize != 64) {
		return Options{}, ErrInvalidOptions
	}
	for _, timeout := range []*time.Duration{&options.StartupTimeout, &options.RPCTimeout, &options.ShutdownTimeout} {
		if *timeout < 0 || *timeout > maximumTimeout {
			return Options{}, ErrInvalidOptions
		}
	}
	if options.StartupTimeout == 0 {
		options.StartupTimeout = defaultStartupTimeout
	}
	if options.RPCTimeout == 0 {
		options.RPCTimeout = defaultRPCTimeout
	}
	if options.ShutdownTimeout == 0 {
		options.ShutdownTimeout = defaultShutdownTimeout
	}
	if len(options.InheritedHandles) > maxInheritedHandles {
		return Options{}, ErrInvalidOptions
	}
	handles := append([]uintptr(nil), options.InheritedHandles...)
	seen := make(map[uintptr]bool, len(handles))
	for _, handle := range handles {
		if handle == 0 || handle == ^uintptr(0) || seen[handle] {
			return Options{}, ErrInvalidOptions
		}
		seen[handle] = true
	}
	if !validInheritedHandles(handles) {
		return Options{}, ErrInvalidOptions
	}
	options.InheritedHandles = handles
	installation, err := canonicalDirectory(options.Installation)
	if err != nil {
		return Options{}, ErrInvalidOptions
	}
	python, err := canonicalFile(options.Python)
	if err != nil || !within(installation, python) {
		return Options{}, ErrInvalidOptions
	}
	script, err := canonicalFile(options.Script)
	if err != nil {
		return Options{}, ErrInvalidOptions
	}
	options.Installation, options.Python, options.Script = installation, python, script
	return options, nil
}

func canonicalDirectory(path string) (string, error) {
	if path == "" || len(path) > maxPathBytes || !filepath.IsAbs(path) || !ordinaryPath(path) {
		return "", ErrInvalidOptions
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil || !filepath.IsAbs(resolved) || !ordinaryPath(resolved) {
		return "", ErrInvalidOptions
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", ErrInvalidOptions
	}
	return filepath.Clean(resolved), nil
}

func canonicalFile(path string) (string, error) {
	if path == "" || len(path) > maxPathBytes || !filepath.IsAbs(path) || !ordinaryPath(path) {
		return "", ErrInvalidOptions
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil || !filepath.IsAbs(resolved) || !ordinaryPath(resolved) {
		return "", ErrInvalidOptions
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		return "", ErrInvalidOptions
	}
	return filepath.Clean(resolved), nil
}

func within(directory, file string) bool {
	relative, err := filepath.Rel(directory, file)
	return err == nil && relative != "." && relative != ".." &&
		!filepath.IsAbs(relative) && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func samePath(left, right string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}
