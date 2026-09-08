package multi

import (
	"fmt"
	"regexp"
	"strings"
)

var componentLine = regexp.MustCompile(`^\s*([^\s]+)(?: \((.*)\))?\s*$`)

// ComponentID is an opaque identifier emitted by MULTI's components command.
type ComponentID string

// ComponentRole captures the stable component categories observed in M0.
type ComponentRole uint8

const (
	ComponentUnknown ComponentRole = iota
	ComponentProgram
	// ComponentPIDAlias is MULTI's debugger.pid.N alias. N is a MULTI
	// process identifier, not a target-core ordinal.
	ComponentPIDAlias
	ComponentDebugServer
)

// Component is one registered MULTI component. A debugger.pid.N descriptor is
// a routable process alias; its suffix is recorded as ProcessID and must not
// be interpreted as a target-core ID.
type Component struct {
	ID        ComponentID
	Role      ComponentRole
	ProcessID uint64
	Name      string
}

// ParseComponents parses components command output captured from MULTI 7.1.6d.
// The undocumented text format is pinned by testdata/components.txt.
func ParseComponents(output string) ([]Component, error) {
	var components []Component
	seenIDs := make(map[ComponentID]struct{})
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line == "The currently registered components are:" {
			continue
		}
		match := componentLine.FindStringSubmatch(line)
		if match == nil {
			return nil, fmt.Errorf("parse components: malformed row")
		}
		component := Component{ID: ComponentID(match[1])}
		if _, duplicate := seenIDs[component.ID]; duplicate {
			return nil, fmt.Errorf("parse components: duplicate component ID")
		}
		seenIDs[component.ID] = struct{}{}
		descriptor := match[2]
		switch {
		case strings.HasPrefix(descriptor, "debugger.pid."):
			processID, ok := parseUint(strings.TrimPrefix(descriptor, "debugger.pid."))
			if !ok || processID == 0 {
				return nil, fmt.Errorf("parse components: invalid process alias")
			}
			component.Role = ComponentPIDAlias
			component.ProcessID = processID
		case strings.HasPrefix(descriptor, "debugger.name."):
			component.Role = ComponentProgram
			component.Name = strings.TrimPrefix(descriptor, "debugger.name.")
		case strings.HasPrefix(descriptor, "debugserver.name."):
			component.Role = ComponentDebugServer
			component.Name = strings.TrimPrefix(descriptor, "debugserver.name.")
		}
		components = append(components, component)
	}
	if len(components) == 0 {
		return nil, fmt.Errorf("parse components: no rows")
	}
	return components, nil
}
