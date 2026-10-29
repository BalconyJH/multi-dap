package perf

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Tacrolimus/multi-dap/internal/dap"
)

func wire(body string) []byte {
	return []byte(fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(body), body))
}

func requestWire(sequence int, command string) []byte {
	return wire(fmt.Sprintf(`{"seq":%d,"type":"request","command":%q}`, sequence, command))
}

func responseWire(sequence, requestSequence int, command string, success bool) []byte {
	return wire(fmt.Sprintf(`{"seq":%d,"type":"response","request_seq":%d,"command":%q,"success":%t}`,
		sequence, requestSequence, command, success))
}

func eventWire(sequence int, event string) []byte {
	return wire(fmt.Sprintf(`{"seq":%d,"type":"event","event":%q}`, sequence, event))
}

func write(t *testing.T, writer io.Writer, data []byte) {
	t.Helper()
	written, err := writer.Write(data)
	if err != nil || written != len(data) {
		t.Fatalf("Write returned (%d, %v), want (%d, nil)", written, err, len(data))
	}
}

func recorderAt(t *testing.T, maxEvents int) (*Recorder, func(time.Duration)) {
	t.Helper()
	current := time.Unix(100, 0)
	recorder, err := newRecorder(maxEvents, func() time.Time { return current })
	if err != nil {
		t.Fatal(err)
	}
	return recorder, func(sinceStart time.Duration) { current = time.Unix(100, 0).Add(sinceStart) }
}

func dataEvents(trace Trace) []Event {
	var result []Event
	for _, event := range trace.TraceEvents {
		if event.Phase != "M" {
			result = append(result, event)
		}
	}
	return result
}

func TestNewRecorderLimits(t *testing.T) {
	for _, limit := range []int{-1, 0, MaxEvents + 1} {
		if _, err := NewRecorder(limit); err == nil {
			t.Errorf("NewRecorder(%d) accepted an invalid limit", limit)
		}
	}
	for _, limit := range []int{1, DefaultMaxEvents, MaxEvents} {
		if _, err := NewRecorder(limit); err != nil {
			t.Errorf("NewRecorder(%d): %v", limit, err)
		}
	}
}

func TestRecorderSplitFrames(t *testing.T) {
	request := requestWire(1, "threads")
	response := responseWire(1, 1, "threads", true)
	for split := 0; split <= len(request); split++ {
		t.Run(fmt.Sprint(split), func(t *testing.T) {
			recorder, at := recorderAt(t, 10)
			client, server := recorder.ClientWriter(), recorder.ServerWriter()
			at(time.Microsecond)
			write(t, client, request[:split])
			write(t, client, request[split:])
			at(5 * time.Microsecond)
			for _, b := range response {
				write(t, server, []byte{b})
			}
			at(8 * time.Microsecond)
			trace := recorder.Finish(false)
			events := dataEvents(trace)
			if len(events) != 1 || events[0].Name != "threads" || events[0].Phase != "X" ||
				events[0].Timestamp != 1 || events[0].Duration != 4 ||
				events[0].Args.Success == nil || !*events[0].Args.Success {
				t.Fatalf("events = %#v", events)
			}
			meta := trace.OtherData
			if !meta.CaptureComplete || meta.CompletedRequests != 1 || meta.ObservedRequests != 1 ||
				meta.RetainedEvents != 1 || meta.DurationUS != 8 || meta.FramingErrors != 0 {
				t.Fatalf("metadata = %#v", meta)
			}
		})
	}
}

