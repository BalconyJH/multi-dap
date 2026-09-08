package dap

// Source is the DAP presentation of one source file. SourceReference is kept
// for protocol completeness, but multi-dap currently accepts only path-backed
// sources because source contents are owned by the client workspace.
type Source struct {
	Name            string `json:"name,omitempty"`
	Path            string `json:"path,omitempty"`
	SourceReference int32  `json:"sourceReference,omitempty"`
}

type SourceBreakpoint struct {
	Line         int64  `json:"line"`
	Column       int64  `json:"column,omitempty"`
	Condition    string `json:"condition,omitempty"`
	HitCondition string `json:"hitCondition,omitempty"`
	LogMessage   string `json:"logMessage,omitempty"`
	Mode         string `json:"mode,omitempty"`
}

type SetBreakpointsArguments struct {
	Source         Source             `json:"source"`
	Breakpoints    []SourceBreakpoint `json:"breakpoints,omitempty"`
	Lines          []int64            `json:"lines,omitempty"`
	SourceModified bool               `json:"sourceModified,omitempty"`
}

type Breakpoint struct {
	ID                   int32   `json:"id,omitempty"`
	Verified             bool    `json:"verified"`
	Message              string  `json:"message,omitempty"`
	Source               *Source `json:"source,omitempty"`
	Line                 int64   `json:"line,omitempty"`
	Column               int64   `json:"column,omitempty"`
	EndLine              int64   `json:"endLine,omitempty"`
	EndColumn            int64   `json:"endColumn,omitempty"`
	InstructionReference string  `json:"instructionReference,omitempty"`
	Offset               int64   `json:"offset,omitempty"`
}

type SetBreakpointsBody struct {
	Breakpoints []Breakpoint `json:"breakpoints"`
}

// SetExceptionBreakpointsArguments is the DAP request shape for configuring
// exception breakpoints. The frontend accepts only an empty configuration: it
// has no backend action because exception breakpoint semantics are not wired.
type SetExceptionBreakpointsArguments struct {
	Filters          []string                 `json:"filters,omitempty"`
	FilterOptions    []ExceptionFilterOptions `json:"filterOptions,omitempty"`
	ExceptionOptions []ExceptionOptions       `json:"exceptionOptions,omitempty"`
}

type ExceptionFilterOptions struct {
	Filter    string `json:"filter"`
	Condition string `json:"condition,omitempty"`
}

type ExceptionOptions struct {
	Path      []ExceptionPathSegment `json:"path"`
	BreakMode string                 `json:"breakMode"`
}

type ExceptionPathSegment struct {
	Names  []string `json:"names"`
	Negate bool     `json:"negate,omitempty"`
}
