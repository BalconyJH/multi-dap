// Command dwarfspike is a throwaway diagnostic, committed because its answer
// must be reproducible. It validates the DWARF-scanning risk flagged by
// architecture.md §6.6: whether the DWARF version, DW_AT_comp_dir /
// DW_AT_name forms, and any architecture-specific encoding emitted by the
// target toolchain are handled by Go's debug/elf and debug/dwarf packages.
//
// Usage:
//
//	go run ./cmd/dwarfspike <elf-path> [<elf-path> ...]
//
// For each ELF it prints the ELF class/machine/type, then for every
// compilation unit: the DWARF version, DW_AT_comp_dir, DW_AT_name, the
// number of line-table file entries, and the first ten file names verbatim.
// Every error is printed with the CU offset that produced it; a bad CU does
// not abort the scan of the rest of the file.
package main

import (
	"debug/dwarf"
	"debug/elf"
	"encoding/binary"
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: dwarfspike <elf-path> [<elf-path> ...]")
		os.Exit(2)
	}
	for _, path := range os.Args[1:] {
		scan(path)
	}
}

func scan(path string) {
	fmt.Printf("=== %s ===\n", path)

	f, err := elf.Open(path)
	if err != nil {
		fmt.Printf("elf.Open error: %v\n", err)
		return
	}
	defer f.Close()

	fmt.Printf("ELF class:  %s\n", f.Class)
	fmt.Printf("ELF data:   %s\n", f.Data)
	fmt.Printf("ELF type:   %s\n", f.Type)
	fmt.Printf("ELF machine: %s (raw=%d)\n", f.Machine, uint16(f.Machine))

	// debug/dwarf does not expose the per-unit DWARF version through any
	// exported API (the unit.vers field is unexported), so the raw
	// .debug_info section is walked by hand to recover it. This is the
	// only non-debug/dwarf parsing this tool does; everything else below
	// goes through debug/dwarf as the architecture requires.
	versions, verErr := unitVersions(f)
	if verErr != nil {
		fmt.Printf("raw .debug_info version scan error: %v\n", verErr)
	}

	data, err := f.DWARF()
	if err != nil {
		fmt.Printf("f.DWARF() error: %v\n", err)
		return
	}

	r := data.Reader()
	cuIndex := 0
	for {
		entry, err := r.Next()
		if err != nil {
			fmt.Printf("reader.Next() error (CU #%d): %v\n", cuIndex, err)
			break
		}
		if entry == nil {
			break // end of .debug_info
		}
		if entry.Tag != dwarf.TagCompileUnit && entry.Tag != dwarf.TagPartialUnit {
			continue
		}
		cuIndex++
		off := entry.Offset
		fmt.Printf("--- CU #%d at info offset %#x ---\n", cuIndex, off)

		if v, ok := versions[off]; ok {
			fmt.Printf("  DWARF version: %d\n", v)
		} else {
			fmt.Printf("  DWARF version: unknown (offset %#x not found by raw scan)\n", off)
		}

		name, nameOK := entry.Val(dwarf.AttrName).(string)
		fmt.Printf("  DW_AT_name:     %q (readable=%v)\n", name, nameOK)
		compDir, compDirOK := entry.Val(dwarf.AttrCompDir).(string)
		fmt.Printf("  DW_AT_comp_dir: %q (readable=%v)\n", compDir, compDirOK)

		lr, lrErr := data.LineReader(entry)
		if lrErr != nil {
			fmt.Printf("  LineReader error (CU offset %#x): %v\n", off, lrErr)
			r.SkipChildren()
			continue
		}
		if lr == nil {
			fmt.Printf("  LineReader: nil (no line table for this CU)\n")
			r.SkipChildren()
			continue
		}
		files := lr.Files()
		fmt.Printf("  line-table file entries: %d\n", len(files))
		limit := 10
		if len(files) < limit {
			limit = len(files)
		}
		for i := 0; i < limit; i++ {
			ff := files[i]
			if ff == nil {
				fmt.Printf("  file[%d]: <nil entry>\n", i)
				continue
			}
			fmt.Printf("  file[%d]: %q\n", i, ff.Name)
		}

		r.SkipChildren()
	}
}

// unitVersions walks the raw .debug_info section by hand and returns a map
// from the byte offset of each compilation unit's root DIE (i.e. the offset
// debug/dwarf.Entry.Offset reports for that CU) to the DWARF version found
// in that unit's header. debug/dwarf parses this internally but does not
// export it.
func unitVersions(f *elf.File) (map[dwarf.Offset]uint16, error) {
	sec := f.Section(".debug_info")
	if sec == nil {
		return nil, fmt.Errorf("no .debug_info section")
	}
	raw, err := sec.Data()
	if err != nil {
		return nil, err
	}

	order := binary.ByteOrder(binary.LittleEndian)
	if f.Data == elf.ELFDATA2MSB {
		order = binary.BigEndian
	}

	versions := make(map[dwarf.Offset]uint16)
	off := int64(0)
	n := int64(len(raw))
	for off < n {
		start := off
		if off+4 > n {
			return versions, fmt.Errorf("truncated unit header at offset %#x", start)
		}
		unitLength := int64(order.Uint32(raw[off:]))
		off += 4
		is64 := false
		if uint32(unitLength) == 0xffffffff {
			is64 = true
			if off+8 > n {
				return versions, fmt.Errorf("truncated 64-bit unit length at offset %#x", start)
			}
			unitLength = int64(order.Uint64(raw[off:]))
			off += 8
		}
		unitEnd := off + unitLength
		if unitLength <= 0 || unitEnd > n {
			return versions, fmt.Errorf("bad unit length %d at offset %#x", unitLength, start)
		}

		if off+2 > n {
			return versions, fmt.Errorf("truncated version field at offset %#x", start)
		}
		version := order.Uint16(raw[off:])
		off += 2

		offsetSize := int64(4)
		if is64 {
			offsetSize = 8
		}

		if version >= 5 {
			// unit_type(1) + address_size(1) + abbrev_offset(offsetSize)
			off += 1 + 1 + offsetSize
		} else {
			// abbrev_offset(offsetSize) + address_size(1)
			off += offsetSize + 1
		}
		if off > unitEnd {
			return versions, fmt.Errorf("unit header overruns unit at offset %#x (version=%d)", start, version)
		}

		// The first DIE of the unit begins right here; this is the
		// offset debug/dwarf.Entry.Offset reports for the CU's root DIE.
		versions[dwarf.Offset(off)] = version

		off = unitEnd
	}
	return versions, nil
}
