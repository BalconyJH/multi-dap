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
	// NameAmbiguous marks valid metadata-like suffixes in the preserved display
	// tail. Commas are unescaped: Name is not a parsed ELF path, and suffixes
	// after the name never establish ProcessID or routing.
	NameAmbiguous bool
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
	seenPID, seenName := false, false
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
			if seenPID {
				return fmt.Errorf("parse components: duplicate process alias")
			}
			seenPID = true
			if component.Role == ComponentDebugServer {
				return fmt.Errorf("parse components: conflicting component roles")
			}
			if seenName {
				component.NameAmbiguous = true
			} else {
				component.Role = ComponentPIDAlias
				component.ProcessID = processID
			}
		case strings.HasPrefix(alias, "debugger.core."):
			if !validComponentCoreAlias(strings.TrimPrefix(alias, "debugger.core.")) {
				return fmt.Errorf("parse components: invalid core alias")
			}
			if seenName {
				component.NameAmbiguous = true
			}
		case strings.HasPrefix(alias, "debugger.name."), strings.HasPrefix(alias, "debugserver.name."):
			if seenName {
				return fmt.Errorf("parse components: conflicting name aliases")
			}
			seenName = true
			prefix := "debugger.name."
			if strings.HasPrefix(alias, prefix) {
				component.Role = ComponentProgram
			} else {
				if seenPID {
					return fmt.Errorf("parse components: conflicting component roles")
				}
				prefix = "debugserver.name."
				component.Role = ComponentDebugServer
			}
			if strings.TrimSpace(strings.TrimPrefix(alias, prefix)) == "" {
				return fmt.Errorf("parse components: empty name alias")
			}
			// Keep all commas and alias-looking bytes: only routed P establishes
			// the selected program's full ELF path.
			component.Name = strings.TrimPrefix(strings.TrimSpace(strings.Join(aliases[index:], ",")), prefix)
		}
	}
	return nil
}

// Observed core aliases are an ordinal or a server name followed by an ordinal.
// Syntax validation never establishes a target-core identity.
func validComponentCoreAlias(suffix string) bool {
	parts := strings.Split(suffix, ".")
	if _, ok := parseUint(parts[len(parts)-1]); !ok {
		return false
	}
	for _, part := range parts[:len(parts)-1] {
		if part == "" {
			return false
		}
		for _, c := range part {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
				return false
			}
		}
	}
	return true
}
