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

// ParseComponents parses components command output captured from MULTI 7.1.6d
// and 8.1.4. The undocumented text formats are pinned by testdata/components*.txt.
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
		if err := parseComponentAliases(&component, match[2]); err != nil {
			return nil, err
		}
		components = append(components, component)
	}
	if len(components) == 0 {
		return nil, fmt.Errorf("parse components: no rows")
	}
	return components, nil
}

func parseComponentAliases(component *Component, descriptor string) error {
	if descriptor == "" {
		return nil
	}
	aliases := strings.Split(descriptor, ",")
	for index, rawAlias := range aliases {
		alias := strings.TrimSpace(rawAlias)
		switch {
		case alias == "":
			return fmt.Errorf("parse components: empty alias")
		case strings.HasPrefix(alias, "debugger.pid."):
			processID, ok := parseUint(strings.TrimPrefix(alias, "debugger.pid."))
			if !ok || processID == 0 {
				return fmt.Errorf("parse components: invalid process alias")
			}
			if component.ProcessID != 0 {
				return fmt.Errorf("parse components: duplicate process alias")
			}
			component.Role = ComponentPIDAlias
			component.ProcessID = processID
		case strings.HasPrefix(alias, "debugger.name."), strings.HasPrefix(alias, "debugserver.name."):
			// Preserve literal commas in names; only known alias prefixes are ambiguous.
			for _, remainder := range aliases[index+1:] {
				remainder = strings.TrimSpace(remainder)
				if strings.HasPrefix(remainder, "debugger.pid.") ||
					strings.HasPrefix(remainder, "debugger.core.") ||
					strings.HasPrefix(remainder, "debugger.name.") ||
					strings.HasPrefix(remainder, "debugserver.name.") {
					return fmt.Errorf("parse components: ambiguous name alias")
				}
			}
			name := strings.TrimSpace(strings.Join(aliases[index:], ","))
			if strings.HasPrefix(alias, "debugger.name.") {
				component.Role = ComponentProgram
				component.Name = strings.TrimPrefix(name, "debugger.name.")
			} else {
				if component.ProcessID != 0 {
					return fmt.Errorf("parse components: conflicting component roles")
				}
				component.Role = ComponentDebugServer
				component.Name = strings.TrimPrefix(name, "debugserver.name.")
			}
			if strings.TrimSpace(component.Name) == "" {
				return fmt.Errorf("parse components: empty name alias")
			}
			return nil
		}
	}
	return nil
}