func TestRecorderMergedFramesAndOutOfOrderResponses(t *testing.T) {
	recorder, at := recorderAt(t, 20)
	client, server := recorder.ClientWriter(), recorder.ServerWriter()
	at(time.Microsecond)
	write(t, client, requestWire(1, "attach"))
	at(3 * time.Microsecond)
	write(t, client, requestWire(2, "configurationDone"))
	at(6 * time.Microsecond)
	write(t, server, append(responseWire(1, 2, "configurationDone", true), eventWire(2, "stopped")...))
	at(9 * time.Microsecond)
	write(t, server, responseWire(3, 1, "attach", false))
	at(10 * time.Microsecond)
	trace := recorder.Finish(false)
	events := dataEvents(trace)
	if len(events) != 3 {
		t.Fatalf("events = %#v", events)
	}
	config, stopped, attach := events[0], events[1], events[2]
	if config.Name != "configurationDone" || config.Timestamp != 3 || config.Duration != 3 ||
		attach.Name != "attach" || attach.Timestamp != 1 || attach.Duration != 8 ||
		config.TID == attach.TID || attach.Args.Success == nil || *attach.Args.Success {
		t.Fatalf("request events = %#v / %#v", config, attach)
	}
	if stopped.Name != "stopped" || stopped.Phase != "i" || stopped.Scope != "t" ||
		stopped.Timestamp != 6 || stopped.TID == config.TID || stopped.TID == attach.TID {
		t.Fatalf("stopped = %#v", stopped)
	}
	meta := trace.OtherData
	if !meta.CaptureComplete || meta.CompletedRequests != 2 || meta.FailedRequests != 1 ||
		meta.ObservedEvents != 1 || len(meta.RequestSummary) != 2 {
		t.Fatalf("metadata = %#v", meta)
	}
	if meta.RequestSummary[0] != (RequestSummary{Command: "attach", Completed: 1, Failed: 1, TotalDurationUS: 8, MaxDurationUS: 8}) ||
		meta.RequestSummary[1] != (RequestSummary{Command: "configurationDone", Completed: 1, TotalDurationUS: 3, MaxDurationUS: 3}) {
		t.Fatalf("request summary = %#v", meta.RequestSummary)
	}
}

func TestRecorderMergedRequestsAndLaneReuse(t *testing.T) {
	recorder, _ := recorderAt(t, 20)
	client, server := recorder.ClientWriter(), recorder.ServerWriter()
	write(t, client, append(requestWire(1, "threads"), requestWire(2, "threads")...))
	write(t, server, responseWire(1, 1, "threads", true))
	write(t, client, requestWire(3, "threads"))
	write(t, server, append(responseWire(2, 2, "threads", true), responseWire(3, 3, "threads", true)...))
	trace := recorder.Finish(false)
	events := dataEvents(trace)
	if len(events) != 3 || events[0].TID == events[1].TID || events[0].TID != events[2].TID {
		t.Fatalf("events = %#v", events)
	}
	if len(trace.TraceEvents) != len(events)+4 {
		t.Fatalf("metadata count = %d, want 4", len(trace.TraceEvents)-len(events))
	}
}

func TestRecorderNeverExportsPayloadsOrUnrecognizedNames(t *testing.T) {
	recorder, _ := recorderAt(t, 20)
	client, server := recorder.ClientWriter(), recorder.ServerWriter()
	secrets := []string{
		"secret-token-value", "private-command-name", "private-event-name",
		`D:\\private\\project\\secret.elf`, "PRIVATE_SYMBOL", "private-diagnostic",
		"deadbeefcafefeed",
	}
	write(t, client, wire(`{"seq":1,"type":"request","command":"evaluate","arguments":{"expression":"PRIVATE_SYMBOL","token":"secret-token-value","path":"D:\\private\\project\\secret.elf"}}`))
	write(t, server, wire(`{"seq":1,"type":"response","request_seq":1,"command":"evaluate","success":false,"message":"private-diagnostic","body":{"result":"deadbeefcafefeed"}}`))
	write(t, client, requestWire(2, "private-command-name"))
	write(t, server, responseWire(2, 2, "private-command-name", true))
	write(t, server, eventWire(3, "private-event-name"))
	write(t, server, wire(`{"seq":4,"type":"event","event":"output","body":{"output":"secret-token-value PRIVATE_SYMBOL private-diagnostic"}}`))
	trace := recorder.Finish(false)
	encoded, err := json.Marshal(trace)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range secrets {
		if bytes.Contains(encoded, []byte(secret)) {
			t.Errorf("trace contains private value %q", secret)
		}
	}
	events := dataEvents(trace)
	if len(events) != 4 || events[0].Name != "evaluate" || events[1].Name != "unknown" ||
		events[2].Name != "unknown" || events[3].Name != "output" {
		t.Fatalf("events = %#v", events)
	}
	if !bytes.Contains(encoded, []byte(`"target_cpu_timing":false`)) ||
		!bytes.Contains(encoded, []byte(`"clock":"host-monotonic"`)) ||
		!bytes.Contains(encoded, []byte(`"success":false`)) {
		t.Fatalf("trace lacks evidence or failure fields: %s", encoded)
	}
}

