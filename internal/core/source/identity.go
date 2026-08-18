// Package source implements canonical source-file identity and the
// DWARF-backed source index described in architecture.md §6.6.
//
// Path comparison happens in exactly one place: multi-dap converts DAP
// paths/URIs, DWARF DW_AT_comp_dir/DW_AT_name, and MULTI source paths into
// an Identity before any comparison, and no other module compares path
// strings on its own.
package source

import (
	"path"
	"strings"
)

// Identity is the canonical identity of a source file, as defined by
// architecture.md §6.6. ClientPath is the path as the DAP client refers to
// it, in the negotiated form (path or URI, 0- or 1-based lines/columns are
// resolved at the frontend boundary before an Identity is built). DebugPath
// is the path as DWARF or MULTI refer to it. Key is the canonical
// comparison key derived from Canonicalize; it is what every lookup in the
// source index and the breakpoint store actually compares.
type Identity struct {
	ClientPath string
	DebugPath  string
	Key        string
}

// Canonicalize turns a file path into a comparison key: backslashes become
// forward slashes, "." and ".." segments are resolved, and the result is
// lowercased.
//
// The lowercasing assumption is deliberate and Windows-specific: multi-dap
// only ever runs against a Windows host filesystem (MULTI, the compiler,
// and the daemon all run there), where path comparison is case-insensitive.
// This key is therefore not safe to reuse against a case-sensitive host in
// a future version; that would need a second, host-aware canonicalization
// rule rather than a change to this one.
func Canonicalize(p string) string {
	p = strings.ReplaceAll(p, `\`, "/")
	p = path.Clean(p)
	return strings.ToLower(p)
}
