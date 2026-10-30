//go:build !windows

package hostprocess

import "context"

// Collect explicitly reports that this Windows process diagnostic is absent.
// It does not inspect another platform or imply an empty Windows inventory.
func Collect(_ context.Context, _ string) Report {
	return emptyReport(StateUnsupported, "platform_unsupported")
}
