package control

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	"gorm.io/gorm"

	"gpt-load/internal/modelnotify"
	"gpt-load/internal/platform/securefile"
	"gpt-load/internal/storage/models"
)

const quotaResetSyncKind = "fork_quota_reset_sync"

var errQuotaResetSkip = errors.New("quota reset sync skipped")

type quotaResetNotifier interface {
	Enabled() bool
	Send(context.Context, string, modelnotify.Message) (modelnotify.Result, error)
}

type quotaResetMonitorState struct {
	Version int                        `json:"version"`
	Cursors map[uint]*quotaResetCursor `json:"cursors"`
	Events  []quotaResetEvent          `json:"events"`
}

type quotaResetMonitor struct {
	service  *Service
	path     string
	state    quotaResetMonitorState
	notifier quotaResetNotifier
	first    bool
}

// RunQuotaResetMonitor shares bounded account observations with optional
// per-account expiring-card actions; it never performs model probes.
func (s *Service) RunQuotaResetMonitor(ctx context.Context) {
	if s == nil || s.quotaResetDataDir == "" {
		return
	}
	monitor, err := newQuotaResetMonitor(s)
	if err != nil {
		logrus.WithField("event", "control.quota_reset_monitor_state_unavailable").Warn("quota reset monitor did not start")
		return // Never discard a corrupt cursor/outbox and replay against a blank baseline.
	}
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			if err := monitor.cycle(ctx); err != nil {
				if ctx.Err() == nil {
					logrus.WithField("event", "control.quota_reset_monitor_stopped").Warn("quota reset monitor could not persist progress")
				}
				return // Fail closed if write-ahead durability is unavailable.
			}
			timer.Reset(30 * time.Second)
		}
	}
}

