package dap

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func TestClientConnectionFramesRequestsAndAsynchronousEvents(t *testing.T) {
	serverSide, clientSide := net.Pipe()
	client := newClientConnection(serverSide)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errs := make(chan error, 1)
	go func() {
		errs <- client.serve(ctx, &fakeBackend{result: ActionResult{State: TargetState{Execution: TargetStopped, Stopped: StoppedBody{Reason: "pause", ThreadID: 1}}}})
	}()

	encoder := NewEncoder(clientSide)
	decoder := NewDecoder(clientSide)
	writeRequest(t, encoder, request(1, "initialize", `{"adapterID":"multi-dap"}`))
	if got := readFrame(t, decoder); got.Type != TypeResponse || got.Command != "initialize" || !*got.Success {
		t.Fatalf("initialize response = %#v", got)
	}

	writeRequest(t, encoder, request(2, "attach", `{}`))
	if got := readFrame(t, decoder); got.Type != TypeEvent || got.Event != "initialized" {
		t.Fatalf("attach event = %#v", got)
	}
	writeRequest(t, encoder, request(3, "configurationDone", `{}`))
	for index, want := range []string{"configurationDone", "attach"} {
		got := readFrame(t, decoder)
		name := got.Command
		if got.Type == TypeEvent {
			name = got.Event
		}
		if name != want {
			t.Fatalf("configuration message %d = %#v, want %q", index, got, want)
		}
	}
	if !client.offer(outboundInput{kind: inputTargetState, state: TargetState{Execution: TargetStopped, Stopped: StoppedBody{Reason: "pause", ThreadID: 1}}}) {
		t.Fatal("initial canonical target state was rejected")
	}
	if got := readFrame(t, decoder); got.Type != TypeEvent || got.Event != "stopped" {
		t.Fatalf("initial target event = %#v", got)
	}

	if !client.offer(outboundInput{kind: inputTargetState, state: TargetState{Execution: TargetRunning, Continued: ContinuedBody{ThreadID: 1}}}) {
		t.Fatal("asynchronous target transition was rejected")
	}
	if got := readFrame(t, decoder); got.Type != TypeEvent || got.Event != "continued" {
		t.Fatalf("asynchronous event = %#v", got)
	}

	if err := clientSide.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-errs; err != nil {
		t.Fatalf("serve after clean EOF: %v", err)
	}
}

func TestClientConnectionFramesUnicodeOutputInOrderWithLaterDAPFrames(t *testing.T) {
	serverSide, clientSide := net.Pipe()
	client := newClientConnection(serverSide)
	errs := make(chan error, 1)
	go func() { errs <- client.serve(context.Background(), &fakeBackend{}) }()

	encoder := NewEncoder(clientSide)
	decoder := NewDecoder(clientSide)
	writeRequest(t, encoder, request(1, "initialize", `{"adapterID":"multi-dap"}`))
	_ = readFrame(t, decoder)
	writeRequest(t, encoder, request(2, "attach", `{}`))
	_ = readFrame(t, decoder)
	writeRequest(t, encoder, request(3, "configurationDone", `{}`))
	_ = readFrame(t, decoder)
	_ = readFrame(t, decoder)

	if !client.offer(outboundInput{kind: inputOutput, output: OutputBody{Category: "console", Output: "caf\u00e9 \u0394\u03bf\u03ba\u03b9\u03bc\u03ae\\n"}}) {
		t.Fatal("unicode output was rejected")
	}
	output := readFrame(t, decoder)
	if output.Type != TypeEvent || output.Event != "output" {
		t.Fatalf("output frame = %#v", output)
	}
	var body OutputBody
	if err := json.Unmarshal(output.Body, &body); err != nil {
		t.Fatal(err)
	}
	if body.Category != "console" || body.Output != "caf\u00e9 \u0394\u03bf\u03ba\u03b9\u03bc\u03ae\\n" {
		t.Fatalf("output body = %#v", body)
	}

	writeRequest(t, encoder, request(4, "threads", `{}`))
	if later := readFrame(t, decoder); later.Type != TypeResponse || later.Command != "threads" || later.Success == nil || !*later.Success {
		t.Fatalf("frame after unicode output = %#v", later)
	}

	_ = clientSide.Close()
	if err := <-errs; err != nil {
		t.Fatalf("serve after EOF = %v", err)
	}
}

