package daemon

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/Tacrolimus/multi-dap/internal/bridge"
	"github.com/Tacrolimus/multi-dap/internal/config"
	"github.com/Tacrolimus/multi-dap/internal/multi"
)

// warmRouterFinder only inventories listeners owned by the configured MULTI
// installation. It does not identify a target session; that proof is the
// typed warm Open below.
type warmRouterFinder func(context.Context, string) ([]bridge.ServiceRouter, error)

var findWarmRouters warmRouterFinder = discoverWarmRouters

var verifyWarmRouter = func(ctx context.Context, cfg config.Validated, script string, warm WarmSession) error {
	process, err := bridge.Start(ctx, bridge.LaunchSpec{
		Executable: cfg.Multi.Executable, BridgeScript: script,
		RPCPort: cfg.Endpoints.MBP.Port, RPCHost: cfg.Endpoints.MBP.Host,
		ServiceRouter: &bridge.ServiceRouter{Host: warm.ServiceRouterHost, Port: warm.ServiceRouterPort},
		SessionMode:   bridge.SessionModeWarm,
	})
	if err != nil {
		return err
	}
	defer process.Close() // Owns only this temporary mpythonrun child.
	driver, err := multi.NewDriver(process.Client())
	if err != nil {
		return err
	}
	return driver.Open(ctx, multi.OpenRequest{Mode: multi.OpenModeWarm, PrimaryELF: warm.PrimaryELF})
}

// DiscoverWarmSession identifies exactly one service router which can bind the
// configured primary ELF through the Window Register. Neither router process
// names nor listener ports are sufficient evidence on their own.
func DiscoverWarmSession(ctx context.Context, cfg config.Validated, bridgeScript, primaryELF string) (WarmSession, error) {
	primaryELF, err := regularFile(primaryELF, "warm primary ELF")
	if err != nil {
		return WarmSession{}, err
	}
	configured := false
	for _, core := range cfg.Cores {
		if strings.EqualFold(filepath.Clean(core.ELF), primaryELF) {
			configured = true
			break
		}
	}
	if !configured {
		return WarmSession{}, errors.New("daemon: warm primary ELF must exactly match a configured core ELF")
	}
	script, err := regularBridgeScript(bridgeScript)
	if err != nil {
		return WarmSession{}, err
	}
	candidates, err := findWarmRouters(ctx, cfg.Multi.Installation)
	if err != nil {
		return WarmSession{}, err
	}
	matched := make([]WarmSession, 0, 1)
	for _, candidate := range candidates {
		warm := WarmSession{ServiceRouterHost: candidate.Host, ServiceRouterPort: candidate.Port, PrimaryELF: primaryELF}
		if _, _, _, err := sessionAcquisition(Options{Config: cfg, Warm: &warm}); err != nil {
			return WarmSession{}, err
		}
		if err := verifyWarmRouter(ctx, cfg, script, warm); err == nil {
			matched = append(matched, warm)
		}
	}
	switch len(matched) {
	case 1:
		return matched[0], nil
	case 0:
		return WarmSession{}, errors.New("daemon: no configured MULTI service router has one verified primary ELF window")
	default:
		return WarmSession{}, fmt.Errorf("daemon: %d configured MULTI service routers have a verified primary ELF window", len(matched))
	}
}
