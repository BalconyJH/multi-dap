package contract

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"

	"github.com/Tacrolimus/multi-dap/internal/perf"
)

func TestVSCodePerformanceOptionsMatchRecorderAndRemainOptIn(t *testing.T) {
	root := repositoryRoot(t)
	var manifest struct {
		Contributes struct {
			Debuggers []struct {
				Type                    string `json:"type"`
				ConfigurationAttributes struct {
					Attach struct {
						Properties   map[string]json.RawMessage `json:"properties"`
						Required     []string                   `json:"required"`
						Dependencies map[string][]string        `json:"dependencies"`
					} `json:"attach"`
				} `json:"configurationAttributes"`
				ConfigurationSnippets []struct {
					Body map[string]json.RawMessage `json:"body"`
				} `json:"configurationSnippets"`
			} `json:"debuggers"`
		} `json:"contributes"`
	}
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(root, "editors", "vscode", "package.json"))), &manifest); err != nil {
		t.Fatal(err)
	}
	matched := 0
	for _, debugger := range manifest.Contributes.Debuggers {
		if debugger.Type != "multi-dap" {
			continue
		}
		matched++
		attach := debugger.ConfigurationAttributes.Attach
		for name, expectedType := range map[string]string{"perfOutput": "string", "perfMaxEvents": "integer"} {
			var property map[string]json.RawMessage
			if err := json.Unmarshal(attach.Properties[name], &property); err != nil {
				t.Fatalf("missing or invalid %s schema: %v", name, err)
			}
			var actualType string
			if err := json.Unmarshal(property["type"], &actualType); err != nil || actualType != expectedType {
				t.Fatalf("incorrect %s type", name)
			}
			if _, exists := property["default"]; exists {
				t.Fatalf("%s must not inject recording configuration by default", name)
			}
			for _, required := range attach.Required {
				if required == name {
					t.Fatalf("%s must remain optional", name)
				}
			}
			for _, snippet := range debugger.ConfigurationSnippets {
				if _, exists := snippet.Body[name]; exists {
					t.Fatalf("default snippet implicitly selects %s", name)
				}
			}
			if name == "perfMaxEvents" {
				var minimum, maximum int
				if json.Unmarshal(property["minimum"], &minimum) != nil ||
					json.Unmarshal(property["maximum"], &maximum) != nil ||
					minimum != 1 || maximum != perf.MaxEvents {
					t.Fatal("VS Code event bounds disagree with the actual Go recorder")
				}
			}
		}
		dependencies := attach.Dependencies["perfMaxEvents"]
		if len(dependencies) != 1 || dependencies[0] != "perfOutput" {
			t.Fatal("event limit must depend on explicit capture output")
		}
	}
	if matched != 1 {
		t.Fatalf("multi-dap debugger registrations = %d, want 1", matched)
	}
	extension := readFile(t, filepath.Join(root, "editors", "vscode", "src", "extension.js"))
	match := regexp.MustCompile(`(?m)^const MAX_PERF_EVENTS = ([0-9]+);$`).FindStringSubmatch(extension)
	if len(match) != 2 {
		t.Fatal("missing explicit VS Code runtime event bound")
	}
	maximum, err := strconv.Atoi(match[1])
	if err != nil || maximum != perf.MaxEvents {
		t.Fatal("VS Code runtime event bound disagrees with the actual Go recorder")
	}
}
