package api

import (
	"context"
	"strings"
)

// membershipPipelineScope resolves safe endpoint aliases in sync.db, then
// filters pipelines in control.db without copying private membership data.
func (s *Server) membershipPipelineScope(ctx context.Context, syncSetID string) (string, []any, error) {
	if syncSetID == "" {
		return "", nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT alias FROM endpoints WHERE sync_set_id=? ORDER BY alias`, syncSetID)
	if err != nil {
		return "", nil, err
	}
	defer rows.Close()
	var args []any
	var placeholders []string
	for rows.Next() {
		var alias string
		if err := rows.Scan(&alias); err != nil {
			return "", nil, err
		}
		args = append(args, alias)
		placeholders = append(placeholders, "?")
	}
	if err := rows.Err(); err != nil {
		return "", nil, err
	}
	if len(args) == 0 {
		return "0", nil, nil
	}
	return "endpoint_alias IN (" + strings.Join(placeholders, ",") + ")", args, nil
}
