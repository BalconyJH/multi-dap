package dap

type StepArguments struct {
	ThreadID     int32  `json:"threadId"`
	SingleThread bool   `json:"singleThread,omitempty"`
	Granularity  string `json:"granularity,omitempty"`
}