func newQuotaResetMonitor(s *Service) (*quotaResetMonitor, error) {
	if s == nil || s.quotaResetDataDir == "" {
		return nil, errors.New("quota reset monitor data directory unavailable")
	}
	if err := securefile.PrepareManagedDataDir(s.quotaResetDataDir); err != nil {
		return nil, err
	}
	monitor := &quotaResetMonitor{service: s, path: filepath.Join(s.quotaResetDataDir, "fork-quota-reset-monitor.json"), first: true}
	monitor.state = quotaResetMonitorState{Version: 1, Cursors: make(map[uint]*quotaResetCursor), Events: []quotaResetEvent{}}
	if err := securefile.HardenManagedFileIfExists(monitor.path); err != nil {
		return nil, err
	}
	body, err := os.ReadFile(monitor.path)
	if err == nil {
		if len(body) > 16<<20 || json.Unmarshal(body, &monitor.state) != nil || monitor.state.Version != 1 || monitor.state.Cursors == nil {
			return nil, errors.New("invalid quota reset monitor state")
		}
		for _, event := range monitor.state.Events {
			digest, decodeErr := hex.DecodeString(event.Digest)
			if decodeErr != nil || len(digest) != 32 || event.ID != quotaResetUUID(event.Digest) {
				return nil, errors.New("invalid quota reset monitor event")
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	client, err := modelnotify.NewFromEnvironment()
	if err == nil {
		monitor.notifier = client
	} else {
		logrus.WithField("event", "control.quota_reset_notification_configuration_invalid").Warn("quota reset notifications unavailable")
	}
	return monitor, nil
}

// save atomically commits both the replacement baseline and its pending event.
// The managed directory rejects symlinks; CreateTemp creates an exclusive 0600
// file. Sync both file and directory so a crash cannot expose half a JSON write.
func (monitor *quotaResetMonitor) save() error {
	body, err := json.Marshal(monitor.state)
	if err != nil {
		return err
	}
	dir := filepath.Dir(monitor.path)
	file, err := os.CreateTemp(dir, ".fork-quota-reset-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err = file.Chmod(0600); err == nil {
		_, err = file.Write(body)
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(file.Name(), monitor.path); err != nil {
		return err
	}
	directory, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func quotaResetBackoff(failures int) time.Duration {
	return []time.Duration{5 * time.Minute, 10 * time.Minute, 20 * time.Minute, 30 * time.Minute}[min(max(failures-1, 0), 3)]
}

func (cursor *quotaResetCursor) schedule(now time.Time) {
	interval := 5 * time.Minute
	if cursor.Failures > 0 {
		interval = quotaResetBackoff(cursor.Failures)
	} else {
		if len(cursor.Candidates) != 0 {
			interval = time.Minute
		}
		if cursor.CardExpiryMS > now.UnixMilli() && cursor.CardExpiryMS <= now.Add(autoResetCreditLead+5*time.Minute).UnixMilli() {
			interval = time.Minute
		}
		for _, window := range cursor.Windows {
			until := time.UnixMilli(window.EndMS).Sub(now)
			if until >= -5*time.Minute && until <= 5*time.Minute {
				interval = time.Minute
			}
		}
	}
	cursor.NextAttemptMS = now.Add(interval).UnixMilli()
}

func (monitor *quotaResetMonitor) cycle(ctx context.Context) error {
	s := monitor.service
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var credentials []models.Credential
	// Do not carry writeMu across requests. The existing observation flight and
	// expected-target checks fence edits; application rechecks everything again.
	s.writeMu.RLock()
	err := s.db.WithContext(ctx).Preload("Group").Where("status = ?", models.CredentialStatusActive).
		Order("group_id ASC, id ASC").Find(&credentials).Error
	s.writeMu.RUnlock()
	if err != nil {
		return err
	}
	now := s.now()
	active := make(map[uint]bool)
	eligible := make([]models.Credential, 0, len(credentials))
	for _, credential := range credentials {
		if credential.Group == nil || !credential.Group.Enabled || !s.supportsCredentialObservation(*credential.Group) {
			continue
		}
		active[credential.ID] = true
		if monitor.state.Cursors[credential.ID] == nil {
			monitor.state.Cursors[credential.ID] = &quotaResetCursor{}
		}
		eligible = append(eligible, credential)
	}
	for id := range monitor.state.Cursors {
		if !active[id] {
			delete(monitor.state.Cursors, id)
		}
	}
	// Due expiring cards take priority; other accounts retain oldest-due order.
	sort.SliceStable(eligible, func(i, j int) bool {
		left, right := monitor.state.Cursors[eligible[i].ID], monitor.state.Cursors[eligible[j].ID]
		urgent := func(cursor *quotaResetCursor) bool {
			return cursor.NextAttemptMS <= now.UnixMilli() && cursor.CardExpiryMS > now.UnixMilli() && cursor.CardExpiryMS <= now.Add(autoResetCreditLead).UnixMilli()
		}
		if urgent(left) != urgent(right) {
			return urgent(left)
		}
		if urgent(left) && left.CardExpiryMS != right.CardExpiryMS {
			return left.CardExpiryMS < right.CardExpiryMS
		}
		return left.NextAttemptMS < right.NextAttemptMS
	})
	cap := 20
	if monitor.first {
		cap, monitor.first = 3, false
	}
	observed := 0
	for _, credential := range eligible {
		cursor := monitor.state.Cursors[credential.ID]
		if cursor.NextAttemptMS > now.UnixMilli() || observed >= cap || ctx.Err() != nil {
			continue
		}
		observed++
		group := *credential.Group
		requestContext, cancel := context.WithTimeout(ctx, 40*time.Second)
		response, refreshErr := s.refreshCredentialObservationForTarget(requestContext, group.ID, credential.ID, observationRefreshAutomatic, &group)
		cancel()
		// Never ingest cached DB observations on startup. Only this successful
		// current refresh can create/confirm an event; preserved partials fail closed.
		current, targetErr := s.quotaResetCurrentTarget(ctx, credential, group)
		if refreshErr == nil && targetErr == nil && current && quotaResetFresh(response) {
			enabled := false
			if snapshot := s.manager.Current(); snapshot != nil {
				enabled = snapshot.Groups[group.ID].AccountQuotaResetSyncEnabled
			}
			for _, event := range cursor.observe(credential, group, response, enabled) {
				duplicate := slices.ContainsFunc(monitor.state.Events, func(previous quotaResetEvent) bool { return previous.ID == event.ID })
				if !duplicate {
					monitor.state.Events = append(monitor.state.Events, event)
					logrus.WithFields(logrus.Fields{"event": "control.quota_reset_detected", "reset_kind": event.Kind, "period_seconds": event.Period}).Info("upstream subscription restoration confirmed")
				}
			}
			cursor.Failures = 0
			_, cursor.CardExpiryMS, _ = autoResetCreditCandidate(response, s.now())
		} else {
			cursor.Failures++
			cursor.Candidates = make(map[string]quotaResetCandidate)
		}
		cursor.schedule(s.now())
		monitor.prune(s.now())
		if err := monitor.save(); err != nil {
			return err // Pending event is durable BEFORE any key mutation/send.
		}
		if err := monitor.autoResetCredit(ctx, credential, group, response); err != nil {
			return err
		}
		if cursor.CardExpiryMS > s.now().UnixMilli() && cursor.CardExpiryMS <= s.now().Add(autoResetCreditLead).UnixMilli() {
			cursor.NextAttemptMS = s.now().Add(time.Minute).UnixMilli()
			if err := monitor.save(); err != nil {
				return err
			}
		}
	}
	monitor.prune(s.now())
	if err := monitor.save(); err != nil {
		return err
	}
	return monitor.flush(ctx)
}

func (s *Service) quotaResetCurrentTarget(ctx context.Context, credential models.Credential, group models.Group) (bool, error) {
	var current models.Credential
	var target models.Group
	err := s.withReadSnapshot(ctx, func(tx *gorm.DB) error {
		if err := tx.First(&current, credential.ID).Error; err != nil {
			return err
		}
		return tx.First(&target, group.ID).Error
	})
	return err == nil && current.Status == models.CredentialStatusActive && current.GroupID == group.ID && target.Enabled &&
		quotaResetIdentity(current) == quotaResetIdentity(credential) && quotaResetTarget(target) == quotaResetTarget(group), err
}

func (monitor *quotaResetMonitor) prune(now time.Time) {
	cutoff := now.Add(-30 * 24 * time.Hour).UnixMilli()
	events := monitor.state.Events[:0]
	for _, event := range monitor.state.Events {
		if event.Kind == "card" && !event.DecisionDone || event.CreatedMS >= cutoff {
			events = append(events, event)
		}
	}
	if len(events) > 1000 {
		events = events[len(events)-1000:]
	}
	monitor.state.Events = events
}

type quotaResetSyncResult struct {
	Enabled        bool             `json:"enabled"`
	Keys           int              `json:"keys"`
	Reason         string           `json:"reason"`
	Seeds          []quotaResetSeed `json:"seeds,omitempty"`
	ProofFromMS    int64            `json:"proof_from_ms,omitempty"`
	TotalBaselines map[uint]int64   `json:"total_baselines_nano_usd,omitempty"`
}

func groupOverrideBool(group models.Group, key string) bool {
	var overrides map[string]any
	if json.Unmarshal(group.Overrides, &overrides) != nil {
		return false
	}
	value, _ := overrides[key].(bool)
	return value
}

// singletonConfiguredGroups is intentionally a policy on CONFIGURED scope, not
// effective routing: empty (all groups) and multi-group keys are never dedicated.
func singletonConfiguredGroups(groups []uint, groupID uint) bool {
	return len(groups) == 1 && groups[0] == groupID
}

func (s *Service) quotaResetReceipt(ctx context.Context, event quotaResetEvent) (quotaResetSyncResult, bool, error) {
	return s.quotaResetReceiptDB(s.db.WithContext(ctx), event)
}

func (s *Service) quotaResetReceiptDB(db *gorm.DB, event quotaResetEvent) (quotaResetSyncResult, bool, error) {
	var receipt models.ControlOperation
	err := db.Where("idempotency_key = ?", event.ID).Take(&receipt).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return quotaResetSyncResult{}, false, nil
	}
	if err != nil {
		return quotaResetSyncResult{}, false, err
	}
	digest, _ := hex.DecodeString(event.Digest)
	if receipt.OperationKind != quotaResetSyncKind || receipt.ResourceIdentity != event.Digest ||
		!bytes.Equal(receipt.RequestDigest, digest) || receipt.CompletedAtMS == nil {
		return quotaResetSyncResult{}, false, errors.New("quota reset receipt mismatch")
	}
	result := quotaResetSyncResult{Enabled: true, Reason: "already_processed"}
	if len(receipt.CanonicalResult) != 0 {
		_ = json.Unmarshal(receipt.CanonicalResult, &result)
	}
	return result, true, nil
}

func (monitor *quotaResetMonitor) flush(ctx context.Context) error {
	if err := monitor.scanResetCreditOperations(ctx); err != nil {
		return err
	}
	// Card receipts take priority over heuristic events from the same refresh.
	slices.SortStableFunc(monitor.state.Events, func(a, b quotaResetEvent) int {
		if (a.Kind == "card") == (b.Kind == "card") {
			return 0
		}
		if a.Kind == "card" {
			return -1
		}
		return 1
	})
	if err := monitor.save(); err != nil {
		return err
	}
	now := monitor.service.now()
	decisions, submissions := 0, 0
	for index := range monitor.state.Events {
		event := &monitor.state.Events[index]
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !event.DecisionDone && event.DecisionNextMS <= now.UnixMilli() && decisions < 20 {
			decisions++
			if event.Kind == "card" {
				ready, _, reason, err := monitor.service.readyResetCreditEvent(ctx, *event)
				if err != nil {
					return err
				}
				if reason == "" && event.EndMS == 0 {
					*event = ready
					if err := monitor.save(); err != nil {
						return err
					}
				}
			}
			result, err := monitor.service.syncQuotaReset(ctx, *event)
			if err == nil && quotaResetRetryReason(result.Reason) {
				event.DecisionTries++
				event.DecisionNextMS = now.Add(time.Minute).UnixMilli()
				event.SkipReason = result.Reason
				if err := monitor.save(); err != nil {
					return err
				}
				continue
			}
			if err != nil {
				event.DecisionTries++
				event.DecisionNextMS = now.Add(quotaResetBackoff(event.DecisionTries)).UnixMilli()
				if event.DecisionTries >= 8 && event.Kind != "card" {
					event.DecisionDone, event.SkipReason = true, "sync_error"
				}
			} else {
				event.DecisionDone, event.SyncEnabled, event.SyncedKeys, event.SkipReason = true, result.Enabled, result.Keys, result.Reason
			}
			if event.DecisionDone {
				message := monitor.message(*event)
				event.Message = &message
			}
			if err := monitor.save(); err != nil {
				return err
			}
		}
		if event.DecisionDone && event.NotifyState == "" && event.Kind == "natural" && event.Period == 18000 {
			event.NotifyState = "suppressed"
			if err := monitor.save(); err != nil {
				return err
			}
			continue
		}
		if !event.DecisionDone || event.NotifyState != "" || event.NotifyNextMS > now.UnixMilli() || submissions >= 3 {
			continue
		}
		if event.Message == nil {
			message := monitor.message(*event)
			event.Message = &message
			if err := monitor.save(); err != nil {
				return err
			}
		}
		if monitor.notifier == nil || !monitor.notifier.Enabled() {
			event.NotifyNextMS = now.Add(30 * time.Minute).UnixMilli()
			if err := monitor.save(); err != nil {
				return err
			}
			continue // Detection never waits for a notification configuration.
		}
		submissions++
		requestContext, cancel := context.WithTimeout(ctx, 10*time.Second)
		_, err := monitor.notifier.Send(requestContext, event.ID, *event.Message)
		cancel()
		if err == nil {
			event.NotifyState = "accepted" // Gateway accepted; this is NOT recipient delivery.
		} else if errors.Is(err, modelnotify.ErrDisabled) {
			event.NotifyNextMS = now.Add(30 * time.Minute).UnixMilli()
		} else {
			event.NotifyTries++
			var classified *modelnotify.Error
			if errors.As(err, &classified) && !classified.Retryable {
				event.NotifyState = "error"
				if classified.Code == "unrouted" {
					event.NotifyState = "unrouted"
				}
			} else if event.NotifyTries >= 8 {
				event.NotifyState = "error"
			} else {
				event.NotifyNextMS = now.Add(quotaResetBackoff(event.NotifyTries)).UnixMilli()
			}
		}
		if err := monitor.save(); err != nil {
			return err
		}
	}
	return nil
}

func quotaResetSafeLabel(value string, fallback string) string {
	if value == "" || strings.ContainsAny(value, "@\r\n") {
		return fallback
	}
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) > 60 {
		value = string(runes[:60])
	}
	return value
}

func (monitor *quotaResetMonitor) message(event quotaResetEvent) modelnotify.Message {
	s := monitor.service
	groupName, modelNames := fmt.Sprintf("分组 #%d", event.GroupID), "账号级额度"
	if snapshot := s.manager.Current(); snapshot != nil {
		if group, exists := snapshot.Groups[event.GroupID]; exists {
			groupName = quotaResetSafeLabel(group.Name, groupName)
			models := []string{}
			for _, model := range group.Models {
				if len(models) >= 8 {
					break
				}
				models = append(models, quotaResetSafeLabel(model.ID, "model"))
			}
			if len(models) != 0 {
				modelNames = strings.Join(models, ", ")
			}
		}
	}
	alias := fmt.Sprintf("账号 #%d", event.CredentialID)
	kind, level := "自然周期重置", "info"
	if event.Kind == "card" {
		kind = "重置卡额度恢复"
	} else if event.Kind == "sudden" {
		kind, level = "额度恢复", "warning"
	}
	setting := "关闭"
	if event.SyncEnabled {
		setting = "开启"
	}
	reasons := map[string]string{"sync_off": "未开启联动", "multiple_active_accounts": "分组包含多个账号，未联动", "ambiguous_period_sources": "周期来源不唯一，未联动", "target_changed": "上游目标已变更，未联动", "identity_changed": "账号已变更，未联动", "outdated_event": "重置记录已过期，未联动", "no_matching_dedicated_periodic_keys": "没有需要同步的 Key", "already_processed": "已处理", "sync_error": "Key 同步失败", "synced": "已同步"}
	reason := reasons[event.SkipReason]
	if reason == "" {
		reason = "未联动"
	}
	cst := time.FixedZone("CST", 8*60*60)
	format := func(ms int64) string { return time.UnixMilli(ms).In(cst).Format("2006-01-02 15:04:05 CST") }
	period := strconv.FormatInt(event.Period, 10) + " 秒"
	if event.Period%86400 == 0 {
		period = strconv.FormatInt(event.Period/86400, 10) + " 天"
	} else if event.Period%3600 == 0 {
		period = strconv.FormatInt(event.Period/3600, 10) + " 小时"
	}
	body := fmt.Sprintf("模型：%s\n周期：%s\n原重置时间：%s\n下次重置：%s\nAPIKey 联动：%s\n同步 Key：%d 个\n结果：%s", modelNames, period, format(event.OldEndMS), format(event.EndMS), setting, event.SyncedKeys, reason)
	return modelnotify.Message{Topic: "model.subscription.reset", Title: kind + " · " + groupName + " · " + alias,
		Level: level, Body: body, DedupeKey: event.ID, DedupeTTL: 86400,
		Metadata: map[string]any{"event_id": event.ID, "kind": event.Kind, "group_id": event.GroupID, "credential_id": event.CredentialID,
			"period_seconds": event.Period, "source": event.Source, "window_key": event.WindowKey, "sync_enabled": event.SyncEnabled, "synced_keys": event.SyncedKeys, "skip_reason": event.SkipReason}}
}
