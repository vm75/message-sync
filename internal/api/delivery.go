package api

import (
	"net/http"
	"time"

	"github.com/vm75/message-sync/internal/delivery"
)

type deliveryStatusResponse struct {
	Endpoints []deliveryStatusDTO `json:"endpoints"`
}

type deliveryStatusDTO struct {
	Alias           string `json:"alias"`
	Transport       string `json:"transport"`
	TransportStatus string `json:"transportStatus,omitempty"`
	LaneState       string `json:"laneState"`
	QueueDepth      int    `json:"queueDepth"`
	QueueCapacity   int    `json:"queueCapacity"`
	Queued          int64  `json:"queued"`
	Retrying        int64  `json:"retrying"`
	AwaitingReplay  int64  `json:"awaitingReplay"`
	Failed          int64  `json:"failed"`
	OldestActiveAge int64  `json:"oldestActiveAgeSeconds"`
	FailureClass    string `json:"lastFailureClass,omitempty"`
}

func (s *Server) handleDeliveryStatus(w http.ResponseWriter, r *http.Request) {
	if s.db == nil || s.delivery == nil {
		WriteError(w, http.StatusServiceUnavailable, "delivery status unavailable")
		return
	}
	rows, err := s.db.QueryContext(r.Context(), `SELECT alias, transport FROM endpoints ORDER BY alias ASC`)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to query delivery endpoints")
		return
	}
	defer rows.Close()
	type endpoint struct{ alias, transport string }
	endpoints := make([]endpoint, 0)
	for rows.Next() {
		var item endpoint
		if err := rows.Scan(&item.alias, &item.transport); err != nil {
			WriteError(w, http.StatusInternalServerError, "failed to read delivery endpoints")
			return
		}
		endpoints = append(endpoints, item)
	}
	if err := rows.Err(); err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to iterate delivery endpoints")
		return
	}
	statuses, err := s.delivery.DeliveryStatus(r.Context())
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to query delivery status")
		return
	}
	byAlias := make(map[string]delivery.EndpointStatus, len(statuses))
	for _, status := range statuses {
		byAlias[status.EndpointID] = status
	}
	transportStatus := s.transportStatuses(r)
	result := deliveryStatusResponse{Endpoints: make([]deliveryStatusDTO, 0, len(endpoints))}
	for _, endpoint := range endpoints {
		status := byAlias[endpoint.alias]
		if status.LaneState == "" {
			status.LaneState = "stopped"
		}
		result.Endpoints = append(result.Endpoints, deliveryStatusDTO{
			Alias: endpoint.alias, Transport: endpoint.transport,
			TransportStatus: transportStatus[endpoint.alias], LaneState: status.LaneState,
			QueueDepth: status.QueueDepth, QueueCapacity: status.QueueCapacity,
			Queued: status.Queued, Retrying: status.Retrying, AwaitingReplay: status.AwaitingReplay,
			Failed: status.Failed, OldestActiveAge: int64(status.OldestActiveAge / time.Second),
			FailureClass: status.FailureClass,
		})
	}
	_ = WriteJSON(w, http.StatusOK, result)
}

func (s *Server) transportStatuses(r *http.Request) map[string]string {
	result := make(map[string]string)
	if s.whatsapp != nil {
		status := s.whatsapp.Status(r.Context()).Status
		rows, err := s.db.QueryContext(r.Context(), `SELECT alias FROM endpoints WHERE transport = 'whatsapp'`)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var alias string
				if rows.Scan(&alias) == nil {
					result[alias] = status
				}
			}
		}
	}
	if s.discord != nil {
		status := s.discord.AdminStatus(r.Context())
		for _, item := range status.Webhooks {
			result[item.Alias] = string(item.Status)
		}
	}
	if s.telegram != nil {
		status := s.telegram.AdminStatus(r.Context())
		for _, item := range status.Endpoints {
			result[item.Alias] = item.Status
		}
	}
	return result
}
