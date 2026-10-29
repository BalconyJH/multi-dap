package timemachine

import (
	"encoding/hex"
	"errors"
	"math/big"
	"sort"
	"strings"
)

type Timestamp struct {
	RawTag string `json:"raw_tag"`
	Value  string `json:"value,omitempty"`
	Unit   string `json:"unit"`
}

type Observation struct {
	Generation      uint64     `json:"generation,string"`
	ClockEpoch      uint64     `json:"clock_epoch,string"`
	Index           uint64     `json:"index,string"`
	Packet          Packet     `json:"packet"`
	Timestamp       *Timestamp `json:"timestamp,omitempty"`
	Context         *Context   `json:"context,omitempty"`
	ContextEvidence string     `json:"context_evidence"`
}

type Span struct {
	Generation      uint64   `json:"generation,string"`
	ClockEpoch      uint64   `json:"clock_epoch,string"`
	EntryIndex      uint64   `json:"entry_index,string"`
	ExitIndex       uint64   `json:"exit_index,string"`
	Address         string   `json:"address"`
	Context         Context  `json:"context"`
	ContextEvidence []string `json:"context_evidence"`
	Start           string   `json:"start"`
	End             string   `json:"end"`
	Elapsed         string   `json:"elapsed"`
	Unit            string   `json:"unit"`
	RawStart        string   `json:"raw_start"`
	RawEnd          string   `json:"raw_end"`
	Depth           int      `json:"depth"`
}

type Discontinuity struct {
	Generation uint64 `json:"generation,string"`
	ClockEpoch uint64 `json:"clock_epoch,string"`
	Index      uint64 `json:"index,string"`
	Reason     string `json:"reason"`
}

type Statistics struct {
	Packets                int `json:"packets"`
	Generations            int `json:"generations"`
	UnknownPackets         int `json:"unknown_packets"`
	UnknownEvents          int `json:"unknown_events"`
	UnattributedPackets    int `json:"unattributed_packets"`
	AttributionConflicts   int `json:"attribution_conflicts"`
	ClockRegressions       int `json:"clock_regressions"`
	ClockEpochs            int `json:"clock_epochs"`
	UnknownClockEvents     int `json:"unknown_clock_events"`
	UnknownFunctionEvents  int `json:"unknown_function_events"`
	UnmatchedExits         int `json:"unmatched_exits"`
	MismatchedExits        int `json:"mismatched_exits"`
	InvalidatedOpenFrames  int `json:"invalidated_open_frames"`
	UnfinishedFrames       int `json:"unfinished_frames"`
	CompletedFunctionSpans int `json:"completed_function_spans"`
}

type Report struct {
	Schema                 string          `json:"schema"`
	CanonicalCaptureSHA256 string          `json:"canonical_capture_sha256"`
	Source                 Source          `json:"source"`
	MultiVersion           string          `json:"multi_version"`
	Info                   Info            `json:"info"`
	ABIContractMatched     bool            `json:"abi_contract_matched"`
	NativeRuntimeQualified bool            `json:"native_runtime_qualified"`
	HardwareQualified      bool            `json:"hardware_qualified"`
	TargetCPUTime          bool            `json:"target_cpu_time"`
	FunctionClockBasis     string          `json:"function_clock_basis"`
	Observations           []Observation   `json:"observations"`
	Spans                  []Span          `json:"spans"`
	Discontinuities        []Discontinuity `json:"discontinuities"`
	Statistics             Statistics      `json:"statistics"`
}

type openFrame struct {
	observation Observation
}

type normalizer struct {
	report   Report
	stacks   map[Context][]openFrame
	contexts map[Context]bool
	open     int
	epoch    uint64
}

