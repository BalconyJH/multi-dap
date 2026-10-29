// Package timemachine decodes explicitly versioned native packet observations.
// It does not load MULTI libraries, connect to sessions, or operate a target.
package timemachine

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
)

const MaxBatchPackets = 4096

type ABI struct {
	ByteOrder           string `json:"byte_order"`
	PointerBits         int    `json:"pointer_bits"`
	InfoSize            int    `json:"info_size"`
	PacketSize          int    `json:"packet_size"`
	PacketAlignment     int    `json:"packet_alignment"`
	TypeOffset          int    `json:"type_offset"`
	ReconstructedOffset int    `json:"reconstructed_offset"`
	AddressOffset       int    `json:"address_offset"`
	PayloadOffset       int    `json:"payload_offset"`
}

type Profile struct {
	Version        string
	ABI            ABI
	SignedTimetags bool
	HasNewPackets  bool
	HasAddressFlag bool
}

// ProfileForVersion selects the two inspected contracts, never a major-version
// approximation. Matching this description does not qualify a native runtime.
func ProfileForVersion(version string) (Profile, error) {
	profile := Profile{
		Version: version,
		ABI: ABI{
			ByteOrder: "little", PacketSize: 32, PacketAlignment: 8,
			TypeOffset: 0, ReconstructedOffset: 1, AddressOffset: 8, PayloadOffset: 16,
		},
	}
	switch version {
	case "8.1.4":
		profile.ABI.PointerBits, profile.ABI.InfoSize = 64, 64
		profile.HasNewPackets, profile.HasAddressFlag = true, true
	case "7.1.6d":
		profile.ABI.PointerBits, profile.ABI.InfoSize = 32, 60
		profile.SignedTimetags = true
	default:
		return Profile{}, errors.New("timemachine: unsupported exact release")
	}
	return profile, nil
}

func validateProfile(profile Profile) error {
	expected, err := ProfileForVersion(profile.Version)
	if err != nil || profile != expected {
		return errors.New("timemachine: profile does not match the versioned ABI")
	}
	return nil
}

type Info struct {
	Timebase                      uint32 `json:"timebase"`
	Unit                          string `json:"unit"`
	HasCycleCounts                bool   `json:"has_cycle_counts"`
	HasCacheMisses                bool   `json:"has_cache_misses"`
	HasDataAddresses              bool   `json:"has_data_addresses"`
	HasDataValues                 bool   `json:"has_data_values"`
	HasUnexecuted                 bool   `json:"has_unexecuted"`
	HasVariableLengthInstructions bool   `json:"has_variable_length_instructions"`
	AddressBits                   int    `json:"address_bits"`
	IdentifierHex                 string `json:"identifier_hex"`
}

func DecodeInfo(profile Profile, data []byte) (Info, error) {
	if err := validateProfile(profile); err != nil {
		return Info{}, err
	}
	if len(data) != profile.ABI.InfoSize {
		return Info{}, errors.New("timemachine: information size does not match ABI")
	}
	word := func(offset int) uint32 { return binary.LittleEndian.Uint32(data[offset:]) }
	info := Info{
		HasCycleCounts: word(0) != 0, HasCacheMisses: word(4) != 0, Timebase: word(8),
		HasDataAddresses: word(12) != 0, HasDataValues: word(16) != 0,
		HasUnexecuted: word(20) != 0,
	}
	identifierOffset := 28
	if profile.HasAddressFlag {
		info.AddressBits = 32
		if word(24) != 0 {
			info.AddressBits = 64
		}
		info.HasVariableLengthInstructions = word(28) != 0
		identifierOffset = 32
	} else {
		// The legacy information structure has no address-width assertion.
		info.HasVariableLengthInstructions = word(24) != 0
	}
	info.IdentifierHex = hex.EncodeToString(data[identifierOffset : identifierOffset+32])
	switch info.Timebase {
	case 1:
		info.Unit = "instructions"
	case 2:
		info.Unit = "cycles"
	case 3:
		info.Unit = "picoseconds"
	default:
		info.Unit = "unknown"
	}
	return info, nil
}

type Context struct {
	TaskID         string `json:"task_id"`
	AddressSpaceID string `json:"address_space_id"`
}

type Timetag struct {
	Raw     string `json:"raw"`
	RawBits string `json:"raw_bits"`
	Signed  bool   `json:"signed"`
}

type Instruction struct {
	Opcode           uint32 `json:"opcode"`
	ArchitectureByte uint8  `json:"architecture_byte"`
	Executed         uint8  `json:"executed"`
	Cycles           uint16 `json:"cycles"`
}

type DataAccess struct {
	Value        string   `json:"value"`
	Mask         uint8    `json:"mask"`
	Flags        []string `json:"flags"`
	Size         uint8    `json:"size"`
	Uncorrelated uint8    `json:"uncorrelated"`
}

