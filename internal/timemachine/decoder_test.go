package timemachine

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"math"
	"strings"
	"testing"
)

// These bytes are synthetic interoperability fixtures, not vendor captures or
// evidence of an actual target, probe, DLL call, or trace session.
func syntheticPacket(kind byte, address uint64, payload func([]byte)) []byte {
	data := make([]byte, 32)
	data[0] = kind
	binary.LittleEndian.PutUint64(data[8:], address)
	if payload != nil {
		payload(data[16:])
	}
	return data
}

func encoded(data []byte) string { return hex.EncodeToString(data) }

func syntheticFunction(address, tag uint64, entry bool) []byte {
	return syntheticPacket(10, address, func(payload []byte) {
		if !entry {
			binary.LittleEndian.PutUint32(payload, 1)
		}
		binary.LittleEndian.PutUint64(payload[8:], tag)
	})
}

func syntheticContext(task, space uint64, previous bool) []byte {
	kind := byte(5)
	if previous {
		kind = 13
	}
	return syntheticPacket(kind, 0, func(payload []byte) {
		binary.LittleEndian.PutUint64(payload, task)
		binary.LittleEndian.PutUint64(payload[8:], space)
	})
}

func syntheticEvent(code uint32) []byte {
	return syntheticPacket(2, 0, func(payload []byte) { binary.LittleEndian.PutUint32(payload, code) })
}

func syntheticInfo(profile Profile, timebase uint32) []byte {
	info := make([]byte, profile.ABI.InfoSize)
	binary.LittleEndian.PutUint32(info[8:], timebase)
	return info
}

func profileFor(t *testing.T, version string) Profile {
	t.Helper()
	profile, err := ProfileForVersion(version)
	if err != nil {
		t.Fatal(err)
	}
	return profile
}

func TestProfilesRequireExactABI(t *testing.T) {
	for _, version := range []string{"", "8", "8.1", "8.1.5", "7.1.6", "8.1.4-beta"} {
		if _, err := ProfileForVersion(version); err == nil {
			t.Fatalf("unrecognized version accepted: %q", version)
		}
	}
	for _, version := range []string{"8.1.4", "7.1.6d"} {
		profile := profileFor(t, version)
		if profile.ABI.PacketSize != 32 || profile.ABI.PacketAlignment != 8 ||
			profile.ABI.AddressOffset != 8 || profile.ABI.PayloadOffset != 16 {
			t.Fatalf("ABI = %#v", profile.ABI)
		}
		profile.ABI.PacketSize++
		if _, err := DecodePacket(profile, make([]byte, 32)); err == nil {
			t.Fatal("forged profile accepted")
		}
	}
}

func TestDecodeInfoPreservesLegacyWidthAndNonzeroFlags(t *testing.T) {
	modern := profileFor(t, "8.1.4")
	data := syntheticInfo(modern, 3)
	for _, offset := range []int{0, 4, 12, 16, 20, 24, 28} {
		binary.LittleEndian.PutUint32(data[offset:], 2)
	}
	copy(data[32:], []byte("synthetic-source"))
	info, err := DecodeInfo(modern, data)
	if err != nil {
		t.Fatal(err)
	}
	if !info.HasCycleCounts || !info.HasCacheMisses || !info.HasDataAddresses ||
		!info.HasDataValues || !info.HasUnexecuted || !info.HasVariableLengthInstructions ||
		info.AddressBits != 64 || info.Unit != "picoseconds" || len(info.IdentifierHex) != 64 {
		t.Fatalf("info = %#v", info)
	}
	legacy := profileFor(t, "7.1.6d")
	info, err = DecodeInfo(legacy, syntheticInfo(legacy, 2))
	if err != nil || info.AddressBits != 0 || info.Unit != "cycles" {
		t.Fatalf("legacy info = %#v, %v", info, err)
	}
	unknown := syntheticInfo(modern, 99)
	if info, err := DecodeInfo(modern, unknown); err != nil || info.Unit != "unknown" || info.Timebase != 99 {
		t.Fatalf("unknown timebase = %#v, %v", info, err)
	}
	if _, err := DecodeInfo(modern, data[:60]); err == nil {
		t.Fatal("legacy-sized information accepted by new ABI")
	}
}

