package control

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"gpt-load/internal/accessquota"
	app_errors "gpt-load/internal/platform/errors"
	"gpt-load/internal/platform/response"
	"gpt-load/internal/pricing"
	"gpt-load/internal/state"
	"gpt-load/internal/storage/models"
)

const forkQuotaImportKind = "fork_key_quota_import"

var errForkQuotaImportConflict = &app_errors.APIError{
	HTTPStatus: http.StatusConflict, Code: "QUOTA_IMPORT_CONFLICT",
	Message: "Quota import requires a disabled, drained target with no existing quota rules; a previous import may only be replayed unchanged",
}

type ForkKeyQuotaImportRule struct {
	Kind              accessquota.Kind `json:"kind"`
	LimitUSD          string           `json:"limit_usd"`
	PeriodSeconds     int64            `json:"period_seconds"`
	UsedUSD           string           `json:"used_usd"`
	WindowStartedAtMS *int64           `json:"window_started_at_ms"`
	WindowEndsAtMS    *int64           `json:"window_ends_at_ms"`
}

type ForkKeyQuotaImportRequest struct {
	Rules []ForkKeyQuotaImportRule `json:"rules"`
}

type ForkKeyQuotaImportResult struct {
	AccessKeyID uint                     `json:"access_key_id"`
	Status      state.AccessKeyStatus    `json:"status"`
	Fingerprint string                   `json:"fingerprint"`
	Replayed    bool                     `json:"replayed"`
	Quota       AccessKeyCostLimitStatus `json:"quota"`
}

func (s *Server) handleForkKeyQuotaImport(c *gin.Context) {
	id, ok := accessKeyID(c)
	if !ok {
		return
	}
	var request ForkKeyQuotaImportRequest
	if err := bindStrictJSON(c, &request); err != nil {
		writeServiceError(c, "fork_key_quota_import", mapControlJSONError(err))
		return
	}
	result, err := s.service.ImportForkKeyQuota(c.Request.Context(), id, request)
	if err != nil {
		writeServiceError(c, "fork_key_quota_import", err)
		return
	}
	response.SuccessI18n(c, "common.success", result)
}

// ImportForkKeyQuota is intentionally not a general quota setter. Only an empty,
// disabled native target can receive a one-shot migration. Its terminal receipt
// shares the existing operation table; no pending operation/recovery stages are
// introduced. The digest survives normal operation-result compaction.
func (s *Service) ImportForkKeyQuota(ctx context.Context, id uint, request ForkKeyQuotaImportRequest) (ForkKeyQuotaImportResult, error) {
	if id == 0 {
		return ForkKeyQuotaImportResult{}, app_errors.ErrBadRequest
	}
	rules, err := normalizeForkQuotaImport(request, s.now().UnixMilli())
	if err != nil {
		return ForkKeyQuotaImportResult{}, err
	}
	body, err := json.Marshal(ForkKeyQuotaImportRequest{Rules: rules})
	if err != nil {
		return ForkKeyQuotaImportResult{}, app_errors.ErrInternalServer
	}
	digest := sha256.Sum256(body)
	// A target-scoped natural idempotency key forbids a second import even
	// when the caller changes or omits an HTTP Idempotency-Key.
	identity := fmt.Sprintf("access-key:%d", id)
	receiptKey := forkQuotaReceiptKey(identity)
	result := ForkKeyQuotaImportResult{AccessKeyID: id, Status: state.AccessKeyStatusDisabled, Fingerprint: hex.EncodeToString(digest[:])}
	var install func() error
	_, err = s.writeConfig(ctx, func(tx *gorm.DB) error {
		if s.manager == nil || s.accessQuota == nil {
			return app_errors.ErrInternalServer
		}
		var key models.AccessKey
		if err := tx.Select("id", "status").First(&key, id).Error; err != nil {
			return app_errors.ParseDBError(err)
		}
		// writeConfig's writeMu excludes enable/update mutations throughout DB
		// commit, install, and publication. Snapshot-coordinated quota admissions
		// reject stale snapshots after disable; already admitted requests must drain.
		snapshot := s.manager.Current()
		if key.Status != string(state.AccessKeyStatusDisabled) || snapshot == nil {
			return errForkQuotaImportConflict
		}
		view, exists := snapshot.AccessKeysByID[id]
		if !exists || view.Status != state.AccessKeyStatusDisabled || s.manager.Concurrency().AccessKeyCurrent(id) != 0 {
			return errForkQuotaImportConflict
		}
		var receipt models.ControlOperation
		err := tx.Where("idempotency_key = ?", receiptKey).Take(&receipt).Error
		if err == nil {
			if receipt.OperationKind != forkQuotaImportKind || receipt.ResourceIdentity != identity ||
				!bytes.Equal(receipt.RequestDigest, digest[:]) {
				return errForkQuotaImportConflict
			}
			result.Replayed = true
			return nil // Never reapply counters, including after periods expire.
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return app_errors.ParseDBError(err)
		}
		var count int64
		if err := tx.Model(&models.AccessKeyCostLimitRule{}).Where("access_key_id = ?", id).Count(&count).Error; err != nil {
			return app_errors.ParseDBError(err)
		}
		if count != 0 || len(view.CostLimitRules) != 0 {
			return errForkQuotaImportConflict
		}
		definitions := make([]normalizedAccessKeyCostLimitRule, 0, len(rules))
		for _, input := range rules {
			limit, _ := pricing.ParseUSD(input.LimitUSD)
			definitions = append(definitions, normalizedAccessKeyCostLimitRule{Kind: input.Kind, LimitNanoUSD: int64(limit), PeriodSeconds: input.PeriodSeconds})
		}
		created, err := createAccessKeyCostLimitRules(tx, id, definitions)
		if err != nil {
			return err
		}
		nativeRules := make([]accessquota.Rule, 0, len(created))
		states := make([]accessquota.RestoredState, 0, len(created))
		for index, row := range created {
			input := rules[index]
			used, _ := pricing.ParseUSD(input.UsedUSD)
			generation := uint64(0)
			if input.WindowStartedAtMS != nil {
				generation = 1
			}
			checkpoint := models.AccessKeyCostLimitState{
				RuleID: row.ID, RuleRevision: row.RuleRevision, UsedNanoUSD: int64(used),
				WindowStartedAtMS: input.WindowStartedAtMS, WindowEndsAtMS: input.WindowEndsAtMS,
				WindowGeneration: generation, SnapshotVersion: 1,
			}
			if err := tx.Save(&checkpoint).Error; err != nil {
				return app_errors.ParseDBError(err)
			}
			nativeRules = append(nativeRules, accessquota.Rule{ID: row.ID, Revision: row.RuleRevision, Kind: input.Kind, LimitNanoUSD: row.LimitNanoUSD, PeriodSeconds: row.PeriodSeconds})
			states = append(states, accessquota.RestoredState{
				AccessKeyID: id, RuleID: row.ID, RuleRevision: row.RuleRevision, UsedNanoUSD: checkpoint.UsedNanoUSD,
				WindowStartedAtMS: input.WindowStartedAtMS, WindowEndsAtMS: input.WindowEndsAtMS,
				WindowGeneration: generation, SnapshotVersion: 1,
			})
		}
		install, err = s.accessQuota.PrepareFreshImport(id, nativeRules, states)
		if err != nil {
			return errForkQuotaImportConflict
		}
		nowMS := s.now().UnixMilli()
		receipt = models.ControlOperation{
			OperationID: receiptKey, IdempotencyKey: receiptKey, DigestVersion: 1, RequestDigest: digest[:],
			OperationKind: forkQuotaImportKind, ResourceIdentity: identity, CompletedAtMS: &nowMS,
		}
		if err := tx.Create(&receipt).Error; err != nil {
			return app_errors.ParseDBError(err)
		}
		return nil
	}, func() error {
		if install != nil {
			return install()
		}
		return nil
	})
	if err != nil {
		return ForkKeyQuotaImportResult{}, err
	}
	result.Quota = mapAccessKeyCostLimitStatus(s.accessQuota.Snapshot(id, s.now()))
	return result, nil
}

