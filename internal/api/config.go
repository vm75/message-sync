package api

import (
	"net/http"
)

type privateDMRelaySetting struct {
	PrivateDMRelayEnabled bool `json:"privateDMRelayEnabled"`
}

// The legacy /api/config route now exposes only the global safety gate for
// private DMs. Runtime routing settings remain owned by sync sets.
func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		WriteError(w, http.StatusServiceUnavailable, "configuration store is unavailable")
		return
	}
	var enabled bool
	if err := s.db.QueryRowContext(r.Context(), `SELECT private_dm_relay_enabled FROM global_config WHERE id=1`).Scan(&enabled); err != nil {
		WriteError(w, http.StatusInternalServerError, "load private messaging setting")
		return
	}
	_ = WriteJSON(w, http.StatusOK, privateDMRelaySetting{PrivateDMRelayEnabled: enabled})
}

func (s *Server) handleUpdateConfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PrivateDMRelayEnabled *bool `json:"privateDMRelayEnabled"`
	}
	if err := ReadJSON(r, &req); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid private messaging setting")
		return
	}
	if req.PrivateDMRelayEnabled == nil {
		WriteError(w, http.StatusBadRequest, "privateDMRelayEnabled is required")
		return
	}
	if s.db == nil {
		WriteError(w, http.StatusServiceUnavailable, "configuration store is unavailable")
		return
	}
	if _, err := s.db.ExecContext(r.Context(), `UPDATE global_config SET private_dm_relay_enabled=? WHERE id=1`, *req.PrivateDMRelayEnabled); err != nil {
		WriteError(w, http.StatusInternalServerError, "save private messaging setting")
		return
	}
	if s.onConfigChange != nil {
		if err := s.onConfigChange(r.Context()); err != nil {
			WriteError(w, http.StatusInternalServerError, "reload private messaging setting")
			return
		}
	}
	_ = WriteJSON(w, http.StatusOK, privateDMRelaySetting{PrivateDMRelayEnabled: *req.PrivateDMRelayEnabled})
}
