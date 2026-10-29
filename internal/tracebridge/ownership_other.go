//go:build !windows

package tracebridge

import "os"

type hostTestProcessOwner struct{}

func newProcessOwner() (processOwner, error)          { return hostTestProcessOwner{}, nil }
func (hostTestProcessOwner) Assign(*os.Process) error { return nil }
func (hostTestProcessOwner) Close() error             { return nil }
