package config

import (
	"errors"
	"strings"
)

// SyncSetSettings owns the message and lifecycle policy for one routing mesh.
type SyncSetSettings struct {
	MembershipReviewEnabled bool                    `json:"membershipReviewEnabled"`
	PrivateDMRelayEnabled   bool                    `json:"privateDMRelayEnabled"`
	UsernameMode            UsernameMode            `json:"usernameMode"`
	ShowDiscriminator       bool                    `json:"showDiscriminator"`
	CodeLength              int                     `json:"codeLength"`
	ChildContextDisplayMode ChildContextDisplayMode `json:"childContextDisplayMode"`
	Media                   Media                   `json:"media"`
	Recovery                Recovery                `json:"recovery"`
	Storage                 Storage                 `json:"storage"`
	Polls                   Polls                   `json:"polls"`
	WhatsAppCleanup         WhatsAppCleanup         `json:"whatsappCleanup"`
	LocalPrefix             string                  `json:"localPrefix"`
}

func DefaultSyncSetSettings() SyncSetSettings {
	return SyncSetSettings{
		UsernameMode:            UsernameModePushName,
		CodeLength:              8,
		ChildContextDisplayMode: ChildContextDisplayOpaque,
		Media:                   Media{Enabled: true, MaxSizeMB: 100},
		Recovery:                Recovery{Enabled: true, MaxAgeHours: 24, MaxMessagesPerGroup: 200},
		Storage:                 Storage{MessageRetentionDays: 90},
		Polls:                   Polls{AggregationTrigger: "aggregate-response"},
		WhatsAppCleanup:         WhatsAppCleanup{RetentionDays: 30},
	}
}

// LegacySettings is used only to import settings predating sync-set ownership.
func (c *Config) LegacySettings() SyncSetSettings {
	copy := *c
	applyDefaults(&copy)
	return SyncSetSettings{
		UsernameMode:            copy.Identity.UsernameMode,
		ShowDiscriminator:       copy.ShowDiscriminator,
		CodeLength:              5,
		ChildContextDisplayMode: copy.ChildContextDisplayMode,
		Media:                   copy.Media,
		Recovery:                copy.Recovery,
		Storage:                 copy.Storage,
		Polls:                   copy.Polls,
		WhatsAppCleanup:         copy.WhatsAppCleanup,
		LocalPrefix:             copy.LocalPrefix,
	}
}

func (c *Config) SettingsForSet(set SyncSet) SyncSetSettings {
	if set.Settings != nil {
		return *set.Settings
	}
	return c.LegacySettings()
}

func (c *Config) SettingsForEndpoint(alias string) SyncSetSettings {
	for _, set := range c.SyncSets {
		for _, endpoint := range set.Endpoints {
			if endpoint == alias {
				return c.SettingsForSet(set)
			}
		}
	}
	return DefaultSyncSetSettings()
}

func (c *Config) EndpointSettings(connectionID string) map[string]SyncSetSettings {
	result := make(map[string]SyncSetSettings)
	for alias, endpoint := range c.Endpoints {
		if endpoint.ConnectionID == connectionID {
			result[alias] = c.SettingsForEndpoint(alias)
		}
	}
	return result
}

func (c *Config) PrivateDMConnectionEnabled(connectionID string) bool {
	if !c.PrivateDMRelayEnabled {
		return false
	}
	for alias, endpoint := range c.Endpoints {
		if endpoint.ConnectionID == connectionID && c.SettingsForEndpoint(alias).PrivateDMRelayEnabled {
			return true
		}
	}
	return false
}

func (s SyncSetSettings) Validate() error {
	if !s.UsernameMode.IsValid() {
		return errors.New("usernameMode must be push_name or hash")
	}
	if s.CodeLength < 5 || s.CodeLength > 10 {
		return errors.New("codeLength must be between 5 and 10")
	}
	if !s.ChildContextDisplayMode.IsValid() {
		return errors.New("childContextDisplayMode must be opaque or friendly")
	}
	if s.Media.MaxSizeMB < 1 {
		return errors.New("media.maxSizeMB must be positive")
	}
	if s.Recovery.MaxAgeHours < 1 {
		return errors.New("recovery.maxAgeHours must be positive")
	}
	if s.Recovery.MaxMessagesPerGroup < 1 {
		return errors.New("recovery.maxMessagesPerGroup must be positive")
	}
	if s.Storage.MessageRetentionDays < 1 {
		return errors.New("storage.messageRetentionDays must be positive")
	}
	if s.WhatsAppCleanup.RetentionDays < 1 {
		return errors.New("whatsappCleanup.retentionDays must be positive")
	}
	if err := ValidateLocalPrefix(s.LocalPrefix); err != nil {
		return err
	}
	if err := ValidateAggregationTrigger(s.Polls.AggregationTrigger); err != nil {
		return err
	}
	return nil
}

func (s *SyncSetSettings) Normalize() {
	s.LocalPrefix = strings.Join(strings.Fields(s.LocalPrefix), " ")
	s.Polls.AggregationTrigger = strings.Join(strings.Fields(s.Polls.AggregationTrigger), " ")
	if s.Polls.AggregationTrigger == "" {
		s.Polls.AggregationTrigger = "aggregate-response"
	}
}
