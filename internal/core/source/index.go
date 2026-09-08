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

	// configured contains every core declared for this session. complete is
	// true only when that core's entire usable DWARF line-table scan completed.
	// A positive file entry remains evidence even when the scan later becomes
	// incomplete; only a complete scan may prove a non-match absent.
	configured map[int]struct{}
	complete   map[int]bool

	// Errors collects every failure encountered while scanning: an ELF
	// that would not open, a file with no usable DWARF data, or a single
	// compilation unit that failed to parse. One bad CU must not cost the
	// whole index, so scanning continues past every entry here rather
	// than aborting BuildIndex.
	Errors []error
}

// NewIndex creates an empty source-to-core index. It is used by the primary
// MULTI-query path on toolchains such as Green Hills where ELF DWARF is not
// available, and by tests/replay code that already has observed source
// knowledge. BuildIndex remains an opportunistic DWARF fast path.
func NewIndex() *Index {
	return &Index{
		files:      make(map[string]map[int]struct{}),
		configured: make(map[int]struct{}),
		complete:   make(map[int]bool),
	}
}

// RecordKnown caches a positive per-core source lookup. The caller must have
// obtained that fact from an authoritative source (normally a structured
// MULTI operation); this package does not invent source-to-core mappings.
func (idx *Index) RecordKnown(core int, identity Identity) {
	if idx == nil || core < 0 || identity.Key == "" {
		return
	}
	idx.add(identity.Key, core)
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
	idx := NewIndex()

	ids := make([]int, 0, len(cores))
	for id := range cores {
		ids = append(ids, id)
	}
	sort.Ints(ids)

	for _, id := range ids {
		idx.configured[id] = struct{}{}
		idx.scanCore(id, cores[id])
	}
	return idx, nil
}

func (idx *Index) scanCore(core int, elfPath string) {
	// A scan starts uncertain. It becomes definitive only after every
	// compilation unit supplied a non-empty, readable line table and the
	// complete .debug_info traversal reached EOF.
	idx.complete[core] = false
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
	sawUnit := false
	complete := true
	for {
		entry, err := r.Next()
		if err != nil {
			idx.Errors = append(idx.Errors, fmt.Errorf("core %d: %s: reading .debug_info: %w", core, elfPath, err))
			return
		}
		if entry == nil {
			idx.complete[core] = sawUnit && complete
			return
		}
		if entry.Tag != dwarf.TagCompileUnit && entry.Tag != dwarf.TagPartialUnit {
			continue
		}
		sawUnit = true
		if !idx.scanUnit(core, elfPath, data, r, entry) {
			// Retain all positive file evidence already extracted, but no missing
			// file can be classified as absent after a partial scan. Continue so
			// later valid units can still contribute positive evidence.
			complete = false
		}
	}
}

// scanUnit records every line-table file name of one compilation unit. It
// always calls r.SkipChildren so a CU whose line table cannot be read still
// leaves the Reader positioned correctly for the next CU.
func (idx *Index) scanUnit(core int, elfPath string, data *dwarf.Data, r *dwarf.Reader, entry *dwarf.Entry) bool {
	defer r.SkipChildren()

	compDir, _ := entry.Val(dwarf.AttrCompDir).(string)

	lr, err := data.LineReader(entry)
	if err != nil {
		idx.Errors = append(idx.Errors, fmt.Errorf(
			"core %d: %s: CU at info offset %#x: line reader: %w", core, elfPath, entry.Offset, err))
		return false
	}
	if lr == nil {
		idx.Errors = append(idx.Errors, fmt.Errorf(
			"core %d: %s: CU at info offset %#x: no line table", core, elfPath, entry.Offset))
		return false
	}

	files := lr.Files()
	if len(files) == 0 {
		idx.Errors = append(idx.Errors, fmt.Errorf(
			"core %d: %s: CU at info offset %#x: empty line table", core, elfPath, entry.Offset))
		return false
	}
	sawFile := false
	for _, lf := range files {
		if lf == nil || lf.Name == "" {
			continue
		}
		key := canonicalAgainst(compDir, lf.Name)
		if key == "" {
			continue
		}
		idx.add(key, core)
		sawFile = true
	}
	if !sawFile {
		idx.Errors = append(idx.Errors, fmt.Errorf(
			"core %d: %s: CU at info offset %#x: empty line table", core, elfPath, entry.Offset))
		return false
	}
	return true
}

func (idx *Index) add(key string, core int) {
	if idx.files == nil {
		idx.files = make(map[string]map[int]struct{})
	}
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

// Coverage returns one result for every configured core, sorted by core id.
// A positive DWARF hit is always Present. A non-hit is Absent only after a
// complete usable line-table scan; no DWARF, an empty line table, and every
// parse failure remain Unknown. Callers that need atomic multi-core decisions
// must use this method rather than the intentionally lossy CoresFor view.
func (idx *Index) Coverage(key string) []Observation {
	if idx == nil {
		return nil
	}
	cores := make([]int, 0, len(idx.configured))
	for core := range idx.configured {
		cores = append(cores, core)
	}
	sort.Ints(cores)
	observations := make([]Observation, 0, len(cores))
	for _, core := range cores {
		presence := PresenceUnknown
		if _, present := idx.files[key][core]; present {
			presence = PresencePresent
		} else if idx.complete[core] {
			presence = PresenceAbsent
		}
		observations = append(observations, Observation{Core: core, Presence: presence})
	}
	return observations
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
