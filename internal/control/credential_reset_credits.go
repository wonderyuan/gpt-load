package control

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/sirupsen/logrus"
	"gorm.io/gorm"

	"gpt-load/internal/platform/canonicaljson"
	app_errors "gpt-load/internal/platform/errors"
	"gpt-load/internal/platform/utils"
	"gpt-load/internal/storage/models"
	subscriptionruntime "gpt-load/internal/subscription/runtime"
)

type ObservationResetCredit struct {
	Kind        string `json:"kind,omitempty"`
	Available   *bool  `json:"available,omitempty"`
	ExpiresAtMS *int64 `json:"expires_at_ms,omitempty"`
}

type ResetCreditConsumeRequest struct {
	ResetType string `json:"reset_type,omitempty"`
}

func (request *ResetCreditConsumeRequest) UnmarshalJSON(raw []byte) error {
	var wire struct {
		ResetType optionalField[string] `json:"reset_type"`
	}
	if err := decodeStrictControlJSONObject(raw, &wire); err != nil {
		return err
	}
	if wire.ResetType.Set && (wire.ResetType.Null || (wire.ResetType.Value != "five_hour" && wire.ResetType.Value != "week")) {
		return app_errors.ErrValidation
	}
	request.ResetType = wire.ResetType.Value
	return nil
}

type ResetCreditConsumeResponse struct {
	Status             string                         `json:"status"`
	WindowsReset       int                            `json:"windows_reset"`
	RedeemedAtMS       *int64                         `json:"redeemed_at_ms,omitempty"`
	Observation        *CredentialObservationResponse `json:"observation,omitempty"`
	ObservationPending bool                           `json:"observation_pending,omitempty"`
	Replayed           bool                           `json:"replayed"`
}

type storedResetCreditResult struct {
	Status       string `json:"status"`
	WindowsReset int    `json:"windows_reset"`
	RedeemedAtMS *int64 `json:"redeemed_at_ms,omitempty"`
}

type storedResetCreditRequest struct {
	Request json.RawMessage `json:"request"`
}

