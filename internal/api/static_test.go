package api_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vm75/message-sync/internal/api"
)

func TestStaticHandler(t *testing.T) {
	srv := api.NewServer(api.Options{})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	client := ts.Client()

	t.Run("Root serves index.html", func(t *testing.T) {
		resp, err := client.Get(ts.URL + "/")
		if err != nil {
			t.Fatalf("GET / failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected status 200, got %d", resp.StatusCode)
		}

		ct := resp.Header.Get("Content-Type")
		if !strings.Contains(ct, "text/html") {
			t.Errorf("expected Content-Type text/html, got %s", ct)
		}

		body, _ := io.ReadAll(resp.Body)
		bodyStr := string(body)
		if !strings.Contains(bodyStr, "Message Sync • Admin Console") {
			t.Errorf("expected body to contain title, got: %s", bodyStr)
		}
		if !strings.Contains(bodyStr, "/css/app.css") {
			t.Errorf("expected body to link to /css/app.css")
		}
		if !strings.Contains(bodyStr, "/js/app.js") {
			t.Errorf("expected body to link to /js/app.js")
		}
	})

	t.Run("Serves css stylesheet", func(t *testing.T) {
		resp, err := client.Get(ts.URL + "/css/app.css")
		if err != nil {
			t.Fatalf("GET /css/app.css failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected status 200, got %d", resp.StatusCode)
		}

		ct := resp.Header.Get("Content-Type")
		if !strings.Contains(ct, "text/css") {
			t.Errorf("expected Content-Type text/css, got %s", ct)
		}

		body, _ := io.ReadAll(resp.Body)
		if !strings.Contains(string(body), "--bg-base") {
			t.Errorf("expected CSS content, got %s", string(body))
		}
	})

	t.Run("Connection-centric Web UI, safe paste-once credential forms, and discovery", func(t *testing.T) {
		resp, err := client.Get(ts.URL + "/")
		if err != nil {
			t.Fatalf("GET / failed: %v", err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		html := string(body)

		for _, expected := range []string{
			`id="nav-connections"`,
			`id="view-connections"`,
			`id="modal-add-connection"`,
			`id="modal-replace-token"`,
			`id="modal-discovery"`,
			`id="modal-reassign-endpoint"`,
			`id="modal-wa-pair"`,
			`id="add-conn-pairing-view"`,
			`id="add-endpoint-conn-select"`,
			"Connections → discovered parent conversations → Endpoints → Sync Sets",
			"child scopes",
			"admin-only",
			`id="settings-child-context-mode"`,
			"Friendly thread/topic names",
			"Disabling friendly names removes stored names",
			`id="add-conn-help-discord"`,
			`id="add-conn-help-telegram"`,
			`id="add-conn-telegram-mode"`,
			`id="add-conn-mtproto-api-id"`,
			`id="add-conn-mtproto-api-hash"`,
			`id="add-conn-mtproto-phone"`,
			`id="modal-telegram-mtproto"`,
			`id="mtproto-code"`,
			`id="mtproto-password"`,
			`id="mtproto-backfill-group"`,
			`id="add-conn-wa-help"`,
			"Create &amp; Pair",
			"Linked Devices",
			`id="btn-wa-retry-pair"`,
			`id="btn-wa-pair-discover"`,
		} {
			if !strings.Contains(html, expected) {
				t.Fatalf("Admin HTML missing %q", expected)
			}
		}

		dashboardStart := strings.Index(html, `id="view-dashboard"`)
		dashboardEnd := strings.Index(html, `id="view-connections"`)
		if dashboardStart < 0 || dashboardEnd <= dashboardStart {
			t.Fatal("dashboard view markers are missing")
		}
		dashboardHTML := html[dashboardStart:dashboardEnd]
		for _, removed := range []string{
			`id="btn-dash-refresh"`,
			`id="btn-wa-pair"`,
			`id="btn-wa-logout"`,
			`Manage Connections`,
			`Oldest active`,
		} {
			if strings.Contains(dashboardHTML, removed) {
				t.Fatalf("dashboard still contains removed control or column %q", removed)
			}
		}

		// Verify token forms are password type with autocomplete="new-password"
		for _, expected := range []string{
			`type="password" id="conn-bot-token" class="form-input" autocomplete="new-password"`,
			`type="password" id="replace-conn-bot-token" class="form-input" autocomplete="new-password"`,
		} {
			if !strings.Contains(html, expected) {
				t.Fatalf("Admin HTML missing secure token input %q", expected)
			}
		}

		// Ensure no tokens stored in URLs or browser storage
		resp, err = client.Get(ts.URL + "/js/app.js")
		if err != nil {
			t.Fatalf("GET /js/app.js failed: %v", err)
		}
		js, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		appJS := string(js)

		for _, expected := range []string{
			"listConnections",
			"getConnectionStatus",
			"getConnectionDiscovery",
			"createConnection",
			"updateConnection",
			"deleteConnection",
			"pairWhatsAppConnection",
			"openDiscovery",
			"openReassignModal",
			"openReplaceTokenModal",
			"activePairingConnId",
			"connectionId",
			"addConnHelpDiscord",
			"addConnHelpTelegram",
			"classList.toggle('hidden', transport !== 'discord')",
			"classList.toggle('hidden', transport !== 'telegram')",
		} {
			if !strings.Contains(appJS, expected) {
				t.Fatalf("Admin JS missing dynamic connection symbol %q", expected)
			}
		}

		// Verify password input is immediately cleared upon submission and not saved to localStorage/sessionStorage
		for _, forbidden := range []string{
			"localStorage.setItem('token'",
			"sessionStorage.setItem('bot_token'",
			"sessionStorage.setItem('token'",
		} {
			if strings.Contains(appJS, forbidden) {
				t.Fatalf("Admin JS contains forbidden token storage %q", forbidden)
			}
		}
		if strings.Contains(appJS, "closeModal(modalAddConnection);\n        await openWaPairModal(createdID)") {
			t.Fatal("WhatsApp create flow still leaves Add Connection before pairing")
		}

		resp, err = client.Get(ts.URL + "/js/app-base.js")
		if err != nil {
			t.Fatalf("GET /js/app-base.js failed: %v", err)
		}
		baseBytes, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		baseJS := string(baseBytes)
		for _, expected := range []string{"selectAddTelegramMode", "openTelegramMTProto", "setupTelegramMTProto", "submitTelegramMTProtoCode", "submitTelegramMTProtoPassword", "backfillTelegramMTProto", "caps.historyRecovery", "caps.topicDiscovery"} {
			if !strings.Contains(baseJS, expected) {
				t.Fatalf("Admin base JS missing Telegram dual-mode symbol %q", expected)
			}
		}
		for _, forbidden := range []string{"localStorage.setItem('apiHash'", "localStorage.setItem('phone'", "localStorage.setItem('code'", "localStorage.setItem('password'", "sessionStorage.setItem('apiHash'", "sessionStorage.setItem('phone'"} {
			if strings.Contains(baseJS, forbidden) {
				t.Fatalf("Admin JS persists MTProto secret %q", forbidden)
			}
		}
	})

	t.Run("Serves js scripts", func(t *testing.T) {
		scripts := []string{"/js/qrcode.js", "/js/api.js", "/js/router.js", "/js/app.js"}
		for _, s := range scripts {
			resp, err := client.Get(ts.URL + s)
			if err != nil {
				t.Fatalf("GET %s failed: %v", s, err)
			}

			if resp.StatusCode != http.StatusOK {
				resp.Body.Close()
				t.Fatalf("expected status 200 for %s, got %d", s, resp.StatusCode)
			}

			ct := resp.Header.Get("Content-Type")
			if !strings.Contains(ct, "javascript") {
				resp.Body.Close()
				t.Errorf("expected javascript content-type for %s, got %s", s, ct)
			}
			resp.Body.Close()
		}
	})

	t.Run("SPA route fallback to index.html", func(t *testing.T) {
		routes := []string{"/setup", "/login", "/dashboard", "/connections", "/sync-sets", "/settings", "/users", "/membership"}
		for _, route := range routes {
			resp, err := client.Get(ts.URL + route)
			if err != nil {
				t.Fatalf("GET %s failed: %v", route, err)
			}

			if resp.StatusCode != http.StatusOK {
				resp.Body.Close()
				t.Fatalf("expected status 200 for %s, got %d", route, resp.StatusCode)
			}

			ct := resp.Header.Get("Content-Type")
			if !strings.Contains(ct, "text/html") {
				resp.Body.Close()
				t.Errorf("expected Content-Type text/html for %s, got %s", route, ct)
			}

			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if !strings.Contains(string(body), "Message Sync • Admin Console") {
				t.Errorf("expected index.html body for SPA route %s", route)
			}
		}
	})

	t.Run("Method Not Allowed for non-GET/HEAD", func(t *testing.T) {
		resp, err := client.Post(ts.URL+"/css/app.css", "text/plain", strings.NewReader("bad"))
		if err != nil {
			t.Fatalf("POST failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("expected status 405, got %d", resp.StatusCode)
		}
	})

	t.Run("HEAD request works", func(t *testing.T) {
		resp, err := client.Head(ts.URL + "/")
		if err != nil {
			t.Fatalf("HEAD / failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("expected status 200 for HEAD, got %d", resp.StatusCode)
		}
	})
}
