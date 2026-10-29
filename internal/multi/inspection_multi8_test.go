package multi

import "testing"

func TestScalarDisplaysPreserveCharacterAndIntegerRepresentations(t *testing.T) {
	// Synthetic cases protect the display-only contract around MULTI 8's
	// documented char-bitfield change. They are not vendor output fixtures.
	for _, test := range []struct{ name, display string }{
		{"flags.bits", "'a'"},
		{"flags.bits", "'\\017'"},
		{"flags.bits", "0"},
		{"flags.bits", "15"},
		{"flags.bits", "-1"},
		{"flags.bits", "0x0f"},
		{"counter", "18446744073709551615"},
	} {
		t.Run(test.display, func(t *testing.T) {
			value, err := ParseValue(test.name + " = " + test.display + "\n")
			if err != nil {
				t.Fatal(err)
			}
			if value.Name != test.name || value.Display != test.display ||
				value.State != ValueAvailable || value.Hidden || value.HasChildren {
				t.Fatalf("scalar display changed meaning: %#v", value)
			}
		})
	}
}
