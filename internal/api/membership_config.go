package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"
)

var membershipFieldKeyPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,31}$`)

type MembershipCustomField struct {
	Key       string   `json:"key"`
	Label     string   `json:"label"`
	Type      string   `json:"type"`
	Required  bool     `json:"required"`
	MaxLength int      `json:"maxLength"`
	Position  int      `json:"position"`
	Options   []string `json:"options,omitempty"`
}

type MembershipConfig struct {
	ApplicantInstructions string                  `json:"applicantInstructions"`
	EvidenceInstructions  string                  `json:"evidenceInstructions"`
	ReviewerGuidance      string                  `json:"reviewerGuidance"`
	EvidenceRequired      bool                    `json:"evidenceRequired"`
	CustomFields          []MembershipCustomField `json:"customFields"`
}

func (s *Server) membershipConfig(ctx context.Context, syncSetID string) (MembershipConfig, error) {
	var out MembershipConfig
	err := s.controlDB.QueryRowContext(ctx, `SELECT applicant_instructions,evidence_instructions,reviewer_guidance,evidence_required FROM membership_configs WHERE sync_set_id=?`, syncSetID).Scan(&out.ApplicantInstructions, &out.EvidenceInstructions, &out.ReviewerGuidance, &out.EvidenceRequired)
	if errors.Is(err, sql.ErrNoRows) {
		out.CustomFields = []MembershipCustomField{}
		return out, nil
	}
	if err != nil {
		return MembershipConfig{}, err
	}
	out.CustomFields = []MembershipCustomField{}
	rows, err := s.controlDB.QueryContext(ctx, `SELECT field_key,label,field_type,required,max_length,position,options_json FROM membership_custom_fields WHERE sync_set_id=? ORDER BY position`, syncSetID)
	if err != nil {
		return MembershipConfig{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var field MembershipCustomField
		var options string
		if err := rows.Scan(&field.Key, &field.Label, &field.Type, &field.Required, &field.MaxLength, &field.Position, &options); err != nil {
			return MembershipConfig{}, err
		}
		if err := json.Unmarshal([]byte(options), &field.Options); err != nil {
			return MembershipConfig{}, err
		}
		out.CustomFields = append(out.CustomFields, field)
	}
	return out, rows.Err()
}

func marshalMembershipSnapshot(cfg MembershipConfig) (string, error) {
	b, err := json.Marshal(cfg)
	return string(b), err
}

func validateMembershipAnswers(cfg MembershipConfig, raw string) (map[string]any, error) {
	answers := map[string]any{}
	if strings.TrimSpace(raw) != "" {
		var values map[string]json.RawMessage
		if err := json.Unmarshal([]byte(raw), &values); err != nil {
			return nil, errors.New("invalid membership answers")
		}
		for key, value := range values {
			var answer any
			decoder := json.NewDecoder(strings.NewReader(string(value)))
			if err := decoder.Decode(&answer); err != nil {
				return nil, errors.New("invalid membership answer")
			}
			answers[key] = answer
		}
	}
	for _, field := range cfg.CustomFields {
		answer, ok := answers[field.Key]
		if !ok {
			if field.Required {
				return nil, errors.New("required membership answer is missing")
			}
			continue
		}
		switch field.Type {
		case "text", "textarea":
			text, valid := answer.(string)
			if !valid {
				return nil, errors.New("invalid membership answer")
			}
			text = strings.TrimSpace(text)
			answers[field.Key] = text
			if text == "" && field.Required {
				return nil, errors.New("required membership answer is missing")
			}
			if len(text) > field.MaxLength {
				return nil, errors.New("membership answer is too long")
			}
		case "select":
			text, valid := answer.(string)
			if !valid {
				return nil, errors.New("invalid membership answer")
			}
			valid = false
			for _, option := range field.Options {
				if text == option {
					valid = true
					break
				}
			}
			if !valid {
				return nil, errors.New("invalid membership option")
			}
		case "checkbox":
			checked, valid := answer.(bool)
			if !valid || (field.Required && !checked) {
				return nil, errors.New("invalid membership checkbox")
			}
		default:
			return nil, errors.New("invalid membership field type")
		}
	}
	for key := range answers {
		found := false
		for _, field := range cfg.CustomFields {
			if field.Key == key {
				found = true
				break
			}
		}
		if !found {
			return nil, errors.New("unknown membership answer")
		}
	}
	return answers, nil
}

func (s *Server) membershipSyncSetExists(r *http.Request, id string) error {
	if s.db == nil || s.controlDB == nil {
		return errors.New("database unavailable")
	}
	var found string
	if err := s.db.QueryRowContext(r.Context(), `SELECT id FROM sync_sets WHERE id=?`, id).Scan(&found); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("sync set not found")
		}
		return errors.New("database unavailable")
	}
	return nil
}

func validateMembershipConfig(in MembershipConfig) error {
	if len(in.ApplicantInstructions) > 4000 || len(in.EvidenceInstructions) > 2000 || len(in.ReviewerGuidance) > 4000 || len(in.CustomFields) > 32 {
		return errors.New("membership configuration is too large")
	}
	seen := make(map[string]struct{}, len(in.CustomFields))
	positions := make(map[int]struct{}, len(in.CustomFields))
	for i, field := range in.CustomFields {
		field.Key = strings.TrimSpace(field.Key)
		field.Label = strings.TrimSpace(field.Label)
		field.Type = strings.TrimSpace(field.Type)
		if !membershipFieldKeyPattern.MatchString(field.Key) || field.Label == "" || len(field.Label) > 120 {
			return errors.New("invalid membership custom field")
		}
		if _, ok := seen[field.Key]; ok {
			return errors.New("duplicate membership custom field")
		}
		seen[field.Key] = struct{}{}
		if field.Type != "text" && field.Type != "textarea" && field.Type != "select" && field.Type != "checkbox" {
			return errors.New("invalid membership custom field type")
		}
		if field.Type != "select" && len(field.Options) != 0 {
			return errors.New("options are only valid for select fields")
		}
		if field.Type == "select" && (len(field.Options) == 0 || len(field.Options) > 32) {
			return errors.New("select options are required and bounded")
		}
		seenOptions := map[string]struct{}{}
		for _, option := range field.Options {
			option = strings.TrimSpace(option)
			if option == "" || len(option) > 120 {
				return errors.New("invalid select option")
			}
			if _, ok := seenOptions[option]; ok {
				return errors.New("duplicate select option")
			}
			seenOptions[option] = struct{}{}
		}
		if field.MaxLength < 0 || field.MaxLength > 2000 || field.Position < 0 || field.Position > 31 || i > 31 {
			return errors.New("invalid membership custom field bounds")
		}
		position := field.Position
		if position == 0 && i != 0 {
			position = i
		}
		if _, ok := positions[position]; ok {
			return errors.New("duplicate membership custom field position")
		}
		positions[position] = struct{}{}
	}
	return nil
}

func (s *Server) handleGetMembershipConfig(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if err := s.membershipSyncSetExists(r, id); err != nil {
		if err.Error() == "sync set not found" {
			WriteError(w, http.StatusNotFound, err.Error())
		} else {
			WriteError(w, http.StatusServiceUnavailable, err.Error())
		}
		return
	}
	out, err := s.membershipConfig(r.Context(), id)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to read membership configuration")
		return
	}
	_ = WriteJSON(w, http.StatusOK, out)
}

func (s *Server) handlePutMembershipConfig(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if err := s.membershipSyncSetExists(r, id); err != nil {
		if err.Error() == "sync set not found" {
			WriteError(w, http.StatusNotFound, err.Error())
		} else {
			WriteError(w, http.StatusServiceUnavailable, err.Error())
		}
		return
	}
	var in MembershipConfig
	if ReadJSON(r, &in) != nil || validateMembershipConfig(in) != nil {
		WriteError(w, http.StatusBadRequest, "invalid membership configuration")
		return
	}
	now := time.Now().UnixMilli()
	tx, err := s.controlDB.BeginTx(r.Context(), nil)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "membership configuration unavailable")
		return
	}
	defer tx.Rollback()
	p, _ := principalFromContext(r.Context())
	_, err = tx.ExecContext(r.Context(), `INSERT INTO membership_configs(sync_set_id,applicant_instructions,evidence_instructions,reviewer_guidance,evidence_required,updated_by_user_id,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(sync_set_id) DO UPDATE SET applicant_instructions=excluded.applicant_instructions,evidence_instructions=excluded.evidence_instructions,reviewer_guidance=excluded.reviewer_guidance,evidence_required=excluded.evidence_required,updated_by_user_id=excluded.updated_by_user_id,updated_at=excluded.updated_at`, id, strings.TrimSpace(in.ApplicantInstructions), strings.TrimSpace(in.EvidenceInstructions), strings.TrimSpace(in.ReviewerGuidance), in.EvidenceRequired, p.ID, now, now)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "membership configuration unavailable")
		return
	}
	if _, err = tx.ExecContext(r.Context(), `DELETE FROM membership_custom_fields WHERE sync_set_id=?`, id); err != nil {
		WriteError(w, http.StatusInternalServerError, "membership configuration unavailable")
		return
	}
	for i, field := range in.CustomFields {
		position := field.Position
		if position == 0 {
			position = i
		}
		maxLength := field.MaxLength
		if maxLength == 0 {
			maxLength = 500
		}
		fieldID, idErr := randomOpaqueID()
		if idErr != nil {
			WriteError(w, http.StatusInternalServerError, "membership configuration unavailable")
			return
		}
		options, _ := json.Marshal(field.Options)
		if _, err = tx.ExecContext(r.Context(), `INSERT INTO membership_custom_fields(id,sync_set_id,field_key,label,field_type,required,max_length,position,options_json) VALUES(?,?,?,?,?,?,?,?,?)`, fieldID, id, strings.TrimSpace(field.Key), strings.TrimSpace(field.Label), strings.TrimSpace(field.Type), field.Required, maxLength, position, string(options)); err != nil {
			WriteError(w, http.StatusInternalServerError, "membership configuration unavailable")
			return
		}
	}
	if err = tx.Commit(); err != nil {
		WriteError(w, http.StatusInternalServerError, "membership configuration unavailable")
		return
	}
	_ = WriteJSON(w, http.StatusOK, in)
}

func (s *Server) handleDeleteMembershipConfig(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if err := s.membershipSyncSetExists(r, id); err != nil {
		if err.Error() == "sync set not found" {
			WriteError(w, http.StatusNotFound, err.Error())
		} else {
			WriteError(w, http.StatusServiceUnavailable, err.Error())
		}
		return
	}
	if _, err := s.controlDB.ExecContext(r.Context(), `DELETE FROM membership_configs WHERE sync_set_id=?`, id); err != nil {
		WriteError(w, http.StatusInternalServerError, "membership configuration unavailable")
		return
	}
	_ = WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
