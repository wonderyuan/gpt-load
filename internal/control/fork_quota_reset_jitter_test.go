package control

import (
	"math"
	"testing"
	"time"

	"gpt-load/internal/storage/models"
)

func quotaResetJitterFixture() (*quotaResetCursor, models.Credential, models.Group, time.Time, int64) {
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	group := models.Group{ID: 1, ChannelID: "codex", ConnectionType: models.ConnectionTypeSubscription, Params: models.JSON(`{}`)}
	credential := models.Credential{ID: 2, GroupID: 1, IdentityFingerprint: "jitter-account"}
	end := now.Add(time.Hour).UnixMilli()
	cursor := &quotaResetCursor{}
	cursor.observe(credential, group, quotaResetTestObservation(now, 1, 80, end), true)
	return cursor, credential, group, now, end
}

func TestQuotaResetSameAnchor(t *testing.T) {
	for _, tc := range []struct {
		name string
		a, b int64
		want bool
	}{
		{"equal", 100000, 100000, true},
		{"eleven_seconds", 100000, 111000, true},
		{"boundary", 100000, 160000, true},
		{"over_boundary", 100000, 160001, false},
		{"overflow", math.MinInt64, math.MaxInt64, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := quotaResetSameAnchor(tc.a, tc.b); got != tc.want {
				t.Fatalf("sameAnchor(%d, %d) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
			if got := quotaResetSameAnchor(tc.b, tc.a); got != tc.want {
				t.Fatalf("reverse sameAnchor = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestQuotaResetDetectJitterConfirmation(t *testing.T) {
	for _, kind := range []string{"natural", "sudden", "same_anchor"} {
		for _, jitter := range []time.Duration{-time.Minute, -11 * time.Second, 11 * time.Second, time.Minute} {
			t.Run(kind+"/"+jitter.String(), func(t *testing.T) {
				cursor, credential, group, now, oldEnd := quotaResetJitterFixture()
				at, end, wantKind := now.Add(time.Minute), now.Add(5*time.Hour).UnixMilli(), "sudden"
				if kind == "natural" {
					at, end, wantKind = time.UnixMilli(oldEnd), oldEnd+18000*1000, "natural"
				} else if kind == "same_anchor" {
					end = oldEnd
				}
				cursor.observe(credential, group, quotaResetTestObservation(at, 2, 0, end), true)
				if len(cursor.Candidates) != 1 {
					t.Fatalf("missing candidate: %#v", cursor.Candidates)
				}
				jitterEnd := end + jitter.Milliseconds()
				if events := cursor.observe(credential, group, quotaResetTestObservation(at.Add(29*time.Second), 3, 0, jitterEnd), true); len(events) != 0 {
					t.Fatal("confirmed before 30 seconds")
				}
				for _, candidate := range cursor.Candidates {
					if candidate.New.EndMS != end || candidate.FirstMS != at.UnixMilli() {
						t.Fatalf("candidate followed jitter: %#v", candidate)
					}
				}
				events := cursor.observe(credential, group, quotaResetTestObservation(at.Add(30*time.Second), 4, 0, jitterEnd), true)
				if len(events) != 1 || events[0].Kind != wantKind || events[0].OldEndMS != oldEnd || events[0].EndMS != jitterEnd {
					t.Fatalf("jitter confirmation = %#v", events)
				}
				for i := uint64(5); i <= 7; i++ {
					if again := cursor.observe(credential, group, quotaResetTestObservation(at.Add(time.Duration(i)*time.Minute), i, 0, end), true); len(again) != 0 {
						t.Fatalf("same 0%% repeated event: %#v", again)
					}
				}
			})
		}
	}
}

func TestQuotaResetDetectJitterInvalidCandidatePreservesOldEvidence(t *testing.T) {
	for _, tc := range []struct {
		name   string
		age    time.Duration
		jitter time.Duration
	}{
		{"over_tolerance", 30 * time.Second, time.Minute + time.Millisecond},
		{"expired", 10*time.Minute + time.Millisecond, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cursor, credential, group, now, oldEnd := quotaResetJitterFixture()
			at, end := now.Add(time.Minute), now.Add(4*time.Hour).UnixMilli()
			cursor.observe(credential, group, quotaResetTestObservation(at, 2, 0, end), true)
			restartAt, restartEnd := at.Add(tc.age), end+tc.jitter.Milliseconds()
			if events := cursor.observe(credential, group, quotaResetTestObservation(restartAt, 3, 0, restartEnd), true); len(events) != 0 {
				t.Fatalf("invalid candidate confirmed: %#v", events)
			}
			if len(cursor.Candidates) != 1 {
				t.Fatalf("drop evidence lost: %#v", cursor.Candidates)
			}
			for key, candidate := range cursor.Candidates {
				if candidate.Old.EndMS != oldEnd || candidate.Old.UsedPercent != 80 || candidate.New.EndMS != restartEnd || candidate.FirstMS != restartAt.UnixMilli() || cursor.Windows[key].UsedPercent != 80 {
					t.Fatalf("replacement lost baseline or timing: %#v, %#v", candidate, cursor.Windows[key])
				}
			}
			events := cursor.observe(credential, group, quotaResetTestObservation(restartAt.Add(30*time.Second), 4, 0, restartEnd), true)
			if len(events) != 1 || events[0].OldEndMS != oldEnd {
				t.Fatalf("replacement not confirmed: %#v", events)
			}
		})
	}
}

func TestQuotaResetDetectJitterDoesNotAccumulate(t *testing.T) {
	cursor, credential, group, now, oldEnd := quotaResetJitterFixture()
	at, end := now.Add(time.Minute), now.Add(4*time.Hour).UnixMilli()
	cursor.observe(credential, group, quotaResetTestObservation(at, 2, 0, end), true)
	for i, jitter := range []time.Duration{40 * time.Second, 60 * time.Second, 80 * time.Second} {
		if events := cursor.observe(credential, group, quotaResetTestObservation(at.Add(time.Duration(i+1)*10*time.Second), uint64(i+3), 0, end+jitter.Milliseconds()), true); len(events) != 0 {
			t.Fatalf("drifting candidate confirmed: %#v", events)
		}
		for _, candidate := range cursor.Candidates {
			if i < 2 && (candidate.New.EndMS != end || candidate.FirstMS != at.UnixMilli()) {
				t.Fatalf("candidate followed drift: %#v", candidate)
			}
			if i == 2 && (candidate.New.EndMS != end+jitter.Milliseconds() || candidate.FirstMS != at.Add(30*time.Second).UnixMilli() || candidate.Old.EndMS != oldEnd) {
				t.Fatalf("candidate did not restart from old evidence: %#v", candidate)
			}
		}
	}
	if len(cursor.Candidates) != 1 {
		t.Fatal("invalidated drift lost drop evidence")
	}
	events := cursor.observe(credential, group, quotaResetTestObservation(at.Add(time.Minute), 6, 0, end+80000), true)
	if len(events) != 1 {
		t.Fatalf("stable replacement did not confirm: %#v", events)
	}
}

func TestQuotaResetDetectJitterGuards(t *testing.T) {
	for _, guard := range []string{"source", "period", "unknown", "identity", "target", "duplicate", "classification", "expired_anchor"} {
		t.Run(guard, func(t *testing.T) {
			cursor, credential, group, now, _ := quotaResetJitterFixture()
			at, end := now.Add(time.Minute), now.Add(4*time.Hour).UnixMilli()
			cursor.observe(credential, group, quotaResetTestObservation(at, 2, 0, end), true)
			for i := uint64(3); i <= 4; i++ {
				response := quotaResetTestObservation(at.Add(time.Duration(i-2)*time.Minute), i, 0, end+11000)
				window := &response.Snapshot.QuotaWindows[0]
				switch guard {
				case "source":
					window.SourceID = "other"
				case "period":
					period := int64(604800)
					window.WindowSeconds = &period
				case "unknown":
					window.State = "unknown"
				case "identity":
					credential.IdentityFingerprint = "other-account"
				case "target":
					group.Params = models.JSON(`{"base_url":"https://other.example.com"}`)
				case "duplicate":
					response.Snapshot.QuotaWindows = append(response.Snapshot.QuotaWindows, *window)
				case "classification":
					used := float64(80)
					window.Used = &used
				case "expired_anchor":
					expired := *response.ObservedAtMS - 1
					window.ResetAtMS = &expired
				}
				if events := cursor.observe(credential, group, response, true); len(events) != 0 {
					t.Fatalf("guard emitted event: %#v", events)
				}
			}
		})
	}
}

func TestQuotaResetDetectJitterUnknownBreaksConfirmation(t *testing.T) {
	cursor, credential, group, now, _ := quotaResetJitterFixture()
	at, end := now.Add(time.Minute), now.Add(4*time.Hour).UnixMilli()
	cursor.observe(credential, group, quotaResetTestObservation(at, 2, 0, end), true)
	unknown := quotaResetTestObservation(at.Add(time.Minute), 3, 0, end+11000)
	unknown.Snapshot.QuotaWindows[0].State = "unknown"
	if events := cursor.observe(credential, group, unknown, true); len(events) != 0 || len(cursor.Candidates) != 0 {
		t.Fatalf("unknown confirmed or retained candidate: %#v, %#v", events, cursor.Candidates)
	}
	for i := uint64(4); i <= 6; i++ {
		if events := cursor.observe(credential, group, quotaResetTestObservation(at.Add(time.Duration(i)*time.Minute), i, 0, end), true); len(events) != 0 {
			t.Fatalf("unknown followed by unchanged 0%% replayed drop: %#v", events)
		}
	}
}

func TestQuotaResetDetectJitterNonUniquePeriodCannotSync(t *testing.T) {
	cursor, credential, group, now, _ := quotaResetJitterFixture()
	at, end := now.Add(time.Minute), now.Add(4*time.Hour).UnixMilli()
	cursor.observe(credential, group, quotaResetTestObservation(at, 2, 0, end), true)
	response := quotaResetTestObservation(at.Add(time.Minute), 3, 0, end+11000)
	other := response.Snapshot.QuotaWindows[0]
	other.SourceID = "other"
	response.Snapshot.QuotaWindows = append(response.Snapshot.QuotaWindows, other)
	events := cursor.observe(credential, group, response, true)
	if len(events) != 1 || events[0].UniquePeriod {
		t.Fatalf("ambiguous period became syncable: %#v", events)
	}
}

func TestQuotaResetDetectJitterTenMinuteBoundary(t *testing.T) {
	cursor, credential, group, now, _ := quotaResetJitterFixture()
	at, end := now.Add(time.Minute), now.Add(4*time.Hour).UnixMilli()
	cursor.observe(credential, group, quotaResetTestObservation(at, 2, 0, end), true)
	events := cursor.observe(credential, group, quotaResetTestObservation(at.Add(10*time.Minute), 3, 0, end+11000), true)
	if len(events) != 1 {
		t.Fatalf("candidate expired at inclusive boundary: %#v", events)
	}
}

func TestQuotaResetDetectJitterSameAnchorRestorationEpoch(t *testing.T) {
	cursor, credential, group, now, end := quotaResetJitterFixture()
	at := now.Add(time.Minute)
	cursor.observe(credential, group, quotaResetTestObservation(at, 2, 0, end), true)
	first := cursor.observe(credential, group, quotaResetTestObservation(at.Add(time.Minute), 3, 0, end+11000), true)
	if len(first) != 1 {
		t.Fatalf("first restoration = %#v", first)
	}
	cursor.observe(credential, group, quotaResetTestObservation(at.Add(2*time.Minute), 4, 80, end), true)
	cursor.observe(credential, group, quotaResetTestObservation(at.Add(3*time.Minute), 5, 0, end), true)
	second := cursor.observe(credential, group, quotaResetTestObservation(at.Add(4*time.Minute), 6, 0, end+11000), true)
	if len(second) != 1 || second[0].ID == first[0].ID || second[0].Digest == first[0].Digest {
		t.Fatalf("new same-anchor restoration collided: first=%#v second=%#v", first, second)
	}
	if again := cursor.observe(credential, group, quotaResetTestObservation(at.Add(5*time.Minute), 7, 0, end), true); len(again) != 0 {
		t.Fatalf("same 0%% emitted another restoration: %#v", again)
	}
}
