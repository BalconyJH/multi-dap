// Package version provides build metadata for the multi-dap executable.
package version

import "strings"

// These values are replaced by release builds with -ldflags. They deliberately
// describe the product build only; protocol versions remain owned by their
// respective protocol packages.
var (
	Version   = "0.0.0-dev"
	Commit    = ""
	BuildDate = ""
)

// Info is the stable, machine-readable representation printed by the CLI.
type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit,omitempty"`
	BuildDate string `json:"build_date,omitempty"`
}

func Current() Info {
	return Info{
		Version:   strings.TrimSpace(Version),
		Commit:    strings.TrimSpace(Commit),
		BuildDate: strings.TrimSpace(BuildDate),
	}
}
