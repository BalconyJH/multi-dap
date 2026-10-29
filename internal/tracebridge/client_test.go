package tracebridge

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	fakeHelperMode  = "MULTI_DAP_TRACEBRIDGE_FAKE"
	fakeCloseSignal = "MULTI_DAP_TRACEBRIDGE_CLOSE_SIGNAL"
)

func TestMain(m *testing.M) {
	if mode := os.Getenv(fakeHelperMode); mode != "" {
		os.Exit(runFakeHelper(mode))
	}
	os.Exit(m.Run())
}

func runFakeHelper(mode string) int {
	if mode == "job_parent" {
		return runJobParent()
	}
	emit := func(value any) {
		_ = json.NewEncoder(os.Stdout).Encode(value)
	}
	greeting := map[string]any{
		"v": 1, "event": "ready", "protocol": ProtocolName,
		"python_major": 3, "python_minor": 8, "pointer_bits": 64,
	}
	if mode == "legacy" {
		greeting["pointer_bits"] = 32
	}
	switch mode {
	case "ready_timeout":
		time.Sleep(30 * time.Second)
		return 0
	case "early_exit":
		return 7
	case "ready_arch":
		greeting["pointer_bits"] = 32
	case "ready_python":
		greeting["python_minor"] = 7
	case "ready_bad":
		_, _ = io.WriteString(os.Stdout, "PRIVATE_RAW_STDOUT\n")
		time.Sleep(30 * time.Second)
		return 0
	case "ready_oversize":
		_, _ = io.WriteString(os.Stdout, strings.Repeat("x", MaxResponseBytes+1))
		time.Sleep(30 * time.Second)
		return 0
	case "ready_partial_eof":
		_, _ = io.WriteString(os.Stdout, `{"v":1,"event":"ready"`)
		return 0
	case "ready_fatal":
		emit(map[string]any{"v": 1, "event": "fatal", "error": map[string]string{"code": "reader_failed"}})
		time.Sleep(30 * time.Second)
		return 0
	}
	if mode == "stderr_flood" {
		for index := 0; index < 1024; index++ {
			_, _ = io.WriteString(os.Stderr, strings.Repeat("PRIVATE_RAW_STDERR", 64))
		}
	}
	if mode == "fragmented" {
		data, _ := json.Marshal(greeting)
		for _, b := range append(data, '\n') {
			_, _ = os.Stdout.Write([]byte{b})
		}
	} else {
		emit(greeting)
	}
	if mode == "job_stubborn" {
		// Simulates a native owner blocked in FFI: stdin EOF alone cannot
		// make this fixture leave while its daemon parent disappears.
		time.Sleep(30 * time.Second)
		return 0
	}
	generation := uint64(1)
	if mode == "generation_regressed" {
		generation = 2
	}
	readCount := 0
	reader := bufio.NewReader(os.Stdin)
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			return 0
		}
		var request struct {
			V    int             `json:"v"`
			Seq  int64           `json:"seq"`
			Op   string          `json:"op"`
			Args json.RawMessage `json:"args"`
		}
		if json.Unmarshal(line, &request) != nil || request.V != 1 {
			return 8
		}
		response := map[string]any{"v": 1, "seq": request.Seq, "ok": true}
		switch request.Op {
		case "open", "status":
			if request.Op == "status" {
				switch mode {
				case "status_timeout", "status_late":
					time.Sleep(30 * time.Second)
				case "status_delay":
					time.Sleep(150 * time.Millisecond)
				case "status_wrong_seq":
					response["seq"] = request.Seq + 1
				case "status_fatal":
					emit(map[string]any{"v": 1, "event": "fatal", "error": map[string]string{"code": "native_failure"}})
					time.Sleep(30 * time.Second)
					return 0
				case "fatal_stale":
					emit(map[string]any{"v": 1, "event": "fatal", "error": map[string]string{"code": "stale_generation"}})
					time.Sleep(30 * time.Second)
					return 0
				case "stale_status":
					emit(map[string]any{"v": 1, "seq": request.Seq, "ok": false, "error": map[string]string{"code": "stale_generation"}})
					continue
				case "status_unknown_code":
					emit(map[string]any{"v": 1, "seq": request.Seq, "ok": false, "error": map[string]string{"code": "PRIVATE_NATIVE_ERROR"}})
					continue
				case "status_partial_eof":
					_, _ = io.WriteString(os.Stdout, `{"v":1,"seq":`)
					return 0
				}
			}
			result := fakeStatus(generation)
			if mode == "legacy" {
				result["info_hex"] = strings.Repeat("00", 60)
			}
			if request.Op == "status" {
				switch mode {
				case "status_extra":
					result["PRIVATE_SOURCE_PATH"] = "PRIVATE_SYMBOL"
				case "status_null":
					result["notifications"].(map[string]any)["events"] = nil
				case "status_case":
					result["Generation"] = result["generation"]
					delete(result, "generation")
				case "status_noncanonical":
					result["packet_count"] = "01"
				case "status_overflow":
					result["packet_count"] = "18446744073709551615"
				case "status_unknown_event":
					result["notifications"].(map[string]any)["events"] = []string{"PRIVATE_EVENT"}
				case "status_bad_info":
					result["info_hex"] = strings.Repeat("g", 128)
				}
			}
			response["result"] = result
			if request.Op == "status" && mode == "status_duplicate_key" {
				data, _ := json.Marshal(response)
				text := strings.Replace(string(data), `"generation":"1"`, `"generation":"9","generation":"1"`, 1)
				_, _ = io.WriteString(os.Stdout, text+"\n")
				continue
			}
			emit(response)
			if request.Op == "status" && mode == "status_duplicate" {
				emit(response)
			}
			if request.Op == "status" && mode == "status_exit" {
				return 0
			}
		case "read":
			var arguments ReadArgs
			if json.Unmarshal(request.Args, &arguments) != nil {
				return 9
			}
			readCount++
			if mode == "stale_read" && readCount == 1 {
				generation = 2
				emit(map[string]any{"v": 1, "seq": request.Seq, "ok": false, "error": map[string]string{"code": "stale_generation"}})
				continue
			}
			result := ReadResult{
				Generation: arguments.Generation, Start: arguments.Start,
				Count: arguments.Count, PacketsHex: make([]string, arguments.Count),
			}
			for index := range result.PacketsHex {
				result.PacketsHex[index] = strings.Repeat("00", 32)
			}
			switch mode {
			case "read_wrong_start":
				result.Start++
			case "read_wrong_generation":
				result.Generation++
			case "read_bad_packet":
				result.PacketsHex[0] = "bad"
			case "read_bad_count":
				result.Count++
			case "read_partial":
				result.Count = 1
				result.PacketsHex = result.PacketsHex[:1]
			case "read_empty":
				result.Count = 0
				result.PacketsHex = []string{}
			}
			response["result"] = result
			emit(response)
		case "start", "stop":
			result := ControlResult{Accepted: true, Generation: generation, TraceState: request.Op + "_requested"}
			if mode == "control_confirmed" {
				result.Confirmed = true
			}
			if mode == "generation_regressed" {
				result.Generation = 1
			}
			response["result"] = result
			emit(response)
		case "close":
			if mode == "close_error" {
				emit(map[string]any{"v": 1, "seq": request.Seq, "ok": false, "error": map[string]string{"code": "native_failure"}})
				return 4
			}
			if strings.HasPrefix(mode, "close_gated_") && !waitFakeCloseSignal("ack") {
				return 15
			}
			response["result"] = map[string]bool{"closed": true}
			emit(response)
			if mode == "close_gated_exit" {
				if !waitFakeCloseSignal("exit") {
					return 16
				}
				return 4
			}
			if mode == "close_stall" || mode == "close_gated_abort" {
				time.Sleep(30 * time.Second)
			}
			return 0
		default:
			return 10
		}
	}
}