func TestRecorderEventLimitDoesNotLimitSummaryOrAffectWrites(t *testing.T) {
	recorder, at := recorderAt(t, 1)
	client, server := recorder.ClientWriter(), recorder.ServerWriter()
	write(t, server, eventWire(1, "initialized"))
	at(time.Microsecond)
	write(t, client, requestWire(1, "threads"))
	at(4 * time.Microsecond)
	write(t, server, append(responseWire(2, 1, "threads", true), eventWire(3, "stopped")...))
	trace := recorder.Finish(false)
	if len(dataEvents(trace)) != 1 || trace.OtherData.RetainedEvents != 1 ||
		trace.OtherData.DroppedEvents != 2 || trace.OtherData.CompletedRequests != 1 ||
		trace.OtherData.CaptureComplete {
		t.Fatalf("trace = %#v", trace)
	}
	if len(trace.OtherData.RequestSummary) != 1 || trace.OtherData.RequestSummary[0].TotalDurationUS != 3 {
		t.Fatalf("summary = %#v", trace.OtherData.RequestSummary)
	}
}

func TestRecorderPendingLimitAndUnfinishedRequests(t *testing.T) {
	recorder, _ := recorderAt(t, 1000)
	client, server := recorder.ClientWriter(), recorder.ServerWriter()
	for sequence := 1; sequence <= maxPendingRequests+1; sequence++ {
		write(t, client, requestWire(sequence, "threads"))
	}
	if len(recorder.pending) != maxPendingRequests {
		t.Fatalf("pending count = %d", len(recorder.pending))
	}
	write(t, server, responseWire(1, maxPendingRequests+1, "threads", true))
	write(t, server, responseWire(2, 1, "threads", true))
	write(t, client, requestWire(maxPendingRequests+2, "threads"))
	write(t, server, responseWire(3, maxPendingRequests+2, "threads", true))
	trace := recorder.Finish(false)
	meta := trace.OtherData
	if meta.PendingLimitDrops != 1 || meta.UnmatchedResponses != 1 ||
		meta.UnfinishedRequests != maxPendingRequests-1 || meta.CompletedRequests != 2 ||
		meta.CaptureComplete {
		t.Fatalf("metadata = %#v", meta)
	}
}

func TestRecorderDuplicateSequenceCannotCorrelateAmbiguousResponses(t *testing.T) {
	recorder, _ := recorderAt(t, 20)
	client, server := recorder.ClientWriter(), recorder.ServerWriter()
	write(t, client, requestWire(1, "threads"))
	write(t, client, requestWire(1, "threads"))
	write(t, server, responseWire(1, 1, "threads", true))
	write(t, client, requestWire(2, "threads"))
	write(t, server, responseWire(2, 2, "threads", true))
	write(t, client, requestWire(2, "threads"))
	write(t, server, responseWire(3, 2, "threads", true))
	trace := recorder.Finish(false)
	meta := trace.OtherData
	if meta.DuplicateRequests != 1 || meta.OutOfOrderRequests != 1 ||
		meta.AbandonedRequests != 1 || meta.UnmatchedResponses != 2 ||
		meta.CompletedRequests != 1 || meta.CaptureComplete {
		t.Fatalf("metadata = %#v", meta)
	}
	events := dataEvents(trace)
	if len(events) != 1 || events[0].Args.RequestSeq != 2 {
		t.Fatalf("events = %#v", events)
	}
}

func TestRecorderUnmatchedAndMismatchedResponses(t *testing.T) {
	recorder, _ := recorderAt(t, 20)
	client, server := recorder.ClientWriter(), recorder.ServerWriter()
	write(t, client, requestWire(1, "threads"))
	write(t, server, responseWire(1, 2, "threads", true))
	write(t, server, responseWire(2, 1, "stackTrace", true))
	trace := recorder.Finish(false)
	meta := trace.OtherData
	if meta.UnmatchedResponses != 1 || meta.CommandMismatches != 1 ||
		meta.AbandonedRequests != 1 || meta.CompletedRequests != 0 ||
		meta.UnfinishedRequests != 0 || meta.CaptureComplete || len(dataEvents(trace)) != 0 {
		t.Fatalf("metadata = %#v, events = %#v", meta, trace.TraceEvents)
	}
}

