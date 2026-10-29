package profile

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/Tacrolimus/multi-dap/internal/config"
)

const MappingSchema = "multi-dap.profile-mapping.v1"

type Mapping struct {
	Schema        string          `json:"schema"`
	MultiVersion  string          `json:"multi_version"`
	ExportSurface string          `json:"export_surface"`
	View          string          `json:"view"`
	Mode          string          `json:"mode"`
	Provenance    string          `json:"provenance"`
	FilterScope   string          `json:"filter_scope"`
	NameStyle     string          `json:"name_style"`
	ListLayout    string          `json:"list_layout"`
	Headers       []string        `json:"headers"`
	AllowUTF8BOM  bool            `json:"allow_utf8_bom"`
	Identity      IdentityMapping `json:"identity"`
	LineBasis     string          `json:"line_basis"`
	Metrics       []MetricMapping `json:"metrics"`
	MissingTokens []string        `json:"missing_tokens"`
	MissingPolicy string          `json:"missing_policy"`
}

// IdentityMapping values are exact header names, not expressions or fallback
// aliases. Multiple identity roles may intentionally share the same column.
type IdentityMapping struct {
	Label     string `json:"label"`
	Addrspace string `json:"addrspace,omitempty"`
	Task      string `json:"task,omitempty"`
	File      string `json:"file,omitempty"`
	Function  string `json:"function,omitempty"`
	Address   string `json:"address,omitempty"`
	Line      string `json:"line,omitempty"`
}

type MetricMapping struct {
	Name        string            `json:"name"`
	Column      string            `json:"column"`
	Semantic    string            `json:"semantic"`
	Unit        string            `json:"unit"`
	Attribution string            `json:"attribution"`
	Denominator string            `json:"denominator,omitempty"`
	TokenMap    map[string]string `json:"token_map,omitempty"`
}

// DecodeMapping accepts exactly one UTF-8 JSON object. Duplicate keys, unknown
// fields (including nested fields), and trailing JSON are rejected.
func DecodeMapping(reader io.Reader) (Mapping, error) {
	if reader == nil {
		return Mapping{}, failure("mapping_read_failed")
	}
	data, err := io.ReadAll(io.LimitReader(reader, MaxMappingBytes+1))
	if err != nil {
		return Mapping{}, failure("mapping_read_failed")
	}
	if len(data) > MaxMappingBytes {
		return Mapping{}, failure("mapping_size_limit")
	}
	if !utf8.Valid(data) {
		return Mapping{}, failure("mapping_invalid_utf8")
	}
	tokens := json.NewDecoder(bytes.NewReader(data))
	tokens.UseNumber()
	if err := checkJSONValue(tokens, 0); err != nil {
		return Mapping{}, err
	}
	if _, err := tokens.Token(); err != io.EOF {
		return Mapping{}, failure("mapping_trailing_json")
	}
	if err := exactMappingFields(data); err != nil {
		return Mapping{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var mapping Mapping
	if err := decoder.Decode(&mapping); err != nil {
		return Mapping{}, failure("mapping_unknown_or_invalid_field")
	}
	mapping, _, err = canonicalMapping(mapping)
	return mapping, err
}

func checkJSONValue(decoder *json.Decoder, depth int) error {
	if depth > maxMappingDepth {
		return failure("mapping_depth_limit")
	}
	token, err := decoder.Token()
	if err != nil {
		return failure("mapping_invalid_json")
	}
	if token == nil {
		return failure("mapping_unknown_or_invalid_field")
	}
	delimiter, structured := token.(json.Delim)
	if !structured {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return failure("mapping_invalid_json")
			}
			key, ok := keyToken.(string)
			if !ok {
				return failure("mapping_invalid_json")
			}
			if _, exists := seen[key]; exists {
				return failure("mapping_duplicate_key")
			}
			seen[key] = struct{}{}
			if err := checkJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := checkJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
	default:
		return failure("mapping_invalid_json")
	}
	end, err := decoder.Token()
	if err != nil || (delimiter == '{' && end != json.Delim('}')) || (delimiter == '[' && end != json.Delim(']')) {
		return failure("mapping_invalid_json")
	}
	return nil
}

// encoding/json also matches struct fields case-insensitively. An explicit
// field inventory rejects aliases such as "Schema" rather than letting them
// override a schema field while escaping duplicate-key detection.
func exactMappingFields(data []byte) error {
	fields, err := exactObjectFields(data,
		"schema", "multi_version", "export_surface", "view", "mode", "provenance",
		"filter_scope", "name_style", "list_layout", "headers", "allow_utf8_bom",
		"identity", "line_basis", "metrics", "missing_tokens", "missing_policy")
	if err != nil {
		return err
	}
	if identity, exists := fields["identity"]; exists {
		if _, err := exactObjectFields(identity, "label", "addrspace", "task", "file", "function", "address", "line"); err != nil {
			return err
		}
	}
	if metrics, exists := fields["metrics"]; exists {
		var entries []json.RawMessage
		if err := json.Unmarshal(metrics, &entries); err != nil {
			return failure("mapping_unknown_or_invalid_field")
		}
		for _, entry := range entries {
			if _, err := exactObjectFields(entry, "name", "column", "semantic", "unit", "attribution", "denominator", "token_map"); err != nil {
				return err
			}
		}
	}
	return nil
}

func exactObjectFields(data []byte, allowed ...string) (map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || fields == nil {
		return nil, failure("mapping_unknown_or_invalid_field")
	}
	for name := range fields {
		if !oneOf(name, allowed...) {
			return nil, failure("mapping_unknown_or_invalid_field")
		}
	}
	return fields, nil
}

// canonicalMapping validates even programmatically constructed mappings, then
// snapshots their slices/maps. The hash binds normalized JSON, not source bytes.
func canonicalMapping(mapping Mapping) (Mapping, []byte, error) {
	if err := validateMapping(mapping); err != nil {
		return Mapping{}, nil, err
	}
	normalized, err := config.NormalizeMultiVersion(mapping.MultiVersion)
	if err != nil {
		return Mapping{}, nil, failure("mapping_multi_version")
	}
	mapping.MultiVersion = normalized
	if mapping.MissingTokens == nil {
		mapping.MissingTokens = []string{}
	}
	data, err := json.Marshal(mapping)
	if err != nil || len(data) > MaxMappingBytes {
		return Mapping{}, nil, failure("mapping_size_limit")
	}
	var snapshot Mapping
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return Mapping{}, nil, failure("mapping_invalid_json")
	}
	return snapshot, data, nil
}