func TestDecodeTimetagsStayExactAcrossVersions(t *testing.T) {
	data := syntheticFunction(math.MaxUint64, math.MaxUint64, true)
	for _, test := range []struct {
		version string
		raw     string
		signed  bool
	}{
		{"8.1.4", "18446744073709551615", false},
		{"7.1.6d", "-1", true},
	} {
		packet, err := DecodePacket(profileFor(t, test.version), data)
		if err != nil {
			t.Fatal(err)
		}
		if packet.Tag.Raw != test.raw || packet.Tag.RawBits != "18446744073709551615" ||
			packet.Tag.Signed != test.signed || packet.Address != "0xffffffffffffffff" ||
			packet.Function.Kind != "entry" {
			t.Fatalf("packet = %#v", packet)
		}
		wire, err := json.Marshal(packet)
		if err != nil || !bytes.Contains(wire, []byte(`"raw_bits":"18446744073709551615"`)) {
			t.Fatalf("integer lost in JSON: %s, %v", wire, err)
		}
	}
}

func TestDataMaskFourUsesHeaderContractNotStalePythonExample(t *testing.T) {
	data := syntheticPacket(1, 0, func(payload []byte) {
		payload[8], payload[9] = 4, 8
		binary.LittleEndian.PutUint64(payload, math.MaxUint64)
	})
	for _, test := range []struct{ version, flag string }{{"8.1.4", "failed"}, {"7.1.6d", "fetch"}} {
		packet, err := DecodePacket(profileFor(t, test.version), data)
		if err != nil || len(packet.Data.Flags) != 1 || packet.Data.Flags[0] != test.flag ||
			packet.Data.Value != "18446744073709551615" {
			t.Fatalf("%s packet = %#v, %v", test.version, packet, err)
		}
	}
}

func TestDecodeKnownPayloadsAndRetainUnknownBytes(t *testing.T) {
	profile := profileFor(t, "8.1.4")
	pid, err := DecodePacket(profile, syntheticContext(math.MaxUint64-10, math.MaxUint64-20, true))
	if err != nil || pid.Kind != "previous_pid" || pid.Context.TaskID != "18446744073709551605" {
		t.Fatalf("context packet = %#v, %v", pid, err)
	}
	instruction, err := DecodePacket(profile, syntheticPacket(0, 0x1234, func(payload []byte) {
		binary.LittleEndian.PutUint64(payload, 123)
		binary.LittleEndian.PutUint32(payload[8:], 0xabcdef)
		payload[12], payload[13] = 4, 1
		binary.LittleEndian.PutUint16(payload[14:], 17)
	}))
	if err != nil || instruction.Tag.Raw != "123" || instruction.Instruction.Opcode != 0xabcdef ||
		instruction.Instruction.Cycles != 17 || instruction.Instruction.ArchitectureByte != 4 {
		t.Fatalf("instruction = %#v, %v", instruction, err)
	}
	raw := syntheticPacket(200, 0x1234, func(payload []byte) { copy(payload, []byte("synthetic-packet")) })
	unknown, err := DecodePacket(profile, raw)
	if err != nil || unknown.Kind != "unknown" || unknown.Type != 200 ||
		unknown.PayloadHex != hex.EncodeToString(raw[16:]) || unknown.Tag != nil {
		t.Fatalf("unknown packet = %#v, %v", unknown, err)
	}
	legacyNew, err := DecodePacket(profileFor(t, "7.1.6d"), syntheticContext(1, 2, true))
	if err != nil || legacyNew.Kind != "unknown" || legacyNew.Context != nil {
		t.Fatalf("legacy new packet = %#v, %v", legacyNew, err)
	}
	event, err := DecodePacket(profile, syntheticEvent(200))
	if err != nil || event.Event.Code != 200 || event.Event.Known || !event.Event.Discontinuity {
		t.Fatalf("plugin event = %#v, %v", event, err)
	}
}

func TestWallClockLayoutIsLabeledNotHostCalibration(t *testing.T) {
	const seconds uint64 = 0x0102030405
	const microseconds uint64 = 0x050607
	packet, err := DecodePacket(profileFor(t, "8.1.4"), syntheticPacket(12, 0, func(payload []byte) {
		binary.LittleEndian.PutUint64(payload, math.MaxUint64)
		binary.LittleEndian.PutUint64(payload[8:], seconds|(microseconds<<40))
	}))
	if err != nil {
		t.Fatal(err)
	}
	if packet.WallClock.Seconds != "4328719365" || packet.WallClock.Microseconds != 329223 ||
		!packet.WallClock.FieldsInRange || packet.WallClock.TargetClockVerified ||
		packet.WallClock.LayoutEvidence != "local_x64_header_layout_only" {
		t.Fatalf("wall-clock = %#v", packet.WallClock)
	}
	packet, err = DecodePacket(profileFor(t, "8.1.4"), syntheticPacket(12, 0, func(payload []byte) {
		binary.LittleEndian.PutUint64(payload[8:], math.MaxUint64)
	}))
	if err != nil || packet.WallClock.FieldsInRange {
		t.Fatalf("invalid wall-clock fields = %#v, %v", packet.WallClock, err)
	}
}