func TestRecorderDistinctUnknownCommandsNeverCorrelate(t *testing.T) {
	recorder, _ := recorderAt(t, 20)
	write(t, recorder.ClientWriter(), requestWire(1, "secret-request-name"))
	write(t, recorder.ServerWriter(), responseWire(1, 1, "secret-other-name", true))
	trace := recorder.Finish(false)
	meta := trace.OtherData
	if meta.CommandMismatches != 1 || meta.AbandonedRequests != 1 ||
		meta.CompletedRequests != 0 || meta.UnfinishedRequests != 0 ||
		meta.CaptureComplete || len(dataEvents(trace)) != 0 {
		t.Fatalf("metadata = %#v", meta)
	}
	encoded, err := json.Marshal(trace)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"secret-request-name", "secret-other-name", "commandHash", "command_hash"} {
		if bytes.Contains(encoded, []byte(forbidden)) {
			t.Fatalf("private command identity leaked into trace: %q", forbidden)
		}
	}
}

func TestRecorderNamesFixedNativeTraceCommandsWithoutRetainingPayloads(t *testing.T) {
	commands := []string{
		"multi-dap/trace/open", "multi-dap/trace/poll", "multi-dap/trace/read",
		"multi-dap/trace/start", "multi-dap/trace/stop", "multi-dap/trace/close",
		"multi-dap/trace/status", "multi-dap/trace/PRIVATE_DYNAMIC_COMMAND",
	}
	recorder, _ := recorderAt(t, len(commands))
	for index, command := range commands {
		write(t, recorder.ClientWriter(), wire(fmt.Sprintf(
			`{"seq":%d,"type":"request","command":%q,"arguments":{"sourceId":"PRIVATE_SOURCE"}}`,
			index+1, command,
		)))
		write(t, recorder.ServerWriter(), wire(fmt.Sprintf(
			`{"seq":%d,"type":"response","request_seq":%d,"command":%q,"success":true,"body":{"packets":"PRIVATE_PACKETS"}}`,
			index+1, index+1, command,
		)))
	}
	trace := recorder.Finish(false)
	events := dataEvents(trace)
	if !trace.OtherData.CaptureComplete || len(events) != len(commands) {
		t.Fatal("native request observation was incomplete")
	}
	for index, event := range events {
		want := commands[index]
		if index == len(commands)-1 {
			want = "unknown"
		}
		if event.Name != want {
			t.Fatalf("native command label = %q, want %q", event.Name, want)
		}
	}
	encoded, err := json.Marshal(trace)
	if err != nil || bytes.Contains(encoded, []byte("PRIVATE")) {
		t.Fatal("native payload or dynamic command leaked into host observations")
	}
}

func TestRecorderFinishFreezesIndependentSnapshot(t *testing.T) {
	recorder, _ := recorderAt(t, 20)
	client, server := recorder.ClientWriter(), recorder.ServerWriter()
	write(t, client, requestWire(1, "threads"))
	write(t, server, responseWire(1, 1, "threads", true))
	first := recorder.Finish(true)
	original, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	first.TraceEvents[0].Args.Name = "caller mutation"
	first.OtherData.RequestSummary[0].Command = "caller mutation"
	for index := range first.TraceEvents {
		if first.TraceEvents[index].Args.Success != nil {
			*first.TraceEvents[index].Args.Success = false
		}
	}
	write(t, client, requestWire(2, "stackTrace"))
	write(t, server, responseWire(2, 2, "stackTrace", true))
	frozen := recorder.Finish(false)
	encoded, err := json.Marshal(frozen)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(encoded, original) || !frozen.OtherData.ProxyFailed || frozen.OtherData.CaptureComplete {
		t.Fatalf("Finish changed frozen snapshot: %s versus %s", encoded, original)
	}
}