func validateMapping(mapping Mapping) error {
	if mapping.Schema != MappingSchema {
		return failure("mapping_schema")
	}
	if len(mapping.MultiVersion) > 64 {
		return failure("mapping_multi_version")
	}
	if _, err := config.NormalizeMultiVersion(mapping.MultiVersion); err != nil {
		return failure("mapping_multi_version")
	}
	if mapping.ExportSurface != "profile_csvexport" {
		return failure("mapping_export_surface")
	}
	if !oneOf(mapping.View, "tasks", "address_spaces", "files", "functions", "functions_source", "coverage", "coverage_source", "blocks", "assembly") {
		return failure("mapping_view")
	}
	if !oneOf(mapping.Mode, "pc_sampling", "instrumentation_performance", "instrumentation_coverage", "trace_performance", "trace_coverage", "history_performance") {
		return failure("mapping_mode")
	}
	if !oneOf(mapping.Provenance, "synthetic_fixture", "operator_supplied_export") {
		return failure("mapping_provenance")
	}
	if !oneOf(mapping.FilterScope, "full_recording", "filtered", "unknown") {
		return failure("mapping_filter_scope")
	}
	if !oneOf(mapping.NameStyle, "long", "short", "unknown") {
		return failure("mapping_name_style")
	}
	if mapping.ListLayout != "flat" {
		return failure("mapping_list_layout")
	}
	if !oneOf(mapping.MissingPolicy, "reject", "preserve_missing") {
		return failure("mapping_missing_policy")
	}
	if len(mapping.Headers) == 0 || len(mapping.Headers) > MaxColumns {
		return failure("mapping_headers")
	}
	headers := make(map[string]struct{}, len(mapping.Headers))
	for _, header := range mapping.Headers {
		if !validText(header, false) {
			return failure("mapping_headers")
		}
		if _, exists := headers[header]; exists {
			return failure("mapping_duplicate_header")
		}
		headers[header] = struct{}{}
	}
	if mapping.Identity.Label == "" {
		return failure("mapping_identity")
	}
	for _, role := range []string{
		mapping.Identity.Label, mapping.Identity.Addrspace, mapping.Identity.Task,
		mapping.Identity.File, mapping.Identity.Function, mapping.Identity.Address, mapping.Identity.Line,
	} {
		if role != "" {
			if _, exists := headers[role]; !exists {
				return failure("mapping_identity")
			}
		}
	}
	if mapping.Identity.Line == "" {
		if mapping.LineBasis != "unavailable" {
			return failure("mapping_line_basis")
		}
	} else if !oneOf(mapping.LineBasis, "function_relative", "absolute") {
		return failure("mapping_line_basis")
	}
	if len(mapping.MissingTokens) > maxTokens {
		return failure("mapping_missing_tokens")
	}
	missing := make(map[string]struct{}, len(mapping.MissingTokens))
	for _, token := range mapping.MissingTokens {
		if !validText(token, true) {
			return failure("mapping_missing_tokens")
		}
		if _, exists := missing[token]; exists {
			return failure("mapping_missing_tokens")
		}
		missing[token] = struct{}{}
	}
	if len(mapping.Metrics) == 0 || len(mapping.Metrics) > MaxMetrics {
		return failure("mapping_metrics")
	}
	names := make(map[string]struct{}, len(mapping.Metrics))
	columns := make(map[string]struct{}, len(mapping.Metrics))
	for _, metric := range mapping.Metrics {
		if !metricName(metric.Name) {
			return failure("mapping_metric_name")
		}
		if _, exists := names[metric.Name]; exists {
			return failure("mapping_metric_name")
		}
		names[metric.Name] = struct{}{}
		if _, exists := headers[metric.Column]; !exists {
			return failure("mapping_metric_column")
		}
		if _, exists := columns[metric.Column]; exists {
			return failure("mapping_metric_column")
		}
		columns[metric.Column] = struct{}{}
		if !oneOf(metric.Attribution, "self", "inclusive", "vendor_unspecified") {
			return failure("mapping_attribution")
		}
		if err := validateMetric(metric, missing); err != nil {
			return err
		}
	}
	if mappingTextBytes(mapping) > MaxMappingBytes {
		return failure("mapping_size_limit")
	}
	return nil
}

