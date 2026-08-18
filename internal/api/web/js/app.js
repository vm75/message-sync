/**
 * Message Sync • Main Application Controller
 * Handles View routing, Auth flows, WhatsApp pairing/QR rendering,
 * Groups & SyncSets CRUD management, Settings configuration, Modals, and Toasts.
 */

(function () {
  'use strict';

  // Validation Regular Expressions
  const ALIAS_REGEX = /^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$/;
  const GROUP_JID_REGEX = /^[0-9]+(-[0-9]+)?@g\.us$/;
  const SYNC_SET_ID_REGEX = /^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$/;

  // DOM Elements
  const headerEl = document.getElementById('app-header');
  const toastContainer = document.getElementById('toast-container');
  const btnLogout = document.getElementById('btn-logout');

  // Views
  const views = {
    loading: document.getElementById('view-loading'),
    setup: document.getElementById('view-setup'),
    login: document.getElementById('view-login'),
    dashboard: document.getElementById('view-dashboard'),
    whatsapp: document.getElementById('view-whatsapp'),
    groups: document.getElementById('view-groups'),
    'sync-sets': document.getElementById('view-sync-sets'),
    settings: document.getElementById('view-settings')
  };

  // Auth Form Elements
  const formSetup = document.getElementById('form-setup');
  const setupPasswordInput = document.getElementById('setup-password');
  const setupConfirmPasswordInput = document.getElementById('setup-confirm-password');
  const setupStrengthFill = document.getElementById('setup-strength-fill');
  const setupPasswordHint = document.getElementById('setup-password-hint');
  const setupConfirmHint = document.getElementById('setup-confirm-hint');
  const setupAlert = document.getElementById('setup-alert');
  const btnSubmitSetup = document.getElementById('btn-submit-setup');

  const formLogin = document.getElementById('form-login');
  const loginPasswordInput = document.getElementById('login-password');
  const loginAlert = document.getElementById('login-alert');
  const btnSubmitLogin = document.getElementById('btn-submit-login');

  // Dashboard Stats Elements
  const dashWaStatus = document.getElementById('dash-wa-status');
  const dashGroupsCount = document.getElementById('dash-groups-count');
  const dashSyncSetsCount = document.getElementById('dash-sync-sets-count');
  const dashConfigMode = document.getElementById('dash-config-mode');

  // WhatsApp View Elements
  const btnWaRefresh = document.getElementById('btn-wa-refresh');
  const waStatusIndicator = document.getElementById('wa-status-indicator');
  const waStatusText = document.getElementById('wa-status-text');
  const waStatusDesc = document.getElementById('wa-status-desc');
  const btnWaPair = document.getElementById('btn-wa-pair');
  const btnWaCancel = document.getElementById('btn-wa-cancel');
  const waQrSection = document.getElementById('wa-qr-section');
  const waQrCanvas = document.getElementById('wa-qr-canvas');
  const waQrLoader = document.getElementById('wa-qr-loader');
  const waCountdownBadge = document.getElementById('wa-countdown-badge');
  const waCountdownText = document.getElementById('wa-countdown-text');

  // Groups View Elements
  const btnOpenAddGroup = document.getElementById('btn-open-add-group');
  const searchGroupsInput = document.getElementById('search-groups');
  const groupsCountBadge = document.getElementById('groups-count-badge');
  const groupsTableBody = document.getElementById('groups-table-body');
  const groupsEmptyState = document.getElementById('groups-empty-state');

  // Group Modal Elements
  const modalGroup = document.getElementById('modal-group');
  const modalGroupTitle = document.getElementById('modal-group-title');
  const formGroup = document.getElementById('form-group');
  const modalGroupAlert = document.getElementById('modal-group-alert');
  const inputGroupAlias = document.getElementById('input-group-alias');
  const inputGroupJid = document.getElementById('input-group-jid');
  const selectGroupSyncSet = document.getElementById('select-group-sync-set');
  const btnSubmitGroup = document.getElementById('btn-submit-group');

  // Sync Sets View Elements
  const btnOpenAddSyncSet = document.getElementById('btn-open-add-sync-set');
  const searchSyncSetsInput = document.getElementById('search-sync-sets');
  const syncSetsCountBadge = document.getElementById('sync-sets-count-badge');
  const syncSetsContainer = document.getElementById('sync-sets-container');
  const syncSetsEmptyState = document.getElementById('sync-sets-empty-state');

  // Sync Set Modal Elements
  const modalSyncSet = document.getElementById('modal-sync-set');
  const modalSyncSetTitle = document.getElementById('modal-sync-set-title');
  const formSyncSet = document.getElementById('form-sync-set');
  const modalSyncSetAlert = document.getElementById('modal-sync-set-alert');
  const inputSyncSetId = document.getElementById('input-sync-set-id');
  const syncSetGroupsChecklist = document.getElementById('sync-set-groups-checklist');
  const btnSubmitSyncSet = document.getElementById('btn-submit-sync-set');

  // Settings View Elements
  const formSettings = document.getElementById('form-settings');
  const settingsAlert = document.getElementById('settings-alert');
  const settingsUsernameMode = document.getElementById('settings-username-mode');
  const settingsMediaEnabled = document.getElementById('settings-media-enabled');
  const settingsMediaMaxSize = document.getElementById('settings-media-max-size');
  const settingsRecoveryEnabled = document.getElementById('settings-recovery-enabled');
  const settingsRecoveryMaxAge = document.getElementById('settings-recovery-max-age');
  const settingsRecoveryMaxMsgs = document.getElementById('settings-recovery-max-msgs');
  const settingsRetentionDays = document.getElementById('settings-retention-days');
  const btnResetSettings = document.getElementById('btn-reset-settings');
  const btnSaveSettings = document.getElementById('btn-save-settings');

  // Universal Confirmation Modal
  const modalConfirm = document.getElementById('modal-confirm');
  const modalConfirmTitle = document.getElementById('modal-confirm-title');
  const modalConfirmMsg = document.getElementById('modal-confirm-msg');
  const btnConfirmOk = document.getElementById('btn-confirm-ok');

  // Application State
  let isSetup = null;
  let isAuthenticated = false;
  let cachedGroups = [];
  let cachedSyncSets = [];
  let cachedConfig = null;
  let editingGroupAlias = null;
  let editingSyncSetId = null;
  let confirmCallback = null;

  // WhatsApp State & Timers
  let waPollTimer = null;
  let waCountdownTimer = null;
  let qrExpiresAt = null;

  /**
   * Escape HTML to prevent XSS
   */
  function escapeHtml(text) {
    if (text === null || text === undefined) return '';
    const div = document.createElement('div');
    div.textContent = String(text);
    return div.innerHTML;
  }

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

  /**
   * Button loading state helper
   */
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
   * Modal Management
   */
  function openModal(modalEl) {
    if (!modalEl) return;
    modalEl.classList.remove('hidden');
    document.body.style.overflow = 'hidden';
  }

  function closeModal(modalEl) {
    if (!modalEl) return;
    modalEl.classList.add('hidden');
    document.body.style.overflow = '';
  }

  function setupModalListeners() {
    // Close modal triggers
    document.querySelectorAll('[data-modal]').forEach((btn) => {
      btn.addEventListener('click', (e) => {
        const modalId = btn.getAttribute('data-modal');
        const target = document.getElementById(modalId);
        if (target) closeModal(target);
      });
    });

    // Close on backdrop click
    document.querySelectorAll('.modal-overlay').forEach((overlay) => {
      overlay.addEventListener('click', (e) => {
        if (e.target === overlay) {
          closeModal(overlay);
        }
      });
    });

    // Close on Escape key
    window.addEventListener('keydown', (e) => {
      if (e.key === 'Escape') {
        document.querySelectorAll('.modal-overlay:not(.hidden)').forEach((modal) => {
          closeModal(modal);
        });
      }
    });

    // Confirm button click
    if (btnConfirmOk) {
      btnConfirmOk.addEventListener('click', async () => {
        if (typeof confirmCallback === 'function') {
          setButtonLoading(btnConfirmOk, true);
          try {
            await confirmCallback();
            closeModal(modalConfirm);
          } catch (err) {
            showToast(err.message || 'Action failed', 'danger');
          } finally {
            setButtonLoading(btnConfirmOk, false);
          }
        }
      });
    }
  }

  function showConfirmDialog({ title, message, btnText = 'Delete', isDanger = true, onConfirm }) {
    if (modalConfirmTitle) modalConfirmTitle.textContent = title;
    if (modalConfirmMsg) modalConfirmMsg.textContent = message;
    if (btnConfirmOk) {
      const textSpan = btnConfirmOk.querySelector('.btn-text');
      if (textSpan) textSpan.textContent = btnText;
      btnConfirmOk.className = isDanger ? 'btn btn-danger' : 'btn btn-primary';
    }
    confirmCallback = onConfirm;
    openModal(modalConfirm);
  }

  /**
   * Switch Active View & Nav Tab
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

    const isPublicView = targetName === 'setup' || targetName === 'login' || targetName === 'loading';
    if (isAuthenticated && !isPublicView) {
      headerEl.classList.remove('hidden');
      document.querySelectorAll('.nav-tab').forEach((tab) => {
        if (tab.getAttribute('data-tab') === targetName) {
          tab.classList.add('active');
        } else {
          tab.classList.remove('active');
        }
      });
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
   * Setup Form Live Validation
   */
  function setupFormValidation() {
    function evaluatePasswordStrength(password) {
      if (!password || password.length < 8) return 0;
      let score = 1;
      if (password.length >= 12) score++;
      if (/[A-Z]/.test(password) && /[a-z]/.test(password)) score++;
      if (/[0-9]/.test(password) && /[^A-Za-z0-9]/.test(password)) score++;
      return score;
    }

    function updateSetupValidation() {
      if (!setupPasswordInput) return;
      const pwd = setupPasswordInput.value;
      const confirmPwd = setupConfirmPasswordInput ? setupConfirmPasswordInput.value : '';

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

      if (confirmPwd.length > 0) {
        if (confirmPwd !== pwd) {
          setupConfirmHint.className = 'form-hint hint-error';
          setupConfirmHint.textContent = 'Passwords do not match';
        } else {
          setupConfirmHint.className = 'form-hint hint-success';
          setupConfirmHint.textContent = 'Passwords match';
        }
      } else if (setupConfirmHint) {
        setupConfirmHint.textContent = '';
      }
    }

    if (setupPasswordInput) setupPasswordInput.addEventListener('input', updateSetupValidation);
    if (setupConfirmPasswordInput) setupConfirmPasswordInput.addEventListener('input', updateSetupValidation);
  }

  /**
   * Auth Handlers
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

  async function handleLogout() {
    try {
      await window.API.logout();
    } catch (e) {
      console.warn('Logout error', e);
    } finally {
      isAuthenticated = false;
      stopWhatsAppPolling();
      stopQrCountdown();
      showToast('Signed out of admin console.', 'success');
      window.Router.navigate('login');
    }
  }

  /**
   * Dashboard View Controller
   */
  async function loadDashboardStats() {
    try {
      const [waStatus, groups, syncSets, cfg] = await Promise.allSettled([
        window.API.getWhatsAppStatus(),
        window.API.getGroups(),
        window.API.getSyncSets(),
        window.API.getConfig()
      ]);

      if (dashWaStatus) {
        if (waStatus.status === 'fulfilled' && waStatus.value) {
          const s = waStatus.value;
          dashWaStatus.textContent = s.isConnected ? 'Connected' : s.isLoggedIn ? 'Logged In (Idle)' : s.status === 'pairing' ? 'Pairing' : 'Unlinked';
          dashWaStatus.className = s.isConnected ? 'stat-value text-success' : s.status === 'pairing' ? 'stat-value text-warning' : 'stat-value';
        } else {
          dashWaStatus.textContent = 'Standby';
        }
      }

      if (dashGroupsCount && groups.status === 'fulfilled' && Array.isArray(groups.value)) {
        cachedGroups = groups.value;
        dashGroupsCount.textContent = `${cachedGroups.length} Group${cachedGroups.length === 1 ? '' : 's'}`;
      }

      if (dashSyncSetsCount && syncSets.status === 'fulfilled' && Array.isArray(syncSets.value)) {
        cachedSyncSets = syncSets.value;
        dashSyncSetsCount.textContent = `${cachedSyncSets.length} Set${cachedSyncSets.length === 1 ? '' : 's'}`;
      }

      if (dashConfigMode && cfg.status === 'fulfilled' && cfg.value) {
        cachedConfig = cfg.value;
        dashConfigMode.textContent = cfg.value.usernameMode === 'hash' ? 'Hash (Zero-PII)' : 'Push Name';
      }
    } catch (e) {
      console.warn('Dashboard stats error', e);
    }
  }

  /**
   * WhatsApp View Controller
   */
  function stopWhatsAppPolling() {
    if (waPollTimer) {
      clearInterval(waPollTimer);
      waPollTimer = null;
    }
  }

  function startWhatsAppPolling(intervalMs = 3000) {
    stopWhatsAppPolling();
    waPollTimer = setInterval(() => {
      if (window.Router.getRoute() === 'whatsapp' || window.Router.getRoute() === 'dashboard') {
        loadWhatsAppStatus(false);
      } else {
        stopWhatsAppPolling();
      }
    }, intervalMs);
  }

  function stopQrCountdown() {
    if (waCountdownTimer) {
      clearInterval(waCountdownTimer);
      waCountdownTimer = null;
    }
  }

  function startQrCountdown(seconds) {
    stopQrCountdown();
    let remaining = seconds;
    qrExpiresAt = Date.now() + seconds * 1000;

    function tick() {
      const now = Date.now();
      const left = Math.max(0, Math.ceil((qrExpiresAt - now) / 1000));
      if (waCountdownText) {
        waCountdownText.textContent = `Expires in ${left}s`;
      }
      if (left <= 0) {
        stopQrCountdown();
        // Refresh status when expired
        loadWhatsAppStatus(false);
      }
    }

    tick();
    waCountdownTimer = setInterval(tick, 1000);
  }

  async function loadWhatsAppStatus(showSpinner = true) {
    if (showSpinner && btnWaRefresh) setButtonLoading(btnWaRefresh, true);

    try {
      const data = await window.API.getWhatsAppStatus();
      renderWhatsAppStatus(data);
    } catch (err) {
      console.error('Failed to load WhatsApp status', err);
      renderWhatsAppError(err.message || 'Unable to query WhatsApp connection state.');
    } finally {
      if (showSpinner && btnWaRefresh) setButtonLoading(btnWaRefresh, false);
    }
  }

  function renderWhatsAppStatus(data) {
    if (!data) return;

    if (data.isConnected) {
      // Fully connected
      waStatusIndicator.className = 'status-indicator indicator-success';
      waStatusText.className = 'status-badge badge-success';
      waStatusText.textContent = 'Connected & Active';
      waStatusDesc.textContent = 'WhatsApp protocol connection established. Router is ready to synchronize messages.';
      
      btnWaPair.classList.add('hidden');
      btnWaCancel.classList.add('hidden');
      waQrSection.classList.add('hidden');
      stopQrCountdown();
      stopWhatsAppPolling();
    } else if (data.isLoggedIn) {
      // Logged in but not actively connected
      waStatusIndicator.className = 'status-indicator indicator-success';
      waStatusText.className = 'status-badge badge-success';
      waStatusText.textContent = 'Authenticated (Standby)';
      waStatusDesc.textContent = 'Device is authenticated with WhatsApp servers. Connecting transport stream...';

      btnWaPair.classList.add('hidden');
      btnWaCancel.classList.add('hidden');
      waQrSection.classList.add('hidden');
      stopQrCountdown();
    } else if (data.status === 'pairing' || (data.qrCode && data.qrCode.length > 0)) {
      // Active pairing
      waStatusIndicator.className = 'status-indicator indicator-warning';
      waStatusText.className = 'status-badge badge-warning';
      waStatusText.textContent = 'Pairing In Progress';
      waStatusDesc.textContent = 'Scan the QR code below using WhatsApp on your primary phone to link this instance.';

      btnWaPair.classList.add('hidden');
      btnWaCancel.classList.remove('hidden');
      waQrSection.classList.remove('hidden');

      if (data.qrCode && window.QRCode && waQrCanvas) {
        window.QRCode.renderCanvas(waQrCanvas, data.qrCode, { size: 260 });
        if (waQrLoader) waQrLoader.classList.add('hidden');
      }

      startWhatsAppPolling(3000);
    } else {
      // Unpaired / Disconnected
      waStatusIndicator.className = 'status-indicator indicator-neutral';
      waStatusText.className = 'status-badge badge-neutral';
      waStatusText.textContent = 'Unlinked (Not Paired)';
      waStatusDesc.textContent = 'No active WhatsApp session found. Click Pair WhatsApp Device to link your account via QR code.';

      btnWaPair.classList.remove('hidden');
      btnWaCancel.classList.add('hidden');
      waQrSection.classList.add('hidden');
      stopQrCountdown();
      stopWhatsAppPolling();
    }
  }

  function renderWhatsAppError(msg) {
    waStatusIndicator.className = 'status-indicator indicator-danger';
    waStatusText.className = 'status-badge badge-danger';
    waStatusText.textContent = 'Service Unavailable';
    waStatusDesc.textContent = msg;
  }

  async function handlePairWhatsApp() {
    setButtonLoading(btnWaPair, true);
    if (waQrLoader) waQrLoader.classList.remove('hidden');
    waQrSection.classList.remove('hidden');

    try {
      const res = await window.API.pairWhatsApp();
      if (res.isLoggedIn || res.status === 'connected') {
        showToast('WhatsApp client is already logged in!', 'success');
        await loadWhatsAppStatus(false);
        return;
      }

      if (res.qrCode && window.QRCode && waQrCanvas) {
        window.QRCode.renderCanvas(waQrCanvas, res.qrCode, { size: 260 });
        if (waQrLoader) waQrLoader.classList.add('hidden');
      }

      const timeout = res.timeoutSeconds || 30;
      startQrCountdown(timeout);
      renderWhatsAppStatus({ status: 'pairing', qrCode: res.qrCode });
      startWhatsAppPolling(2500);
      showToast('QR code generated. Point your phone to scan!', 'success');
    } catch (err) {
      showToast(err.message || 'Failed to initiate pairing', 'danger');
      waQrSection.classList.add('hidden');
    } finally {
      setButtonLoading(btnWaPair, false);
      if (waQrLoader) waQrLoader.classList.add('hidden');
    }
  }

  async function handleCancelPairWhatsApp() {
    setButtonLoading(btnWaCancel, true);
    try {
      await window.API.cancelPairWhatsApp();
      stopQrCountdown();
      stopWhatsAppPolling();
      showToast('Pairing session cancelled.', 'success');
      await loadWhatsAppStatus(false);
    } catch (err) {
      showToast(err.message || 'Failed to cancel pairing', 'danger');
    } finally {
      setButtonLoading(btnWaCancel, false);
    }
  }

  /**
   * Groups View Controller
   */
  async function loadGroups() {
    try {
      const [groupsRes, syncSetsRes] = await Promise.all([
        window.API.getGroups(),
        window.API.getSyncSets()
      ]);

      cachedGroups = Array.isArray(groupsRes) ? groupsRes : [];
      cachedSyncSets = Array.isArray(syncSetsRes) ? syncSetsRes : [];
      renderGroupsTable(searchGroupsInput ? searchGroupsInput.value : '');
    } catch (err) {
      console.error('Failed to load groups', err);
      showToast(err.message || 'Failed to load groups list', 'danger');
    }
  }

  function renderGroupsTable(query = '') {
    if (!groupsTableBody) return;
    const q = query.trim().toLowerCase();

    const filtered = cachedGroups.filter((g) => {
      if (!q) return true;
      return (g.alias && g.alias.toLowerCase().includes(q)) || (g.jid && g.jid.toLowerCase().includes(q));
    });

    if (groupsCountBadge) {
      groupsCountBadge.textContent = `${cachedGroups.length} Group${cachedGroups.length === 1 ? '' : 's'}`;
    }

    if (filtered.length === 0) {
      groupsTableBody.innerHTML = '';
      if (groupsEmptyState) groupsEmptyState.classList.remove('hidden');
      return;
    }

    if (groupsEmptyState) groupsEmptyState.classList.add('hidden');

    groupsTableBody.innerHTML = filtered.map((g) => {
      const syncBadge = g.syncSetId
        ? `<span class="badge-assigned"><svg class="icon icon-xs" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="16 3 21 3 21 8"></polyline><line x1="4" y1="20" x2="21" y2="3"></line><polyline points="21 16 21 21 16 21"></polyline><line x1="15" y1="15" x2="21" y2="21"></line><line x1="4" y1="4" x2="9" y2="9"></line></svg>${escapeHtml(g.syncSetId)}</span>`
        : `<span class="badge-unassigned">Unassigned</span>`;

      return `
        <tr data-alias="${escapeHtml(g.alias)}">
          <td><span class="alias-badge">${escapeHtml(g.alias)}</span></td>
          <td><span class="jid-text">${escapeHtml(g.jid)}</span></td>
          <td>${syncBadge}</td>
          <td class="text-right">
            <div class="table-actions">
              <button class="btn-action-icon btn-edit-group" data-alias="${escapeHtml(g.alias)}" title="Edit Group">
                <svg class="icon icon-sm" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
                  <path d="M11 4H4a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7"></path>
                  <path d="M18.5 2.5a2.121 2.121 0 0 1 3 3L12 15l-4 1 1-4 9.5-9.5z"></path>
                </svg>
              </button>
              <button class="btn-action-icon btn-action-delete btn-delete-group" data-alias="${escapeHtml(g.alias)}" title="Delete Group">
                <svg class="icon icon-sm" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
                  <polyline points="3 6 5 6 21 6"></polyline>
                  <path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"></path>
                </svg>
              </button>
            </div>
          </td>
        </tr>
      `;
    }).join('');

    // Attach click listeners for action buttons
    groupsTableBody.querySelectorAll('.btn-edit-group').forEach((btn) => {
      btn.addEventListener('click', () => {
        const alias = btn.getAttribute('data-alias');
        openEditGroupModal(alias);
      });
    });

    groupsTableBody.querySelectorAll('.btn-delete-group').forEach((btn) => {
      btn.addEventListener('click', () => {
        const alias = btn.getAttribute('data-alias');
        handleDeleteGroup(alias);
      });
    });
  }

  function populateSyncSetSelect(selectedId = '') {
    if (!selectGroupSyncSet) return;
    selectGroupSyncSet.innerHTML = '<option value="">-- None (Unassigned) --</option>';
    cachedSyncSets.forEach((set) => {
      const opt = document.createElement('option');
      opt.value = set.id;
      opt.textContent = `${set.id} (${set.groups ? set.groups.length : 0} members)`;
      if (set.id === selectedId) opt.selected = true;
      selectGroupSyncSet.appendChild(opt);
    });
  }

  function openAddGroupModal() {
    editingGroupAlias = null;
    modalGroupTitle.textContent = 'Add Configured Group';
    modalGroupAlert.classList.add('hidden');
    inputGroupAlias.value = '';
    inputGroupAlias.disabled = false;
    inputGroupJid.value = '';
    populateSyncSetSelect('');
    openModal(modalGroup);
    inputGroupAlias.focus();
  }

  function openEditGroupModal(alias) {
    const group = cachedGroups.find((g) => g.alias === alias);
    if (!group) return;

    editingGroupAlias = alias;
    modalGroupTitle.textContent = `Edit Group: ${alias}`;
    modalGroupAlert.classList.add('hidden');
    inputGroupAlias.value = group.alias;
    inputGroupAlias.disabled = true; // Alias is primary key in path
    inputGroupJid.value = group.jid;
    populateSyncSetSelect(group.syncSetId || '');
    openModal(modalGroup);
    inputGroupJid.focus();
  }

  async function handleGroupFormSubmit(e) {
    e.preventDefault();
    modalGroupAlert.classList.add('hidden');

    const alias = inputGroupAlias.value.trim();
    const jid = inputGroupJid.value.trim();
    const syncSetId = selectGroupSyncSet.value.trim() || null;

    if (!ALIAS_REGEX.test(alias)) {
      modalGroupAlert.textContent = 'Alias must start with alphanumeric character and contain up to 64 letters, numbers, hyphens, or underscores.';
      modalGroupAlert.classList.remove('hidden');
      inputGroupAlias.focus();
      return;
    }

    if (!GROUP_JID_REGEX.test(jid)) {
      modalGroupAlert.textContent = 'Group JID must match standard WhatsApp format (e.g. 120363045678901234@g.us).';
      modalGroupAlert.classList.remove('hidden');
      inputGroupJid.focus();
      return;
    }

    setButtonLoading(btnSubmitGroup, true);
    try {
      if (editingGroupAlias) {
        // Update existing group
        await window.API.updateGroup(editingGroupAlias, { jid, syncSetId });
        showToast(`Group '${editingGroupAlias}' updated successfully.`, 'success');
      } else {
        // Create new group
        await window.API.createGroup({ alias, jid, syncSetId });
        showToast(`Group '${alias}' created successfully.`, 'success');
      }
      closeModal(modalGroup);
      await loadGroups();
    } catch (err) {
      modalGroupAlert.textContent = err.message || 'Failed to save group.';
      modalGroupAlert.classList.remove('hidden');
    } finally {
      setButtonLoading(btnSubmitGroup, false);
    }
  }

  function handleDeleteGroup(alias) {
    showConfirmDialog({
      title: `Delete Group: ${alias}`,
      message: `Are you sure you want to delete group '${alias}'? This will remove its JID binding and detach it from any assigned sync set.`,
      btnText: 'Delete Group',
      isDanger: true,
      onConfirm: async () => {
        await window.API.deleteGroup(alias);
        showToast(`Group '${alias}' deleted successfully.`, 'success');
        await loadGroups();
      }
    });
  }

  /**
   * Sync Sets View Controller
   */
  async function loadSyncSets() {
    try {
      const [syncSetsRes, groupsRes] = await Promise.all([
        window.API.getSyncSets(),
        window.API.getGroups()
      ]);

      cachedSyncSets = Array.isArray(syncSetsRes) ? syncSetsRes : [];
      cachedGroups = Array.isArray(groupsRes) ? groupsRes : [];
      renderSyncSetsGrid(searchSyncSetsInput ? searchSyncSetsInput.value : '');
    } catch (err) {
      console.error('Failed to load sync sets', err);
      showToast(err.message || 'Failed to load sync sets', 'danger');
    }
  }

  function renderSyncSetsGrid(query = '') {
    if (!syncSetsContainer) return;
    const q = query.trim().toLowerCase();

    const filtered = cachedSyncSets.filter((set) => {
      if (!q) return true;
      const matchId = set.id && set.id.toLowerCase().includes(q);
      const matchMember = Array.isArray(set.groups) && set.groups.some((g) => g.toLowerCase().includes(q));
      return matchId || matchMember;
    });

    if (syncSetsCountBadge) {
      syncSetsCountBadge.textContent = `${cachedSyncSets.length} Set${cachedSyncSets.length === 1 ? '' : 's'}`;
    }

    if (filtered.length === 0) {
      syncSetsContainer.innerHTML = '';
      if (syncSetsEmptyState) syncSetsEmptyState.classList.remove('hidden');
      return;
    }

    if (syncSetsEmptyState) syncSetsEmptyState.classList.add('hidden');

    syncSetsContainer.innerHTML = filtered.map((set) => {
      const groups = Array.isArray(set.groups) ? set.groups : [];
      const memberChips = groups.length > 0
        ? groups.map((g) => `<span class="group-chip-tag"><svg class="icon icon-xs" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M17 21v-2a4 4 0 0 0-4-4H5a4 4 0 0 0-4 4v2"></path><circle cx="9" cy="7" r="4"></circle></svg>${escapeHtml(g)}</span>`).join('')
        : `<span class="no-members-text">No member groups assigned yet</span>`;

      return `
        <div class="card sync-set-card" data-id="${escapeHtml(set.id)}">
          <div class="sync-set-header">
            <div class="sync-set-id-badge">
              <div class="stat-icon-wrapper icon-sync" style="width: 32px; height: 32px; border-radius: 8px;">
                <svg class="icon icon-xs" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
                  <polyline points="16 3 21 3 21 8"></polyline>
                  <line x1="4" y1="20" x2="21" y2="3"></line>
                  <polyline points="21 16 21 21 16 21"></polyline>
                  <line x1="15" y1="15" x2="21" y2="21"></line>
                </svg>
              </div>
              <span class="sync-set-id-text">${escapeHtml(set.id)}</span>
            </div>
            <div class="table-actions">
              <button class="btn-action-icon btn-edit-sync-set" data-id="${escapeHtml(set.id)}" title="Edit Members">
                <svg class="icon icon-sm" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
                  <path d="M11 4H4a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7"></path>
                  <path d="M18.5 2.5a2.121 2.121 0 0 1 3 3L12 15l-4 1 1-4 9.5-9.5z"></path>
                </svg>
              </button>
              <button class="btn-action-icon btn-action-delete btn-delete-sync-set" data-id="${escapeHtml(set.id)}" title="Delete Sync Set">
                <svg class="icon icon-sm" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
                  <polyline points="3 6 5 6 21 6"></polyline>
                  <path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"></path>
                </svg>
              </button>
            </div>
          </div>

          <div class="sync-set-members">
            <span class="sync-set-members-label">Connected Endpoints (${groups.length})</span>
            <div class="group-chips-container">
              ${memberChips}
            </div>
          </div>

          <div class="sync-set-footer">
            <span class="sync-set-count-pill">${groups.length >= 2 ? '<span class="text-success">● Active All-to-All Sync</span>' : '<span class="text-warning">● Need 2+ groups to sync</span>'}</span>
            <button class="btn btn-ghost btn-sm btn-edit-sync-set" data-id="${escapeHtml(set.id)}">Manage</button>
          </div>
        </div>
      `;
    }).join('');

    // Attach listeners
    syncSetsContainer.querySelectorAll('.btn-edit-sync-set').forEach((btn) => {
      btn.addEventListener('click', () => {
        const id = btn.getAttribute('data-id');
        openEditSyncSetModal(id);
      });
    });

    syncSetsContainer.querySelectorAll('.btn-delete-sync-set').forEach((btn) => {
      btn.addEventListener('click', () => {
        const id = btn.getAttribute('data-id');
        handleDeleteSyncSet(id);
      });
    });
  }

  function renderGroupChecklist(selectedGroupAliases = [], currentSetId = '') {
    if (!syncSetGroupsChecklist) return;

    if (cachedGroups.length === 0) {
      syncSetGroupsChecklist.innerHTML = '<div class="checklist-empty">No groups available. Please add groups first.</div>';
      return;
    }

    syncSetGroupsChecklist.innerHTML = cachedGroups.map((g) => {
      const isSelected = selectedGroupAliases.includes(g.alias);
      const isAssignedOther = g.syncSetId && g.syncSetId !== currentSetId;
      const assignedBadge = isAssignedOther ? `<span class="checklist-assigned-tag">In '${escapeHtml(g.syncSetId)}'</span>` : '';

      return `
        <label class="checklist-item ${isSelected ? 'selected' : ''}">
          <div class="checklist-item-main">
            <input 
              type="checkbox" 
              class="checklist-checkbox" 
              value="${escapeHtml(g.alias)}" 
              ${isSelected ? 'checked' : ''}
            >
            <span class="checklist-alias">${escapeHtml(g.alias)}</span>
          </div>
          ${assignedBadge}
        </label>
      `;
    }).join('');

    syncSetGroupsChecklist.querySelectorAll('.checklist-item').forEach((item) => {
      const cb = item.querySelector('.checklist-checkbox');
      cb.addEventListener('change', () => {
        if (cb.checked) {
          item.classList.add('selected');
        } else {
          item.classList.remove('selected');
        }
      });
    });
  }

  function openAddSyncSetModal() {
    editingSyncSetId = null;
    modalSyncSetTitle.textContent = 'Add Sync Set';
    modalSyncSetAlert.classList.add('hidden');
    inputSyncSetId.value = '';
    inputSyncSetId.disabled = false;
    renderGroupChecklist([], '');
    openModal(modalSyncSet);
    inputSyncSetId.focus();
  }

  function openEditSyncSetModal(id) {
    const set = cachedSyncSets.find((s) => s.id === id);
    if (!set) return;

    editingSyncSetId = id;
    modalSyncSetTitle.textContent = `Edit Sync Set: ${id}`;
    modalSyncSetAlert.classList.add('hidden');
    inputSyncSetId.value = set.id;
    inputSyncSetId.disabled = true;
    renderGroupChecklist(set.groups || [], id);
    openModal(modalSyncSet);
  }

  async function handleSyncSetFormSubmit(e) {
    e.preventDefault();
    modalSyncSetAlert.classList.add('hidden');

    const id = inputSyncSetId.value.trim();
    if (!SYNC_SET_ID_REGEX.test(id)) {
      modalSyncSetAlert.textContent = 'Sync Set ID must start with alphanumeric character and contain up to 64 letters, numbers, hyphens, or underscores.';
      modalSyncSetAlert.classList.remove('hidden');
      inputSyncSetId.focus();
      return;
    }

    const selectedGroups = [];
    if (syncSetGroupsChecklist) {
      syncSetGroupsChecklist.querySelectorAll('.checklist-checkbox:checked').forEach((cb) => {
        selectedGroups.push(cb.value);
      });
    }

    setButtonLoading(btnSubmitSyncSet, true);
    try {
      if (editingSyncSetId) {
        await window.API.updateSyncSet(editingSyncSetId, { groups: selectedGroups });
        showToast(`Sync set '${editingSyncSetId}' updated successfully.`, 'success');
      } else {
        await window.API.createSyncSet({ id, groups: selectedGroups });
        showToast(`Sync set '${id}' created successfully.`, 'success');
      }
      closeModal(modalSyncSet);
      await loadSyncSets();
    } catch (err) {
      modalSyncSetAlert.textContent = err.message || 'Failed to save sync set.';
      modalSyncSetAlert.classList.remove('hidden');
    } finally {
      setButtonLoading(btnSubmitSyncSet, false);
    }
  }

  function handleDeleteSyncSet(id) {
    showConfirmDialog({
      title: `Delete Sync Set: ${id}`,
      message: `Are you sure you want to delete sync set '${id}'? Member groups will be unlinked from this synchronization set.`,
      btnText: 'Delete Sync Set',
      isDanger: true,
      onConfirm: async () => {
        await window.API.deleteSyncSet(id);
        showToast(`Sync set '${id}' deleted successfully.`, 'success');
        await loadSyncSets();
      }
    });
  }

  /**
   * Settings View Controller
   */
  async function loadSettings() {
    if (settingsAlert) settingsAlert.classList.add('hidden');
    try {
      const cfg = await window.API.getConfig();
      cachedConfig = cfg;
      populateSettingsForm(cfg);
    } catch (err) {
      console.error('Failed to load config', err);
      showToast(err.message || 'Failed to load configuration settings', 'danger');
    }
  }

  function populateSettingsForm(cfg) {
    if (!cfg) return;

    if (settingsUsernameMode) settingsUsernameMode.value = cfg.usernameMode || 'push_name';
    if (settingsMediaEnabled) settingsMediaEnabled.checked = cfg.media ? cfg.media.enabled : false;
    if (settingsMediaMaxSize) settingsMediaMaxSize.value = cfg.media ? cfg.media.maxSizeMB : 50;
    if (settingsRecoveryEnabled) settingsRecoveryEnabled.checked = cfg.recovery ? cfg.recovery.enabled : true;
    if (settingsRecoveryMaxAge) settingsRecoveryMaxAge.value = cfg.recovery ? cfg.recovery.maxAgeHours : 72;
    if (settingsRecoveryMaxMsgs) settingsRecoveryMaxMsgs.value = cfg.recovery ? cfg.recovery.maxMessagesPerGroup : 1000;
    if (settingsRetentionDays) settingsRetentionDays.value = cfg.storage ? cfg.storage.messageRetentionDays : 14;
  }

  async function handleSettingsSubmit(e) {
    e.preventDefault();
    if (settingsAlert) settingsAlert.classList.add('hidden');

    const usernameMode = settingsUsernameMode.value;
    const mediaEnabled = settingsMediaEnabled.checked;
    const mediaMaxSize = parseInt(settingsMediaMaxSize.value, 10);
    const recoveryEnabled = settingsRecoveryEnabled.checked;
    const recoveryMaxAge = parseInt(settingsRecoveryMaxAge.value, 10);
    const recoveryMaxMsgs = parseInt(settingsRecoveryMaxMsgs.value, 10);
    const retentionDays = parseInt(settingsRetentionDays.value, 10);

    if (isNaN(mediaMaxSize) || mediaMaxSize < 1) {
      settingsAlert.textContent = 'Media max size must be a positive number (1 MB minimum).';
      settingsAlert.classList.remove('hidden');
      settingsMediaMaxSize.focus();
      return;
    }

    if (isNaN(recoveryMaxAge) || recoveryMaxAge < 1) {
      settingsAlert.textContent = 'Recovery max age must be a positive integer (1 hour minimum).';
      settingsAlert.classList.remove('hidden');
      settingsRecoveryMaxAge.focus();
      return;
    }

    if (isNaN(recoveryMaxMsgs) || recoveryMaxMsgs < 1) {
      settingsAlert.textContent = 'Recovery max messages must be a positive integer (1 message minimum).';
      settingsAlert.classList.remove('hidden');
      settingsRecoveryMaxMsgs.focus();
      return;
    }

    if (isNaN(retentionDays) || retentionDays < 1) {
      settingsAlert.textContent = 'Storage message retention must be a positive integer (1 day minimum).';
      settingsAlert.classList.remove('hidden');
      settingsRetentionDays.focus();
      return;
    }

    const payload = {
      usernameMode,
      media: {
        enabled: mediaEnabled,
        maxSizeMB: mediaMaxSize
      },
      recovery: {
        enabled: recoveryEnabled,
        maxAgeHours: recoveryMaxAge,
        maxMessagesPerGroup: recoveryMaxMsgs
      },
      storage: {
        messageRetentionDays: retentionDays
      }
    };

    setButtonLoading(btnSaveSettings, true);
    try {
      const updated = await window.API.updateConfig(payload);
      cachedConfig = updated;
      populateSettingsForm(updated);
      showToast('Global configuration updated successfully!', 'success');
    } catch (err) {
      if (settingsAlert) {
        settingsAlert.textContent = err.message || 'Failed to update configuration.';
        settingsAlert.classList.remove('hidden');
      }
    } finally {
      setButtonLoading(btnSaveSettings, false);
    }
  }

  /**
   * Setup Event Listeners & Router
   */
  function setupEventListeners() {
    setupPasswordToggles();
    setupFormValidation();
    setupModalListeners();

    // Auth forms
    if (formSetup) formSetup.addEventListener('submit', handleSetupSubmit);
    if (formLogin) formLogin.addEventListener('submit', handleLoginSubmit);
    if (btnLogout) btnLogout.addEventListener('click', handleLogout);

    // Nav tabs
    document.querySelectorAll('.nav-tab').forEach((tab) => {
      tab.addEventListener('click', () => {
        const targetTab = tab.getAttribute('data-tab');
        window.Router.navigate(targetTab);
      });
    });

    // Dashboard feature cards
    document.querySelectorAll('.stat-card').forEach((card) => {
      card.addEventListener('click', () => {
        const action = card.getAttribute('data-action');
        if (action) window.Router.navigate(action);
      });
    });

    // WhatsApp view
    if (btnWaRefresh) btnWaRefresh.addEventListener('click', () => loadWhatsAppStatus(true));
    if (btnWaPair) btnWaPair.addEventListener('click', handlePairWhatsApp);
    if (btnWaCancel) btnWaCancel.addEventListener('click', handleCancelPairWhatsApp);

    // Groups view
    if (btnOpenAddGroup) btnOpenAddGroup.addEventListener('click', openAddGroupModal);
    if (formGroup) formGroup.addEventListener('submit', handleGroupFormSubmit);
    if (searchGroupsInput) {
      searchGroupsInput.addEventListener('input', (e) => renderGroupsTable(e.target.value));
    }

    // Sync Sets view
    if (btnOpenAddSyncSet) btnOpenAddSyncSet.addEventListener('click', openAddSyncSetModal);
    if (formSyncSet) formSyncSet.addEventListener('submit', handleSyncSetFormSubmit);
    if (searchSyncSetsInput) {
      searchSyncSetsInput.addEventListener('input', (e) => renderSyncSetsGrid(e.target.value));
    }

    // Settings view
    if (formSettings) formSettings.addEventListener('submit', handleSettingsSubmit);
    if (btnResetSettings) {
      btnResetSettings.addEventListener('click', () => {
        if (cachedConfig) populateSettingsForm(cachedConfig);
      });
    }

    // API 401 unauthorized
    window.addEventListener('auth:unauthorized', () => {
      if (isAuthenticated) {
        isAuthenticated = false;
        stopWhatsAppPolling();
        stopQrCountdown();
        showToast('Your session has expired. Please sign in again.', 'danger');
        window.Router.navigate('login');
      }
    });
  }

  /**
   * App Initialization & Route Registration
   */
  async function initApp() {
    setupEventListeners();

    // Global router guards
    window.Router.beforeEach(async (route) => {
      if (isSetup === null) {
        try {
          const res = await window.API.getAuthStatus();
          isSetup = res && res.isSetup === true;
        } catch (err) {
          console.error('Failed to get auth status', err);
          isSetup = false;
        }
      }

      if (!isSetup) return 'setup';
      if (route === 'setup') return 'login';

      if (!isAuthenticated) {
        try {
          await window.API.getConfig();
          isAuthenticated = true;
        } catch (err) {
          isAuthenticated = false;
          if (route !== 'login') return 'login';
        }
      }

      if (isAuthenticated && route === 'login') {
        return 'dashboard';
      }

      return route;
    });

    // Register route handlers
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

    window.Router.addRoute('whatsapp', () => {
      switchView('whatsapp');
      loadWhatsAppStatus(true);
    });

    window.Router.addRoute('groups', () => {
      switchView('groups');
      loadGroups();
    });

    window.Router.addRoute('sync-sets', () => {
      switchView('sync-sets');
      loadSyncSets();
    });

    window.Router.addRoute('settings', () => {
      switchView('settings');
      loadSettings();
    });

    // Initialize router
    await window.Router.init();
  }

  // Launch on DOM ready
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', initApp);
  } else {
    initApp();
  }
})();
