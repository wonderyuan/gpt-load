package accessquota

import (
	"math"
	"reflect"
	"testing"
	"time"
)

func TestPeriodicRebaseStagesUntilReconcilePreservesOtherState(t *testing.T) {
	runtime, definitions, now := periodicRebaseFixture(t)
	ticket, _ := runtime.Admit(1, now)
	runtime.Complete(ticket, 9)
	otherTicket, _ := runtime.Admit(2, now.Add(-time.Minute))
	runtime.Complete(otherTicket, 4)
	before, dirtyBefore := periodicRebaseStates(runtime), runtime.DirtySnapshots(-1)
	rules := []Rule{definitions[1][1]}
	rules[0].Revision++
	state := periodicRebaseCheckpoint(1, rules[0], now, 6)
	wantState := cloneRestoredState(state)
	install, err := runtime.PreparePeriodicRebase(1, rules, []RestoredState{state})
	if err != nil {
		t.Fatal(err)
	}
	if len(runtime.periodicRebases) != 0 || !reflect.DeepEqual(periodicRebaseStates(runtime), before) ||
		!reflect.DeepEqual(runtime.DirtySnapshots(-1), dirtyBefore) {
		t.Fatal("preparation changed live or staged state")
	}

	// Caller-owned rules and window pointers must not alias the prepared seed.
	rules[0].LimitNanoUSD++
	*state.WindowEndsAtMS = 0
	notifications := 0
	runtime.SetDirtyNotifier(func() { notifications++ })
	if err := install(); err != nil {
		t.Fatal(err)
	}
	if err := install(); err != nil {
		t.Fatalf("staged replay: %v", err)
	}
	if len(runtime.periodicRebases) != 1 || !reflect.DeepEqual(periodicRebaseStates(runtime), before) ||
		!reflect.DeepEqual(runtime.DirtySnapshots(-1), dirtyBefore) || notifications != 0 {
		t.Fatal("installer changed counters, checkpoints or notifications before publication")
	}
	// Reconciliation of the still-published revision must not consume a seed.
	if err := runtime.Reconcile(definitions); err != nil {
		t.Fatal(err)
	}
	if len(runtime.periodicRebases) != 1 || !reflect.DeepEqual(periodicRebaseStates(runtime), before) {
		t.Fatal("unchanged revision consumed or applied staged state")
	}
	// In-flight requests can finish while the seed is staged. Unchanged rules
	// must copy their latest state, not a snapshot taken during preparation.
	runtime.Complete(ticket, 2)
	before = periodicRebaseStates(runtime)
	dirtyBefore = runtime.DirtySnapshots(-1)
	next := periodicRebaseDefinitions(definitions)
	next[1][1].Revision++
	if err := runtime.Reconcile(next); err != nil {
		t.Fatal(err)
	}
	want := before
	want[11] = wantState
	if got := periodicRebaseStates(runtime); !reflect.DeepEqual(got, want) {
		t.Fatalf("reconciled states = %#v, want %#v", got, want)
	}
	var wantDirty []RestoredState
	for _, checkpoint := range dirtyBefore {
		if checkpoint.RuleID != 11 {
			wantDirty = append(wantDirty, checkpoint)
		}
	}
	if got := runtime.DirtySnapshots(-1); !reflect.DeepEqual(got, wantDirty) {
		t.Fatalf("dirty states = %#v, want %#v", got, wantDirty)
	}
	if len(runtime.periodicRebases) != 0 {
		t.Fatal("successful reconcile did not consume seed")
	}
	view := ruleViewByID(t, runtime.Snapshot(1, now).Rules, 11)
	if view.WindowEndsAtMS == nil || *view.WindowEndsAtMS != *wantState.WindowEndsAtMS ||
		view.UsedNanoUSD != 6 || view.Status != RuleStatusAvailable {
		t.Fatalf("rebased window = %#v", view)
	}
}