func normalizeForkQuotaImport(request ForkKeyQuotaImportRequest, nowMS int64) ([]ForkKeyQuotaImportRule, error) {
	if len(request.Rules) == 0 || len(request.Rules) > accessquota.MaxPeriodicRules+1 {
		return nil, app_errors.ErrValidation
	}
	rules := append([]ForkKeyQuotaImportRule(nil), request.Rules...)
	seen := make(map[int64]bool)
	periodicCount := 0
	for index := range rules {
		rule := &rules[index]
		limit, err := pricing.ParseUSD(rule.LimitUSD)
		if err != nil || limit <= 0 {
			return nil, app_errors.ErrValidation
		}
		used, err := pricing.ParseUSD(rule.UsedUSD)
		if err != nil { // Usage above the limit is valid and must not be clamped.
			return nil, app_errors.ErrValidation
		}
		rule.LimitUSD, rule.UsedUSD = pricing.FormatUSD(limit), pricing.FormatUSD(used)
		switch rule.Kind {
		case accessquota.KindTotal:
			if rule.PeriodSeconds != 0 || rule.WindowStartedAtMS != nil || rule.WindowEndsAtMS != nil {
				return nil, app_errors.ErrValidation
			}
		case accessquota.KindPeriodic:
			periodicCount++
			if rule.PeriodSeconds < accessquota.MinPeriodSeconds || rule.PeriodSeconds > accessquota.MaxPeriodSeconds || periodicCount > accessquota.MaxPeriodicRules {
				return nil, app_errors.ErrValidation
			}
			if (rule.WindowStartedAtMS == nil) != (rule.WindowEndsAtMS == nil) {
				return nil, app_errors.ErrValidation
			}
			if rule.WindowStartedAtMS == nil {
				if used != 0 {
					return nil, app_errors.ErrValidation
				}
			} else {
				start, end := *rule.WindowStartedAtMS, *rule.WindowEndsAtMS
				if start < 0 || start > nowMS || end > 9_007_199_254_740_991 || end <= start || end-start != rule.PeriodSeconds*1000 {
					return nil, app_errors.ErrValidation
				}
				rule.WindowStartedAtMS, rule.WindowEndsAtMS = &start, &end
			}
		default:
			return nil, app_errors.ErrValidation
		}
		if seen[rule.PeriodSeconds] { // total has period zero; periodic periods are positive.
			return nil, app_errors.ErrValidation
		}
		seen[rule.PeriodSeconds] = true
	}
	sort.Slice(rules, func(i, j int) bool { return rules[i].PeriodSeconds < rules[j].PeriodSeconds })
	return rules, nil
}

func forkQuotaReceiptKey(identity string) string {
	value := sha256.Sum256([]byte("gpt-load/fork-quota-import/v1/" + identity))
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	encoded := hex.EncodeToString(value[:16])
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:]
}
