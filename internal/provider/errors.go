package provider

import "errors"

// Keep validation errors static so callers can use errors.Is while the
// surrounding identifiers remain in the human-readable message.
var (
	errCatalogVersion         = errors.New("provider catalog version")
	errTrailingJSON           = errors.New("trailing JSON")
	errUnsupportedVersion     = errors.New("unsupported")
	errInvalidID              = errors.New("invalid id")
	errDuplicateProvider      = errors.New("provider already exists")
	errProviderFieldsRequired = errors.New("name, api and baseUrl required")
	errModelIDRequired        = errors.New("model id required")
	errDuplicateModel         = errors.New("duplicate model")
	errOverrideModelMissing   = errors.New("override model not found")
	errAtLeastOneModel        = errors.New("at least one model required")
	errNameRequired           = errors.New("name required")
	errInvalidAPI             = errors.New("invalid api")
	errInvalidBaseURL         = errors.New("invalid baseUrl")
	errTokenLimitsPositive    = errors.New("token limits must be positive")
	errTextInputRequired      = errors.New("input must include text")
	errInvalidPatchToolType   = errors.New("invalid applyPatchToolType")
	errFreeformResponsesAPI   = errors.New("freeform apply_patch requires responses api")
	errInvalidThinkingLevel   = errors.New("invalid thinking level")
	errCostTierOrder          = errors.New("cost tiers must have increasing thresholds")
	errNegativeCostRates      = errors.New("cost rates must not be negative")
	errUnavailable            = errors.New("unavailable")
	errNoAPIKey               = errors.New("has no configured credential")
	errProviderNotFound       = errors.New("provider not found")
	errAPIKeyRequired         = errors.New("apiKey must not be empty")
	errCredentialType         = errors.New("credential type does not match provider auth")
	errInvalidThinkingEffort  = errors.New("invalid thinking effort")
	errNoThinkingEffort       = errors.New("model has no supported thinking effort")
)
