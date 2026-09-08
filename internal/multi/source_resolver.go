package multi

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/Tacrolimus/multi-dap/internal/bridge"
	"github.com/Tacrolimus/multi-dap/internal/core/source"
)

// SourceProbe owns one per-core, per-Resolve source-list snapshot. It receives
// topology rather than a route token so the production probe must reverify the
// selected program immediately before its read-only command.
type SourceProbe interface {
	ProbeSource(context.Context, *Driver, *Topology, int, source.Identity) (source.Presence, error)
}

type SourceProbeFunc func(context.Context, *Driver, *Topology, int, source.Identity) (source.Presence, error)

func (f SourceProbeFunc) ProbeSource(ctx context.Context, driver *Driver, topology *Topology, core int, identity source.Identity) (source.Presence, error) {
	return f(ctx, driver, topology, core, identity)
}

// SourceResolver is bound exactly once to Actor.Open's verified topology. It
// intentionally has no cross-request source cache: an external GUI may reload
// unchanged ELF paths without an observable content-generation change.
type SourceResolver struct {
	mu       sync.Mutex
	expected []sourceResolverCore
	topology *Topology
	probe    SourceProbe
	bound    bool
}

type sourceResolverCore struct {
	id  int
	elf string
}

func NewSourceResolver(topology *Topology, probe SourceProbe) (*SourceResolver, error) {
	if topology == nil {
		return nil, errors.New("multi: source resolver requires topology")
	}
	expected, err := sourceResolverExpected(topology.Cores())
	if err != nil {
		return nil, err
	}
	if probe == nil {
		return nil, errors.New("multi: source resolver probe is required")
	}
	return &SourceResolver{expected: expected, topology: topology, probe: probe, bound: true}, nil
}

// NewDeferredSourceResolver permits daemon construction before Actor.Open.
func NewDeferredSourceResolver(specs []CoreSpec, probe SourceProbe) (*SourceResolver, error) {
	_, cores, err := canonicalCoreSpecs(specs)
	if err != nil {
		return nil, err
	}
	expected, err := sourceResolverExpected(cores)
	if err != nil {
		return nil, err
	}
	if probe == nil {
		return nil, errors.New("multi: source resolver probe is required")
	}
	return &SourceResolver{expected: expected, probe: probe}, nil
}

func sourceResolverExpected(cores []TopologyCore) ([]sourceResolverCore, error) {
	if len(cores) == 0 {
		return nil, errors.New("multi: source resolver requires configured cores")
	}
	expected := make([]sourceResolverCore, 0, len(cores))
	for _, core := range cores {
		elf, err := canonicalWindowsELF(core.ELF)
		if err != nil {
			return nil, fmt.Errorf("multi: source resolver core %d ELF: %w", core.ID, err)
		}
		expected = append(expected, sourceResolverCore{id: core.ID, elf: elf})
	}
	return expected, nil
}

func (r *SourceResolver) BindMULTITopology(topology *Topology) error {
	if r == nil || topology == nil {
		return errors.New("multi: source resolver requires topology")
	}
	actual, err := sourceResolverExpected(topology.Cores())
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.bound {
		if r.topology == topology {
			return nil
		}
		return errors.New("multi: source resolver topology is already bound")
	}
	if len(actual) != len(r.expected) {
		return errors.New("multi: source resolver topology core set mismatched")
	}
	for i := range actual {
		if actual[i] != r.expected[i] {
			return errors.New("multi: source resolver topology core set mismatched")
		}
	}
	r.topology = topology
	r.bound = true
	return nil
}

func (r *SourceResolver) Resolve(ctx context.Context, client *bridge.Client, identity source.Identity) ([]source.Observation, error) {
	driver, err := NewDriver(client)
	if err != nil {
		return nil, err
	}
	return r.resolve(ctx, driver, identity)
}

func (r *SourceResolver) resolve(ctx context.Context, driver *Driver, identity source.Identity) ([]source.Observation, error) {
	if r == nil || driver == nil {
		return nil, errors.New("multi: source resolver is not initialized")
	}
	if err := source.ValidateTargetIdentity(identity); err != nil {
		return nil, fmt.Errorf("multi: source identity: %w", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.bound || r.topology == nil {
		return nil, errors.New("multi: source resolver topology is not bound")
	}
	observations := make([]source.Observation, 0, len(r.expected))
	for _, configured := range r.expected {
		presence, err := r.probe.ProbeSource(ctx, driver, r.topology, configured.id, identity)
		if err != nil {
			return nil, fmt.Errorf("multi: source probe core %d: %w", configured.id, err)
		}
		if presence != source.PresenceUnknown && presence != source.PresencePresent && presence != source.PresenceAbsent {
			return nil, fmt.Errorf("multi: source probe core %d returned %s", configured.id, presence)
		}
		observations = append(observations, source.Observation{Core: configured.id, Presence: presence})
	}
	return observations, nil
}