func TestOutputFrameUsesContentLengthCRLFAndPreservesFollowingFrame(t *testing.T) {
	session := attachedSession(t, &fakeBackend{})
	output := session.PublishOutput(OutputBody{Category: "console", Output: "caf\u00e9 \u0394\u03bf\u03ba\u03b9\u03bc\u03ae\n"})
	if len(output) != 1 {
		t.Fatalf("output messages = %#v", output)
	}
	later := session.Handle(context.Background(), request(4, "threads", `{}`))
	if len(later) != 1 {
		t.Fatalf("threads messages = %#v", later)
	}

	var wire bytes.Buffer
	encoder := NewEncoder(&wire)
	if err := encodeAll(encoder, append(output, later...)); err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(wire.Bytes(), []byte("Content-Length: ")) || !bytes.Contains(wire.Bytes(), []byte("\r\n\r\n")) {
		t.Fatalf("DAP framing = %q, want Content-Length with CRLF separator", wire.Bytes())
	}
	decoder := NewDecoder(&wire)
	if got := readFrame(t, decoder); got.Type != TypeEvent || got.Event != "output" {
		t.Fatalf("decoded output = %#v", got)
	}
	if got := readFrame(t, decoder); got.Type != TypeResponse || got.Command != "threads" || got.Success == nil || !*got.Success {
		t.Fatalf("decoded frame after output = %#v", got)
	}
}

