package daemon

import (
	"testing"

	"github.com/Tacrolimus/multi-dap/internal/core/source"
	"github.com/Tacrolimus/multi-dap/internal/dap"
)

func TestSourcePathsRoundTripPathAndURI(t *testing.T) {
	paths, err := NewSourcePaths([]SourceRewrite{{
		DebugPrefix: "D:/build/project/src", ClientPrefix: `C:\workspace\project\src`,
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name       string
		initialize dap.InitializeArguments
		input      string
		wantClient string
	}{
		{"path", dap.InitializeArguments{PathFormat: "path"}, `C:\workspace\project\src\main.c`, "C:/workspace/project/src/main.c"},
		{"uri", dap.InitializeArguments{PathFormat: "uri"}, "file:///C:/workspace/project/src/main.c", "file:///C:/workspace/project/src/main.c"},
	} {
		t.Run(test.name, func(t *testing.T) {
			identity, err := paths.FromClient(test.initialize, dap.Source{Path: test.input})
			if err != nil {
				t.Fatal(err)
			}
			if identity.DebugPath != "D:/build/project/src/main.c" || identity.Key != source.Canonicalize(identity.DebugPath) {
				t.Fatalf("identity = %#v", identity)
			}
			presented, err := paths.ToClient(test.initialize, identity)
			if err != nil {
				t.Fatal(err)
			}
			if presented == nil || presented.Path != test.wantClient {
				t.Fatalf("presented = %#v, want %q", presented, test.wantClient)
			}
		})
	}
}

func TestSourcePathsMapsDebuggerFramesForward(t *testing.T) {
	paths, err := NewSourcePaths([]SourceRewrite{{DebugPrefix: "D:/build/src", ClientPrefix: "C:/work/src"}})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := paths.MapDebugPath(`d:\BUILD\src\module\file.c`)
	if err != nil {
		t.Fatal(err)
	}
	if identity.ClientPath != "C:/work/src/module/file.c" || identity.DebugPath != "d:/BUILD/src/module/file.c" {
		t.Fatalf("identity = %#v", identity)
	}
	uri, err := paths.ToClient(dap.InitializeArguments{PathFormat: "uri"}, identity)
	if err != nil || uri.Path != "file:///C:/work/src/module/file.c" {
		t.Fatalf("URI = %#v, %v", uri, err)
	}
}

func TestSourcePathsUsesComponentBoundariesAndDirectFallback(t *testing.T) {
	paths, err := NewSourcePaths([]SourceRewrite{{DebugPrefix: "D:/build/src", ClientPrefix: "C:/work/src"}})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := paths.MapDebugPath("D:/build/src-other/file.c")
	if err != nil {
		t.Fatal(err)
	}
	if identity.ClientPath != "D:/build/src-other/file.c" {
		t.Fatalf("partial prefix was rewritten: %#v", identity)
	}
}

func TestSourcePathsRejectsAmbiguousAndInvalidInputs(t *testing.T) {
	for _, rewrites := range [][]SourceRewrite{
		{{DebugPrefix: "D:/a", ClientPrefix: "C:/one"}, {DebugPrefix: "D:/a/sub", ClientPrefix: "C:/two"}},
		{{DebugPrefix: "D:/one", ClientPrefix: "C:/a"}, {DebugPrefix: "D:/two", ClientPrefix: "C:/a/sub"}},
		{{DebugPrefix: "", ClientPrefix: "C:/a"}},
	} {
		if _, err := NewSourcePaths(rewrites); err == nil {
			t.Fatalf("NewSourcePaths(%#v) succeeded", rewrites)
		}
	}
	paths, _ := NewSourcePaths(nil)
	for _, value := range []string{"https://example.test/main.c", "file:///C:/main.c?rev=1", "file:///"} {
		if _, err := paths.FromClient(dap.InitializeArguments{PathFormat: "uri"}, dap.Source{Path: value}); err == nil {
			t.Fatalf("FromClient(%q) succeeded", value)
		}
	}
}

func TestSourcePathsFormatsUNCFileURI(t *testing.T) {
	paths, _ := NewSourcePaths(nil)
	presented, err := paths.ToClient(dap.InitializeArguments{PathFormat: "uri"}, source.Identity{ClientPath: "//server/share/file.c"})
	if err != nil || presented.Path != "file://server/share/file.c" {
		t.Fatalf("UNC URI = %#v, %v", presented, err)
	}
}
