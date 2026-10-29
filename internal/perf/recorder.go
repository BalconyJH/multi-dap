// Package perf records a bounded, payload-free view of an existing DAP byte
// stream. Its timestamps describe observations at the host proxy, not target
// execution, CPU time, or time spent exclusively inside the debugger.
package perf

import (
	"crypto/sha256"
	"fmt"
	"io"
	"sort"
	"strconv"
	"sync"
	"time"
)

const (
	DefaultMaxEvents = 10000
	MaxEvents        = 100000

	maxPendingRequests = 256
	tracePID           = 1
	eventTID           = 1
)

// Event is the Chrome Trace event subset emitted by this recorder. All string
// fields are generated locally or selected from fixed allowlists.
type Event struct {
	Name      string    `json:"name"`
	Category  string    `json:"cat,omitempty"`
	Phase     string    `json:"ph"`
	Timestamp float64   `json:"ts"`
	Duration  float64   `json:"dur"`
	PID       int       `json:"pid"`
	TID       int       `json:"tid"`
	Scope     string    `json:"s,omitempty"`
	Args      EventArgs `json:"args"`
}

type EventArgs struct {
	Name       string `json:"name,omitempty"`
	RequestSeq int64  `json:"request_seq,omitempty"`
	Success    *bool  `json:"success,omitempty"`
}

// RequestSummary covers all correlated responses, including those whose
// timeline event could not be retained after the event limit was reached.
type RequestSummary struct {
	Command         string  `json:"command"`
	Completed       uint64  `json:"completed"`
	Failed          uint64  `json:"failed"`
	TotalDurationUS float64 `json:"total_duration_us"`
	MaxDurationUS   float64 `json:"max_duration_us"`
}

type Metadata struct {
	Schema                    string           `json:"schema"`
	Clock                     string           `json:"clock"`
	Evidence                  string           `json:"evidence"`
	TargetCPUTiming           bool             `json:"target_cpu_timing"`
	MaxEvents                 int              `json:"max_events"`
	RetainedEvents            int              `json:"retained_events"`
	DroppedEvents             uint64           `json:"dropped_events"`
	DurationUS                float64          `json:"duration_us"`
	ObservedRequests          uint64           `json:"observed_requests"`
	CompletedRequests         uint64           `json:"completed_requests"`
	FailedRequests            uint64           `json:"failed_requests"`
	UnfinishedRequests        int              `json:"unfinished_requests"`
	PendingLimitDrops         uint64           `json:"pending_limit_drops"`
	DuplicateRequests         uint64           `json:"duplicate_requests"`
	OutOfOrderRequests        uint64           `json:"out_of_order_requests"`
	AbandonedRequests         uint64           `json:"abandoned_requests"`
	UnmatchedResponses        uint64           `json:"unmatched_responses"`
	CommandMismatches         uint64           `json:"command_mismatches"`
	ObservedEvents            uint64           `json:"observed_events"`
	FramingErrors             uint64           `json:"framing_errors"`
	IgnoredMessages           uint64           `json:"ignored_messages"`
	ClockRegressions          uint64           `json:"clock_regressions"`
	ClientObservationDisabled bool             `json:"client_observation_disabled"`
	ServerObservationDisabled bool             `json:"server_observation_disabled"`
	ProxyFailed               bool             `json:"proxy_failed"`
	CaptureComplete           bool             `json:"capture_complete"`
	RequestSummary            []RequestSummary `json:"request_summary"`
}

// Trace is directly consumable as Chrome Trace JSON. Timestamps and durations
// are in microseconds; DisplayTimeUnit affects the viewer's presentation only.
type Trace struct {
	TraceEvents     []Event  `json:"traceEvents"`
	DisplayTimeUnit string   `json:"displayTimeUnit"`
	OtherData       Metadata `json:"otherData"`
}

type pendingRequest struct {
	command string
	// Unknown commands share a display label but must never share identity.
	commandHash [sha256.Size]byte
	startNS     int64
	lane        int
}

// Recorder is safe for concurrent use by the two proxy directions. Finish
// freezes it; late writes from a draining proxy goroutine are harmless.
type Recorder struct {
	mu       sync.Mutex
	now      func() time.Time
	started  time.Time
	lastNS   int64
	finished bool
	frozen   Trace

	client stream
	server stream
	events []Event
	meta   Metadata

	lastRequestSeq int64
	pending        map[int64]pendingRequest
	lanes          [maxPendingRequests]bool
	usedLanes      [maxPendingRequests]bool
	summary        map[string]RequestSummary
}

func NewRecorder(maxEvents int) (*Recorder, error) {
	return newRecorder(maxEvents, time.Now)
}

