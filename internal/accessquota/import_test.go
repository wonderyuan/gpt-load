package accessquota

import (
	"testing"
	"time"
)

func TestFreshImportBlocksRecoversAndCheckpoints(t *testing.T) {
	start := time.Unix(1_790_000_000, 0)
	end := start.Add(5 * time.Hour)
	rules := []Rule{{ID: 10, Revision: 1, Kind: KindPeriodic, LimitNanoUSD: 10, PeriodSeconds: 18000}}
	states := []RestoredState{{AccessKeyID: 9, RuleID: 10, RuleRevision: 1, UsedNanoUSD: 15,
		WindowStartedAtMS: ptrInt64(start.UnixMilli()), WindowEndsAtMS: ptrInt64(end.UnixMilli()), WindowGeneration: 1, SnapshotVersion: 1}}
	runtime := NewRuntime()
	install, err := runtime.PrepareFreshImport(9, rules, states)
	if err != nil {
		t.Fatal(err)
	}
	if len(runtime.Snapshot(9, start).Rules) != 0 {
		t.Fatal("prepare published before commit")
	}
	// The prepared state must not alias caller-owned pointers.
	*states[0].WindowEndsAtMS = 0
	if err := install(); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Reconcile(map[uint][]Rule{9: rules}); err != nil {
		t.Fatal(err)
	}
	view := runtime.Snapshot(9, start)
	if view.Allowed || view.Rules[0].UsedNanoUSD != 15 || view.Rules[0].RemainingNanoUSD != 0 || *view.Rules[0].WindowEndsAtMS != end.UnixMilli() {
		t.Fatalf("imported view = %#v", view)
	}
	if _, decision := runtime.Admit(9, start); decision.Allowed || !decision.Recoverable || *decision.NextAvailableAtMS != end.UnixMilli() {
		t.Fatalf("blocked admission = %#v", decision)
	}
	if len(runtime.DirtySnapshots(-1)) != 0 {
		t.Fatal("durable imported checkpoint marked dirty")
	}
	if !runtime.Check(9, end).Allowed || runtime.Snapshot(9, end).Rules[0].Status != RuleStatusInactive {
		t.Fatal("expired period did not become available")
	}
	ticket, decision := runtime.Admit(9, end.Add(time.Minute))
	if !decision.Allowed {
		t.Fatal(decision)
	}
	runtime.Complete(ticket, 3)
	checkpoint := runtime.DirtySnapshots(-1)
	if len(checkpoint) != 1 || checkpoint[0].UsedNanoUSD != 3 || checkpoint[0].WindowGeneration != 2 || checkpoint[0].SnapshotVersion != 3 {
		t.Fatalf("renewed checkpoint = %#v", checkpoint)
	}
	restarted := NewRuntime()
	if err := restarted.Restore(checkpoint); err != nil {
		t.Fatal(err)
	}
	if err := restarted.Reconcile(map[uint][]Rule{9: rules}); err != nil {
		t.Fatal(err)
	}
	if got := restarted.Snapshot(9, end.Add(time.Minute)).Rules[0].UsedNanoUSD; got != 3 {
		t.Fatalf("restart usage = %d", got)
	}
	if err := install(); err == nil {
		t.Fatal("installer must not reset an existing entry")
	}
}

func TestFreshImportValidatesStateBeforeMutation(t *testing.T) {
	base := RestoredState{AccessKeyID: 9, RuleID: 10, RuleRevision: 1, UsedNanoUSD: 11,
		WindowStartedAtMS: ptrInt64(1000), WindowEndsAtMS: ptrInt64(301000), WindowGeneration: 1, SnapshotVersion: 1}
	periodic := Rule{ID: 10, Revision: 1, Kind: KindPeriodic, LimitNanoUSD: 10, PeriodSeconds: 300}
	for _, test := range []struct {
		name string
		edit func(*RestoredState)
	}{
		{"wrong key", func(s *RestoredState) { s.AccessKeyID = 8 }},
		{"wrong rule", func(s *RestoredState) { s.RuleID = 11 }},
		{"wrong revision", func(s *RestoredState) { s.RuleRevision++ }},
		{"negative start", func(s *RestoredState) { s.WindowStartedAtMS = ptrInt64(-1) }},
		{"incomplete window", func(s *RestoredState) { s.WindowEndsAtMS = nil }},
		{"wrong duration", func(s *RestoredState) { s.WindowEndsAtMS = ptrInt64(302000) }},
		{"negative used", func(s *RestoredState) { s.UsedNanoUSD = -1 }},
		{"inactive with usage", func(s *RestoredState) { s.WindowStartedAtMS = nil; s.WindowEndsAtMS = nil; s.WindowGeneration = 0 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			runtime := NewRuntime()
			state := base
			test.edit(&state)
			if _, err := runtime.PrepareFreshImport(9, []Rule{periodic}, []RestoredState{state}); err == nil {
				t.Fatal("invalid import accepted")
			}
			if len(runtime.Snapshot(9, time.UnixMilli(2000)).Rules) != 0 {
				t.Fatal("invalid import mutated runtime")
			}
		})
	}
	runtime := NewRuntime()
	if err := runtime.Reconcile(map[uint][]Rule{8: {periodic}}); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.PrepareFreshImport(9, []Rule{periodic}, []RestoredState{base}); err == nil {
		t.Fatal("cross-key rule identity accepted")
	}
	if _, err := runtime.PrepareFreshImport(8, []Rule{periodic}, []RestoredState{base}); err == nil {
		t.Fatal("existing target accepted")
	}
}
