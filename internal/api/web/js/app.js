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
  const DISCORD_CHANNEL_ID_REGEX = /^[0-9]+$/;
  const TELEGRAM_CHAT_ID_REGEX = /^-[0-9]+$/;
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
    discord: document.getElementById('view-discord'),
    telegram: document.getElementById('view-telegram'),
    groups: document.getElementById('view-groups'),
    'sync-sets': document.getElementById('view-sync-sets'),
    settings: document.getElementById('view-settings')
    ,users: document.getElementById('view-users'), membership: document.getElementById('view-membership'), invite: document.getElementById('view-invite'), reset: document.getElementById('view-reset')
  };

  // Auth Form Elements
  const formSetup = document.getElementById('form-setup');
  const setupUsernameInput = document.getElementById('setup-username');
  const setupPasswordInput = document.getElementById('setup-password');
  const setupConfirmPasswordInput = document.getElementById('setup-confirm-password');
  const setupStrengthFill = document.getElementById('setup-strength-fill');
  const setupPasswordHint = document.getElementById('setup-password-hint');
  const setupConfirmHint = document.getElementById('setup-confirm-hint');
  const setupAlert = document.getElementById('setup-alert');
  const btnSubmitSetup = document.getElementById('btn-submit-setup');

  const formLogin = document.getElementById('form-login');
  const loginUsernameInput = document.getElementById('login-username');
  const loginPasswordInput = document.getElementById('login-password');
  const loginAlert = document.getElementById('login-alert');
  const btnSubmitLogin = document.getElementById('btn-submit-login');
  const navUsers = document.getElementById('nav-users');
  const formInvite = document.getElementById('form-invite');
  const formReset = document.getElementById('form-reset');

  // Dashboard Stats Elements
  const dashWaStatus = document.getElementById('dash-wa-status');
  const dashDiscordStatus = document.getElementById('dash-discord-status');
  const dashTelegramStatus = document.getElementById('dash-telegram-status');
  const dashGroupsCount = document.getElementById('dash-groups-count');
  const dashSyncSetsCount = document.getElementById('dash-sync-sets-count');
  const dashConfigMode = document.getElementById('dash-config-mode');
  const deliveryHealthBody = document.getElementById('delivery-health-body');
  const deliveryHealthEmpty = document.getElementById('delivery-health-empty');
  const deliveryHealthError = document.getElementById('delivery-health-error');

  // WhatsApp View Elements
  const btnWaRefresh = document.getElementById('btn-wa-refresh');
  const waStatusIndicator = document.getElementById('wa-status-indicator');
  const waStatusText = document.getElementById('wa-status-text');
  const waStatusDesc = document.getElementById('wa-status-desc');
  const btnWaPair = document.getElementById('btn-wa-pair');
  const btnWaCancel = document.getElementById('btn-wa-cancel');
  const btnWaLogout = document.getElementById('btn-wa-logout');
  const waQrSection = document.getElementById('wa-qr-section');
  const waQrCanvas = document.getElementById('wa-qr-canvas');
  const waQrLoader = document.getElementById('wa-qr-loader');
  const waCountdownBadge = document.getElementById('wa-countdown-badge');
  const waCountdownText = document.getElementById('wa-countdown-text');

  // Discord View Elements
  const btnDiscordRefresh = document.getElementById('btn-discord-refresh');
  const btnDiscordDiscover = document.getElementById('btn-discord-discover');
  const discordStatusIndicator = document.getElementById('discord-status-indicator');
  const discordStatusText = document.getElementById('discord-status-text');
  const discordStatusDesc = document.getElementById('discord-status-desc');
  const discordConfiguredBody = document.getElementById('discord-configured-body');
  const discordConfiguredEmpty = document.getElementById('discord-configured-empty');
  const discordDiscoveryAlert = document.getElementById('discord-discovery-alert');
  const discordDiscoveryBody = document.getElementById('discord-discovery-body');
  const discordDiscoveryEmpty = document.getElementById('discord-discovery-empty');

  // Telegram View Elements
  const btnTelegramRefresh = document.getElementById('btn-telegram-refresh');
  const btnTelegramDiscover = document.getElementById('btn-telegram-discover');
  const telegramStatusIndicator = document.getElementById('telegram-status-indicator');
  const telegramStatusText = document.getElementById('telegram-status-text');
  const telegramStatusDesc = document.getElementById('telegram-status-desc');
  const telegramConfiguredBody = document.getElementById('telegram-configured-body');
  const telegramConfiguredEmpty = document.getElementById('telegram-configured-empty');
  const telegramDiscoveryAlert = document.getElementById('telegram-discovery-alert');
  const telegramDiscoveryBody = document.getElementById('telegram-discovery-body');
  const telegramDiscoveryEmpty = document.getElementById('telegram-discovery-empty');

  // Endpoint View Elements
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
  const selectGroupWA = document.getElementById('select-group-wa');
  const selectGroupWAHint = document.getElementById('select-group-wa-hint');
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
  const syncSetEndpointsChecklist = document.getElementById('sync-set-groups-checklist');
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
  const settingsPollsAggregationTrigger = document.getElementById('settings-polls-aggregation-trigger');
  const settingsWhatsAppCleanupEnabled = document.getElementById('settings-whatsapp-cleanup-enabled');
  const settingsWhatsAppCleanupRetentionDays = document.getElementById('settings-whatsapp-cleanup-retention-days');
  const btnResetSettings = document.getElementById('btn-reset-settings');
  const btnSaveSettings = document.getElementById('btn-save-settings');

  // Change Password Form Elements
  const formChangePassword = document.getElementById('form-change-password');
  const changePwdAlert = document.getElementById('change-pwd-alert');
  const changePwdCurrent = document.getElementById('change-pwd-current');
  const changePwdNew = document.getElementById('change-pwd-new');
  const changePwdConfirm = document.getElementById('change-pwd-confirm');
  const changePwdStrengthFill = document.getElementById('change-pwd-strength-fill');
  const changePwdHint = document.getElementById('change-pwd-hint');
  const changePwdConfirmHint = document.getElementById('change-pwd-confirm-hint');
  const btnSubmitChangePwd = document.getElementById('btn-submit-change-pwd');

  // Universal Confirmation Modal
  const modalConfirm = document.getElementById('modal-confirm');
  const modalConfirmTitle = document.getElementById('modal-confirm-title');
  const modalConfirmMsg = document.getElementById('modal-confirm-msg');
  const btnConfirmOk = document.getElementById('btn-confirm-ok');

  // Application State
  let isSetup = null;
  let isAuthenticated = false;
  let currentUser = null;
  let cachedEndpoints = [];
  let cachedSyncSets = [];
  let cachedConfig = null;
  let cachedDiscordChannels = [];
  let cachedDiscordStatus = null;
  let cachedTelegramChats = [];
  let cachedTelegramStatus = null;
  let editingGroupAlias = null;
  let editingEndpointTransport = 'whatsapp';
  let editingSyncSetId = null;
  let confirmCallback = null;

  // WhatsApp State & Timers
  let waPollTimer = null;
  let waCountdownTimer = null;
  let qrExpiresAt = null;
  let deliveryPollTimer = null;

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
   * Password Strength Evaluator
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
   * Form Live Validation
   */
  function setupFormValidation() {
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

    function updateChangePwdValidation() {
      if (!changePwdNew) return;
      const pwd = changePwdNew.value;
      const confirmPwd = changePwdConfirm ? changePwdConfirm.value : '';

      if (pwd.length === 0) {
        if (changePwdStrengthFill) changePwdStrengthFill.className = 'strength-fill';
        if (changePwdHint) {
          changePwdHint.className = 'form-hint';
          changePwdHint.textContent = 'Between 8 and 72 characters';
        }
      } else if (pwd.length < 8) {
        if (changePwdStrengthFill) changePwdStrengthFill.className = 'strength-fill strength-weak';
        if (changePwdHint) {
          changePwdHint.className = 'form-hint hint-error';
          changePwdHint.textContent = `Too short (${pwd.length}/8 characters minimum)`;
        }
      } else if (pwd.length > 72) {
        if (changePwdStrengthFill) changePwdStrengthFill.className = 'strength-fill strength-weak';
        if (changePwdHint) {
          changePwdHint.className = 'form-hint hint-error';
          changePwdHint.textContent = `Too long (${pwd.length}/72 characters maximum)`;
        }
      } else {
        const strength = evaluatePasswordStrength(pwd);
        if (strength <= 1) {
          if (changePwdStrengthFill) changePwdStrengthFill.className = 'strength-fill strength-weak';
          if (changePwdHint) {
            changePwdHint.className = 'form-hint';
            changePwdHint.textContent = 'Weak password';
          }
        } else if (strength <= 2) {
          if (changePwdStrengthFill) changePwdStrengthFill.className = 'strength-fill strength-fair';
          if (changePwdHint) {
            changePwdHint.className = 'form-hint';
            changePwdHint.textContent = 'Fair password';
          }
        } else {
          if (changePwdStrengthFill) changePwdStrengthFill.className = 'strength-fill strength-strong';
          if (changePwdHint) {
            changePwdHint.className = 'form-hint hint-success';
            changePwdHint.textContent = 'Strong password';
          }
        }
      }

      if (confirmPwd.length > 0) {
        if (confirmPwd !== pwd) {
          if (changePwdConfirmHint) {
            changePwdConfirmHint.className = 'form-hint hint-error';
            changePwdConfirmHint.textContent = 'Passwords do not match';
          }
        } else {
          if (changePwdConfirmHint) {
            changePwdConfirmHint.className = 'form-hint hint-success';
            changePwdConfirmHint.textContent = 'Passwords match';
          }
        }
      } else if (changePwdConfirmHint) {
        changePwdConfirmHint.textContent = '';
      }
    }

    if (setupPasswordInput) setupPasswordInput.addEventListener('input', updateSetupValidation);
    if (setupConfirmPasswordInput) setupConfirmPasswordInput.addEventListener('input', updateSetupValidation);
    if (changePwdNew) changePwdNew.addEventListener('input', updateChangePwdValidation);
    if (changePwdConfirm) changePwdConfirm.addEventListener('input', updateChangePwdValidation);
  }

  /**
   * Auth Handlers
   */
  async function handleSetupSubmit(e) {
    e.preventDefault();
    setupAlert.classList.add('hidden');

    const password = setupPasswordInput.value;
    const username = setupUsernameInput ? setupUsernameInput.value.trim() : '';
    const confirmPassword = setupConfirmPasswordInput.value;

    if (!username) {
      setupAlert.textContent = 'Username is required.';
      setupAlert.classList.remove('hidden');
      setupUsernameInput.focus();
      return;
    }
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
      await window.API.setupPassword(username, password);
      isSetup = true;
      isAuthenticated = true;
      showToast('Administrator account created. Welcome to Message Sync.', 'success');
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
    const username = loginUsernameInput ? loginUsernameInput.value.trim() : '';
    if (!username || !password) {
      loginAlert.textContent = 'Please enter your username and password.';
      loginAlert.classList.remove('hidden');
      loginPasswordInput.focus();
      return;
    }

    setButtonLoading(btnSubmitLogin, true);
    try {
      const result = await window.API.login(username, password);
      isAuthenticated = true;
      currentUser = result && result.user ? result.user : null;
      const userEl = document.getElementById('session-username');
      const roleEl = document.getElementById('session-role');
      if (userEl && currentUser) userEl.textContent = currentUser.username;
      if (roleEl && currentUser) roleEl.textContent = currentUser.role;
      if (navUsers) navUsers.classList.toggle('hidden', !currentUser || currentUser.role !== 'admin');
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
      stopDeliveryPolling();
      stopQrCountdown();
      cachedDiscordChannels = [];
      cachedDiscordStatus = null;
      cachedTelegramChats = [];
      cachedTelegramStatus = null;
      showToast('Signed out of admin console.', 'success');
      window.Router.navigate('login');
    }
  }

  /**
   * Dashboard View Controller
   */
  async function loadDashboardStats() {
    try {
      const [waStatus, discordStatus, telegramStatus, endpoints, syncSets, cfg] = await Promise.allSettled([
        window.API.getWhatsAppStatus(),
        window.API.getDiscordStatus(),
        window.API.getTelegramStatus(),
        window.API.getEndpoints(),
        window.API.getSyncSets(),
        window.API.getConfig()
      ]);

      if (dashWaStatus) {
        if (waStatus.status === 'fulfilled' && waStatus.value) {
          const s = waStatus.value;
          const isFullyConnected = s.status === 'connected' || (s.isConnected && s.isLoggedIn);
          dashWaStatus.textContent = isFullyConnected ? 'Connected' : s.isLoggedIn ? 'Logged In (Idle)' : s.status === 'pairing' ? 'Pairing' : 'Unlinked';
          dashWaStatus.className = isFullyConnected ? 'stat-value text-success' : s.status === 'pairing' ? 'stat-value text-warning' : 'stat-value';
        } else {
          dashWaStatus.textContent = 'Standby';
        }
      }

      if (dashDiscordStatus) {
        if (discordStatus.status === 'fulfilled' && discordStatus.value) {
          const s = discordStatus.value;
          dashDiscordStatus.textContent = !s.configured ? 'Not Configured' : s.connected ? 'Connected' : 'Disconnected';
          dashDiscordStatus.className = s.connected ? 'stat-value text-success' : s.configured ? 'stat-value text-warning' : 'stat-value';
        } else {
          dashDiscordStatus.textContent = 'Standby';
        }
      }

      if (dashTelegramStatus) {
        if (telegramStatus.status === 'fulfilled' && telegramStatus.value) {
          const s = telegramStatus.value;
          dashTelegramStatus.textContent = !s.tokenConfigured ? 'Not Configured' : s.running ? 'Polling' : 'Stopped';
          dashTelegramStatus.className = s.running ? 'stat-value text-success' : s.tokenConfigured ? 'stat-value text-warning' : 'stat-value';
        } else {
          dashTelegramStatus.textContent = 'Standby';
        }
      }

      if (dashGroupsCount && endpoints.status === 'fulfilled' && Array.isArray(endpoints.value)) {
        cachedEndpoints = endpoints.value;
        dashGroupsCount.textContent = `${cachedEndpoints.length} Endpoint${cachedEndpoints.length === 1 ? '' : 's'}`;
      }

      if (dashSyncSetsCount && syncSets.status === 'fulfilled' && Array.isArray(syncSets.value)) {
        cachedSyncSets = syncSets.value;
        dashSyncSetsCount.textContent = `${cachedSyncSets.length} Set${cachedSyncSets.length === 1 ? '' : 's'}`;
      }

      if (dashConfigMode && cfg.status === 'fulfilled' && cfg.value) {
        cachedConfig = cfg.value;
        dashConfigMode.textContent = cfg.value.usernameMode === 'hash' ? 'Hash (Zero-PII)' : 'Push Name';
      }
      loadDeliveryStatus();
    } catch (e) {
      console.warn('Dashboard stats error', e);
    }
  }

  function deliveryLabel(state) {
    return ({ healthy: 'Healthy', queued: 'Queued', retrying: 'Retrying', awaiting_replay: 'Awaiting replay', failed: 'Failed', stopped: 'Stopped' })[state] || 'Unknown';
  }

  async function loadDeliveryStatus() {
    if (!deliveryHealthBody) return;
    try {
      const data = await window.API.getDeliveryStatus();
      const endpoints = data && Array.isArray(data.endpoints) ? data.endpoints : [];
      deliveryHealthBody.innerHTML = endpoints.map((item) => {
        const ledger = `Q ${item.queued} · R ${item.retrying} · A ${item.awaitingReplay} · F ${item.failed}`;
        const queue = `${item.queueDepth}/${item.queueCapacity}`;
        const age = item.oldestActiveAgeSeconds > 0 ? `${item.oldestActiveAgeSeconds}s` : '—';
        const failure = item.lastFailureClass ? ` · ${escapeHtml(item.lastFailureClass)}` : '';
        return `<tr><td><span class="alias-badge">${escapeHtml(item.alias)}</span></td><td>${escapeHtml(item.transport)}</td><td>${escapeHtml(deliveryLabel(item.laneState))}${failure}</td><td>${queue}</td><td>${ledger}</td><td>${age}</td><td>${escapeHtml(item.transportStatus || 'Unavailable')}</td></tr>`;
      }).join('');
      deliveryHealthEmpty.classList.toggle('hidden', endpoints.length !== 0);
      deliveryHealthError.classList.add('hidden');
    } catch (err) {
      deliveryHealthError.textContent = 'Unable to load delivery health.';
      deliveryHealthError.classList.remove('hidden');
    }
  }

  function startDeliveryPolling() {
    if (deliveryPollTimer) clearInterval(deliveryPollTimer);
    deliveryPollTimer = setInterval(() => {
      if (window.Router.getRoute() === 'dashboard') loadDeliveryStatus();
    }, 3000);
  }

  function stopDeliveryPolling() {
    if (deliveryPollTimer) { clearInterval(deliveryPollTimer); deliveryPollTimer = null; }
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

    if (data.status === 'connected' || (data.isConnected && data.isLoggedIn)) {
      // Fully connected & authenticated
      waStatusIndicator.className = 'status-indicator indicator-success';
      waStatusText.className = 'status-badge badge-success';
      waStatusText.textContent = 'Connected & Active';
      waStatusDesc.textContent = 'WhatsApp protocol connection established. Router is ready to synchronize messages.';
      
      btnWaPair.classList.add('hidden');
      btnWaCancel.classList.add('hidden');
      if (btnWaLogout) btnWaLogout.classList.remove('hidden');
      waQrSection.classList.add('hidden');
      stopQrCountdown();
      stopWhatsAppPolling();
    } else if (data.status === 'pairing' || (data.qrCode && data.qrCode.length > 0)) {
      // Active pairing
      waStatusIndicator.className = 'status-indicator indicator-warning';
      waStatusText.className = 'status-badge badge-warning';
      waStatusText.textContent = 'Pairing In Progress';
      waStatusDesc.textContent = 'Scan the QR code below using WhatsApp on your primary phone to link this instance.';

      btnWaPair.classList.add('hidden');
      btnWaCancel.classList.remove('hidden');
      if (btnWaLogout) btnWaLogout.classList.add('hidden');
      waQrSection.classList.remove('hidden');

      if (data.qrCode && window.QRCode && waQrCanvas) {
        window.QRCode.renderCanvas(waQrCanvas, data.qrCode, { size: 260 });
        if (waQrLoader) waQrLoader.classList.add('hidden');
      }

      startWhatsAppPolling(3000);
    } else if (data.status === 'disconnected' || data.isLoggedIn) {
      // Logged in but not actively connected
      waStatusIndicator.className = 'status-indicator indicator-warning';
      waStatusText.className = 'status-badge badge-warning';
      waStatusText.textContent = 'Authenticated (Standby)';
      waStatusDesc.textContent = 'Device is authenticated with WhatsApp servers. Connecting transport stream...';

      btnWaPair.classList.add('hidden');
      btnWaCancel.classList.add('hidden');
      if (btnWaLogout) btnWaLogout.classList.remove('hidden');
      waQrSection.classList.add('hidden');
      stopQrCountdown();
    } else {
      // Unpaired / Disconnected
      waStatusIndicator.className = 'status-indicator indicator-neutral';
      waStatusText.className = 'status-badge badge-neutral';
      waStatusText.textContent = 'Unlinked (Not Paired)';
      waStatusDesc.textContent = 'No active WhatsApp session found. Click Pair WhatsApp Device to link your account via QR code.';

      btnWaPair.classList.remove('hidden');
      btnWaCancel.classList.add('hidden');
      if (btnWaLogout) btnWaLogout.classList.add('hidden');
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
    if (btnWaLogout) btnWaLogout.classList.add('hidden');
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

  function handleLogoutWhatsApp() {
    showConfirmDialog({
      title: 'Log Out WhatsApp',
      message: 'Are you sure you want to disconnect and log out the WhatsApp session? The current session credentials will be removed and you will need to scan a QR code to reconnect.',
      btnText: 'Log Out WhatsApp',
      isDanger: true,
      onConfirm: async () => {
        setButtonLoading(btnWaLogout, true);
        try {
          await window.API.logoutWhatsApp();
          stopQrCountdown();
          stopWhatsAppPolling();
          showToast('WhatsApp session logged out successfully.', 'success');
          await loadWhatsAppStatus(false);
        } catch (err) {
          showToast(err.message || 'Failed to logout WhatsApp', 'danger');
        } finally {
          setButtonLoading(btnWaLogout, false);
        }
      }
    });
  }

  /**
   * Discord Admin & Discovery Controller
   */
  function webhookStatusForAlias(alias) {
    if (!cachedDiscordStatus || !Array.isArray(cachedDiscordStatus.webhooks)) return 'unavailable';
    const item = cachedDiscordStatus.webhooks.find((entry) => entry.alias === alias);
    return item ? item.status : 'unavailable';
  }

  function renderWebhookReadiness(status) {
    if (status === 'ready') {
      return '<span class="badge badge-success">Webhook ready</span>';
    }
    if (status === 'missing_permission') {
      return '<span class="badge badge-warning">Manage Webhooks required</span>';
    }
    return '<span class="badge badge-neutral">Webhook unavailable</span>';
  }

  function renderDiscordStatus(data) {
    if (!data) return;
    cachedDiscordStatus = data;
    const missingPermission = Array.isArray(data.webhooks) && data.webhooks.some((item) => item.status === 'missing_permission');

    if (!data.configured) {
      discordStatusIndicator.className = 'status-indicator indicator-neutral';
      discordStatusText.className = 'status-badge badge-neutral';
      discordStatusText.textContent = 'Not Configured';
      discordStatusDesc.textContent = 'Set DISCORD_BOT_TOKEN or DISCORD_BOT_TOKEN_FILE on the server and restart message-sync. Credentials are deployment-only and are never entered in this browser.';
      if (btnDiscordDiscover) btnDiscordDiscover.disabled = true;
    } else if (data.connected) {
      discordStatusIndicator.className = missingPermission ? 'status-indicator indicator-warning' : 'status-indicator indicator-success';
      discordStatusText.className = missingPermission ? 'status-badge badge-warning' : 'status-badge badge-success';
      discordStatusText.textContent = missingPermission ? 'Connected • Permission Action Needed' : 'Connected & Active';
      discordStatusDesc.textContent = missingPermission
        ? 'Discord is connected, but one or more configured endpoints need the bot permission Manage Webhooks. Grant it in those channels, then refresh.'
        : 'Discord gateway is connected. Live channel discovery is available.';
      if (btnDiscordDiscover) btnDiscordDiscover.disabled = false;
    } else {
      discordStatusIndicator.className = 'status-indicator indicator-warning';
      discordStatusText.className = 'status-badge badge-warning';
      discordStatusText.textContent = 'Configured • Disconnected';
      discordStatusDesc.textContent = 'A Discord credential source is configured, but the gateway is not currently connected.';
      if (btnDiscordDiscover) btnDiscordDiscover.disabled = true;
    }

    renderConfiguredDiscordEndpoints();
  }

  function renderConfiguredDiscordEndpoints() {
    if (!discordConfiguredBody) return;
    const endpoints = cachedEndpoints.filter((endpoint) => endpoint.transport === 'discord');
    if (endpoints.length === 0) {
      discordConfiguredBody.innerHTML = '';
      if (discordConfiguredEmpty) discordConfiguredEmpty.classList.remove('hidden');
      return;
    }
    if (discordConfiguredEmpty) discordConfiguredEmpty.classList.add('hidden');
    discordConfiguredBody.innerHTML = endpoints.map((endpoint) => {
      const syncSet = endpoint.syncSetId
        ? `<span class="badge-assigned">${escapeHtml(endpoint.syncSetId)}</span>`
        : '<span class="badge-unassigned">Unassigned</span>';
      return `
        <tr>
          <td><span class="alias-badge">${escapeHtml(endpoint.alias)}</span></td>
          <td><span class="jid-text">${escapeHtml(endpoint.remoteId)}</span></td>
          <td>${syncSet}</td>
          <td>${renderWebhookReadiness(webhookStatusForAlias(endpoint.alias))}</td>
        </tr>
      `;
    }).join('');
  }

  async function loadDiscordStatus(showSpinner = true) {
    if (showSpinner && btnDiscordRefresh) setButtonLoading(btnDiscordRefresh, true);
    try {
      const [status, endpoints, syncSets] = await Promise.all([
        window.API.getDiscordStatus(),
        window.API.getEndpoints(),
        window.API.getSyncSets()
      ]);
      cachedEndpoints = Array.isArray(endpoints) ? endpoints : [];
      cachedSyncSets = Array.isArray(syncSets) ? syncSets : [];
      renderDiscordStatus(status);
      if (cachedDiscordChannels.length > 0) renderDiscordDiscovery();
    } catch (err) {
      if (discordStatusIndicator) discordStatusIndicator.className = 'status-indicator indicator-danger';
      if (discordStatusText) {
        discordStatusText.className = 'status-badge badge-danger';
        discordStatusText.textContent = 'Service Unavailable';
      }
      if (discordStatusDesc) discordStatusDesc.textContent = err.message || 'Unable to query Discord status.';
    } finally {
      if (showSpinner && btnDiscordRefresh) setButtonLoading(btnDiscordRefresh, false);
    }
  }

  function suggestedDiscordAlias(channel) {
    const base = sanitizeAlias(channel.channelName) || 'discord_channel';
    let candidate = base;
    let suffix = 2;
    const aliases = new Set(cachedEndpoints.map((endpoint) => endpoint.alias));
    while (aliases.has(candidate)) {
      const suffixText = `_${suffix++}`;
      candidate = (base.slice(0, Math.max(1, 64 - suffixText.length)) + suffixText).slice(0, 64);
    }
    return candidate;
  }

  function renderDiscordDiscovery() {
    if (!discordDiscoveryBody) return;
    if (!Array.isArray(cachedDiscordChannels) || cachedDiscordChannels.length === 0) {
      discordDiscoveryBody.innerHTML = '';
      if (discordDiscoveryEmpty) {
        discordDiscoveryEmpty.classList.remove('hidden');
        discordDiscoveryEmpty.querySelector('.empty-title').textContent = 'No Discoverable Text Channels';
        discordDiscoveryEmpty.querySelector('.empty-desc').textContent = 'The connected bot did not return any guild text or announcement channels.';
      }
      return;
    }
    if (discordDiscoveryEmpty) discordDiscoveryEmpty.classList.add('hidden');

    discordDiscoveryBody.innerHTML = cachedDiscordChannels.map((channel) => {
      const existing = cachedEndpoints.find((endpoint) => endpoint.transport === 'discord' && endpoint.remoteId === channel.channelId);
      if (existing) {
        const setText = existing.syncSetId ? ` • Sync set: ${escapeHtml(existing.syncSetId)}` : ' • Unassigned';
        return `
          <tr>
            <td>${escapeHtml(channel.guildName || channel.guildId)}</td>
            <td><strong>#${escapeHtml(channel.channelName || channel.channelId)}</strong></td>
            <td><span class="jid-text">${escapeHtml(channel.channelId)}</span></td>
            <td><span class="alias-badge">${escapeHtml(existing.alias)}</span>${setText}<br>${renderWebhookReadiness(webhookStatusForAlias(existing.alias))}</td>
          </tr>
        `;
      }

      const syncOptions = ['<option value="">-- Unassigned --</option>']
        .concat(cachedSyncSets.map((set) => `<option value="${escapeHtml(set.id)}">${escapeHtml(set.id)}</option>`))
        .join('');
      return `
        <tr>
          <td>${escapeHtml(channel.guildName || channel.guildId)}</td>
          <td><strong>#${escapeHtml(channel.channelName || channel.channelId)}</strong></td>
          <td><span class="jid-text">${escapeHtml(channel.channelId)}</span></td>
          <td>
            <div class="form-grid-2">
              <input class="form-input font-mono discord-alias-input" data-channel-id="${escapeHtml(channel.channelId)}" value="${escapeHtml(suggestedDiscordAlias(channel))}" aria-label="Endpoint alias">
              <select class="form-select discord-sync-set-select" data-channel-id="${escapeHtml(channel.channelId)}">${syncOptions}</select>
            </div>
            <button type="button" class="btn btn-primary btn-sm btn-configure-discord-channel" data-channel-id="${escapeHtml(channel.channelId)}" style="margin-top:.5rem;">Configure Endpoint</button>
          </td>
        </tr>
      `;
    }).join('');

    discordDiscoveryBody.querySelectorAll('.btn-configure-discord-channel').forEach((btn) => {
      btn.addEventListener('click', async () => {
        const channelId = btn.getAttribute('data-channel-id');
        const aliasInput = discordDiscoveryBody.querySelector(`.discord-alias-input[data-channel-id="${channelId}"]`);
        const syncSelect = discordDiscoveryBody.querySelector(`.discord-sync-set-select[data-channel-id="${channelId}"]`);
        const alias = aliasInput ? aliasInput.value.trim() : '';
        const syncSetId = syncSelect && syncSelect.value ? syncSelect.value : null;
        if (!ALIAS_REGEX.test(alias)) {
          showToast('Alias must start with an alphanumeric character and contain up to 64 letters, numbers, hyphens, or underscores.', 'warning');
          if (aliasInput) aliasInput.focus();
          return;
        }
        setButtonLoading(btn, true);
        try {
          await window.API.createEndpoint({ alias, transport: 'discord', remoteId: channelId, syncSetId });
          showToast(`Discord endpoint '${alias}' configured.`, 'success');
          await loadDiscordStatus(false);
          await discoverDiscordChannels();
        } catch (err) {
          showToast(err.message || 'Failed to configure Discord endpoint', 'danger');
        } finally {
          setButtonLoading(btn, false);
        }
      });
    });
  }

  async function discoverDiscordChannels() {
    if (!btnDiscordDiscover) return;
    setButtonLoading(btnDiscordDiscover, true);
    if (discordDiscoveryAlert) discordDiscoveryAlert.classList.add('hidden');
    try {
      const [channels, endpoints, syncSets, status] = await Promise.all([
        window.API.getDiscordChannels(),
        window.API.getEndpoints(),
        window.API.getSyncSets(),
        window.API.getDiscordStatus()
      ]);
      cachedDiscordChannels = Array.isArray(channels) ? channels : [];
      cachedEndpoints = Array.isArray(endpoints) ? endpoints : [];
      cachedSyncSets = Array.isArray(syncSets) ? syncSets : [];
      cachedDiscordStatus = status;
      renderDiscordStatus(status);
      renderDiscordDiscovery();
    } catch (err) {
      cachedDiscordChannels = [];
      renderDiscordDiscovery();
      if (discordDiscoveryAlert) {
        discordDiscoveryAlert.textContent = err.message || 'Discord channel discovery failed.';
        discordDiscoveryAlert.classList.remove('hidden');
      }
    } finally {
      setButtonLoading(btnDiscordDiscover, false);
    }
  }

  /**
   * Telegram Admin & Transient Discovery Controller
   */
  function telegramEndpointStatusForAlias(alias) {
    if (!cachedTelegramStatus || !Array.isArray(cachedTelegramStatus.endpoints)) return 'unavailable';
    const item = cachedTelegramStatus.endpoints.find((entry) => entry.alias === alias);
    return item ? item.status : 'unavailable';
  }

  function renderTelegramReadiness(status) {
    if (status === 'ready') return '<span class="badge badge-success">Polling ready</span>';
    return '<span class="badge badge-neutral">Unavailable</span>';
  }

  function renderTelegramStatus(data) {
    if (!data) return;
    cachedTelegramStatus = data;

    if (!data.tokenConfigured) {
      telegramStatusIndicator.className = 'status-indicator indicator-neutral';
      telegramStatusText.className = 'status-badge badge-neutral';
      telegramStatusText.textContent = 'Not Configured';
      telegramStatusDesc.textContent = 'Set TELEGRAM_BOT_TOKEN or TELEGRAM_BOT_TOKEN_FILE on the server and restart message-sync. Credentials are deployment-only and are never entered in this browser.';
      if (btnTelegramDiscover) btnTelegramDiscover.disabled = true;
    } else if (data.running) {
      telegramStatusIndicator.className = 'status-indicator indicator-success';
      telegramStatusText.className = 'status-badge badge-success';
      telegramStatusText.textContent = 'Long Polling Active';
      if (data.privacyModeKnown) {
        telegramStatusDesc.textContent = data.privacyModeEnabled
          ? 'Bot Privacy Mode is enabled. Disable it or grant appropriate administrator visibility for ordinary group messages, then send a group message to refresh discovery.'
          : 'Bot Privacy Mode is disabled. Send a group message to make that group appear in transient discovery.';
      } else {
        telegramStatusDesc.textContent = data.visibilityGuidance || 'Send a group message to make that group appear in transient discovery.';
      }
      if (btnTelegramDiscover) btnTelegramDiscover.disabled = false;
    } else {
      telegramStatusIndicator.className = 'status-indicator indicator-warning';
      telegramStatusText.className = 'status-badge badge-warning';
      telegramStatusText.textContent = 'Configured • Polling Stopped';
      telegramStatusDesc.textContent = 'A Telegram credential source is configured, but long polling is not currently running.';
      if (btnTelegramDiscover) btnTelegramDiscover.disabled = true;
    }

    renderConfiguredTelegramEndpoints();
  }

  function renderConfiguredTelegramEndpoints() {
    if (!telegramConfiguredBody) return;
    const endpoints = cachedEndpoints.filter((endpoint) => endpoint.transport === 'telegram');
    if (endpoints.length === 0) {
      telegramConfiguredBody.innerHTML = '';
      if (telegramConfiguredEmpty) telegramConfiguredEmpty.classList.remove('hidden');
      return;
    }
    if (telegramConfiguredEmpty) telegramConfiguredEmpty.classList.add('hidden');
    telegramConfiguredBody.innerHTML = endpoints.map((endpoint) => {
      const syncSet = endpoint.syncSetId
        ? `<span class="badge-assigned">${escapeHtml(endpoint.syncSetId)}</span>`
        : '<span class="badge-unassigned">Unassigned</span>';
      return `
        <tr>
          <td><span class="alias-badge">${escapeHtml(endpoint.alias)}</span></td>
          <td><span class="jid-text">${escapeHtml(endpoint.remoteId)}</span></td>
          <td>${syncSet}</td>
          <td>${renderTelegramReadiness(telegramEndpointStatusForAlias(endpoint.alias))}</td>
        </tr>
      `;
    }).join('');
  }

  async function loadTelegramStatus(showSpinner = true) {
    if (showSpinner && btnTelegramRefresh) setButtonLoading(btnTelegramRefresh, true);
    try {
      const [status, endpoints, syncSets] = await Promise.all([
        window.API.getTelegramStatus(),
        window.API.getEndpoints(),
        window.API.getSyncSets()
      ]);
      cachedEndpoints = Array.isArray(endpoints) ? endpoints : [];
      cachedSyncSets = Array.isArray(syncSets) ? syncSets : [];
      renderTelegramStatus(status);
      if (cachedTelegramChats.length > 0) renderTelegramDiscovery();
    } catch (err) {
      if (telegramStatusIndicator) telegramStatusIndicator.className = 'status-indicator indicator-danger';
      if (telegramStatusText) {
        telegramStatusText.className = 'status-badge badge-danger';
        telegramStatusText.textContent = 'Service Unavailable';
      }
      if (telegramStatusDesc) telegramStatusDesc.textContent = err.message || 'Unable to query Telegram status.';
    } finally {
      if (showSpinner && btnTelegramRefresh) setButtonLoading(btnTelegramRefresh, false);
    }
  }

  function suggestedTelegramAlias() {
    const base = 'telegram_group';
    let candidate = base;
    let suffix = 2;
    const aliases = new Set(cachedEndpoints.map((endpoint) => endpoint.alias));
    while (aliases.has(candidate)) {
      candidate = `${base}_${suffix++}`;
    }
    return candidate;
  }

  function renderTelegramDiscovery() {
    if (!telegramDiscoveryBody) return;
    if (!Array.isArray(cachedTelegramChats) || cachedTelegramChats.length === 0) {
      telegramDiscoveryBody.innerHTML = '';
      if (telegramDiscoveryEmpty) {
        telegramDiscoveryEmpty.classList.remove('hidden');
        telegramDiscoveryEmpty.querySelector('.empty-title').textContent = 'No Observed Telegram Groups';
        telegramDiscoveryEmpty.querySelector('.empty-desc').textContent = 'Add the bot to a group/supergroup, ensure it can receive messages, then send a group message and refresh discovery.';
      }
      return;
    }
    if (telegramDiscoveryEmpty) telegramDiscoveryEmpty.classList.add('hidden');

    telegramDiscoveryBody.innerHTML = cachedTelegramChats.map((chat) => {
      const existing = cachedEndpoints.find((endpoint) => endpoint.transport === 'telegram' && endpoint.remoteId === chat.chatId);
      const title = chat.title || chat.username || 'Observed group';
      const username = chat.username ? `@${escapeHtml(chat.username)}` : '—';
      if (existing) {
        const setText = existing.syncSetId ? ` • Sync set: ${escapeHtml(existing.syncSetId)}` : ' • Unassigned';
        return `
          <tr>
            <td><strong>${escapeHtml(title)}</strong></td>
            <td>${username}</td>
            <td><span class="badge badge-neutral">${escapeHtml(chat.type)}</span></td>
            <td><span class="jid-text">${escapeHtml(chat.chatId)}</span></td>
            <td><span class="alias-badge">${escapeHtml(existing.alias)}</span>${setText}</td>
          </tr>
        `;
      }

      const syncOptions = ['<option value="">-- Unassigned --</option>']
        .concat(cachedSyncSets.map((set) => `<option value="${escapeHtml(set.id)}">${escapeHtml(set.id)}</option>`))
        .join('');
      return `
        <tr>
          <td><strong>${escapeHtml(title)}</strong></td>
          <td>${username}</td>
          <td><span class="badge badge-neutral">${escapeHtml(chat.type)}</span></td>
          <td><span class="jid-text">${escapeHtml(chat.chatId)}</span></td>
          <td>
            <div class="form-grid-2">
              <input class="form-input font-mono telegram-alias-input" data-chat-id="${escapeHtml(chat.chatId)}" value="${escapeHtml(suggestedTelegramAlias())}" aria-label="Endpoint alias">
              <select class="form-select telegram-sync-set-select" data-chat-id="${escapeHtml(chat.chatId)}">${syncOptions}</select>
            </div>
            <button type="button" class="btn btn-primary btn-sm btn-configure-telegram-chat" data-chat-id="${escapeHtml(chat.chatId)}" style="margin-top:.5rem;">Configure Endpoint</button>
          </td>
        </tr>
      `;
    }).join('');

    telegramDiscoveryBody.querySelectorAll('.btn-configure-telegram-chat').forEach((btn) => {
      btn.addEventListener('click', async () => {
        const chatId = btn.getAttribute('data-chat-id');
        const aliasInput = telegramDiscoveryBody.querySelector(`.telegram-alias-input[data-chat-id="${chatId}"]`);
        const syncSelect = telegramDiscoveryBody.querySelector(`.telegram-sync-set-select[data-chat-id="${chatId}"]`);
        const alias = aliasInput ? aliasInput.value.trim() : '';
        const syncSetId = syncSelect && syncSelect.value ? syncSelect.value : null;
        if (!ALIAS_REGEX.test(alias)) {
          showToast('Alias must start with an alphanumeric character and contain up to 64 letters, numbers, hyphens, or underscores.', 'warning');
          if (aliasInput) aliasInput.focus();
          return;
        }
        if (!TELEGRAM_CHAT_ID_REGEX.test(chatId || '')) {
          showToast('Observed Telegram chat ID is invalid.', 'danger');
          return;
        }
        setButtonLoading(btn, true);
        try {
          await window.API.createEndpoint({ alias, transport: 'telegram', remoteId: chatId, syncSetId });
          showToast(`Telegram endpoint '${alias}' configured.`, 'success');
          await loadTelegramStatus(false);
          await discoverTelegramChats();
        } catch (err) {
          showToast(err.message || 'Failed to configure Telegram endpoint', 'danger');
        } finally {
          setButtonLoading(btn, false);
        }
      });
    });
  }

  async function discoverTelegramChats() {
    if (!btnTelegramDiscover) return;
    setButtonLoading(btnTelegramDiscover, true);
    if (telegramDiscoveryAlert) telegramDiscoveryAlert.classList.add('hidden');
    try {
      const [chats, endpoints, syncSets, status] = await Promise.all([
        window.API.getTelegramChats(),
        window.API.getEndpoints(),
        window.API.getSyncSets(),
        window.API.getTelegramStatus()
      ]);
      cachedTelegramChats = Array.isArray(chats) ? chats : [];
      cachedEndpoints = Array.isArray(endpoints) ? endpoints : [];
      cachedSyncSets = Array.isArray(syncSets) ? syncSets : [];
      cachedTelegramStatus = status;
      renderTelegramStatus(status);
      renderTelegramDiscovery();
    } catch (err) {
      cachedTelegramChats = [];
      renderTelegramDiscovery();
      if (telegramDiscoveryAlert) {
        telegramDiscoveryAlert.textContent = err.message || 'Telegram chat discovery failed.';
        telegramDiscoveryAlert.classList.remove('hidden');
      }
    } finally {
      setButtonLoading(btnTelegramDiscover, false);
    }
  }

  /**
   * Endpoint View Controller
   */
  async function loadGroups() {
    try {
      const [endpointsRes, syncSetsRes] = await Promise.all([
        window.API.getEndpoints(),
        window.API.getSyncSets()
      ]);
      cachedEndpoints = Array.isArray(endpointsRes) ? endpointsRes : [];
      cachedSyncSets = Array.isArray(syncSetsRes) ? syncSetsRes : [];
      renderGroupsTable(searchGroupsInput ? searchGroupsInput.value : '');
    } catch (err) {
      console.error('Failed to load endpoints', err);
      showToast(err.message || 'Failed to load endpoint list', 'danger');
    }
  }

  function renderGroupsTable(query = '') {
    if (!groupsTableBody) return;
    const q = query.trim().toLowerCase();
    const filtered = cachedEndpoints.filter((endpoint) => {
      if (!q) return true;
      return [endpoint.alias, endpoint.transport, endpoint.remoteId, endpoint.syncSetId]
        .some((value) => value && String(value).toLowerCase().includes(q));
    });

    if (groupsCountBadge) {
      groupsCountBadge.textContent = `${cachedEndpoints.length} Endpoint${cachedEndpoints.length === 1 ? '' : 's'}`;
    }
    if (filtered.length === 0) {
      groupsTableBody.innerHTML = '';
      if (groupsEmptyState) groupsEmptyState.classList.remove('hidden');
      return;
    }
    if (groupsEmptyState) groupsEmptyState.classList.add('hidden');

    groupsTableBody.innerHTML = filtered.map((endpoint) => {
      const syncBadge = endpoint.syncSetId
        ? `<span class="badge-assigned">${escapeHtml(endpoint.syncSetId)}</span>`
        : '<span class="badge-unassigned">Unassigned</span>';
      const transportBadge = endpoint.transport === 'discord'
        ? '<span class="badge badge-primary">Discord</span>'
        : endpoint.transport === 'telegram'
          ? '<span class="badge badge-warning">Telegram</span>'
          : '<span class="badge badge-success">WhatsApp</span>';
      return `
        <tr data-alias="${escapeHtml(endpoint.alias)}">
          <td><span class="alias-badge">${escapeHtml(endpoint.alias)}</span></td>
          <td>${transportBadge}</td>
          <td><span class="jid-text">${escapeHtml(endpoint.remoteId)}</span></td>
          <td>${syncBadge}</td>
          <td class="text-right">
            <div class="table-actions">
              <button class="btn-action-icon btn-edit-group" data-alias="${escapeHtml(endpoint.alias)}" title="Edit Endpoint">
                <svg class="icon icon-sm" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M11 4H4a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7"></path><path d="M18.5 2.5a2.121 2.121 0 0 1 3 3L12 15l-4 1 1-4 9.5-9.5z"></path></svg>
              </button>
              <button class="btn-action-icon btn-action-delete btn-delete-group" data-alias="${escapeHtml(endpoint.alias)}" title="Delete Endpoint">
                <svg class="icon icon-sm" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><polyline points="3 6 5 6 21 6"></polyline><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"></path></svg>
              </button>
            </div>
          </td>
        </tr>
      `;
    }).join('');

    groupsTableBody.querySelectorAll('.btn-edit-group').forEach((btn) => {
      btn.addEventListener('click', () => openEditGroupModal(btn.getAttribute('data-alias')));
    });
    groupsTableBody.querySelectorAll('.btn-delete-group').forEach((btn) => {
      btn.addEventListener('click', () => handleDeleteGroup(btn.getAttribute('data-alias')));
    });
  }

  function populateSyncSetSelect(selectedId = '') {
    if (!selectGroupSyncSet) return;
    selectGroupSyncSet.innerHTML = '<option value="">-- None (Unassigned) --</option>';
    cachedSyncSets.forEach((set) => {
      const opt = document.createElement('option');
      opt.value = set.id;
      opt.textContent = `${set.id} (${set.endpoints ? set.endpoints.length : 0} members)`;
      if (set.id === selectedId) opt.selected = true;
      selectGroupSyncSet.appendChild(opt);
    });
  }

  function sanitizeAlias(name) {
    if (!name) return '';
    let s = name.trim().toLowerCase().replace(/[^a-z0-9_-]+/g, '_').replace(/^_+|_+$/g, '');
    if (s.length > 64) s = s.substring(0, 64);
    if (!/^[a-z0-9]/.test(s)) s = 'grp_' + s;
    return s.slice(0, 64);
  }

  async function openAddGroupModal() {
    editingGroupAlias = null;
    editingEndpointTransport = 'whatsapp';
    modalGroupTitle.textContent = 'Add WhatsApp Endpoint';
    modalGroupAlert.classList.add('hidden');
    inputGroupAlias.value = '';
    inputGroupAlias.disabled = false;
    inputGroupJid.value = '';
    populateSyncSetSelect('');

    if (selectGroupWA) {
      selectGroupWA.disabled = true;
      selectGroupWA.innerHTML = '<option value="">Fetching endpoints...</option>';
    }
    if (selectGroupWAHint) selectGroupWAHint.textContent = 'Fetching joined groups from active WhatsApp session...';
    openModal(modalGroup);

    try {
      const [waGroupsRes, endpointsRes] = await Promise.all([
        window.API.getWhatsAppJoinedGroups().catch(() => []),
        window.API.getEndpoints().catch(() => [])
      ]);
      cachedEndpoints = Array.isArray(endpointsRes) ? endpointsRes : cachedEndpoints;
      const waGroups = Array.isArray(waGroupsRes) ? waGroupsRes : [];
      if (!selectGroupWA) return;
      selectGroupWA.disabled = false;

      if (waGroups.length === 0) {
        selectGroupWA.innerHTML = '<option value="">No joined WhatsApp groups found</option>';
        if (selectGroupWAHint) selectGroupWAHint.textContent = 'WhatsApp is not connected or no groups have been joined.';
        return;
      }

      selectGroupWA.innerHTML = '<option value="">-- Select a WhatsApp Group --</option>' +
        waGroups.map((group) => {
          const existing = cachedEndpoints.find((endpoint) => endpoint.transport === 'whatsapp' && endpoint.remoteId === group.jid);
          if (existing) {
            return `<option value="${escapeHtml(group.jid)}" disabled>${escapeHtml(group.name || group.jid)} (Configured as: ${escapeHtml(existing.alias)})</option>`;
          }
          return `<option value="${escapeHtml(group.jid)}" data-name="${escapeHtml(group.name || '')}">${escapeHtml(group.name || group.jid)}</option>`;
        }).join('');
      if (selectGroupWAHint) selectGroupWAHint.textContent = 'Select an active WhatsApp group to assign an endpoint alias.';
    } catch (err) {
      if (selectGroupWA) {
        selectGroupWA.disabled = false;
        selectGroupWA.innerHTML = '<option value="">Failed to load WhatsApp groups</option>';
      }
      if (selectGroupWAHint) selectGroupWAHint.textContent = err.message || 'Error fetching WhatsApp groups.';
    }
  }

  if (selectGroupWA) {
    selectGroupWA.addEventListener('change', () => {
      const selectedJid = selectGroupWA.value;
      inputGroupJid.value = selectedJid;
      const selectedOpt = selectGroupWA.options[selectGroupWA.selectedIndex];
      if (selectedOpt && selectedOpt.dataset.name && !inputGroupAlias.value) {
        const suggested = sanitizeAlias(selectedOpt.dataset.name);
        if (suggested && !cachedEndpoints.some((endpoint) => endpoint.alias === suggested)) inputGroupAlias.value = suggested;
      }
    });
  }

  function openEditGroupModal(alias) {
    const endpoint = cachedEndpoints.find((item) => item.alias === alias);
    if (!endpoint) return;
    editingGroupAlias = alias;
    editingEndpointTransport = endpoint.transport;
    modalGroupTitle.textContent = `Edit Endpoint: ${alias}`;
    modalGroupAlert.classList.add('hidden');
    inputGroupAlias.value = endpoint.alias;
    inputGroupAlias.disabled = true;
    inputGroupJid.value = endpoint.remoteId;

    if (selectGroupWA) {
      const transportLabel = endpoint.transport === 'discord'
        ? 'Discord channel'
        : endpoint.transport === 'telegram'
          ? 'Telegram group/supergroup'
          : 'WhatsApp group';
      selectGroupWA.innerHTML = `<option value="${escapeHtml(endpoint.remoteId)}" selected>${transportLabel} • ${escapeHtml(endpoint.remoteId)}</option>`;
      selectGroupWA.disabled = true;
    }
    if (selectGroupWAHint) {
      selectGroupWAHint.textContent = 'Transport target is locked for the existing alias; edit sync-set membership here.';
    }
    populateSyncSetSelect(endpoint.syncSetId || '');
    openModal(modalGroup);
  }

  async function handleGroupFormSubmit(e) {
    e.preventDefault();
    modalGroupAlert.classList.add('hidden');
    const alias = inputGroupAlias.value.trim();
    const remoteId = inputGroupJid.value.trim();
    const syncSetId = selectGroupSyncSet.value.trim() || null;
    const transport = editingGroupAlias ? editingEndpointTransport : 'whatsapp';

    if ((transport === 'whatsapp' && (!remoteId || !GROUP_JID_REGEX.test(remoteId))) ||
        (transport === 'discord' && (!remoteId || !DISCORD_CHANNEL_ID_REGEX.test(remoteId))) ||
        (transport === 'telegram' && (!remoteId || !TELEGRAM_CHAT_ID_REGEX.test(remoteId)))) {
      modalGroupAlert.textContent = transport === 'discord'
        ? 'Discord channel ID is invalid.'
        : transport === 'telegram'
          ? 'Telegram group/supergroup chat ID is invalid.'
          : 'Please select a valid WhatsApp group.';
      modalGroupAlert.classList.remove('hidden');
      return;
    }
    if (!ALIAS_REGEX.test(alias)) {
      modalGroupAlert.textContent = 'Alias must start with an alphanumeric character and contain up to 64 letters, numbers, hyphens, or underscores.';
      modalGroupAlert.classList.remove('hidden');
      inputGroupAlias.focus();
      return;
    }

    setButtonLoading(btnSubmitGroup, true);
    try {
      if (editingGroupAlias) {
        await window.API.updateEndpoint(editingGroupAlias, { transport, remoteId, syncSetId });
        showToast(`Endpoint '${editingGroupAlias}' updated successfully.`, 'success');
      } else {
        await window.API.createEndpoint({ alias, transport, remoteId, syncSetId });
        showToast(`Endpoint '${alias}' created successfully.`, 'success');
      }
      closeModal(modalGroup);
      await loadGroups();
    } catch (err) {
      modalGroupAlert.textContent = err.message || 'Failed to save endpoint.';
      modalGroupAlert.classList.remove('hidden');
    } finally {
      setButtonLoading(btnSubmitGroup, false);
    }
  }

  function handleDeleteGroup(alias) {
    showConfirmDialog({
      title: `Delete Endpoint: ${alias}`,
      message: `Are you sure you want to delete endpoint '${alias}'? It will be detached from any assigned sync set.`,
      btnText: 'Delete Endpoint',
      isDanger: true,
      onConfirm: async () => {
        await window.API.deleteEndpoint(alias);
        showToast(`Endpoint '${alias}' deleted successfully.`, 'success');
        await loadGroups();
      }
    });
  }

  /**
   * Sync Sets View Controller
   */
  async function loadSyncSets() {
    try {
      const [syncSetsRes, endpointsRes] = await Promise.all([
        window.API.getSyncSets(),
        window.API.getEndpoints()
      ]);

      cachedSyncSets = Array.isArray(syncSetsRes) ? syncSetsRes : [];
      cachedEndpoints = Array.isArray(endpointsRes) ? endpointsRes : [];
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
      const matchMember = Array.isArray(set.endpoints) && set.endpoints.some((g) => g.toLowerCase().includes(q));
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
      const endpoints = Array.isArray(set.endpoints) ? set.endpoints : [];
      const memberChips = endpoints.length > 0
        ? endpoints.map((g) => `<span class="group-chip-tag"><svg class="icon icon-xs" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M17 21v-2a4 4 0 0 0-4-4H5a4 4 0 0 0-4 4v2"></path><circle cx="9" cy="7" r="4"></circle></svg>${escapeHtml(g)}</span>`).join('')
        : `<span class="no-members-text">No member endpoints assigned yet</span>`;

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
            <span class="sync-set-members-label">Connected Endpoints (${endpoints.length})</span>
            <div class="group-chips-container">
              ${memberChips}
            </div>
          </div>

          <div class="sync-set-footer">
            <span class="sync-set-count-pill">${endpoints.length >= 2 ? '<span class="text-success">● Active All-to-All Sync</span>' : '<span class="text-warning">● Need 2+ groups to sync</span>'}</span>
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

  function renderSyncSetEndpointChecklist(waGroups = [], endpoints = [], selectedEndpointAliases = [], currentSetId = '') {
    if (!syncSetEndpointsChecklist) return;

    const endpointMap = new Map();
    endpoints.forEach((endpoint) => {
      endpointMap.set(`${endpoint.transport}:${endpoint.remoteId}`, {
        remoteId: endpoint.remoteId,
        transport: endpoint.transport,
        name: '',
        alias: endpoint.alias,
        syncSetId: endpoint.syncSetId || null
      });
    });

    waGroups.forEach((group) => {
      const key = `whatsapp:${group.jid}`;
      if (endpointMap.has(key)) {
        endpointMap.get(key).name = group.name;
      } else {
        endpointMap.set(key, {
          remoteId: group.jid,
          transport: 'whatsapp',
          name: group.name,
          alias: null,
          syncSetId: null
        });
      }
    });

    const items = Array.from(endpointMap.values());
    if (items.length === 0) {
      syncSetEndpointsChecklist.innerHTML = '<div class="checklist-empty">No endpoints available. Pair WhatsApp or configure Discord/Telegram endpoints first.</div>';
      return;
    }

    syncSetEndpointsChecklist.innerHTML = items.map((item) => {
      const hasAlias = Boolean(item.alias);
      const isSelected = hasAlias && selectedEndpointAliases.includes(item.alias);
      const isAssignedOther = hasAlias && item.syncSetId && item.syncSetId !== currentSetId;
      const isAssignedThis = hasAlias && item.syncSetId === currentSetId;
      const isDisabled = !hasAlias || isAssignedOther;
      let statusBadge = '';
      if (!hasAlias) {
        statusBadge = '<span class="checklist-no-alias-tag">Needs Alias</span>';
      } else if (isAssignedOther) {
        statusBadge = `<span class="checklist-assigned-tag">In Set '${escapeHtml(item.syncSetId)}'</span>`;
      } else if (isAssignedThis) {
        statusBadge = '<span class="badge badge-success font-mono" style="font-size:0.72rem;">Current Member</span>';
      }
      const aliasBadge = hasAlias ? `<span class="checklist-alias">${escapeHtml(item.alias)}</span>` : '';
      const transportBadge = item.transport === 'discord'
        ? '<span class="badge badge-primary">Discord</span>'
        : item.transport === 'telegram'
          ? '<span class="badge badge-warning">Telegram</span>'
          : '<span class="badge badge-success">WhatsApp</span>';
      const defineAliasBtn = !hasAlias && item.transport === 'whatsapp'
        ? `<button type="button" class="btn-inline-alias" data-jid="${escapeHtml(item.remoteId)}" data-name="${escapeHtml(item.name || '')}">+ Define Alias</button>`
        : '';
      const displayName = item.name ? escapeHtml(item.name) : (hasAlias ? escapeHtml(item.alias) : escapeHtml(item.remoteId));

      return `
        <div class="checklist-item ${isSelected ? 'selected' : ''} ${isDisabled ? 'disabled' : ''}" data-remote-id="${escapeHtml(item.remoteId)}" data-alias="${escapeHtml(item.alias || '')}">
          <div class="checklist-item-row">
            <label class="checklist-item-main">
              <input type="checkbox" class="checklist-checkbox" value="${escapeHtml(item.alias || '')}" ${isSelected ? 'checked' : ''} ${isDisabled ? 'disabled' : ''}>
              <div class="checklist-info">
                <div class="checklist-group-name">${displayName}</div>
                <div class="checklist-group-meta">${transportBadge} ${aliasBadge} ${statusBadge}</div>
              </div>
            </label>
            <div class="checklist-action">${defineAliasBtn}</div>
          </div>
          ${item.transport === 'whatsapp' && !hasAlias ? `
          <div class="inline-alias-box hidden" id="alias-box-${escapeHtml(item.remoteId.replace(/[^a-zA-Z0-9]/g, '_'))}">
            <input type="text" class="inline-alias-input" placeholder="e.g. team_a" value="${escapeHtml(sanitizeAlias(item.name))}">
            <button type="button" class="btn btn-primary btn-sm btn-save-inline-alias" data-jid="${escapeHtml(item.remoteId)}">Save</button>
            <button type="button" class="btn btn-ghost btn-sm btn-cancel-inline-alias">Cancel</button>
          </div>` : ''}
        </div>
      `;
    }).join('');

    syncSetEndpointsChecklist.querySelectorAll('.checklist-checkbox').forEach((cb) => {
      cb.addEventListener('change', () => cb.closest('.checklist-item').classList.toggle('selected', cb.checked));
    });
    syncSetEndpointsChecklist.querySelectorAll('.btn-inline-alias').forEach((btn) => {
      btn.addEventListener('click', (e) => {
        e.preventDefault();
        const jid = btn.getAttribute('data-jid');
        const box = document.getElementById('alias-box-' + jid.replace(/[^a-zA-Z0-9]/g, '_'));
        if (box) {
          box.classList.toggle('hidden');
          const input = box.querySelector('.inline-alias-input');
          if (input) input.focus();
        }
      });
    });
    syncSetEndpointsChecklist.querySelectorAll('.btn-cancel-inline-alias').forEach((btn) => {
      btn.addEventListener('click', () => {
        const box = btn.closest('.inline-alias-box');
        if (box) box.classList.add('hidden');
      });
    });
    syncSetEndpointsChecklist.querySelectorAll('.btn-save-inline-alias').forEach((btn) => {
      btn.addEventListener('click', async () => {
        const jid = btn.getAttribute('data-jid');
        const box = btn.closest('.inline-alias-box');
        const input = box.querySelector('.inline-alias-input');
        const alias = input.value.trim();
        if (!ALIAS_REGEX.test(alias)) {
          showToast('Alias must start with alphanumeric character and contain up to 64 letters, numbers, hyphens, or underscores.', 'warning');
          input.focus();
          return;
        }
        btn.disabled = true;
        try {
          await window.API.createEndpoint({ alias, transport: 'whatsapp', remoteId: jid });
          showToast(`Endpoint alias '${alias}' saved.`, 'success');
          const [waGroupsRes, endpointsRes] = await Promise.all([
            window.API.getWhatsAppJoinedGroups().catch(() => []),
            window.API.getEndpoints()
          ]);
          cachedEndpoints = endpointsRes;
          const currentSelected = Array.from(syncSetEndpointsChecklist.querySelectorAll('.checklist-checkbox:checked')).map((checkbox) => checkbox.value);
          currentSelected.push(alias);
          renderSyncSetEndpointChecklist(waGroupsRes, cachedEndpoints, currentSelected, currentSetId);
        } catch (err) {
          showToast(err.message || 'Failed to save alias', 'danger');
        } finally {
          btn.disabled = false;
        }
      });
    });
  }

  async function openAddSyncSetModal() {
    editingSyncSetId = null;
    modalSyncSetTitle.textContent = 'Add Sync Set';
    modalSyncSetAlert.classList.add('hidden');
    inputSyncSetId.value = '';
    inputSyncSetId.disabled = false;

    if (syncSetEndpointsChecklist) {
      syncSetEndpointsChecklist.innerHTML = '<div class="checklist-empty"><div class="btn-spinner" style="display:inline-block; vertical-align:middle; margin-right:8px;"></div> Fetching endpoints...</div>';
    }
    openModal(modalSyncSet);
    inputSyncSetId.focus();

    try {
      const [waGroupsRes, endpointsRes] = await Promise.all([
        window.API.getWhatsAppJoinedGroups().catch(() => []),
        window.API.getEndpoints().catch(() => [])
      ]);
      cachedEndpoints = Array.isArray(endpointsRes) ? endpointsRes : cachedEndpoints;
      const waGroups = Array.isArray(waGroupsRes) ? waGroupsRes : [];
      renderSyncSetEndpointChecklist(waGroups, cachedEndpoints, [], '');
    } catch (err) {
      if (syncSetEndpointsChecklist) {
        syncSetEndpointsChecklist.innerHTML = `<div class="checklist-empty text-danger">${escapeHtml(err.message || 'Failed to load groups')}</div>`;
      }
    }
  }

  async function openEditSyncSetModal(id) {
    const set = cachedSyncSets.find((s) => s.id === id);
    if (!set) return;

    editingSyncSetId = id;
    modalSyncSetTitle.textContent = `Edit Sync Set: ${id}`;
    modalSyncSetAlert.classList.add('hidden');
    inputSyncSetId.value = set.id;
    inputSyncSetId.disabled = true;

    if (syncSetEndpointsChecklist) {
      syncSetEndpointsChecklist.innerHTML = '<div class="checklist-empty"><div class="btn-spinner" style="display:inline-block; vertical-align:middle; margin-right:8px;"></div> Fetching endpoints...</div>';
    }
    openModal(modalSyncSet);

    try {
      const [waGroupsRes, endpointsRes] = await Promise.all([
        window.API.getWhatsAppJoinedGroups().catch(() => []),
        window.API.getEndpoints().catch(() => [])
      ]);
      cachedEndpoints = Array.isArray(endpointsRes) ? endpointsRes : cachedEndpoints;
      const waGroups = Array.isArray(waGroupsRes) ? waGroupsRes : [];
      renderSyncSetEndpointChecklist(waGroups, cachedEndpoints, set.endpoints || [], id);
    } catch (err) {
      if (syncSetEndpointsChecklist) {
        syncSetEndpointsChecklist.innerHTML = `<div class="checklist-empty text-danger">${escapeHtml(err.message || 'Failed to load groups')}</div>`;
      }
    }
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

    const selectedEndpoints = [];
    if (syncSetEndpointsChecklist) {
      syncSetEndpointsChecklist.querySelectorAll('.checklist-checkbox:checked').forEach((cb) => {
        selectedEndpoints.push(cb.value);
      });
    }

    setButtonLoading(btnSubmitSyncSet, true);
    try {
      if (editingSyncSetId) {
        await window.API.updateSyncSet(editingSyncSetId, { endpoints: selectedEndpoints });
        showToast(`Sync set '${editingSyncSetId}' updated successfully.`, 'success');
      } else {
        await window.API.createSyncSet({ id, endpoints: selectedEndpoints });
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
    if (settingsPollsAggregationTrigger) settingsPollsAggregationTrigger.value = cfg.polls ? cfg.polls.aggregationTrigger : 'aggregate-response';
    if (settingsWhatsAppCleanupEnabled) settingsWhatsAppCleanupEnabled.checked = cfg.whatsappCleanup ? cfg.whatsappCleanup.enabled : false;
    if (settingsWhatsAppCleanupRetentionDays) settingsWhatsAppCleanupRetentionDays.value = cfg.whatsappCleanup ? cfg.whatsappCleanup.retentionDays : 30;
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
    const pollsAggregationTrigger = settingsPollsAggregationTrigger.value.trim();
    const whatsappCleanupEnabled = settingsWhatsAppCleanupEnabled ? settingsWhatsAppCleanupEnabled.checked : false;
    const whatsappCleanupRetentionDays = settingsWhatsAppCleanupRetentionDays ? parseInt(settingsWhatsAppCleanupRetentionDays.value, 10) : 30;

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

    if (!pollsAggregationTrigger) {
      settingsAlert.textContent = 'Polls aggregation trigger cannot be empty.';
      settingsAlert.classList.remove('hidden');
      settingsPollsAggregationTrigger.focus();
      return;
    }

    if (whatsappCleanupEnabled && (isNaN(whatsappCleanupRetentionDays) || whatsappCleanupRetentionDays < 1)) {
      settingsAlert.textContent = 'WhatsApp chat retention must be a positive integer (1 day minimum).';
      settingsAlert.classList.remove('hidden');
      if (settingsWhatsAppCleanupRetentionDays) settingsWhatsAppCleanupRetentionDays.focus();
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
      },
      polls: {
        aggregationTrigger: pollsAggregationTrigger
      },
      whatsappCleanup: {
        enabled: whatsappCleanupEnabled,
        retentionDays: isNaN(whatsappCleanupRetentionDays) || whatsappCleanupRetentionDays < 1 ? 30 : whatsappCleanupRetentionDays
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

  async function handleChangePasswordSubmit(e) {
    e.preventDefault();
    if (changePwdAlert) changePwdAlert.classList.add('hidden');

    const currentPassword = changePwdCurrent ? changePwdCurrent.value : '';
    const newPassword = changePwdNew ? changePwdNew.value : '';
    const confirmPassword = changePwdConfirm ? changePwdConfirm.value : '';

    if (!currentPassword) {
      if (changePwdAlert) {
        changePwdAlert.textContent = 'Current password is required.';
        changePwdAlert.classList.remove('hidden');
      }
      if (changePwdCurrent) changePwdCurrent.focus();
      return;
    }

    if (newPassword.length < 8 || newPassword.length > 72) {
      if (changePwdAlert) {
        changePwdAlert.textContent = 'New password must be between 8 and 72 characters.';
        changePwdAlert.classList.remove('hidden');
      }
      if (changePwdNew) changePwdNew.focus();
      return;
    }

    if (newPassword !== confirmPassword) {
      if (changePwdAlert) {
        changePwdAlert.textContent = 'New passwords do not match.';
        changePwdAlert.classList.remove('hidden');
      }
      if (changePwdConfirm) changePwdConfirm.focus();
      return;
    }

    setButtonLoading(btnSubmitChangePwd, true);
    try {
      await window.API.changePassword(currentPassword, newPassword);
      showToast('Admin password updated successfully!', 'success');
      if (formChangePassword) formChangePassword.reset();
      if (changePwdStrengthFill) changePwdStrengthFill.className = 'strength-fill';
      if (changePwdHint) {
        changePwdHint.className = 'form-hint';
        changePwdHint.textContent = 'Between 8 and 72 characters';
      }
      if (changePwdConfirmHint) changePwdConfirmHint.textContent = '';
    } catch (err) {
      if (changePwdAlert) {
        changePwdAlert.textContent = err.message || 'Failed to update password.';
        changePwdAlert.classList.remove('hidden');
      }
    } finally {
      setButtonLoading(btnSubmitChangePwd, false);
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
    if (formInvite) formInvite.addEventListener('submit', async (e) => { e.preventDefault(); try { const r=await window.API.redeemInvite(document.getElementById('invite-token').value,document.getElementById('invite-username').value,document.getElementById('invite-password').value); currentUser=r.user; isAuthenticated=true; window.Router.navigate('dashboard'); } catch(err) { document.getElementById('invite-alert').textContent=err.message; document.getElementById('invite-alert').classList.remove('hidden'); } });
    if (formReset) formReset.addEventListener('submit', async (e) => { e.preventDefault(); try { await window.API.resetPassword(document.getElementById('reset-token').value,document.getElementById('reset-password').value); window.Router.navigate('login'); } catch(err) { document.getElementById('reset-alert').textContent=err.message; document.getElementById('reset-alert').classList.remove('hidden'); } });
    const inviteButton = document.getElementById('btn-create-invite');
    if (inviteButton) inviteButton.addEventListener('click', async () => {
      try { const result = await window.API.createInvite(document.getElementById('invite-role').value, 24); document.getElementById('invite-result').textContent = `Copy this one-time token now: ${result.token}`; } catch (err) { showToast(err.message, 'danger'); }
    });

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
    if (btnWaLogout) btnWaLogout.addEventListener('click', handleLogoutWhatsApp);

    // Discord view
    if (btnDiscordRefresh) btnDiscordRefresh.addEventListener('click', () => loadDiscordStatus(true));
    if (btnDiscordDiscover) btnDiscordDiscover.addEventListener('click', discoverDiscordChannels);

    // Telegram view
    if (btnTelegramRefresh) btnTelegramRefresh.addEventListener('click', () => loadTelegramStatus(true));
    if (btnTelegramDiscover) btnTelegramDiscover.addEventListener('click', discoverTelegramChats);

    // Endpoint view
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
    if (formChangePassword) formChangePassword.addEventListener('submit', handleChangePasswordSubmit);
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
        cachedDiscordChannels = [];
        cachedDiscordStatus = null;
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
          currentUser = await window.API.getCurrentUser();
          const userEl = document.getElementById('session-username');
          const roleEl = document.getElementById('session-role');
          if (userEl && currentUser) userEl.textContent = currentUser.username;
          if (roleEl && currentUser) roleEl.textContent = currentUser.role;
          if (navUsers) navUsers.classList.toggle('hidden', !currentUser || currentUser.role !== 'admin');
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
    window.Router.addRoute('invite', () => switchView('invite'));
    window.Router.addRoute('reset', () => switchView('reset'));

    window.Router.addRoute('dashboard', () => {
      switchView('dashboard');
      loadDashboardStats();
      startDeliveryPolling();
    });

    window.Router.addRoute('whatsapp', () => {
      switchView('whatsapp');
      loadWhatsAppStatus(true);
    });

    window.Router.addRoute('discord', () => {
      switchView('discord');
      loadDiscordStatus(true);
    });

    window.Router.addRoute('telegram', () => {
      switchView('telegram');
      loadTelegramStatus(true);
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
    window.Router.addRoute('users', () => {
      if (!currentUser || currentUser.role !== 'admin') { window.Router.navigate('dashboard'); return; }
      switchView('users');
      window.API.getUsers().then(users => { const body=document.getElementById('users-table-body'); if (body) body.innerHTML=users.map(u=>`<tr><td>${escapeHtml(u.username)}</td><td>${escapeHtml(u.role)}</td><td>${u.active?'active':'inactive'}</td><td><button class="btn btn-sm btn-ghost" data-user-id="${escapeHtml(u.id)}" data-user-active="${u.active?'false':'true'}">${u.active?'Deactivate':'Reactivate'}</button> <button class="btn btn-sm btn-ghost" data-reset-id="${escapeHtml(u.id)}">Reset token</button></td></tr>`).join(''); body.querySelectorAll('[data-user-id]').forEach(b=>b.addEventListener('click',async()=>{try { await window.API.setUserActive(b.dataset.userId,b.dataset.userActive==='true'); window.Router.handleRouteChange(); } catch(err) { showToast(err.message,'danger'); }})); body.querySelectorAll('[data-reset-id]').forEach(b=>b.addEventListener('click',async()=>{try { const result=await window.API.createResetToken(b.dataset.resetId); showToast(`Copy this one-time reset token now: ${result.token}`,'success',10000); } catch(err) { showToast(err.message,'danger'); }})); }).catch(err=>showToast(err.message,'danger'));
    });
    async function loadMembership() { const status = document.getElementById('membership-status-filter')?.value || ''; const ageHours = document.getElementById('membership-age-filter')?.value || ''; const requests = await window.API.getMembershipRequests({ status, ageHours }); const body = document.getElementById('membership-table-body'); if (!body) return; body.innerHTML = requests.map(q => `<tr><td><button class="btn btn-sm btn-ghost" data-detail-id="${escapeHtml(q.id)}">${escapeHtml(q.id)}</button></td><td>${escapeHtml(q.status)}</td><td>${escapeHtml(q.verificationState)}</td><td>${escapeHtml(q.fulfillmentState)}${q.failureClass ? ` (${escapeHtml(q.failureClass)})` : ''}</td><td>${q.status === 'pending_admin' ? `<button class="btn btn-sm btn-primary" data-decision="approve" data-request-id="${escapeHtml(q.id)}">Approve</button> <button class="btn btn-sm btn-ghost" data-decision="reject" data-request-id="${escapeHtml(q.id)}">Reject</button> <button class="btn btn-sm btn-ghost" data-decision="needs_review" data-request-id="${escapeHtml(q.id)}">Needs review</button>` : q.status === 'approved' && q.fulfillmentState !== 'succeeded' ? `<button class="btn btn-sm btn-ghost" data-fulfill-id="${escapeHtml(q.id)}">Retry fulfillment</button>` : '—'}</td></tr>`).join(''); body.querySelectorAll('[data-decision]').forEach(b=>b.addEventListener('click',async()=>{try{await window.API.decideMembership(b.dataset.requestId,b.dataset.decision);await loadMembership()}catch(err){showToast(err.message,'danger')}})); body.querySelectorAll('[data-detail-id]').forEach(b=>b.addEventListener('click',async()=>{try{const q=await window.API.getMembershipRequest(b.dataset.detailId);document.getElementById('membership-detail').textContent=`${q.workEmail} | ${q.whatsappPhone || q.discordUserId} | LinkedIn: ${q.linkedinUrl || 'none'} | deterministic: ${q.deterministicResult || 'pending'} | AI: ${q.aiState || 'not configured'} ${q.aiConfidence || ''} ${q.aiAssessment || ''}`;}catch(err){showToast(err.message,'danger')}})); body.querySelectorAll('[data-fulfill-id]').forEach(b=>b.addEventListener('click',async()=>{try{await window.API.request(`/api/verification/requests/${encodeURIComponent(b.dataset.fulfillId)}/fulfill`,{method:'POST'});await loadMembership()}catch(err){showToast(err.message,'danger')}})); }
    window.Router.addRoute('membership', () => { switchView('membership'); loadMembership().catch(err=>showToast(err.message,'danger')); const admin = currentUser && currentUser.role === 'admin'; document.getElementById('membership-pipelines')?.classList.toggle('hidden', !admin); if (admin) { window.API.getVerificationPipelines().then(ps => { const el=document.getElementById('pipeline-list'); if (el) el.innerHTML=ps.map(p=>`<p>${escapeHtml(p.label)} — ${escapeHtml(p.endpointAlias)} — <a href="/api/verification/${encodeURIComponent(p.publicToken)}" target="_blank" rel="noreferrer">public link</a> <button class="btn btn-sm btn-ghost" data-pipeline-delete="${escapeHtml(p.id)}">Delete</button></p>`).join(''); el?.querySelectorAll('[data-pipeline-delete]').forEach(b=>b.addEventListener('click',async()=>{try{await window.API.deleteVerificationPipeline(b.dataset.pipelineDelete);window.Router.handleRouteChange()}catch(err){showToast(err.message,'danger')}})); }).catch(err=>showToast(err.message,'danger')); } });
    document.getElementById('membership-refresh')?.addEventListener('click', () => loadMembership().catch(err=>showToast(err.message,'danger')));
    document.getElementById('pipeline-create')?.addEventListener('click', async () => { try { await window.API.createVerificationPipeline({label:document.getElementById('pipeline-label').value,transport:document.getElementById('pipeline-transport').value,endpointAlias:document.getElementById('pipeline-endpoint').value,discordRoleId:document.getElementById('pipeline-role').value,enabled:true}); window.Router.handleRouteChange(); } catch(err) { showToast(err.message,'danger'); } });

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