type NativeEvent struct {
	Code          uint32 `json:"code"`
	Name          string `json:"name"`
	Known         bool   `json:"known"`
	Discontinuity bool   `json:"discontinuity"`
}

type FunctionEvent struct {
	Type uint32 `json:"type"`
	Kind string `json:"kind"`
}

type RegisterValue struct {
	Value  string `json:"value"`
	Number uint32 `json:"number"`
	Size   uint8  `json:"size"`
	Access uint8  `json:"access"`
}

type RawTrace struct {
	Size   uint8  `json:"size"`
	Hidden bool   `json:"hidden"`
	Start  bool   `json:"start"`
	Data   string `json:"data_hex"`
}

type WallClock struct {
	Seconds             string `json:"seconds"`
	Microseconds        uint32 `json:"microseconds"`
	FieldsInRange       bool   `json:"fields_in_range"`
	LayoutEvidence      string `json:"layout_evidence"`
	TargetClockVerified bool   `json:"target_clock_verified"`
}

type Packet struct {
	Type           uint8          `json:"type"`
	Kind           string         `json:"kind"`
	Reconstructed  bool           `json:"reconstructed"`
	Address        string         `json:"address"`
	AddressMeaning string         `json:"address_meaning"`
	PayloadHex     string         `json:"payload_hex"`
	Tag            *Timetag       `json:"tag,omitempty"`
	Instruction    *Instruction   `json:"instruction,omitempty"`
	Data           *DataAccess    `json:"data,omitempty"`
	Event          *NativeEvent   `json:"event,omitempty"`
	Context        *Context       `json:"context,omitempty"`
	Function       *FunctionEvent `json:"function,omitempty"`
	Register       *RegisterValue `json:"register,omitempty"`
	RawTrace       *RawTrace      `json:"raw_trace,omitempty"`
	OpcodeHex      string         `json:"opcode_hex,omitempty"`
	WallClockRaw   string         `json:"wall_clock_raw,omitempty"`
	WallClock      *WallClock     `json:"wall_clock,omitempty"`
}

func DecodePacket(profile Profile, data []byte) (Packet, error) {
	if err := validateProfile(profile); err != nil {
		return Packet{}, err
	}
	if len(data) != profile.ABI.PacketSize {
		return Packet{}, errors.New("timemachine: packet size does not match ABI")
	}
	if data[1] > 1 {
		return Packet{}, errors.New("timemachine: invalid reconstructed flag")
	}
	payload := data[16:32]
	word := func(offset int) uint32 { return binary.LittleEndian.Uint32(payload[offset:]) }
	wide := func(offset int) uint64 { return binary.LittleEndian.Uint64(payload[offset:]) }
	packet := Packet{
		Type: data[0], Kind: "unknown", Reconstructed: data[1] != 0,
		Address:        fmt.Sprintf("0x%016x", binary.LittleEndian.Uint64(data[8:16])),
		AddressMeaning: "unspecified_raw_field",
		PayloadHex:     hex.EncodeToString(payload),
	}
	tag := func(value uint64) *Timetag {
		decimal := strconv.FormatUint(value, 10)
		result := &Timetag{Raw: decimal, RawBits: decimal, Signed: profile.SignedTimetags}
		if profile.SignedTimetags {
			result.Raw = strconv.FormatInt(int64(value), 10)
		}
		return result
	}
	switch packet.Type {
	case 0:
		packet.Kind, packet.Tag = "instruction", tag(wide(0))
		packet.AddressMeaning = "instruction_address"
		packet.Instruction = &Instruction{
			Opcode: word(8), ArchitectureByte: payload[12],
			Executed: payload[13], Cycles: binary.LittleEndian.Uint16(payload[14:]),
		}
	case 1:
		packet.Kind = "data"
		packet.AddressMeaning = "data_address_availability_requires_info_and_mask"
		packet.Data = &DataAccess{
			Value: strconv.FormatUint(wide(0), 10), Mask: payload[8],
			Flags: accessFlags(profile, payload[8]), Size: payload[9], Uncorrelated: payload[10],
		}
	case 2:
		packet.Kind, packet.Event = "event", decodeEvent(profile, word(0))
	case 3:
		packet.Kind = "raw_data"
	case 4:
		packet.Kind = "raw_data_start"
	case 5:
		packet.Kind = "pid"
		packet.Context = &Context{TaskID: strconv.FormatUint(wide(0), 10), AddressSpaceID: strconv.FormatUint(wide(8), 10)}
	case 6:
		packet.Kind = "extended_opcode"
		size := word(0)
		if size > 12 {
			return Packet{}, errors.New("timemachine: extended opcode exceeds payload")
		}
		packet.OpcodeHex = hex.EncodeToString(payload[4 : 4+size])
	case 8:
		packet.Kind = "register"
		packet.Register = &RegisterValue{
			Value: strconv.FormatUint(wide(0), 10), Number: word(8), Size: payload[12], Access: payload[13],
		}
	case 9:
		packet.Kind, packet.Tag = "raw_trace", tag(wide(0))
		size := payload[8] & 0x3f
		if size > 7 {
			return Packet{}, errors.New("timemachine: raw trace size exceeds payload")
		}
		packet.RawTrace = &RawTrace{
			Size: size, Hidden: payload[8]&0x80 != 0, Start: payload[8]&0x40 != 0,
			Data: hex.EncodeToString(payload[9 : 9+size]),
		}
	case 10:
		packet.Kind, packet.Tag = "function", tag(wide(8))
		packet.AddressMeaning = "vendor_example_function_address_unqualified"
		packet.Function = &FunctionEvent{Type: word(0), Kind: "unknown"}
		if word(0) == 0 {
			packet.Function.Kind = "entry"
		} else if word(0) == 1 {
			packet.Function.Kind = "exit"
		}
	case 11:
		packet.Kind, packet.Tag = "address_sync", tag(wide(0))
		packet.AddressMeaning = "vendor_example_sync_address_unqualified"
	case 12:
		if profile.HasNewPackets {
			packet.Kind, packet.Tag = "wall_clock_sync", tag(wide(0))
			packet.WallClockRaw = strconv.FormatUint(wide(8), 10)
			// This matches a local x64 C-header layout probe. It is not
			// evidence of target clock synchronization or DLL qualification.
			microseconds := uint32(wide(8) >> 40)
			packet.WallClock = &WallClock{
				Seconds: strconv.FormatUint(wide(8)&((1<<40)-1), 10), Microseconds: microseconds,
				FieldsInRange: microseconds < 1000000, LayoutEvidence: "local_x64_header_layout_only",
			}
		}
	case 13:
		if profile.HasNewPackets {
			packet.Kind = "previous_pid"
			packet.Context = &Context{TaskID: strconv.FormatUint(wide(0), 10), AddressSpaceID: strconv.FormatUint(wide(8), 10)}
		}
	}
	return packet, nil
}