func newRecorder(maxEvents int, now func() time.Time) (*Recorder, error) {
	if maxEvents < 1 || maxEvents > MaxEvents {
		return nil, fmt.Errorf("perf: max events must be in 1..%d", MaxEvents)
	}
	return &Recorder{
		now:     now,
		started: now(),
		events:  make([]Event, 0, min(maxEvents, 1024)),
		meta: Metadata{
			Schema: "multi-dap.host-trace.v1", Clock: "host-monotonic",
			Evidence: "host_observation", MaxEvents: maxEvents,
		},
		pending: make(map[int64]pendingRequest),
		summary: make(map[string]RequestSummary),
	}, nil
}

// ClientWriter observes client-to-daemon DAP bytes, never control-plane bytes.
// It always accepts the full write, even after malformed input or Finish.
func (r *Recorder) ClientWriter() io.Writer { return observer{recorder: r, client: true} }

// ServerWriter observes daemon-to-client DAP bytes after the proxy handshake.
// It always accepts the full write, even after malformed input or Finish.
func (r *Recorder) ServerWriter() io.Writer { return observer{recorder: r} }

type observer struct {
	recorder *Recorder
	client   bool
}

func (o observer) Write(data []byte) (int, error) {
	r := o.recorder
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.finished {
		direction := &r.server
		if o.client {
			direction = &r.client
		}
		r.observe(direction, data, o.client)
	}
	return len(data), nil
}

// Finish marks unfinished frames/requests without inventing end timestamps.
// It is idempotent: only the first call's proxyFailed value is used. Returned
// slices and pointers are independent, so callers cannot mutate the snapshot.
func (r *Recorder) Finish(proxyFailed bool) Trace {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.finished {
		return cloneTrace(r.frozen)
	}
	r.meta.DurationUS = micros(r.timestamp())
	for _, direction := range []*stream{&r.client, &r.server} {
		if !direction.disabled && direction.partial() {
			r.disable(direction)
		}
	}
	r.meta.ClientObservationDisabled = r.client.disabled
	r.meta.ServerObservationDisabled = r.server.disabled
	r.meta.ProxyFailed = proxyFailed
	r.meta.RetainedEvents = len(r.events)
	r.meta.UnfinishedRequests = len(r.pending)
	r.meta.CaptureComplete = !proxyFailed && r.meta.FramingErrors == 0 &&
		r.meta.DroppedEvents == 0 && r.meta.UnfinishedRequests == 0 &&
		r.meta.PendingLimitDrops == 0 && r.meta.DuplicateRequests == 0 &&
		r.meta.OutOfOrderRequests == 0 && r.meta.UnmatchedResponses == 0 &&
		r.meta.CommandMismatches == 0 && r.meta.IgnoredMessages == 0 &&
		r.meta.ClockRegressions == 0
	r.meta.RequestSummary = make([]RequestSummary, 0, len(r.summary))
	for _, summary := range r.summary {
		r.meta.RequestSummary = append(r.meta.RequestSummary, summary)
	}
	sort.Slice(r.meta.RequestSummary, func(i, j int) bool {
		return r.meta.RequestSummary[i].Command < r.meta.RequestSummary[j].Command
	})

	metadata := []Event{
		{Name: "process_name", Phase: "M", PID: tracePID, Args: EventArgs{Name: "multi-dap proxy (host)"}},
		{Name: "thread_name", Phase: "M", PID: tracePID, TID: eventTID, Args: EventArgs{Name: "Observed DAP events"}},
	}
	for lane, used := range r.usedLanes {
		if used {
			metadata = append(metadata, Event{
				Name: "thread_name", Phase: "M", PID: tracePID, TID: lane + 2,
				Args: EventArgs{Name: "DAP request lane " + strconv.Itoa(lane+1)},
			})
		}
	}
	events := make([]Event, 0, len(metadata)+len(r.events))
	events = append(events, metadata...)
	events = append(events, r.events...)
	r.frozen = Trace{TraceEvents: events, DisplayTimeUnit: "ms", OtherData: r.meta}
	r.finished = true
	r.events = nil
	r.pending = nil
	r.summary = nil
	r.client.clear()
	r.server.clear()
	return cloneTrace(r.frozen)
}

func (r *Recorder) timestamp() int64 {
	elapsed := r.now().Sub(r.started).Nanoseconds()
	if elapsed < r.lastNS {
		elapsed = r.lastNS
		r.meta.ClockRegressions++
	}
	r.lastNS = elapsed
	return elapsed
}

func micros(nanoseconds int64) float64 { return float64(nanoseconds) / 1000 }

