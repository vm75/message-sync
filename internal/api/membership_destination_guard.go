package api

import "context"

const outstandingMembershipPredicate = `(status IN ('pending_email','pending_admin','pending') OR (status='approved' AND fulfillment_state!='succeeded'))`

func (s *Server) pipelineHasOutstandingMembershipRequests(ctx context.Context, pipelineID string) (bool, error) {
	if s == nil || s.controlDB == nil {
		return false, nil
	}
	var count int
	err := s.controlDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM membership_requests WHERE pipeline_id=? AND `+outstandingMembershipPredicate, pipelineID).Scan(&count)
	return count > 0, err
}

func (s *Server) endpointHasOutstandingMembershipRequests(ctx context.Context, endpointAlias string) (bool, error) {
	if s == nil || s.controlDB == nil {
		return false, nil
	}
	var count int
	err := s.controlDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM membership_requests m JOIN verification_pipelines p ON p.id=m.pipeline_id WHERE p.endpoint_alias=? AND (m.status IN ('pending_email','pending_admin','pending') OR (m.status='approved' AND m.fulfillment_state!='succeeded'))`, endpointAlias).Scan(&count)
	return count > 0, err
}
