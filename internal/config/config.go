package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

type Config struct {
	Groups   map[string]Group `json:"groups"`
	SyncSets []SyncSet        `json:"syncSets"`
	Identity Identity         `json:"identity"`
	Media    Media            `json:"media"`
	Recovery Recovery         `json:"recovery"`
	Storage  Storage          `json:"storage"`
}

type Group struct {
	JID string `json:"jid"`
}

type SyncSet struct {
	ID     string   `json:"id"`
	Groups []string `json:"groups"`
}

type Identity struct {
	UsernameMode string `json:"usernameMode"`
}

type Media struct {
	Enabled   bool `json:"enabled"`
	MaxSizeMB int  `json:"maxSizeMB"`
}

type Recovery struct {
	Enabled             bool `json:"enabled"`
	MaxAgeHours         int  `json:"maxAgeHours"`
	MaxMessagesPerGroup int  `json:"maxMessagesPerGroup"`
}

type Storage struct {
	MessageRetentionDays int `json:"messageRetentionDays"`
}

func PathFromEnv() string {
	if path := strings.TrimSpace(os.Getenv("CONFIG_PATH")); path != "" {
		return path
	}
	return "./config.json"
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return nil, err
	}
	applyDefaults(&cfg)
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func applyDefaults(cfg *Config) {
	if cfg.Identity.UsernameMode == "" {
		cfg.Identity.UsernameMode = "push_name"
	}
	if cfg.Media.MaxSizeMB == 0 {
		cfg.Media.MaxSizeMB = 100
	}
	if cfg.Recovery.MaxAgeHours == 0 {
		cfg.Recovery.MaxAgeHours = 24
	}
	if cfg.Recovery.MaxMessagesPerGroup == 0 {
		cfg.Recovery.MaxMessagesPerGroup = 200
	}
	if cfg.Storage.MessageRetentionDays == 0 {
		cfg.Storage.MessageRetentionDays = 90
	}
}

func (c Config) Validate() error {
	if len(c.Groups) < 2 {
		return errors.New("at least two groups are required")
	}
	if len(c.SyncSets) == 0 {
		return errors.New("at least one sync set is required")
	}
	if c.Identity.UsernameMode != "push_name" && c.Identity.UsernameMode != "hash" {
		return fmt.Errorf("identity.usernameMode must be push_name or hash")
	}
	if c.Media.MaxSizeMB < 1 {
		return errors.New("media.maxSizeMB must be positive")
	}
	if c.Storage.MessageRetentionDays < 1 {
		return errors.New("storage.messageRetentionDays must be positive")
	}

	membership := make(map[string]string)
	for _, set := range c.SyncSets {
		if strings.TrimSpace(set.ID) == "" {
			return errors.New("sync set id is required")
		}
		if len(set.Groups) < 2 {
			return fmt.Errorf("sync set %q must contain at least two groups", set.ID)
		}
		seen := make(map[string]struct{})
		for _, alias := range set.Groups {
			group, ok := c.Groups[alias]
			if !ok {
				return fmt.Errorf("sync set %q references unknown group %q", set.ID, alias)
			}
			if strings.TrimSpace(group.JID) == "" {
				return fmt.Errorf("group %q has empty jid", alias)
			}
			if _, duplicate := seen[alias]; duplicate {
				return fmt.Errorf("sync set %q repeats group %q", set.ID, alias)
			}
			seen[alias] = struct{}{}
			if previous, exists := membership[alias]; exists {
				return fmt.Errorf("group %q belongs to both %q and %q", alias, previous, set.ID)
			}
			membership[alias] = set.ID
		}
	}
	return nil
}