func waitFakeCloseSignal(expected string) bool {
	deadline := time.Now().Add(5 * time.Second)
	path := os.Getenv(fakeCloseSignal)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(path); err == nil && string(data) == expected {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return false
}

func runJobParent() int {
	executable, err := os.Executable()
	if err != nil || os.Setenv(fakeHelperMode, "job_stubborn") != nil {
		return 11
	}
	client, err := Start(context.Background(), Options{
		Python: executable, Script: executable, Installation: filepath.Dir(executable),
		ExpectedPointerBits: 64, ExpectedInfoSize: 64,
	})
	if err != nil {
		if client != nil {
			client.Abort()
			_ = client.Wait(context.Background())
		}
		return 12
	}
	if json.NewEncoder(os.Stdout).Encode(map[string]int{"pid": client.command.Process.Pid}) != nil {
		return 13
	}
	if _, err := bufio.NewReader(os.Stdin).ReadString('\n'); err != nil {
		return 14
	}
	// Deliberately no Close/Abort/defer. TestMain calls os.Exit immediately;
	// only the Windows kernel's final job-handle close owns child teardown.
	return 0
}

func fakeStatus(generation uint64) map[string]any {
	return map[string]any{
		"info_hex":     strings.Repeat("00", 64),
		"packet_count": "18446744073709551614",
		"generation":   strconv.FormatUint(generation, 10),
		"trace_state":  "unknown",
		"notifications": map[string]any{
			"events": []string{"decoder_replacement"}, "dropped": 0, "ignored_callbacks": 0,
			"callback_failed": false, "generation_invalidated": false,
		},
	}
}

func fakeOptions(t *testing.T) Options {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(t.TempDir(), "helper.py")
	if err := os.WriteFile(script, []byte("# fake helper; the test executable does not interpret it\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return Options{
		Python: executable, Script: script, Installation: filepath.Dir(executable),
		ExpectedPointerBits: 64, ExpectedInfoSize: 64,
		StartupTimeout: 2 * time.Second, RPCTimeout: 2 * time.Second, ShutdownTimeout: 2 * time.Second,
	}
}

func startFake(t *testing.T, mode string) *Client {
	t.Helper()
	t.Setenv(fakeHelperMode, mode)
	client, err := Start(context.Background(), fakeOptions(t))
	trackClient(t, client)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func trackClient(t *testing.T, client *Client) {
	t.Helper()
	if client == nil {
		return
	}
	t.Cleanup(func() {
		client.Abort()
		if err := client.Wait(context.Background()); err != nil {
			t.Errorf("helper was not reaped: %v", err)
		}
	})
}

func openFake(t *testing.T, client *Client) Status {
	t.Helper()
	status, err := client.Open(context.Background(), OpenArgs{
		Installation: client.options.Installation, MultiVersion: "8.1.4",
		DLLSHA256: strings.Repeat("1a", 32), SessionID: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	return status
}

func waitPending(t *testing.T, client *Client, operation string) {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		client.mu.Lock()
		pending := client.pending != nil && client.pending.op == operation
		client.mu.Unlock()
		if pending {
			return
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatal("request was not admitted")
		}
	}
}

func TestHelperTypedRoundTripAndCleanReaping(t *testing.T) {
	client := startFake(t, "happy")
	if _, err := client.Status(context.Background()); !errors.Is(err, ErrNotOpen) || client.Poisoned() {
		t.Fatalf("unopened status = %v", err)
	}
	initial := openFake(t, client)
	if initial.PacketCount != math.MaxUint64-1 || initial.Generation != 1 ||
		len(initial.InfoHex) != 128 || initial.Notifications.Events[0] != "decoder_replacement" {
		t.Fatalf("status = %#v", initial)
	}
	if _, err := client.Open(context.Background(), OpenArgs{
		Installation: client.options.Installation, MultiVersion: "8.1.4", DLLSHA256: strings.Repeat("ab", 32), SessionID: 1,
	}); !errors.Is(err, ErrAlreadyOpen) {
		t.Fatalf("second open = %v", err)
	}
	read, err := client.Read(context.Background(), ReadArgs{Start: 9007199254740993, Count: 2, Generation: 1})
	if err != nil || read.Start != 9007199254740993 || read.Count != 2 || len(read.PacketsHex) != 2 {
		t.Fatalf("read = %#v, %v", read, err)
	}
	if _, err := client.StartTrace(context.Background(), false); !errors.Is(err, ErrNotAuthorized) || client.Poisoned() {
		t.Fatalf("unauthorized start = %v", err)
	}
	start, err := client.StartTrace(context.Background(), true)
	if err != nil || !start.Accepted || start.Confirmed || start.TraceState != "start_requested" {
		t.Fatalf("start = %#v, %v", start, err)
	}
	stop, err := client.StopTrace(context.Background(), true)
	if err != nil || stop.Confirmed || stop.TraceState != "stop_requested" {
		t.Fatalf("stop = %#v, %v", stop, err)
	}
	if err := client.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if client.Poisoned() || client.Alive() || client.command.ProcessState == nil || !client.command.ProcessState.Success() {
		t.Fatal("clean Close did not establish successful child exit")
	}
	select {
	case <-client.Done():
	default:
		t.Fatal("Close returned before reaping")
	}
	if err := client.Close(context.Background()); err != nil {
		t.Fatalf("repeated close = %v", err)
	}
	if _, err := client.Status(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatalf("status after close = %v", err)
	}
}

type closeWaitContext struct {
	context.Context
	mu      sync.Mutex
	calls   int
	waitAt  int
	entered chan struct{}
	waiting chan struct{}
}

func (c *closeWaitContext) Deadline() (time.Time, bool) {
	c.mu.Lock()
	c.calls++
	if c.calls == 1 {
		close(c.entered)
	}
	if c.calls == c.waitAt {
		close(c.waiting)
	}
	c.mu.Unlock()
	return c.Context.Deadline()
}

func TestConcurrentCloseDoesNotEquateReapingWithSuccess(t *testing.T) {
	for _, branch := range []string{"already_closed", "queued"} {
		for _, failure := range []string{"abort", "exit"} {
			t.Run(branch+"/"+failure, func(t *testing.T) {
				signal := filepath.Join(t.TempDir(), "close-stage")
				t.Setenv(fakeCloseSignal, signal)
				client := startFake(t, "close_gated_"+failure)
				openFake(t, client)
				first, second := make(chan error, 1), make(chan error, 1)
				go func() { first <- client.Close(context.Background()) }()
				waitPending(t, client, "close")
				probe := &closeWaitContext{
					Context: context.Background(), waitAt: 1,
					entered: make(chan struct{}), waiting: make(chan struct{}),
				}
				if branch == "queued" {
					// One deadline belongs to call admission, the next to
					// the idempotent completion wait after ErrClosed.
					probe.waitAt = 2
					go func() { second <- client.Close(probe) }()
					select {
					case <-probe.entered:
					case <-time.After(2 * time.Second):
						t.Fatal("second Close did not enter admission")
					}
				}
				if err := os.WriteFile(signal, []byte("ack"), 0o600); err != nil {
					t.Fatal(err)
				}
				if branch == "already_closed" {
					deadline := time.Now().Add(2 * time.Second)
					for {
						client.mu.Lock()
						closed := client.closed
						client.mu.Unlock()
						if closed {
							break
						}
						if time.Now().After(deadline) {
							t.Fatal("first Close did not accept its acknowledgement")
						}
						time.Sleep(time.Millisecond)
					}
					go func() { second <- client.Close(probe) }()
				}
				select {
				case <-probe.waiting:
				case <-time.After(2 * time.Second):
					t.Fatal("second Close did not wait for acknowledged helper exit")
				}
				if failure == "abort" {
					client.Abort()
				} else if err := os.WriteFile(signal, []byte("exit"), 0o600); err != nil {
					t.Fatal(err)
				}
				for index, result := range []<-chan error{first, second} {
					select {
					case err := <-result:
						if !errors.Is(err, ErrPoisoned) {
							t.Fatalf("Close caller %d falsely accepted reaping: %v", index+1, err)
						}
					case <-time.After(3 * time.Second):
						t.Fatalf("Close caller %d remained blocked", index+1)
					}
				}
				if err := client.Wait(context.Background()); err != nil {
					t.Fatalf("owned helper was not reaped: %v", err)
				}
			})
		}
	}
}

func TestLegacyProfileHasItsOwnRuntimeAndInformationContract(t *testing.T) {
	t.Setenv(fakeHelperMode, "legacy")
	options := fakeOptions(t)
	options.ExpectedPointerBits, options.ExpectedInfoSize = 32, 60
	client, err := Start(context.Background(), options)
	trackClient(t, client)
	if err != nil {
		t.Fatal(err)
	}
	status, err := client.Open(context.Background(), OpenArgs{
		Installation: options.Installation, MultiVersion: "7.1.6d",
		DLLSHA256: strings.Repeat("ab", 32), SessionID: 4,
	})
	if err != nil || len(status.InfoHex) != 120 {
		t.Fatalf("legacy status = %#v, %v", status, err)
	}
	if err := client.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestStartupFailuresAreBoundedAndSanitized(t *testing.T) {
	for _, mode := range []string{"ready_timeout", "early_exit", "ready_arch", "ready_python", "ready_bad", "ready_oversize", "ready_partial_eof", "ready_fatal"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv(fakeHelperMode, mode)
			options := fakeOptions(t)
			if mode == "ready_timeout" {
				options.StartupTimeout = 40 * time.Millisecond
			}
			started := time.Now()
			client, err := Start(context.Background(), options)
			trackClient(t, client)
			if client == nil || err == nil || !client.Poisoned() ||
				!errors.Is(err, ErrPoisoned) || time.Since(started) > 4*time.Second {
				t.Fatalf("startup %s = %v, %v", mode, client, err)
			}
			if err := client.Wait(context.Background()); err != nil || client.Alive() ||
				client.command.ProcessState == nil {
				t.Fatalf("failed startup did not retain reaped ownership: %v", err)
			}
			if strings.Contains(err.Error(), "PRIVATE") || strings.Contains(err.Error(), options.Python) {
				t.Fatal("startup failure leaked subprocess data")
			}
			if mode == "ready_timeout" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("startup deadline cause lost: %v", err)
			}
		})
	}
}

func TestFragmentedReadyAndDiscardedStderr(t *testing.T) {
	for _, mode := range []string{"fragmented", "stderr_flood"} {
		t.Run(mode, func(t *testing.T) {
			client := startFake(t, mode)
			openFake(t, client)
			if err := client.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestProtocolAndRemoteFailuresPoisonAndReap(t *testing.T) {
	for _, mode := range []string{
		"status_wrong_seq", "status_fatal", "fatal_stale", "stale_status",
		"status_unknown_code", "status_partial_eof", "status_extra", "status_null",
		"status_case", "status_noncanonical", "status_overflow", "status_unknown_event",
		"status_bad_info", "status_duplicate_key",
	} {
		t.Run(mode, func(t *testing.T) {
			client := startFake(t, mode)
			openFake(t, client)
			result, err := client.Status(context.Background())
			if err == nil || !client.Poisoned() || !errors.Is(err, ErrPoisoned) || errors.Is(err, ErrStaleGeneration) ||
				result.InfoHex != "" || result.Generation != 0 {
				t.Fatalf("status %s = %#v, %v", mode, result, err)
			}
			if strings.Contains(err.Error(), "PRIVATE") {
				t.Fatal("failure leaked helper content")
			}
			if err := client.Wait(context.Background()); err != nil || client.Alive() {
				t.Fatalf("poisoned helper was not reaped: %v", err)
			}
			if _, err := client.Status(context.Background()); !errors.Is(err, ErrPoisoned) {
				t.Fatalf("poisoned helper was reused: %v", err)
			}
		})
	}
}

func TestDuplicateOrUnexpectedExitCannotBecomeNextResponse(t *testing.T) {
	for _, mode := range []string{"status_duplicate", "status_exit"} {
		t.Run(mode, func(t *testing.T) {
			client := startFake(t, mode)
			openFake(t, client)
			_, _ = client.Status(context.Background())
			if err := client.Wait(context.Background()); err != nil || !client.Poisoned() {
				t.Fatalf("unexpected lifetime end = %v, poisoned=%v", err, client.Poisoned())
			}
			if _, err := client.Status(context.Background()); !errors.Is(err, ErrPoisoned) {
				t.Fatalf("unexpected extra traffic reused: %v", err)
			}
		})
	}
}

func TestReadGenerationFailureIsTheOnlyRecoverableRemoteError(t *testing.T) {
	client := startFake(t, "stale_read")
	openFake(t, client)
	result, err := client.Read(context.Background(), ReadArgs{Start: 0, Count: 1, Generation: 1})
	if !errors.Is(err, ErrStaleGeneration) || errors.Is(err, ErrPoisoned) || client.Poisoned() || len(result.PacketsHex) != 0 {
		t.Fatalf("stale read = %#v, %v", result, err)
	}
	status, err := client.Status(context.Background())
	if err != nil || status.Generation != 2 {
		t.Fatalf("fresh status = %#v, %v", status, err)
	}
	result, err = client.Read(context.Background(), ReadArgs{Start: 0, Count: 1, Generation: 2})
	if err != nil || result.Generation != 2 || result.Count != 1 {
		t.Fatalf("fresh read = %#v, %v", result, err)
	}
}

func TestReadResultsAreBoundedAndRequestBound(t *testing.T) {
	for _, mode := range []string{"read_wrong_start", "read_wrong_generation", "read_bad_packet", "read_bad_count"} {
		t.Run(mode, func(t *testing.T) {
			client := startFake(t, mode)
			openFake(t, client)
			result, err := client.Read(context.Background(), ReadArgs{Start: 0, Count: 2, Generation: 1})
			if !errors.Is(err, ErrProtocol) || !client.Poisoned() || len(result.PacketsHex) != 0 {
				t.Fatalf("read = %#v, %v", result, err)
			}
		})
	}
	for _, mode := range []string{"read_partial", "read_empty"} {
		t.Run(mode, func(t *testing.T) {
			client := startFake(t, mode)
			openFake(t, client)
			result, err := client.Read(context.Background(), ReadArgs{Start: 0, Count: 2, Generation: 1})
			want := 1
			if mode == "read_empty" {
				want = 0
			}
			if err != nil || result.Count != want || len(result.PacketsHex) != want || client.Poisoned() {
				t.Fatalf("bounded partial result = %#v, %v", result, err)
			}
		})
	}
}

func TestControlAcknowledgementCannotClaimConfirmationOrRegressGeneration(t *testing.T) {
	for _, mode := range []string{"control_confirmed", "generation_regressed"} {
		t.Run(mode, func(t *testing.T) {
			client := startFake(t, mode)
			openFake(t, client)
			result, err := client.StartTrace(context.Background(), true)
			if !errors.Is(err, ErrProtocol) || !client.Poisoned() || result.Accepted {
				t.Fatalf("control = %#v, %v", result, err)
			}
		})
	}
}

func TestCancellationBeforeDispatchDoesNotPoison(t *testing.T) {
	client := startFake(t, "status_delay")
	openFake(t, client)
	first := make(chan error, 1)
	go func() { _, err := client.Status(context.Background()); first <- err }()
	waitPending(t, client, "status")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := client.Status(ctx); !errors.Is(err, context.DeadlineExceeded) || client.Poisoned() {
		t.Fatalf("queued cancellation = %v, poisoned=%v", err, client.Poisoned())
	}
	if err := <-first; err != nil || client.Poisoned() {
		t.Fatalf("first request was affected: %v", err)
	}
	cancelled, stop := context.WithCancel(context.Background())
	stop()
	if _, err := client.Status(cancelled); !errors.Is(err, context.Canceled) || client.Poisoned() {
		t.Fatalf("pre-cancelled request = %v", err)
	}
	if _, err := client.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestInFlightTimeoutPermanentlyPoisons(t *testing.T) {
	client := startFake(t, "status_late")
	openFake(t, client)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := client.Status(ctx); !errors.Is(err, context.DeadlineExceeded) ||
		!errors.Is(err, ErrPoisoned) || !client.Poisoned() {
		t.Fatalf("in-flight timeout = %v", err)
	}
	if err := client.Wait(context.Background()); err != nil || client.Alive() {
		t.Fatalf("timed-out helper not reaped: %v", err)
	}
	if _, err := client.Status(context.Background()); !errors.Is(err, ErrPoisoned) {
		t.Fatalf("late response was reusable: %v", err)
	}
}

func TestConfiguredRPCDeadlineAppliesToBackgroundContext(t *testing.T) {
	client := startFake(t, "status_timeout")
	openFake(t, client)
	client.options.RPCTimeout = 30 * time.Millisecond
	started := time.Now()
	if _, err := client.Status(context.Background()); !errors.Is(err, context.DeadlineExceeded) ||
		!client.Poisoned() || time.Since(started) > time.Second {
		t.Fatalf("default-bounded request = %v", err)
	}
}

func TestAbortDoesNotWaitForTheCallGate(t *testing.T) {
	client := startFake(t, "status_timeout")
	openFake(t, client)
	called := make(chan error, 1)
	go func() { _, err := client.Status(context.Background()); called <- err }()
	waitPending(t, client, "status")
	started := time.Now()
	client.Abort()
	if time.Since(started) > 100*time.Millisecond || !client.Poisoned() {
		t.Fatal("Abort waited for native RPC or failed to invalidate admission")
	}
	if err := <-called; !errors.Is(err, ErrPoisoned) {
		t.Fatalf("in-flight Abort = %v", err)
	}
	if err := client.Wait(context.Background()); err != nil {
		t.Fatalf("Abort did not reap the child: %v", err)
	}
}

func TestConcurrentRequestsStaySingleFlight(t *testing.T) {
	client := startFake(t, "happy")
	openFake(t, client)
	var wait sync.WaitGroup
	errors := make(chan error, 20)
	for index := 0; index < 20; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := client.Status(context.Background())
			errors <- err
		}()
	}
	wait.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	client.mu.Lock()
	sequence := client.nextSeq
	client.mu.Unlock()
	if sequence != 21 || client.Poisoned() {
		t.Fatalf("sequence = %d, poisoned=%v", sequence, client.Poisoned())
	}
}

func TestLocalArgumentValidationDoesNotDispatchOrPoison(t *testing.T) {
	client := startFake(t, "happy")
	for _, args := range []OpenArgs{
		{Installation: client.options.Installation, MultiVersion: "8.1.4", DLLSHA256: "bad", SessionID: 1},
		{Installation: client.options.Installation, MultiVersion: "8.1.4", DLLSHA256: strings.Repeat("0", 64), SessionID: 0},
		{Installation: client.options.Installation, MultiVersion: "8.1.4", DLLSHA256: strings.Repeat("0", 64), SessionID: math.MaxUint32},
		{Installation: client.options.Installation, MultiVersion: "latest", DLLSHA256: strings.Repeat("0", 64), SessionID: 1},
		{Installation: t.TempDir(), MultiVersion: "8.1.4", DLLSHA256: strings.Repeat("0", 64), SessionID: 1},
		{Installation: client.options.Installation, MultiVersion: "7.1.6d", DLLSHA256: strings.Repeat("0", 64), SessionID: 1},
	} {
		if _, err := client.Open(context.Background(), args); !errors.Is(err, ErrInvalidArguments) || client.Poisoned() {
			t.Fatalf("invalid open = %v", err)
		}
	}
	openFake(t, client)
	for _, args := range []ReadArgs{
		{Start: 0, Count: 0, Generation: 1}, {Start: 0, Count: MaxPacketBatch + 1, Generation: 1},
		{Start: 0, Count: 1, Generation: 0}, {Start: math.MaxUint64, Count: 1, Generation: 1},
	} {
		if _, err := client.Read(context.Background(), args); !errors.Is(err, ErrInvalidArguments) || client.Poisoned() {
			t.Fatalf("invalid read = %v", err)
		}
	}
	client.mu.Lock()
	sequence := client.nextSeq
	client.mu.Unlock()
	if sequence != 1 {
		t.Fatalf("rejected requests advanced sequence to %d", sequence)
	}
}

func TestCloseFailureAndNonExitingCloseRemainPoisoned(t *testing.T) {
	for _, mode := range []string{"close_error", "close_stall"} {
		t.Run(mode, func(t *testing.T) {
			client := startFake(t, mode)
			openFake(t, client)
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			if err := client.Close(ctx); err == nil || !client.Poisoned() {
				t.Fatalf("unconfirmed close = %v", err)
			}
			if err := client.Wait(context.Background()); err != nil {
				t.Fatalf("failed close did not reap: %v", err)
			}
		})
	}
}

func TestOptionsRequireAbsoluteRegularContainedPaths(t *testing.T) {
	base := fakeOptions(t)
	cases := []struct {
		name   string
		change func(*Options)
	}{
		{"relative interpreter", func(o *Options) { o.Python = "python.exe" }},
		{"relative script", func(o *Options) { o.Script = "helper.py" }},
		{"relative install", func(o *Options) { o.Installation = "." }},
		{"escaped interpreter", func(o *Options) { o.Installation = t.TempDir() }},
		{"directory script", func(o *Options) { o.Script = filepath.Dir(base.Script) }},
		{"missing interpreter", func(o *Options) { o.Python = filepath.Join(o.Installation, "PRIVATE_MISSING.exe") }},
		{"wrong pointer", func(o *Options) { o.ExpectedPointerBits = 16 }},
		{"wrong info", func(o *Options) { o.ExpectedInfoSize = 60 }},
		{"negative deadline", func(o *Options) { o.RPCTimeout = -time.Second }},
		{"unbounded deadline", func(o *Options) { o.StartupTimeout = time.Hour }},
		{"null inherited handle", func(o *Options) { o.InheritedHandles = []uintptr{0} }},
		{"duplicate inherited handle", func(o *Options) { o.InheritedHandles = []uintptr{123, 123} }},
		{"pseudo process handle", func(o *Options) { o.InheritedHandles = []uintptr{^uintptr(0)} }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			options := base
			test.change(&options)
			client, err := Start(context.Background(), options)
			trackClient(t, client)
			if client != nil || !errors.Is(err, ErrInvalidOptions) ||
				strings.Contains(err.Error(), "PRIVATE") {
				t.Fatalf("invalid options = %v, %v", client, err)
			}
		})
	}
	options := base
	options.StartupTimeout, options.RPCTimeout, options.ShutdownTimeout = 0, 0, 0
	got, err := validateOptions(options)
	if err != nil || got.StartupTimeout <= 0 || got.RPCTimeout <= 0 || got.ShutdownTimeout <= 0 {
		t.Fatalf("default deadlines = %#v, %v", got, err)
	}
}

func TestInterpreterLinkCannotEscapeInstallation(t *testing.T) {
	base := fakeOptions(t)
	installation := t.TempDir()
	link := filepath.Join(installation, "python.exe")
	if err := os.Symlink(base.Python, link); err != nil {
		t.Skip("symlink creation unavailable on this host")
	}
	base.Python, base.Installation = link, installation
	if _, err := validateOptions(base); !errors.Is(err, ErrInvalidOptions) {
		t.Fatalf("escaping interpreter link accepted: %v", err)
	}
}

func TestReadyProcessCreationFailureIsSanitized(t *testing.T) {
	options := fakeOptions(t)
	options.Python = filepath.Join(options.Installation, "private-invalid-image.exe")
	if err := os.WriteFile(options.Python, []byte("not an executable"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(options.Python) })
	client, err := Start(context.Background(), options)
	trackClient(t, client)
	if client != nil || err == nil ||
		strings.Contains(err.Error(), options.Python) || strings.Contains(err.Error(), "private-invalid-image") {
		t.Fatalf("invalid process image = %v, %v", client, err)
	}
}

func TestWaitAloneDoesNotAbortAnAliveHelper(t *testing.T) {
	client := startFake(t, "happy")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	if err := client.Wait(ctx); !errors.Is(err, context.DeadlineExceeded) || client.Poisoned() || !client.Alive() {
		t.Fatalf("observation wait = %v, poisoned=%v, alive=%v", err, client.Poisoned(), client.Alive())
	}
}

func TestUnfinishedStartupCleanupIsNotReportedAsReaped(t *testing.T) {
	client := &Client{
		options: Options{ShutdownTimeout: time.Millisecond},
		done:    make(chan struct{}),
	}
	err := client.failedStartError(context.DeadlineExceeded)
	if !errors.Is(err, ErrCleanupUnconfirmed) || !errors.Is(err, context.DeadlineExceeded) ||
		!errors.Is(err, ErrPoisoned) {
		t.Fatalf("cleanup uncertainty was lost: %v", err)
	}
}

type refusingOwner struct {
	once     sync.Once
	closes   int
	assigned *os.Process
}

func (o *refusingOwner) Assign(process *os.Process) error {
	o.assigned = process
	return errors.New("PRIVATE_ASSIGNMENT_ERROR")
}

func (o *refusingOwner) Close() error {
	o.once.Do(func() { o.closes++ })
	return nil
}

func TestOwnershipAssignmentFailureKillsAndReapsBeforeAdmission(t *testing.T) {
	t.Setenv(fakeHelperMode, "job_stubborn")
	options, err := validateOptions(fakeOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(options.Python, "-I", "-S", "-B", "-u", options.Script)
	configureProcess(command)
	owner := &refusingOwner{}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	client, err := startOwnedCommand(ctx, options, command, owner)
	trackClient(t, client)
	if client == nil || err == nil || !client.Poisoned() || !errors.Is(err, ErrPoisoned) ||
		strings.Contains(err.Error(), "PRIVATE") {
		t.Fatalf("unowned startup = %v, %v", client, err)
	}
	if owner.assigned == nil || command.ProcessState == nil ||
		owner.closes != 1 {
		t.Fatal("assignment failure did not close/reap only the attempted child")
	}
}

type delayedCloseOwner struct {
	refusingOwner
	reject  bool
	started chan struct{}
	release chan struct{}
}

func (o *delayedCloseOwner) Assign(process *os.Process) error {
	o.assigned = process
	if o.reject {
		return errors.New("PRIVATE_ASSIGNMENT_ERROR")
	}
	return nil
}

func (o *delayedCloseOwner) Close() error {
	o.once.Do(func() {
		close(o.started)
		<-o.release
		o.closes++
	})
	return nil
}

func TestFailedStartupRetainsOwnershipUntilDelayedCleanupCompletes(t *testing.T) {
	for _, test := range []struct {
		name, mode string
		reject     bool
		cause      error
	}{
		{"assignment", "job_stubborn", true, ErrTransport},
		{"protocol", "ready_bad", false, ErrProtocol},
		{"deadline", "ready_timeout", false, context.DeadlineExceeded},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv(fakeHelperMode, test.mode)
			options, err := validateOptions(fakeOptions(t))
			if err != nil {
				t.Fatal(err)
			}
			options.ShutdownTimeout = 20 * time.Millisecond
			command := exec.Command(options.Python, "-I", "-S", "-B", "-u", options.Script)
			configureProcess(command)
			owner := &delayedCloseOwner{
				reject: test.reject, started: make(chan struct{}), release: make(chan struct{}),
			}
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(owner.release) }) }
			var client *Client
			t.Cleanup(func() {
				release()
				if client != nil {
					client.Abort()
					select {
					case <-client.Done():
						if err := client.Wait(context.Background()); err != nil {
							t.Errorf("failed startup cleanup remained unconfirmed: %v", err)
						}
					case <-time.After(2 * time.Second):
						t.Error("failed startup lost its child during cleanup")
					}
				}
			})
			timeout := 2 * time.Second
			if test.mode == "ready_timeout" {
				timeout = 40 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			started := time.Now()
			client, err = startOwnedCommand(ctx, options, command, owner)
			if client == nil || err == nil || !client.Poisoned() ||
				!errors.Is(err, ErrPoisoned) || !errors.Is(err, ErrCleanupUnconfirmed) ||
				!errors.Is(err, test.cause) || time.Since(started) > 3*time.Second {
				t.Fatalf("startup did not return bounded, retained ownership: %v, %v", client, err)
			}
			if strings.Contains(err.Error(), "PRIVATE") || owner.assigned != command.Process {
				t.Fatal("startup leaked an error or lost the exact spawned process")
			}
			select {
			case <-owner.started:
			default:
				t.Fatal("startup did not initiate owned process cleanup")
			}
			select {
			case <-client.Done():
				t.Fatal("startup reported completion while cleanup was still blocked")
			default:
			}
			if err := client.Wait(context.Background()); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("unfinished cleanup was reported as reaped: %v", err)
			}
			if _, err := client.Status(context.Background()); !errors.Is(err, ErrPoisoned) {
				t.Fatalf("failed startup admitted an RPC: %v", err)
			}
			aborted := make(chan struct{})
			go func() { client.Abort(); close(aborted) }()
			select {
			case <-aborted:
			case <-time.After(time.Second):
				t.Fatal("Abort waited for blocked process cleanup")
			}
			release()
			select {
			case <-client.Done():
			case <-time.After(2 * time.Second):
				t.Fatal("retained failed-start client did not observe actual reaping")
			}
			if err := client.Wait(context.Background()); err != nil || client.Alive() ||
				command.ProcessState == nil || owner.closes != 1 || !client.Poisoned() {
				t.Fatalf("failed startup did not finish cleanup exactly once: %v", err)
			}
		})
	}
}