func TestClientConnectionReportsMalformedFrame(t *testing.T) {
	serverSide, clientSide := net.Pipe()
	client := newClientConnection(serverSide)
	errs := make(chan error, 1)
	go func() { errs <- client.serve(context.Background(), &fakeBackend{}) }()
	if _, err := io.WriteString(clientSide, "Content-Length: nope\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	_ = clientSide.Close()
	err := <-errs
	if err == nil || !strings.Contains(err.Error(), "decode client frame") {
		t.Fatalf("serve error = %v, want malformed-frame error", err)
	}
}

func TestClientConnectionDetachesBackendAfterAbruptEOF(t *testing.T) {
	serverSide, clientSide := net.Pipe()
	client := newClientConnection(serverSide)
	backend := &fakeBackend{}
	errs := make(chan error, 1)
	go func() { errs <- client.serve(context.Background(), backend) }()

	encoder := NewEncoder(clientSide)
	decoder := NewDecoder(clientSide)
	writeRequest(t, encoder, request(1, "initialize", `{"adapterID":"multi-dap"}`))
	_ = readFrame(t, decoder)
	writeRequest(t, encoder, request(2, "attach", `{}`))
	_ = readFrame(t, decoder)
	writeRequest(t, encoder, request(3, "configurationDone", `{}`))
	for range 2 {
		_ = readFrame(t, decoder)
	}

	if err := clientSide.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-errs; err != nil {
		t.Fatalf("serve after EOF = %v", err)
	}
	want := []ActionKind{ActionAttach, ActionConfigurationDone, ActionDisconnect}
	if len(backend.actions) != len(want) {
		t.Fatalf("backend actions = %#v, want %#v", backend.actions, want)
	}
	for index, kind := range want {
		if backend.actions[index].Kind != kind {
			t.Fatalf("backend action %d = %v, want %v", index, backend.actions[index].Kind, kind)
		}
	}
}

func TestClientConnectionReportsWriteFailure(t *testing.T) {
	serverSide, clientSide := net.Pipe()
	client := newClientConnection(serverSide)
	errs := make(chan error, 1)
	go func() { errs <- client.serve(context.Background(), &fakeBackend{}) }()

	if err := NewEncoder(clientSide).Encode(request(1, "initialize", `{"adapterID":"x"}`)); err != nil {
		t.Fatal(err)
	}
	_ = clientSide.Close()
	err := <-errs
	if err == nil || !strings.Contains(err.Error(), "encode server frame") {
		t.Fatalf("serve error = %v, want write failure", err)
	}
}

func TestClientConnectionKeepsServingAfterStackTraceFailure(t *testing.T) {
	serverSide, clientSide := net.Pipe()
	client := newClientConnection(serverSide)
	backend := &actionBackend{call: func(action Action) (ActionResult, error) {
		switch action.Kind {
		case ActionAttach:
			return ActionResult{State: TargetState{Execution: TargetStopped, Stopped: StoppedBody{Reason: "pause", ThreadID: 1}}}, nil
		case ActionStackTrace:
			return ActionResult{}, errors.New("backend stack text is malformed")
		case ActionThreads:
			return ActionResult{Threads: []Thread{{ID: 1, Name: "core0"}}}, nil
		default:
			return ActionResult{}, nil
		}
	}}
	errs := make(chan error, 1)
	go func() { errs <- client.serve(context.Background(), backend) }()

	encoder := NewEncoder(clientSide)
	decoder := NewDecoder(clientSide)
	writeRequest(t, encoder, request(1, "initialize", `{"adapterID":"multi-dap"}`))
	_ = readFrame(t, decoder)
	writeRequest(t, encoder, request(2, "attach", `{}`))
	_ = readFrame(t, decoder)
	writeRequest(t, encoder, request(3, "configurationDone", `{}`))
	for range 2 {
		_ = readFrame(t, decoder)
	}

	writeRequest(t, encoder, request(4, "stackTrace", `{"threadId":1}`))
	if failed := readFrame(t, decoder); failed.Command != "stackTrace" || failed.Success == nil || *failed.Success || !strings.Contains(failed.Message, "malformed") {
		t.Fatalf("stackTrace failure = %#v", failed)
	}
	writeRequest(t, encoder, request(5, "threads", `{}`))
	if got := readFrame(t, decoder); got.Command != "threads" || got.Success == nil || !*got.Success {
		t.Fatalf("threads after stackTrace failure = %#v", got)
	}

	_ = clientSide.Close()
	if err := <-errs; err != nil {
		t.Fatalf("serve after stackTrace failure and EOF = %v", err)
	}
}

func TestClientConnectionCancelsCleanly(t *testing.T) {
	serverSide, clientSide := net.Pipe()
	defer clientSide.Close()
	client := newClientConnection(serverSide)
	ctx, cancel := context.WithCancel(context.Background())
	errs := make(chan error, 1)
	go func() { errs <- client.serve(ctx, &fakeBackend{}) }()
	cancel()
	select {
	case err := <-errs:
		if err != nil {
			t.Fatalf("serve cancellation error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("serve did not exit after context cancellation")
	}
}

func TestServerRejectsSecondClientAndAllowsReconnect(t *testing.T) {
	server, err := Listen("127.0.0.1:0", &fakeBackend{})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serveErrors := make(chan error, 1)
	go func() { serveErrors <- server.Serve(ctx) }()

	first, err := net.Dial("tcp", server.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	firstEncoder := NewEncoder(first)
	firstDecoder := NewDecoder(first)
	writeRequest(t, firstEncoder, request(1, "initialize", `{"adapterID":"multi-dap"}`))
	if got := readFrame(t, firstDecoder); got.Command != "initialize" || !*got.Success {
		t.Fatalf("first client initialize = %#v", got)
	}

	second, err := net.Dial("tcp", server.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	_ = second.SetReadDeadline(time.Now().Add(time.Second))
	secondEncoder := NewEncoder(second)
	secondDecoder := NewDecoder(second)
	writeRequest(t, secondEncoder, request(1, "initialize", `{"adapterID":"second"}`))
	rejected := readFrame(t, secondDecoder)
	if rejected.Type != TypeResponse || rejected.Command != "initialize" || rejected.Success == nil || *rejected.Success || !strings.Contains(rejected.Message, "already active") {
		t.Fatalf("second client response = %#v, want clear active-client rejection", rejected)
	}
	if _, err := secondDecoder.Decode(); err == nil || !errors.Is(err, io.EOF) {
		t.Fatalf("second client post-rejection error = %v, want EOF", err)
	}
	_ = second.Close()
	_ = first.Close()
	waitForNoActiveClient(t, server)

	third, err := net.Dial("tcp", server.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer third.Close()
	thirdEncoder := NewEncoder(third)
	thirdDecoder := NewDecoder(third)
	writeRequest(t, thirdEncoder, request(1, "initialize", `{"adapterID":"multi-dap"}`))
	if got := readFrame(t, thirdDecoder); got.Command != "initialize" || !*got.Success {
		t.Fatalf("reconnected client initialize = %#v", got)
	}

	cancel()
	if err := <-serveErrors; err != nil {
		t.Fatalf("Serve() = %v", err)
	}
}

func TestServerServeConnectionSharesSingleFrontendAdmission(t *testing.T) {
	server, err := Listen("127.0.0.1:0", &fakeBackend{})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serveErrors := make(chan error, 1)
	go func() { serveErrors <- server.Serve(ctx) }()

	upgradeListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upgradeListener.Close()
	upgradedClient, err := net.Dial("tcp", upgradeListener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer upgradedClient.Close()
	upgradedServer, err := upgradeListener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	upgradedDone := make(chan error, 1)
	go func() { upgradedDone <- server.ServeConnection(ctx, upgradedServer) }()

	upgradedEncoder := NewEncoder(upgradedClient)
	upgradedDecoder := NewDecoder(upgradedClient)
	writeRequest(t, upgradedEncoder, request(1, "initialize", `{"adapterID":"multi-dap"}`))
	if got := readFrame(t, upgradedDecoder); got.Command != "initialize" || got.Success == nil || !*got.Success {
		t.Fatalf("upgraded client initialize = %#v", got)
	}

	second, err := net.Dial("tcp", server.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	_ = second.SetReadDeadline(time.Now().Add(time.Second))
	secondEncoder := NewEncoder(second)
	secondDecoder := NewDecoder(second)
	writeRequest(t, secondEncoder, request(1, "initialize", `{"adapterID":"second"}`))
	if got := readFrame(t, secondDecoder); got.Success == nil || *got.Success || !strings.Contains(got.Message, "already active") {
		t.Fatalf("second client response = %#v", got)
	}

	_ = upgradedClient.Close()
	if err := <-upgradedDone; err != nil {
		t.Fatalf("ServeConnection() = %v", err)
	}
	cancel()
	if err := <-serveErrors; err != nil {
		t.Fatalf("Serve() = %v", err)
	}
}

func TestClientConnectionClosesAfterDisconnectResponse(t *testing.T) {
	serverSide, clientSide := net.Pipe()
	client := newClientConnection(serverSide)
	errs := make(chan error, 1)
	go func() { errs <- client.serve(context.Background(), &fakeBackend{}) }()

	encoder := NewEncoder(clientSide)
	decoder := NewDecoder(clientSide)
	writeRequest(t, encoder, request(1, "initialize", `{"adapterID":"multi-dap"}`))
	_ = readFrame(t, decoder)
	writeRequest(t, encoder, request(2, "disconnect", `{}`))
	if got := readFrame(t, decoder); got.Command != "disconnect" || got.Success == nil || !*got.Success {
		t.Fatalf("disconnect response = %#v", got)
	}

	_ = clientSide.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := decoder.Decode(); err == nil || !errors.Is(err, io.EOF) {
		t.Fatalf("read after disconnect = %v, want EOF", err)
	}
	if err := <-errs; err != nil {
		t.Fatalf("serve after disconnect = %v", err)
	}
}

func TestServerDisconnectsClientWhenEventQueueOverflows(t *testing.T) {
	serverSide, clientSide := net.Pipe()
	defer clientSide.Close()
	server := &Server{
		backend: &fakeBackend{},
		errors:  make(chan error, 1),
		active:  newClientConnection(serverSide),
	}

	for index := 0; index < defaultEventQueueSize; index++ {
		if !server.PublishTargetState(TargetState{Execution: TargetRunning}) {
			t.Fatalf("event %d was rejected before the queue filled", index)
		}
	}
	if server.PublishTargetState(TargetState{Execution: TargetRunning}) {
		t.Fatal("overflowing event unexpectedly succeeded")
	}
	select {
	case <-server.active.done:
	case <-time.After(time.Second):
		t.Fatal("overflow did not close the active client")
	}
	select {
	case err := <-server.Errors():
		if err == nil || !strings.Contains(err.Error(), "asynchronous event") {
			t.Fatalf("overflow diagnostic = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("overflow diagnostic was not reported")
	}
}

func TestServerPublishOutputRejectsEmptyAndFailsClosedOnQueueOverflow(t *testing.T) {
	serverSide, clientSide := net.Pipe()
	defer clientSide.Close()
	server := &Server{
		backend: &fakeBackend{},
		errors:  make(chan error, 1),
		active:  newClientConnection(serverSide),
	}
	if server.PublishOutput(OutputBody{Category: "console"}) {
		t.Fatal("empty output unexpectedly succeeded")
	}
	for index := 0; index < defaultEventQueueSize; index++ {
		if !server.PublishOutput(OutputBody{Category: "console", Output: "x"}) {
			t.Fatalf("output %d was rejected before the queue filled", index)
		}
	}
	if server.PublishOutput(OutputBody{Category: "console", Output: "overflow"}) {
		t.Fatal("overflowing output unexpectedly succeeded")
	}
	select {
	case <-server.active.done:
	case <-time.After(time.Second):
		t.Fatal("output overflow did not close the active client")
	}
}

func TestNewServerRejectsMissingBackend(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if _, err := NewServer(listener, nil); err == nil || !strings.Contains(err.Error(), "backend") {
		t.Fatalf("NewServer() error = %v, want missing-backend error", err)
	}
}

func TestListenRejectsNonLiteralLoopbackAddresses(t *testing.T) {
	for _, address := range []string{"localhost:0", "0.0.0.0:0", "[::]:0", "example.test:0"} {
		t.Run(address, func(t *testing.T) {
			if _, err := Listen(address, &fakeBackend{}); err == nil {
				t.Fatalf("Listen(%q) unexpectedly succeeded", address)
			}
		})
	}
}

func writeRequest(t *testing.T, encoder *Encoder, message Envelope) {
	t.Helper()
	if err := encoder.Encode(message); err != nil {
		t.Fatal(err)
	}
}

func readFrame(t *testing.T, decoder *Decoder) Envelope {
	t.Helper()
	message, err := decoder.Decode()
	if err != nil {
		t.Fatal(err)
	}
	return message
}

func waitForNoActiveClient(t *testing.T, server *Server) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		server.mu.Lock()
		active := server.active
		server.mu.Unlock()
		if active == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("server did not release disconnected client")
		}
		time.Sleep(time.Millisecond)
	}
}
