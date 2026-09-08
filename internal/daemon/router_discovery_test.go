package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Tacrolimus/multi-dap/internal/bridge"
	"github.com/Tacrolimus/multi-dap/internal/config"
)

func TestDiscoverWarmSessionRequiresExactlyOneVerifiedRouter(t *testing.T) {
	cfg := testRuntimeConfig()
	directory := t.TempDir()
	primary := filepath.Join(directory, "primary.elf")
	if err := os.WriteFile(primary, []byte("elf"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.Cores = []config.ValidatedCore{{ID: 0, ELF: primary}}
	script := testBridgeScript(t)
	previousFinder, previousVerifier := findWarmRouters, verifyWarmRouter
	t.Cleanup(func() { findWarmRouters, verifyWarmRouter = previousFinder, previousVerifier })
	verifyWarmRouter = func(_ context.Context, _ config.Validated, _ string, warm WarmSession) error {
		if warm.ServiceRouterPort == 40124 {
			return errors.New("not this session")
		}
		return nil
	}

	for _, test := range []struct {
		name       string
		candidates []bridge.ServiceRouter
		wantErr    bool
	}{
		{name: "none", wantErr: true},
		{name: "one", candidates: []bridge.ServiceRouter{{Host: "127.0.0.1", Port: 40123}}},
		{name: "two verified", candidates: []bridge.ServiceRouter{{Host: "127.0.0.1", Port: 40123}, {Host: "127.0.0.1", Port: 40125}}, wantErr: true},
		{name: "one of two verified", candidates: []bridge.ServiceRouter{{Host: "127.0.0.1", Port: 40123}, {Host: "127.0.0.1", Port: 40124}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			findWarmRouters = func(context.Context, string) ([]bridge.ServiceRouter, error) { return test.candidates, nil }
			warm, err := DiscoverWarmSession(context.Background(), cfg, script, primary)
			if test.wantErr {
				if err == nil {
					t.Fatal("DiscoverWarmSession() error = nil")
				}
				return
			}
			if err != nil || warm.ServiceRouterPort != 40123 || warm.PrimaryELF != primary {
				t.Fatalf("DiscoverWarmSession() = (%#v, %v)", warm, err)
			}
		})
	}
}