// Opt-in startup/EOF interoperability only: this test never sends Open and
// therefore never loads a vendor DLL or contacts a trace session.
func TestNoNativeStartupSmoke(t *testing.T) {
	python := os.Getenv("MULTI_DAP_TRACE_HELPER_PYTHON")
	script := os.Getenv("MULTI_DAP_TRACE_HELPER_SCRIPT")
	installation := os.Getenv("MULTI_DAP_TRACE_HELPER_INSTALLATION")
	if python == "" || script == "" || installation == "" {
		t.Skip("explicit no-native helper smoke paths are not configured")
	}
	client, err := Start(context.Background(), Options{
		Python: python, Script: script, Installation: installation,
		ExpectedPointerBits: 64, ExpectedInfoSize: 64,
	})
	trackClient(t, client)
	if err != nil {
		t.Fatal(err)
	}
	if client.Poisoned() || !client.Alive() || client.opened || client.nextSeq != 0 {
		t.Fatal("startup unexpectedly acquired a native session")
	}
	if err := client.input.Close(); err != nil {
		t.Fatal(err)
	}
	if err := client.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if client.Alive() {
		t.Fatal("EOF did not terminate the helper")
	}
}

func ExampleReadArgs() {
	data, _ := json.Marshal(ReadArgs{Start: 9007199254740993, Count: 1, Generation: 2})
	fmt.Println(string(data))
	// Output: {"start":"9007199254740993","count":1,"generation":"2"}
}