func TestDecodePacketAndBatchBounds(t *testing.T) {
	profile := profileFor(t, "8.1.4")
	for _, length := range []int{0, 1, 31, 33} {
		if _, err := DecodePacket(profile, make([]byte, length)); err == nil {
			t.Fatalf("packet length %d accepted", length)
		}
	}
	for _, length := range []int{31, (MaxBatchPackets + 1) * 32} {
		if _, err := DecodePackets(profile, make([]byte, length)); err == nil {
			t.Fatalf("batch length %d accepted", length)
		}
	}
	data := append(syntheticFunction(1, 2, true), syntheticFunction(1, 3, false)...)
	packets, err := DecodePackets(profile, data)
	if err != nil || len(packets) != 2 {
		t.Fatalf("batch = %#v, %v", packets, err)
	}
	for _, data := range [][]byte{
		syntheticPacket(6, 0, func(payload []byte) { binary.LittleEndian.PutUint32(payload, 13) }),
		syntheticPacket(9, 0, func(payload []byte) { payload[8] = 8 }),
	} {
		if _, err := DecodePacket(profile, data); err == nil {
			t.Fatal("invalid payload size accepted")
		}
	}
	badFlag := syntheticPacket(0, 0, nil)
	badFlag[1] = 2
	if _, err := DecodePacket(profile, badFlag); err == nil {
		t.Fatal("invalid reconstructed flag accepted")
	}
}

func TestDecodeCaptureRejectsDuplicateUnknownAndOversizeInput(t *testing.T) {
	capture := syntheticCapture(t, "8.1.4", 3, syntheticContext(1, 0, false))
	data, err := json.Marshal(capture)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeCapture(bytes.NewReader(data))
	if err != nil || decoded.Schema != CaptureSchema {
		t.Fatalf("capture decode = %#v, %v", decoded, err)
	}
	for _, invalid := range []string{
		`null`, `[]`, `{}`,
		string(data) + `{}`,
		strings.Replace(string(data), `"schema":`, `"schema":"duplicate","schema":`, 1),
		strings.Replace(string(data), `"schema":`, `"Schema":`, 1),
		strings.Replace(string(data), `"schema":`, `"Schema":"duplicate","schema":`, 1),
		strings.Replace(string(data), `"abi":{`, `"abi":{"private-secret":1,`, 1),
		strings.Replace(string(data), `"abi":{`, `"ABI":{`, 1),
		strings.Replace(string(data), `"info_size":64`, `"info_size":64,"info_size":64`, 1),
		strings.Replace(string(data), `"id":"1"`, `"id":1`, 1),
		strings.Replace(string(data), `"type_offset":0`, `"type_offset":null`, 1),
		strings.Replace(string(data), `"type_offset":0,`, ``, 1),
		strings.Replace(string(data), `"session_id":0`, `"session_id":null`, 1),
		strings.Replace(string(data), `"session_id":0,`, ``, 1),
		strings.Replace(string(data), `"first_index":"0"`, `"first_index":null`, 1),
		strings.Replace(string(data), `"first_index":"0",`, ``, 1),
	} {
		if _, err := DecodeCapture(strings.NewReader(invalid)); err == nil {
			t.Fatalf("invalid capture accepted: %.40s", invalid)
		} else if strings.Contains(err.Error(), "private-secret") {
			t.Fatal("unknown JSON field leaked into error")
		}
	}
	if _, err := DecodeCapture(bytes.NewReader([]byte{0xff})); err == nil {
		t.Fatal("invalid UTF-8 accepted")
	}
	if _, err := DecodeCapture(strings.NewReader(strings.Repeat(" ", MaxCaptureBytes+1))); err == nil {
		t.Fatal("oversize capture accepted")
	}
}

func FuzzVersionedPacketDecoder(f *testing.F) {
	f.Add(syntheticFunction(0x1000, 99, true), true)
	f.Add(syntheticPacket(200, 0, nil), false)
	f.Fuzz(func(t *testing.T, data []byte, modern bool) {
		version := "7.1.6d"
		if modern {
			version = "8.1.4"
		}
		packet, err := DecodePacket(profileFor(t, version), data)
		if err != nil {
			return
		}
		if len(data) != 32 || len(packet.PayloadHex) != 32 {
			t.Fatal("decoder accepted invalid structure")
		}
		if _, err := json.Marshal(packet); err != nil {
			t.Fatal(err)
		}
	})
}
