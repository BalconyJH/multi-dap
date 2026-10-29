package timemachine

import (
	"bytes"
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func syntheticCapture(t *testing.T, version string, timebase uint32, packets ...[]byte) Capture {
	t.Helper()
	profile := profileFor(t, version)
	generation := Generation{ID: 1, Reason: "initial", PacketsHex: make([]string, 0, len(packets))}
	for _, packet := range packets {
		generation.PacketsHex = append(generation.PacketsHex, encoded(packet))
	}
	return Capture{
		Schema: CaptureSchema, MultiVersion: version, ABI: profile.ABI,
		Source:  Source{Kind: "synthetic_fixture", SourceID: "synthetic-not-vendor"},
		InfoHex: encoded(syntheticInfo(profile, timebase)), Generations: []Generation{generation},
	}
}

func normalizeFor(t *testing.T, capture Capture) Report {
	t.Helper()
	report, err := Normalize(capture)
	if err != nil {
		t.Fatal(err)
	}
	return report
}

func TestNormalizePairsOnlyKnownContextWithSeparateTaskStacks(t *testing.T) {
	capture := syntheticCapture(t, "8.1.4", 3,
		syntheticContext(1, 0, false), syntheticFunction(0x1000, 100, true),
		syntheticContext(2, 4, false), syntheticFunction(0x2000, 200, true),
		syntheticFunction(0x2000, 300, false), syntheticContext(1, 0, false),
		syntheticFunction(0x1000, 400, false),
	)
	report := normalizeFor(t, capture)
	if len(report.Spans) != 2 || report.Statistics.CompletedFunctionSpans != 2 ||
		report.Spans[0].Context.TaskID != "2" || report.Spans[1].Context.TaskID != "1" ||
		report.Spans[1].Elapsed != "300" || report.Spans[1].Unit != "picoseconds" ||
		report.Spans[1].ContextEvidence[0] != "native_pid" || report.Source.CoreID != nil {
		t.Fatalf("report = %#v", report)
	}
	if report.HardwareQualified || report.NativeRuntimeQualified || report.TargetCPUTime ||
		!report.ABIContractMatched || len(report.CanonicalCaptureSHA256) != 64 {
		t.Fatalf("incorrect evidence = %#v", report)
	}
	wire, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(wire, []byte(`"kind":"synthetic_fixture"`)) ||
		!bytes.Contains(wire, []byte(`"hardware_qualified":false`)) {
		t.Fatalf("synthetic provenance lost: %s", wire)
	}
}

func TestPreviousPIDAttributesOnlyItsPrecedingSegment(t *testing.T) {
	capture := syntheticCapture(t, "8.1.4", 3,
		syntheticFunction(0x1000, 1, true), syntheticFunction(0x1000, 2, false),
		syntheticContext(7, 8, true),
		syntheticFunction(0x1000, 3, true), syntheticFunction(0x1000, 4, false),
	)
	report := normalizeFor(t, capture)
	if len(report.Spans) != 1 || report.Spans[0].Context != (Context{"7", "8"}) ||
		report.Spans[0].ContextEvidence[0] != "native_previous_pid" ||
		report.Observations[3].Context != nil || report.Statistics.UnattributedPackets != 2 {
		t.Fatalf("report = %#v", report)
	}
}

func TestForwardAndPreviousPIDConflictDoesNotCreateSpan(t *testing.T) {
	capture := syntheticCapture(t, "8.1.4", 3,
		syntheticContext(1, 0, false),
		syntheticFunction(0x1000, 1, true), syntheticFunction(0x1000, 2, false),
		syntheticContext(2, 0, true),
	)
	report := normalizeFor(t, capture)
	if len(report.Spans) != 0 || report.Statistics.AttributionConflicts != 1 ||
		report.Observations[1].Context != nil ||
		report.Observations[1].ContextEvidence != "conflicting_native_attribution" {
		t.Fatalf("report = %#v", report)
	}
	capture.Generations[0].PacketsHex[3] = encoded(syntheticContext(1, 0, true))
	report = normalizeFor(t, capture)
	if len(report.Spans) != 1 || report.Spans[0].ContextEvidence[0] != "native_pid_and_native_previous_pid" {
		t.Fatalf("corroborated report = %#v", report)
	}
}

func TestOperatorDeclarationRemainsDistinctAndDoesNotSurviveReplacement(t *testing.T) {
	capture := syntheticCapture(t, "8.1.4", 3,
		syntheticFunction(0x1000, 1, true), syntheticFunction(0x1000, 2, false),
	)
	initial := Context{"8", "9"}
	capture.Source.InitialContext = &initial
	report := normalizeFor(t, capture)
	if len(report.Spans) != 1 || report.Spans[0].ContextEvidence[0] != "operator_declared" {
		t.Fatalf("declared report = %#v", report)
	}
	initial.TaskID = "10"
	if report.Source.InitialContext.TaskID != "8" {
		t.Fatal("report source aliases caller-owned context")
	}
	capture.Generations = append(capture.Generations, Generation{
		ID: 2, Reason: "decoder_replacement", PacketsHex: append([]string(nil), capture.Generations[0].PacketsHex...),
	})
	report = normalizeFor(t, capture)
	if len(report.Spans) != 1 || report.Observations[2].Context != nil {
		t.Fatalf("declaration crossed replacement: %#v", report)
	}
}

func TestMissingOrSentinelContextCannotInventFunctionOwnership(t *testing.T) {
	for _, context := range [][]byte{nil, syntheticContext(math.MaxUint32, 0, false), syntheticContext(1, math.MaxUint64, false)} {
		packets := [][]byte{}
		if context != nil {
			packets = append(packets, context)
		}
		packets = append(packets, syntheticFunction(1, 1, true), syntheticFunction(1, 2, false))
		report := normalizeFor(t, syntheticCapture(t, "8.1.4", 3, packets...))
		if len(report.Spans) != 0 || report.Statistics.UnattributedPackets != 2 {
			t.Fatalf("unknown context produced span: %#v", report)
		}
	}
}

func TestDiscontinuityNeverJoinsFunctionIntervals(t *testing.T) {
	for _, gap := range [][]byte{
		syntheticEvent(2), syntheticEvent(66), syntheticEvent(67), syntheticEvent(74),
		syntheticEvent(200), syntheticPacket(200, 0, nil),
		syntheticPacket(10, 0, func(payload []byte) { payload[0] = 9 }),
	} {
		report := normalizeFor(t, syntheticCapture(t, "8.1.4", 3,
			syntheticContext(1, 0, false), syntheticFunction(1, 1, true), gap,
			syntheticContext(1, 0, false), syntheticFunction(1, 3, false),
		))
		if len(report.Spans) != 0 || report.Statistics.InvalidatedOpenFrames != 1 ||
			report.Statistics.UnmatchedExits != 1 || len(report.Discontinuities) == 0 {
			t.Fatalf("gap produced span: %#v", report)
		}
	}
	// Harmless markers have no timestamp; they neither create one nor break a
	// known interval merely to manufacture a timeline annotation.
	report := normalizeFor(t, syntheticCapture(t, "8.1.4", 3,
		syntheticContext(1, 0, false), syntheticFunction(1, 1, true),
		syntheticEvent(72), syntheticFunction(1, 3, false),
	))
	if len(report.Spans) != 1 || report.Observations[2].Timestamp != nil {
		t.Fatalf("marker semantics = %#v", report)
	}
}

func TestGenerationAndUnknownAttributionBoundariesInvalidateOpenFrames(t *testing.T) {
	capture := syntheticCapture(t, "8.1.4", 3,
		syntheticContext(1, 0, false), syntheticFunction(1, 1, true),
	)
	capture.Generations = append(capture.Generations, Generation{
		ID: 2, Reason: "count_regression",
		PacketsHex: []string{encoded(syntheticContext(1, 0, false)), encoded(syntheticFunction(1, 3, false))},
	})
	report := normalizeFor(t, capture)
	if len(report.Spans) != 0 || report.Statistics.UnfinishedFrames != 1 ||
		report.Statistics.UnmatchedExits != 1 || report.Statistics.Generations != 2 {
		t.Fatalf("generation produced span: %#v", report)
	}
	report = normalizeFor(t, syntheticCapture(t, "8.1.4", 3,
		syntheticContext(1, 0, false), syntheticFunction(1, 1, true),
		syntheticContext(1, 0, true), syntheticPacket(0, 0, nil),
		syntheticContext(1, 0, false), syntheticFunction(1, 3, false),
	))
	if len(report.Spans) != 0 || report.Statistics.InvalidatedOpenFrames != 1 {
		t.Fatalf("unattributed interval produced span: %#v", report)
	}
}

func TestClockRegressionAndUnmatchedReturnsNeverSynthesizeEnd(t *testing.T) {
	report := normalizeFor(t, syntheticCapture(t, "8.1.4", 3,
		syntheticContext(1, 0, false), syntheticFunction(1, 100, true),
		syntheticFunction(1, 99, false), syntheticFunction(2, 101, false),
		syntheticFunction(3, 102, true), syntheticFunction(4, 103, false),
	))
	if len(report.Spans) != 0 || report.Statistics.ClockRegressions != 1 ||
		report.Statistics.UnmatchedExits != 2 || report.Statistics.MismatchedExits != 1 {
		t.Fatalf("invalid events created spans: %#v", report)
	}
}

func TestCompletedSpanBeforeClockRegressionUsesDifferentChromeClockDomain(t *testing.T) {
	report := normalizeFor(t, syntheticCapture(t, "8.1.4", 3,
		syntheticContext(1, 0, false),
		syntheticFunction(1, 100, true), syntheticFunction(1, 200, false),
		syntheticFunction(2, 160, true), syntheticFunction(2, 250, false),
	))
	if len(report.Spans) != 2 || report.Spans[0].ClockEpoch != 1 || report.Spans[1].ClockEpoch != 2 ||
		report.Statistics.ClockRegressions != 1 || report.Statistics.ClockEpochs != 2 {
		t.Fatalf("clock epochs = %#v", report)
	}
	trace, err := ChromeTrace(report)
	if err != nil {
		t.Fatal(err)
	}
	var spans []TraceEvent
	for _, event := range trace.TraceEvents {
		if event.Phase == "X" {
			spans = append(spans, event)
		}
	}
	if len(spans) != 2 || spans[0].PID == spans[1].PID ||
		spans[0].Timestamp != "0" || spans[1].Timestamp != "0" ||
		len(trace.OtherData.GenerationClocks) != 2 ||
		trace.OtherData.GenerationClocks[0].Origin != "100" ||
		trace.OtherData.GenerationClocks[1].Origin != "160" ||
		!trace.OtherData.ClockEpochsNotAligned {
		t.Fatalf("unrelated clocks were aligned: %#v", trace)
	}
}

func TestClockEpochCannotJoinAnEntryBeforeRegressionToExitAfterRegression(t *testing.T) {
	report := normalizeFor(t, syntheticCapture(t, "8.1.4", 3,
		syntheticContext(1, 0, false),
		syntheticFunction(1, 100, true),
		syntheticPacket(11, 0, func(payload []byte) { payload[0] = 50 }),
		syntheticFunction(1, 150, false),
	))
	if len(report.Spans) != 0 || report.Statistics.InvalidatedOpenFrames != 1 ||
		report.Statistics.UnmatchedExits != 1 ||
		report.Observations[1].ClockEpoch == report.Observations[3].ClockEpoch {
		t.Fatalf("span crossed a clock epoch: %#v", report)
	}
}

func TestCountsRemainCountsAndCyclesShiftEndpointsSeparately(t *testing.T) {
	for _, timebase := range []uint32{1, 2, 99} {
		report := normalizeFor(t, syntheticCapture(t, "8.1.4", timebase,
			syntheticContext(1, 0, false), syntheticFunction(1, 255, true), syntheticFunction(1, 256, false),
		))
		if _, err := ChromeTrace(report); err == nil {
			t.Fatalf("count/unknown timebase %d generated time chart", timebase)
		}
		switch timebase {
		case 1:
			if len(report.Spans) != 1 || report.Spans[0].Unit != "instructions" || report.Spans[0].Elapsed != "1" {
				t.Fatalf("instruction counts = %#v", report)
			}
		case 2:
			if len(report.Spans) != 1 || report.Spans[0].Start != "0" ||
				report.Spans[0].End != "1" || report.Spans[0].Elapsed != "1" ||
				report.Spans[0].RawStart != "255" || report.Spans[0].Unit != "cycles" {
				t.Fatalf("cycle counts = %#v", report)
			}
		default:
			if len(report.Spans) != 0 || report.Statistics.UnknownClockEvents != 2 ||
				report.Observations[1].Timestamp.RawTag != "255" || report.Observations[1].Timestamp.Value != "" {
				t.Fatalf("unknown timebase = %#v", report)
			}
		}
	}
}

func TestLegacyNegativeTagsAndWideUnsignedTagsRemainExact(t *testing.T) {
	start, end := int64(-257), int64(-1)
	report := normalizeFor(t, syntheticCapture(t, "7.1.6d", 2,
		syntheticContext(1, 0, false), syntheticFunction(1, uint64(start), true),
		syntheticFunction(1, uint64(end), false),
	))
	if len(report.Spans) != 1 || report.Spans[0].Start != "-2" ||
		report.Spans[0].End != "-1" || report.Spans[0].Elapsed != "1" {
		t.Fatalf("signed cycle counts = %#v", report)
	}
	report = normalizeFor(t, syntheticCapture(t, "8.1.4", 3,
		syntheticContext(1, 0, false), syntheticFunction(1, math.MaxUint64-1, true),
		syntheticFunction(1, math.MaxUint64, false),
	))
	if len(report.Spans) != 1 || report.Spans[0].Elapsed != "1" ||
		report.Spans[0].End != "18446744073709551615" {
		t.Fatalf("unsigned timestamp precision = %#v", report)
	}
	trace, err := ChromeTrace(report)
	if err != nil {
		t.Fatal(err)
	}
	complete := 0
	for _, event := range trace.TraceEvents {
		if event.Phase == "X" {
			complete++
			if event.Timestamp.String() != "0" || event.Duration.String() != "0.000001" {
				t.Fatalf("exact picosecond conversion = %#v", event)
			}
		}
	}
	if complete != 1 || trace.OtherData.GenerationClocks[0].Origin != "18446744073709551614" ||
		!trace.OtherData.GenerationsNotAligned || trace.OtherData.TargetCPUTime {
		t.Fatalf("trace = %#v", trace)
	}
	encoded, err := json.Marshal(trace)
	if err != nil || !bytes.Contains(encoded, []byte(`"dur":0.000001`)) {
		t.Fatalf("decimal JSON = %s, %v", encoded, err)
	}
}

func TestChromeUsesSeparateGenerationOriginsAndDoesNotTimestampUntimedEvents(t *testing.T) {
	capture := syntheticCapture(t, "8.1.4", 3,
		syntheticContext(1, 0, false), syntheticFunction(1, 2000000, true),
		syntheticEvent(72), syntheticFunction(1, 3000000, false), syntheticEvent(74),
	)
	capture.Generations = append(capture.Generations, Generation{
		ID: 5, Reason: "data_clear",
		PacketsHex: []string{encoded(syntheticContext(1, 0, false)),
			encoded(syntheticFunction(1, 10, true)), encoded(syntheticFunction(1, 20, false))},
	})
	trace, err := ChromeTrace(normalizeFor(t, capture))
	if err != nil {
		t.Fatal(err)
	}
	var spans []TraceEvent
	for _, event := range trace.TraceEvents {
		if event.Phase == "X" {
			spans = append(spans, event)
		}
		if event.Phase == "i" && (event.Name == "event" || event.Name == "marker" || event.Name == "undecodable_data") {
			t.Fatal("untimed event received a made-up timestamp")
		}
	}
	if len(spans) != 2 || spans[0].PID == spans[1].PID ||
		spans[0].Timestamp != "0" || spans[1].Timestamp != "0" ||
		len(trace.OtherData.UntimedDiscontinuities) != 2 {
		t.Fatalf("generation tracks = %#v", trace)
	}
}

func TestNormalizeRejectsInvalidProvenanceABIAndBoundsWithoutPayloads(t *testing.T) {
	base := syntheticCapture(t, "8.1.4", 3, syntheticContext(1, 0, false))
	mutations := []func(*Capture){
		func(c *Capture) { c.ABI.PointerBits = 32 },
		func(c *Capture) { c.Source.SourceID = "private\npath" },
		func(c *Capture) { c.Source.Kind = "live"; c.Source.SessionID = 0 },
		func(c *Capture) { c.Source.Kind = "snapshot"; c.Source.SessionID = 1 },
		func(c *Capture) { c.Source.ImageSHA256 = "private-secret" },
		func(c *Capture) { c.Source.InitialContext = &Context{"01", "0"} },
		func(c *Capture) { c.Generations[0].ID = 0 },
		func(c *Capture) { c.Generations[0].Reason = "private-secret" },
		func(c *Capture) { c.Generations[0].PacketsHex[0] = "private-secret" },
		func(c *Capture) {
			c.Generations[0].FirstIndex = math.MaxUint64
			c.Generations[0].PacketsHex = []string{encoded(syntheticEvent(0)), encoded(syntheticEvent(0))}
		},
	}
	data, _ := json.Marshal(base)
	for _, mutate := range mutations {
		var capture Capture
		if err := json.Unmarshal(data, &capture); err != nil {
			t.Fatal(err)
		}
		mutate(&capture)
		if _, err := Normalize(capture); err == nil {
			t.Fatal("invalid capture accepted")
		} else if strings.Contains(err.Error(), "private") {
			t.Fatalf("private input echoed: %v", err)
		}
	}
}

func TestNormalizeStackDepthLimit(t *testing.T) {
	capture := syntheticCapture(t, "8.1.4", 3, syntheticContext(1, 0, false))
	for index := 0; index <= MaxContextDepth; index++ {
		capture.Generations[0].PacketsHex = append(capture.Generations[0].PacketsHex, encoded(syntheticFunction(1, uint64(index), true)))
	}
	if _, err := Normalize(capture); err == nil {
		t.Fatal("unbounded recursive stack accepted")
	}
}

func TestCanonicalCaptureDigestTracksSourceAndContents(t *testing.T) {
	capture := syntheticCapture(t, "8.1.4", 3, syntheticContext(1, 0, false))
	first := normalizeFor(t, capture)
	data, err := json.MarshalIndent(capture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeCapture(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	second := normalizeFor(t, decoded)
	if first.CanonicalCaptureSHA256 != second.CanonicalCaptureSHA256 {
		t.Fatal("canonical digest changed with JSON whitespace")
	}
	capture.Source.SourceID = "different-explicit-source"
	if normalizeFor(t, capture).CanonicalCaptureSHA256 == first.CanonicalCaptureSHA256 {
		t.Fatal("canonical digest did not bind source")
	}
}

func FuzzDecodeCapture(f *testing.F) {
	profile, _ := ProfileForVersion("8.1.4")
	fixture := Capture{
		Schema: CaptureSchema, MultiVersion: profile.Version, ABI: profile.ABI,
		Source:      Source{Kind: "synthetic_fixture", SourceID: "synthetic-fuzz"},
		InfoHex:     encoded(syntheticInfo(profile, 3)),
		Generations: []Generation{{ID: 1, Reason: "initial", PacketsHex: []string{encoded(syntheticEvent(0))}}},
	}
	data, _ := json.Marshal(fixture)
	f.Add(data)
	f.Add([]byte(`{"schema":"bad","schema":"bad"}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		capture, err := DecodeCapture(bytes.NewReader(data))
		if err != nil {
			return
		}
		report, err := Normalize(capture)
		if err != nil {
			return
		}
		if len(report.Observations) > MaxCapturePackets || report.NativeRuntimeQualified || report.HardwareQualified {
			t.Fatal("invalid bounds or evidence")
		}
		if _, err := json.Marshal(report); err != nil {
			t.Fatal(err)
		}
	})
}
