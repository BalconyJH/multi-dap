package timemachine

import (
	"encoding/json"
	"errors"
	"math/big"
	"sort"
	"strconv"
	"strings"
)

const maxTraceTracks = 4096

type TraceArgs struct {
	Name            string   `json:"name,omitempty"`
	PacketIndex     string   `json:"packet_index,omitempty"`
	Address         string   `json:"address,omitempty"`
	RawTag          string   `json:"raw_tag,omitempty"`
	RawEnd          string   `json:"raw_end,omitempty"`
	ContextEvidence []string `json:"context_evidence,omitempty"`
}

type TraceEvent struct {
	Name      string      `json:"name"`
	Category  string      `json:"cat,omitempty"`
	Phase     string      `json:"ph"`
	Timestamp json.Number `json:"ts,omitempty"`
	Duration  json.Number `json:"dur,omitempty"`
	PID       int         `json:"pid"`
	TID       int         `json:"tid,omitempty"`
	Scope     string      `json:"s,omitempty"`
	Args      TraceArgs   `json:"args"`
}

type GenerationClock struct {
	Generation uint64 `json:"generation,string"`
	ClockEpoch uint64 `json:"clock_epoch,string"`
	Origin     string `json:"origin_picoseconds"`
}

type TraceMetadata struct {
	Schema                 string            `json:"schema"`
	CanonicalCaptureSHA256 string            `json:"canonical_capture_sha256"`
	Source                 Source            `json:"source"`
	MultiVersion           string            `json:"multi_version"`
	Evidence               string            `json:"evidence"`
	NativeRuntimeQualified bool              `json:"native_runtime_qualified"`
	HardwareQualified      bool              `json:"hardware_qualified"`
	TargetCPUTime          bool              `json:"target_cpu_time"`
	Clock                  string            `json:"clock"`
	GenerationsNotAligned  bool              `json:"generations_not_aligned"`
	ClockEpochsNotAligned  bool              `json:"clock_epochs_not_aligned"`
	FunctionClockBasis     string            `json:"function_clock_basis"`
	GenerationClocks       []GenerationClock `json:"generation_clocks"`
	UntimedDiscontinuities []Discontinuity   `json:"untimed_discontinuities"`
	Statistics             Statistics        `json:"statistics"`
}

type Trace struct {
	TraceEvents     []TraceEvent  `json:"traceEvents"`
	DisplayTimeUnit string        `json:"displayTimeUnit"`
	OtherData       TraceMetadata `json:"otherData"`
}

type generationTrace struct {
	pid    int
	origin *big.Int
	tracks map[string]int
}

type clockDomain struct {
	generation uint64
	epoch      uint64
}