// Normalize retains every packet and only pairs fully observed function events.
// Span elapsed values are function-lifetime intervals, never CPU attribution.
func Normalize(capture Capture) (Report, error) {
	profile, info, err := validateCapture(capture)
	if err != nil {
		return Report{}, err
	}
	digest, err := canonicalDigest(capture)
	if err != nil {
		return Report{}, err
	}
	n := normalizer{
		report: Report{
			Schema: "multi-dap.native-report.v1", CanonicalCaptureSHA256: digest,
			Source: cloneSource(capture.Source), MultiVersion: profile.Version, Info: info,
			ABIContractMatched: true,
			FunctionClockBasis: "stream_timebase_as_used_by_vendor_example_unqualified",
			Observations:       make([]Observation, 0), Spans: make([]Span, 0),
			Discontinuities: make([]Discontinuity, 0),
		},
		stacks: make(map[Context][]openFrame), contexts: make(map[Context]bool),
	}
	for generationNumber, generation := range capture.Generations {
		n.report.Statistics.Generations++
		n.report.Statistics.ClockEpochs++
		n.epoch = 1
		if generationNumber > 0 || generation.Reason != "initial" || generation.FirstIndex != 0 {
			n.barrier(generation.ID, generation.FirstIndex, "generation_"+generation.Reason)
		}
		var lastTag *big.Int
		clockEpoch := uint64(1)
		start := len(n.report.Observations)
		for index, encoded := range generation.PacketsHex {
			data, _ := hex.DecodeString(encoded)
			packet, err := DecodePacket(profile, data)
			if err != nil {
				return Report{}, err
			}
			observation := Observation{
				Generation: generation.ID, Index: generation.FirstIndex + uint64(index),
				Packet: packet, ContextEvidence: "unavailable",
			}
			if packet.Tag != nil {
				observation.Timestamp = normalizeTimestamp(*packet.Tag, info)
				raw, _ := new(big.Int).SetString(packet.Tag.Raw, 10)
				if lastTag != nil && raw.Cmp(lastTag) < 0 {
					clockEpoch++
					n.report.Statistics.ClockRegressions++
					n.report.Statistics.ClockEpochs++
				}
				lastTag = raw
			}
			observation.ClockEpoch = clockEpoch
			n.report.Observations = append(n.report.Observations, observation)
			n.report.Statistics.Packets++
			if packet.Kind == "unknown" {
				n.report.Statistics.UnknownPackets++
			}
			if packet.Event != nil && !packet.Event.Known {
				n.report.Statistics.UnknownEvents++
			}
		}
		var declared *Context
		// An initial declaration is not automatically valid after a replacement.
		if generationNumber == 0 && generation.Reason == "initial" {
			declared = capture.Source.InitialContext
		}
		if err := n.generation(start, len(n.report.Observations), declared); err != nil {
			return Report{}, err
		}
		// No function entry may survive a generation boundary, including when
		// the next generation happens to reuse an address and task identity.
		n.report.Statistics.UnfinishedFrames += n.open
		n.stacks = make(map[Context][]openFrame)
		n.open = 0
	}
	n.report.Statistics.CompletedFunctionSpans = len(n.report.Spans)
	return n.report, nil
}

func normalizeTimestamp(tag Timetag, info Info) *Timestamp {
	result := &Timestamp{RawTag: tag.Raw, Unit: info.Unit}
	value, ok := new(big.Int).SetString(tag.Raw, 10)
	if !ok || info.Unit == "unknown" {
		return result
	}
	if info.Timebase == 2 {
		value.Rsh(value, 8)
	}
	result.Value = value.String()
	return result
}

// A segment is bounded by context markers. A forward PID and a following
// previous-PID can corroborate it; conflicting attribution invalidates it.
func (n *normalizer) generation(start, end int, initial *Context) error {
	segmentStart := start
	current, evidence := initial, "operator_declared"
	for index := start; index < end; index++ {
		observation := &n.report.Observations[index]
		packet := observation.Packet
		switch packet.Kind {
		case "pid":
			if err := n.segment(segmentStart, index, current, evidence, nil); err != nil {
				return err
			}
			n.advanceClock(*observation)
			current, evidence = packet.Context, "native_pid"
			observation.Context = cloneContext(current)
			observation.ContextEvidence = "native_pid_for_following_packets"
			segmentStart = index + 1
		case "previous_pid":
			if err := n.segment(segmentStart, index, current, evidence, packet.Context); err != nil {
				return err
			}
			n.advanceClock(*observation)
			observation.Context = cloneContext(packet.Context)
			observation.ContextEvidence = "native_previous_pid_for_preceding_packets"
			current, evidence = nil, "unavailable"
			segmentStart = index + 1
		default:
			if packetBarrier(packet) {
				if err := n.segment(segmentStart, index, current, evidence, nil); err != nil {
					return err
				}
				n.advanceClock(*observation)
				reason := "unknown_packet"
				if packet.Event != nil {
					reason = packet.Event.Name
				} else if packet.Function != nil {
					reason = "unknown_function_event"
					n.report.Statistics.UnknownFunctionEvents++
				}
				n.barrier(observation.Generation, observation.Index, reason)
				current, evidence = nil, "unavailable"
				segmentStart = index + 1
			}
		}
	}
	return n.segment(segmentStart, end, current, evidence, nil)
}

