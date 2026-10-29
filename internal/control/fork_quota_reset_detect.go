package control

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"sort"
	"strconv"
	"time"

	"gpt-load/internal/accessquota"
	"gpt-load/internal/modelnotify"
	"gpt-load/internal/storage/models"
)

const quotaResetToleranceMS = int64(time.Minute / time.Millisecond)

// The monitor deliberately owns a baseline independent of CredentialObservation:
// consuming a reset card invalidates that row, but must not erase reset evidence.
// Only hashes, numeric windows and non-secret IDs are persisted here.
type quotaResetCursor struct {
	GroupID        uint                           `json:"group_id"`
	Identity       string                         `json:"identity"`
	Target         string                         `json:"target"`
	LastObservedMS int64                          `json:"last_observed_ms"`
	LastVersion    uint64                         `json:"last_version"`
	NextAttemptMS  int64                          `json:"next_attempt_ms"`
	Failures       int                            `json:"failures"`
	Windows        map[string]quotaResetWindow    `json:"windows"`
	Candidates     map[string]quotaResetCandidate `json:"candidates"`
}

type quotaResetWindow struct {
	Key          string  `json:"key"`
	Source       string  `json:"source"`
	Period       int64   `json:"period"`
	EndMS        int64   `json:"end_ms"`
	UsedPercent  float64 `json:"used_percent"`
	ObservedMS   int64   `json:"observed_ms"`
	UniquePeriod bool    `json:"unique_period"`
}

type quotaResetCandidate struct {
	Old         quotaResetWindow `json:"old"`
	New         quotaResetWindow `json:"new"`
	Kind        string           `json:"kind"`
	FirstMS     int64            `json:"first_ms"`
	SyncAllowed bool             `json:"sync_allowed"`
}

type quotaResetEvent struct {
	ID             string               `json:"id"`
	Digest         string               `json:"digest"`
	CredentialID   uint                 `json:"credential_id"`
	GroupID        uint                 `json:"group_id"`
	Identity       string               `json:"identity"`
	Target         string               `json:"target"`
	WindowKey      string               `json:"window_key"`
	Source         string               `json:"source"`
	Period         int64                `json:"period"`
	OldEndMS       int64                `json:"old_end_ms"`
	EndMS          int64                `json:"end_ms"`
	Kind           string               `json:"kind"`
	CreatedMS      int64                `json:"created_ms"`
	UniquePeriod   bool                 `json:"unique_period"`
	SyncAllowed    bool                 `json:"sync_allowed"`
	DecisionDone   bool                 `json:"decision_done"`
	SyncEnabled    bool                 `json:"sync_enabled"`
	SyncedKeys     int                  `json:"synced_keys"`
	SkipReason     string               `json:"skip_reason,omitempty"`
	DecisionTries  int                  `json:"decision_tries"`
	DecisionNextMS int64                `json:"decision_next_ms"`
	NotifyState    string               `json:"notify_state,omitempty"`
	NotifyTries    int                  `json:"notify_tries"`
	NotifyNextMS   int64                `json:"notify_next_ms"`
	Message        *modelnotify.Message `json:"message,omitempty"`
}

func quotaResetHash(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func quotaResetIdentity(credential models.Credential) string {
	return quotaResetHash(credential.IdentityFingerprint)
}

func quotaResetTarget(group models.Group) string {
	var params any
	if json.Unmarshal(group.Params, &params) != nil {
		return "" // Malformed targets cannot establish a usable identity.
	}
	canonical, _ := json.Marshal(params)
	return quotaResetHash(strconv.FormatUint(uint64(group.ID), 10) + "\x00" + group.ChannelID + "\x00" + string(group.ConnectionType) + "\x00" + string(canonical))
}

func quotaResetUUID(digest string) string {
	value, _ := hex.DecodeString(digest)
	value[6] = value[6]&0x0f | 0x50
	value[8] = value[8]&0x3f | 0x80
	encoded := hex.EncodeToString(value[:16])
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:]
}

