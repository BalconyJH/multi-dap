package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const syntheticProfileMapping = `{
  "schema":"multi-dap.profile-mapping.v1",
  "multi_version":"8.1.4",
  "export_surface":"profile_csvexport",
  "view":"functions",
  "mode":"history_performance",
  "provenance":"synthetic_fixture",
  "filter_scope":"unknown",
  "name_style":"long",
  "list_layout":"flat",
  "headers":["Label","Seconds","Occurrences"],
  "identity":{"label":"Label"},
  "line_basis":"unavailable",
  "missing_tokens":[],
  "missing_policy":"reject",
  "metrics":[
    {"name":"time","column":"Seconds","semantic":"time","unit":"s","attribution":"vendor_unspecified"},
    {"name":"calls","column":"Occurrences","semantic":"observed_calls","unit":"count","attribution":"vendor_unspecified"}
  ]
}`

func syntheticProfileFiles(t *testing.T) (string, string, string) {
	t.Helper()
	dir := t.TempDir()
	input := filepath.Join(dir, "private-export.csv")
	mapping := filepath.Join(dir, "private-mapping.json")
	output := filepath.Join(dir, "private-report.json")
	if err := os.WriteFile(input, []byte("Label,Seconds,Occurrences\nPRIVATE_FUNCTION,0.125,18446744073709551615\nOTHER_FUNCTION,0.5,2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mapping, []byte(syntheticProfileMapping), 0o600); err != nil {
		t.Fatal(err)
	}
	return input, mapping, output
}

func TestProfileImportProducesPrivateReportWithoutDaemon(t *testing.T) {
	input, mapping, output := syntheticProfileFiles(t)
	var stdout bytes.Buffer
	if err := run(context.Background(), []string{
		"profile", "import", "--input", input, "--mapping", mapping, "--output", output,
	}, &stdout); err != nil {
		t.Fatal(err)
	}
	var status struct {
		Event           string `json:"event"`
		Rows            int    `json:"rows"`
		FormatStatus    string `json:"format_status"`
		VendorQualified bool   `json:"vendor_qualified"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.Event != "profile_imported" || status.Rows != 2 ||
		status.FormatStatus != "operator_mapped" || status.VendorQualified {
		t.Fatalf("status = %#v", status)
	}
	for _, secret := range []string{"PRIVATE_FUNCTION", "OTHER_FUNCTION", input, mapping, output} {
		if strings.Contains(stdout.String(), secret) {
			t.Fatal("profile CLI disclosed private analysis data")
		}
	}
	report, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(report) || !bytes.Contains(report, []byte("PRIVATE_FUNCTION")) ||
		!bytes.Contains(report, []byte("18446744073709551615")) ||
		!bytes.Contains(report, []byte("synthetic_fixture")) {
		t.Fatal("private report lost the source identity, provenance, or exact count")
	}
}

func TestProfileImportRejectsInvalidInputWithoutCreatingArtifact(t *testing.T) {
	for _, scenario := range []string{"headers", "number", "mapping", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			input, mapping, output := syntheticProfileFiles(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch scenario {
			case "headers":
				if err := os.WriteFile(input, []byte("PRIVATE_WRONG_HEADER\nPRIVATE_CONTENT\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "number":
				if err := os.WriteFile(input, []byte("Label,Seconds,Occurrences\nPRIVATE_CONTENT,NaN,1\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "mapping":
				if err := os.WriteFile(mapping, []byte(`{"PRIVATE_UNKNOWN_KEY":"PRIVATE_CONTENT"}`), 0o600); err != nil {
					t.Fatal(err)
				}
			case "cancelled":
				cancel()
			}
			var stdout bytes.Buffer
			err := runProfile(ctx, []string{"import", "--input", input, "--mapping", mapping, "--output", output}, &stdout)
			if err == nil || stdout.Len() != 0 {
				t.Fatalf("invalid import returned %v and stdout length %d", err, stdout.Len())
			}
			for _, secret := range []string{"PRIVATE_WRONG_HEADER", "PRIVATE_UNKNOWN_KEY", "PRIVATE_CONTENT", input, mapping, output} {
				if strings.Contains(err.Error(), secret) {
					t.Fatal("profile import error disclosed private input")
				}
			}
			if _, err := os.Stat(output); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("invalid import created an output: %v", err)
			}
		})
	}
}

func TestProfileImportDoesNotOverwriteOutputOrItsInput(t *testing.T) {
	input, mapping, output := syntheticProfileFiles(t)
	if err := os.WriteFile(output, []byte("existing analysis"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{output, input, mapping} {
		before, err := os.ReadFile(target)
		if err != nil {
			t.Fatal(err)
		}
		var stdout bytes.Buffer
		err = runProfile(context.Background(), []string{"import", "--input", input, "--mapping", mapping, "--output", target}, &stdout)
		if err == nil || stdout.Len() != 0 {
			t.Fatal("existing output was accepted")
		}
		after, err := os.ReadFile(target)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatal("existing artifact changed")
		}
	}
}

func TestProfileImportFlagAndInputBoundaries(t *testing.T) {
	input, mapping, output := syntheticProfileFiles(t)
	for _, arguments := range [][]string{
		nil,
		{"capture"},
		{"import", "--input", input, "--mapping", mapping, "--output", "relative.json"},
		{"import", "--input", "relative.csv", "--mapping", mapping, "--output", output},
		{"import", "--input", input, "--mapping", "relative.json", "--output", output},
		{"import", "--input", input, "--mapping", mapping, "--output", output, "extra"},
		{"import", "--input", input, "--mapping", mapping, "--output", output, "--connect"},
	} {
		if err := runProfile(context.Background(), arguments, &bytes.Buffer{}); err == nil {
			t.Fatalf("profile import accepted invalid flags %q", arguments)
		}
	}
	if _, err := openAnalysisInput(filepath.Dir(input)); err == nil {
		t.Fatal("analysis accepted a directory")
	}
	var stdout bytes.Buffer
	missing := filepath.Join(filepath.Dir(input), "PRIVATE_MISSING_FILE")
	err := runProfile(context.Background(), []string{"import", "--input", missing, "--mapping", mapping, "--output", output}, &stdout)
	if err == nil || strings.Contains(err.Error(), missing) || strings.Contains(err.Error(), "PRIVATE_MISSING_FILE") {
		t.Fatalf("missing input error = %v", err)
	}
}

func TestAnalysisOutputFailureRetainsItsCauseWithoutPrintingPaths(t *testing.T) {
	path := filepath.Join(t.TempDir(), "PRIVATE_PARENT_MISSING", "PRIVATE_OUTPUT.json")
	err := writeAnalysisReport(path, struct{}{})
	if err == nil || !errors.Is(err, os.ErrNotExist) ||
		strings.Contains(err.Error(), "PRIVATE_") {
		t.Fatalf("output failure = %v", err)
	}
}
