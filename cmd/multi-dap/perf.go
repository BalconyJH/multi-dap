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
	"sync"

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
	capture := &proxyPerfCapture{file: file, recorder: recorder}
	proxyErr := control.Proxy(ctx, record, io.TeeReader(input, recorder.ClientWriter()), proxyPerfWriter{capture: capture, output: output})
	return capture.finish(proxyErr)
}

type proxyPerfCapture struct {
	mu       sync.Mutex
	file     *os.File
	recorder *perf.Recorder
	finished bool
	err      error
}

func (c *proxyPerfCapture) finishDisconnect() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.finished {
		return
	}
	trace, ready := c.recorder.FinishOnDisconnect()
	if !ready {
		return
	}
	// The editor may terminate this process as soon as it receives the
	// disconnect response. Sync and close before forwarding its final bytes,
	// and never rewrite a durable capture after that observable boundary.
	c.finished = true
	c.err = finishPerfTrace(c.file, trace, nil)
}

func (c *proxyPerfCapture) finish(proxyErr error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.finished {
		c.finished = true
		c.err = finishPerfTrace(c.file, c.recorder.Finish(proxyErr != nil), nil)
	}
	return errors.Join(proxyErr, c.err)
}

type proxyPerfWriter struct {
	capture *proxyPerfCapture
	output  io.Writer
}

func (w proxyPerfWriter) Write(data []byte) (int, error) {
	_, _ = w.capture.recorder.ServerWriter().Write(data)
	w.capture.finishDisconnect()
	return w.output.Write(data)
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
