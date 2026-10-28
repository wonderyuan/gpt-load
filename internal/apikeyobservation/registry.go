// Package apikeyobservation binds quota and narrow reset-credit capabilities
// without adding subscription authorization, refresh, or execution to API-key channels.
package apikeyobservation

import (
	"context"
	"encoding/json"
	"fmt"

	"gpt-load/internal/channel"
	"gpt-load/internal/channel/spec"
	subscriptionruntime "gpt-load/internal/subscription/runtime"
)

type Request struct {
	Credential channel.Credential
	Target     channel.ResolvedTarget
}

type Observer interface {
	ID() spec.UtilityID
	SupportsTarget(channel.ResolvedTarget) bool
	Observe(context.Context, Request) (subscriptionruntime.Observation, error)
}

// ResetCreditAction is independent of OAuth/subscription drivers. Prepare returns
// only non-secret selection data; the caller must persist it before Consume.
type ResetCreditAction interface {
	ResetTypes() []string
	PrepareResetCredit(context.Context, Request, string) (json.RawMessage, error)
	ConsumeResetCredit(context.Context, Request, subscriptionruntime.ResetCreditRequest) (subscriptionruntime.ResetCreditResult, error)
}

func (r *Registry) ResetCreditAction(target channel.ResolvedTarget) (ResetCreditAction, bool) {
	observer, ok := r.Resolve(target)
	if !ok {
		return nil, false
	}
	action, ok := observer.(ResetCreditAction)
	return action, ok
}

type Registry struct {
	byChannel map[channel.ID]Observer
}

func NewRegistry(channels *channel.Registry, observers ...Observer) (*Registry, error) {
	if channels == nil {
		return nil, fmt.Errorf("compile API-key observations: channel registry is unavailable")
	}
	byID := make(map[spec.UtilityID]Observer, len(observers))
	for _, observer := range observers {
		if observer == nil || observer.ID() == "" {
			return nil, fmt.Errorf("compile API-key observations: invalid observer")
		}
		if _, exists := byID[observer.ID()]; exists {
			return nil, fmt.Errorf("compile API-key observations: duplicate observer %q", observer.ID())
		}
		byID[observer.ID()] = observer
	}
	result := &Registry{byChannel: make(map[channel.ID]Observer)}
	for _, descriptor := range channels.List() {
		if descriptor.Connection.Type != string(spec.ConnectionAPIKey) {
			continue
		}
		bindings, _ := channels.CapabilityBindings(descriptor.ID)
		if bindings.QuotaObservation == "" {
			continue
		}
		observer, ok := byID[bindings.QuotaObservation]
		if !ok {
			return nil, fmt.Errorf("compile API-key observations: channel %q references unknown observer %q", descriptor.ID, bindings.QuotaObservation)
		}
		result.byChannel[descriptor.ID] = observer
	}
	return result, nil
}

func (r *Registry) Resolve(target channel.ResolvedTarget) (Observer, bool) {
	if r == nil {
		return nil, false
	}
	observer, ok := r.byChannel[target.ChannelID]
	return observer, ok && observer.SupportsTarget(target)
}
