/**
 * Message Sync • API Client
 * Self-contained client wrapper for the backend REST API
 */

(function () {
  'use strict';

  const API = {
    /** Authentication is provided by the HttpOnly cookie. */
    getToken() {
      return '';
    },

    /** Kept as a no-op so API responses remain compatible with callers. */
    setToken(token) {
      // Authentication is carried by the HttpOnly session cookie.
    },

    /**
     * Clear the stored session token.
     */
    clearToken() {
      this.setToken('');
    },

    /**
     * Execute a JSON request to the API with standard error handling and token inclusion.
     */
    async request(path, options = {}) {
      const url = path.startsWith('/') ? path : `/${path}`;
      const headers = {
        'Accept': 'application/json',
        ...(options.headers || {})
      };

      const token = this.getToken();
      if (token && !headers['Authorization']) {
        headers['Authorization'] = `Bearer ${token}`;
      }

      if (options.body && typeof options.body === 'object' && !(options.body instanceof FormData)) {
        headers['Content-Type'] = 'application/json';
        options.body = JSON.stringify(options.body);
      }

      const fetchOptions = {
        ...options,
        headers,
        credentials: 'same-origin' // Ensures HttpOnly session cookies are transmitted
      };

      try {
        const response = await fetch(url, fetchOptions);
        const isJson = (response.headers.get('content-type') || '').includes('application/json');
        const data = isJson ? await response.json() : null;

        if (!response.ok) {
          if (response.status === 401 && !url.includes('/api/auth/login')) {
            this.clearToken();
            window.dispatchEvent(new CustomEvent('auth:unauthorized'));
          }
          const errorMessage = data && data.error ? data.error : `Request failed with status ${response.status}`;
          const error = new Error(errorMessage);
          error.status = response.status;
          error.data = data;
          throw error;
        }

        return data;
      } catch (err) {
        throw err;
      }
    },

    /**
     * Auth Endpoints
     */
    async getAuthStatus() {
      return this.request('/api/auth/status');
    },
    async getCurrentUser() { return this.request('/api/auth/me'); },

    async setupPassword(username, password) {
      const res = await this.request('/api/auth/setup', {
        method: 'POST',
        body: { username, password }
      });
      if (res && res.token) {
        this.setToken(res.token);
      }
      return res;
    },

    async login(username, password) {
      const res = await this.request('/api/auth/login', {
        method: 'POST',
        body: { username, password }
      });
      if (res && res.token) {
        this.setToken(res.token);
      }
      return res;
    },

    async logout() {
      try {
        await this.request('/api/auth/logout', { method: 'POST' });
      } finally {
        this.clearToken();
      }
    },

    async changePassword(currentPassword, newPassword) {
      return this.request('/api/auth/change-password', {
        method: 'POST',
        body: { currentPassword, newPassword }
      });
    },
    async redeemInvite(token, username, password) { const res = await this.request('/api/auth/invite/redeem', { method: 'POST', body: { token, username, password } }); if (res && res.token) this.setToken(res.token); return res; },
    async resetPassword(token, password) { return this.request('/api/auth/reset-password', { method: 'POST', body: { token, password } }); },

    async getUsers() { return this.request('/api/users'); },
    async createInvite(role, ttlHours) { return this.request('/api/users/invites', { method: 'POST', body: { role, ttlHours } }); },
    async setUserActive(id, active) { return this.request(`/api/users/${encodeURIComponent(id)}/active`, { method: 'POST', body: { active } }); },
    async createResetToken(id) { return this.request(`/api/users/${encodeURIComponent(id)}/reset-token`, { method: 'POST' }); },
    async getMembershipRequests(filters = {}) { const query = new URLSearchParams(Object.entries(filters).filter(([, value]) => value)); return this.request('/api/verification/requests' + (query.toString() ? `?${query}` : '')); },
    async getMembershipRequest(id) { return this.request(`/api/verification/requests/${encodeURIComponent(id)}`); },
    async decideMembership(id, action) { return this.request(`/api/verification/requests/${encodeURIComponent(id)}/decision`, { method: 'POST', body: { action } }); },
    async getVerificationPipelines() { return this.request('/api/verification/pipelines'); },
    async createVerificationPipeline(body) { return this.request('/api/verification/pipelines', { method: 'POST', body }); },
    async updateVerificationPipeline(id, body) { return this.request(`/api/verification/pipelines/${encodeURIComponent(id)}`, { method: 'PUT', body }); },
    async deleteVerificationPipeline(id) { return this.request(`/api/verification/pipelines/${encodeURIComponent(id)}`, { method: 'DELETE' }); },

    /**
     * Configuration & Status Endpoints
     */
    async getConfig() {
      return this.request('/api/config');
    },

    async updateConfig(configData) {
      return this.request('/api/config', {
        method: 'PUT',
        body: configData
      });
    },

    /**
     * WhatsApp Lifecycle Endpoints
     */
    async getWhatsAppStatus() {
      return this.request('/api/whatsapp/status');
    },

    async pairWhatsApp() {
      return this.request('/api/whatsapp/pair', {
        method: 'POST'
      });
    },

    async cancelPairWhatsApp() {
      return this.request('/api/whatsapp/pair', {
        method: 'DELETE'
      });
    },

    async logoutWhatsApp() {
      return this.request('/api/whatsapp/logout', {
        method: 'POST'
      });
    },

    async getWhatsAppJoinedGroups() {
      return this.request('/api/whatsapp/groups');
    },

    /**
     * Discord Admin Endpoints
     */
    async getDiscordStatus() {
      return this.request('/api/discord/status');
    },

    async getDiscordChannels() {
      return this.request('/api/discord/channels');
    },

    /**
     * Telegram Admin Endpoints
     */
    async getTelegramStatus() {
      return this.request('/api/telegram/status');
    },

    async getTelegramChats() {
      return this.request('/api/telegram/chats');
    },

    async getDeliveryStatus() {
      return this.request('/api/delivery/status');
    },

    /**
     * Transport-neutral Endpoint CRUD
     */
    async getEndpoints() {
      return this.request('/api/endpoints');
    },

    async getEndpoint(alias) {
      return this.request(`/api/endpoints/${encodeURIComponent(alias)}`);
    },

    async createEndpoint(endpointData) {
      return this.request('/api/endpoints', {
        method: 'POST',
        body: endpointData
      });
    },

    async updateEndpoint(alias, endpointData) {
      return this.request(`/api/endpoints/${encodeURIComponent(alias)}`, {
        method: 'PUT',
        body: endpointData
      });
    },

    async deleteEndpoint(alias) {
      return this.request(`/api/endpoints/${encodeURIComponent(alias)}`, {
        method: 'DELETE'
      });
    },

    /**
     * Sync Sets CRUD Endpoints
     */
    async getSyncSets() {
      return this.request('/api/sync-sets');
    },

    async getSyncSet(id) {
      return this.request(`/api/sync-sets/${encodeURIComponent(id)}`);
    },

    async createSyncSet(syncSetData) {
      return this.request('/api/sync-sets', {
        method: 'POST',
        body: syncSetData
      });
    },

    async updateSyncSet(id, syncSetData) {
      return this.request(`/api/sync-sets/${encodeURIComponent(id)}`, {
        method: 'PUT',
        body: syncSetData
      });
    },

    async deleteSyncSet(id) {
      return this.request(`/api/sync-sets/${encodeURIComponent(id)}`, {
        method: 'DELETE'
      });
    }
  };

  window.API = API;
})();
