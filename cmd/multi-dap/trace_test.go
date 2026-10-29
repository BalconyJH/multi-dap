package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func syntheticNativeCapture(timebase uint32) map[string]any {
	info := make([]byte, 64)
	binary.LittleEndian.PutUint32(info[8:], timebase)
	binary.LittleEndian.PutUint32(info[24:], 1)
	if timebase == 2 {
		binary.LittleEndian.PutUint32(info, 1)
	}
	copy(info[32:], "synthetic-only")
	pid := make([]byte, 32)
	pid[0] = 5
	binary.LittleEndian.PutUint64(pid[16:], 17)
	binary.LittleEndian.PutUint64(pid[24:], 23)
	function := func(kind uint32, tag uint64) string {
		packet := make([]byte, 32)
		packet[0] = 10
		binary.LittleEndian.PutUint64(packet[8:], 0x1234)
		binary.LittleEndian.PutUint32(packet[16:], kind)
		binary.LittleEndian.PutUint64(packet[24:], tag)
		return hex.EncodeToString(packet)
	}
	return map[string]any{
		"schema":        "multi-dap.native-capture.v1",
		"multi_version": "8.1.4",
		"abi": map[string]any{
			"byte_order": "little", "pointer_bits": 64, "info_size": 64, "packet_size": 32,
			"packet_alignment": 8, "type_offset": 0, "reconstructed_offset": 1, "address_offset": 8, "payload_offset": 16,
		},
		"source": map[string]any{
			"kind": "synthetic_fixture", "session_id": 0, "source_id": "PRIVATE_CAPTURE_SOURCE", "core_id": nil,
		},
		"info_hex": hex.EncodeToString(info),
		"generations": []any{map[string]any{
			"id": "1", "reason": "initial", "first_index": "0",
			"packets_hex": []string{
				hex.EncodeToString(pid), function(0, 18446744073000000000), function(1, 18446744073100000000),
			},
		}},
	}
}

func writeSyntheticNativeCapture(t *testing.T, capture any) (string, string, string) {
	t.Helper()
	dir := t.TempDir()
	input, output, chrome := filepath.Join(dir, "input.json"), filepath.Join(dir, "report.json"), filepath.Join(dir, "trace.json")
	data, err := json.Marshal(capture)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(input, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return input, output, chrome
}

func TestTraceNormalizeExportsPrivatePhysicalTimeWithoutDaemon(t *testing.T) {
	input, output, chrome := writeSyntheticNativeCapture(t, syntheticNativeCapture(3))
	var stdout bytes.Buffer
	if err := run(context.Background(), []string{
		"trace", "normalize", "--input", input, "--output", output, "--chrome-output", chrome,
	}, &stdout); err != nil {
		t.Fatal(err)
	}
	var status struct {
		Event             string `json:"event"`
		VendorQualified   bool   `json:"vendor_qualified"`
		HardwareQualified bool   `json:"hardware_qualified"`
		ChromeWritten     bool   `json:"chrome_written"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.Event != "native_trace_normalized" || !status.ChromeWritten || status.VendorQualified || status.HardwareQualified {
		t.Fatalf("status = %#v", status)
	}
	for _, secret := range []string{input, output, chrome, "PRIVATE_CAPTURE_SOURCE", "18446744073000000000"} {
		if strings.Contains(stdout.String(), secret) {
			t.Fatal("native analysis disclosed private capture details")
		}
	}
	report, err := os.ReadFile(output)
	if err != nil || !json.Valid(report) || !bytes.Contains(report, []byte("18446744073000000000")) {
		t.Fatalf("native report did not retain exact uint64 timetag: %v", err)
	}
	data, err := os.ReadFile(chrome)
	if err != nil {
		t.Fatal(err)
	}
	var trace struct {
		Events []struct {
			Phase    string      `json:"ph"`
			Duration json.Number `json:"dur"`
		} `json:"traceEvents"`
	}
	if err := json.Unmarshal(data, &trace); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range trace.Events {
		if event.Phase == "X" {
			found = true
			if event.Duration.String() != "100" {
				t.Fatalf("picosecond elapsed duration = %s microseconds, want 100", event.Duration)
			}
		}
	}
	if !found {
		t.Fatal("paired native function events did not produce a span")
	}
}

func TestTraceNormalizeDoesNotInventTimeForCounts(t *testing.T) {
	for _, timebase := range []uint32{1, 2} {
		input, output, chrome := writeSyntheticNativeCapture(t, syntheticNativeCapture(timebase))
		var stdout bytes.Buffer
		err := runTrace(context.Background(), []string{
			"normalize", "--input", input, "--output", output, "--chrome-output", chrome,
		}, &stdout)
		if err == nil || stdout.Len() != 0 {
			t.Fatal("count-based trace became a physical-time trace")
		}
		for _, path := range []string{output, chrome} {
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("invalid time conversion created an artifact: %v", err)
			}
		}
		if err := runTrace(context.Background(), []string{
			"normalize", "--input", input, "--output", output,
		}, &stdout); err != nil {
			t.Fatalf("count-domain normalization failed: %v", err)
		}
	}
}

func TestTraceNormalizeRejectsUnsupportedABIAndCancelledWork(t *testing.T) {
	for _, scenario := range []string{"abi", "schema", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			capture := syntheticNativeCapture(3)
			switch scenario {
			case "abi":
				capture["abi"].(map[string]any)["packet_size"] = 24
			case "schema":
				capture["schema"] = "PRIVATE_UNSUPPORTED_FORMAT"
			}
			input, output, chrome := writeSyntheticNativeCapture(t, capture)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if scenario == "cancelled" {
				cancel()
			}
			var stdout bytes.Buffer
			err := runTrace(ctx, []string{
				"normalize", "--input", input, "--output", output, "--chrome-output", chrome,
			}, &stdout)
			if err == nil || stdout.Len() != 0 {
				t.Fatal("unsupported capture was accepted")
			}
			if strings.Contains(err.Error(), "PRIVATE_") || strings.Contains(err.Error(), input) {
				t.Fatal("native normalization error disclosed capture contents")
			}
			for _, path := range []string{output, chrome} {
				if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("invalid capture created an output: %v", err)
				}
			}
		})
	}
}

func TestTraceNormalizeNeverOverwritesAndRequiresDistinctPaths(t *testing.T) {
	input, output, chrome := writeSyntheticNativeCapture(t, syntheticNativeCapture(3))
	if err := os.WriteFile(output, []byte("preserve existing report"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	err := runTrace(context.Background(), []string{
		"normalize", "--input", input, "--output", output, "--chrome-output", chrome,
	}, &stdout)
	if err == nil || stdout.Len() != 0 {
		t.Fatal("existing native analysis was overwritten")
	}
	data, err := os.ReadFile(output)
	if err != nil || string(data) != "preserve existing report" {
		t.Fatal("existing native analysis changed")
	}
	if _, err := os.Stat(chrome); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed reservation created an unrelated trace")
	}
	if err := runTrace(context.Background(), []string{
		"normalize", "--input", input, "--output", chrome, "--chrome-output", chrome,
	}, &bytes.Buffer{}); err == nil {
		t.Fatal("duplicate output paths accepted")
	}
}
