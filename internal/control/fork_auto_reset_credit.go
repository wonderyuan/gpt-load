package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"gpt-load/internal/channel"
	app_errors "gpt-load/internal/platform/errors"
	"gpt-load/internal/platform/response"
	"gpt-load/internal/storage/models"
	subscriptionruntime "gpt-load/internal/subscription/runtime"
)

const autoResetCreditLead = 30 * time.Minute
const autoResetCreditPrefix = models.InternalSystemSettingPrefix + "reset_credit_auto.v1."

type AutoResetCreditStatus struct {
	Enabled       bool   `json:"enabled"`
	State         string `json:"state"`
	ExpiresAtMS   *int64 `json:"expires_at_ms,omitempty"`
	LastAttemptMS *int64 `json:"last_attempt_at_ms,omitempty"`
}

type autoResetCreditPolicy struct {
	Enabled       bool   `json:"enabled"`
	Identity      string `json:"identity"`
	Target        string `json:"target"`
	RequestID     string `json:"request_id,omitempty"`
	Kind          string `json:"kind,omitempty"`
	ExpiresMS     int64  `json:"expires_ms,omitempty"`
	State         string `json:"state,omitempty"`
	LastAttemptMS int64  `json:"last_attempt_ms,omitempty"`
	NextAttemptMS int64  `json:"next_attempt_ms,omitempty"`
	Attempts      int    `json:"attempts,omitempty"`
}

type autoResetCreditContextKey struct{}

type autoResetCreditDispatch struct {
	Identity string
	Target   string
	ExpiryMS int64
}

func autoResetCreditKey(id uint) string { return fmt.Sprintf("%s%d", autoResetCreditPrefix, id) }

func (s *Service) supportsAutoResetCredit(group models.Group) bool {
	if group.ChannelID == "codex" && normalizeGroupConnectionType(group.ConnectionType) == models.ConnectionTypeSubscription {
		_, ok := s.subscriptions.ResetCreditAction(channel.ID(group.ChannelID))
		return ok
	}
	if group.ChannelID != "zhipuai" || normalizeGroupConnectionType(group.ConnectionType) != models.ConnectionTypeAPIKey {
		return false
	}
	supported, _ := s.credentialResetTypes(group)
	return supported
}

func (s *Service) loadAutoResetCreditPolicy(ctx context.Context, credential models.Credential, group models.Group) (autoResetCreditPolicy, error) {
	var row models.SystemSetting
	err := s.db.WithContext(ctx).Where("key = ?", autoResetCreditKey(credential.ID)).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return autoResetCreditPolicy{}, nil
	}
	if err != nil {
		return autoResetCreditPolicy{}, err
	}
	var policy autoResetCreditPolicy
	if json.Unmarshal([]byte(row.Value), &policy) != nil {
		return policy, errors.New("invalid auto reset credit state")
	}
	if policy.Identity != quotaResetIdentity(credential) || policy.Target != quotaResetTarget(group) {
		return autoResetCreditPolicy{}, nil
	}
	if policy.RequestID != "" && validateIdempotencyKey(policy.RequestID) != nil {
		return policy, errors.New("invalid auto reset credit request")
	}
	return policy, nil
}

func (s *Service) saveAutoResetCreditPolicy(ctx context.Context, id uint, policy autoResetCreditPolicy) error {
	raw, err := json.Marshal(policy)
	if err != nil {
		return err
	}
	row := models.SystemSetting{Key: autoResetCreditKey(id), Value: string(raw), UpdatedAtMS: s.now().UnixMilli()}
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "key"}}, DoUpdates: clause.AssignmentColumns([]string{"value", "updated_at_ms"})}).Create(&row).Error
}

func autoResetCreditView(policy autoResetCreditPolicy) *AutoResetCreditStatus {
	status := &AutoResetCreditStatus{Enabled: policy.Enabled, State: policy.State}
	if status.State == "" {
		status.State = "waiting"
	}
	if policy.ExpiresMS > 0 {
		status.ExpiresAtMS = &policy.ExpiresMS
	}
	if policy.LastAttemptMS > 0 {
		status.LastAttemptMS = &policy.LastAttemptMS
	}
	return status
}