// ConsumeCredentialResetCredit idempotently consumes one provider reset credit
// and returns the refreshed observation when it is immediately available.
func (s *Service) ConsumeCredentialResetCredit(
	ctx context.Context,
	groupID uint,
	credentialID uint,
	idempotencyKey string,
	resetTypes ...string,
) (ResetCreditConsumeResponse, error) {
	kind := ""
	if len(resetTypes) > 1 {
		return ResetCreditConsumeResponse{}, app_errors.ErrValidation
	}
	if len(resetTypes) == 1 {
		kind = resetTypes[0]
	}
	if kind != "" && kind != "five_hour" && kind != "week" {
		return ResetCreditConsumeResponse{}, app_errors.ErrValidation
	}
	if groupID == 0 || credentialID == 0 {
		return ResetCreditConsumeResponse{}, app_errors.ErrValidation
	}
	if validateIdempotencyKey(idempotencyKey) != nil {
		return ResetCreditConsumeResponse{}, app_errors.ErrInvalidIdempotencyKey
	}
	group, credential, _, err := s.loadObservationTarget(ctx, groupID, credentialID)
	if err != nil {
		return ResetCreditConsumeResponse{}, err
	}
	network, err := s.credentialNetworkContext(ctx, s.db, group, credential)
	if err != nil {
		return ResetCreditConsumeResponse{}, err
	}
	ctx = subscriptionruntime.WithNetworkContext(ctx, network)
	target, baseURL, err := s.resolveResetCreditTarget(group)
	if err != nil {
		return ResetCreditConsumeResponse{}, err
	}
	digest := resetCreditRequestDigest(groupID, credentialID, credential.IdentityFingerprint, baseURL, kind)
	if replay, found, replayErr := s.replayResetCreditOperationIfExists(
		ctx,
		groupID,
		credentialID,
		digest,
		idempotencyKey,
	); found {
		return replay, replayErr
	}
	dispatch, err := s.prepareResetCreditDispatch(ctx, group, credential, target, kind)
	if err != nil {
		return ResetCreditConsumeResponse{}, err
	}

	operation, replayed, err := s.beginResetCreditOperation(
		ctx,
		groupID,
		credentialID,
		digest,
		idempotencyKey,
	)
	if err != nil {
		return ResetCreditConsumeResponse{}, err
	}
	if replayed {
		return s.replayResetCreditOperation(ctx, groupID, credentialID, operation)
	}

	if dispatch.consume == nil {
		_ = s.finishResetCreditOperation(operation, models.CredentialResetOperationOutcomeUnknown, nil, app_errors.ErrResetCreditOutcomeUnknown.Code)
		return ResetCreditConsumeResponse{}, app_errors.ErrResetCreditOutcomeUnknown
	}
	callContext, cancel := context.WithTimeout(ctx, defaultSubscriptionControlTimeout)
	request, prepareErr := s.prepareResetCreditRequest(callContext, operation, dispatch.prepare)
	if prepareErr != nil {
		cancel()
		state, apiErr := models.CredentialResetOperationRejected, classifyResetCreditPreparationError(prepareErr)
		// 已持久化选卡的操作可能发送过兑换，不能因恢复参数失败而解除保护。
		if len(operation.ResultJSON) > 0 {
			state, apiErr = models.CredentialResetOperationOutcomeUnknown, app_errors.ErrResetCreditOutcomeUnknown
		}
		if err := s.finishResetCreditOperation(operation, state, nil, apiErr.Code); err != nil {
			return ResetCreditConsumeResponse{}, app_errors.ErrResetCreditOutcomeUnknown
		}
		return ResetCreditConsumeResponse{}, apiErr
	}
	upstream, consumeErr := dispatch.consume(callContext, request)
	cancel()
	if consumeErr != nil {
		state, apiErr := classifyResetCreditConsumeError(consumeErr)
		if normalizeGroupConnectionType(group.ConnectionType) == models.ConnectionTypeAPIKey && len(operation.ResultJSON) > 0 {
			// A retry of a previously dispatched card can now be unavailable
			// because the first request succeeded. Never unlock a new card.
			state, apiErr = models.CredentialResetOperationOutcomeUnknown, app_errors.ErrResetCreditOutcomeUnknown
		}
		if persistErr := s.finishResetCreditOperation(operation, state, nil, apiErr.Code); persistErr != nil {
			return ResetCreditConsumeResponse{}, app_errors.ErrResetCreditOutcomeUnknown
		}
		return ResetCreditConsumeResponse{}, apiErr
	}
	if upstream.Status != "succeeded" || upstream.WindowsReset < 0 {
		_ = s.finishResetCreditOperation(operation, models.CredentialResetOperationOutcomeUnknown, nil, app_errors.ErrResetCreditOutcomeUnknown.Code)
		return ResetCreditConsumeResponse{}, app_errors.ErrResetCreditOutcomeUnknown
	}
	result := storedResetCreditResult{
		Status: upstream.Status, WindowsReset: upstream.WindowsReset, RedeemedAtMS: upstream.RedeemedAtMS,
	}
	runtimeRestored, targetCurrent, targetErr := s.restoreCredentialRuntimeForTarget(ctx, group, credentialID)
	canonicalResult, err := canonicaljson.Marshal(result)
	if err != nil || s.finishResetCreditOperation(
		operation,
		models.CredentialResetOperationSucceeded,
		canonicalResult,
		"",
	) != nil {
		return ResetCreditConsumeResponse{}, app_errors.ErrResetCreditOutcomeUnknown
	}
	if !runtimeRestored && (targetCurrent || targetErr != nil) {
		utils.LogPlaneBestEffort(
			logrus.StandardLogger(),
			logrus.WarnLevel,
			utils.LogPlaneControl,
			logrus.Fields{"credential_id": credentialID, "group_id": groupID},
			"Reset credit was consumed but credential runtime health could not be restored",
		)
	}

	response := resetCreditResponse(result, false)
	if !targetCurrent {
		response.ObservationPending = true
		return response, nil
	}
	observationContext, cancelObservation := context.WithTimeout(
		context.Background(),
		2*defaultSubscriptionControlTimeout,
	)
	observation, observationErr := s.refreshCredentialObservationForTarget(
		observationContext,
		groupID,
		credentialID,
		observationRefreshAfterReset,
		&group,
	)
	cancelObservation()
	if observation.State != "" {
		response.Observation = &observation
	}
	response.ObservationPending = observationErr != nil ||
		observation.State != string(models.CredentialObservationFresh) ||
		observation.LastErrorCode == "observation_partial"
	if observationErr != nil {
		utils.LogPlaneBestEffort(
			logrus.StandardLogger(),
			logrus.WarnLevel,
			utils.LogPlaneControl,
			logrus.Fields{"credential_id": credentialID, "group_id": groupID},
			"Reset credit was consumed but the credential observation could not be refreshed",
		)
	}
	return response, nil
}

