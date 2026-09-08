package daemon

import (
	"testing"

	"github.com/Tacrolimus/multi-dap/internal/bridge"
	"github.com/Tacrolimus/multi-dap/internal/config"
	"github.com/Tacrolimus/multi-dap/internal/multi"
)

func TestSessionAcquisitionColdCarriesPreparation(t *testing.T) {
	cfg := testRuntimeConfig()
	cfg.Connection.Preparation = config.ConnectionPreparationAlreadyPresentNoVerify
	mode, router, request, err := sessionAcquisition(Options{Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	if mode != bridge.SessionModeCold || router != nil || request.Mode != multi.OpenModeCold || request.Preparation != multi.ColdPreparationAlreadyPresentNoVerify {
		t.Fatalf("cold acquisition = mode %q router %#v request %#v", mode, router, request)
	}
}

func TestSessionAcquisitionRejectsColdPreparationForWarmSession(t *testing.T) {
	cfg := testRuntimeConfig()
	cfg.Connection.Preparation = config.ConnectionPreparationAlreadyPresentNoVerify
	_, _, _, err := sessionAcquisition(Options{Config: cfg, Warm: &WarmSession{}})
	if err == nil {
		t.Fatal("warm acquisition accepted cold preparation")
	}
}