func (s *Service) SetAutoResetCredit(ctx context.Context, groupID, credentialID uint, enabled bool) (AutoResetCreditStatus, error) {
	s.resetCreditActionMu.Lock()
	defer s.resetCreditActionMu.Unlock()
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	group, credential, _, err := s.loadObservationTarget(ctx, groupID, credentialID)
	if err != nil {
		return AutoResetCreditStatus{}, err
	}
	if !s.supportsAutoResetCredit(group) {
		return AutoResetCreditStatus{}, app_errors.ErrValidation
	}
	policy, err := s.loadAutoResetCreditPolicy(ctx, credential, group)
	if err != nil {
		return AutoResetCreditStatus{}, app_errors.ErrInternalServer
	}
	policy.Enabled, policy.Identity, policy.Target = enabled, quotaResetIdentity(credential), quotaResetTarget(group)
	if err := s.saveAutoResetCreditPolicy(ctx, credentialID, policy); err != nil {
		return AutoResetCreditStatus{}, app_errors.ParseDBError(err)
	}
	return *autoResetCreditView(policy), nil
}

func (s *Server) handleSetAutoResetCredit(c *gin.Context) {
	groupID, ok := groupID(c, "auto_reset_credit")
	if !ok {
		return
	}
	credentialID, ok := credentialID(c, "auto_reset_credit")
	if !ok {
		return
	}
	var request struct {
		Enabled *bool `json:"enabled"`
	}
	if err := bindStrictJSON(c, &request); err != nil || request.Enabled == nil {
		writeServiceError(c, "auto_reset_credit", app_errors.ErrValidation)
		return
	}
	status, err := s.service.SetAutoResetCredit(c.Request.Context(), groupID, credentialID, *request.Enabled)
	if err != nil {
		writeServiceError(c, "auto_reset_credit", err)
		return
	}
	response.SuccessI18n(c, "common.success", status)
}

func autoResetCreditCandidate(observation CredentialObservationResponse, now time.Time) (string, int64, bool) {
	if !quotaResetFresh(observation) || observation.Snapshot.ResetCreditsAvailable == nil || *observation.Snapshot.ResetCreditsAvailable <= 0 ||
		*observation.ObservedAtMS > now.UnixMilli() || now.UnixMilli()-*observation.ObservedAtMS > int64(2*time.Minute/time.Millisecond) {
		return "", 0, false
	}
	cards := observation.Snapshot.ResetCredits
	if int64(len(cards)) != *observation.Snapshot.ResetCreditsAvailable {
		return "", 0, false
	}
	var available []ObservationResetCredit
	for _, card := range cards {
		if card.Available != nil && !*card.Available {
			continue
		}
		if card.ExpiresAtMS == nil || *card.ExpiresAtMS <= 0 {
			return "", 0, false
		}
		if *card.ExpiresAtMS > now.UnixMilli() {
			available = append(available, card)
		}
	}
	if len(available) == 0 {
		return "", 0, false
	}
	sort.SliceStable(available, func(i, j int) bool { return *available[i].ExpiresAtMS < *available[j].ExpiresAtMS })
	card := available[0]
	return card.Kind, *card.ExpiresAtMS, *card.ExpiresAtMS <= now.Add(autoResetCreditLead).UnixMilli()
}

// One upstream account must have only one automatic owner, including duplicate
// credential rows in other groups. Manual and automatic spends share the lock.
func (s *Service) uniqueAutoResetAccount(ctx context.Context, credential models.Credential) bool {
	var count int64
	err := s.db.WithContext(ctx).Model(&models.Credential{}).
		Joins("JOIN groups ON groups.id = credentials.group_id").
		Where("credentials.identity_fingerprint = ? AND credentials.status = ? AND groups.enabled = ? AND groups.channel_id = ?", credential.IdentityFingerprint, models.CredentialStatusActive, true, credential.Group.ChannelID).
		Count(&count).Error
	return err == nil && count == 1
}

