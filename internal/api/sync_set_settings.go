package api

import (
	"bytes"
	"encoding/json"
	"errors"

	"github.com/vm75/message-sync/internal/config"
)

func mergeSyncSetSettings(current config.SyncSetSettings, raw json.RawMessage) (config.SyncSetSettings, error) {
	if len(raw) != 0 {
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return current, errors.New("settings must be an object")
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&current); err != nil {
			return current, errors.New("invalid sync-set settings")
		}
	}
	current.Normalize()
	return current, current.Validate()
}
