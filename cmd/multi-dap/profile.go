package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Tacrolimus/multi-dap/internal/profile"
)

// Offline analysis deliberately needs no daemon configuration or MULTI
// connection. It never creates an acquisition as an import prerequisite.
func runProfile(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) == 0 || args[0] != "import" {
		return usageError("profile requires the import subcommand")
	}
	flags := flag.NewFlagSet("profile import", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	inputPath := flags.String("input", "", "absolute path to the exported CSV")
	mappingPath := flags.String("mapping", "", "absolute path to an explicit column mapping")
	outputPath := flags.String("output", "", "new absolute path to a private analysis report")
	if err := flags.Parse(args[1:]); err != nil {
		return usageError("invalid profile import options")
	}
	if flags.NArg() != 0 {
		return usageError("profile import accepts no positional arguments")
	}
	for name, path := range map[string]string{"input": *inputPath, "mapping": *mappingPath, "output": *outputPath} {
		if strings.TrimSpace(path) == "" || !filepath.IsAbs(path) {
			return usageError("profile import --" + name + " requires an absolute path")
		}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	mappingFile, err := openAnalysisInput(*mappingPath)
	if err != nil {
		return &analysisIOError{operation: "open mapping", cause: err}
	}
	mapping, decodeErr := profile.DecodeMapping(contextReader{ctx: ctx, reader: mappingFile})
	closeErr := mappingFile.Close()
	if decodeErr != nil {
		return fmt.Errorf("profile import: %w", decodeErr)
	}
	if closeErr != nil {
		return &analysisIOError{operation: "close mapping", cause: closeErr}
	}
	input, err := openAnalysisInput(*inputPath)
	if err != nil {
		return &analysisIOError{operation: "open input", cause: err}
	}
	report, importErr := profile.Import(contextReader{ctx: ctx, reader: input}, mapping)
	closeErr = input.Close()
	if importErr != nil {
		return fmt.Errorf("profile import: %w", importErr)
	}
	if closeErr != nil {
		return &analysisIOError{operation: "close input", cause: closeErr}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := writeAnalysisReport(*outputPath, report); err != nil {
		return err
	}
	// Symbols, paths, source rows and report contents stay in the private
	// artifact. The terminal gets only an operation outcome and a row count.
	return json.NewEncoder(stdout).Encode(struct {
		Event           string `json:"event"`
		Rows            int    `json:"rows"`
		FormatStatus    string `json:"format_status"`
		VendorQualified bool   `json:"vendor_qualified"`
	}{
		Event: "profile_imported", Rows: len(report.Rows), FormatStatus: "operator_mapped",
	})
}

func openAnalysisInput(path string) (*os.File, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, errors.New("input is not a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	after, err := file.Stat()
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(before, after) {
		_ = file.Close()
		return nil, errors.New("input changed while opening")
	}
	return file, nil
}

func writeAnalysisReport(path string, report any) error {
	file, err := createPerfFile(path)
	if err != nil {
		return &analysisIOError{operation: "create output", cause: err}
	}
	return finishAnalysisReport(file, report)
}

func finishAnalysisReport(file *os.File, report any) error {
	var result error
	if err := json.NewEncoder(file).Encode(report); err != nil {
		result = &analysisIOError{operation: "write output", cause: err}
	} else if err := file.Sync(); err != nil {
		result = &analysisIOError{operation: "sync output", cause: err}
	}
	if err := file.Close(); err != nil {
		result = errors.Join(result, &analysisIOError{operation: "close output", cause: err})
	}
	return result
}

type analysisIOError struct {
	operation string
	cause     error
}

func (e *analysisIOError) Error() string { return "analysis: " + e.operation + " failed" }
func (e *analysisIOError) Unwrap() error { return e.cause }

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(data []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.reader.Read(data)
	if cancellation := r.ctx.Err(); cancellation != nil {
		return 0, cancellation
	}
	return n, err
}
