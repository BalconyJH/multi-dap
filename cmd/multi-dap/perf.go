package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Tacrolimus/multi-dap/internal/control"
	"github.com/Tacrolimus/multi-dap/internal/perf"
)

type proxyPerfOptions struct {
	output    string
	maxEvents int
}

func validateProxyPerfOptions(output string, maxEvents int) (proxyPerfOptions, error) {
	output = strings.TrimSpace(output)
	if output == "" {
		return proxyPerfOptions{}, errors.New("--perf-output is required with performance options")
	}
	if !filepath.IsAbs(output) {
		return proxyPerfOptions{}, errors.New("--perf-output must be an absolute path")
	}
	if maxEvents < 1 || maxEvents > perf.MaxEvents {
		return proxyPerfOptions{}, fmt.Errorf("--perf-max-events must be in 1..%d", perf.MaxEvents)
	}
	return proxyPerfOptions{output: filepath.Clean(output), maxEvents: maxEvents}, nil
}

func runPerfProxy(ctx context.Context, record control.Record, options proxyPerfOptions, input io.Reader, output io.Writer) error {
	recorder, err := perf.NewRecorder(options.maxEvents)
	if err != nil {
		return err
	}
	// Reserve the private output before dialing. O_EXCL/CREATE_NEW rejects
	// existing files, including links, and the open descriptor remains owned
	// by this invocation until capture finalization.
	file, err := createPerfFile(options.output)
	if err != nil {
		return fmt.Errorf("performance capture: create output: %w", err)
	}
	proxyErr := control.Proxy(ctx, record, io.TeeReader(input, recorder.ClientWriter()), io.MultiWriter(recorder.ServerWriter(), output))
	return finishPerfTrace(file, recorder.Finish(proxyErr != nil), proxyErr)
}

func finishPerfTrace(file *os.File, trace perf.Trace, proxyErr error) error {
	var exportErr error
	if err := json.NewEncoder(file).Encode(trace); err != nil {
		exportErr = fmt.Errorf("performance capture: write output: %w", err)
	} else if err := file.Sync(); err != nil {
		exportErr = fmt.Errorf("performance capture: sync output: %w", err)
	}
	if err := file.Close(); err != nil {
		exportErr = errors.Join(exportErr, fmt.Errorf("performance capture: close output: %w", err))
	}
	return errors.Join(proxyErr, exportErr)
}