func (r *Recorder) appendEvent(event Event) bool {
	if len(r.events) == r.meta.MaxEvents {
		r.meta.DroppedEvents++
		return false
	}
	r.events = append(r.events, event)
	return true
}

func (r *Recorder) request(message message, timestamp int64) {
	r.meta.ObservedRequests++
	// DAP sequence numbers must increase. In particular, never correlate a
	// late response with a new request reusing a previously completed number.
	if message.seq <= r.lastRequestSeq {
		if previous, exists := r.pending[message.seq]; exists {
			delete(r.pending, message.seq)
			r.lanes[previous.lane] = false
			r.meta.DuplicateRequests++
			r.meta.AbandonedRequests++
		} else {
			r.meta.OutOfOrderRequests++
		}
		return
	}
	r.lastRequestSeq = message.seq
	if len(r.pending) == maxPendingRequests {
		r.meta.PendingLimitDrops++
		return
	}
	lane := 0
	for r.lanes[lane] {
		lane++
	}
	r.lanes[lane] = true
	r.pending[message.seq] = pendingRequest{
		command: commandName(message.command), commandHash: sha256.Sum256([]byte(message.command)),
		startNS: timestamp, lane: lane,
	}
}

func (r *Recorder) response(message message, timestamp int64) {
	request, exists := r.pending[message.requestSeq]
	if !exists {
		r.meta.UnmatchedResponses++
		return
	}
	delete(r.pending, message.requestSeq)
	r.lanes[request.lane] = false
	if sha256.Sum256([]byte(message.command)) != request.commandHash {
		r.meta.CommandMismatches++
		r.meta.AbandonedRequests++
		return
	}
	duration := micros(timestamp - request.startNS)
	r.meta.CompletedRequests++
	summary := r.summary[request.command]
	summary.Command = request.command
	summary.Completed++
	summary.TotalDurationUS += duration
	summary.MaxDurationUS = max(summary.MaxDurationUS, duration)
	if !message.success {
		r.meta.FailedRequests++
		summary.Failed++
	}
	r.summary[request.command] = summary
	success := message.success
	if r.appendEvent(Event{
		Name: request.command, Category: "dap.request", Phase: "X",
		Timestamp: micros(request.startNS), Duration: duration, PID: tracePID,
		TID: request.lane + 2, Args: EventArgs{RequestSeq: message.requestSeq, Success: &success},
	}) {
		r.usedLanes[request.lane] = true
	}
}

func (r *Recorder) event(message message, timestamp int64) {
	r.meta.ObservedEvents++
	r.appendEvent(Event{
		Name: eventName(message.event), Category: "dap.event", Phase: "i",
		Timestamp: micros(timestamp), PID: tracePID, TID: eventTID, Scope: "t",
	})
}

func cloneTrace(source Trace) Trace {
	cloned := source
	cloned.TraceEvents = append([]Event(nil), source.TraceEvents...)
	for index := range cloned.TraceEvents {
		if value := cloned.TraceEvents[index].Args.Success; value != nil {
			independent := *value
			cloned.TraceEvents[index].Args.Success = &independent
		}
	}
	cloned.OtherData.RequestSummary = append([]RequestSummary{}, source.OtherData.RequestSummary...)
	return cloned
}

func commandName(command string) string {
	switch command {
	case "initialize", "attach", "launch", "configurationDone", "disconnect",
		"restart", "terminate", "cancel", "threads", "stackTrace", "scopes",
		"variables", "evaluate", "setVariable", "setExpression", "source",
		"loadedSources", "modules", "readMemory", "writeMemory", "disassemble",
		"setBreakpoints", "setFunctionBreakpoints", "setInstructionBreakpoints",
		"setDataBreakpoints", "dataBreakpointInfo", "breakpointLocations",
		"setExceptionBreakpoints", "exceptionInfo", "continue", "pause",
		"next", "stepIn", "stepOut", "stepBack", "reverseContinue", "restartFrame",
		"goto", "gotoTargets", "stepInTargets", "completions", "terminateThreads",
		"multi-dap/trace/open", "multi-dap/trace/poll", "multi-dap/trace/read",
		"multi-dap/trace/start", "multi-dap/trace/stop", "multi-dap/trace/close",
		"multi-dap/trace/status":
		return command
	default:
		return "unknown"
	}
}

func eventName(event string) string {
	switch event {
	case "initialized", "stopped", "continued", "exited", "terminated", "thread",
		"output", "breakpoint", "module", "loadedSource", "process", "capabilities",
		"progressStart", "progressUpdate", "progressEnd", "invalidated", "memory":
		return event
	default:
		return "unknown"
	}
}
