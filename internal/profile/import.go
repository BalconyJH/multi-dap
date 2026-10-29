package profile

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"sort"
	"strings"
)

const ReportSchema = "multi-dap.profile-report.v1"

type Provenance struct {
	Format          string `json:"format"`
	Source          string `json:"source"`
	VendorQualified bool   `json:"vendor_qualified"`
}

type Identity struct {
	Label     string  `json:"label"`
	Addrspace *string `json:"addrspace,omitempty"`
	Task      *string `json:"task,omitempty"`
	File      *string `json:"file,omitempty"`
	Function  *string `json:"function,omitempty"`
	Address   *string `json:"address,omitempty"`
	Line      *string `json:"line,omitempty"`
}

type Row struct {
	RowIndex   int                `json:"row_index"`
	SourceLine int                `json:"source_line"`
	Identity   Identity           `json:"identity"`
	Values     map[string]*string `json:"values"`
}

type Metric struct {
	Name        string `json:"name"`
	Semantic    string `json:"semantic"`
	Meaning     string `json:"meaning"`
	Unit        string `json:"unit"`
	Attribution string `json:"attribution"`
	Denominator string `json:"denominator,omitempty"`
}

type Ranking struct {
	Metric            string `json:"metric"`
	RowIndices        []int  `json:"row_indices"`
	MissingRowIndices []int  `json:"missing_row_indices"`
}

type Report struct {
	Schema          string     `json:"schema"`
	Provenance      Provenance `json:"provenance"`
	InputSHA256     string     `json:"input_sha256"`
	MappingSHA256   string     `json:"mapping_sha256"`
	MappingHashKind string     `json:"mapping_hash_kind"`
	InputBytes      int64      `json:"input_bytes"`
	RowIndexBasis   string     `json:"row_index_basis"`
	Mapping         Mapping    `json:"mapping"`
	Metrics         []Metric   `json:"metrics"`
	Rows            []Row      `json:"rows"`
	Rankings        []Ranking  `json:"rankings"`
}

// Import preserves each data record as a separate row. It neither combines
// equal names nor infers callers, timestamps, total time, or missing values.
// Its explicit mapping is an operator assertion, not vendor qualification.
func Import(reader io.Reader, supplied Mapping) (Report, error) {
	mapping, canonical, err := canonicalMapping(supplied)
	if err != nil {
		return Report{}, err
	}
	input, err := newCSVInput(reader, mapping.AllowUTF8BOM)
	if err != nil {
		return Report{}, err
	}
	header, _, err := input.readRecord()
	if err == io.EOF {
		return Report{}, failure("header_missing")
	}
	if err != nil {
		return Report{}, err
	}
	if len(header) != len(mapping.Headers) {
		return Report{}, failure("header_mismatch")
	}
	columns := make(map[string]int, len(header))
	for index, name := range header {
		if name != mapping.Headers[index] {
			return Report{}, failure("header_mismatch")
		}
		columns[name] = index
	}
	missing := make(map[string]struct{}, len(mapping.MissingTokens))
	for _, token := range mapping.MissingTokens {
		missing[token] = struct{}{}
	}
	report := Report{
		Schema: ReportSchema, Mapping: mapping, MappingHashKind: "canonical_json",
		RowIndexBasis: "data_record_1_based",
		Provenance:    Provenance{Format: "operator_mapped", Source: mapping.Provenance},
		Metrics:       make([]Metric, 0, len(mapping.Metrics)), Rows: []Row{}, Rankings: []Ranking{},
	}
	mappingHash := sha256.Sum256(canonical)
	report.MappingSHA256 = hex.EncodeToString(mappingHash[:])
	for _, metric := range mapping.Metrics {
		meaning := metric.Semantic
		if mapping.Mode == "history_performance" && metric.Semantic == "observed_calls" {
			meaning = "occurrences"
		}
		report.Metrics = append(report.Metrics, Metric{
			Name: metric.Name, Semantic: metric.Semantic, Meaning: meaning,
			Unit: metric.Unit, Attribution: metric.Attribution, Denominator: metric.Denominator,
		})
	}
	for {
		cells, sourceLine, err := input.readRecord()
		if err == io.EOF {
			break
		}
		rowIndex := len(report.Rows) + 1
		if err != nil {
			return Report{}, atRow(err, rowIndex)
		}
		if rowIndex > MaxRows {
			return Report{}, atRow(failure("row_limit"), rowIndex)
		}
		if rowIndex > MaxCells/len(header) {
			return Report{}, atRow(failure("cell_budget"), rowIndex)
		}
		if len(cells) != len(header) {
			return Report{}, atRow(failure("row_width"), rowIndex)
		}
		identity, err := rowIdentity(cells, mapping, columns, missing)
		if err != nil {
			return Report{}, atRow(err, rowIndex)
		}
		row := Row{
			RowIndex: rowIndex, SourceLine: sourceLine, Identity: identity,
			Values: make(map[string]*string, len(mapping.Metrics)),
		}
		for _, metric := range mapping.Metrics {
			raw := cells[columns[metric.Column]]
			value, err := metricValue(raw, metric, mapping.MissingPolicy, missing)
			if err != nil {
				return Report{}, atRow(err, rowIndex)
			}
			row.Values[metric.Name] = value
		}
		report.Rows = append(report.Rows, row)
	}
	report.InputBytes = input.input.bytes
	report.InputSHA256 = hex.EncodeToString(input.input.hash.Sum(nil))
	report.Rankings = rankRows(report.Rows, mapping.Metrics)
	return report, nil
}

