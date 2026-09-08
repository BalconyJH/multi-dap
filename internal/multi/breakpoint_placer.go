package multi

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/Tacrolimus/multi-dap/internal/core/breakpoint"
	"github.com/Tacrolimus/multi-dap/internal/core/source"
)

var (
	breakpointLocationPattern = regexp.MustCompile(`^.+#([1-9][0-9]*)$`)
	sourceLocatorPattern      = regexp.MustCompile(`^[A-Za-z0-9_./:\\-]+$`)
)

// BreakpointPlacer uses one immutable configured-core topology. Every MULTI
// command revalidates the selected program before the actual operation.
type BreakpointPlacer struct {
	topology *Topology
	mu       sync.RWMutex
	known    map[int]map[string]struct{}
}

func NewBreakpointPlacer(topology *Topology) (*BreakpointPlacer, error) {
	if topology == nil || len(topology.Cores()) == 0 {
		return nil, errors.New("multi: breakpoint placer requires topology")
	}
	return &BreakpointPlacer{topology: topology, known: make(map[int]map[string]struct{})}, nil
}

func (p *BreakpointPlacer) Set(ctx context.Context, driver *Driver, request breakpoint.PlacementRequest) (breakpoint.Physical, error) {
	if err := validatePlacement(request); err != nil {
		return breakpoint.Physical{}, err
	}
	before, err := p.list(ctx, driver, request.Core)
	if err != nil {
		return breakpoint.Physical{}, fmt.Errorf("multi: list breakpoints before placement: %w", err)
	}
	token := commandToken(request.HintToken)
	if containsCommandToken(before, token) {
		return breakpoint.Physical{}, errors.New("multi: requested hint token is already present")
	}
	if _, err := p.routed(ctx, driver, request.Core, fmt.Sprintf("b %s#%d {%s}", request.Source.DebugPath, request.Line, token)); err != nil {
		return breakpoint.Physical{}, fmt.Errorf("multi: set breakpoint: %w", err)
	}
	// From this point on MULTI accepted b, so an unreadable or ambiguous B
	// listing cannot be represented as a zero-value failure. The Store owns
	// this token-bound recovery intent and will attempt an exact cleanup.
	intent := breakpoint.Physical{
		Core:              request.Core,
		RequestedLine:     request.Line,
		HintToken:         request.HintToken,
		PossiblyCommitted: true,
	}
	after, err := p.list(ctx, driver, request.Core)
	if err != nil {
		return intent, fmt.Errorf("multi: list breakpoints after placement: %w", err)
	}
	placed, err := newlyPlaced(before, after, token)
	if err != nil {
		return intent, err
	}
	intent.MULTIHandle = strconv.FormatUint(placed.Index, 10)
	line, err := breakpointLineNumber(placed.Location)
	if err != nil {
		return intent, err
	}
	p.remember(request.Core, request.Source.Key)
	return breakpoint.Physical{Core: request.Core, RequestedLine: request.Line, ActualLine: line, MULTIHandle: intent.MULTIHandle, HintToken: request.HintToken}, nil
}

func (p *BreakpointPlacer) Clear(ctx context.Context, driver *Driver, physical breakpoint.Physical) error {
	before, err := p.list(ctx, driver, physical.Core)
	if err != nil {
		return fmt.Errorf("multi: list breakpoints before clear: %w", err)
	}
	if physical.HintToken == 0 {
		return errors.New("multi: breakpoint recovery requires a hint token")
	}
	token := commandToken(physical.HintToken)
	if physical.MULTIHandle == "" {
		return p.clearExactToken(ctx, driver, physical.Core, before, token)
	}
	index, err := parseBreakpointHandle(physical.MULTIHandle)
	if err != nil {
		return err
	}
	located, present := breakpointByIndex(before, index)
	if !present {
		// The previous Clear might have committed before its confirmation was
		// lost. A complete B listing can still prove absence by exact token.
		return p.clearExactToken(ctx, driver, physical.Core, before, token)
	}
	if !breakpointHasToken(located, token) {
		return fmt.Errorf("multi: recorded breakpoint %d has been reused by another command token", index)
	}
	after, err := p.deleteExact(ctx, driver, physical.Core, before, index, token)
	if err != nil {
		return err
	}
	if containsExactToken(after, token) {
		return p.clearExactToken(ctx, driver, physical.Core, after, token)
	}
	return nil
}

// clearExactToken removes every exact occurrence of a command token. It is
// used when Set never learned a handle and when a previously-known handle has
// disappeared. Zero matches in a complete B listing is positive evidence that
// no recoverable physical breakpoint remains.
func (p *BreakpointPlacer) clearExactToken(ctx context.Context, driver *Driver, core int, listed []Breakpoint, token string) error {
	for {
		matches := breakpointsWithToken(listed, token)
		if len(matches) == 0 {
			return nil
		}
		after, err := p.deleteExact(ctx, driver, core, listed, matches[0].Index, token)
		if err != nil {
			return err
		}
		listed = after
	}
}

