package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	goruntime "runtime"
	"strings"

	"github.com/Tacrolimus/multi-dap/internal/timemachine"
)

func runTrace(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) > 0 && args[0] == "capture" {
		return runTraceCapture(ctx, args[1:], stdout)
	}
	if len(args) == 0 || args[0] != "normalize" {
		return usageError("trace requires the capture or normalize subcommand")
	}
	flags := flag.NewFlagSet("trace normalize", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	inputPath := flags.String("input", "", "absolute path to a multi-dap native capture")
	outputPath := flags.String("output", "", "new absolute path to a private normalized report")
	chromePath := flags.String("chrome-output", "", "optional new absolute path to a physical-time Chrome trace")
	if err := flags.Parse(args[1:]); err != nil {
		return usageError("invalid trace normalize options")
	}
	if flags.NArg() != 0 {
		return usageError("trace normalize accepts no positional arguments")
	}
	for _, option := range []struct{ name, value string }{
		{"input", *inputPath}, {"output", *outputPath},
	} {
		if strings.TrimSpace(option.value) == "" || !filepath.IsAbs(option.value) {
			return usageError("trace normalize --" + option.name + " requires an absolute path")
		}
	}
	if *chromePath != "" {
		if !filepath.IsAbs(*chromePath) {
			return usageError("trace normalize --chrome-output requires an absolute path")
		}
		left, right := filepath.Clean(*outputPath), filepath.Clean(*chromePath)
		if left == right || goruntime.GOOS == "windows" && strings.EqualFold(left, right) {
			return usageError("trace normalize output paths must be different")
		}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	input, err := openAnalysisInput(*inputPath)
	if err != nil {
		return &analysisIOError{operation: "open input", cause: err}
	}
	capture, decodeErr := timemachine.DecodeCapture(contextReader{ctx: ctx, reader: input})
	closeErr := input.Close()
	if decodeErr != nil {
		return fmt.Errorf("trace normalize: %w", decodeErr)
	}
	if closeErr != nil {
		return &analysisIOError{operation: "close input", cause: closeErr}
	}
	report, err := timemachine.Normalize(capture)
	if err != nil {
		return fmt.Errorf("trace normalize: %w", err)
	}
	var chrome any
	if *chromePath != "" {
		trace, err := timemachine.ChromeTrace(report)
		if err != nil {
			return fmt.Errorf("trace normalize: %w", err)
		}
		chrome = trace
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Reserve all requested outputs before writing either one. A failure can
	// leave reserved/partial private files, but never a false success receipt.
	normalizedFile, err := createPerfFile(*outputPath)
	if err != nil {
		return &analysisIOError{operation: "create output", cause: err}
	}
	if chrome != nil {
		chromeFile, err := createPerfFile(*chromePath)
		if err != nil {
			result := error(&analysisIOError{operation: "create Chrome output", cause: err})
			if closeErr := normalizedFile.Close(); closeErr != nil {
				result = errors.Join(result, &analysisIOError{operation: "close output", cause: closeErr})
			}
			return result
		}
		normalErr := finishAnalysisReport(normalizedFile, report)
		chromeErr := finishAnalysisReport(chromeFile, chrome)
		if err := errors.Join(normalErr, chromeErr); err != nil {
			return err
		}
	} else if err := finishAnalysisReport(normalizedFile, report); err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(struct {
		Event             string `json:"event"`
		VendorQualified   bool   `json:"vendor_qualified"`
		HardwareQualified bool   `json:"hardware_qualified"`
		ChromeWritten     bool   `json:"chrome_written"`
	}{
		Event: "native_trace_normalized", ChromeWritten: chrome != nil,
	})
}
