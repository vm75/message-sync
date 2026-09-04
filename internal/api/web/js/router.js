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
      const [route] = hash.split('?');
      return route || 'dashboard';
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

  // Presentation-only dashboard enhancement. It installs a MutationObserver
  // before app-base.js begins polling, so every delivery refresh is reformatted
  // without changing the controller or delivery API semantics.
  const deliveryHealthPresentation = document.createElement('script');
  deliveryHealthPresentation.src = '/js/delivery-health-polish.js?v=1';
  deliveryHealthPresentation.async = false;
  document.body.appendChild(deliveryHealthPresentation);

  // Client-only feature preference. Membership review is opt-in and hidden by
  // default without changing any server-side membership behavior or config.
  const membershipFeature = document.createElement('script');
  membershipFeature.src = '/js/membership-feature.js?v=1';
  membershipFeature.async = false;
  document.body.appendChild(membershipFeature);
})();
