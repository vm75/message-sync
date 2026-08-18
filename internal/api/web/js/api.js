/**
 * Message Sync • API Client
 * Self-contained client wrapper for the backend REST API
 */

(function () {
  'use strict';

  const TOKEN_STORAGE_KEY = 'msg_sync_token';

  const API = {
    /**
     * Get the stored session token from sessionStorage.
     */
    getToken() {
      try {
        return sessionStorage.getItem(TOKEN_STORAGE_KEY) || '';
      } catch (e) {
        return '';
      }
    },

    /**
     * Store the session token in sessionStorage.
     */
    setToken(token) {
      try {
        if (token) {
          sessionStorage.setItem(TOKEN_STORAGE_KEY, token);
        } else {
          sessionStorage.removeItem(TOKEN_STORAGE_KEY);
        }
      } catch (e) {
        console.warn('Unable to access sessionStorage', e);
      }
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

    async setupPassword(password) {
      const res = await this.request('/api/auth/setup', {
        method: 'POST',
        body: { password }
      });
      if (res && res.token) {
        this.setToken(res.token);
      }
      return res;
    },

    async login(password) {
      const res = await this.request('/api/auth/login', {
        method: 'POST',
        body: { password }
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

    /**
     * Groups CRUD Endpoints
     */
    async getGroups() {
      return this.request('/api/groups');
    },

    async getGroup(alias) {
      return this.request(`/api/groups/${encodeURIComponent(alias)}`);
    },

    async createGroup(groupData) {
      return this.request('/api/groups', {
        method: 'POST',
        body: groupData
      });
    },

    async updateGroup(alias, groupData) {
      return this.request(`/api/groups/${encodeURIComponent(alias)}`, {
        method: 'PUT',
        body: groupData
      });
    },

    async deleteGroup(alias) {
      return this.request(`/api/groups/${encodeURIComponent(alias)}`, {
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
