package dap

type StackTraceArguments struct {
	ThreadID   int32  `json:"threadId"`
	StartFrame uint32 `json:"startFrame,omitempty"`
	Levels     uint32 `json:"levels,omitempty"`
}

type StackFrame struct {
	ID                          int32   `json:"id"`
	Name                        string  `json:"name"`
	Source                      *Source `json:"source,omitempty"`
	Line                        int64   `json:"line"`
	Column                      int64   `json:"column"`
	EndLine                     int64   `json:"endLine,omitempty"`
	EndColumn                   int64   `json:"endColumn,omitempty"`
	InstructionPointerReference string  `json:"instructionPointerReference,omitempty"`
}

type StackTraceBody struct {
	StackFrames []StackFrame `json:"stackFrames"`
	TotalFrames int          `json:"totalFrames,omitempty"`
}

type ScopesArguments struct {
	FrameID int32 `json:"frameId"`
}

type Scope struct {
	Name               string  `json:"name"`
	PresentationHint   string  `json:"presentationHint,omitempty"`
	VariablesReference int32   `json:"variablesReference"`
	NamedVariables     int     `json:"namedVariables,omitempty"`
	IndexedVariables   int     `json:"indexedVariables,omitempty"`
	Expensive          bool    `json:"expensive"`
	Source             *Source `json:"source,omitempty"`
	Line               int64   `json:"line,omitempty"`
	Column             int64   `json:"column,omitempty"`
}

type ScopesBody struct {
	Scopes []Scope `json:"scopes"`
}

type ValueFormat struct {
	Hex bool `json:"hex,omitempty"`
}

type VariablesArguments struct {
	VariablesReference int32       `json:"variablesReference"`
	Filter             string      `json:"filter,omitempty"`
	Start              uint32      `json:"start,omitempty"`
	Count              uint32      `json:"count,omitempty"`
	Format             ValueFormat `json:"format,omitempty"`
}

type Variable struct {
	Name               string `json:"name"`
	Value              string `json:"value"`
	Type               string `json:"type,omitempty"`
	EvaluateName       string `json:"evaluateName,omitempty"`
	VariablesReference int32  `json:"variablesReference"`
	NamedVariables     int    `json:"namedVariables,omitempty"`
	IndexedVariables   int    `json:"indexedVariables,omitempty"`
	MemoryReference    string `json:"memoryReference,omitempty"`
}

// VariablePresentationHint is intentionally limited to the standard DAP
// fields that Debugger Core can prove. Frontends may add new fields to this
// open protocol object; those remain a frontend concern and are not echoed.
type VariablePresentationHint struct {
	Kind       string   `json:"kind,omitempty"`
	Attributes []string `json:"attributes,omitempty"`
	Visibility string   `json:"visibility,omitempty"`
	Lazy       bool     `json:"lazy,omitempty"`
}

type VariablesBody struct {
	Variables []Variable `json:"variables"`
}

type EvaluateArguments struct {
	Expression string      `json:"expression"`
	FrameID    int32       `json:"frameId,omitempty"`
	Context    string      `json:"context,omitempty"`
	Format     ValueFormat `json:"format,omitempty"`
}

type EvaluateBody struct {
	Result             string                    `json:"result"`
	Type               string                    `json:"type,omitempty"`
	PresentationHint   *VariablePresentationHint `json:"presentationHint,omitempty"`
	VariablesReference int32                     `json:"variablesReference"`
	NamedVariables     int                       `json:"namedVariables,omitempty"`
	IndexedVariables   int                       `json:"indexedVariables,omitempty"`
	MemoryReference    string                    `json:"memoryReference,omitempty"`
}
