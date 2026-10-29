// Package profile imports explicitly mapped CSV observations. It does not
// discover vendor columns, start a debugger, or qualify a vendor export format.
package profile

import "fmt"

const (
	MaxMappingBytes  = 64 << 10
	MaxInputBytes    = 64 << 20
	MaxRows          = 100000
	MaxColumns       = 64
	MaxCellBytes     = 4 << 10
	MaxMetrics       = 32
	MaxCells         = 1000000
	MaxNumericDigits = 128

	maxTokens       = 64
	maxMappingDepth = 16
)

// Error deliberately contains only a fixed category and a data-record index.
// Neither parser errors nor untrusted header, cell, or mapping text is exposed.
// Row zero identifies mapping, header, or input-wide errors.
type Error struct {
	Category string
	Row      int
}

func (e *Error) Error() string {
	if e.Row > 0 {
		return fmt.Sprintf("profile: %s at data row %d", e.Category, e.Row)
	}
	return "profile: " + e.Category
}

func failure(category string) error { return &Error{Category: category} }

func atRow(err error, row int) error {
	if category, ok := err.(*Error); ok {
		return &Error{Category: category.Category, Row: row}
	}
	return &Error{Category: "input_read_failed", Row: row}
}
