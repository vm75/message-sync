package api

import "net/http"

// Runtime policy belongs to sync sets; this authenticated tombstone prevents
// older clients from silently writing settings that no longer govern routing.
func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	WriteError(w, http.StatusGone, "runtime settings moved to /api/sync-sets/{id}; WhatsApp device names belong to /api/connections/{id}")
}

func (s *Server) handleUpdateConfig(w http.ResponseWriter, r *http.Request) {
	s.handleGetConfig(w, r)
}
