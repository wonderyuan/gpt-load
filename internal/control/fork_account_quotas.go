package control

import (
	"context"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"gpt-load/internal/channel"
	app_errors "gpt-load/internal/platform/errors"
	"gpt-load/internal/platform/response"
	"gpt-load/internal/state"
	"gpt-load/internal/storage/models"
)

type forkAccountQuotaQuery struct {
	Page     int
	PageSize int
	GroupID  uint
}

type ForkAccountQuotaItem struct {
	GroupID        uint                       `json:"group_id"`
	GroupName      string                     `json:"group_name"`
	ChannelID      string                     `json:"channel_id"`
	ChannelName    string                     `json:"channel_name"`
	QuotaSupported bool                       `json:"quota_supported"`
	ResetSupported bool                       `json:"reset_supported"`
	ResetTypes     []string                   `json:"reset_types"`
	Credential     CredentialItemResponse     `json:"credential"`
	CycleEstimates []ForkAccountCycleEstimate `json:"cycle_estimates"`
}

type ForkAccountQuotasResponse struct {
	ObservedAtMS int64                  `json:"observed_at_ms"`
	Items        []ForkAccountQuotaItem `json:"items"`
	Total        int64                  `json:"total"`
	Page         int                    `json:"page"`
	PageSize     int                    `json:"page_size"`
}

func parseForkAccountQuotaQuery(raw string) (forkAccountQuotaQuery, error) {
	query := forkAccountQuotaQuery{Page: 1, PageSize: 50}
	values, err := url.ParseQuery(raw)
	if err != nil {
		return query, app_errors.ErrBadRequest
	}
	for key, entries := range values {
		if len(entries) != 1 {
			return query, app_errors.ErrBadRequest
		}
		switch key {
		case "page", "page_size":
			value, err := parseCanonicalSafeUint(entries[0])
			if err != nil || value == 0 || value > 1_000_000 || key == "page_size" && value > 100 {
				return query, app_errors.ErrBadRequest
			}
			if key == "page" {
				query.Page = int(value)
			} else {
				query.PageSize = int(value)
			}
		case "group_id":
			query.GroupID, err = parseCanonicalSafePlatformUint(entries[0])
			if err != nil || query.GroupID == 0 {
				return query, app_errors.ErrBadRequest
			}
		default:
			return query, app_errors.ErrBadRequest
		}
	}
	return query, nil
}

func (s *Server) handleForkAccountQuotas(c *gin.Context) {
	query, err := parseForkAccountQuotaQuery(c.Request.URL.RawQuery)
	if err != nil {
		writeServiceError(c, "fork_account_quotas", err)
		return
	}
	result, err := s.service.readForkAccountQuotas(c.Request.Context(), query)
	if err != nil {
		writeServiceError(c, "fork_account_quotas", err)
		return
	}
	if _, shared := currentAccessKeyID(c); shared {
		result = maskSharedForkAccountQuotas(result)
	}
	c.Header("Cache-Control", "no-store")
	response.SuccessI18n(c, "common.success", result)
}

// Admin account summaries can contain identifying text outside the email field
// (for example an email-valued display name). Shared reads need quotas and
// masked labels only; detach the redaction path and leave the admin DTO intact.
func maskSharedForkAccountQuotas(current ForkAccountQuotasResponse) ForkAccountQuotasResponse {
	result := current
	result.Items = append([]ForkAccountQuotaItem{}, current.Items...)
	for index := range result.Items {
		credential := &result.Items[index].Credential
		if strings.Contains(credential.Name, "@") {
			credential.Name = maskEmail(credential.Name)
		}
		credential.Account.Email = ""
		credential.Observation = maskForkAccountQuotaObservation(credential.Observation)
		if credential.Observation != nil && credential.Observation.Snapshot != nil {
			credential.Observation.Snapshot.Account = nil
		}
	}
	return result
}

