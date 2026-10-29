package profile

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
)

func syntheticMapping() Mapping {
	return Mapping{
		Schema: MappingSchema, MultiVersion: "8.1.4", ExportSurface: "profile_csvexport",
		View: "functions", Mode: "pc_sampling", Provenance: "synthetic_fixture",
		FilterScope: "unknown", NameStyle: "unknown", ListLayout: "flat",
		Headers: []string{"entity", "value"}, Identity: IdentityMapping{Label: "entity"},
		LineBasis: "unavailable", MissingTokens: []string{"NA"}, MissingPolicy: "reject",
		Metrics: []MetricMapping{{
			Name: "samples", Column: "value", Semantic: "sample_count",
			Unit: "count", Attribution: "vendor_unspecified",
		}},
	}
}

func mappingJSON(t *testing.T, mapping Mapping) []byte {
	t.Helper()
	data, err := json.Marshal(mapping)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func errorCategory(t *testing.T, err error, want string) *Error {
	t.Helper()
	var failure *Error
	if !errors.As(err, &failure) || failure.Category != want {
		t.Fatalf("error = %v, want category %s", err, want)
	}
	return failure
}

func requireValue(t *testing.T, row Row, metric, want string) {
	t.Helper()
	value := row.Values[metric]
	if value == nil || *value != want {
		t.Fatalf("row %d metric %s = %v, want %q", row.RowIndex, metric, value, want)
	}
}

func TestSyntheticFixtureImportPreservesEvidenceAndExactValues(t *testing.T) {
	mappingBytes, err := os.ReadFile("testdata/synthetic-mapping.json")
	if err != nil {
		t.Fatal(err)
	}
	csv, err := os.ReadFile("testdata/synthetic-functions.csv")
	if err != nil {
		t.Fatal(err)
	}
	mapping, err := DecodeMapping(bytes.NewReader(mappingBytes))
	if err != nil {
		t.Fatal(err)
	}
	report, err := Import(bytes.NewReader(csv), mapping)
	if err != nil {
		t.Fatal(err)
	}
	if report.Schema != ReportSchema || report.Provenance.Format != "operator_mapped" ||
		report.Provenance.Source != "synthetic_fixture" || report.Provenance.VendorQualified ||
		len(report.Rows) != 3 || len(report.Metrics) != 4 || len(report.Rankings) != 3 {
		t.Fatalf("report = %#v", report)
	}
	if report.Rows[0].Identity.Label != report.Rows[1].Identity.Label ||
		report.Rows[0].RowIndex != 1 || report.Rows[1].RowIndex != 2 ||
		report.Rows[0].SourceLine != 2 || report.Rows[1].SourceLine != 3 {
		t.Fatal("same-name rows were merged or lost their source identity")
	}
	requireValue(t, report.Rows[0], "self_time", "1.250")
	requireValue(t, report.Rows[0], "calls", "18446744073709551616")
	requireValue(t, report.Rows[1], "calls", "18446744073709551617")
	requireValue(t, report.Rows[1], "coverage", "partial")
	if report.Rows[2].Values["self_time"] != nil {
		t.Fatal("missing time was zero-filled")
	}
	if !reflect.DeepEqual(report.Rankings[0].RowIndices, []int{2, 1}) ||
		!reflect.DeepEqual(report.Rankings[0].MissingRowIndices, []int{3}) ||
		!reflect.DeepEqual(report.Rankings[1].RowIndices, []int{2, 1, 3}) {
		t.Fatalf("rankings = %#v", report.Rankings)
	}
	if report.Metrics[1].Meaning != "occurrences" || report.Mapping.Mode != "history_performance" {
		t.Fatal("History occurrences were mislabeled as exact function calls")
	}
	wantInputHash := sha256.Sum256(csv)
	if report.InputSHA256 != hex.EncodeToString(wantInputHash[:]) || report.InputBytes != int64(len(csv)) {
		t.Fatal("input hash or byte count does not bind actual CSV bytes")
	}
	_, canonical, err := canonicalMapping(mapping)
	if err != nil {
		t.Fatal(err)
	}
	wantMappingHash := sha256.Sum256(canonical)
	if report.MappingSHA256 != hex.EncodeToString(wantMappingHash[:]) ||
		report.MappingHashKind != "canonical_json" || report.RowIndexBasis != "data_record_1_based" {
		t.Fatal("mapping hash semantics or row index basis is missing")
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{`"vendor_qualified":false`, `"self_time":null`, `"18446744073709551617"`} {
		if !bytes.Contains(encoded, []byte(required)) {
			t.Fatalf("report is missing %s", required)
		}
	}
	for _, forbidden := range []string{`"timeline"`, `"callgraph"`, `"total_time"`} {
		if bytes.Contains(encoded, []byte(forbidden)) {
			t.Fatalf("report invented %s", forbidden)
		}
	}
}

func TestMappingJSONIsStrictAndErrorsAreSanitized(t *testing.T) {
	base := string(mappingJSON(t, syntheticMapping()))
	cases := []struct {
		name, input, category string
	}{
		{"duplicate top-level", strings.Replace(base, `"schema":`, `"schema":"private-secret","schema":`, 1), "mapping_duplicate_key"},
		{"escaped duplicate", strings.Replace(base, `"schema":`, `"sch\u0065ma":"private-secret","schema":`, 1), "mapping_duplicate_key"},
		{"duplicate identity", strings.Replace(base, `"label":"entity"`, `"label":"entity","label":"private-secret"`, 1), "mapping_duplicate_key"},
		{"duplicate metric", strings.Replace(base, `"name":"samples"`, `"name":"samples","name":"private-secret"`, 1), "mapping_duplicate_key"},
		{"unknown top-level", strings.TrimSuffix(base, "}") + `,"private-secret":true}`, "mapping_unknown_or_invalid_field"},
		{"case alias top-level", strings.TrimSuffix(base, "}") + `,"Schema":"multi-dap.profile-mapping.v1"}`, "mapping_unknown_or_invalid_field"},
		{"unearned qualification", strings.TrimSuffix(base, "}") + `,"qualified":true}`, "mapping_unknown_or_invalid_field"},
		{"unknown identity", strings.Replace(base, `"label":"entity"`, `"label":"entity","private-secret":"ignored"`, 1), "mapping_unknown_or_invalid_field"},
		{"case alias identity", strings.Replace(base, `"label":"entity"`, `"Label":"entity"`, 1), "mapping_unknown_or_invalid_field"},
		{"unknown metric", strings.Replace(base, `"name":"samples"`, `"name":"samples","private-secret":true`, 1), "mapping_unknown_or_invalid_field"},
		{"case alias metric", strings.Replace(base, `"name":"samples"`, `"Name":"samples"`, 1), "mapping_unknown_or_invalid_field"},
		{"wrong type", strings.Replace(base, `"allow_utf8_bom":false`, `"allow_utf8_bom":"private-secret"`, 1), "mapping_unknown_or_invalid_field"},
		{"null option", strings.Replace(base, `"allow_utf8_bom":false`, `"allow_utf8_bom":null`, 1), "mapping_unknown_or_invalid_field"},
		{"null token", strings.Replace(base, `"missing_tokens":["NA"]`, `"missing_tokens":[null]`, 1), "mapping_unknown_or_invalid_field"},
		{"trailing object", base + `{"private-secret":true}`, "mapping_trailing_json"},
		{"trailing garbage", base + "private-secret", "mapping_trailing_json"},
		{"invalid syntax", `{"private-secret":`, "mapping_invalid_json"},
		{"invalid utf8", string([]byte{'{', '"', 0xff, '"', ':', '1', '}'}), "mapping_invalid_utf8"},
		{"mapping BOM", "\xef\xbb\xbf" + base, "mapping_invalid_json"},
		{"too large", strings.Repeat(" ", MaxMappingBytes) + base, "mapping_size_limit"},
		{"too deep", strings.Repeat("[", maxMappingDepth+2) + "0" + strings.Repeat("]", maxMappingDepth+2), "mapping_depth_limit"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			_, err := DecodeMapping(strings.NewReader(test.input))
			errorCategory(t, err, test.category)
			if strings.Contains(err.Error(), "private-secret") {
				t.Fatalf("error leaks input: %v", err)
			}
		})
	}
}

func TestMappingTokenMapDuplicateIsRejected(t *testing.T) {
	mapping := syntheticMapping()
	mapping.Metrics[0] = MetricMapping{
		Name: "state", Column: "value", Semantic: "coverage_state", Unit: "state",
		Attribution: "vendor_unspecified", TokenMap: map[string]string{"ok": "covered"},
	}
	data := strings.Replace(string(mappingJSON(t, mapping)), `"ok":"covered"`, `"ok":"covered","ok":"uncovered"`, 1)
	_, err := DecodeMapping(strings.NewReader(data))
	errorCategory(t, err, "mapping_duplicate_key")
}

func TestMappingNormalizesOnlyVersionAndCanonicalJSON(t *testing.T) {
	mapping := syntheticMapping()
	mapping.MultiVersion = "v8.1.4"
	compact := mappingJSON(t, mapping)
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, compact, "", "  "); err != nil {
		t.Fatal(err)
	}
	first, err := DecodeMapping(bytes.NewReader(compact))
	if err != nil {
		t.Fatal(err)
	}
	second, err := DecodeMapping(&pretty)
	if err != nil {
		t.Fatal(err)
	}
	if first.MultiVersion != "8.1.4" || !reflect.DeepEqual(first, second) {
		t.Fatalf("mappings = %#v / %#v", first, second)
	}
	one, err := Import(strings.NewReader("entity,value\nsame,1\n"), first)
	if err != nil {
		t.Fatal(err)
	}
	two, err := Import(strings.NewReader("entity,value\nsame,1\n"), second)
	if err != nil {
		t.Fatal(err)
	}
	if one.MappingSHA256 != two.MappingSHA256 {
		t.Fatal("insignificant mapping JSON whitespace changed canonical hash")
	}
}