func (monitor *quotaResetMonitor) autoResetCredit(ctx context.Context, credential models.Credential, group models.Group, observation CredentialObservationResponse) error {
	s := monitor.service
	if !s.supportsAutoResetCredit(group) {
		return nil
	}
	s.resetCreditActionMu.Lock()
	defer s.resetCreditActionMu.Unlock()
	currentGroup, current, _, err := s.loadObservationTarget(ctx, group.ID, credential.ID)
	if err != nil {
		return nil
	}
	if !currentGroup.Enabled || current.Status != models.CredentialStatusActive ||
		(current.AuthState != "" && current.AuthState != models.CredentialAuthStateReady) ||
		quotaResetIdentity(current) != quotaResetIdentity(credential) || quotaResetTarget(currentGroup) != quotaResetTarget(group) {
		return nil
	}
	current.Group = &currentGroup
	policy, err := s.loadAutoResetCreditPolicy(ctx, current, currentGroup)
	if err != nil {
		return err
	}
	if !policy.Enabled || !s.uniqueAutoResetAccount(ctx, current) {
		return nil
	}
	now := s.now()
	kind, expiry, due := autoResetCreditCandidate(observation, now)
	pending := policy.State == "pending" || policy.State == "unknown"
	if pending {
		if policy.NextAttemptMS > now.UnixMilli() || policy.ExpiresMS <= now.UnixMilli() || policy.Attempts >= 3 {
			return nil
		}
	} else {
		if !due || expiry <= policy.ExpiresMS {
			return nil
		}
		var recent int64
		if err := s.db.WithContext(ctx).Model(&models.CredentialResetOperation{}).
			Where("credential_id = ? AND state = ? AND updated_at_ms > ?", current.ID, models.CredentialResetOperationSucceeded, now.Add(-5*time.Minute).UnixMilli()).Count(&recent).Error; err != nil {
			return err
		}
		if recent > 0 {
			return nil
		}
		if policy.LastAttemptMS > 0 && now.UnixMilli()-policy.LastAttemptMS < int64(5*time.Minute/time.Millisecond) {
			return nil
		}
		policy.RequestID, policy.Kind, policy.ExpiresMS = uuid.NewString(), kind, expiry
		policy.Attempts = 0
	}
	policy.Identity, policy.Target = quotaResetIdentity(current), quotaResetTarget(currentGroup)
	policy.State, policy.LastAttemptMS, policy.NextAttemptMS = "pending", now.UnixMilli(), now.Add(time.Minute).UnixMilli()
	policy.Attempts++
	if err := s.saveAutoResetCreditPolicy(ctx, current.ID, policy); err != nil {
		return err
	}
	call := context.WithValue(ctx, autoResetCreditContextKey{}, autoResetCreditDispatch{Identity: policy.Identity, Target: policy.Target, ExpiryMS: policy.ExpiresMS})
	call = subscriptionruntime.WithResetCreditExpiryCondition(call, subscriptionruntime.ResetCreditExpiryCondition{ExpiresAtMS: policy.ExpiresMS, NowMS: now.UnixMilli()})
	_, consumeErr := s.consumeCredentialResetCredit(call, current.GroupID, current.ID, policy.RequestID, policy.Kind)
	if consumeErr == nil {
		policy.State = "succeeded"
	} else {
		policy.State = "failed"
		var apiErr *app_errors.APIError
		if errors.As(consumeErr, &apiErr) {
			if apiErr.Code == app_errors.ErrResetCreditOutcomeUnknown.Code {
				policy.State = "unknown"
			}
			if apiErr.Code == app_errors.ErrResetCreditPreparationFailed.Code {
				policy.State = "pending"
			}
		}
	}
	cleanup, cancel := context.WithTimeout(context.Background(), controlTransactionCleanupTimeout)
	defer cancel()
	return s.saveAutoResetCreditPolicy(cleanup, current.ID, policy)
}

func (s *Service) autoResetCreditAllowed(ctx context.Context, group models.Group, credential models.Credential) bool {
	intent, automatic := ctx.Value(autoResetCreditContextKey{}).(autoResetCreditDispatch)
	if !automatic {
		return true
	}
	if !s.supportsAutoResetCredit(group) || !group.Enabled || credential.Status != models.CredentialStatusActive ||
		(credential.AuthState != "" && credential.AuthState != models.CredentialAuthStateReady) ||
		intent.Identity != quotaResetIdentity(credential) || intent.Target != quotaResetTarget(group) {
		return false
	}
	policy, err := s.loadAutoResetCreditPolicy(ctx, credential, group)
	return err == nil && policy.Enabled && policy.ExpiresMS == intent.ExpiryMS
}

func (s *Service) resetCreditAutomatic(ctx context.Context) bool {
	_, ok := ctx.Value(autoResetCreditContextKey{}).(autoResetCreditDispatch)
	return ok
}