func (n *normalizer) segment(start, end int, forward *Context, evidence string, backward *Context) error {
	if start < end {
		n.advanceClock(n.report.Observations[start])
	}
	if !knownContext(forward) {
		forward = nil
		evidence = "unavailable"
	}
	context := forward
	if backward != nil {
		if !knownContext(backward) {
			context, evidence = nil, "unavailable"
		} else if forward != nil && *forward != *backward {
			if start < end {
				n.report.Statistics.AttributionConflicts++
				n.barrier(n.report.Observations[start].Generation, n.report.Observations[start].Index, "attribution_conflict")
			}
			conflictEvidence := "conflicting_native_attribution"
			if evidence == "operator_declared" {
				conflictEvidence = "conflicting_declared_and_native_attribution"
			}
			context, evidence = nil, conflictEvidence
		} else {
			context = backward
			switch {
			case forward == nil:
				evidence = "native_previous_pid"
			case evidence == "operator_declared":
				evidence = "operator_declared_and_native_previous_pid"
			default:
				evidence = "native_pid_and_native_previous_pid"
			}
		}
	}
	if !knownContext(context) {
		context = nil
	}
	if context == nil && start < end && n.open > 0 {
		n.barrier(n.report.Observations[start].Generation, n.report.Observations[start].Index, "unknown_context")
	}
	if context != nil && start < end && !n.contexts[*context] {
		if len(n.contexts) >= MaxContexts {
			return errors.New("timemachine: context limit exceeded")
		}
		n.contexts[*context] = true
	}
	for index := start; index < end; index++ {
		observation := &n.report.Observations[index]
		observation.Context, observation.ContextEvidence = cloneContext(context), evidence
		if context == nil {
			n.report.Statistics.UnattributedPackets++
			if evidence != "conflicting_native_attribution" && evidence != "conflicting_declared_and_native_attribution" {
				observation.ContextEvidence = "unavailable"
			}
		}
		n.advanceClock(*observation)
		if observation.Packet.Function == nil {
			continue
		}
		if observation.Timestamp == nil || observation.Timestamp.Value == "" {
			n.report.Statistics.UnknownClockEvents++
			n.barrier(observation.Generation, observation.Index, "unknown_function_clock")
			continue
		}
		if context == nil {
			// Unknown task identity cannot join two functions merely because
			// their native address fields happen to be equal.
			n.barrier(observation.Generation, observation.Index, "unknown_function_context")
			continue
		}
		if err := n.function(*observation); err != nil {
			return err
		}
	}
	return nil
}

func packetBarrier(packet Packet) bool {
	return packet.Kind == "unknown" ||
		(packet.Event != nil && packet.Event.Discontinuity) ||
		(packet.Function != nil && packet.Function.Kind == "unknown")
}

func (n *normalizer) function(observation Observation) error {
	context := *observation.Context
	stack := n.stacks[context]
	if observation.Packet.Function.Kind == "entry" {
		if n.open >= MaxOpenFrames || len(stack) >= MaxContextDepth {
			return errors.New("timemachine: open function limit exceeded")
		}
		n.stacks[context] = append(stack, openFrame{observation: observation})
		n.open++
		return nil
	}
	if len(stack) == 0 {
		n.report.Statistics.UnmatchedExits++
		return nil
	}
	entry := stack[len(stack)-1].observation
	if entry.Packet.Address != observation.Packet.Address {
		n.report.Statistics.MismatchedExits++
		n.barrier(observation.Generation, observation.Index, "function_exit_mismatch")
		return nil
	}
	start, _ := new(big.Int).SetString(entry.Timestamp.Value, 10)
	end, _ := new(big.Int).SetString(observation.Timestamp.Value, 10)
	if start.Cmp(end) > 0 {
		n.report.Statistics.ClockRegressions++
		n.barrier(observation.Generation, observation.Index, "function_clock_regression")
		return nil
	}
	evidence := []string{entry.ContextEvidence}
	if observation.ContextEvidence != entry.ContextEvidence {
		evidence = append(evidence, observation.ContextEvidence)
		sort.Strings(evidence)
	}
	n.report.Spans = append(n.report.Spans, Span{
		Generation: observation.Generation, ClockEpoch: observation.ClockEpoch,
		EntryIndex: entry.Index, ExitIndex: observation.Index,
		Address: entry.Packet.Address, Context: context, ContextEvidence: evidence,
		Start: start.String(), End: end.String(), Elapsed: new(big.Int).Sub(end, start).String(),
		Unit: entry.Timestamp.Unit, RawStart: entry.Timestamp.RawTag, RawEnd: observation.Timestamp.RawTag,
		Depth: len(stack) - 1,
	})
	n.stacks[context] = stack[:len(stack)-1]
	n.open--
	return nil
}

func (n *normalizer) barrier(generation, index uint64, reason string) {
	n.report.Discontinuities = append(n.report.Discontinuities, Discontinuity{
		Generation: generation, ClockEpoch: n.epoch, Index: index, Reason: reason,
	})
	n.report.Statistics.InvalidatedOpenFrames += n.open
	n.open = 0
	n.stacks = make(map[Context][]openFrame)
}

func (n *normalizer) advanceClock(observation Observation) {
	if observation.ClockEpoch != n.epoch {
		n.epoch = observation.ClockEpoch
		n.barrier(observation.Generation, observation.Index, "clock_regression")
	}
}

func cloneContext(value *Context) *Context {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func cloneSource(source Source) Source {
	result := source
	result.InitialContext = cloneContext(source.InitialContext)
	if source.CoreID != nil {
		coreID := *source.CoreID
		result.CoreID = &coreID
	}
	return result
}

func contextKey(context Context) string {
	return strings.Join([]string{context.TaskID, context.AddressSpaceID}, ":")
}