func TestMappingValidationRejectsUnsupportedAndAmbiguousContracts(t *testing.T) {
	cases := []struct {
		name, category string
		change         func(*Mapping)
	}{
		{"schema", "mapping_schema", func(m *Mapping) { m.Schema = "other" }},
		{"version", "mapping_multi_version", func(m *Mapping) { m.MultiVersion = "latest" }},
		{"surface", "mapping_export_surface", func(m *Mapping) { m.ExportSurface = "guessed" }},
		{"view", "mapping_view", func(m *Mapping) { m.View = "tree" }},
		{"mode", "mapping_mode", func(m *Mapping) { m.Mode = "unknown" }},
		{"provenance", "mapping_provenance", func(m *Mapping) { m.Provenance = "vendor_qualified" }},
		{"filter", "mapping_filter_scope", func(m *Mapping) { m.FilterScope = "auto" }},
		{"name style", "mapping_name_style", func(m *Mapping) { m.NameStyle = "auto" }},
		{"layout", "mapping_list_layout", func(m *Mapping) { m.ListLayout = "tree" }},
		{"no headers", "mapping_headers", func(m *Mapping) { m.Headers = []string{} }},
		{"duplicate headers", "mapping_duplicate_header", func(m *Mapping) { m.Headers = []string{"entity", "entity"} }},
		{"missing label", "mapping_identity", func(m *Mapping) { m.Identity.Label = "" }},
		{"unknown label", "mapping_identity", func(m *Mapping) { m.Identity.Label = "not-a-header" }},
		{"unknown role", "mapping_identity", func(m *Mapping) { m.Identity.Task = "not-a-header" }},
		{"no line role", "mapping_line_basis", func(m *Mapping) { m.LineBasis = "absolute" }},
		{"no line basis", "mapping_line_basis", func(m *Mapping) { m.Identity.Line = "value" }},
		{"missing policy", "mapping_missing_policy", func(m *Mapping) { m.MissingPolicy = "fill_zero" }},
		{"duplicate missing tokens", "mapping_missing_tokens", func(m *Mapping) { m.MissingTokens = []string{"NA", "NA"} }},
		{"no metrics", "mapping_metrics", func(m *Mapping) { m.Metrics = []MetricMapping{} }},
		{"metric name", "mapping_metric_name", func(m *Mapping) { m.Metrics[0].Name = "private/path" }},
		{"metric column", "mapping_metric_column", func(m *Mapping) { m.Metrics[0].Column = "not-a-header" }},
		{"metric semantic", "mapping_metric_semantic", func(m *Mapping) { m.Metrics[0].Semantic = "cpu_utilization" }},
		{"metric unit", "mapping_metric_unit", func(m *Mapping) { m.Metrics[0].Unit = "float" }},
		{"attribution", "mapping_attribution", func(m *Mapping) { m.Metrics[0].Attribution = "exclusive" }},
		{"count denominator", "mapping_percent_denominator", func(m *Mapping) { m.Metrics[0].Denominator = "recording" }},
		{"count token map", "mapping_token_map", func(m *Mapping) { m.Metrics[0].TokenMap = map[string]string{"one": "covered"} }},
		{"duplicate metric name", "mapping_metric_name", func(m *Mapping) {
			m.Headers = append(m.Headers, "second")
			m.Metrics = append(m.Metrics, MetricMapping{Name: "samples", Column: "second", Semantic: "time", Unit: "ns", Attribution: "self"})
		}},
		{"duplicate metric column", "mapping_metric_column", func(m *Mapping) {
			m.Metrics = append(m.Metrics, MetricMapping{Name: "other", Column: "value", Semantic: "time", Unit: "ns", Attribution: "self"})
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			mapping := syntheticMapping()
			test.change(&mapping)
			_, err := DecodeMapping(bytes.NewReader(mappingJSON(t, mapping)))
			errorCategory(t, err, test.category)
			_, err = Import(strings.NewReader("entity,value\nitem,1\n"), mapping)
			errorCategory(t, err, test.category)
		})
	}
}