func DecodePackets(profile Profile, data []byte) ([]Packet, error) {
	if err := validateProfile(profile); err != nil {
		return nil, err
	}
	if len(data)%profile.ABI.PacketSize != 0 || len(data)/profile.ABI.PacketSize > MaxBatchPackets {
		return nil, errors.New("timemachine: invalid or oversized packet batch")
	}
	result := make([]Packet, 0, len(data)/profile.ABI.PacketSize)
	for offset := 0; offset < len(data); offset += profile.ABI.PacketSize {
		packet, err := DecodePacket(profile, data[offset:offset+profile.ABI.PacketSize])
		if err != nil {
			return nil, err
		}
		result = append(result, packet)
	}
	return result, nil
}

func accessFlags(profile Profile, mask uint8) []string {
	names := []string{"read", "write", "failed", "data_unavailable", "address_unavailable", "address_partial", "coprocessor", "cache_miss"}
	if profile.SignedTimetags {
		names[2] = "fetch"
	}
	result := make([]string, 0, len(names))
	for bit, name := range names {
		if mask&(1<<bit) != 0 {
			result = append(result, name)
		}
	}
	return result
}

func decodeEvent(profile Profile, code uint32) *NativeEvent {
	event := &NativeEvent{Code: code, Name: "unknown_event", Discontinuity: true}
	names := map[uint32]string{
		0: "trigger", 1: "trace_disabled", 2: "fifo_overflow", 3: "debug_mode", 4: "reset",
		28: "pc_changed", 30: "context_trace_disabled", 31: "out_of_sync",
		32: "unknown_address_space", 50: "untraced_functions", 51: "untraced_callback",
		63: "error", 64: "warning", 65: "return_from_exception",
	}
	if name, ok := names[code]; ok {
		event.Name, event.Known = name, true
	}
	if profile.HasNewPackets {
		newNames := map[uint32]string{
			66: "data_overflow_start", 67: "data_overflow_end", 68: "memory_from_target",
			69: "protocol_error", 70: "sleep", 71: "task_started",
			72: "marker", 73: "overlap_failed", 74: "undecodable_data",
		}
		if name, ok := newNames[code]; ok {
			event.Name, event.Known = name, true
		}
	}
	// Only explicitly harmless markers preserve a function interval. Native
	// exception/context semantics need qualification before weakening this.
	event.Discontinuity = !(code == 0 || (profile.HasNewPackets && code == 72))
	return event
}
