package api

import (
	"net/http"
	"time"

	"github.com/vm75/message-sync/internal/delivery"
	discord "github.com/vm75/message-sync/internal/transport/discord"
	telegram "github.com/vm75/message-sync/internal/transport/telegram"
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
	configured := make([]endpoint, 0)
	for rows.Next() {
		var item endpoint
		if err := rows.Scan(&item.alias, &item.transport); err != nil {
			WriteError(w, http.StatusInternalServerError, "failed to read delivery endpoints")
			return
		}
		configured = append(configured, item)
	}
	if err := rows.Err(); err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to iterate delivery endpoints")
		return
	}
	statuses, err := s.delivery.DeliveryStatus(r.Context())
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to collect delivery status")
		return
	}
	statusByEndpoint := make(map[string]delivery.EndpointStatus, len(statuses))
	for _, st := range statuses {
		statusByEndpoint[st.EndpointID] = st
	}
	transportStatus := s.transportStatuses(r)
	result := deliveryStatusResponse{Endpoints: make([]deliveryStatusDTO, 0, len(configured))}
	for _, item := range configured {
		status := statusByEndpoint[item.alias]
		if status.LaneState == "" {
			status.LaneState = "stopped"
		}
		result.Endpoints = append(result.Endpoints, deliveryStatusDTO{
			Alias: item.alias, Transport: item.transport,
			TransportStatus: transportStatus[item.alias], LaneState: status.LaneState,
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
	if s.connections != nil && s.db != nil {
		rows, err := s.db.QueryContext(r.Context(), `SELECT alias, transport, connection_id FROM endpoints`)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var alias, transport, connID string
				if rows.Scan(&alias, &transport, &connID) == nil && connID != "" {
					if st, err := s.connections.ConnectionStatus(r.Context(), connID); err == nil && st != nil {
						switch v := st.(type) {
						case WhatsAppStatus:
							result[alias] = v.Status
						case *WhatsAppStatus:
							result[alias] = v.Status
						case discord.AdminStatus:
							result[alias] = string(v.Status)
							for _, wh := range v.Webhooks {
								if wh.Alias == alias {
									result[alias] = string(wh.Status)
									break
								}
							}
						case *discord.AdminStatus:
							result[alias] = string(v.Status)
							for _, wh := range v.Webhooks {
								if wh.Alias == alias {
									result[alias] = string(wh.Status)
									break
								}
							}
						case telegram.AdminStatus:
							result[alias] = "polling"
							for _, ep := range v.Endpoints {
								if ep.Alias == alias {
									result[alias] = ep.Status
									break
								}
							}
						case *telegram.AdminStatus:
							result[alias] = "polling"
							for _, ep := range v.Endpoints {
								if ep.Alias == alias {
									result[alias] = ep.Status
									break
								}
							}
						case map[string]any:
							if stat, ok := v["status"].(string); ok {
								result[alias] = stat
							}
						}
					}
				}
			}
		}
	}
	if s.whatsapp != nil {
		status := s.whatsapp.Status(r.Context()).Status
		rows, err := s.db.QueryContext(r.Context(), `SELECT alias FROM endpoints WHERE transport = 'whatsapp'`)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var alias string
				if rows.Scan(&alias) == nil {
					if _, exists := result[alias]; !exists {
						result[alias] = status
					}
				}
			}
		}
	}
	if s.discord != nil {
		status := s.discord.AdminStatus(r.Context())
		for _, item := range status.Webhooks {
			if _, exists := result[item.Alias]; !exists {
				result[item.Alias] = string(item.Status)
			}
		}
	}
	if s.telegram != nil {
		status := s.telegram.AdminStatus(r.Context())
		for _, item := range status.Endpoints {
			if _, exists := result[item.Alias]; !exists {
				result[item.Alias] = item.Status
			}
		}
	}
	return result
}