func TestUnitAndDenominatorCombinations(t *testing.T) {
	cases := []struct {
		semantic, unit, denominator string
		valid                       bool
	}{
		{"time", "s", "", true}, {"time", "ms", "", true}, {"time", "us", "", true}, {"time", "ns", "", true},
		{"time", "count", "", false}, {"time", "percent", "recording", false},
		{"time_percent", "percent", "recording", true}, {"time_percent", "percent", "filter", true},
		{"time_percent", "percent", "", false}, {"time_percent", "percent", "entity_instructions", false},
		{"instruction_percent", "percent", "recording", true}, {"instruction_percent", "percent", "filter", true},
		{"instruction_percent", "percent", "entity_instructions", true},
		{"coverage_percent", "percent", "entity_instructions", true}, {"coverage_percent", "percent", "recording", false},
		{"coverage_percent", "count", "entity_instructions", false},
		{"instruction_count", "count", "", true}, {"observed_calls", "count", "", true},
		{"sample_count", "count", "", true}, {"uncovered_instructions", "count", "", true},
		{"sample_count", "count", "recording", false}, {"observed_calls", "ns", "", false},
	}
	for _, test := range cases {
		t.Run(test.semantic+"-"+test.unit+"-"+test.denominator, func(t *testing.T) {
			mapping := syntheticMapping()
			mapping.Metrics[0].Semantic, mapping.Metrics[0].Unit, mapping.Metrics[0].Denominator = test.semantic, test.unit, test.denominator
			_, err := DecodeMapping(bytes.NewReader(mappingJSON(t, mapping)))
			if (err == nil) != test.valid {
				t.Fatalf("valid=%t, error=%v", test.valid, err)
			}
		})
	}
}

