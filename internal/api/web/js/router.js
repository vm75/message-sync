/**
 * Message Sync • Client-Side Router
 * Lightweight hash-based router with stateful authentication guards
 */

(function () {
  'use strict';

  class Router {
    constructor() {
      this.routes = {};
      this.currentRoute = '';
      this.isSetup = null;
      this.isAuthenticated = false;
      this.beforeHook = null;

      window.addEventListener('hashchange', () => this.handleRouteChange());
    }

    /**
     * Register a route handler
     */
    addRoute(route, handler) {
      this.routes[route] = handler;
    }

    /**
     * Set a global guard before routing occurs
     */
    beforeEach(hook) {
      this.beforeHook = hook;
    }

    /**
     * Navigate to a hash route
     */
    navigate(route) {
      const targetHash = route.startsWith('#') ? route : `#${route}`;
      if (window.location.hash !== targetHash) {
        window.location.hash = targetHash;
      } else {
        this.handleRouteChange();
      }
    }

    /**
     * Get the current sanitized route name (without #)
     */
    getRoute() {
      const hash = window.location.hash.slice(1).trim();
      return hash || 'dashboard';
    }

    /**
     * Trigger route resolution
     */
    async handleRouteChange() {
      const route = this.getRoute();

      if (this.beforeHook) {
        const allowedRoute = await this.beforeHook(route);
        if (allowedRoute && allowedRoute !== route) {
          this.navigate(allowedRoute);
          return;
        }
      }

      this.currentRoute = route;
      const handler = this.routes[route] || this.routes['*'] || this.routes['dashboard'];
      if (typeof handler === 'function') {
        handler(route);
      }
    }

    /**
     * Initialize router and check auth setup status
     */
    async init() {
      await this.handleRouteChange();
    }
  }

  window.Router = new Router();
})();
