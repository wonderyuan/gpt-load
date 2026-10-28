package accessquota

import "fmt"

// PrepareFreshImport validates a one-shot migration into a key with no native
// quota rules. The control plane must keep the key disabled and drain requests
// before calling this method, through commit and publication. It deliberately
// cannot replace/reset an existing quota entry. Preparation has no side effects;
// invoke the returned installer only after the rules and checkpoints commit.
func (runtime *Runtime) PrepareFreshImport(accessKeyID uint, rules []Rule, states []RestoredState) (func() error, error) {
	if runtime == nil || accessKeyID == 0 || len(rules) == 0 || len(rules) != len(states) {
		return nil, fmt.Errorf("prepare access quota import: invalid target or rule count")
	}
	normalized, err := normalizeDefinitions(map[uint][]Rule{accessKeyID: rules})
	if err != nil {
		return nil, err
	}
	restored := make(map[uint]RestoredState, len(states))
	for _, state := range states {
		if err := validateRestoredState(state); err != nil {
			return nil, err
		}
		if state.AccessKeyID != accessKeyID {
			return nil, fmt.Errorf("prepare access quota import: state belongs to another key")
		}
		if _, duplicate := restored[state.RuleID]; duplicate {
			return nil, fmt.Errorf("prepare access quota import: duplicate state")
		}
		restored[state.RuleID] = cloneRestoredState(state)
	}
	entries, err := entriesFromRestore(normalized, restored)
	if err != nil {
		return nil, err
	}
	entry := entries[accessKeyID]
	validateFreshLocked := func() error {
		if runtime.restorePending || runtime.entries[accessKeyID] != nil {
			return fmt.Errorf("import access quota: target already has native quota state")
		}
		for _, current := range runtime.entries {
			for ruleID := range entry.byID {
				if current.byID[ruleID] != nil {
					return fmt.Errorf("import access quota: rule belongs to another key")
				}
			}
		}
		return nil
	}
	runtime.mu.Lock()
	err = validateFreshLocked()
	runtime.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return func() error {
		runtime.mu.Lock()
		defer runtime.mu.Unlock()
		if err := validateFreshLocked(); err != nil {
			return err
		}
		if runtime.entries == nil {
			runtime.entries = make(map[uint]*accessKeyEntry)
		}
		// These states are already durable at SnapshotVersion; do not mark them
		// dirty. Subsequent admissions/completions advance the native checkpoint.
		runtime.entries[accessKeyID] = entry
		return nil
	}, nil
}