func (s *Service) invalidateCredentialObservationAfterReset(
	ctx context.Context,
	credential models.Credential,
	previous *models.CredentialObservation,
) (*CredentialObservationResponse, error) {
	if s.registry != nil {
		s.registry.SetCredentialQuotaObservation(credential.ID, nil, time.Time{})
	}
	if previous == nil || previous.CredentialID == 0 || previous.IdentityFingerprint != credential.IdentityFingerprint {
		return nil, nil
	}
	previous.State = models.CredentialObservationStale
	previous.NextAllowedAtMS = nil
	previous.LastErrorCode = ""
	previous.UpdatedAtMS = s.now().UTC().UnixMilli()
	updates := map[string]any{
		"state":              previous.State,
		"next_allowed_at_ms": previous.NextAllowedAtMS,
		"last_error_code":    previous.LastErrorCode,
		"updated_at_ms":      previous.UpdatedAtMS,
	}
	if err := s.upsertCredentialObservationMetadataOnly(ctx, credential.ID, *previous, updates); err != nil {
		return nil, err
	}
	response := mapCredentialObservation(*previous)
	return &response, nil
}

func (s *Service) restoreCredentialRuntimeAfterReset(credentialID uint) bool {
	if credentialID == 0 || s.registry == nil || s.stats == nil {
		return false
	}
	restored := false
	apply := func() {
		if !s.registry.RestoreRuntimeState(credentialID) {
			return
		}
		if !s.registry.ClearModelCooldowns(credentialID) {
			return
		}
		s.stats.ClearProblemState(credentialID)
		restored = true
	}
	if s.mutations == nil {
		apply()
	} else {
		s.mutations.Do(credentialID, apply)
	}
	return restored
}

func (s *Service) beginResetCreditOperation(
	ctx context.Context,
	groupID uint,
	credentialID uint,
	digest [sha256.Size]byte,
	idempotencyKey string,
) (models.CredentialResetOperation, bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	nowMS := s.now().UTC().UnixMilli()

	var existing models.CredentialResetOperation
	err := s.db.WithContext(ctx).Where("idempotency_key = ?", idempotencyKey).Take(&existing).Error
	if err == nil {
		if !bytes.Equal(existing.RequestDigest, digest[:]) ||
			existing.GroupID != groupID || existing.CredentialID != credentialID {
			return models.CredentialResetOperation{}, false, app_errors.ErrIdempotencyKeyReused
		}
		if resetCreditOperationCanRetry(existing, nowMS) {
			// 准备失败后的重试与新操作一样，必须避让其他正在兑换的请求。
			if existing.State == models.CredentialResetOperationRejected && !s.resetCreditOperationAllowsNewKey(ctx, credentialID) {
				return models.CredentialResetOperation{}, false, app_errors.ErrResetCreditOutcomeUnknown
			}
			result := s.db.WithContext(ctx).Model(&models.CredentialResetOperation{}).
				Where("idempotency_key = ? AND state = ? AND updated_at_ms = ?", existing.IdempotencyKey, existing.State, existing.UpdatedAtMS).
				Updates(map[string]any{
					"state":           models.CredentialResetOperationPrepared,
					"error_code":      "",
					"updated_at_ms":   nowMS,
					"completed_at_ms": nil,
				})
			if result.Error != nil {
				return models.CredentialResetOperation{}, false, app_errors.ParseDBError(result.Error)
			}
			if result.RowsAffected != 1 {
				return models.CredentialResetOperation{}, false, app_errors.ErrResetCreditOutcomeUnknown
			}
			existing.State = models.CredentialResetOperationPrepared
			existing.ErrorCode = ""
			existing.UpdatedAtMS = nowMS
			existing.CompletedAtMS = nil
			return existing, false, nil
		}
		replayed, existingErr := classifyExistingResetCreditOperation(existing, digest, groupID, credentialID)
		if existingErr != nil {
			return models.CredentialResetOperation{}, false, existingErr
		}
		return existing, replayed, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return models.CredentialResetOperation{}, false, app_errors.ParseDBError(err)
	}
	if !s.resetCreditOperationAllowsNewKey(ctx, credentialID) {
		return models.CredentialResetOperation{}, false, app_errors.ErrResetCreditOutcomeUnknown
	}
	operation := models.CredentialResetOperation{
		IdempotencyKey: idempotencyKey, RequestDigest: append([]byte(nil), digest[:]...),
		GroupID: groupID, CredentialID: credentialID, RedeemRequestID: idempotencyKey,
		State: models.CredentialResetOperationPrepared, CreatedAtMS: nowMS, UpdatedAtMS: nowMS,
	}
	if err := s.db.WithContext(ctx).Create(&operation).Error; err != nil {
		return models.CredentialResetOperation{}, false, app_errors.ParseDBError(err)
	}
	return operation, false, nil
}