func TestPeriodicRebaseRejectsInvalidPreparation(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*Runtime, *uint, *[]Rule, *[]RestoredState)
	}{
		{"zero key", func(_ *Runtime, key *uint, _ *[]Rule, _ *[]RestoredState) { *key = 0 }},
		{"missing key", func(_ *Runtime, key *uint, _ *[]Rule, states *[]RestoredState) {
			*key = 3
			(*states)[0].AccessKeyID = 3
		}},
		{"empty rules", func(_ *Runtime, _ *uint, rules *[]Rule, states *[]RestoredState) { *rules = nil; *states = nil }},
		{"missing state", func(_ *Runtime, _ *uint, _ *[]Rule, states *[]RestoredState) { *states = nil }},
		{"unchanged revision", func(_ *Runtime, _ *uint, rules *[]Rule, states *[]RestoredState) {
			(*rules)[0].Revision--
			(*states)[0].RuleRevision--
		}},
		{"skipped revision", func(_ *Runtime, _ *uint, rules *[]Rule, states *[]RestoredState) {
			(*rules)[0].Revision++
			(*states)[0].RuleRevision++
		}},
		{"zero revision", func(_ *Runtime, _ *uint, rules *[]Rule, states *[]RestoredState) {
			(*rules)[0].Revision = 0
			(*states)[0].RuleRevision = 0
		}},
		{"period changed", func(_ *Runtime, _ *uint, rules *[]Rule, states *[]RestoredState) {
			(*rules)[0].PeriodSeconds++
			(*states)[0].WindowEndsAtMS = ptrInt64(*(*states)[0].WindowEndsAtMS + 1000)
		}},
		{"limit changed", func(_ *Runtime, _ *uint, rules *[]Rule, _ *[]RestoredState) { (*rules)[0].LimitNanoUSD++ }},
		{"nonperiodic", func(_ *Runtime, _ *uint, rules *[]Rule, states *[]RestoredState) {
			(*rules)[0] = Rule{ID: 10, Revision: 3, Kind: KindTotal, LimitNanoUSD: 1000}
			(*states)[0] = RestoredState{AccessKeyID: 1, RuleID: 10, RuleRevision: 3, SnapshotVersion: 1}
		}},
		{"unknown rule", func(_ *Runtime, _ *uint, rules *[]Rule, states *[]RestoredState) {
			(*rules)[0].ID = 99
			(*states)[0].RuleID = 99
		}},
		{"cross key rule", func(_ *Runtime, _ *uint, rules *[]Rule, states *[]RestoredState) {
			(*rules)[0] = Rule{ID: 21, Revision: 2, Kind: KindPeriodic, LimitNanoUSD: 100, PeriodSeconds: 120}
			(*states)[0] = RestoredState{AccessKeyID: 1, RuleID: 21, RuleRevision: 2, SnapshotVersion: 1}
		}},
		{"duplicate rules", func(_ *Runtime, _ *uint, rules *[]Rule, states *[]RestoredState) {
			*rules = append(*rules, (*rules)[0])
			*states = append(*states, cloneRestoredState((*states)[0]))
		}},
		{"duplicate states", func(_ *Runtime, _ *uint, rules *[]Rule, states *[]RestoredState) {
			*rules = append(*rules, Rule{ID: 12, Revision: 8, Kind: KindPeriodic, LimitNanoUSD: 100, PeriodSeconds: 600})
			*states = append(*states, cloneRestoredState((*states)[0]))
		}},
		{"state wrong key", func(_ *Runtime, _ *uint, _ *[]Rule, states *[]RestoredState) { (*states)[0].AccessKeyID = 2 }},
		{"state wrong id", func(_ *Runtime, _ *uint, _ *[]Rule, states *[]RestoredState) { (*states)[0].RuleID++ }},
		{"state wrong revision", func(_ *Runtime, _ *uint, _ *[]Rule, states *[]RestoredState) { (*states)[0].RuleRevision++ }},
		{"negative usage", func(_ *Runtime, _ *uint, _ *[]Rule, states *[]RestoredState) { (*states)[0].UsedNanoUSD = -1 }},
		{"zero checkpoint", func(_ *Runtime, _ *uint, _ *[]Rule, states *[]RestoredState) { (*states)[0].SnapshotVersion = 0 }},
		{"noninitial checkpoint", func(_ *Runtime, _ *uint, _ *[]Rule, states *[]RestoredState) { (*states)[0].SnapshotVersion = 2 }},
		{"incomplete window", func(_ *Runtime, _ *uint, _ *[]Rule, states *[]RestoredState) { (*states)[0].WindowEndsAtMS = nil }},
		{"negative start", func(_ *Runtime, _ *uint, _ *[]Rule, states *[]RestoredState) {
			(*states)[0].WindowStartedAtMS = ptrInt64(-1)
		}},
		{"inverted window", func(_ *Runtime, _ *uint, _ *[]Rule, states *[]RestoredState) {
			(*states)[0].WindowEndsAtMS = ptrInt64(*(*states)[0].WindowStartedAtMS)
		}},
		{"window period mismatch", func(_ *Runtime, _ *uint, _ *[]Rule, states *[]RestoredState) {
			(*states)[0].WindowEndsAtMS = ptrInt64(*(*states)[0].WindowEndsAtMS + 1)
		}},
		{"active without generation", func(_ *Runtime, _ *uint, _ *[]Rule, states *[]RestoredState) { (*states)[0].WindowGeneration = 0 }},
		{"inactive with usage", func(_ *Runtime, _ *uint, _ *[]Rule, states *[]RestoredState) {
			(*states)[0].WindowStartedAtMS, (*states)[0].WindowEndsAtMS = nil, nil
			(*states)[0].WindowGeneration = 0
		}},
		{"inactive with generation", func(_ *Runtime, _ *uint, _ *[]Rule, states *[]RestoredState) {
			(*states)[0].WindowStartedAtMS, (*states)[0].WindowEndsAtMS = nil, nil
			(*states)[0].UsedNanoUSD = 0
		}},
		{"revision overflow", func(runtime *Runtime, _ *uint, rules *[]Rule, states *[]RestoredState) {
			runtime.entries[1].byID[11].definition.Revision = math.MaxUint64
			(*rules)[0].Revision = 1
			(*states)[0].RuleRevision = 1
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			runtime, definitions, now := periodicRebaseFixture(t)
			key := uint(1)
			rules := []Rule{definitions[1][1]}
			rules[0].Revision++
			states := []RestoredState{periodicRebaseCheckpoint(key, rules[0], now, 3)}
			test.edit(runtime, &key, &rules, &states)
			before, dirtyBefore := periodicRebaseStates(runtime), runtime.DirtySnapshots(-1)
			if install, err := runtime.PreparePeriodicRebase(key, rules, states); err == nil || install != nil {
				t.Fatalf("invalid preparation = (%v, %v)", install != nil, err)
			}
			if len(runtime.periodicRebases) != 0 || !reflect.DeepEqual(periodicRebaseStates(runtime), before) ||
				!reflect.DeepEqual(runtime.DirtySnapshots(-1), dirtyBefore) {
				t.Fatal("rejected preparation mutated runtime")
			}
		})
	}
	var missing *Runtime
	if _, err := missing.PreparePeriodicRebase(1, nil, nil); err == nil {
		t.Fatal("nil runtime accepted")
	}
}

