package accessquota

import (
	"fmt"
	"reflect"
)

type periodicRebaseKey struct {
	accessKeyID uint
	ruleID      uint
	revision    uint64
}

type periodicRebaseSeed struct {
	definition Rule
	state      RestoredState
}

func (seed periodicRebaseSeed) key() periodicRebaseKey {
	return periodicRebaseKey{
		accessKeyID: seed.state.AccessKeyID,
		ruleID:      seed.definition.ID,
		revision:    seed.definition.Revision,
	}
}

// PreparePeriodicRebase validates durable replacement checkpoints for an affected
// subset of a key's periodic rules. IDs, periods and limits must be unchanged;
// each proposed revision must be the current revision plus one, with a matching
// state at SnapshotVersion 1. Preparation has no side effects or retained locks.
//
// Call the installer only after the rules and checkpoints commit. It stages the
// seeds without replacing entries or changing counters. Reconcile applies them
// atomically with publication of the changed revisions. The caller must serialize
// configuration writes and fence admissions with the manager publication lock;
// no runtime lock is held over the transaction or publication callback. Startup
// after a crash uses the ordinary Restore path for the committed checkpoints.
func (runtime *Runtime) PreparePeriodicRebase(accessKeyID uint, rules []Rule, states []RestoredState) (func() error, error) {
	if runtime == nil || accessKeyID == 0 || len(rules) == 0 || len(rules) != len(states) {
		return nil, fmt.Errorf("prepare periodic access quota rebase: invalid target or rule count")
	}
	normalized, err := normalizeDefinitions(map[uint][]Rule{accessKeyID: rules})
	if err != nil {
		return nil, err
	}
	for _, rule := range normalized[accessKeyID] {
		if rule.Kind != KindPeriodic {
			return nil, fmt.Errorf("prepare periodic access quota rebase rule %d: rule is not periodic", rule.ID)
		}
	}
	restored := make(map[uint]RestoredState, len(states))
	for _, state := range states {
		if err := validateRestoredState(state); err != nil {
			return nil, err
		}
		if state.AccessKeyID != accessKeyID || state.SnapshotVersion != 1 {
			return nil, fmt.Errorf("prepare periodic access quota rebase rule %d: wrong key or checkpoint version", state.RuleID)
		}
		if _, duplicate := restored[state.RuleID]; duplicate {
			return nil, fmt.Errorf("prepare periodic access quota rebase rule %d: duplicate state", state.RuleID)
		}
		restored[state.RuleID] = cloneRestoredState(state)
	}
	// Reuse restore's exact ownership/revision and physical-window validation,
	// including applyRestoredState, against detached entries only.
	entries, err := entriesFromRestore(normalized, restored)
	if err != nil {
		return nil, err
	}
	seeds := make([]periodicRebaseSeed, 0, len(rules))
	for _, rule := range entries[accessKeyID].rules {
		seeds = append(seeds, periodicRebaseSeed{
			definition: rule.definition,
			state:      restoredState(accessKeyID, rule),
		})
	}

	runtime.mu.RLock()
	for _, seed := range seeds {
		if _, err := runtime.validatePeriodicRebaseLocked(seed, false); err != nil {
			runtime.mu.RUnlock()
			return nil, err
		}
	}
	runtime.mu.RUnlock()

	return func() error {
		runtime.mu.Lock()
		defer runtime.mu.Unlock()
		pending := make([]periodicRebaseSeed, 0, len(seeds))
		for _, seed := range seeds {
			applied, err := runtime.validatePeriodicRebaseLocked(seed, true)
			if err != nil {
				return err
			}
			if applied {
				continue
			}
			if staged, exists := runtime.periodicRebases[seed.key()]; exists && !reflect.DeepEqual(staged, seed) {
				return fmt.Errorf("stage periodic access quota rebase rule %d: conflicting seed", seed.definition.ID)
			}
			pending = append(pending, seed)
		}
		// Validate the entire batch before staging anything. Replays of applied
		// revisions do not restage old checkpoints or overwrite accrued usage.
		if len(pending) != 0 && runtime.periodicRebases == nil {
			runtime.periodicRebases = make(map[periodicRebaseKey]periodicRebaseSeed)
		}
		for _, seed := range pending {
			runtime.periodicRebases[seed.key()] = seed
		}
		return nil
	}, nil
}

// validatePeriodicRebaseLocked reads immutable rule definitions under runtime.mu.
// Counters may continue changing under entry locks throughout preparation/staging.
func (runtime *Runtime) validatePeriodicRebaseLocked(seed periodicRebaseSeed, allowApplied bool) (bool, error) {
	entry := runtime.entries[seed.state.AccessKeyID]
	if runtime.restorePending || entry == nil || entry.byID[seed.definition.ID] == nil {
		return false, fmt.Errorf("periodic access quota rebase rule %d: current rule is missing for key %d", seed.definition.ID, seed.state.AccessKeyID)
	}
	current := entry.byID[seed.definition.ID].definition
	if allowApplied && current.Revision > seed.definition.Revision {
		return true, nil
	}
	if current.Kind != KindPeriodic || current.PeriodSeconds != seed.definition.PeriodSeconds ||
		current.LimitNanoUSD != seed.definition.LimitNanoUSD {
		return false, fmt.Errorf("periodic access quota rebase rule %d: kind, period or limit changed", seed.definition.ID)
	}
	if allowApplied && current.Revision == seed.definition.Revision {
		return true, nil
	}
	if seed.definition.Revision != current.Revision+1 {
		return false, fmt.Errorf("periodic access quota rebase rule %d: proposed revision must be current plus one", seed.definition.ID)
	}
	return false, nil
}

// applyPeriodicRebaseLocked only writes a detached replacement rule. The caller
// must only invoke it for a changed revision and must not consume the returned
// key until the whole reconciliation has succeeded.
func (runtime *Runtime) applyPeriodicRebaseLocked(accessKeyID uint, rule *runtimeRule) (periodicRebaseKey, bool, error) {
	key := periodicRebaseKey{accessKeyID: accessKeyID, ruleID: rule.definition.ID, revision: rule.definition.Revision}
	seed, exists := runtime.periodicRebases[key]
	if !exists {
		return key, false, nil
	}
	if rule.definition != seed.definition {
		return key, false, fmt.Errorf("reconcile periodic access quota rebase rule %d: definition does not match staged seed", rule.definition.ID)
	}
	if err := applyRestoredState(rule, seed.state); err != nil {
		return key, false, err
	}
	// The replacement checkpoint was committed before staging; future usage
	// advances SnapshotVersion from 1 in the new rule revision.
	rule.dirty = false
	return key, true, nil
}