func (s *Service) replayResetCreditOperationIfExists(
	ctx context.Context,
	groupID uint,
	credentialID uint,
	digest [sha256.Size]byte,
	idempotencyKey string,
) (ResetCreditConsumeResponse, bool, error) {
	var existing models.CredentialResetOperation
	err := s.db.WithContext(ctx).Where("idempotency_key = ?", idempotencyKey).Take(&existing).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ResetCreditConsumeResponse{}, false, nil
	}
	if err != nil {
		return ResetCreditConsumeResponse{}, true, app_errors.ParseDBError(err)
	}
	if !bytes.Equal(existing.RequestDigest, digest[:]) ||
		existing.GroupID != groupID || existing.CredentialID != credentialID {
		return ResetCreditConsumeResponse{}, true, app_errors.ErrIdempotencyKeyReused
	}
	if resetCreditOperationCanRetry(existing, s.now().UTC().UnixMilli()) {
		return ResetCreditConsumeResponse{}, false, nil
	}
	replayed, err := classifyExistingResetCreditOperation(existing, digest, groupID, credentialID)
	if err != nil {
		return ResetCreditConsumeResponse{}, true, err
	}
	if !replayed {
		return ResetCreditConsumeResponse{}, true, app_errors.ErrResetCreditOutcomeUnknown
	}
	response, err := s.replayResetCreditOperation(ctx, groupID, credentialID, existing)
	return response, true, err
}

func (s *Service) replayResetCreditOperation(
	ctx context.Context,
	groupID uint,
	credentialID uint,
	operation models.CredentialResetOperation,
) (ResetCreditConsumeResponse, error) {
	result, err := decodeStoredResetCreditResult(operation.ResultJSON)
	if err != nil {
		return ResetCreditConsumeResponse{}, app_errors.ErrResetCreditOutcomeUnknown
	}
	response := resetCreditResponse(result, true)
	observation, observationErr := s.GetCredentialObservation(ctx, groupID, credentialID)
	if observationErr == nil {
		response.Observation = &observation
		response.ObservationPending = observation.State != string(models.CredentialObservationFresh) ||
			observation.LastErrorCode == "observation_partial"
	} else {
		response.ObservationPending = true
	}
	response.ObservationPending = response.ObservationPending ||
		s.credentialObservationRefreshInFlight(groupID, credentialID)
	return response, nil
}