func TestPeriodicRebaseOldTicketsAndCheckpointFencing(t *testing.T) {
	runtime, definitions, now := periodicRebaseFixture(t)
	oldTicket, _ := runtime.Admit(1, now)
	runtime.Complete(oldTicket, 8)
	staleCheckpoints := runtime.DirtySnapshots(-1)
	next := periodicRebaseDefinitions(definitions)
	next[1][1].Revision++
	state := periodicRebaseCheckpoint(1, next[1][1], now, 5)
	install, err := runtime.PreparePeriodicRebase(1, []Rule{next[1][1]}, []RestoredState{state})
	if err != nil {
		t.Fatal(err)
	}
	if err := install(); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Reconcile(next); err != nil {
		t.Fatal(err)
	}
	runtime.Complete(oldTicket, 7)
	view := runtime.Snapshot(1, now)
	if got := ruleViewByID(t, view.Rules, 10).UsedNanoUSD; got != 15 {
		t.Fatalf("old ticket lost total cost: got %d, want 15", got)
	}
	if got := ruleViewByID(t, view.Rules, 12).UsedNanoUSD; got != 15 {
		t.Fatalf("old ticket lost unchanged periodic cost: got %d, want 15", got)
	}
	if got := ruleViewByID(t, view.Rules, 11).UsedNanoUSD; got != 5 {
		t.Fatalf("old ticket charged rebased periodic: got %d, want 5", got)
	}
	newTicket, decision := runtime.Admit(1, now)
	if !decision.Allowed {
		t.Fatal(decision)
	}
	runtime.Complete(newTicket, 3)
	for _, checkpoint := range staleCheckpoints {
		runtime.Ack(checkpoint.AccessKeyID, checkpoint.RuleID, checkpoint.RuleRevision, checkpoint.SnapshotVersion)
	}
	checkpoint := periodicRebaseStates(runtime)[11]
	if checkpoint.UsedNanoUSD != 8 || checkpoint.RuleRevision != state.RuleRevision || checkpoint.SnapshotVersion != 2 ||
		checkpoint.WindowGeneration != state.WindowGeneration || *checkpoint.WindowEndsAtMS != *state.WindowEndsAtMS {
		t.Fatalf("new revision checkpoint = %#v", checkpoint)
	}
	foundDirty := false
	for _, dirty := range runtime.DirtySnapshots(-1) {
		if dirty.RuleID == 11 {
			foundDirty = reflect.DeepEqual(dirty, checkpoint)
		}
	}
	if !foundDirty {
		t.Fatal("old revision acknowledgment cleared the new dirty checkpoint")
	}
}

