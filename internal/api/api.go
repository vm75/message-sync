package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/vm75/message-sync/internal/delivery"
	"github.com/vm75/message-sync/internal/safelog"
	discord "github.com/vm75/message-sync/internal/transport/discord"
	telegram "github.com/vm75/message-sync/internal/transport/telegram"
)

type WhatsAppStatus struct {
	Status      string `json:"status"`
	IsLoggedIn  bool   `json:"isLoggedIn"`
	IsConnected bool   `json:"isConnected"`
	QRCode      string `json:"qrCode,omitempty"`
}

type WhatsAppPairResponse struct {
	Status         string `json:"status"`
	QRCode         string `json:"qrCode,omitempty"`
	TimeoutSeconds int    `json:"timeoutSeconds,omitempty"`
	IsLoggedIn     bool   `json:"isLoggedIn,omitempty"`
}

type WhatsAppGroup struct {
	JID  string `json:"jid"`
	Name string `json:"name"`
}

type WhatsAppService interface {
	Status(ctx context.Context) WhatsAppStatus
	Pair(ctx context.Context) (WhatsAppPairResponse, error)
	CancelPair(ctx context.Context) error
	Logout(ctx context.Context) error
	GetJoinedGroups(ctx context.Context) ([]WhatsAppGroup, error)
}

type Options struct {
	Addr       string
	Logger     *slog.Logger
	DB         *sql.DB
	Secret     []byte
	SessionTTL time.Duration
	WhatsApp   WhatsAppService
	Discord    discord.AdminService
	Telegram   telegram.AdminService
	Delivery   interface {
		DeliveryStatus(context.Context) ([]delivery.EndpointStatus, error)
	}
	OnConfigChange func(ctx context.Context) error
}

type Server struct {
	httpServer *http.Server
	mux        *http.ServeMux
	handler    http.Handler
	logger     *slog.Logger
	db         *sql.DB
	sessions   *SessionManager
	whatsapp   WhatsAppService
	discord    discord.AdminService
	telegram   telegram.AdminService
	delivery   interface {
		DeliveryStatus(context.Context) ([]delivery.EndpointStatus, error)
	}
	onConfigChange func(ctx context.Context) error
	listener       net.Listener
}