// This read-only projection never contacts upstreams or changes the existing
// activity-ranked homepage contract. Rows represent credential memberships.
func (s *Service) readForkAccountQuotas(ctx context.Context, query forkAccountQuotaQuery) (ForkAccountQuotasResponse, error) {
	if s == nil || s.db == nil || s.registrySnapshot == nil || s.stats == nil || s.now == nil || s.channelRegistry == nil || s.encryption == nil {
		return ForkAccountQuotasResponse{}, app_errors.ErrInternalServer
	}
	if query.Page < 1 || query.Page > 1_000_000 || query.PageSize < 1 || query.PageSize > 100 {
		return ForkAccountQuotasResponse{}, app_errors.ErrBadRequest
	}
	if err := ctx.Err(); err != nil {
		return ForkAccountQuotasResponse{}, err
	}
	s.writeMu.RLock()
	defer s.writeMu.RUnlock()
	observedAt := s.now().UTC()
	observedAtMS, err := safeEpochMilliseconds(observedAt)
	if err != nil {
		return ForkAccountQuotasResponse{}, err
	}
	result := ForkAccountQuotasResponse{ObservedAtMS: observedAtMS, Items: []ForkAccountQuotaItem{}, Page: query.Page, PageSize: query.PageSize}
	var rows []models.Credential
	var observations []models.CredentialObservation
	err = s.withReadSnapshot(ctx, func(tx *gorm.DB) error {
		scope := tx.Model(&models.Credential{})
		if query.GroupID != 0 {
			scope = scope.Where("group_id = ?", query.GroupID)
		}
		if err := scope.Count(&result.Total).Error; err != nil {
			return err
		}
		offset := int64(query.Page-1) * int64(query.PageSize)
		if offset >= result.Total {
			return nil
		}
		if err := scope.Preload("Group").Order("group_id ASC, id ASC").Offset(int(offset)).Limit(query.PageSize).Find(&rows).Error; err != nil {
			return err
		}
		return tx.Where("credential_id IN ?", credentialIDs(rows)).Find(&observations).Error
	})
	if err != nil {
		return ForkAccountQuotasResponse{}, app_errors.ParseDBError(err)
	}
	views := make(map[uint]state.CredentialRuntimeView)
	for _, view := range s.registrySnapshot() {
		views[view.ID] = view
	}
	byCredential := make(map[uint]models.CredentialObservation, len(observations))
	for _, observation := range observations {
		byCredential[observation.CredentialID] = observation
	}
	for _, row := range rows {
		if row.Group == nil {
			return ForkAccountQuotasResponse{}, app_errors.ErrInternalServer
		}
		group := *row.Group
		view, exists := views[row.ID]
		if !exists || view.GroupID != group.ID || view.Version != groupCollectionCredentialVersion(row.SecretVersion) ||
			view.IdentityGeneration != groupCollectionCredentialIdentity(row.IdentityFingerprint, group) ||
			view.Status != state.CredentialStatus(row.Status) || view.AuthState != normalizeRuntimeCredentialAuthState(row.AuthState) {
			return ForkAccountQuotasResponse{}, dbRegistryMismatch(mismatchIdentity, group.ID, row.ID)
		}
		canonical, identity, err := s.decodeCredential(group, row)
		if err != nil {
			return ForkAccountQuotasResponse{}, err
		}
		mask, account, presentationErr := s.credentialPresentation(group, row, canonical, identity)
		clear(canonical)
		identity = ""
		if presentationErr != nil {
			return ForkAccountQuotasResponse{}, presentationErr
		}
		account.Email = ""
		catalog := state.GroupCatalogView{ID: group.ID, Name: group.Name, Enabled: group.Enabled, WeightManual: cloneInt(group.WeightManual)}
		item, err := mapCredentialRuntimeItem(mask, row.ID, view, classifyHealthKey(catalog, view, observedAt), s.stats.Snapshot(row.ID, observedAt), observedAt)
		if err != nil {
			return ForkAccountQuotasResponse{}, err
		}
		item.Name = row.Name
		item.ConnectionType = string(normalizeGroupConnectionType(group.ConnectionType))
		item.SecretVersion = row.SecretVersion
		item.AuthState = string(normalizeRuntimeCredentialAuthState(row.AuthState))
		item.AuthErrorCode = safeInternalErrorCode(row.AuthErrorCode)
		item.Account = account
		supported := s.supportsCredentialObservation(group)
		if supported {
			item.Observation = maskForkAccountQuotaObservation(withCredentialPlan(presentCredentialObservation(byCredential[row.ID], row.IdentityFingerprint), account.PlanType))
		}
		descriptor, exists := s.channelRegistry.Get(channel.ID(group.ChannelID))
		if !exists {
			return ForkAccountQuotasResponse{}, app_errors.ErrInternalServer
		}
		resetSupported, resetTypes := s.credentialResetTypes(group)
		result.Items = append(result.Items, ForkAccountQuotaItem{
			GroupID: group.ID, GroupName: group.Name, ChannelID: group.ChannelID,
			ChannelName: descriptor.Name, QuotaSupported: supported, ResetSupported: resetSupported, ResetTypes: resetTypes, Credential: item,
			CycleEstimates: s.forkAccountCycleEstimates(ctx, group, row.ID, item.Observation, observedAtMS),
		})
	}
	return result, nil
}

// Detach every pointer on the path we redact. This projection must not mutate
// a shared observation or change the original credential-detail contract.
func maskForkAccountQuotaObservation(current *CredentialObservationResponse) *CredentialObservationResponse {
	if current == nil {
		return nil
	}
	result := *current
	if current.Snapshot != nil {
		snapshot := *current.Snapshot
		if current.Snapshot.Account != nil {
			account := *current.Snapshot.Account
			account.Email = ""
			snapshot.Account = &account
		}
		result.Snapshot = &snapshot
	}
	return &result
}