func TestPeriodicRebaseReplayAfterUsageAndLaterRevision(t *testing.T) {
	runtime, definitions, now := periodicRebaseFixture(t)
	ticket, _ := runtime.Admit(1, now)
	runtime.Complete(ticket, 4)
	next := periodicRebaseDefinitions(definitions)
	next[1][1].Revision++
	state := periodicRebaseCheckpoint(1, next[1][1], now, 2)
	install, err := runtime.PreparePeriodicRebase(1, []Rule{next[1][1]}, []RestoredState{state})
	if err != nil {
		t.Fatal(err)
	}
	if err := install(); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Reconcile(next); err != nil {
		t.Fatal(err)
	}
	newTicket, _ := runtime.Admit(1, now)
	runtime.Complete(newTicket, 7)
	before, dirtyBefore := periodicRebaseStates(runtime), runtime.DirtySnapshots(-1)
	for range 2 {
		if err := install(); err != nil {
			t.Fatalf("applied replay: %v", err)
		}
		if len(runtime.periodicRebases) != 0 {
			t.Fatal("applied replay restaged stale seed")
		}
		if err := runtime.Reconcile(next); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(periodicRebaseStates(runtime), before) || !reflect.DeepEqual(runtime.DirtySnapshots(-1), dirtyBefore) {
			t.Fatal("applied replay reset newly accrued usage or dirty version")
		}
	}
	// A subsequent legitimate rebase must not make the first installer usable
	// again, even if that installer is replayed after further completions.
	later := periodicRebaseDefinitions(next)
	later[1][1].Revision++
	laterState := periodicRebaseCheckpoint(1, later[1][1], now.Add(time.Minute), 1)
	installLater, err := runtime.PreparePeriodicRebase(1, []Rule{later[1][1]}, []RestoredState{laterState})
	if err != nil {
		t.Fatal(err)
	}
	if err := installLater(); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Reconcile(later); err != nil {
		t.Fatal(err)
	}
	laterTicket, _ := runtime.Admit(1, now.Add(time.Minute))
	runtime.Complete(laterTicket, 3)
	before, dirtyBefore = periodicRebaseStates(runtime), runtime.DirtySnapshots(-1)
	if err := install(); err != nil {
		t.Fatalf("superseded replay: %v", err)
	}
	if len(runtime.periodicRebases) != 0 || !reflect.DeepEqual(periodicRebaseStates(runtime), before) ||
		!reflect.DeepEqual(runtime.DirtySnapshots(-1), dirtyBefore) {
		t.Fatal("superseded replay restaged stale seed or reset usage")
	}
}

