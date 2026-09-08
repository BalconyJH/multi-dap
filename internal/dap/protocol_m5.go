package dap

type ReadMemoryArguments struct {
	MemoryReference string `json:"memoryReference"`
	Offset          int64  `json:"offset,omitempty"`
	Count           uint64 `json:"count"`
}

type ReadMemoryBody struct {
	Address         string `json:"address"`
	UnreadableBytes uint64 `json:"unreadableBytes,omitempty"`
	Data            string `json:"data,omitempty"`
}

type WriteMemoryArguments struct {
	MemoryReference string `json:"memoryReference"`
	Offset          int64  `json:"offset,omitempty"`
	AllowPartial    bool   `json:"allowPartial,omitempty"`
	Data            string `json:"data"`
}

type WriteMemoryBody struct {
	Offset       int64  `json:"offset,omitempty"`
	BytesWritten uint32 `json:"bytesWritten,omitempty"`
}

type DisassembleArguments struct {
	MemoryReference   string `json:"memoryReference"`
	Offset            int64  `json:"offset,omitempty"`
	InstructionOffset int64  `json:"instructionOffset,omitempty"`
	InstructionCount  uint32 `json:"instructionCount"`
	ResolveSymbols    bool   `json:"resolveSymbols,omitempty"`
}

type DisassembledInstruction struct {
	Address          string  `json:"address"`
	InstructionBytes string  `json:"instructionBytes,omitempty"`
	Instruction      string  `json:"instruction"`
	Symbol           string  `json:"symbol,omitempty"`
	Location         *Source `json:"location,omitempty"`
	Line             int64   `json:"line,omitempty"`
	Column           int64   `json:"column,omitempty"`
	EndLine          int64   `json:"endLine,omitempty"`
	EndColumn        int64   `json:"endColumn,omitempty"`
}

type DisassembleBody struct {
	Instructions []DisassembledInstruction `json:"instructions"`
}
