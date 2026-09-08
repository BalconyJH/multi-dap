// Package contract protects architectural boundaries that span Python and Go.
package contract

import (
	"bytes"
	"encoding/json"
	"go/scanner"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/Tacrolimus/multi-dap/internal/bridge"
)

const expectedProtocolVersion = 1

var expectedMethods = map[string]struct{}{
	"open": {}, "close": {}, "state": {}, "cores": {},
	"resume": {}, "halt": {}, "step_in": {}, "next": {}, "run_commands": {},
	"console_read": {}, "console_reset": {}, "memory_read": {}, "disassemble": {},
}

func TestCLionProfileUsesCidrNativeDAPCMakeDebugContract(t *testing.T) {
	root := repositoryRoot(t)
	data := []byte(readFile(t, filepath.Join(root, "editors", "clion", "profile-contract.json")))
	var profile struct {
		SchemaVersion      int    `json:"schema_version"`
		Client             string `json:"client"`
		ValidatedVersion   string `json:"validated_version"`
		ValidatedBuild     string `json:"validated_build"`
		Integration        string `json:"integration"`
		DaemonPrerequisite struct {
			Command          []string `json:"command"`
			Program          string   `json:"program"`
			WorkingDirectory string   `json:"working_directory"`
			Lifetime         string   `json:"lifetime"`
			SatisfiedBy      []string `json:"satisfied_by"`
		} `json:"daemon_prerequisite"`
		CMakeConfiguration struct {
			Kind            string `json:"kind"`
			NamePlaceholder string `json:"name_placeholder"`
			DebugProfile    string `json:"debug_profile"`
			Activation      string `json:"activation"`
		} `json:"cmake_configuration"`
		CidrDebugProfile struct {
			Kind                 string            `json:"kind"`
			Name                 string            `json:"name"`
			AdapterExecutable    string            `json:"adapter_executable"`
			AdapterArguments     []string          `json:"adapter_arguments"`
			ConfigurationBinding string            `json:"configuration_binding"`
			Communication        string            `json:"communication"`
			LaunchTabJSON        map[string]string `json:"launch_tab_json"`
			AttachTab            string            `json:"attach_tab"`
		} `json:"cidr_debug_profile"`
		Forbidden []string `json:"forbidden"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&profile); err != nil {
		t.Fatalf("decode CLion profile contract: %v", err)
	}
	if profile.SchemaVersion != 8 || profile.Client != "CLion" || profile.ValidatedVersion != "2026.2.1" || profile.ValidatedBuild != "262.9437.136" {
		t.Fatalf("CLion validation identity = %#v", profile)
	}
	wantEnsure := []string{"ensure", "--config", "<absolute-project.toml>", "--bridge-script", "<absolute-path-to-bridge.py>", "--acquisition", "cold"}
	if profile.Integration != "cidr-native-dap-cmake-debug" ||
		strings.Join(profile.DaemonPrerequisite.Command, "\x00") != strings.Join(wantEnsure, "\x00") ||
		profile.DaemonPrerequisite.Program != "<absolute-path-to-multi-dap.exe>" ||
		profile.DaemonPrerequisite.WorkingDirectory != "<project-root>" ||
		profile.DaemonPrerequisite.Lifetime != "synchronous-short-lived-explicit-cold-idempotent" ||
		strings.Join(profile.DaemonPrerequisite.SatisfiedBy, "\x00") != "CMake configuration Before Launch external tool\x00manual ensure before Debug" {
		t.Fatalf("CLion daemon prerequisite = %#v", profile.DaemonPrerequisite)
	}
	if profile.CMakeConfiguration.Kind != "CMake Application" ||
		profile.CMakeConfiguration.NamePlaceholder != "<your-cmake-application>" ||
		profile.CMakeConfiguration.DebugProfile != "multi-dap" ||
		profile.CMakeConfiguration.Activation != "Debug button only; never green Run" {
		t.Fatalf("CLion CMake configuration = %#v", profile.CMakeConfiguration)
	}
	wantProxy := []string{"proxy", "--config", "<absolute-project.toml>"}
	if profile.CidrDebugProfile.Kind != "DAP" || profile.CidrDebugProfile.Name != "multi-dap" ||
		profile.CidrDebugProfile.AdapterExecutable != "<absolute-path-to-multi-dap.exe>" ||
		strings.Join(profile.CidrDebugProfile.AdapterArguments, "\x00") != strings.Join(wantProxy, "\x00") ||
		profile.CidrDebugProfile.ConfigurationBinding != "validated-project-semantics-v1" ||
		profile.CidrDebugProfile.Communication != "stdin/stdout" ||
		len(profile.CidrDebugProfile.LaunchTabJSON) != 1 || profile.CidrDebugProfile.LaunchTabJSON["request"] != "attach" ||
		profile.CidrDebugProfile.AttachTab != "not-used" {
		t.Fatalf("CLion Cidr debug profile = %#v", profile.CidrDebugProfile)
	}
	wantForbidden := []string{"Python Attach to DAP configuration", "green Run", "DAP profile Attach tab", "TCP remote address", "dynamic DAP port", "unbound proxy --probe-id", "target ELF as a Windows program", "target connection arguments"}
	if strings.Join(profile.Forbidden, "\x00") != strings.Join(wantForbidden, "\x00") {
		t.Fatalf("CLion forbidden paths = %q, want %q", profile.Forbidden, wantForbidden)
	}
	ensureSource := readFile(t, filepath.Join(root, "cmd", "multi-dap", "ensure.go"))
	for _, required := range []string{
		`flag.NewFlagSet("ensure"`, `flags.String("acquisition"`, `flags.String("primary-elf"`,
		"case ensureAcquisitionCold:", "discoverWarmSession(startup", "Acquisition: mode", "Warm: warm",
	} {
		if !strings.Contains(ensureSource, required) {
			t.Errorf("CLion ensure CLI contract is missing %q", required)
		}
	}
}

func TestCLionConfigurationGuideKeepsSupportedEntryPoint(t *testing.T) {
	root := repositoryRoot(t)
	guide := readFile(t, filepath.Join(root, "docs", "clion.md"))
	for _, required := range []string{
		"CLion is the primary supported IDE",
		"Validated with CLion 2026.2.1 (build 262.9437.136)",
		`proxy --config "<absolute-project.toml>"`,
		"[probe].id",
		"legacy, identity-only compatibility path",
		"daemon configuration does not match",
		"stdin/stdout",
		`{"request":"attach"}`,
		"--acquisition cold",
		"--acquisition warm",
		"--control-dir",
		"Before launch",
		"green **Run** button",
	} {
		if !strings.Contains(guide, required) {
			t.Errorf("CLion configuration guide is missing %q", required)
		}
	}

	readme := readFile(t, filepath.Join(root, "README.md"))
	if !strings.Contains(readme, "**CLion is the primary supported IDE integration.**") ||
		!strings.Contains(readme, "[Configure CLion step by step](docs/clion.md)") {
		t.Fatal("README does not expose the primary CLion support path")
	}
}

func TestProjectConfigurationReferenceKeepsStrictBindingContract(t *testing.T) {
	root := repositoryRoot(t)
	reference := readFile(t, filepath.Join(root, "docs", "configuration.md"))
	for _, required := range []string{
		"Project Configuration Reference",
		"rejects unknown TOML fields",
		"multi.installation",
		"multi.executable",
		"connection.project",
		"connection.arguments",
		"connection.preparation",
		"cores[].id",
		"cores[].elf",
		"inspection.default_core",
		"source_rewrites",
		"endpoints.mbp",
		"endpoints.dap",
		"endpoints.hint",
		"timing.poll_cadence",
		"timing.rpc_deadline",
		"timing.startup_deadline",
		"lifecycle.require_reset_after_download",
		"probe.id",
		"Semantic configuration binding",
		"proxy --config",
		"legacy identity-only `proxy --probe-id`",
		"daemon configuration does not match",
	} {
		if !strings.Contains(reference, required) {
			t.Errorf("project configuration reference is missing %q", required)
		}
	}
}

func TestBridgeM1SchemaMatchesGo(t *testing.T) {
	root := repositoryRoot(t)
	source := readFile(t, filepath.Join(root, "bridge", "bridge.py"))

	version := pythonProtocolVersion(t, source)
	if version != expectedProtocolVersion {
		t.Fatalf("bridge protocol version = %d, want contract version %d", version, expectedProtocolVersion)
	}
	if bridge.ProtocolVersion != expectedProtocolVersion {
		t.Fatalf("Go protocol version = %d, want contract version %d", bridge.ProtocolVersion, expectedProtocolVersion)
	}
	if !strings.Contains(source, `"protocol_version": PROTOCOL_VERSION`) {
		t.Fatal("bridge handshake no longer publishes PROTOCOL_VERSION")
	}

	methods := pythonMethods(t, source)
	if len(methods) != len(expectedMethods) {
		t.Fatalf("bridge METHODS = %v, want exactly %v", sortedKeys(methods), sortedKeys(expectedMethods))
	}
	for method := range expectedMethods {
		if _, ok := methods[method]; !ok {
			t.Errorf("bridge METHODS is missing %q", method)
		}
	}
	for _, forbidden := range []string{"download", "reset"} {
		if _, ok := methods[forbidden]; ok {
			t.Errorf("bridge METHODS must not expose unverified method %q", forbidden)
		}
	}
}

func TestBridgeM4ExecutionMethodsRemainExactAndTyped(t *testing.T) {
	source := readFile(t, filepath.Join(repositoryRoot(t), "bridge", "bridge.py"))
	execution := pythonMethodBody(t, source, "_execution")
	for _, required := range []string{
		"getattr(window, method)(0, 0)",
		"if accepted is not True:",
		`return {"accepted": True}`,
	} {
		if !strings.Contains(execution, required) {
			t.Errorf("bridge execution helper is missing %q", required)
		}
	}
	stepIn := pythonMethodBody(t, source, "step_in")
	for _, required := range []string{
		"self._params(params, ())",
		"window.Step(0, 0, 1)",
		"if accepted is not True:",
		`return {"accepted": True}`,
	} {
		if !strings.Contains(stepIn, required) {
			t.Errorf("bridge step_in is missing %q", required)
		}
	}
	next := pythonMethodBody(t, source, "next")
	for _, required := range []string{
		"self._params(params, ())",
		`return self._execution("Next")`,
	} {
		if !strings.Contains(next, required) {
			t.Errorf("bridge next is missing %q", required)
		}
	}
	for _, forbidden := range []string{"StepOut", "Finish", "RunTo", "RunCommands"} {
		if strings.Contains(stepIn, forbidden) || strings.Contains(next, forbidden) {
			t.Errorf("verified M4 bridge methods must not use %q", forbidden)
		}
	}
}

func TestDaemonOptionalM4AndWriteMemoryRemainFailClosed(t *testing.T) {
	source := readFile(t, filepath.Join(repositoryRoot(t), "internal", "daemon", "dap_backend.go"))
	if !strings.Contains(source, "case dap.ActionStepOut:\n\t\treturn dap.ActionResult{}, errors.New(\"daemon: unsupported DAP action stepOut\")") {
		t.Fatal("daemon must reject stepOut before reaching the actor")
	}
	if !strings.Contains(source, "case dap.ActionWriteMemory:\n\t\treturn dap.ActionResult{}, errors.New(\"daemon: unsupported DAP action writeMemory\")") {
		t.Error("daemon must keep writeMemory fail-closed")
	}
	if !strings.Contains(source, "default:\n\t\treturn dap.ActionResult{}, fmt.Errorf(\"daemon: unsupported DAP action %d\", action.Kind)") {
		t.Fatal("daemon must reject unknown actions")
	}
}

func TestBridgeOpenModeSchemaRemainsTypedAndFailClosed(t *testing.T) {
	source := readFile(t, filepath.Join(repositoryRoot(t), "bridge", "bridge.py"))
	for _, required := range []string{
		`SESSION_MODES = frozenset((u"cold", u"warm"))`,
		`"mode", "project", "connection"`,
		`"mode", "primary_elf"`,
		`parser.add_argument("--session-mode", default="cold", choices=("cold", "warm"))`,
		"def _bind_existing_program_window(self, primary_elf):",
		"window.GetProgram()",
		"window.GetStatus()",
		"window.GetCurPrInfo(u\"\")",
	} {
		if !strings.Contains(source, required) {
			t.Errorf("bridge warm open schema is missing %q", required)
		}
	}
	warmStart := strings.Index(source, "def _bind_existing_program_window")
	if warmStart < 0 {
		t.Fatal("cannot isolate warm binder implementation")
	}
	warmEnd := strings.Index(source[warmStart:], "    def open(self, params):")
	if warmEnd < 0 {
		t.Fatal("cannot isolate warm binder implementation")
	}
	warmSource := source[warmStart : warmStart+warmEnd]
	for _, forbidden := range []string{
		"basename", "first-window", "DebugProgram(", "ConnectToTarget(", "Disconnect(",
	} {
		if strings.Contains(warmSource, forbidden) {
			t.Errorf("warm binder must not contain %q", forbidden)
		}
	}
}

func TestBridgeColdPreparationIsExplicitAndCannotUseTheRawCommandRoute(t *testing.T) {
	source := readFile(t, filepath.Join(repositoryRoot(t), "bridge", "bridge.py"))
	open := pythonMethodBody(t, source, "open")
	for _, required := range []string{
		`COLD_PREPARATIONS = frozenset((u"", u"already_present_no_verify"))`,
		`PREPARATION_ALREADY_PRESENT_NO_VERIFY = u"already_present_no_verify"`,
		`("setup_script", "setup_script_args", "multi_log", "preparation")`,
		`if preparation not in COLD_PREPARATIONS:`,
		`window.RunCommands("prepare_target -verify=none", True, False)`,
		`if window.RunCommands("prepare_target -verify=none", True, False) is not True:`,
		`debugger.Disconnect(0)`,
		`raise BridgeError("multi_refused", "MULTI could not prepare target")`,
		`"cold_cleanup_failed"`,
		`"MULTI could not prepare target or disconnect"`,
	} {
		if !strings.Contains(source, required) {
			t.Errorf("bridge cold preparation is missing %q", required)
		}
	}
	if strings.Contains(open, "cmdExecOutput") || strings.Contains(open, "cmdExecStatus") {
		t.Error("bridge cold preparation must not read or propagate command output")
	}
	rawCommands := pythonMethodBody(t, source, "run_commands")
	if !strings.Contains(rawCommands, `normalized.startswith(u"prepare_target")`) {
		t.Error("raw command route must reject prepare_target variants")
	}
}

func TestBridgeHasNoDAPOrCorePolicy(t *testing.T) {
	source := readFile(t, filepath.Join(repositoryRoot(t), "bridge", "bridge.py"))
	tokens := pythonCodeIdentifiers(source)

	forbiddenDAP := map[string]struct{}{
		"variablesreference": {}, "frameid": {}, "stackframe": {}, "stacktrace": {},
		"scopes": {}, "variables": {}, "evaluate": {}, "setbreakpoints": {},
		"setfunctionbreakpoints": {}, "setinstructionbreakpoints": {},
		"breakpointlocations": {}, "readmemory": {}, "writememory": {},
		"exceptioninfo": {}, "configurationdone": {},
	}
	for token := range tokens {
		if _, forbidden := forbiddenDAP[token]; forbidden {
			t.Errorf("bridge.py contains DAP-policy identifier %q", token)
		}
		if strings.Contains(token, "retry") || strings.Contains(token, "statemachine") || strings.Contains(token, "sourcerewrite") ||
			strings.Contains(token, "handlealloc") || strings.Contains(token, "allochandle") || strings.Contains(token, "handleallocator") {
			t.Errorf("bridge.py contains core-policy identifier %q", token)
		}
	}
}

func TestRunCommandsLiteralIsOwnedByMultiDriver(t *testing.T) {
	root := repositoryRoot(t)
	found := 0
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", ".hwscratch":
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" || strings.HasPrefix(filepath.Clean(path), filepath.Join(root, "internal", "contract")+string(filepath.Separator)) {
			return nil
		}
		for _, literal := range goStringLiterals(t, path) {
			if literal != "run_commands" {
				continue
			}
			found++
			relative, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			owner := filepath.Join("internal", "multi") + string(filepath.Separator)
			if !strings.HasPrefix(filepath.Clean(relative), owner) {
				t.Errorf("run_commands literal is outside internal/multi: %s", filepath.ToSlash(relative))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan Go sources: %v", err)
	}
	if found == 0 {
		t.Fatal("no Go-side run_commands literal found in internal/multi")
	}
}

func TestBridgeAndNotifierAreLoopbackOnly(t *testing.T) {
	root := repositoryRoot(t)
	bridgeSource := readFile(t, filepath.Join(root, "bridge", "bridge.py"))
	if !strings.Contains(bridgeSource, `LOOPBACK_HOST = "127.0.0.1"`) {
		t.Fatal("bridge default RPC host must be the IPv4 loopback literal")
	}
	if !strings.Contains(bridgeSource, "def validate_rpc_host(host):") {
		t.Fatal("bridge must validate every configured RPC host before binding")
	}
	if !strings.Contains(bridgeSource, "listener.bind((host, port))") {
		t.Fatal("bridge listener must bind the validated RPC host")
	}
	for _, required := range []string{
		`parser.add_argument("--rpc-host", default=LOOPBACK_HOST`,
		`host.split(u".")`,
		`text == u"::1"`,
	} {
		if !strings.Contains(bridgeSource, required) {
			t.Errorf("bridge RPC host validation is missing %q", required)
		}
	}
	if strings.Contains(bridgeSource, "inet_pton") {
		t.Error("bridge RPC host validation must not depend on inet_pton: MULTI Windows Python 2.7 lacks it")
	}
	assertNoWildcardBind(t, "bridge/bridge.py", bridgeSource)

	notifierSource := readFile(t, filepath.Join(root, "recon", "notifier.py"))
	if !strings.Contains(notifierSource, `_HOST = "127.0.0.1"`) {
		t.Fatal("notifier default destination must be a loopback literal")
	}
	if !strings.Contains(notifierSource, `if host not in ("127.0.0.1", "::1")`) {
		t.Fatal("notifier configure must reject non-loopback destinations")
	}
	assertNoWildcardBind(t, "recon/notifier.py", notifierSource)
}

func TestBridgeReadyFileBootstrapContract(t *testing.T) {
	source := readFile(t, filepath.Join(repositoryRoot(t), "bridge", "bridge.py"))
	for _, required := range []string{
		"def _write_ready_file(self, path, host, port):",
		`parser.add_argument("--ready-file")`,
		"listener.getsockname()[:2]",
		"self._write_ready_file(ready_file, bound_host, bound_port)",
		"connection, address = listener.accept()",
	} {
		if !strings.Contains(source, required) {
			t.Errorf("bridge ready-file bootstrap is missing %q", required)
		}
	}
	if strings.Index(source, "self._write_ready_file(ready_file, bound_host, bound_port)") > strings.Index(source, "connection, address = listener.accept()") {
		t.Fatal("bridge must publish its bound endpoint before accepting a client")
	}
	if !strings.Contains(source, "os.rename(temporary, path)") {
		t.Fatal("bridge ready file must be atomically renamed into place")
	}
}

func assertNoWildcardBind(t *testing.T, name, source string) {
	t.Helper()
	for _, wildcard := range []string{"0.0.0.0", "[::]", `"::"`, `''`, `\"\"`} {
		if strings.Contains(source, wildcard) {
			t.Errorf("%s contains wildcard listener/destination literal %s", name, wildcard)
		}
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtimeCaller()
	if ok {
		if root, ok := walkForRoot(filepath.Dir(thisFile)); ok {
			return root
		}
	}
	current, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	if root, ok := walkForRoot(current); ok {
		return root
	}
	t.Fatal("unable to locate repository root from test source or working directory")
	return ""
}

// runtimeCaller is a variable so tests could replace it without relying on cwd.
var runtimeCaller = func() (pc uintptr, file string, line int, ok bool) {
	return runtime.Caller(0)
}

func walkForRoot(start string) (string, bool) {
	for directory := filepath.Clean(start); ; {
		if fileExists(filepath.Join(directory, "go.mod")) && fileExists(filepath.Join(directory, "bridge", "bridge.py")) {
			return directory, true
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return "", false
		}
		directory = parent
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(contents)
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

var protocolAssignment = regexp.MustCompile(`(?m)^PROTOCOL_VERSION\s*=\s*([0-9]+)\s*$`)
var methodsAssignment = regexp.MustCompile(`(?ms)^METHODS\s*=\s*frozenset\(\((.*?)\)\)`)
var quotedPythonString = regexp.MustCompile(`"([^"]*)"|'([^']*)'`)

func pythonProtocolVersion(t *testing.T, source string) int {
	t.Helper()
	match := protocolAssignment.FindStringSubmatch(source)
	if len(match) != 2 {
		t.Fatal("cannot statically read bridge PROTOCOL_VERSION")
	}
	version, err := strconv.Atoi(match[1])
	if err != nil {
		t.Fatalf("parse bridge PROTOCOL_VERSION: %v", err)
	}
	return version
}

func pythonMethods(t *testing.T, source string) map[string]struct{} {
	t.Helper()
	match := methodsAssignment.FindStringSubmatch(source)
	if len(match) != 2 {
		t.Fatal("cannot statically read bridge METHODS")
	}
	methods := make(map[string]struct{})
	for _, item := range quotedPythonString.FindAllStringSubmatch(match[1], -1) {
		if len(item) != 3 {
			t.Fatalf("invalid bridge METHODS item %q", item)
		}
		method := item[1]
		if method == "" {
			method = item[2]
		}
		if method == "" {
			t.Fatalf("invalid empty bridge METHODS item %q", item)
		}
		if _, exists := methods[method]; exists {
			t.Fatalf("duplicate bridge METHODS item %q", method)
		}
		methods[method] = struct{}{}
	}
	return methods
}

func pythonMethodBody(t *testing.T, source, name string) string {
	t.Helper()
	marker := "    def " + name + "("
	start := strings.Index(source, marker)
	if start < 0 {
		t.Fatalf("cannot find bridge method %q", name)
	}
	rest := source[start+len(marker):]
	next := strings.Index(rest, "\n    def ")
	if next < 0 {
		t.Fatalf("cannot find end of bridge method %q", name)
	}
	return source[start : start+len(marker)+next]
}

func pythonCodeIdentifiers(source string) map[string]struct{} {
	tokens := make(map[string]struct{})
	for index := 0; index < len(source); {
		if source[index] == '#' {
			for index < len(source) && source[index] != '\n' {
				index++
			}
			continue
		}
		if source[index] == '\'' || source[index] == '"' {
			index = skipPythonString(source, index)
			continue
		}
		if isIdentifierStart(source[index]) {
			start := index
			index++
			for index < len(source) && isIdentifierPart(source[index]) {
				index++
			}
			tokens[strings.ToLower(source[start:index])] = struct{}{}
			continue
		}
		index++
	}
	return tokens
}

func skipPythonString(source string, start int) int {
	quote := source[start]
	triple := start+2 < len(source) && source[start+1] == quote && source[start+2] == quote
	index := start + 1
	if triple {
		index = start + 3
	}
	for index < len(source) {
		if source[index] == '\\' {
			index += 2
			continue
		}
		if triple {
			if index+2 < len(source) && source[index] == quote && source[index+1] == quote && source[index+2] == quote {
				return index + 3
			}
		} else if source[index] == quote {
			return index + 1
		}
		index++
	}
	return index
}

func isIdentifierStart(byteValue byte) bool {
	return byteValue == '_' || byteValue >= 'A' && byteValue <= 'Z' || byteValue >= 'a' && byteValue <= 'z'
}

func isIdentifierPart(byteValue byte) bool {
	return isIdentifierStart(byteValue) || byteValue >= '0' && byteValue <= '9'
}

func goStringLiterals(t *testing.T, path string) []string {
	t.Helper()
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	fileSet := token.NewFileSet()
	file := fileSet.AddFile(path, -1, len(source))
	var lexer scanner.Scanner
	lexer.Init(file, source, nil, 0)
	var values []string
	for _, current, literal := lexer.Scan(); current != token.EOF; _, current, literal = lexer.Scan() {
		if current != token.STRING {
			continue
		}
		value, err := strconv.Unquote(literal)
		if err != nil {
			t.Fatalf("unquote Go string in %s: %v", path, err)
		}
		values = append(values, value)
	}
	return values
}

func sortedKeys(values map[string]struct{}) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	// The test output only needs deterministic ordering; an insertion sort keeps this helper local.
	for index := 1; index < len(keys); index++ {
		for prior := index; prior > 0 && keys[prior] < keys[prior-1]; prior-- {
			keys[prior], keys[prior-1] = keys[prior-1], keys[prior]
		}
	}
	return keys
}