func NewServer(opts Options) *Server {
	addr := opts.Addr
	if addr == "" {
		addr = ":8080"
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}

	sessions, err := NewSessionManager(opts.Secret, opts.SessionTTL)
	if err != nil {
		sessions, _ = NewSessionManager(nil, opts.SessionTTL)
	}

	mux := http.NewServeMux()
	s := &Server{
		mux:            mux,
		logger:         logger,
		db:             opts.DB,
		sessions:       sessions,
		whatsapp:       opts.WhatsApp,
		discord:        opts.Discord,
		telegram:       opts.Telegram,
		delivery:       opts.Delivery,
		onConfigChange: opts.OnConfigChange,
	}

	s.registerRoutes()
	s.handler = s.authMiddleware(mux)

	s.httpServer = &http.Server{
		Addr:              addr,
		Handler:           s.handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	return s
}

func (s *Server) registerRoutes() {
	s.mux.HandleFunc("GET /health", s.handleHealth)
	s.mux.HandleFunc("GET /api/auth/status", s.handleAuthStatus)
	s.mux.HandleFunc("POST /api/auth/setup", s.handleAuthSetup)
	s.mux.HandleFunc("POST /api/auth/login", s.handleAuthLogin)
	s.mux.HandleFunc("POST /api/auth/logout", s.handleAuthLogout)
	s.mux.HandleFunc("POST /api/auth/change-password", s.handleAuthChangePassword)

	s.mux.HandleFunc("GET /api/whatsapp/status", s.handleWhatsAppStatus)
	s.mux.HandleFunc("POST /api/whatsapp/pair", s.handleWhatsAppPair)
	s.mux.HandleFunc("DELETE /api/whatsapp/pair", s.handleWhatsAppCancelPair)
	s.mux.HandleFunc("POST /api/whatsapp/logout", s.handleWhatsAppLogout)
	s.mux.HandleFunc("GET /api/whatsapp/groups", s.handleWhatsAppGroups)

	s.mux.HandleFunc("GET /api/discord/status", s.handleDiscordStatus)
	s.mux.HandleFunc("GET /api/discord/channels", s.handleDiscordChannels)

	s.mux.HandleFunc("GET /api/telegram/status", s.handleTelegramStatus)
	s.mux.HandleFunc("GET /api/telegram/chats", s.handleTelegramChats)
	s.mux.HandleFunc("GET /api/delivery/status", s.handleDeliveryStatus)

	s.mux.HandleFunc("GET /api/endpoints", s.handleListEndpoints)
	s.mux.HandleFunc("GET /api/endpoints/{alias}", s.handleGetEndpoint)
	s.mux.HandleFunc("POST /api/endpoints", s.handleCreateEndpoint)
	s.mux.HandleFunc("PUT /api/endpoints/{alias}", s.handleUpdateEndpoint)
	s.mux.HandleFunc("DELETE /api/endpoints/{alias}", s.handleDeleteEndpoint)
	for _, method := range []string{"GET", "POST", "PUT", "DELETE"} {
		s.mux.HandleFunc(method+" /api/groups", s.handleRemovedGroups)
		s.mux.HandleFunc(method+" /api/groups/{alias}", s.handleRemovedGroups)
	}

	s.mux.HandleFunc("GET /api/sync-sets", s.handleListSyncSets)
	s.mux.HandleFunc("GET /api/sync-sets/{id}", s.handleGetSyncSet)
	s.mux.HandleFunc("POST /api/sync-sets", s.handleCreateSyncSet)
	s.mux.HandleFunc("PUT /api/sync-sets/{id}", s.handleUpdateSyncSet)
	s.mux.HandleFunc("DELETE /api/sync-sets/{id}", s.handleDeleteSyncSet)

	s.mux.HandleFunc("GET /api/config", s.handleGetConfig)
	s.mux.HandleFunc("PUT /api/config", s.handleUpdateConfig)

	s.mux.Handle("GET /", StaticHandler())
}

func (s *Server) handleRemovedGroups(w http.ResponseWriter, r *http.Request) {
	WriteError(w, http.StatusNotFound, "route not found")
}

func (s *Server) notifyConfigChange(ctx context.Context) {
	if s.onConfigChange != nil {
		if err := s.onConfigChange(ctx); err != nil {
			safelog.Error(s.logger, "notify config change failed", "config_reload_notification", err)
		}
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	_ = WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) Handler() http.Handler {
	return s.handler
}

func (s *Server) Start() error {
	l, err := net.Listen("tcp", s.httpServer.Addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", s.httpServer.Addr, err)
	}
	s.listener = l
	s.logger.Info("api server listening", "addr", l.Addr().String())

	go func() {
		if err := s.httpServer.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.logger.Error("api server error", "error", err.Error())
		}
	}()
	return nil
}

func (s *Server) Addr() string {
	if s.listener != nil {
		return s.listener.Addr().String()
	}
	return s.httpServer.Addr
}

func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}

// WriteJSON writes the given status code and JSON payload to the ResponseWriter.
func WriteJSON(w http.ResponseWriter, status int, data any) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if data == nil {
		return nil
	}
	return json.NewEncoder(w).Encode(data)
}

// ReadJSON decodes a single JSON object from the request body.
func ReadJSON(r *http.Request, dst any) error {
	if r.Body == nil {
		return errors.New("request body is empty")
	}
	defer r.Body.Close()

	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("decode json: %w", err)
	}

	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("request body must contain a single JSON object")
		}
		return fmt.Errorf("decode trailing json: %w", err)
	}
	return nil
}

type ErrorResponse struct {
	Error string `json:"error"`
}

// WriteError writes a standard JSON error response.
func WriteError(w http.ResponseWriter, status int, message string) {
	_ = WriteJSON(w, status, ErrorResponse{Error: message})
}