func TestExactDecimalRankingAndStableTies(t *testing.T) {
	mapping := syntheticMapping()
	mapping.Metrics[0].Semantic, mapping.Metrics[0].Unit, mapping.Metrics[0].Attribution = "time", "us", "inclusive"
	csv := "entity,value\nsame,9007199254740993.0000000000000001\nsame,9007199254740993.0000000000000002\nsame,0001.2500\nsame,1.25\nsame,0\nsame,000.000\n"
	report, err := Import(strings.NewReader(csv), mapping)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(report.Rankings[0].RowIndices, []int{2, 1, 3, 4, 5, 6}) {
		t.Fatalf("ranking = %#v", report.Rankings)
	}
	requireValue(t, report.Rows[0], "samples", "9007199254740993.0000000000000001")
	if report.Metrics[0].Attribution != "inclusive" {
		t.Fatal("inclusive attribution was lost")
	}
	// A ranking is not a sum; no inclusive or percentage aggregate is emitted.
	encoded, _ := json.Marshal(report)
	if bytes.Contains(encoded, []byte(`"total"`)) || bytes.Contains(encoded, []byte(`"sum"`)) {
		t.Fatal("importer invented an aggregate")
	}
}

func TestNumbersRejectLocaleExponentSignsAndPrecisionOverflow(t *testing.T) {
	for _, value := range []string{
		"-1", "+1", "1e3", "NaN", "Inf", "nan", " 1", "1 ", "1%", "1,000",
		"1_000", ".5", "1.", "1.2.3", "", "\u0661", strings.Repeat("1", MaxNumericDigits+1),
	} {
		t.Run(fmt.Sprintf("%q", value), func(t *testing.T) {
			mapping := syntheticMapping()
			mapping.Metrics[0].Semantic, mapping.Metrics[0].Unit = "time", "s"
			quoted := `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
			_, err := Import(strings.NewReader("entity,value\nitem,"+quoted+"\n"), mapping)
			if len(value) > MaxNumericDigits {
				errorCategory(t, err, "numeric_precision_limit")
			} else {
				errorCategory(t, err, "invalid_number")
			}
		})
	}
	mapping := syntheticMapping()
	for _, value := range []string{"1.0", "0.25"} {
		_, err := Import(strings.NewReader("entity,value\nitem,"+value+"\n"), mapping)
		errorCategory(t, err, "invalid_number")
	}
	report, err := Import(strings.NewReader("entity,value\nitem,"+strings.Repeat("9", MaxNumericDigits)+"\n"), mapping)
	if err != nil || len(report.Rows) != 1 {
		t.Fatalf("maximum exact integer rejected: %v", err)
	}
}

func TestPercentageRangeIsExact(t *testing.T) {
	mapping := syntheticMapping()
	mapping.Metrics[0].Semantic = "time_percent"
	mapping.Metrics[0].Unit = "percent"
	mapping.Metrics[0].Denominator = "recording"
	for _, value := range []string{"0", "0.000", "99.999999999999999999999", "100", "0100.000"} {
		if _, err := Import(strings.NewReader("entity,value\nitem,"+value+"\n"), mapping); err != nil {
			t.Fatalf("valid percentage %s: %v", value, err)
		}
	}
	for _, value := range []string{"100.000000000000000000001", "101", "999999999999999999999"} {
		_, err := Import(strings.NewReader("entity,value\nitem,"+value+"\n"), mapping)
		errorCategory(t, err, "percentage_range")
	}
}

func TestExplicitMissingTokensRejectOrPreserveWithoutZeroFilling(t *testing.T) {
	mapping := syntheticMapping()
	_, err := Import(strings.NewReader("entity,value\nitem,NA\n"), mapping)
	errorCategory(t, err, "missing_value")
	mapping.MissingPolicy = "preserve_missing"
	mapping.MissingTokens = []string{"NA", ""}
	report, err := Import(strings.NewReader("entity,value\nfirst,NA\nsecond,\nzero,0\n"), mapping)
	if err != nil {
		t.Fatal(err)
	}
	if report.Rows[0].Values["samples"] != nil || report.Rows[1].Values["samples"] != nil ||
		!reflect.DeepEqual(report.Rankings[0].MissingRowIndices, []int{1, 2}) ||
		!reflect.DeepEqual(report.Rankings[0].RowIndices, []int{3}) {
		t.Fatalf("report = %#v", report)
	}
	requireValue(t, report.Rows[2], "samples", "0")
	_, err = Import(strings.NewReader("entity,value\nitem,N/A\n"), mapping)
	errorCategory(t, err, "invalid_number")
}

func TestCoverageStateUsesOnlyExplicitTokensAndNeverNumericRanking(t *testing.T) {
	mapping := syntheticMapping()
	mapping.Mode = "trace_coverage"
	mapping.Metrics[0] = MetricMapping{
		Name: "coverage", Column: "value", Semantic: "coverage_state", Unit: "state",
		Attribution: "vendor_unspecified", TokenMap: map[string]string{"Y": "covered", "P": "partial", "N": "uncovered"},
	}
	report, err := Import(strings.NewReader("entity,value\nfirst,Y\nsecond,P\nthird,N\n"), mapping)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Rankings) != 0 {
		t.Fatal("coverage states were assigned invented numeric ranks")
	}
	requireValue(t, report.Rows[0], "coverage", "covered")
	requireValue(t, report.Rows[1], "coverage", "partial")
	requireValue(t, report.Rows[2], "coverage", "uncovered")
	_, err = Import(strings.NewReader("entity,value\nfirst,y\n"), mapping)
	errorCategory(t, err, "coverage_token")
	for _, tokens := range []map[string]string{
		nil, {"Y": "unknown"}, {"": "covered"}, {"NA": "covered"},
	} {
		mapping.Metrics[0].TokenMap = tokens
		_, err := Import(strings.NewReader("entity,value\nfirst,Y\n"), mapping)
		errorCategory(t, err, "mapping_token_map")
	}
}

func TestIdentityRolesLineBasisAndNoNameMerging(t *testing.T) {
	mapping := syntheticMapping()
	mapping.Headers = []string{"entity", "value", "space", "task", "file", "function", "address", "line"}
	mapping.Identity = IdentityMapping{
		Label: "entity", Addrspace: "space", Task: "task", File: "file",
		Function: "function", Address: "address", Line: "line",
	}
	mapping.LineBasis = "function_relative"
	csv := "entity,value,space,task,file,function,address,line\nsame,1,AS1,T1,file.c,work,0xffffffffffffffff,0\nsame,1,AS2,T2,other.c,work,0x1234,1\n"
	report, err := Import(strings.NewReader(csv), mapping)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Rows) != 2 || *report.Rows[0].Identity.Addrspace != "AS1" ||
		*report.Rows[1].Identity.Task != "T2" || *report.Rows[0].Identity.File != "file.c" ||
		*report.Rows[0].Identity.Function != "work" || *report.Rows[0].Identity.Address != "0xffffffffffffffff" ||
		*report.Rows[0].Identity.Line != "0" || report.Mapping.LineBasis != "function_relative" {
		t.Fatalf("rows = %#v", report.Rows)
	}
	mapping.LineBasis = "absolute"
	_, err = Import(strings.NewReader(csv), mapping)
	errorCategory(t, err, "line_number")
	mapping.LineBasis = "function_relative"
	for _, invalid := range []string{"-1", "1.5", "relative", "3-1", "1-", "1--2", "1-2-3", "1 - 3", "1-2.5"} {
		_, err = Import(strings.NewReader(strings.Replace(csv, ",0\n", ","+invalid+"\n", 1)), mapping)
		errorCategory(t, err, "line_number")
	}
}

func TestSourceLineRangesPreserveExplicitBasis(t *testing.T) {
	for _, basis := range []string{"function_relative", "absolute"} {
		mapping := syntheticMapping()
		mapping.Headers = append(mapping.Headers, "line")
		mapping.Identity.Line, mapping.LineBasis = "line", basis
		for _, line := range []string{"1-3", "7-7", "18446744073709551616-18446744073709551617"} {
			report, err := Import(strings.NewReader("entity,value,line\nsame,1,"+line+"\n"), mapping)
			if err != nil || len(report.Rows) != 1 || report.Rows[0].Identity.Line == nil ||
				*report.Rows[0].Identity.Line != line || report.Mapping.LineBasis != basis {
				t.Fatalf("source range %q (%s) = %#v, %v", line, basis, report, err)
			}
		}
		_, err := Import(strings.NewReader("entity,value,line\nsame,1,0-3\n"), mapping)
		if basis == "absolute" {
			errorCategory(t, err, "line_number")
		} else if err != nil {
			t.Fatal(err)
		}
	}
}

func TestIdentityMissingPolicyIsExplicit(t *testing.T) {
	mapping := syntheticMapping()
	mapping.Headers = append(mapping.Headers, "line")
	mapping.Identity.Line, mapping.LineBasis = "line", "absolute"
	mapping.MissingPolicy = "preserve_missing"
	report, err := Import(strings.NewReader("entity,value,line\nitem,1,NA\n"), mapping)
	if err != nil || len(report.Rows) != 1 || report.Rows[0].Identity.Line != nil {
		t.Fatalf("missing identity = %#v, %v", report, err)
	}
	mapping.MissingPolicy = "reject"
	_, err = Import(strings.NewReader("entity,value,line\nitem,1,NA\n"), mapping)
	errorCategory(t, err, "missing_value")
	mapping.MissingPolicy = "preserve_missing"
	_, err = Import(strings.NewReader("entity,value,line\nNA,1,2\n"), mapping)
	errorCategory(t, err, "identity_label")
}

func TestCSVQuotingUTF8BOMAndActualByteHashes(t *testing.T) {
	mapping := syntheticMapping()
	mapping.AllowUTF8BOM = true
	csv := "\xef\xbb\xbfentity,value\r\n\"a, \"\"quoted\"\"\r\n\u00e9\",1\r\nlast,2"
	report, err := Import(strings.NewReader(csv), mapping)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Rows) != 2 || report.Rows[0].Identity.Label != "a, \"quoted\"\r\n\u00e9" ||
		report.Rows[0].SourceLine != 2 || report.Rows[1].SourceLine != 4 {
		t.Fatalf("rows = %#v", report.Rows)
	}
	digest := sha256.Sum256([]byte(csv))
	if report.InputSHA256 != hex.EncodeToString(digest[:]) || report.InputBytes != int64(len(csv)) {
		t.Fatal("BOM, CRLF, or UTF-8 bytes were normalized before hashing")
	}
	mapping.AllowUTF8BOM = false
	_, err = Import(strings.NewReader(csv), mapping)
	errorCategory(t, err, "utf8_bom_not_allowed")
	_, err = Import(strings.NewReader("entity,value\nbad"+string([]byte{0xff})+",1\n"), mapping)
	errorCategory(t, err, "csv_invalid_utf8")
	_, err = Import(strings.NewReader("entity,value\nbad\x00,1\n"), mapping)
	errorCategory(t, err, "csv_invalid_utf8")
}

func TestCSVHeadersWidthsBlankRecordsAndErrorsDoNotLeakValues(t *testing.T) {
	cases := []struct {
		name, input, category string
		row                   int
	}{
		{"empty input", "", "header_missing", 0},
		{"unexpected header", "private-secret,value\nitem,1\n", "header_mismatch", 0},
		{"reordered header", "value,entity\n1,item\n", "header_mismatch", 0},
		{"duplicate header", "entity,entity\nitem,1\n", "header_mismatch", 0},
		{"extra header", "entity,value,private-secret\nitem,1,x\n", "header_mismatch", 0},
		{"short row", "entity,value\nprivate-secret\n", "row_width", 1},
		{"long row", "entity,value\nitem,1,private-secret\n", "row_width", 1},
		{"blank row", "entity,value\n\nitem,1\n", "row_width", 1},
		{"bad numeric", "entity,value\nitem,private-secret\n", "invalid_number", 1},
		{"quote in bare field", "entity,value\nprivate\"secret,1\n", "csv_syntax", 1},
		{"trailing quote text", "entity,value\n\"item\"private-secret,1\n", "csv_syntax", 1},
		{"unterminated quote", "entity,value\n\"private-secret,1\n", "csv_syntax", 1},
		{"bare CR", "entity,value\nitem,1\rprivate-secret", "csv_syntax", 1},
		{"no label", "entity,value\n,1\n", "identity_label", 1},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			report, err := Import(strings.NewReader(test.input), syntheticMapping())
			failure := errorCategory(t, err, test.category)
			if failure.Row != test.row || report.Rows != nil {
				t.Fatalf("error row=%d, report=%#v", failure.Row, report)
			}
			if strings.Contains(err.Error(), "private-secret") {
				t.Fatalf("error leaked data: %v", err)
			}
		})
	}
}

func TestSummaryRowsAreNotGuessedOrSkipped(t *testing.T) {
	mapping := syntheticMapping()
	report, err := Import(strings.NewReader("entity,value\nitem,1\nTotal,2\n  indented,3\n"), mapping)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Rows) != 3 || report.Rows[1].Identity.Label != "Total" ||
		report.Rows[2].Identity.Label != "  indented" {
		t.Fatal("importer guessed summary or tree semantics from display text")
	}
}

func TestEmptyExportWithExactHeaderIsValid(t *testing.T) {
	report, err := Import(strings.NewReader("entity,value"), syntheticMapping())
	if err != nil {
		t.Fatal(err)
	}
	if report.Rows == nil || len(report.Rows) != 0 || len(report.Rankings) != 1 ||
		report.Rankings[0].RowIndices == nil || report.Rankings[0].MissingRowIndices == nil {
		t.Fatalf("empty report = %#v", report)
	}
}

func TestResourceLimits(t *testing.T) {
	t.Run("cell", func(t *testing.T) {
		_, err := Import(strings.NewReader("entity,value\n"+strings.Repeat("x", MaxCellBytes+1)+",1\n"), syntheticMapping())
		errorCategory(t, err, "cell_size_limit")
		report, err := Import(strings.NewReader("entity,value\n"+strings.Repeat("x", MaxCellBytes)+",1\n"), syntheticMapping())
		if err != nil || len(report.Rows) != 1 {
			t.Fatalf("maximum-size cell rejected: %v", err)
		}
	})
	t.Run("columns", func(t *testing.T) {
		_, err := Import(strings.NewReader(strings.Repeat("x,", MaxColumns)+"x\n"), syntheticMapping())
		errorCategory(t, err, "column_limit")
	})
	t.Run("rows", func(t *testing.T) {
		_, err := Import(strings.NewReader("entity,value\n"+strings.Repeat("x,1\n", MaxRows+1)), syntheticMapping())
		errorCategory(t, err, "row_limit")
	})
	t.Run("cells", func(t *testing.T) {
		mapping := syntheticMapping()
		for index := len(mapping.Headers); index < MaxColumns; index++ {
			mapping.Headers = append(mapping.Headers, fmt.Sprintf("ignored_%d", index))
		}
		record := "x,1" + strings.Repeat(",z", MaxColumns-2) + "\n"
		_, err := Import(strings.NewReader(strings.Join(mapping.Headers, ",")+"\n"+strings.Repeat(record, MaxCells/MaxColumns+1)), mapping)
		errorCategory(t, err, "cell_budget")
	})
	t.Run("input bytes", func(t *testing.T) {
		header := "entity,value\n"
		record := strings.Repeat("x", MaxCellBytes) + ",1\n"
		reader := io.MultiReader(strings.NewReader(header), &repeatedReader{value: []byte(record), remaining: MaxInputBytes + 1})
		_, err := Import(reader, syntheticMapping())
		errorCategory(t, err, "input_size_limit")
	})
	t.Run("mapping columns", func(t *testing.T) {
		mapping := syntheticMapping()
		mapping.Headers = make([]string, MaxColumns+1)
		_, err := Import(strings.NewReader(""), mapping)
		errorCategory(t, err, "mapping_headers")
	})
	t.Run("mapping metrics", func(t *testing.T) {
		mapping := syntheticMapping()
		mapping.Metrics = make([]MetricMapping, MaxMetrics+1)
		_, err := Import(strings.NewReader(""), mapping)
		errorCategory(t, err, "mapping_metrics")
	})
	t.Run("direct mapping bytes", func(t *testing.T) {
		mapping := syntheticMapping()
		for index := 0; index < 20; index++ {
			mapping.MissingTokens = append(mapping.MissingTokens, fmt.Sprint(index)+strings.Repeat("x", MaxCellBytes-2))
		}
		_, err := Import(strings.NewReader(""), mapping)
		errorCategory(t, err, "mapping_size_limit")
	})
}

type repeatedReader struct {
	value     []byte
	offset    int
	remaining int
}

func (reader *repeatedReader) Read(buffer []byte) (int, error) {
	if reader.remaining == 0 {
		return 0, io.EOF
	}
	limit := min(len(buffer), reader.remaining)
	for index := 0; index < limit; index++ {
		buffer[index] = reader.value[reader.offset]
		reader.offset = (reader.offset + 1) % len(reader.value)
	}
	reader.remaining -= limit
	return limit, nil
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, errors.New("private-reader-error") }

func TestReadFailuresAreSanitized(t *testing.T) {
	_, err := DecodeMapping(errorReader{})
	errorCategory(t, err, "mapping_read_failed")
	if strings.Contains(err.Error(), "private-reader-error") {
		t.Fatal("mapping reader error leaked")
	}
	_, err = Import(errorReader{}, syntheticMapping())
	errorCategory(t, err, "input_read_failed")
	if strings.Contains(err.Error(), "private-reader-error") {
		t.Fatal("CSV reader error leaked")
	}
	_, err = Import(io.MultiReader(strings.NewReader("entity,value\nitem,1\n"), errorReader{}), syntheticMapping())
	errorCategory(t, err, "input_read_failed")
	_, err = DecodeMapping(nil)
	errorCategory(t, err, "mapping_read_failed")
	_, err = Import(nil, syntheticMapping())
	errorCategory(t, err, "input_read_failed")
}

func TestImportSnapshotsMapping(t *testing.T) {
	mapping := syntheticMapping()
	report, err := Import(strings.NewReader("entity,value\nitem,1\n"), mapping)
	if err != nil {
		t.Fatal(err)
	}
	mapping.Headers[0] = "changed"
	mapping.Metrics[0].Name = "changed"
	mapping.MissingTokens[0] = "changed"
	if report.Mapping.Headers[0] != "entity" || report.Mapping.Metrics[0].Name != "samples" ||
		report.Mapping.MissingTokens[0] != "NA" {
		t.Fatal("report retains mutable caller-owned mapping storage")
	}
}

func FuzzDecodeMapping(f *testing.F) {
	seed, _ := json.Marshal(syntheticMapping())
	f.Add(seed)
	f.Add([]byte(`{"schema":"private-secret","schema":"duplicate"}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		mapping, err := DecodeMapping(bytes.NewReader(data))
		if err == nil {
			if _, _, validationErr := canonicalMapping(mapping); validationErr != nil {
				t.Fatalf("DecodeMapping returned invalid contract: %v", validationErr)
			}
		} else if _, ok := err.(*Error); !ok {
			t.Fatalf("unsanitized error type: %T", err)
		}
	})
}

func FuzzImportBoundedSyntheticMapping(f *testing.F) {
	f.Add([]byte("entity,value\nsame,1\n"))
	f.Add([]byte("entity,value\n\"quoted, label\",18446744073709551616\n"))
	f.Add([]byte("entity,value\nsame,NA\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		mapping := syntheticMapping()
		mapping.MissingPolicy = "preserve_missing"
		report, err := Import(bytes.NewReader(data), mapping)
		if err != nil {
			if _, ok := err.(*Error); !ok {
				t.Fatalf("unsanitized error type: %T", err)
			}
			if report.Rows != nil {
				t.Fatal("failed import returned partial rows")
			}
			return
		}
		if len(report.Rows) > MaxRows || len(report.Rows)*len(mapping.Headers) > MaxCells ||
			report.InputBytes > MaxInputBytes || report.Provenance.VendorQualified {
			t.Fatal("import violated limits or qualification boundary")
		}
		digest := sha256.Sum256(data)
		if report.InputSHA256 != hex.EncodeToString(digest[:]) {
			t.Fatal("input hash does not bind all input bytes")
		}
		if _, err := json.Marshal(report); err != nil {
			t.Fatal(err)
		}
	})
}