func TestPeriodicRebaseFailedReconcileRetainsSeed(t *testing.T) {
	runtime, definitions, now := periodicRebaseFixture(t)
	ticket, _ := runtime.Admit(1, now)
	runtime.Complete(ticket, 9)
	next := periodicRebaseDefinitions(definitions)
	next[1][1].Revision++
	state := periodicRebaseCheckpoint(1, next[1][1], now, 4)
	install, err := runtime.PreparePeriodicRebase(1, []Rule{next[1][1]}, []RestoredState{state})
	if err != nil {
		t.Fatal(err)
	}
	if err := install(); err != nil {
		t.Fatal(err)
	}
	before, dirtyBefore := periodicRebaseStates(runtime), runtime.DirtySnapshots(-1)
	for _, test := range []struct {
		name string
		edit func(map[uint][]Rule)
	}{
		{"invalid definitions", func(defs map[uint][]Rule) { defs[1][2].LimitNanoUSD = 0 }},
		// Sorted rule 11's seed is applied to a detached entry before rule 12
		// fails. Consuming seeds inside that loop would lose the committed seed.
		{"failure after seed application", func(defs map[uint][]Rule) { defs[1][2].PeriodSeconds++ }},
		{"seed period mismatch", func(defs map[uint][]Rule) { defs[1][1].PeriodSeconds++ }},
		{"seed limit mismatch", func(defs map[uint][]Rule) { defs[1][1].LimitNanoUSD++ }},
	} {
		t.Run(test.name, func(t *testing.T) {
			invalid := periodicRebaseDefinitions(next)
			test.edit(invalid)
			if err := runtime.Reconcile(invalid); err == nil {
				t.Fatal("invalid reconcile accepted")
			}
			if len(runtime.periodicRebases) != 1 || !reflect.DeepEqual(periodicRebaseStates(runtime), before) ||
				!reflect.DeepEqual(runtime.DirtySnapshots(-1), dirtyBefore) {
				t.Fatal("failed reconciliation consumed seed or mutated live state")
			}
		})
	}
	// Completions on unchanged rules after a failure must survive the retry.
	runtime.Complete(ticket, 2)
	if err := install(); err != nil {
		t.Fatalf("retry staging: %v", err)
	}
	if err := runtime.Reconcile(next); err != nil {
		t.Fatal(err)
	}
	got := periodicRebaseStates(runtime)
	if !reflect.DeepEqual(got[11], state) || got[10].UsedNanoUSD != 11 || got[12].UsedNanoUSD != 11 || len(runtime.periodicRebases) != 0 {
		t.Fatalf("successful retry states = %#v", got)
	}
}

func TestPeriodicRebaseInstallerValidatesWholeBatch(t *testing.T) {
	runtime, definitions, now := periodicRebaseFixture(t)
	rules := append([]Rule(nil), definitions[1][1:]...)
	var states []RestoredState
	for i := range rules {
		rules[i].Revision++
		states = append(states, periodicRebaseCheckpoint(1, rules[i], now, 2))
	}
	install, err := runtime.PreparePeriodicRebase(1, rules, states)
	if err != nil {
		t.Fatal(err)
	}
	// A concurrent configuration change between prepare and install invalidates
	// the entire batch; rule 11 must not be staged before rejecting rule 12.
	changed := periodicRebaseDefinitions(definitions)
	changed[1][2].LimitNanoUSD++
	if err := runtime.Reconcile(changed); err != nil {
		t.Fatal(err)
	}
	before := periodicRebaseStates(runtime)
	if err := install(); err == nil {
		t.Fatal("stale preparation accepted changed current limit")
	}
	if len(runtime.periodicRebases) != 0 || !reflect.DeepEqual(periodicRebaseStates(runtime), before) {
		t.Fatal("invalid installer partially staged or changed state")
	}
}

func TestPeriodicRebaseRejectsConflictingStage(t *testing.T) {
	runtime, definitions, now := periodicRebaseFixture(t)
	rule := definitions[1][1]
	rule.Revision++
	state := periodicRebaseCheckpoint(1, rule, now, 2)
	install, err := runtime.PreparePeriodicRebase(1, []Rule{rule}, []RestoredState{state})
	if err != nil {
		t.Fatal(err)
	}
	state.UsedNanoUSD++
	conflict, err := runtime.PreparePeriodicRebase(1, []Rule{rule}, []RestoredState{state})
	if err != nil {
		t.Fatal(err)
	}
	if err := install(); err != nil {
		t.Fatal(err)
	}
	if err := conflict(); err == nil {
		t.Fatal("conflicting checkpoint replaced committed staged seed")
	}
	next := periodicRebaseDefinitions(definitions)
	next[1][1] = rule
	if err := runtime.Reconcile(next); err != nil {
		t.Fatal(err)
	}
	if got := periodicRebaseStates(runtime)[11].UsedNanoUSD; got != 2 {
		t.Fatalf("conflict changed staged usage: got %d, want 2", got)
	}
}