func resetCreditRequestDigest(groupID, credentialID uint, identityFingerprint, baseURL string, resetTypes ...string) [sha256.Size]byte {
	if len(resetTypes) > 0 && resetTypes[0] != "" {
		return sha256.Sum256([]byte(fmt.Sprintf(
			"gpt-load/credential-reset/v3/%d/%d/%s/%s/%s", groupID, credentialID, identityFingerprint, baseURL, resetTypes[0],
		)))
	}
	// 自定义地址启用前的操作只使用官方端点，官方模式保留 v1 摘要以兼容旧记录。
	// 自定义目标使用独立摘要，规范化后的完整路径也参与匹配，禁止跨目标复用。
	if baseURL != "" {
		return sha256.Sum256([]byte(fmt.Sprintf(
			"gpt-load/credential-reset/v2/%d/%d/%s/%s",
			groupID,
			credentialID,
			identityFingerprint,
			baseURL,
		)))
	}
	return sha256.Sum256([]byte(fmt.Sprintf(
		"gpt-load/credential-reset/v1/%d/%d/%s",
		groupID,
		credentialID,
		identityFingerprint,
	)))
}

func resetCreditOperationCanRetry(operation models.CredentialResetOperation, nowMS int64) bool {
	if operation.State == models.CredentialResetOperationRejected && operation.ErrorCode == app_errors.ErrResetCreditPreparationFailed.Code {
		return true
	}
	if operation.State == models.CredentialResetOperationOutcomeUnknown {
		return true
	}
	if operation.State != models.CredentialResetOperationPrepared || nowMS < 0 {
		return false
	}
	retryBeforeMS := nowMS - defaultSubscriptionControlTimeout.Milliseconds()
	return retryBeforeMS >= 0 && operation.UpdatedAtMS <= retryBeforeMS
}

func classifyExistingResetCreditOperation(
	existing models.CredentialResetOperation,
	digest [sha256.Size]byte,
	groupID uint,
	credentialID uint,
) (bool, error) {
	if !bytes.Equal(existing.RequestDigest, digest[:]) ||
		existing.GroupID != groupID || existing.CredentialID != credentialID {
		return false, app_errors.ErrIdempotencyKeyReused
	}
	switch existing.State {
	case models.CredentialResetOperationSucceeded:
		return true, nil
	case models.CredentialResetOperationRejected:
		return false, resetCreditErrorByCode(existing.ErrorCode)
	default:
		return false, app_errors.ErrResetCreditOutcomeUnknown
	}
}

func (s *Service) resetCreditOperationAllowsNewKey(
	ctx context.Context,
	credentialID uint,
) bool {
	var latest models.CredentialResetOperation
	err := s.db.WithContext(ctx).
		Where("credential_id = ? AND state IN ?", credentialID, []models.CredentialResetOperationState{
			models.CredentialResetOperationPrepared,
			models.CredentialResetOperationSucceeded,
			models.CredentialResetOperationOutcomeUnknown,
		}).
		Order("updated_at_ms DESC").
		Order("idempotency_key DESC").
		Take(&latest).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return true
	}
	return err == nil && latest.State == models.CredentialResetOperationSucceeded
}

func (s *Service) finishResetCreditOperation(
	operation models.CredentialResetOperation,
	state models.CredentialResetOperationState,
	result []byte,
	errorCode string,
) error {
	cleanupContext, cancel := context.WithTimeout(context.Background(), controlTransactionCleanupTimeout)
	defer cancel()
	nowMS := s.now().UTC().UnixMilli()
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	updates := map[string]any{
		"state": state, "error_code": errorCode,
		"updated_at_ms": nowMS, "completed_at_ms": nowMS,
	}
	// 结果不确定时保留选卡参数，重试不得改用另一张卡。
	if state != models.CredentialResetOperationOutcomeUnknown || result != nil {
		updates["result_json"] = models.JSON(result)
	}
	resultUpdate := s.db.WithContext(cleanupContext).Model(&models.CredentialResetOperation{}).
		Where("idempotency_key = ? AND state = ? AND updated_at_ms = ?", operation.IdempotencyKey, models.CredentialResetOperationPrepared, operation.UpdatedAtMS).
		Updates(updates)
	if resultUpdate.Error != nil {
		return resultUpdate.Error
	}
	if resultUpdate.RowsAffected != 1 {
		return fmt.Errorf("credential reset operation state changed")
	}
	return nil
}