func quotaResetFresh(response CredentialObservationResponse) bool {
	return response.State == string(models.CredentialObservationFresh) && response.LastErrorCode == "" &&
		response.Snapshot != nil && response.ObservedAtMS != nil && response.LastAttemptAtMS != nil &&
		*response.LastAttemptAtMS > 0 && *response.ObservedAtMS >= *response.LastAttemptAtMS
}

func quotaResetPercent(window ObservationQuotaWindow) (float64, bool) {
	finite := func(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 }
	var percent float64
	switch {
	case window.Used != nil && !finite(*window.Used):
		return 0, false
	case window.Used != nil && window.Limit != nil && finite(*window.Limit) && *window.Limit > 0:
		percent = *window.Used / *window.Limit * 100
	case window.Used != nil && window.Unit == "percent":
		percent = *window.Used
	case window.Used == nil && window.Utilization != nil && finite(*window.Utilization):
		percent = *window.Utilization * 100
	default:
		return 0, false // Remaining/balance drift is not usage evidence.
	}
	return percent, finite(percent) && percent <= 100
}

func quotaResetWindows(response CredentialObservationResponse) []quotaResetWindow {
	if !quotaResetFresh(response) {
		return nil
	}
	windows := append([]ObservationQuotaWindow(nil), response.Snapshot.QuotaWindows...)
	sort.SliceStable(windows, func(i, j int) bool { return windows[i].IsPrimary && !windows[j].IsPrimary })
	result := make([]quotaResetWindow, 0, len(windows))
	counts, periods := make(map[string]int), make(map[int64]int)
	for _, window := range windows {
		if window.Scope != "account" || len(window.ModelIDs) != 0 || window.ID == "" ||
			(window.State != "available" && window.State != "exhausted") || window.ResetAtMS == nil || window.WindowSeconds == nil ||
			*window.WindowSeconds < accessquota.MinPeriodSeconds || *window.WindowSeconds > accessquota.MaxPeriodSeconds ||
			*window.ResetAtMS <= *window.WindowSeconds*1000 || *window.ResetAtMS > maxSafeInteger {
			continue
		}
		percent, ok := quotaResetPercent(window)
		if !ok {
			continue
		}
		// Providers can swap primary/secondary display slots. A named source +
		// period + scope is stable; only source-less windows need their native ID.
		id := ""
		if window.SourceID == "" {
			id = window.ID
		}
		identity, _ := json.Marshal([]any{window.SourceID, *window.WindowSeconds, window.Scope, id})
		key := quotaResetHash(string(identity))
		counts[key]++
		periods[*window.WindowSeconds]++
		result = append(result, quotaResetWindow{Key: key, Source: safeInternalErrorCode(window.SourceID), Period: *window.WindowSeconds,
			EndMS: *window.ResetAtMS, UsedPercent: percent, ObservedMS: *response.ObservedAtMS})
	}
	filtered := result[:0]
	for _, window := range result {
		if counts[window.Key] == 1 {
			window.UniquePeriod = periods[window.Period] == 1
			filtered = append(filtered, window)
		}
	}
	return filtered
}

func quotaResetClassify(old, next quotaResetWindow, observedMS int64) string {
	if old.Key != next.Key || old.Period != next.Period || next.EndMS <= observedMS ||
		next.EndMS-next.Period*1000 > observedMS+quotaResetToleranceMS {
		return ""
	}
	delta := next.EndMS - old.EndMS
	drop := next.UsedPercent <= 5 && old.UsedPercent-next.UsedPercent >= 5
	due := observedMS >= old.EndMS-quotaResetToleranceMS
	cycle := old.Period * 1000
	// A fixed-cycle rollover is stronger evidence than remaining-balance changes.
	// Reject tiny anchor corrections, backward anchors, and missed multi-cycles.
	if delta >= cycle-quotaResetToleranceMS && delta <= cycle+quotaResetToleranceMS && due && observedMS <= old.EndMS+cycle {
		return "natural"
	}
	if !drop || delta < 0 || due {
		return ""
	}
	if delta == 0 || delta >= max(quotaResetToleranceMS*5, cycle/20) {
		return "sudden" // Observed unexpected restoration, never proof of a human action.
	}
	return ""
}

