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

	t.Run("Discord admin UI is deployment-only and transport-aware", func(t *testing.T) {
		resp, err := client.Get(ts.URL + "/")
		if err != nil {
			t.Fatalf("GET / failed: %v", err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		html := string(body)
		for _, expected := range []string{
			`data-tab="discord"`,
			"Discord Bot & Channel Discovery",
			"DISCORD_BOT_TOKEN",
			"DISCORD_BOT_TOKEN_FILE",
			"Configured Endpoints",
		} {
			if !strings.Contains(html, expected) {
				t.Fatalf("Discord admin HTML missing %q", expected)
			}
		}
		for _, forbidden := range []string{
			`id="discord-token"`,
			`name="discord-token"`,
			`type="password" id="discord`,
		} {
			if strings.Contains(html, forbidden) {
				t.Fatalf("Discord admin HTML contains credential input marker %q", forbidden)
			}
		}

		resp, err = client.Get(ts.URL + "/js/app.js")
		if err != nil {
			t.Fatalf("GET /js/app.js failed: %v", err)
		}
		js, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		appJS := string(js)
		for _, expected := range []string{
			"missing_permission",
			"Manage Webhooks required",
			"Webhook ready",
			"createEndpoint",
			"getDiscordChannels",
			"cachedDiscordChannels",
		} {
			if !strings.Contains(appJS, expected) {
				t.Fatalf("Discord admin JS missing %q", expected)
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
		routes := []string{"/setup", "/login", "/dashboard", "/whatsapp", "/discord", "/groups", "/sync-sets", "/settings"}
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
