package multi

import (
	"fmt"
	"strings"
	"testing"
)

func TestParseM5DisassemblyPinsHardwareLineShapeAndCount(t *testing.T) {
	instructions, err := parseM5Disassembly(
		"0\tjr RESET_PE0 (0x1a44)\n0x4\tnop\n0x6\tnop\n",
		0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(instructions) != 2 || instructions[0].Address != 0 || instructions[0].Text != "jr RESET_PE0 (0x1a44)" ||
		instructions[1].Address != 4 || instructions[1].Text != "nop" {
		t.Fatalf("instructions = %#v", instructions)
	}
}

func TestParseM5DisassemblyAllowsWorstCaseRH850InputWindow(t *testing.T) {
	var raw strings.Builder
	// 86 requested instructions ask GHS for 516 bytes. A stream of 2-byte
	// instructions legitimately produces 258 lines, which must not be confused
	// with the DAP response limit (the parser returns only the first 86).
	for index := 0; index < 258; index++ {
		fmt.Fprintf(&raw, "0x%x\tnop\n", index*2)
	}
	instructions, err := parseM5Disassembly(raw.String(), 0, 86)
	if err != nil || len(instructions) != 86 || instructions[85].Address != 170 {
		t.Fatalf("worst-case parse = (%d, %v)", len(instructions), err)
	}
}

func TestAttachM5InstructionBytesUsesDecodedBoundaries(t *testing.T) {
	instructions := []Instruction{
		{Address: 0, Text: "jr"},
		{Address: 4, Text: "nop"},
		{Address: 6, Text: "sentinel"},
	}
	result, err := attachM5InstructionBytes(instructions, 0, []byte{0x80, 0x07, 0x44, 0x1a, 0x00, 0x00}, 2)
	if err != nil || len(result) != 2 || fmt.Sprint(result[0].Bytes) != "[128 7 68 26]" || fmt.Sprint(result[1].Bytes) != "[0 0]" {
		t.Fatalf("opcode attachment = (%#v, %v)", result, err)
	}
	if _, err := attachM5InstructionBytes([]Instruction{{Address: 0}, {Address: 3}}, 0, []byte{0, 0, 0}, 1); err == nil {
		t.Fatal("invalid RH850 width succeeded")
	}
}

func TestParseM5DisassemblyFailsClosedForUntrustedOutput(t *testing.T) {
	for _, raw := range []string{
		"0 jr RESET_PE0\n",
		"0\tjr RESET_PE0\n0\tnop\n",
		"0x4\tnop\n",
		"0\t\n",
	} {
		if _, err := parseM5Disassembly(raw, 0, 1); err == nil {
			t.Fatalf("parseM5Disassembly(%q) succeeded", raw)
		}
	}
}

func TestM5AddressOffsetIsOverflowSafe(t *testing.T) {
	tests := []struct {
		address uint64
		offset  int64
		want    uint64
		ok      bool
	}{
		{0x1000, 4, 0x1004, true},
		{0x1000, -4, 0xffc, true},
		{0, -1, 0, false},
		{^uint64(0), 1, 0, false},
	}
	for _, test := range tests {
		got, err := addSignedAddressOffset(test.address, test.offset)
		if (err == nil) != test.ok || test.ok && got != test.want {
			t.Fatalf("addSignedAddressOffset(%#x, %d) = (%#x, %v)", test.address, test.offset, got, err)
		}
	}
	if _, err := addSignedAddressOffset(0, -1<<63); err == nil {
		t.Fatal("minimum offset underflow succeeded")
	}
}