// observe consumes only a CURRENT successful response, never a preserved partial
// snapshot. A candidate retains its old baseline until a separate refresh at
// least 30 seconds later confirms the same anchor and evidence.
func (cursor *quotaResetCursor) observe(credential models.Credential, group models.Group, response CredentialObservationResponse, syncEnabled bool) []quotaResetEvent {
	if !quotaResetFresh(response) {
		cursor.Candidates = make(map[string]quotaResetCandidate)
		return nil
	}
	identity, target := quotaResetIdentity(credential), quotaResetTarget(group)
	if target == "" || credential.IdentityFingerprint == "" {
		return nil
	}
	if cursor.Identity != identity || cursor.Target != target || cursor.GroupID != group.ID {
		*cursor = quotaResetCursor{GroupID: group.ID, Identity: identity, Target: target,
			Windows: make(map[string]quotaResetWindow), Candidates: make(map[string]quotaResetCandidate)}
	}
	if *response.ObservedAtMS <= cursor.LastObservedMS || response.ObservationVersion <= cursor.LastVersion {
		return nil
	}
	cursor.LastObservedMS, cursor.LastVersion = *response.ObservedAtMS, response.ObservationVersion
	if cursor.Windows == nil {
		cursor.Windows = make(map[string]quotaResetWindow)
	}
	if cursor.Candidates == nil {
		cursor.Candidates = make(map[string]quotaResetCandidate)
	}
	events := []quotaResetEvent{}
	present := make(map[string]bool)
	for _, next := range quotaResetWindows(response) {
		present[next.Key] = true
		old, exists := cursor.Windows[next.Key]
		if !exists {
			cursor.Windows[next.Key] = next
			continue
		}
		candidate, confirming := cursor.Candidates[next.Key]
		if confirming {
			kind := quotaResetClassify(candidate.Old, next, candidate.FirstMS)
			if kind != candidate.Kind || next.EndMS != candidate.New.EndMS || next.ObservedMS-candidate.FirstMS > int64(10*time.Minute/time.Millisecond) {
				delete(cursor.Candidates, next.Key)
				cursor.Windows[next.Key] = next
				continue
			}
			if next.ObservedMS-candidate.FirstMS < int64(30*time.Second/time.Millisecond) {
				continue
			}
			event := quotaResetEvent{CredentialID: credential.ID, GroupID: group.ID, Identity: identity, Target: target,
				WindowKey: next.Key, Source: next.Source, Period: next.Period, OldEndMS: candidate.Old.EndMS, EndMS: next.EndMS,
				Kind: candidate.Kind, CreatedMS: next.ObservedMS, UniquePeriod: candidate.Old.UniquePeriod && candidate.New.UniquePeriod && next.UniquePeriod,
				SyncAllowed: syncEnabled && candidate.SyncAllowed}
			// Same-anchor restorations can happen again after genuinely accruing new
			// usage. Persisted first-observation time distinguishes those events.
			epoch := int64(0)
			if event.EndMS == event.OldEndMS {
				epoch = candidate.FirstMS
			}
			body, _ := json.Marshal([]any{"fork-quota-reset-v1", identity, target, next.Key, event.Period, event.OldEndMS, event.EndMS, event.Kind, epoch})
			event.Digest = quotaResetHash(string(body))
			event.ID = quotaResetUUID(event.Digest)
			events = append(events, event)
			delete(cursor.Candidates, next.Key)
			cursor.Windows[next.Key] = next
			continue
		}
		kind := quotaResetClassify(old, next, next.ObservedMS)
		if kind == "" {
			cursor.Windows[next.Key] = next
			continue
		}
		cursor.Candidates[next.Key] = quotaResetCandidate{Old: old, New: next, Kind: kind, FirstMS: next.ObservedMS, SyncAllowed: syncEnabled}
	}
	// Missing or ambiguous windows break confirmation; they never imply reset.
	for key := range cursor.Windows {
		if !present[key] {
			delete(cursor.Windows, key)
			delete(cursor.Candidates, key)
		}
	}
	return events
}
