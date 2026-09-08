package multi

import "testing"

func TestStatusStringMatchesEveryConstant(t *testing.T) {
	cases := []struct {
		s    Status
		want string
	}{
		{StatusNil, "nil"},
		{StatusNoProcess, "no_process"},
		{StatusStopped, "stopped"},
		{StatusRunning, "running"},
		{StatusDying, "dying"},
		{StatusForking, "forking"},
		{StatusExecuting, "executing"},
		{StatusContinuing, "continuing"},
		{StatusZombie, "zombie"},
	}
	for _, c := range cases {
		if got := c.s.String(); got != c.want {
			t.Errorf("Status(%d).String() = %q, want %q", int(c.s), got, c.want)
		}
	}
}

func TestStatusConstantValuesMatchMULTI(t *testing.T) {
	// These integer values match the verified $_STATE contract; GetStatus() returns
	// them directly, so the constants must not be renumbered.
	cases := []struct {
		s    Status
		want int
	}{
		{StatusNil, 0},
		{StatusNoProcess, 1},
		{StatusStopped, 2},
		{StatusRunning, 3},
		{StatusDying, 4},
		{StatusForking, 5},
		{StatusExecuting, 6},
		{StatusContinuing, 7},
		{StatusZombie, 8},
	}
	for _, c := range cases {
		if int(c.s) != c.want {
			t.Errorf("%s = %d, want %d", c.s.String(), int(c.s), c.want)
		}
	}
}

func TestStatusIsStopped(t *testing.T) {
	if !StatusStopped.IsStopped() {
		t.Error("StatusStopped.IsStopped() = false, want true")
	}
	notStopped := []Status{
		StatusNil, StatusNoProcess, StatusRunning, StatusDying,
		StatusForking, StatusExecuting, StatusContinuing, StatusZombie,
	}
	for _, s := range notStopped {
		if s.IsStopped() {
			t.Errorf("%s.IsStopped() = true, want false", s)
		}
	}
}

func TestStatusIsRunning(t *testing.T) {
	running := []Status{StatusRunning, StatusExecuting, StatusContinuing}
	for _, s := range running {
		if !s.IsRunning() {
			t.Errorf("%s.IsRunning() = false, want true", s)
		}
	}
	notRunning := []Status{
		StatusNil, StatusNoProcess, StatusStopped, StatusDying,
		StatusForking, StatusZombie,
	}
	for _, s := range notRunning {
		if s.IsRunning() {
			t.Errorf("%s.IsRunning() = true, want false", s)
		}
	}
}

func TestStatusStringUnknownValue(t *testing.T) {
	s := Status(99)
	got := s.String()
	if got == "" {
		t.Fatal("String() on an out-of-range Status must not be empty")
	}
	// Must not silently claim to be one of the known states.
	for _, known := range []string{
		"nil", "no_process", "stopped", "running", "dying",
		"forking", "executing", "continuing", "zombie",
	} {
		if got == known {
			t.Fatalf("Status(99).String() = %q, collides with a real state", got)
		}
	}
}
