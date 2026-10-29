package tracebridge

import "os"

// A processOwner is created before launch and assigned before the helper can
// be admitted for native operations. Windows supplies a kill-on-close job;
// other platforms retain only the portable host-test process boundary.
type processOwner interface {
	Assign(*os.Process) error
	Close() error
}
