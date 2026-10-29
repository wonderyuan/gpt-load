package control

import (
	"math"
	"testing"
	"time"

	"gpt-load/internal/storage/models"
)

func quotaResetTestObservation(at time.Time, version uint64, used float64, end int64) CredentialObservationResponse {
	observed, period, limit := at.UnixMilli(), int64(18000), float64(100)
	return CredentialObservationResponse{State: "fresh", ObservationVersion: version, ObservedAtMS: &observed, LastAttemptAtMS: &observed,
		Snapshot: &CredentialObservationSnapshot{QuotaWindows: []ObservationQuotaWindow{{SourceID: "codex", ID: "primary", Scope: "account", Unit: "percent", State: "available",
			Used: &used, Limit: &limit, ResetAtMS: &end, WindowSeconds: &period, IsPrimary: true}}}}
}

func TestQuotaResetDetectBaselineAndTwoConfirmations(t *testing.T) {
	for _, kind := range []string{"natural", "sudden", "same_anchor"} {
		t.Run(kind, func(t *testing.T) {
			now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
			group := models.Group{ID: 1, ChannelID: "codex", ConnectionType: models.ConnectionTypeSubscription, Params: models.JSON(`{}`)}
			credential := models.Credential{ID: 2, GroupID: 1, IdentityFingerprint: "private-account"}
			end := now.Add(time.Hour).UnixMilli()
			cursor := &quotaResetCursor{}
			if events := cursor.observe(credential, group, quotaResetTestObservation(now, 1, 80, end), true); len(events) != 0 {
				t.Fatal("baseline emitted event")
			}
			at, nextEnd := now.Add(time.Minute), now.Add(5*time.Hour).UnixMilli()
			want := "sudden"
			if kind == "natural" {
				at, nextEnd, want = time.UnixMilli(end), end+18000*1000, "natural"
			}
			if kind == "same_anchor" {
				nextEnd = end
			}
			first := quotaResetTestObservation(at, 2, 1, nextEnd)
			// Source+period remains stable when providers reorder display slots.
			first.Snapshot.QuotaWindows[0].ID = "secondary"
			if events := cursor.observe(credential, group, first, true); len(events) != 0 || len(cursor.Candidates) != 1 {
				t.Fatalf("first = %#v, %#v", events, cursor.Candidates)
			}
			if events := cursor.observe(credential, group, quotaResetTestObservation(at.Add(20*time.Second), 3, 1, nextEnd), true); len(events) != 0 {
				t.Fatal("confirmed too soon")
			}
			events := cursor.observe(credential, group, quotaResetTestObservation(at.Add(time.Minute), 4, 1, nextEnd), true)
			if len(events) != 1 || events[0].Kind != want || len(events[0].ID) != 36 || len(events[0].Digest) != 64 {
				t.Fatalf("confirmed = %#v", events)
			}
			if again := cursor.observe(credential, group, quotaResetTestObservation(at.Add(2*time.Minute), 5, 1, nextEnd), true); len(again) != 0 {
				t.Fatal("duplicate event")
			}
		})
	}
}

func TestQuotaResetDetectRejectsUnsafeEvidence(t *testing.T) {
	cases := []string{"partial", "failed", "old_attempt", "missing_reset", "missing_usage", "remaining_only", "small_drop", "not_near_zero", "tiny_anchor", "rolling_anchor", "backwards", "nan", "model", "addon", "duplicate", "period_changed", "identity_changed", "target_changed"}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
			group := models.Group{ID: 1, ChannelID: "codex", ConnectionType: models.ConnectionTypeSubscription, Params: models.JSON(`{}`)}
			credential := models.Credential{ID: 2, GroupID: 1, IdentityFingerprint: "account"}
			end := now.Add(time.Hour).UnixMilli()
			cursor := &quotaResetCursor{}
			oldUsed := float64(80)
			if name == "small_drop" {
				oldUsed = 4
			}
			cursor.observe(credential, group, quotaResetTestObservation(now, 1, oldUsed, end), true)
			for i := uint64(2); i <= 3; i++ {
				response := quotaResetTestObservation(now.Add(time.Duration(i)*time.Minute), i, 0, now.Add(5*time.Hour).UnixMilli())
				window := &response.Snapshot.QuotaWindows[0]
				switch name {
				case "partial":
					response.LastErrorCode = "observation_partial"
				case "failed":
					response.LastErrorCode = "observation_upstream_failed"
				case "old_attempt":
					next := *response.ObservedAtMS + 1
					response.LastAttemptAtMS = &next
				case "missing_reset":
					window.ResetAtMS = nil
				case "missing_usage":
					window.Used = nil
				case "remaining_only":
					window.Used = nil
					remaining := float64(100)
					window.Remaining = &remaining
				case "not_near_zero":
					used := float64(10)
					window.Used = &used
				case "tiny_anchor":
					next := end + 5000
					window.ResetAtMS = &next
				case "rolling_anchor":
					next := end + 60000*int64(i)
					window.ResetAtMS = &next
				case "backwards":
					next := end - 60000
					window.ResetAtMS = &next
				case "nan":
					used := math.NaN()
					window.Used = &used
				case "model":
					window.ModelIDs = []string{"private-model"}
				case "addon":
					window.Scope = "addon"
				case "duplicate":
					response.Snapshot.QuotaWindows = append(response.Snapshot.QuotaWindows, *window)
				case "period_changed":
					period := int64(604800)
					window.WindowSeconds = &period
				case "identity_changed":
					credential.IdentityFingerprint = "new-account"
				case "target_changed":
					group.Params = models.JSON(`{"base_url":"https://new.example.com"}`)
				}
				if events := cursor.observe(credential, group, response, true); len(events) != 0 {
					t.Fatalf("unsafe evidence emitted %#v", events)
				}
			}
		})
	}
}

func TestQuotaResetDetectOffCandidateCannotReplayOnEnable(t *testing.T) {
	now := time.Now().UTC()
	group := models.Group{ID: 1, ChannelID: "codex", Params: models.JSON(`{}`)}
	credential := models.Credential{ID: 2, IdentityFingerprint: "account"}
	end := now.Add(time.Hour).UnixMilli()
	cursor := &quotaResetCursor{}
	cursor.observe(credential, group, quotaResetTestObservation(now, 1, 80, end), false)
	cursor.observe(credential, group, quotaResetTestObservation(now.Add(time.Minute), 2, 0, end), false)
	events := cursor.observe(credential, group, quotaResetTestObservation(now.Add(2*time.Minute), 3, 0, end), true)
	if len(events) != 1 || events[0].SyncAllowed {
		t.Fatalf("OFF candidate retroactively enabled: %#v", events)
	}
}
