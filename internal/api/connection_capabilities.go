package api

import (
	"github.com/vm75/message-sync/internal/controlstore"
	telegram "github.com/vm75/message-sync/internal/transport/telegram"
)

func applyConnectionCapabilities(dto *ConnectionDTO) {
	if dto == nil {
		return
	}
	dto.IntegrationMode = controlstore.NormalizeIntegrationMode(dto.Transport, dto.IntegrationMode)
	if dto.Transport != "telegram" {
		dto.IntegrationMode = ""
		dto.Capabilities = nil
		return
	}
	caps := telegram.CapabilitiesForIntegrationMode(dto.IntegrationMode)
	dto.Capabilities = &caps
}

func connectionDTOFromControl(conn controlstore.Connection) ConnectionDTO {
	dto := ConnectionDTO{
		ID:              conn.ID,
		Transport:       conn.Transport,
		IntegrationMode: conn.IntegrationMode,
		Label:           conn.Label,
		Enabled:         conn.Enabled,
		CreatedAt:       conn.CreatedAt,
		UpdatedAt:       conn.UpdatedAt,
	}
	applyConnectionCapabilities(&dto)
	return dto
}