func (s *Service) prepareResetCreditRequest(
	ctx context.Context,
	operation models.CredentialResetOperation,
	prepare func(context.Context) (json.RawMessage, error),
) (subscriptionruntime.ResetCreditRequest, error) {
	request := subscriptionruntime.ResetCreditRequest{ID: operation.RedeemRequestID}
	if len(operation.ResultJSON) > 0 {
		var stored storedResetCreditRequest
		if json.Unmarshal(operation.ResultJSON, &stored) != nil || len(stored.Request) == 0 {
			return request, errors.New("invalid stored reset credit request")
		}
		request.Data = stored.Request
		return request, nil
	}
	if prepare == nil {
		return request, nil
	}
	data, err := prepare(ctx)
	if err != nil || len(data) == 0 {
		return request, err
	}
	if len(data) > 4096 || !json.Valid(data) {
		return request, errors.New("invalid reset credit request data")
	}
	raw, err := json.Marshal(storedResetCreditRequest{Request: data})
	if err != nil {
		return request, err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	result := s.db.WithContext(ctx).Model(&models.CredentialResetOperation{}).
		Where("idempotency_key = ? AND state = ? AND updated_at_ms = ?", operation.IdempotencyKey, models.CredentialResetOperationPrepared, operation.UpdatedAtMS).
		Updates(map[string]any{"result_json": models.JSON(raw), "updated_at_ms": operation.UpdatedAtMS})
	if result.Error != nil {
		return request, app_errors.ParseDBError(result.Error)
	}
	if result.RowsAffected != 1 {
		return request, errors.New("reset credit operation changed before dispatch")
	}
	request.Data = data
	return request, nil
}

func classifyResetCreditPreparationError(err error) *app_errors.APIError {
	var upstream *subscriptionruntime.UpstreamHTTPError
	if errors.As(err, &upstream) {
		switch upstream.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			return app_errors.ErrCredentialReauthorizationRequired
		case http.StatusConflict:
			return app_errors.ErrResetCreditUnavailable
		}
	}
	return app_errors.ErrResetCreditPreparationFailed
}

func classifyResetCreditConsumeError(err error) (models.CredentialResetOperationState, *app_errors.APIError) {
	var upstream *subscriptionruntime.UpstreamHTTPError
	if !errors.As(err, &upstream) || upstream.StatusCode >= http.StatusInternalServerError {
		return models.CredentialResetOperationOutcomeUnknown, app_errors.ErrResetCreditOutcomeUnknown
	}
	switch upstream.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return models.CredentialResetOperationRejected, app_errors.ErrCredentialReauthorizationRequired
	case http.StatusConflict, http.StatusNotFound:
		return models.CredentialResetOperationRejected, app_errors.ErrResetCreditUnavailable
	default:
		return models.CredentialResetOperationRejected, app_errors.ErrResetCreditRejected
	}
}

func resetCreditErrorByCode(code string) *app_errors.APIError {
	switch code {
	case app_errors.ErrResetCreditPreparationFailed.Code:
		return app_errors.ErrResetCreditPreparationFailed
	case app_errors.ErrCredentialReauthorizationRequired.Code:
		return app_errors.ErrCredentialReauthorizationRequired
	case app_errors.ErrResetCreditUnavailable.Code:
		return app_errors.ErrResetCreditUnavailable
	case app_errors.ErrResetCreditRejected.Code:
		return app_errors.ErrResetCreditRejected
	default:
		return app_errors.ErrResetCreditOutcomeUnknown
	}
}

func decodeStoredResetCreditResult(raw []byte) (storedResetCreditResult, error) {
	var result storedResetCreditResult
	if json.Unmarshal(raw, &result) != nil || result.Status != "succeeded" || result.WindowsReset < 0 {
		return storedResetCreditResult{}, errors.New("invalid stored reset credit result")
	}
	return result, nil
}

func resetCreditResponse(result storedResetCreditResult, replayed bool) ResetCreditConsumeResponse {
	return ResetCreditConsumeResponse{
		Status: result.Status, WindowsReset: result.WindowsReset,
		RedeemedAtMS: result.RedeemedAtMS, Replayed: replayed,
	}
}