// ChromeTrace requires physical picosecond tags. It never invents a CPU
// frequency for count-based data or aligns unrelated trace generations.
func ChromeTrace(report Report) (Trace, error) {
	if report.Schema != "multi-dap.native-report.v1" || !validDigest(report.CanonicalCaptureSHA256) ||
		report.Info.Timebase != 3 || report.Info.Unit != "picoseconds" {
		return Trace{}, errors.New("timemachine: Chrome export requires a normalized picosecond report")
	}
	if len(report.Observations) > MaxCapturePackets || len(report.Spans) > MaxCapturePackets ||
		len(report.Discontinuities) > 2*MaxCapturePackets+2*MaxGenerations {
		return Trace{}, errors.New("timemachine: normalized report exceeds limits")
	}
	byGeneration := map[clockDomain]*generationTrace{}
	for _, observation := range report.Observations {
		if observation.Generation == 0 || observation.ClockEpoch == 0 {
			return Trace{}, errors.New("timemachine: invalid report generation")
		}
		domain := clockDomain{observation.Generation, observation.ClockEpoch}
		generation := byGeneration[domain]
		if generation == nil {
			if len(byGeneration) >= maxTraceTracks {
				return Trace{}, errors.New("timemachine: report clock-domain limit exceeded")
			}
			generation = &generationTrace{tracks: make(map[string]int)}
			byGeneration[domain] = generation
		}
		if observation.Timestamp == nil {
			continue
		}
		value, err := physicalTimestamp(observation.Timestamp)
		if err != nil {
			return Trace{}, err
		}
		if generation.origin == nil || value.Cmp(generation.origin) < 0 {
			generation.origin = value
		}
	}
	generations := make([]clockDomain, 0, len(byGeneration))
	for id := range byGeneration {
		generations = append(generations, id)
	}
	sort.Slice(generations, func(i, j int) bool {
		if generations[i].generation != generations[j].generation {
			return generations[i].generation < generations[j].generation
		}
		return generations[i].epoch < generations[j].epoch
	})
	trace := Trace{
		TraceEvents: make([]TraceEvent, 0), DisplayTimeUnit: "ns",
		OtherData: TraceMetadata{
			Schema: "multi-dap.native-chrome-trace.v1", CanonicalCaptureSHA256: report.CanonicalCaptureSHA256,
			Source: cloneSource(report.Source), MultiVersion: report.MultiVersion,
			Evidence: "native_packet_observation_unqualified",
			Clock:    "target_picoseconds_relative_per_clock_domain", GenerationsNotAligned: true, ClockEpochsNotAligned: true,
			FunctionClockBasis:     report.FunctionClockBasis,
			GenerationClocks:       make([]GenerationClock, 0),
			UntimedDiscontinuities: append([]Discontinuity{}, report.Discontinuities...),
			Statistics:             report.Statistics,
		},
	}
	for index, id := range generations {
		generation := byGeneration[id]
		generation.pid = index + 1
		trace.TraceEvents = append(trace.TraceEvents, TraceEvent{
			Name: "process_name", Phase: "M", PID: generation.pid,
			Args: TraceArgs{Name: "Trace generation " + strconv.FormatUint(id.generation, 10) +
				" / clock epoch " + strconv.FormatUint(id.epoch, 10) + " (independent origin)"},
		})
		if generation.origin != nil {
			trace.OtherData.GenerationClocks = append(trace.OtherData.GenerationClocks, GenerationClock{
				Generation: id.generation, ClockEpoch: id.epoch, Origin: generation.origin.String(),
			})
		}
	}
	totalTracks := 0
	track := func(generation *generationTrace, context *Context) (int, error) {
		key, name := "unknown", "Unknown task/address space (core unknown)"
		if context != nil {
			if !validContext(*context) || !knownContext(context) {
				return 0, errors.New("timemachine: invalid normalized context")
			}
			key = contextKey(*context)
			name = "Task " + context.TaskID + " / AS " + context.AddressSpaceID
			if report.Source.CoreID == nil {
				name += " (core unknown)"
			} else {
				name += " (declared core " + strconv.FormatUint(uint64(*report.Source.CoreID), 10) + ")"
			}
		}
		if tid, ok := generation.tracks[key]; ok {
			return tid, nil
		}
		if totalTracks >= maxTraceTracks {
			return 0, errors.New("timemachine: Chrome track limit exceeded")
		}
		totalTracks++
		tid := len(generation.tracks) + 1
		generation.tracks[key] = tid
		trace.TraceEvents = append(trace.TraceEvents, TraceEvent{
			Name: "thread_name", Phase: "M", PID: generation.pid, TID: tid, Args: TraceArgs{Name: name},
		})
		return tid, nil
	}
	for _, observation := range report.Observations {
		if observation.Timestamp == nil {
			continue
		}
		value, err := physicalTimestamp(observation.Timestamp)
		if err != nil {
			return Trace{}, err
		}
		generation := byGeneration[clockDomain{observation.Generation, observation.ClockEpoch}]
		tid, err := track(generation, observation.Context)
		if err != nil {
			return Trace{}, err
		}
		address := ""
		switch observation.Packet.Kind {
		case "instruction", "function", "address_sync":
			address = observation.Packet.Address
		}
		trace.TraceEvents = append(trace.TraceEvents, TraceEvent{
			Name: observation.Packet.Kind, Category: "target.packet", Phase: "i", Scope: "t",
			Timestamp: picosecondsToMicroseconds(new(big.Int).Sub(value, generation.origin)),
			PID:       generation.pid, TID: tid,
			Args: TraceArgs{PacketIndex: strconv.FormatUint(observation.Index, 10), Address: address,
				RawTag: observation.Timestamp.RawTag, ContextEvidence: []string{observation.ContextEvidence}},
		})
	}
	for _, span := range report.Spans {
		generation := byGeneration[clockDomain{span.Generation, span.ClockEpoch}]
		start, startOK := new(big.Int).SetString(span.Start, 10)
		end, endOK := new(big.Int).SetString(span.End, 10)
		if generation == nil || generation.origin == nil || span.Unit != "picoseconds" ||
			!startOK || !endOK || start.Cmp(generation.origin) < 0 || end.Cmp(start) < 0 ||
			len(span.Start) > 21 || len(span.End) > 21 {
			return Trace{}, errors.New("timemachine: invalid normalized function span")
		}
		tid, err := track(generation, &span.Context)
		if err != nil {
			return Trace{}, err
		}
		trace.TraceEvents = append(trace.TraceEvents, TraceEvent{
			Name: "Function event " + span.Address, Category: "target.function_lifetime", Phase: "X",
			Timestamp: picosecondsToMicroseconds(new(big.Int).Sub(start, generation.origin)),
			Duration:  picosecondsToMicroseconds(new(big.Int).Sub(end, start)), PID: generation.pid, TID: tid,
			Args: TraceArgs{Address: span.Address, RawTag: span.RawStart, RawEnd: span.RawEnd,
				ContextEvidence: append([]string{}, span.ContextEvidence...)},
		})
	}
	return trace, nil
}

func physicalTimestamp(timestamp *Timestamp) (*big.Int, error) {
	if timestamp.Unit != "picoseconds" || len(timestamp.Value) == 0 || len(timestamp.Value) > 21 {
		return nil, errors.New("timemachine: invalid physical timestamp")
	}
	value, ok := new(big.Int).SetString(timestamp.Value, 10)
	if !ok {
		return nil, errors.New("timemachine: invalid physical timestamp")
	}
	return value, nil
}

func picosecondsToMicroseconds(value *big.Int) json.Number {
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(value, big.NewInt(1000000), remainder)
	if remainder.Sign() == 0 {
		return json.Number(quotient.String())
	}
	fraction := remainder.String()
	fraction = strings.Repeat("0", 6-len(fraction)) + fraction
	return json.Number(quotient.String() + "." + strings.TrimRight(fraction, "0"))
}
