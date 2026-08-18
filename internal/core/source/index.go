package source

import (
	"debug/dwarf"
	"debug/elf"
	"fmt"
	"path"
	"sort"
	"strings"
)

// Index answers exactly one question: which cores contain a given source
// file (architecture.md §6.6). It does not build a symbol table and does
// not map lines to addresses - MULTI already does that, and bp_set(file#line)
// goes through MULTI.
//
// It is built once per session by scanning each configured core's ELF DWARF
// line-table file names via debug/elf and debug/dwarf.
type Index struct {
	files map[string]map[int]struct{}

	// Errors collects every failure encountered while scanning: an ELF
	// that would not open, a file with no usable DWARF data, or a single
	// compilation unit that failed to parse. One bad CU must not cost the
	// whole index, so scanning continues past every entry here rather
	// than aborting BuildIndex.
	Errors []error
}

// BuildIndex opens each core's ELF, walks every compilation unit's DWARF
// line-table file list, canonicalizes each file name against its CU's
// DW_AT_comp_dir, and records the core id under that key.
//
// A core whose ELF cannot be opened or does not contain DWARF data, or a
// compilation unit that fails to parse, is skipped: its error is appended
// to the returned Index's Errors field and scanning continues with the
// next core or unit. BuildIndex itself only returns a non-nil error for a
// caller-side mistake (a nil/empty cores map), never for a scanning
// failure - scanning failures are Errors, not the return error, precisely
// so a single bad ELF or CU cannot cost the rest of the index.
func BuildIndex(cores map[int]string) (*Index, error) {
	idx := &Index{files: make(map[string]map[int]struct{})}

	ids := make([]int, 0, len(cores))
	for id := range cores {
		ids = append(ids, id)
	}
	sort.Ints(ids)

	for _, id := range ids {
		idx.scanCore(id, cores[id])
	}
	return idx, nil
}

func (idx *Index) scanCore(core int, elfPath string) {
	f, err := elf.Open(elfPath)
	if err != nil {
		idx.Errors = append(idx.Errors, fmt.Errorf("core %d: open %s: %w", core, elfPath, err))
		return
	}
	defer f.Close()

	data, err := f.DWARF()
	if err != nil {
		idx.Errors = append(idx.Errors, fmt.Errorf("core %d: %s: reading DWARF data: %w", core, elfPath, err))
		return
	}

	r := data.Reader()
	for {
		entry, err := r.Next()
		if err != nil {
			idx.Errors = append(idx.Errors, fmt.Errorf("core %d: %s: reading .debug_info: %w", core, elfPath, err))
			return
		}
		if entry == nil {
			return
		}
		if entry.Tag != dwarf.TagCompileUnit && entry.Tag != dwarf.TagPartialUnit {
			continue
		}
		idx.scanUnit(core, elfPath, data, r, entry)
	}
}

// scanUnit records every line-table file name of one compilation unit. It
// always calls r.SkipChildren so a CU whose line table cannot be read still
// leaves the Reader positioned correctly for the next CU.
func (idx *Index) scanUnit(core int, elfPath string, data *dwarf.Data, r *dwarf.Reader, entry *dwarf.Entry) {
	defer r.SkipChildren()

	compDir, _ := entry.Val(dwarf.AttrCompDir).(string)

	lr, err := data.LineReader(entry)
	if err != nil {
		idx.Errors = append(idx.Errors, fmt.Errorf(
			"core %d: %s: CU at info offset %#x: line reader: %w", core, elfPath, entry.Offset, err))
		return
	}
	if lr == nil {
		return // this CU has no line table.
	}

	for _, lf := range lr.Files() {
		if lf == nil || lf.Name == "" {
			continue
		}
		key := canonicalAgainst(compDir, lf.Name)
		if key == "" {
			continue
		}
		idx.add(key, core)
	}
}

func (idx *Index) add(key string, core int) {
	set, ok := idx.files[key]
	if !ok {
		set = make(map[int]struct{})
		idx.files[key] = set
	}
	set[core] = struct{}{}
}

// CoresFor returns the sorted list of core ids whose DWARF line table
// contains key. key must already be in Canonicalize'd form.
func (idx *Index) CoresFor(key string) []int {
	set := idx.files[key]
	if len(set) == 0 {
		return nil
	}
	cores := make([]int, 0, len(set))
	for c := range set {
		cores = append(cores, c)
	}
	sort.Ints(cores)
	return cores
}

// Keys returns every canonical file key the index knows about, in no
// particular order.
func (idx *Index) Keys() []string {
	keys := make([]string, 0, len(idx.files))
	for k := range idx.files {
		keys = append(keys, k)
	}
	return keys
}

// canonicalAgainst resolves a DWARF line-table file name against the
// compilation unit's DW_AT_comp_dir and returns its Canonicalize'd key.
//
// debug/dwarf's LineReader already joins a file name against the line
// table's own directory list (whose entry 0 is comp_dir), so lf.Name is
// often already fully resolved. It is only joined against compDir here
// when it is still relative, which covers line tables that leave comp_dir
// out of the directory list entirely.
func canonicalAgainst(compDir, name string) string {
	full := strings.ReplaceAll(name, `\`, "/")
	if !isAbsPath(full) && compDir != "" {
		full = path.Join(strings.ReplaceAll(compDir, `\`, "/"), full)
	}
	return Canonicalize(full)
}

func isAbsPath(p string) bool {
	if strings.HasPrefix(p, "/") {
		return true
	}
	// Windows drive-letter absolute form, e.g. "C:/proj/src/main.c".
	if len(p) >= 3 && p[1] == ':' && (p[2] == '/' || p[2] == '\\') {
		return true
	}
	return false
}