func mappingTextBytes(mapping Mapping) int {
	size := len(mapping.Schema) + len(mapping.MultiVersion) + len(mapping.ExportSurface) +
		len(mapping.View) + len(mapping.Mode) + len(mapping.Provenance) + len(mapping.FilterScope) +
		len(mapping.NameStyle) + len(mapping.ListLayout) + len(mapping.LineBasis) + len(mapping.MissingPolicy)
	for _, role := range []string{
		mapping.Identity.Label, mapping.Identity.Addrspace, mapping.Identity.Task,
		mapping.Identity.File, mapping.Identity.Function, mapping.Identity.Address, mapping.Identity.Line,
	} {
		size += len(role)
	}
	for _, header := range mapping.Headers {
		size += len(header)
	}
	for _, token := range mapping.MissingTokens {
		size += len(token)
	}
	for _, metric := range mapping.Metrics {
		size += len(metric.Name) + len(metric.Column) + len(metric.Semantic) +
			len(metric.Unit) + len(metric.Attribution) + len(metric.Denominator)
		for token, state := range metric.TokenMap {
			size += len(token) + len(state)
		}
	}
	return size
}

func validateMetric(metric MetricMapping, missing map[string]struct{}) error {
	switch metric.Semantic {
	case "time":
		if !oneOf(metric.Unit, "s", "ms", "us", "ns") {
			return failure("mapping_metric_unit")
		}
	case "time_percent", "instruction_percent", "coverage_percent":
		if metric.Unit != "percent" {
			return failure("mapping_metric_unit")
		}
		switch metric.Semantic {
		case "time_percent":
			if !oneOf(metric.Denominator, "recording", "filter") {
				return failure("mapping_percent_denominator")
			}
		case "coverage_percent":
			if metric.Denominator != "entity_instructions" {
				return failure("mapping_percent_denominator")
			}
		case "instruction_percent":
			if !oneOf(metric.Denominator, "recording", "filter", "entity_instructions") {
				return failure("mapping_percent_denominator")
			}
		}
	case "instruction_count", "observed_calls", "sample_count", "uncovered_instructions":
		if metric.Unit != "count" {
			return failure("mapping_metric_unit")
		}
	case "coverage_state":
		if metric.Unit != "state" {
			return failure("mapping_metric_unit")
		}
		if len(metric.TokenMap) == 0 || len(metric.TokenMap) > maxTokens {
			return failure("mapping_token_map")
		}
		for token, state := range metric.TokenMap {
			if !validText(token, false) || !oneOf(state, "covered", "partial", "uncovered") {
				return failure("mapping_token_map")
			}
			if _, ambiguous := missing[token]; ambiguous {
				return failure("mapping_token_map")
			}
		}
	default:
		return failure("mapping_metric_semantic")
	}
	if metric.Unit != "percent" && metric.Denominator != "" {
		return failure("mapping_percent_denominator")
	}
	if metric.Semantic != "coverage_state" && len(metric.TokenMap) != 0 {
		return failure("mapping_token_map")
	}
	return nil
}

func validText(value string, emptyAllowed bool) bool {
	if (!emptyAllowed && value == "") || len(value) > MaxCellBytes || !utf8.ValidString(value) {
		return false
	}
	for _, b := range []byte(value) {
		if b == 0 {
			return false
		}
	}
	return true
}

func metricName(value string) bool {
	if len(value) == 0 || len(value) > 64 {
		return false
	}
	for i, b := range []byte(value) {
		letter := b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
		if !letter && (i == 0 || !(b >= '0' && b <= '9' || b == '_' || b == '.' || b == '-')) {
			return false
		}
	}
	return true
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func isPercent(metric MetricMapping) bool { return strings.HasSuffix(metric.Semantic, "_percent") }
