package control

import (
	"context"
	"encoding/json"

	"gpt-load/internal/apikeyobservation"
	"gpt-load/internal/channel"
	app_errors "gpt-load/internal/platform/errors"
	"gpt-load/internal/storage/models"
	subscriptionruntime "gpt-load/internal/subscription/runtime"
)

type resetCreditDispatch struct {
	prepare func(context.Context) (json.RawMessage, error)
	consume func(context.Context, subscriptionruntime.ResetCreditRequest) (subscriptionruntime.ResetCreditResult, error)
}

func (s *Service) resolveResetCreditTarget(group models.Group) (subscriptionruntime.Target, string, error) {
	if normalizeGroupConnectionType(group.ConnectionType) == models.ConnectionTypeAPIKey {
		target, err := s.channelRegistry.Resolve(channel.ID(group.ChannelID), json.RawMessage(group.Params))
		if err != nil {
			return subscriptionruntime.Target{}, "", app_errors.ErrValidation
		}
		if _, ok := s.apiKeyObservations.ResetCreditAction(target); !ok {
			return subscriptionruntime.Target{}, "", app_errors.ErrValidation
		}
		// Resolve has canonicalized the full target. Include it in the digest,
		// independently of the provider's fixed list/use endpoints.
		return subscriptionruntime.Target{}, string(target.TargetConfig), nil
	}
	target, err := s.resolveSubscriptionTarget(channel.ID(group.ChannelID), group.Params)
	if err != nil {
		return target, "", app_errors.ErrInternalServer
	}
	baseURL, err := target.BaseURL()
	if err != nil {
		return target, "", app_errors.ErrInternalServer
	}
	return target, baseURL, nil
}

func (s *Service) prepareResetCreditDispatch(ctx context.Context, group models.Group, credential models.Credential, target subscriptionruntime.Target, kind string) (resetCreditDispatch, error) {
	id := channel.ID(group.ChannelID)
	if normalizeGroupConnectionType(group.ConnectionType) != models.ConnectionTypeAPIKey {
		// Typed resets are API-key-only; legacy subscription calls remain {}.
		if kind != "" {
			return resetCreditDispatch{}, app_errors.ErrValidation
		}
		if _, ok := s.subscriptions.ResetCreditAction(id); !ok {
			return resetCreditDispatch{}, app_errors.ErrValidation
		}
		prepared, err := s.prepareStoredSubscriptionCredential(ctx, group, credential)
		if err != nil {
			return resetCreditDispatch{}, err
		}
		dispatch := resetCreditDispatch{}
		if s.prepareSubscriptionResetCredit != nil {
			dispatch.prepare = func(ctx context.Context) (json.RawMessage, error) {
				return s.prepareSubscriptionResetCredit(ctx, id, prepared, target)
			}
		}
		if s.consumeSubscriptionResetCredit != nil {
			dispatch.consume = func(ctx context.Context, request subscriptionruntime.ResetCreditRequest) (subscriptionruntime.ResetCreditResult, error) {
				return s.consumeSubscriptionResetCredit(ctx, id, prepared, target, request)
			}
		}
		return dispatch, nil
	}

	resolved, err := s.channelRegistry.Resolve(id, json.RawMessage(group.Params))
	if err != nil {
		return resetCreditDispatch{}, app_errors.ErrValidation
	}
	action, ok := s.apiKeyObservations.ResetCreditAction(resolved)
	if !ok {
		return resetCreditDispatch{}, app_errors.ErrValidation
	}
	if kind == "" {
		kind = "five_hour"
	}
	validKind := false
	for _, allowed := range action.ResetTypes() {
		validKind = validKind || kind == allowed
	}
	if !validKind {
		return resetCreditDispatch{}, app_errors.ErrValidation
	}
	canonical, _, err := s.decodeCredential(group, credential)
	if err != nil {
		return resetCreditDispatch{}, err
	}
	defer clear(canonical)
	validated, err := s.channelRegistry.ValidateCredential(id, canonical)
	if err != nil {
		return resetCreditDispatch{}, app_errors.ErrValidation
	}
	request := apikeyobservation.Request{Credential: validated, Target: resolved}
	return resetCreditDispatch{
		prepare: func(ctx context.Context) (json.RawMessage, error) {
			return action.PrepareResetCredit(ctx, request, kind)
		},
		consume: func(ctx context.Context, reset subscriptionruntime.ResetCreditRequest) (subscriptionruntime.ResetCreditResult, error) {
			return action.ConsumeResetCredit(ctx, request, reset)
		},
	}, nil
}

// Capability is based on the actual group target, never merely its channel name.
func (s *Service) credentialResetTypes(group models.Group) (bool, []string) {
	if normalizeGroupConnectionType(group.ConnectionType) == models.ConnectionTypeSubscription {
		_, ok := s.subscriptions.ResetCreditAction(channel.ID(group.ChannelID))
		return ok, []string{}
	}
	if normalizeGroupConnectionType(group.ConnectionType) != models.ConnectionTypeAPIKey || s.channelRegistry == nil {
		return false, []string{}
	}
	target, err := s.channelRegistry.Resolve(channel.ID(group.ChannelID), json.RawMessage(group.Params))
	if err != nil {
		return false, []string{}
	}
	action, ok := s.apiKeyObservations.ResetCreditAction(target)
	if !ok {
		return false, []string{}
	}
	return true, append([]string{}, action.ResetTypes()...)
}
