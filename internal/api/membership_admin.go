package api

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/vm75/message-sync/internal/verification"
)

type membershipReadinessAdmin interface {
	verification.WhatsAppAdmin
}

type MembershipReadinessView struct {
	Ready                bool   `json:"ready"`
	JoinApprovalRequired bool   `json:"joinApprovalRequired"`
	FailureClass         string `json:"failureClass,omitempty"`
}

func (s *Server) handleGetMembershipReadiness(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	alias := strings.TrimSpace(r.URL.Query().Get("endpointAlias"))
	if id == "" || alias == "" || s.db == nil || s.connections == nil {
		WriteError(w, http.StatusBadRequest, "connection and endpoint are required")
		return
	}
	var transport, connectionID string
	if err := s.db.QueryRowContext(r.Context(), `SELECT transport,connection_id FROM endpoints WHERE alias=?`, alias).Scan(&transport, &connectionID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			WriteError(w, http.StatusNotFound, "endpoint not found")
		} else {
			WriteError(w, http.StatusServiceUnavailable, "membership readiness unavailable")
		}
		return
	}
	if transport != "whatsapp" || connectionID != id {
		WriteError(w, http.StatusBadRequest, "endpoint is not owned by this WhatsApp connection")
		return
	}
	adapter, ok := s.connections.ConnectionAdapter(id)
	admin, adminOK := adapter.(membershipReadinessAdmin)
	if !ok || !adminOK {
		_ = WriteJSON(w, http.StatusOK, MembershipReadinessView{FailureClass: "connection_unavailable"})
		return
	}
	required, err := admin.JoinApprovalRequired(r.Context(), alias)
	if err != nil {
		_ = WriteJSON(w, http.StatusOK, MembershipReadinessView{FailureClass: "group_unavailable"})
		return
	}
	if !required {
		_ = WriteJSON(w, http.StatusOK, MembershipReadinessView{JoinApprovalRequired: false, FailureClass: "join_approval_required"})
		return
	}
	_ = WriteJSON(w, http.StatusOK, MembershipReadinessView{Ready: true, JoinApprovalRequired: true})
}
