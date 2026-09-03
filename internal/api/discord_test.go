package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	discord "github.com/vm75/message-sync/internal/transport/discord"
)

type fakeDiscordAdminService struct {
	status      discord.AdminStatus
	channels    []discord.DiscoveredChannel
	discoverErr error
}

func (f *fakeDiscordAdminService) AdminStatus(context.Context) discord.AdminStatus {
	return f.status
}

func (f *fakeDiscordAdminService) DiscoverChannels(context.Context) ([]discord.DiscoveredChannel, error) {
	return f.channels, f.discoverErr
}

func authenticatedDiscordRequest(t *testing.T, srv *Server, method, path string) *http.Request {
	t.Helper()
	token, err := srv.sessions.CreateToken()
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	return req
}

func TestDiscordAdminEndpointsRequireAuthentication(t *testing.T) {
	srv := NewServer(Options{
		Secret:  []byte("01234567890123456789012345678901"),
		Discord: &fakeDiscordAdminService{},
	})

	for _, path := range []string{"/api/connections/conn-dc-1/status", "/api/connections/conn-dc-1/discovery"} {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("GET %s status=%d, want 401", path, rec.Code)
		}
	}
}

func TestDiscordStatusNotConfiguredIsSafe(t *testing.T) {
	srv := NewServer(Options{Secret: []byte("01234567890123456789012345678901")})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, authenticatedDiscordRequest(t, srv, http.MethodGet, "/api/connections/conn-dc-1/status"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200: %s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got["status"] != "stopped" && got["status"] != "not_configured" {
		t.Fatalf("unexpected status: %+v", got)
	}
}

func TestDiscordStatusAndDiscoveryExposeOnlyTransientSelectionMetadata(t *testing.T) {
	service := &fakeDiscordAdminService{
		status: discord.AdminStatus{
			Configured: true,
			Connected:  true,
			Status:     "connected",
			Webhooks: []discord.EndpointWebhookStatus{
				{Alias: "dc-ep-1", Status: discord.WebhookStatusReady},
				{Alias: "dc-ep-2", Status: discord.WebhookStatusMissingPermission},
			},
		},
		channels: []discord.DiscoveredChannel{{
			GuildID:     "111111111111111111",
			GuildName:   "Transient Guild",
			ChannelID:   "222222222222222222",
			ChannelName: "transient-channel",
		}},
	}
	srv := NewServer(Options{
		Secret:  []byte("01234567890123456789012345678901"),
		Discord: service,
	})

	statusRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(statusRec, authenticatedDiscordRequest(t, srv, http.MethodGet, "/api/connections/conn-dc-1/status"))
	if statusRec.Code != http.StatusOK {
		t.Fatalf("status endpoint=%d: %s", statusRec.Code, statusRec.Body.String())
	}
	body := statusRec.Body.String()
	for _, forbidden := range []string{"webhookUrl", "webhookToken", "botToken"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("status exposed secret field %q: %s", forbidden, body)
		}
	}
	if !strings.Contains(body, `"status":"ready"`) || !strings.Contains(body, `"status":"missing_permission"`) {
		t.Fatalf("status omitted safe readiness states: %s", body)
	}

	discoveryRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(discoveryRec, authenticatedDiscordRequest(t, srv, http.MethodGet, "/api/connections/conn-dc-1/discovery"))
	if discoveryRec.Code != http.StatusOK {
		t.Fatalf("discovery endpoint=%d: %s", discoveryRec.Code, discoveryRec.Body.String())
	}
	var channels []discord.DiscoveredChannel
	if err := json.NewDecoder(discoveryRec.Body).Decode(&channels); err != nil {
		t.Fatal(err)
	}
	if len(channels) != 1 || channels[0].GuildName != "Transient Guild" || channels[0].ChannelName != "transient-channel" {
		t.Fatalf("unexpected discovery response: %+v", channels)
	}
}

func TestDiscordDiscoveryErrorsAreSafeLogged(t *testing.T) {
	var logs bytes.Buffer
	service := &fakeDiscordAdminService{
		status:      discord.AdminStatus{Configured: true, Connected: true, Status: "connected"},
		discoverErr: errors.New("guild private-name channel 222 secret-token"),
	}
	srv := NewServer(Options{
		Secret:  []byte("01234567890123456789012345678901"),
		Discord: service,
		Logger:  slog.New(slog.NewTextHandler(&logs, nil)),
	})

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, authenticatedDiscordRequest(t, srv, http.MethodGet, "/api/connections/conn-dc-1/discovery"))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d, want 500", rec.Code)
	}
	for _, forbidden := range []string{"private-name", "222", "secret-token"} {
		if strings.Contains(logs.String(), forbidden) {
			t.Fatalf("logs exposed Discord discovery data %q: %s", forbidden, logs.String())
		}
	}
}