func rowIdentity(cells []string, mapping Mapping, columns map[string]int, missing map[string]struct{}) (Identity, error) {
	result := Identity{Label: cells[columns[mapping.Identity.Label]]}
	if _, absent := missing[result.Label]; absent || result.Label == "" {
		return Identity{}, failure("identity_label")
	}
	roles := []struct {
		column string
		target **string
	}{
		{mapping.Identity.Addrspace, &result.Addrspace}, {mapping.Identity.Task, &result.Task},
		{mapping.Identity.File, &result.File}, {mapping.Identity.Function, &result.Function},
		{mapping.Identity.Address, &result.Address}, {mapping.Identity.Line, &result.Line},
	}
	for _, role := range roles {
		if role.column == "" {
			continue
		}
		value := cells[columns[role.column]]
		if _, absent := missing[value]; absent {
			if mapping.MissingPolicy == "reject" {
				return Identity{}, failure("missing_value")
			}
			continue
		}
		*role.target = &value
	}
	if result.Line != nil {
		if err := validateLine(*result.Line, mapping.LineBasis); err != nil {
			return Identity{}, failure("line_number")
		}
	}
	return result, nil
}

func validateLine(value, basis string) error {
	first, last, isRange := strings.Cut(value, "-")
	if err := validateNumber(first, true); err != nil {
		return err
	}
	if basis == "absolute" && compareDecimal(first, "0") == 0 {
		return failure("line_number")
	}
	if isRange {
		if err := validateNumber(last, true); err != nil {
			return err
		}
		if compareDecimal(first, last) > 0 {
			return failure("line_number")
		}
	}
	return nil
}

func metricValue(raw string, metric MetricMapping, policy string, missing map[string]struct{}) (*string, error) {
	if _, absent := missing[raw]; absent {
		if policy == "reject" {
			return nil, failure("missing_value")
		}
		return nil, nil
	}
	if metric.Semantic == "coverage_state" {
		state, found := metric.TokenMap[raw]
		if !found {
			return nil, failure("coverage_token")
		}
		return &state, nil
	}
	if err := validateNumber(raw, metric.Unit == "count"); err != nil {
		return nil, err
	}
	if isPercent(metric) && compareDecimal(raw, "100") > 0 {
		return nil, failure("percentage_range")
	}
	return &raw, nil
}

func validateNumber(value string, integer bool) error {
	if value == "" {
		return failure("invalid_number")
	}
	digits, dot := 0, false
	for index := 0; index < len(value); index++ {
		b := value[index]
		if b >= '0' && b <= '9' {
			digits++
			if digits > MaxNumericDigits {
				return failure("numeric_precision_limit")
			}
			continue
		}
		if b == '.' && !integer && !dot && index != 0 && index != len(value)-1 {
			dot = true
			continue
		}
		return failure("invalid_number")
	}
	return nil
}

// Decimal comparison stays exact without float64 or per-comparison big.Rat
// parsing. Inputs already passed the bounded, non-negative decimal grammar.
func compareDecimal(left, right string) int {
	leftWhole, leftFraction, _ := strings.Cut(left, ".")
	rightWhole, rightFraction, _ := strings.Cut(right, ".")
	leftWhole = strings.TrimLeft(leftWhole, "0")
	rightWhole = strings.TrimLeft(rightWhole, "0")
	if len(leftWhole) < len(rightWhole) {
		return -1
	}
	if len(leftWhole) > len(rightWhole) {
		return 1
	}
	if integer := strings.Compare(leftWhole, rightWhole); integer != 0 {
		return integer
	}
	for index := 0; index < max(len(leftFraction), len(rightFraction)); index++ {
		l, r := byte('0'), byte('0')
		if index < len(leftFraction) {
			l = leftFraction[index]
		}
		if index < len(rightFraction) {
			r = rightFraction[index]
		}
		if l < r {
			return -1
		}
		if l > r {
			return 1
		}
	}
	return 0
}

func rankRows(rows []Row, metrics []MetricMapping) []Ranking {
	rankings := make([]Ranking, 0, len(metrics))
	for _, metric := range metrics {
		if metric.Semantic == "coverage_state" {
			continue
		}
		ranking := Ranking{Metric: metric.Name, RowIndices: []int{}, MissingRowIndices: []int{}}
		for _, row := range rows {
			if row.Values[metric.Name] == nil {
				ranking.MissingRowIndices = append(ranking.MissingRowIndices, row.RowIndex)
			} else {
				ranking.RowIndices = append(ranking.RowIndices, row.RowIndex)
			}
		}
		sort.SliceStable(ranking.RowIndices, func(i, j int) bool {
			left := rows[ranking.RowIndices[i]-1].Values[metric.Name]
			right := rows[ranking.RowIndices[j]-1].Values[metric.Name]
			return compareDecimal(*left, *right) > 0
		})
		rankings = append(rankings, ranking)
	}
	return rankings
}
