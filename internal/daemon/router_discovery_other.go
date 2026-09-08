//go:build !windows

package daemon

import (
	"context"
	"errors"

	"github.com/Tacrolimus/multi-dap/internal/bridge"
)

func discoverWarmRouters(context.Context, string) ([]bridge.ServiceRouter, error) {
	return nil, errors.New("daemon: automatic service-router discovery is only supported on Windows")
}
