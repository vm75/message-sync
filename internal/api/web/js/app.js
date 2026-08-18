/**
 * Message Sync • Main Application Controller
 * Handles View transitions, Auth flows, Toast notifications, and Form validations.
 */

(function () {
  'use strict';

  // DOM Elements
  const headerEl = document.getElementById('app-header');
  const toastContainer = document.getElementById('toast-container');
  const btnLogout = document.getElementById('btn-logout');

  // Views
  const views = {
    loading: document.getElementById('view-loading'),
    setup: document.getElementById('view-setup'),
    login: document.getElementById('view-login'),
    dashboard: document.getElementById('view-dashboard')
  };

  // Setup Form Elements
  const formSetup = document.getElementById('form-setup');
  const setupPasswordInput = document.getElementById('setup-password');
  const setupConfirmPasswordInput = document.getElementById('setup-confirm-password');
  const setupStrengthFill = document.getElementById('setup-strength-fill');
  const setupPasswordHint = document.getElementById('setup-password-hint');
  const setupConfirmHint = document.getElementById('setup-confirm-hint');
  const setupAlert = document.getElementById('setup-alert');
  const btnSubmitSetup = document.getElementById('btn-submit-setup');

  // Login Form Elements
  const formLogin = document.getElementById('form-login');
  const loginPasswordInput = document.getElementById('login-password');
  const loginAlert = document.getElementById('login-alert');
  const btnSubmitLogin = document.getElementById('btn-submit-login');

  // Dashboard Stats Elements
  const dashWaStatus = document.getElementById('dash-wa-status');
  const dashGroupsCount = document.getElementById('dash-groups-count');
  const dashSyncSetsCount = document.getElementById('dash-sync-sets-count');

  // State
  let isSetup = null;
  let isAuthenticated = false;

  /**
   * Toast Notification Helper
   */
  function showToast(message, type = 'success', duration = 3500) {
    if (!toastContainer) return;

    const toast = document.createElement('div');
    toast.className = `toast toast-${type}`;
    
    let iconSvg = '';
    if (type === 'success') {
      iconSvg = `<svg class="icon icon-sm toast-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
        <path d="M22 11.08V12a10 10 0 1 1-5.93-9.14"></path>
        <polyline points="22 4 12 14.01 9 11.01"></polyline>
      </svg>`;
    } else {
      iconSvg = `<svg class="icon icon-sm toast-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
        <circle cx="12" cy="12" r="10"></circle>
        <line x1="12" y1="8" x2="12" y2="12"></line>
        <line x1="12" y1="16" x2="12.01" y2="16"></line>
      </svg>`;
    }

    toast.innerHTML = `
      ${iconSvg}
      <div class="toast-message">${escapeHtml(message)}</div>
    `;

    toastContainer.appendChild(toast);

    setTimeout(() => {
      toast.style.opacity = '0';
      toast.style.transform = 'translateX(20px)';
      setTimeout(() => toast.remove(), 250);
    }, duration);
  }

  function escapeHtml(text) {
    const div = document.createElement('div');
    div.textContent = text;
    return div.innerHTML;
  }

  /**
   * Switch View
   */
  function switchView(targetName) {
    Object.keys(views).forEach((key) => {
      const el = views[key];
      if (el) {
        if (key === targetName) {
          el.classList.remove('hidden');
        } else {
          el.classList.add('hidden');
        }
      }
    });

    if (isAuthenticated && targetName !== 'setup' && targetName !== 'login' && targetName !== 'loading') {
      headerEl.classList.remove('hidden');
    } else {
      headerEl.classList.add('hidden');
    }
  }

  /**
   * Password Visibility Toggle Setup
   */
  function setupPasswordToggles() {
    document.querySelectorAll('.btn-toggle-password').forEach((btn) => {
      btn.addEventListener('click', () => {
        const targetId = btn.getAttribute('data-target');
        const input = document.getElementById(targetId);
        if (!input) return;

        const isPassword = input.type === 'password';
        input.type = isPassword ? 'text' : 'password';

        // Toggle icon visual
        btn.innerHTML = isPassword
          ? `<svg class="icon icon-sm" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
              <path d="M17.94 17.94A10.07 10.07 0 0 1 12 20c-7 0-11-8-11-8a18.45 18.45 0 0 1 5.06-5.94M9.9 4.24A9.12 9.12 0 0 1 12 4c7 0 11 8 11 8a18.5 18.5 0 0 1-2.16 3.19m-6.72-1.07a3 3 0 1 1-4.24-4.24"></path>
              <line x1="1" y1="1" x2="23" y2="23"></line>
            </svg>`
          : `<svg class="icon icon-sm" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
              <path d="M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8z"></path>
              <circle cx="12" cy="12" r="3"></circle>
            </svg>`;
      });
    });
  }

  /**
   * Password Strength Calculator
   */
  function evaluatePasswordStrength(password) {
    if (!password || password.length < 8) return 0;
    let score = 1;
    if (password.length >= 12) score++;
    if (/[A-Z]/.test(password) && /[a-z]/.test(password)) score++;
    if (/[0-9]/.test(password) && /[^A-Za-z0-9]/.test(password)) score++;
    return score;
  }

  /**
   * Setup Form Live Validation
   */
  function setupFormValidation() {
    function updateSetupValidation() {
      const pwd = setupPasswordInput.value;
      const confirmPwd = setupConfirmPasswordInput.value;

      // Password Length & Strength
      if (pwd.length === 0) {
        setupStrengthFill.className = 'strength-fill';
        setupPasswordHint.className = 'form-hint';
        setupPasswordHint.textContent = 'Between 8 and 72 characters';
      } else if (pwd.length < 8) {
        setupStrengthFill.className = 'strength-fill strength-weak';
        setupPasswordHint.className = 'form-hint hint-error';
        setupPasswordHint.textContent = `Too short (${pwd.length}/8 characters minimum)`;
      } else if (pwd.length > 72) {
        setupStrengthFill.className = 'strength-fill strength-weak';
        setupPasswordHint.className = 'form-hint hint-error';
        setupPasswordHint.textContent = `Too long (${pwd.length}/72 characters maximum)`;
      } else {
        const strength = evaluatePasswordStrength(pwd);
        if (strength <= 1) {
          setupStrengthFill.className = 'strength-fill strength-weak';
          setupPasswordHint.className = 'form-hint';
          setupPasswordHint.textContent = 'Weak password';
        } else if (strength <= 2) {
          setupStrengthFill.className = 'strength-fill strength-fair';
          setupPasswordHint.className = 'form-hint';
          setupPasswordHint.textContent = 'Fair password';
        } else {
          setupStrengthFill.className = 'strength-fill strength-strong';
          setupPasswordHint.className = 'form-hint hint-success';
          setupPasswordHint.textContent = 'Strong password';
        }
      }

      // Confirm Password Match
      if (confirmPwd.length > 0) {
        if (confirmPwd !== pwd) {
          setupConfirmHint.className = 'form-hint hint-error';
          setupConfirmHint.textContent = 'Passwords do not match';
        } else {
          setupConfirmHint.className = 'form-hint hint-success';
          setupConfirmHint.textContent = 'Passwords match';
        }
      } else {
        setupConfirmHint.textContent = '';
      }
    }

    setupPasswordInput.addEventListener('input', updateSetupValidation);
    setupConfirmPasswordInput.addEventListener('input', updateSetupValidation);
  }

  /**
   * First-Use Setup Submit Handler
   */
  async function handleSetupSubmit(e) {
    e.preventDefault();
    setupAlert.classList.add('hidden');

    const password = setupPasswordInput.value;
    const confirmPassword = setupConfirmPasswordInput.value;

    if (password.length < 8 || password.length > 72) {
      setupAlert.textContent = 'Password must be between 8 and 72 characters.';
      setupAlert.classList.remove('hidden');
      setupPasswordInput.focus();
      return;
    }

    if (password !== confirmPassword) {
      setupAlert.textContent = 'Passwords do not match.';
      setupAlert.classList.remove('hidden');
      setupConfirmPasswordInput.focus();
      return;
    }

    // Set loading state
    setButtonLoading(btnSubmitSetup, true);

    try {
      await window.API.setupPassword(password);
      isSetup = true;
      isAuthenticated = true;
      showToast('Admin password initialized successfully! Welcome to Message Sync.', 'success');
      window.Router.navigate('dashboard');
    } catch (err) {
      setupAlert.textContent = err.message || 'Failed to initialize password.';
      setupAlert.classList.remove('hidden');
    } finally {
      setButtonLoading(btnSubmitSetup, false);
    }
  }

  /**
   * Login Submit Handler
   */
  async function handleLoginSubmit(e) {
    e.preventDefault();
    loginAlert.classList.add('hidden');

    const password = loginPasswordInput.value;
    if (!password) {
      loginAlert.textContent = 'Please enter your administrator password.';
      loginAlert.classList.remove('hidden');
      loginPasswordInput.focus();
      return;
    }

    setButtonLoading(btnSubmitLogin, true);

    try {
      await window.API.login(password);
      isAuthenticated = true;
      showToast('Signed in successfully.', 'success');
      loginPasswordInput.value = '';
      window.Router.navigate('dashboard');
    } catch (err) {
      loginAlert.textContent = err.message || 'Invalid administrator password.';
      loginAlert.classList.remove('hidden');
      loginPasswordInput.focus();
    } finally {
      setButtonLoading(btnSubmitLogin, false);
    }
  }

  /**
   * Logout Handler
   */
  async function handleLogout() {
    try {
      await window.API.logout();
    } catch (e) {
      console.warn('Logout error', e);
    } finally {
      isAuthenticated = false;
      showToast('Signed out of admin console.', 'success');
      window.Router.navigate('login');
    }
  }

  function setButtonLoading(btn, loading) {
    if (!btn) return;
    const textSpan = btn.querySelector('.btn-text');
    const spinner = btn.querySelector('.btn-spinner');

    if (loading) {
      btn.disabled = true;
      if (textSpan) textSpan.classList.add('hidden');
      if (spinner) spinner.classList.remove('hidden');
    } else {
      btn.disabled = false;
      if (textSpan) textSpan.classList.remove('hidden');
      if (spinner) spinner.classList.add('hidden');
    }
  }

  /**
   * Load dashboard statistics
   */
  async function loadDashboardStats() {
    try {
      const [waStatus, groups, syncSets] = await Promise.allSettled([
        window.API.getWhatsAppStatus(),
        window.API.getGroups(),
        window.API.getSyncSets()
      ]);

      if (dashWaStatus) {
        if (waStatus.status === 'fulfilled' && waStatus.value) {
          const s = waStatus.value;
          dashWaStatus.textContent = s.isConnected ? 'Connected' : s.isLoggedIn ? 'Logged In (Idle)' : 'Unlinked';
          dashWaStatus.className = s.isConnected ? 'stat-value text-success' : 'stat-value';
        } else {
          dashWaStatus.textContent = 'Standby';
        }
      }

      if (dashGroupsCount) {
        if (groups.status === 'fulfilled' && Array.isArray(groups.value)) {
          dashGroupsCount.textContent = `${groups.value.length} Group${groups.value.length === 1 ? '' : 's'}`;
        } else {
          dashGroupsCount.textContent = '0 Groups';
        }
      }

      if (dashSyncSetsCount) {
        if (syncSets.status === 'fulfilled' && Array.isArray(syncSets.value)) {
          dashSyncSetsCount.textContent = `${syncSets.value.length} Set${syncSets.value.length === 1 ? '' : 's'}`;
        } else {
          dashSyncSetsCount.textContent = '0 Sets';
        }
      }
    } catch (e) {
      console.warn('Dashboard stats error', e);
    }
  }

  /**
   * Navigation tab clicks
   */
  function setupNavTabs() {
    document.querySelectorAll('.nav-tab').forEach((tab) => {
      tab.addEventListener('click', () => {
        const targetTab = tab.getAttribute('data-tab');
        document.querySelectorAll('.nav-tab').forEach((t) => t.classList.remove('active'));
        tab.classList.add('active');
        window.Router.navigate(targetTab);
      });
    });

    document.querySelectorAll('.stat-card').forEach((card) => {
      card.addEventListener('click', () => {
        const action = card.getAttribute('data-action');
        if (action) {
          window.Router.navigate(action);
        }
      });
    });
  }

  /**
   * App Initialization
   */
  async function initApp() {
    setupPasswordToggles();
    setupFormValidation();
    setupNavTabs();

    if (formSetup) formSetup.addEventListener('submit', handleSetupSubmit);
    if (formLogin) formLogin.addEventListener('submit', handleLoginSubmit);
    if (btnLogout) btnLogout.addEventListener('click', handleLogout);

    // Listen for unauthorized 401s from API
    window.addEventListener('auth:unauthorized', () => {
      if (isAuthenticated) {
        isAuthenticated = false;
        showToast('Your session has expired. Please sign in again.', 'danger');
        window.Router.navigate('login');
      }
    });

    // Configure Router Guards
    window.Router.beforeEach(async (route) => {
      // First check auth setup status if not known
      if (isSetup === null) {
        try {
          const res = await window.API.getAuthStatus();
          isSetup = res && res.isSetup === true;
        } catch (err) {
          console.error('Failed to get auth status', err);
          isSetup = false;
        }
      }

      // If app is not setup, must go to setup
      if (!isSetup) {
        return 'setup';
      }

      // If already setup, cannot go to setup
      if (route === 'setup') {
        return 'login';
      }

      // If going to login or already marked authenticated, check validity
      if (!isAuthenticated) {
        try {
          // Probe with an authenticated call (e.g. getConfig) to verify session
          await window.API.getConfig();
          isAuthenticated = true;
        } catch (err) {
          isAuthenticated = false;
          if (route !== 'login') {
            return 'login';
          }
        }
      }

      // If authenticated and trying to view login, redirect to dashboard
      if (isAuthenticated && route === 'login') {
        return 'dashboard';
      }

      return route;
    });

    // Register routes
    window.Router.addRoute('setup', () => {
      switchView('setup');
      if (setupPasswordInput) setupPasswordInput.focus();
    });

    window.Router.addRoute('login', () => {
      switchView('login');
      if (loginPasswordInput) loginPasswordInput.focus();
    });

    window.Router.addRoute('dashboard', () => {
      switchView('dashboard');
      loadDashboardStats();
    });

    // Fallback/Placeholder routes for Ticket 6 tabs
    ['whatsapp', 'groups', 'sync-sets', 'settings'].forEach((tabName) => {
      window.Router.addRoute(tabName, () => {
        switchView('dashboard');
        // Update active tab visual
        document.querySelectorAll('.nav-tab').forEach((t) => {
          if (t.getAttribute('data-tab') === tabName) {
            t.classList.add('active');
          } else {
            t.classList.remove('active');
          }
        });
        loadDashboardStats();
      });
    });

    // Initialize router
    await window.Router.init();
  }

  // Run when DOM is ready
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', initApp);
  } else {
    initApp();
  }
})();
