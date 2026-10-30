//go:build !windows

package hostprocess

import (
	"context"
	"testing"
)

func TestInventoryExplicitUnsupportedPlatform(t *testing.T) {
	report := Collect(context.Background(), "/local/multi")
	if report.State != StateUnsupported || report.Reason != "platform_unsupported" ||
		report.Scope != ScopeConfiguredInstallation || report.Scanned != 0 ||
		report.Unresolved != 0 || report.Truncated || report.Processes == nil || len(report.Processes) != 0 {
		t.Fatalf("unsupported report = %#v", report)
	}
}
