package api

import "context"

func (s *Server) pipelineHasOutstandingMembershipRequests(ctx context.Context, pipelineID string) (bool, error) {
	if s == nil || s.controlDB == nil {
		return false, nil
	}
	var count int
	err := s.controlDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM membership_requests WHERE pipeline_id=? AND status IN ('pending_email','pending_admin','pending','approved')`, pipelineID).Scan(&count)
	return count > 0, err
}

func (s *Server) endpointHasOutstandingMembershipRequests(ctx context.Context, endpointAlias string) (bool, error) {
	if s == nil || s.controlDB == nil {
		return false, nil
	}
	var count int
	err := s.controlDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM membership_requests m JOIN verification_pipelines p ON p.id=m.pipeline_id WHERE p.endpoint_alias=? AND m.status IN ('pending_email','pending_admin','pending','approved')`, endpointAlias).Scan(&count)
	return count > 0, err
}
