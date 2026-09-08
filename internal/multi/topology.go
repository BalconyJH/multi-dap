package multi

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

var componentIDPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]*\.[0-9]+$`)

// CoreSpec binds one debugger-core ID to the ELF it owns. ELF is a Windows
// absolute path because this adapter executes in the Windows MULTI process.
type CoreSpec struct {
	ID  int
	ELF string
}

// TopologyCore is the copy-safe public view of one configured core.
type TopologyCore struct {
	ID  int
	ELF string
}

// Topology is the immutable, verified association between configured cores
// and MULTI program components. Its routing details deliberately remain
// package-private so callers cannot compose raw MULTI route commands.
type Topology struct {
	cores  []TopologyCore
	routes map[int]topologyRoute
}

type topologyRoute struct {
	component ComponentID
	elf       string
}

// ResolveTopology verifies a one-to-one association between configured core
// ELFs and the selected process shown by every debugger.name.* component. A
// PID alias and a P-slot are not core identities and are never consumed here.
func ResolveTopology(ctx context.Context, driver *Driver, specs []CoreSpec) (*Topology, error) {
	if driver == nil {
		return nil, errors.New("multi: topology requires driver")
	}
	configured, ordered, err := canonicalCoreSpecs(specs)
	if err != nil {
		return nil, err
	}

	components, err := driver.Components(ctx)
	if err != nil {
		return nil, fmt.Errorf("multi: topology components: %w", err)
	}
	programs, err := programComponents(components)
	if err != nil {
		return nil, err
	}
	if len(programs) != len(configured) {
		return nil, fmt.Errorf("multi: topology has %d program components for %d configured cores", len(programs), len(configured))
	}

	routes := make(map[int]topologyRoute, len(configured))
	for _, program := range programs {
		selected, err := routedSelectedProcess(ctx, driver, program)
		if err != nil {
			return nil, fmt.Errorf("multi: topology component %q: %w", program, err)
		}
		canonicalELF, err := canonicalWindowsELF(selected.Program)
		if err != nil {
			return nil, fmt.Errorf("multi: topology component %q selected program: %w", program, err)
		}
		spec, found := configured[canonicalELF]
		if !found {
			return nil, fmt.Errorf("multi: topology component %q selected an unconfigured program", program)
		}
		if _, duplicate := routes[spec.ID]; duplicate {
			return nil, fmt.Errorf("multi: topology has duplicate selected program for core %d", spec.ID)
		}
		routes[spec.ID] = topologyRoute{component: program, elf: canonicalELF}
	}
	if len(routes) != len(configured) {
		return nil, errors.New("multi: topology is missing configured programs")
	}
	return &Topology{cores: ordered, routes: routes}, nil
}

// Cores returns an independent copy in stable core-ID order.
func (t *Topology) Cores() []TopologyCore {
	if t == nil {
		return nil
	}
	return append([]TopologyCore(nil), t.cores...)
}

// routed re-verifies the selected program immediately before issuing command.
// It never refreshes bindings: component churn or route reuse is an error.
func (t *Topology) routed(ctx context.Context, driver *Driver, core int, command string) (commandResult, error) {
	if t == nil {
		return commandResult{}, errors.New("multi: nil topology")
	}
	if driver == nil {
		return commandResult{}, errors.New("multi: topology requires driver")
	}
	if err := validateVerifiedCommand(command); err != nil {
		return commandResult{}, err
	}
	route, err := t.verifiedRoute(ctx, driver, core)
	if err != nil {
		return commandResult{}, err
	}
	return runRoutedCommand(ctx, driver, route.component, command)
}

// verifiedRoute rechecks the immutable configured-core binding without
// issuing a side-effecting command. It is shared by all topology adapters.
func (t *Topology) verifiedRoute(ctx context.Context, driver *Driver, core int) (topologyRoute, error) {
	if t == nil {
		return topologyRoute{}, errors.New("multi: nil topology")
	}
	if driver == nil {
		return topologyRoute{}, errors.New("multi: topology requires driver")
	}
	route, found := t.routes[core]
	if !found {
		return topologyRoute{}, fmt.Errorf("multi: topology has no configured core %d", core)
	}
	selected, err := routedSelectedProcess(ctx, driver, route.component)
	if err != nil {
		return topologyRoute{}, fmt.Errorf("multi: topology core %d verification: %w", core, err)
	}
	canonicalELF, err := canonicalWindowsELF(selected.Program)
	if err != nil {
		return topologyRoute{}, fmt.Errorf("multi: topology core %d selected program: %w", core, err)
	}
	if canonicalELF != route.elf {
		return topologyRoute{}, fmt.Errorf("multi: topology core %d route program drifted", core)
	}
	return route, nil
}

func canonicalCoreSpecs(specs []CoreSpec) (map[string]CoreSpec, []TopologyCore, error) {
	if len(specs) == 0 {
		return nil, nil, errors.New("multi: topology requires configured cores")
	}
	byELF := make(map[string]CoreSpec, len(specs))
	ids := make(map[int]struct{}, len(specs))
	ordered := make([]TopologyCore, 0, len(specs))
	for _, spec := range specs {
		if spec.ID < 0 {
			return nil, nil, fmt.Errorf("multi: configured core ID %d is negative", spec.ID)
		}
		if _, duplicate := ids[spec.ID]; duplicate {
			return nil, nil, fmt.Errorf("multi: duplicate configured core ID %d", spec.ID)
		}
		canonicalELF, err := canonicalWindowsELF(spec.ELF)
		if err != nil {
			return nil, nil, fmt.Errorf("multi: configured core %d ELF: %w", spec.ID, err)
		}
		if _, duplicate := byELF[canonicalELF]; duplicate {
			return nil, nil, fmt.Errorf("multi: duplicate configured ELF for core %d", spec.ID)
		}
		ids[spec.ID] = struct{}{}
		byELF[canonicalELF] = spec
		ordered = append(ordered, TopologyCore{ID: spec.ID, ELF: spec.ELF})
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })
	return byELF, ordered, nil
}

func programComponents(components []Component) ([]ComponentID, error) {
	programs := make([]ComponentID, 0)
	seen := make(map[ComponentID]struct{})
	for _, component := range components {
		if component.Role != ComponentProgram {
			continue
		}
		if !componentIDPattern.MatchString(string(component.ID)) {
			return nil, fmt.Errorf("multi: program component has unsafe ID %q", component.ID)
		}
		if _, duplicate := seen[component.ID]; duplicate {
			return nil, fmt.Errorf("multi: duplicate program component %q", component.ID)
		}
		seen[component.ID] = struct{}{}
		programs = append(programs, component.ID)
	}
	if len(programs) == 0 {
		return nil, errors.New("multi: topology has no program components")
	}
	sort.Slice(programs, func(i, j int) bool { return programs[i] < programs[j] })
	return programs, nil
}

func routedSelectedProcess(ctx context.Context, driver *Driver, component ComponentID) (Process, error) {
	result, err := runRoutedCommand(ctx, driver, component, "P")
	if err != nil {
		return Process{}, err
	}
	if result.RawLossy {
		return Process{}, protocolError("routed process list", errors.New("process text is lossy"))
	}
	processes, err := ParseProcesses(result.Raw)
	if err != nil {
		return Process{}, protocolError("routed process list text", err)
	}
	var selected []Process
	for _, process := range processes {
		if process.Selected {
			selected = append(selected, process)
		}
	}
	if len(selected) != 1 {
		return Process{}, fmt.Errorf("multi: routed process list selected %d processes", len(selected))
	}
	process := selected[0]
	if strings.TrimSpace(process.Program) == "" {
		return Process{}, errors.New("multi: selected process has no program")
	}
	if process.Status != StatusStopped && process.Status != StatusRunning {
		return Process{}, fmt.Errorf("multi: selected process has unstable status %s", process.Status)
	}
	return process, nil
}

func runRoutedCommand(ctx context.Context, driver *Driver, component ComponentID, command string) (commandResult, error) {
	if !componentIDPattern.MatchString(string(component)) {
		return commandResult{}, fmt.Errorf("multi: unsafe program component ID %q", component)
	}
	if err := validateVerifiedCommand(command); err != nil {
		return commandResult{}, err
	}
	return driver.runCommands(ctx, "route "+string(component)+" "+command)
}

func validateVerifiedCommand(command string) error {
	if strings.TrimSpace(command) == "" {
		return errors.New("multi: routed command is required")
	}
	if strings.ContainsAny(command, "\x00\r\n") {
		return errors.New("multi: routed command contains an unsafe separator")
	}
	return nil
}

func canonicalWindowsELF(value string) (string, error) {
	if value == "" || value != strings.TrimSpace(value) || strings.ContainsAny(value, "\x00\r\n") {
		return "", errors.New("must be a non-empty Windows absolute path")
	}
	value = strings.ReplaceAll(value, "/", "\\")
	if len(value) >= 3 && value[1] == ':' && value[2] == '\\' && isASCIIAlpha(value[0]) {
		parts, err := cleanWindowsPathParts(strings.Split(value[3:], "\\"))
		if err != nil {
			return "", err
		}
		return strings.ToLower(value[:2] + "\\" + strings.Join(parts, "\\")), nil
	}
	if strings.HasPrefix(value, "\\\\") {
		raw := strings.Split(value[2:], "\\")
		if len(raw) < 3 || raw[0] == "" || raw[1] == "" {
			return "", errors.New("UNC path must name a file below its share")
		}
		if err := validateWindowsPathPart(raw[0]); err != nil {
			return "", err
		}
		if err := validateWindowsPathPart(raw[1]); err != nil {
			return "", err
		}
		parts, err := cleanWindowsPathParts(raw[2:])
		if err != nil {
			return "", err
		}
		return "\\\\" + strings.ToLower(raw[0]+"\\"+raw[1]+"\\"+strings.Join(parts, "\\")), nil
	}
	return "", errors.New("must be a Windows drive or UNC absolute path")
}

func cleanWindowsPathParts(raw []string) ([]string, error) {
	parts := make([]string, 0, len(raw))
	for _, part := range raw {
		switch part {
		case "", ".":
			continue
		case "..":
			if len(parts) == 0 {
				return nil, errors.New("path escapes its absolute root")
			}
			parts = parts[:len(parts)-1]
		default:
			if err := validateWindowsPathPart(part); err != nil {
				return nil, err
			}
			parts = append(parts, part)
		}
	}
	if len(parts) == 0 {
		return nil, errors.New("path must name a file")
	}
	return parts, nil
}

func validateWindowsPathPart(part string) error {
	if strings.ContainsAny(part, `<>:"|?*`) || strings.TrimSpace(part) != part {
		return errors.New("path has an invalid Windows component")
	}
	return nil
}

func isASCIIAlpha(value byte) bool {
	return value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z'
}