func TestRecorderFractionalMicrosecondsAndMonotonicClamp(t *testing.T) {
	recorder, at := recorderAt(t, 20)
	client, server := recorder.ClientWriter(), recorder.ServerWriter()
	at(500 * time.Nanosecond)
	write(t, client, requestWire(1, "threads"))
	at(750 * time.Nanosecond)
	write(t, server, responseWire(1, 1, "threads", true))
	at(100 * time.Nanosecond)
	write(t, server, eventWire(2, "stopped"))
	at(time.Microsecond)
	trace := recorder.Finish(false)
	events := dataEvents(trace)
	if len(events) != 2 || events[0].Timestamp != 0.5 || events[0].Duration != 0.25 ||
		events[1].Timestamp != 0.75 || trace.OtherData.ClockRegressions != 1 || trace.OtherData.CaptureComplete {
		t.Fatalf("trace = %#v", trace)
	}
}

func TestRecorderConcurrentDirectionsAndFinish(t *testing.T) {
	recorder, err := NewRecorder(1000)
	if err != nil {
		t.Fatal(err)
	}
	client, server := recorder.ClientWriter(), recorder.ServerWriter()
	ready := make(chan int, 1)
	var writers sync.WaitGroup
	writers.Add(2)
	go func() {
		defer writers.Done()
		defer close(ready)
		for sequence := 1; sequence <= 300; sequence++ {
			if n, err := client.Write(requestWire(sequence, "threads")); err != nil || n == 0 {
				t.Errorf("client Write = %d, %v", n, err)
			}
			ready <- sequence
		}
	}()
	go func() {
		defer writers.Done()
		for sequence := range ready {
			if n, err := server.Write(responseWire(sequence, sequence, "threads", true)); err != nil || n == 0 {
				t.Errorf("server Write = %d, %v", n, err)
			}
		}
	}()
	var finishers sync.WaitGroup
	for index := 0; index < 5; index++ {
		finishers.Add(1)
		go func() {
			defer finishers.Done()
			for count := 0; count < 10; count++ {
				trace := recorder.Finish(false)
				if _, err := json.Marshal(trace); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	writers.Wait()
	finishers.Wait()
	first, _ := json.Marshal(recorder.Finish(false))
	second, _ := json.Marshal(recorder.Finish(true))
	if !bytes.Equal(first, second) {
		t.Fatal("concurrent Finish did not freeze one snapshot")
	}
}

func TestRecorderConcurrentDirectionsWithoutFinishLosesNoResponses(t *testing.T) {
	recorder, err := NewRecorder(1000)
	if err != nil {
		t.Fatal(err)
	}
	client, server := recorder.ClientWriter(), recorder.ServerWriter()
	ready := make(chan int)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for sequence := range ready {
			_, _ = server.Write(responseWire(sequence, sequence, "threads", true))
		}
	}()
	for sequence := 1; sequence <= 300; sequence++ {
		write(t, client, requestWire(sequence, "threads"))
		ready <- sequence
	}
	close(ready)
	<-done
	trace := recorder.Finish(false)
	if !trace.OtherData.CaptureComplete || trace.OtherData.CompletedRequests != 300 {
		t.Fatalf("metadata = %#v", trace.OtherData)
	}
}

func TestRecorderFramingErrorsDisableOnlyAffectedDirection(t *testing.T) {
	cases := map[string][]byte{
		"empty header":     []byte("\r\n\r\n"),
		"wrong header":     []byte("Secret-Header: private-diagnostic\r\n\r\n"),
		"duplicate":        []byte("Content-Length: 2\r\nContent-Length: 2\r\n\r\n{}"),
		"negative length":  []byte("Content-Length: -1\r\n\r\n"),
		"overflow length":  []byte("Content-Length: 18446744073709551616\r\n\r\n"),
		"oversize body":    []byte(fmt.Sprintf("Content-Length: %d\r\n\r\n", dap.DefaultMaxContentLength+1)),
		"zero body":        []byte("Content-Length: 0\r\n\r\n"),
		"invalid json":     wire(`{"private-diagnostic":`),
		"missing fields":   wire(`{}`),
		"null":             wire(`null`),
		"array":            wire(`[]`),
		"invalid sequence": wire(`{"seq":0,"type":"request","command":"threads"}`),
		"high sequence":    wire(`{"seq":2147483648,"type":"request","command":"threads"}`),
		"unknown type":     wire(`{"seq":1,"type":"private-diagnostic"}`),
		"empty command":    wire(`{"seq":1,"type":"request","command":""}`),
		"null success":     wire(`{"seq":1,"type":"response","request_seq":1,"command":"threads","success":null}`),
		"bad success":      wire(`{"seq":1,"type":"response","request_seq":1,"command":"threads","success":"private-diagnostic"}`),
		"missing success":  wire(`{"seq":1,"type":"response","request_seq":1,"command":"threads"}`),
		"empty event":      wire(`{"seq":1,"type":"event","event":""}`),
		"embedded lf":      []byte("Content-Length: 2\n\r\n\r\n{}"),
		"oversize header":  bytes.Repeat([]byte("x"), dap.DefaultMaxHeaderLength+1),
	}
	for name, invalid := range cases {
		t.Run(name, func(t *testing.T) {
			recorder, _ := recorderAt(t, 20)
			client, server := recorder.ClientWriter(), recorder.ServerWriter()
			write(t, client, invalid)
			write(t, client, requestWire(1, "threads"))
			write(t, server, eventWire(1, "stopped"))
			trace := recorder.Finish(false)
			meta := trace.OtherData
			if meta.FramingErrors != 1 || !meta.ClientObservationDisabled ||
				meta.ServerObservationDisabled || meta.ObservedRequests != 0 ||
				meta.ObservedEvents != 1 || meta.CaptureComplete {
				t.Fatalf("metadata = %#v", meta)
			}
			encoded, _ := json.Marshal(trace)
			if bytes.Contains(encoded, []byte("private-diagnostic")) {
				t.Fatal("invalid frame data leaked into trace")
			}
		})
	}
}

func TestRecorderPartialFrameAtFinish(t *testing.T) {
	for _, partial := range [][]byte{
		[]byte("Content-Len"),
		[]byte("Content-Length: 5\r\n\r\n"),
		[]byte("Content-Length: 5\r\n\r\n{}"),
		[]byte("Content-Length: 5\n\n"),
	} {
		recorder, _ := recorderAt(t, 20)
		write(t, recorder.ClientWriter(), requestWire(1, "threads"))
		write(t, recorder.ServerWriter(), partial)
		trace := recorder.Finish(false)
		if trace.OtherData.FramingErrors != 1 || !trace.OtherData.ServerObservationDisabled ||
			trace.OtherData.UnfinishedRequests != 1 || trace.OtherData.CaptureComplete {
			t.Fatalf("partial %q: %#v", partial, trace.OtherData)
		}
	}
}

func TestRecorderServerFramingFailureDoesNotDisableClient(t *testing.T) {
	recorder, _ := recorderAt(t, 20)
	write(t, recorder.ServerWriter(), wire(`{"seq":1,"type":"response","request_seq":1,"command":"threads","success":null}`))
	write(t, recorder.ClientWriter(), requestWire(1, "threads"))
	write(t, recorder.ServerWriter(), responseWire(2, 1, "threads", true))
	trace := recorder.Finish(false)
	meta := trace.OtherData
	if meta.FramingErrors != 1 || !meta.ServerObservationDisabled ||
		meta.ClientObservationDisabled || meta.ObservedRequests != 1 ||
		meta.UnfinishedRequests != 1 || meta.CaptureComplete {
		t.Fatalf("metadata = %#v", meta)
	}
}

func TestRecorderFramingMatchesAllowedHeaderBoundaries(t *testing.T) {
	recorder, _ := recorderAt(t, 20)
	body := `{"seq":1,"type":"request","command":"threads"}`
	header := fmt.Sprintf("content-length:\t%d\r\n\r\n", len(body))
	write(t, recorder.ClientWriter(), []byte(header+body))
	write(t, recorder.ServerWriter(), responseWire(1, 1, "threads", true))
	trace := recorder.Finish(false)
	if !trace.OtherData.CaptureComplete || trace.OtherData.CompletedRequests != 1 {
		t.Fatalf("metadata = %#v", trace.OtherData)
	}
	// Exactly the maximum header length is valid when its terminator fits.
	recorder, _ = recorderAt(t, 20)
	base := fmt.Sprintf("Content-Length: %d", len(body))
	header = base + strings.Repeat(" ", dap.DefaultMaxHeaderLength-len(base)-4) + "\r\n\r\n"
	write(t, recorder.ClientWriter(), []byte(header+body))
	write(t, recorder.ServerWriter(), responseWire(1, 1, "threads", true))
	if trace := recorder.Finish(false); !trace.OtherData.CaptureComplete {
		t.Fatalf("maximum-length header rejected: %#v", trace.OtherData)
	}
}

func TestRecorderBodyBoundAndConsumedPayloadRelease(t *testing.T) {
	recorder, _ := recorderAt(t, 20)
	prefix := `{"seq":1,"type":"request","command":"threads","arguments":{"data":"`
	suffix := `"}}`
	body := prefix + strings.Repeat("x", dap.DefaultMaxContentLength-len(prefix)-len(suffix)) + suffix
	write(t, recorder.ClientWriter(), wire(body))
	if recorder.client.body != nil || recorder.client.readingBody || len(recorder.client.header) != 0 {
		t.Fatal("completed body was retained")
	}
	write(t, recorder.ServerWriter(), responseWire(1, 1, "threads", true))
	trace := recorder.Finish(false)
	if !trace.OtherData.CaptureComplete || trace.OtherData.CompletedRequests != 1 {
		t.Fatalf("maximum-length body rejected: %#v", trace.OtherData)
	}
}

func TestRecorderReverseTrafficIsNotInventedAsHostRequest(t *testing.T) {
	recorder, _ := recorderAt(t, 20)
	write(t, recorder.ServerWriter(), requestWire(1, "runInTerminal"))
	write(t, recorder.ClientWriter(), responseWire(1, 1, "runInTerminal", true))
	trace := recorder.Finish(false)
	if trace.OtherData.IgnoredMessages != 2 || trace.OtherData.CompletedRequests != 0 ||
		len(dataEvents(trace)) != 0 || trace.OtherData.CaptureComplete {
		t.Fatalf("metadata = %#v", trace.OtherData)
	}
}

func FuzzRecorderTransparentBoundedWrites(f *testing.F) {
	f.Add(requestWire(1, "threads"), responseWire(1, 1, "threads", true), uint8(3))
	f.Add([]byte("Content-Length: 10000000000000000000000000000000000000000\r\n\r\n"), []byte{0, 1, 2}, uint8(1))
	f.Fuzz(func(t *testing.T, clientData, serverData []byte, chunk uint8) {
		recorder, _ := recorderAt(t, 4)
		for _, input := range []struct {
			writer io.Writer
			data   []byte
		}{{recorder.ClientWriter(), clientData}, {recorder.ServerWriter(), serverData}} {
			step := int(chunk) + 1
			for len(input.data) != 0 {
				take := min(step, len(input.data))
				write(t, input.writer, input.data[:take])
				input.data = input.data[take:]
			}
		}
		trace := recorder.Finish(false)
		if len(dataEvents(trace)) > 4 || len(trace.TraceEvents) > 4+maxPendingRequests+2 ||
			trace.OtherData.UnfinishedRequests > maxPendingRequests {
			t.Fatalf("recorder exceeded bounds: %#v", trace.OtherData)
		}
		if _, err := json.Marshal(trace); err != nil {
			t.Fatal(err)
		}
	})
}

// This measures host parser/correlation overhead, not target sampling or trace
// export. Reuse one retained slot so growth and sustained overflow do not skew it.
func BenchmarkRecorderRequestResponse(b *testing.B) {
	fixed := time.Unix(100, 0)
	recorder, err := newRecorder(1, func() time.Time { return fixed })
	if err != nil {
		b.Fatal(err)
	}
	client, server := recorder.ClientWriter(), recorder.ServerWriter()
	request := requestWire(1, "threads")
	response := responseWire(1, 1, "threads", true)
	_, _ = client.Write(request)
	_, _ = server.Write(response)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		recorder.lastRequestSeq = 0
		recorder.events = recorder.events[:0]
		_, _ = client.Write(request)
		_, _ = server.Write(response)
	}
}