func TestPeriodicRebaseCommittedStateRestoresAfterCrash(t *testing.T) {
	runtime, definitions, now := periodicRebaseFixture(t)
	ticket, _ := runtime.Admit(1, now)
	runtime.Complete(ticket, 9)
	next := periodicRebaseDefinitions(definitions)
	next[1][1].Revision++
	state := periodicRebaseCheckpoint(1, next[1][1], now, 4)
	if _, err := runtime.PreparePeriodicRebase(1, []Rule{next[1][1]}, []RestoredState{state}); err != nil {
		t.Fatal(err)
	}
	// Simulate the durable transaction having committed, with the process lost
	// before either installation or publication. Startup needs no staged seeds.
	committed := periodicRebaseStates(runtime)
	committed[11] = state
	var checkpoints []RestoredState
	for _, checkpoint := range committed {
		checkpoints = append(checkpoints, checkpoint)
	}
	restarted := NewRuntime()
	if err := restarted.Restore(checkpoints); err != nil {
		t.Fatal(err)
	}
	if err := restarted.Reconcile(next); err != nil {
		t.Fatal(err)
	}
	if got := periodicRebaseStates(restarted); !reflect.DeepEqual(got, committed) {
		t.Fatalf("restored committed state = %#v, want %#v", got, committed)
	}
	if len(restarted.DirtySnapshots(-1)) != 0 {
		t.Fatal("restored durable rebase checkpoint marked dirty")
	}
}

func periodicRebaseFixture(t *testing.T) (*Runtime, map[uint][]Rule, time.Time) {
	t.Helper()
	definitions := map[uint][]Rule{
		1: {
			{ID: 10, Revision: 2, Kind: KindTotal, LimitNanoUSD: 1000},
			{ID: 11, Revision: 4, Kind: KindPeriodic, LimitNanoUSD: 100, PeriodSeconds: 300},
			{ID: 12, Revision: 7, Kind: KindPeriodic, LimitNanoUSD: 100, PeriodSeconds: 600},
		},
		2: {
			{ID: 20, Revision: 3, Kind: KindTotal, LimitNanoUSD: 1000},
			{ID: 21, Revision: 1, Kind: KindPeriodic, LimitNanoUSD: 100, PeriodSeconds: 120},
		},
	}
	runtime := NewRuntime()
	if err := runtime.Reconcile(definitions); err != nil {
		t.Fatal(err)
	}
	return runtime, definitions, time.Unix(1_790_000_000, 0)
}

func periodicRebaseDefinitions(source map[uint][]Rule) map[uint][]Rule {
	cloned := make(map[uint][]Rule, len(source))
	for key, rules := range source {
		cloned[key] = append([]Rule(nil), rules...)
	}
	return cloned
}

func periodicRebaseCheckpoint(accessKeyID uint, rule Rule, now time.Time, used int64) RestoredState {
	start := now.Add(-time.Minute)
	return RestoredState{
		AccessKeyID: accessKeyID, RuleID: rule.ID, RuleRevision: rule.Revision, UsedNanoUSD: used,
		WindowStartedAtMS: ptrInt64(start.UnixMilli()),
		WindowEndsAtMS:    ptrInt64(start.Add(time.Duration(rule.PeriodSeconds) * time.Second).UnixMilli()),
		WindowGeneration:  9, SnapshotVersion: 1,
	}
}

func periodicRebaseStates(runtime *Runtime) map[uint]RestoredState {
	runtime.mu.RLock()
	defer runtime.mu.RUnlock()
	states := make(map[uint]RestoredState)
	for accessKeyID, entry := range runtime.entries {
		entry.mu.Lock()
		for _, rule := range entry.rules {
			states[rule.definition.ID] = restoredState(accessKeyID, rule)
		}
		entry.mu.Unlock()
	}
	return states
}