// deleteExact verifies the index/token pair immediately before deletion and
// again afterwards. The index alone is not ownership evidence because MULTI
// can reuse it after a prior deletion.
func (p *BreakpointPlacer) deleteExact(ctx context.Context, driver *Driver, core int, before []Breakpoint, index uint64, token string) ([]Breakpoint, error) {
	value, present := breakpointByIndex(before, index)
	if !present || !breakpointHasToken(value, token) {
		return nil, fmt.Errorf("multi: breakpoint %d no longer has the owned command token", index)
	}
	// MULTI's d command treats an unprefixed number as an address expression.
	// A breakpoint handle must carry the documented % prefix, including handle 0.
	if _, err := p.routed(ctx, driver, core, "d %"+strconv.FormatUint(index, 10)); err != nil {
		return nil, fmt.Errorf("multi: clear breakpoint: %w", err)
	}
	after, err := p.list(ctx, driver, core)
	if err != nil {
		return nil, fmt.Errorf("multi: list breakpoints after clear: %w", err)
	}
	if value, present := breakpointByIndex(after, index); present && breakpointHasToken(value, token) {
		return nil, fmt.Errorf("multi: breakpoint %d with owned command token remains after clear", index)
	}
	return after, nil
}

func (p *BreakpointPlacer) SourceKnown(_ context.Context, core int, identity source.Identity) (bool, error) {
	if p == nil || p.topology == nil {
		return false, errors.New("multi: breakpoint placer is not initialized")
	}
	if _, ok := p.topology.routes[core]; !ok {
		return false, fmt.Errorf("multi: no configured core %d", core)
	}
	if identity.Key == "" {
		return false, errors.New("multi: source key is required")
	}
	p.mu.RLock()
	_, ok := p.known[core][identity.Key]
	p.mu.RUnlock()
	if ok {
		return true, nil
	}
	return false, fmt.Errorf("%w: source-known has no verified read-only MULTI primitive", ErrUnsupported)
}

func (p *BreakpointPlacer) routed(ctx context.Context, driver *Driver, core int, command string) (commandResult, error) {
	if p == nil || driver == nil || p.topology == nil {
		return commandResult{}, errors.New("multi: breakpoint placer is not initialized")
	}
	result, err := p.topology.routed(ctx, driver, core, command)
	if err != nil {
		return commandResult{}, err
	}
	if result.RawLossy {
		return commandResult{}, protocolError("routed breakpoint command", errors.New("MULTI command output is lossy"))
	}
	return result, nil
}

func (p *BreakpointPlacer) list(ctx context.Context, driver *Driver, core int) ([]Breakpoint, error) {
	result, err := p.routed(ctx, driver, core, "B")
	if err != nil {
		return nil, err
	}
	values, err := ParseBreakpoints(result.Raw)
	if err != nil {
		return nil, protocolError("breakpoint list text", err)
	}
	return values, nil
}

func (p *BreakpointPlacer) remember(core int, key string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.known[core] == nil {
		p.known[core] = make(map[string]struct{})
	}
	p.known[core][key] = struct{}{}
}

func validatePlacement(request breakpoint.PlacementRequest) error {
	if request.Line <= 0 {
		return errors.New("multi: breakpoint line must be positive")
	}
	if request.HintToken == 0 {
		return errors.New("multi: breakpoint hint token is required")
	}
	if err := source.ValidateTargetIdentity(request.Source); err != nil {
		return fmt.Errorf("multi: breakpoint source identity: %w", err)
	}
	if !sourceLocatorPattern.MatchString(request.Source.DebugPath) {
		return errors.New("multi: source path cannot be represented by verified breakpoint syntax")
	}
	return nil
}
func commandToken(token uint32) string { return fmt.Sprintf(`mprintf("HIT 0x%08X\n")`, token) }
func containsCommandToken(values []Breakpoint, token string) bool {
	for _, value := range values {
		if value.HasCommand && string(value.CommandToken) == token {
			return true
		}
	}
	return false
}

func breakpointHasToken(value Breakpoint, token string) bool {
	return value.HasCommand && string(value.CommandToken) == token
}

func containsExactToken(values []Breakpoint, token string) bool {
	return len(breakpointsWithToken(values, token)) != 0
}

func breakpointsWithToken(values []Breakpoint, token string) []Breakpoint {
	matches := make([]Breakpoint, 0)
	for _, value := range values {
		if breakpointHasToken(value, token) {
			matches = append(matches, value)
		}
	}
	return matches
}
func newlyPlaced(before, after []Breakpoint, token string) (Breakpoint, error) {
	old := make(map[uint64]struct{}, len(before))
	for _, value := range before {
		old[value.Index] = struct{}{}
	}
	var matches []Breakpoint
	for _, value := range after {
		if _, existed := old[value.Index]; !existed && value.HasCommand && string(value.CommandToken) == token {
			matches = append(matches, value)
		}
	}
	if len(matches) != 1 {
		return Breakpoint{}, fmt.Errorf("multi: expected exactly one newly listed breakpoint for hint token, found %d", len(matches))
	}
	return matches[0], nil
}
func breakpointLineNumber(location string) (int, error) {
	match := breakpointLocationPattern.FindStringSubmatch(strings.TrimSpace(location))
	if len(match) != 2 {
		return 0, fmt.Errorf("multi: breakpoint location %q has no verified source line", location)
	}
	line, err := strconv.Atoi(match[1])
	if err != nil || line <= 0 {
		return 0, fmt.Errorf("multi: breakpoint location %q has invalid source line", location)
	}
	return line, nil
}
func parseBreakpointHandle(value string) (uint64, error) {
	if value == "" || strings.HasPrefix(value, "+") || strings.HasPrefix(value, "-") {
		return 0, fmt.Errorf("multi: invalid breakpoint handle %q", value)
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("multi: invalid breakpoint handle %q", value)
	}
	return parsed, nil
}
func breakpointByIndex(values []Breakpoint, index uint64) (Breakpoint, bool) {
	for _, value := range values {
		if value.Index == index {
			return value, true
		}
	}
	return Breakpoint{}, false
}
