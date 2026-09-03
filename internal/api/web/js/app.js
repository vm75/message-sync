/**
 * Message Sync • Main Application Controller v4
 * Material-design UI: left-rail nav, dashboard platform cards,
 * unified sync-set editor with inline endpoint creation.
 */

(function () {
  'use strict';

  // ── Validation ──────────────────────────────────────────────
  const ALIAS_REGEX          = /^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$/;
  const GROUP_JID_REGEX      = /^[0-9]+(-[0-9]+)?@g\.us$/;
  const DISCORD_CHANNEL_REGEX = /^[0-9]+$/;
  const TELEGRAM_CHAT_ID_REGEX  = /^-[0-9]+$/;
  const SYNC_SET_ID_REGEX    = /^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$/;

  // Webhook readiness labels (used in delivery table and discovery)
  // status values: 'ready', 'missing_permission', 'unavailable'
  function renderWebhookReadiness(status) {
    if (status === 'ready') return '<span class="badge badge-success">Webhook ready</span>';
    if (status === 'missing_permission') return '<span class="badge badge-warning">Manage Webhooks required</span>';
    return '<span class="badge badge-neutral">Webhook unavailable</span>';
  }

  // ── DOM – shell ─────────────────────────────────────────────
  const appShell         = document.getElementById('app-shell');
  const toastContainer   = document.getElementById('toast-container');
  const btnLogout        = document.getElementById('btn-logout');
  const sessionUsername  = document.getElementById('session-username');
  const sessionRole      = document.getElementById('session-role');
  const navUsers         = document.getElementById('nav-users');
  const navMembership    = document.getElementById('nav-membership');

  // ── DOM – views ─────────────────────────────────────────────
  const views = {
    loading:      document.getElementById('view-loading'),
    setup:        document.getElementById('view-setup'),
    login:        document.getElementById('view-login'),
    invite:       document.getElementById('view-invite'),
    reset:        document.getElementById('view-reset'),
    dashboard:    document.getElementById('view-dashboard'),
    connections:  document.getElementById('view-connections'),
    'sync-sets':  document.getElementById('view-sync-sets'),
    settings:     document.getElementById('view-settings'),
    users:        document.getElementById('view-users'),
    membership:   document.getElementById('view-membership'),
  };

  // ── DOM – auth ──────────────────────────────────────────────
  const formSetup               = document.getElementById('form-setup');
  const setupUsernameInput      = document.getElementById('setup-username');
  const setupPasswordInput      = document.getElementById('setup-password');
  const setupConfirmPasswordInput = document.getElementById('setup-confirm-password');
  const setupStrengthFill       = document.getElementById('setup-strength-fill');
  const setupPasswordHint       = document.getElementById('setup-password-hint');
  const setupConfirmHint        = document.getElementById('setup-confirm-hint');
  const setupAlert              = document.getElementById('setup-alert');
  const btnSubmitSetup          = document.getElementById('btn-submit-setup');

  const formLogin               = document.getElementById('form-login');
  const loginUsernameInput      = document.getElementById('login-username');
  const loginPasswordInput      = document.getElementById('login-password');
  const loginAlert              = document.getElementById('login-alert');
  const btnSubmitLogin          = document.getElementById('btn-submit-login');

  const formInvite              = document.getElementById('form-invite');
  const formReset               = document.getElementById('form-reset');

  // ── DOM – dashboard ─────────────────────────────────────────
  const waStatusDot     = document.getElementById('wa-status-dot');
  const waStatusText    = document.getElementById('wa-status-text');
  const waStatusDesc    = document.getElementById('wa-status-desc');
  const discordDot      = document.getElementById('discord-status-dot');
  const discordText     = document.getElementById('discord-status-text');
  const discordDesc     = document.getElementById('discord-status-desc');
  const telegramDot     = document.getElementById('telegram-status-dot');
  const telegramText    = document.getElementById('telegram-status-text');
  const telegramDesc    = document.getElementById('telegram-status-desc');
  const deliveryBody    = document.getElementById('delivery-health-body');
  const deliveryEmpty   = document.getElementById('delivery-health-empty');
  const deliveryError   = document.getElementById('delivery-health-error');
  const btnDashRefresh  = document.getElementById('btn-dash-refresh');

  // WA menu actions
  const waMenuBtn       = document.getElementById('wa-menu-btn');
  const waMenu          = document.getElementById('wa-menu');
  const btnWaPair       = document.getElementById('btn-wa-pair');
  const btnWaLogout     = document.getElementById('btn-wa-logout');
  const discordMenuBtn  = document.getElementById('discord-menu-btn');
  const discordMenu     = document.getElementById('discord-menu');
  const telegramMenuBtn = document.getElementById('telegram-menu-btn');
  const telegramMenu    = document.getElementById('telegram-menu');

  // ── DOM – WA pair modal ──────────────────────────────────────
  const modalWaPair     = document.getElementById('modal-wa-pair');
  const waPairStatus    = document.getElementById('wa-pair-status');
  const waQrSection     = document.getElementById('wa-qr-section');
  const waQrCanvas      = document.getElementById('wa-qr-canvas');
  const waCountdownText = document.getElementById('wa-countdown-text');
  const btnWaCancelPair = document.getElementById('btn-wa-cancel-pair');

  // ── DOM – Connections ───────────────────────────────────────
  const navConnections        = document.getElementById('nav-connections');
  const btnConnectionsRefresh = document.getElementById('btn-connections-refresh');
  const btnAddConnection      = document.getElementById('btn-add-connection');
  const btnEmptyAddConnection = document.getElementById('btn-empty-add-connection');
  const connectionsContainer  = document.getElementById('connections-container');
  const connectionsEmpty      = document.getElementById('connections-empty');
  const connectionsAlert      = document.getElementById('connections-alert');

  const modalAddConnection    = document.getElementById('modal-add-connection');
  const formAddConnection     = document.getElementById('form-add-connection');
  const addConnAlert          = document.getElementById('add-conn-alert');
  const addConnLabel          = document.getElementById('add-conn-label');
  const addConnId             = document.getElementById('add-conn-id');
  const addConnToken          = document.getElementById('conn-bot-token');
  const addConnTokenGroup     = document.getElementById('add-conn-token-group');
  const btnSubmitAddConn      = document.getElementById('btn-submit-add-conn');

  const modalReplaceToken     = document.getElementById('modal-replace-token');
  const formReplaceToken      = document.getElementById('form-replace-token');
  const replaceTokenAlert     = document.getElementById('replace-token-alert');
  const replaceTokenConnInfo  = document.getElementById('replace-token-conn-info');
  const replaceTokenInput     = document.getElementById('replace-conn-bot-token');
  const btnSubmitReplaceToken = document.getElementById('btn-submit-replace-token');

  const modalDiscovery        = document.getElementById('modal-discovery');
  const discoveryConnBadge    = document.getElementById('discovery-conn-badge');
  const discoveryAlert        = document.getElementById('discovery-alert');
  const discoveryLoading      = document.getElementById('discovery-loading');
  const discoveryEmpty        = document.getElementById('discovery-empty');
  const discoveryEmptyTitle   = document.getElementById('discovery-empty-title');
  const discoveryEmptyDesc    = document.getElementById('discovery-empty-desc');
  const discoveryItems        = document.getElementById('discovery-items');
  const discoveryTbody        = document.getElementById('discovery-tbody');
  const discoveryCreateForm   = document.getElementById('discovery-create-form');
  const discoveryAliasInput   = document.getElementById('discovery-alias-input');
  const discoverySyncsetSelect = document.getElementById('discovery-syncset-select');
  const btnDiscoverySubmitCreate = document.getElementById('btn-discovery-submit-create');
  const btnDiscoveryCancelCreate = document.getElementById('btn-discovery-cancel-create');

  const modalReassignEndpoint = document.getElementById('modal-reassign-endpoint');
  const formReassignEndpoint  = document.getElementById('form-reassign-endpoint');
  const reassignAlert         = document.getElementById('reassign-alert');
  const reassignEndpointInfo  = document.getElementById('reassign-endpoint-info');
  const reassignConnSelect    = document.getElementById('reassign-conn-select');
  const btnSubmitReassign     = document.getElementById('btn-submit-reassign');

  const addEndpointConnSelect = document.getElementById('add-endpoint-conn-select');
  const addEndpointNoConn     = document.getElementById('add-endpoint-no-conn');

  // ── DOM – Sync Sets ──────────────────────────────────────────
  const btnNewSyncSet       = document.getElementById('btn-new-sync-set');
  const syncsetCountBadge   = document.getElementById('syncset-count-badge');
  const syncsetList         = document.getElementById('syncset-list');
  const syncsetEditorForm   = document.getElementById('syncset-editor-form');
  const syncsetEditorPlaceholder = document.getElementById('syncset-editor-placeholder');
  const syncsetEditorTitle  = document.getElementById('syncset-editor-title');
  const syncsetEditorIdDisplay = document.getElementById('syncset-editor-id-display');
  const inputSyncsetId      = document.getElementById('input-syncset-id');
  const syncsetIdField      = document.getElementById('syncset-id-field');
  const endpointChips       = document.getElementById('endpoint-chips');
  const btnDeleteSyncSet    = document.getElementById('btn-delete-sync-set');
  const btnSaveSyncSet      = document.getElementById('btn-save-sync-set');
  const btnCancelEditor     = document.getElementById('btn-cancel-editor');

  // Add endpoint widgets
  const transportTabs       = document.querySelectorAll('.transport-tab');
  const addRemoteLabel      = document.getElementById('add-remote-label');
  const addRemoteSelect     = document.getElementById('add-remote-select');
  const addRemoteHint       = document.getElementById('add-remote-hint');
  const addAliasInput       = document.getElementById('add-alias-input');
  const btnAddEndpoint      = document.getElementById('btn-add-endpoint');
  const addEndpointAlert    = document.getElementById('add-endpoint-alert');
  const platformSetupNote   = document.getElementById('platform-setup-note');
  const platformSetupNoteText = document.getElementById('platform-setup-note-text');

  // ── DOM – Settings ──────────────────────────────────────────
  const formSettings                 = document.getElementById('form-settings');
  const settingsAlert                = document.getElementById('settings-alert');
  const settingsUsernameMode         = document.getElementById('settings-username-mode');
  const settingsMediaEnabled         = document.getElementById('settings-media-enabled');
  const settingsMediaMaxSize         = document.getElementById('settings-media-max-size');
  const settingsRecoveryEnabled      = document.getElementById('settings-recovery-enabled');
  const settingsRecoveryMaxAge       = document.getElementById('settings-recovery-max-age');
  const settingsRecoveryMaxMsgs      = document.getElementById('settings-recovery-max-msgs');
  const settingsRetentionDays        = document.getElementById('settings-retention-days');
  const settingsPollsAggTrigger      = document.getElementById('settings-polls-aggregation-trigger');
  const settingsLocalPrefix          = document.getElementById('settings-local-prefix');
  const settingsWaCleanupEnabled     = document.getElementById('settings-whatsapp-cleanup-enabled');
  const settingsWaCleanupRetention   = document.getElementById('settings-whatsapp-cleanup-retention-days');
  const settingsWaDeviceName         = document.getElementById('settings-whatsapp-device-name');
  const btnResetSettings             = document.getElementById('btn-reset-settings');
  const btnSaveSettings              = document.getElementById('btn-save-settings');

  const formChangePassword           = document.getElementById('form-change-password');
  const changePwdAlert               = document.getElementById('change-pwd-alert');
  const changePwdCurrent             = document.getElementById('change-pwd-current');
  const changePwdNew                 = document.getElementById('change-pwd-new');
  const changePwdConfirm             = document.getElementById('change-pwd-confirm');
  const changePwdStrengthFill        = document.getElementById('change-pwd-strength-fill');
  const changePwdHint                = document.getElementById('change-pwd-hint');
  const changePwdConfirmHint         = document.getElementById('change-pwd-confirm-hint');
  const btnSubmitChangePwd           = document.getElementById('btn-submit-change-pwd');

  // ── DOM – Users ──────────────────────────────────────────────
  const inviteRole    = document.getElementById('invite-role');
  const btnCreateInvite = document.getElementById('btn-create-invite');
  const inviteResult  = document.getElementById('invite-result');
  const usersTableBody = document.getElementById('users-table-body');

  // ── DOM – Membership ─────────────────────────────────────────
  const membershipStatusFilter = document.getElementById('membership-status-filter');
  const membershipAgeFilter    = document.getElementById('membership-age-filter');
  const membershipRefresh      = document.getElementById('membership-refresh');
  const membershipTableBody    = document.getElementById('membership-table-body');
  const membershipDetail       = document.getElementById('membership-detail');
  const membershipPipelines    = document.getElementById('membership-pipelines');
  const pipelineLabel          = document.getElementById('pipeline-label');
  const pipelineTransport      = document.getElementById('pipeline-transport');
  const pipelineEndpoint       = document.getElementById('pipeline-endpoint');
  const pipelineRole           = document.getElementById('pipeline-role');
  const pipelineCreate         = document.getElementById('pipeline-create');
  const pipelineList           = document.getElementById('pipeline-list');

  // ── DOM – Confirm modal ──────────────────────────────────────
  const modalConfirm      = document.getElementById('modal-confirm');
  const modalConfirmTitle = document.getElementById('modal-confirm-title');
  const modalConfirmMsg   = document.getElementById('modal-confirm-msg');
  const btnConfirmOk      = document.getElementById('btn-confirm-ok');

  // ── Application State ────────────────────────────────────────
  let isSetup = null;
  let isAuthenticated = false;
  let currentUser = null;
  let cachedEndpoints = [];
  let cachedSyncSets = [];
  let cachedConfig = null;
  let cachedDiscordChannels = [];
  let cachedDiscordStatus = null;
  let cachedTelegramChats = [];
  let cachedWaGroups = [];

  // Sync set editor state
  let editingSyncSetId = null;      // null = new
  let editorEndpoints  = [];        // { alias, transport, remoteId }[] — pending set

  // WA polling / QR state
  let waPollTimer = null;
  let waCountdownTimer = null;
  let qrExpiresAt = null;
  let deliveryPollTimer = null;

  // Confirm callback
  let confirmCallback = null;

  // Active transport in add-endpoint form
  let activeTransport = 'whatsapp';

  // ══════════════════════════════════════════════════════════════
  // Helpers
  // ══════════════════════════════════════════════════════════════

  function escapeHtml(text) {
    if (text == null) return '';
    const d = document.createElement('div');
    d.textContent = String(text);
    return d.innerHTML;
  }

  function sanitizeAlias(name) {
    if (!name) return '';
    let s = name.trim().toLowerCase().replace(/[^a-z0-9_-]+/g, '_').replace(/^_+|_+$/g, '');
    if (s.length > 64) s = s.substring(0, 64);
    if (!/^[a-z0-9]/.test(s)) s = 'ep_' + s;
    return s.slice(0, 64);
  }

  function uniqueAlias(base, existing) {
    const set = new Set(existing);
    let candidate = base;
    let n = 2;
    while (set.has(candidate)) {
      const suf = `_${n++}`;
      candidate = (base.slice(0, Math.max(1, 64 - suf.length)) + suf).slice(0, 64);
    }
    return candidate;
  }

  function evaluatePasswordStrength(p) {
    if (!p || p.length < 8) return 0;
    let s = 1;
    if (p.length >= 12) s++;
    if (/[A-Z]/.test(p) && /[a-z]/.test(p)) s++;
    if (/[0-9]/.test(p) && /[^A-Za-z0-9]/.test(p)) s++;
    return s;
  }

  // ── Toast ────────────────────────────────────────────────────
  function showToast(message, type = 'success', duration = 3500) {
    if (!toastContainer) return;
    const toast = document.createElement('div');
    toast.className = `toast toast-${type}`;
    const iconMap = {
      success: `<svg class="icon toast-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M22 11.08V12a10 10 0 1 1-5.93-9.14"></path><polyline points="22 4 12 14.01 9 11.01"></polyline></svg>`,
      danger:  `<svg class="icon toast-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="12" cy="12" r="10"></circle><line x1="12" y1="8" x2="12" y2="12"></line><line x1="12" y1="16" x2="12.01" y2="16"></line></svg>`,
      warning: `<svg class="icon toast-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M10.29 3.86L1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z"></path><line x1="12" y1="9" x2="12" y2="13"></line><line x1="12" y1="17" x2="12.01" y2="17"></line></svg>`,
      info:    `<svg class="icon toast-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="12" cy="12" r="10"></circle><line x1="12" y1="8" x2="12" y2="12"></line><line x1="12" y1="16" x2="12.01" y2="16"></line></svg>`,
    };
    toast.innerHTML = `${iconMap[type] || iconMap.info}<div class="toast-message">${escapeHtml(message)}</div>`;
    toastContainer.appendChild(toast);
    setTimeout(() => {
      toast.style.opacity = '0';
      toast.style.transform = 'translateX(20px)';
      setTimeout(() => toast.remove(), 250);
    }, duration);
  }

  // ── Button loading ───────────────────────────────────────────
  function setButtonLoading(btn, loading) {
    if (!btn) return;
    const textSpan = btn.querySelector('.btn-text');
    const spinner = btn.querySelector('.btn-spinner');
    btn.disabled = loading;
    if (textSpan) textSpan.classList.toggle('hidden', loading);
    if (spinner) spinner.classList.toggle('hidden', !loading);
  }

  // ── Modal ────────────────────────────────────────────────────
  function openModal(el) {
    if (!el) return;
    el.classList.remove('hidden');
    document.body.style.overflow = 'hidden';
  }

  function closeModal(el) {
    if (!el) return;
    el.classList.add('hidden');
    document.body.style.overflow = '';
  }

  function showConfirmDialog({ title, message, btnText = 'Delete', isDanger = true, onConfirm }) {
    if (modalConfirmTitle) modalConfirmTitle.textContent = title;
    if (modalConfirmMsg) modalConfirmMsg.textContent = message;
    if (btnConfirmOk) {
      const ts = btnConfirmOk.querySelector('.btn-text');
      if (ts) ts.textContent = btnText;
      btnConfirmOk.className = isDanger ? 'btn btn-danger' : 'btn btn-primary';
    }
    confirmCallback = onConfirm;
    openModal(modalConfirm);
  }

  function setupModalListeners() {
    document.querySelectorAll('[data-modal]').forEach(btn => {
      btn.addEventListener('click', () => {
        const id = btn.getAttribute('data-modal');
        const t = document.getElementById(id);
        if (t) closeModal(t);
      });
    });

    document.querySelectorAll('.modal-overlay').forEach(overlay => {
      overlay.addEventListener('click', e => {
        if (e.target === overlay) closeModal(overlay);
      });
    });

    window.addEventListener('keydown', e => {
      if (e.key === 'Escape') {
        document.querySelectorAll('.modal-overlay:not(.hidden)').forEach(m => closeModal(m));
        closeAllDropdowns();
      }
    });

    if (btnConfirmOk) {
      btnConfirmOk.addEventListener('click', async () => {
        if (typeof confirmCallback !== 'function') return;
        setButtonLoading(btnConfirmOk, true);
        try {
          await confirmCallback();
          closeModal(modalConfirm);
        } catch (err) {
          showToast(err.message || 'Action failed', 'danger');
        } finally {
          setButtonLoading(btnConfirmOk, false);
        }
      });
    }
  }

  // ── Dropdown menus ───────────────────────────────────────────
  function closeAllDropdowns() {
    document.querySelectorAll('.platform-card-dropdown.open').forEach(d => d.classList.remove('open'));
    document.querySelectorAll('.platform-card-menu-btn').forEach(b => b.setAttribute('aria-expanded', 'false'));
  }

  function toggleDropdown(btn, menu) {
    const isOpen = menu.classList.contains('open');
    closeAllDropdowns();
    if (!isOpen) {
      menu.classList.add('open');
      btn.setAttribute('aria-expanded', 'true');
    }
  }

  document.addEventListener('click', e => {
    if (!e.target.closest('.platform-card-menu')) closeAllDropdowns();
  });

  // ── View Switching ───────────────────────────────────────────
  function switchView(name) {
    // For auth views: hide shell, show standalone view
    const isAuthView = ['loading', 'setup', 'login', 'invite', 'reset'].includes(name);

    Object.keys(views).forEach(key => {
      const el = views[key];
      if (!el) return;
      if (key === name) {
        el.classList.remove('hidden');
        el.classList.add('active');
      } else {
        el.classList.add('hidden');
        el.classList.remove('active');
      }
    });

    if (isAuthView) {
      if (appShell) appShell.classList.add('hidden');
      if (name === 'invite') {
        const hash = window.location.hash;
        const searchParams = new URLSearchParams(hash.includes('?') ? hash.split('?')[1] : window.location.search);
        const token = searchParams.get('token');
        const tokenInput = document.getElementById('invite-token');
        if (token && tokenInput) tokenInput.value = token;
      }
    } else {
      if (appShell) appShell.classList.remove('hidden');
      document.querySelectorAll('.nav-item[data-nav]').forEach(tab => {
        const isActive = tab.getAttribute('data-nav') === name;
        tab.classList.toggle('active', isActive);
      });
      // Trigger load for the view
      onViewActivate(name);
    }
  }

  async function onViewActivate(name) {
    if (name === 'dashboard') {
      await loadDashboard();
    } else if (name === 'connections') {
      await loadConnections();
    } else if (name === 'sync-sets') {
      await loadSyncSetsPage();
    } else if (name === 'settings') {
      await loadSettings();
    } else if (name === 'users') {
      await loadUsers();
    } else if (name === 'membership') {
      await loadMembership();
    }
  }

  // ── Password Visibility Toggles ──────────────────────────────
  function setupPasswordToggles() {
    document.querySelectorAll('.btn-toggle-password').forEach(btn => {
      btn.addEventListener('click', () => {
        const targetId = btn.getAttribute('data-target');
        const input = document.getElementById(targetId);
        if (!input) return;
        const isPassword = input.type === 'password';
        input.type = isPassword ? 'text' : 'password';
        btn.innerHTML = isPassword
          ? `<svg class="icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M17.94 17.94A10.07 10.07 0 0 1 12 20c-7 0-11-8-11-8a18.45 18.45 0 0 1 5.06-5.94M9.9 4.24A9.12 9.12 0 0 1 12 4c7 0 11 8 11 8a18.5 18.5 0 0 1-2.16 3.19m-6.72-1.07a3 3 0 1 1-4.24-4.24"></path><line x1="1" y1="1" x2="23" y2="23"></line></svg>`
          : `<svg class="icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8z"></path><circle cx="12" cy="12" r="3"></circle></svg>`;
      });
    });
  }

  // ── Form Validation ──────────────────────────────────────────
  function setupFormValidation() {
    function updateStrength(pwdInput, strengthFill, hintEl) {
      if (!pwdInput) return;
      const pwd = pwdInput.value;
      if (!strengthFill || !hintEl) return;
      if (pwd.length === 0) {
        strengthFill.className = 'strength-fill';
        hintEl.className = 'form-hint';
        hintEl.textContent = 'Between 8 and 72 characters';
      } else if (pwd.length < 8) {
        strengthFill.className = 'strength-fill strength-weak';
        hintEl.className = 'form-hint hint-error';
        hintEl.textContent = `Too short (${pwd.length}/8 min)`;
      } else if (pwd.length > 72) {
        strengthFill.className = 'strength-fill strength-weak';
        hintEl.className = 'form-hint hint-error';
        hintEl.textContent = `Too long (${pwd.length}/72 max)`;
      } else {
        const s = evaluatePasswordStrength(pwd);
        strengthFill.className = `strength-fill ${s <= 1 ? 'strength-weak' : s <= 2 ? 'strength-fair' : 'strength-strong'}`;
        hintEl.className = `form-hint ${s >= 3 ? 'hint-success' : ''}`;
        hintEl.textContent = s <= 1 ? 'Weak password' : s <= 2 ? 'Fair password' : 'Strong password';
      }
    }

    function updateConfirm(pwdInput, confirmInput, hintEl) {
      if (!confirmInput || !hintEl) return;
      const c = confirmInput.value;
      if (!c) { hintEl.textContent = ''; hintEl.className = 'form-hint'; return; }
      const match = c === pwdInput.value;
      hintEl.className = `form-hint ${match ? 'hint-success' : 'hint-error'}`;
      hintEl.textContent = match ? 'Passwords match' : 'Passwords do not match';
    }

    if (setupPasswordInput) {
      setupPasswordInput.addEventListener('input', () => updateStrength(setupPasswordInput, setupStrengthFill, setupPasswordHint));
      if (setupConfirmPasswordInput) setupConfirmPasswordInput.addEventListener('input', () => updateConfirm(setupPasswordInput, setupConfirmPasswordInput, setupConfirmHint));
    }
    if (changePwdNew) {
      changePwdNew.addEventListener('input', () => updateStrength(changePwdNew, changePwdStrengthFill, changePwdHint));
      if (changePwdConfirm) changePwdConfirm.addEventListener('input', () => updateConfirm(changePwdNew, changePwdConfirm, changePwdConfirmHint));
    }
  }

  // ══════════════════════════════════════════════════════════════
  // Auth Handlers
  // ══════════════════════════════════════════════════════════════

  async function handleSetupSubmit(e) {
    e.preventDefault();
    setupAlert.classList.add('hidden');
    const username = setupUsernameInput.value.trim();
    const password = setupPasswordInput.value;
    const confirm  = setupConfirmPasswordInput.value;
    if (!username) { setupAlert.textContent = 'Username is required.'; setupAlert.classList.remove('hidden'); return; }
    if (password.length < 8 || password.length > 72) { setupAlert.textContent = 'Password must be 8–72 characters.'; setupAlert.classList.remove('hidden'); return; }
    if (password !== confirm) { setupAlert.textContent = 'Passwords do not match.'; setupAlert.classList.remove('hidden'); return; }
    setButtonLoading(btnSubmitSetup, true);
    try {
      const res = await window.API.setupPassword(username, password);
      isSetup = true;
      isAuthenticated = true;
      currentUser = (res && res.user) ? res.user : { username, role: 'admin' };
      updateSessionDisplay();
      showToast('Administrator account created. Welcome!', 'success');
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
    const username = loginUsernameInput.value.trim();
    const password = loginPasswordInput.value;
    if (!username || !password) { loginAlert.textContent = 'Username and password required.'; loginAlert.classList.remove('hidden'); return; }
    setButtonLoading(btnSubmitLogin, true);
    try {
      const result = await window.API.login(username, password);
      isSetup = true;
      isAuthenticated = true;
      currentUser = result && result.user ? result.user : null;
      updateSessionDisplay();
      showToast('Signed in successfully.', 'success');
      loginPasswordInput.value = '';
      window.Router.navigate('dashboard');
    } catch (err) {
      loginAlert.textContent = err.message || 'Invalid credentials.';
      loginAlert.classList.remove('hidden');
    } finally {
      setButtonLoading(btnSubmitLogin, false);
    }
  }

  async function handleLogout() {
    try { await window.API.logout(); } catch (e) {}
    isAuthenticated = false;
    currentUser = null;
    updateSessionDisplay();
    stopWaPolling();
    stopDeliveryPolling();
    stopQrCountdown();
    cachedDiscordChannels = [];
    cachedDiscordStatus = null;
    cachedTelegramChats = [];
    cachedWaGroups = [];
    showToast('Signed out.', 'success');
    window.Router.navigate('login');
  }

  function updateSessionDisplay() {
    const user = currentUser;
    const username = user ? (user.username || user.Username || '') : '';
    const role = user ? (user.role || user.Role || '') : '';
    if (sessionUsername) sessionUsername.textContent = username || '—';
    if (sessionRole) sessionRole.textContent = role;
    const isAdmin = role === 'admin';
    if (navUsers) navUsers.classList.toggle('hidden', !isAdmin);
    if (navMembership) navMembership.classList.toggle('hidden', !isAdmin);
    document.querySelectorAll('.admin-only').forEach(el => {
      el.classList.toggle('hidden', !isAdmin);
    });
  }

  async function handleInviteSubmit(e) {
    e.preventDefault();
    const token = document.getElementById('invite-token').value.trim();
    const username = document.getElementById('invite-username').value.trim();
    const password = document.getElementById('invite-password').value;
    const alert = document.getElementById('invite-alert');
    alert.classList.add('hidden');
    try {
      const res = await window.API.redeemInvite(token, username, password);
      isSetup = true;
      isAuthenticated = true;
      currentUser = res && res.user ? res.user : null;
      if (!currentUser) {
        try { currentUser = await window.API.getCurrentUser(); } catch (e) {}
      }
      updateSessionDisplay();
      showToast('Account created!', 'success');
      window.Router.navigate('dashboard');
    } catch (err) {
      alert.textContent = err.message || 'Failed to redeem invite.';
      alert.classList.remove('hidden');
    }
  }

  async function handleResetSubmit(e) {
    e.preventDefault();
    const token = document.getElementById('reset-token').value.trim();
    const password = document.getElementById('reset-password').value;
    const alert = document.getElementById('reset-alert');
    alert.classList.add('hidden');
    try {
      await window.API.resetPassword(token, password);
      showToast('Password reset. Please sign in.', 'success');
      window.Router.navigate('login');
    } catch (err) {
      alert.textContent = err.message || 'Reset failed.';
      alert.classList.remove('hidden');
    }
  }

  // ══════════════════════════════════════════════════════════════
  // Dashboard
  // ══════════════════════════════════════════════════════════════

  async function loadDashboard() {
    try {
      const [wa, discord, telegram, endpoints, syncSets] = await Promise.allSettled([
        window.API.getWhatsAppStatus(),
        window.API.getDiscordStatus(),
        window.API.getTelegramStatus(),
        window.API.getEndpoints(),
        window.API.getSyncSets(),
      ]);
      try {
        renderWaStatus(wa.status === 'fulfilled' ? wa.value : null);
      } catch (e) {}
      try {
        renderDiscordStatus(discord.status === 'fulfilled' ? discord.value : null);
      } catch (e) {}
      try {
        renderTelegramStatus(telegram.status === 'fulfilled' ? telegram.value : null);
      } catch (e) {}
      if (endpoints.status === 'fulfilled') cachedEndpoints = Array.isArray(endpoints.value) ? endpoints.value : [];
      if (syncSets.status === 'fulfilled') cachedSyncSets = Array.isArray(syncSets.value) ? syncSets.value : [];
    } catch (e) {}
    await loadDeliveryStatus();
    startDeliveryPolling();
  }

  // ── Platform status renderers ────────────────────────────────
  function setStatusCard(dot, text, desc, state, label, descText) {
    // state: 'connected' | 'warning' | 'error' | 'neutral'
    if (dot) dot.className = `status-dot ${state}`;
    if (text) text.textContent = label;
    if (desc) desc.textContent = descText || '';
  }

  function renderWaStatus(data) {
    if (!data) {
      setStatusCard(waStatusDot, waStatusText, waStatusDesc, 'neutral', 'Unlinked', '');
      return;
    }
    const connected = data.status === 'connected' || (data.isConnected && data.isLoggedIn);
    const pairing   = data.status === 'pairing' || (data.qrCode && data.qrCode.length > 0);
    const loggedIn  = data.isLoggedIn;
    if (connected) {
      setStatusCard(waStatusDot, waStatusText, waStatusDesc, 'connected', 'Connected & Active', '');
    } else if (pairing) {
      setStatusCard(waStatusDot, waStatusText, waStatusDesc, 'warning', 'Pairing In Progress', 'Scan QR code to link.');
    } else if (loggedIn) {
      setStatusCard(waStatusDot, waStatusText, waStatusDesc, 'warning', 'Authenticated (Standby)', '');
    } else {
      setStatusCard(waStatusDot, waStatusText, waStatusDesc, 'neutral', 'Unlinked', '');
    }
  }

  function renderDiscordStatus(data) {
    cachedDiscordStatus = data;
    if (!data || !data.configured || data.status === 'not_configured') {
      setStatusCard(discordDot, discordText, discordDesc, 'neutral', 'Not Configured', '');
    } else if (data.connected) {
      const warning = Array.isArray(data.webhooks) && data.webhooks.some(w => w.status === 'missing_permission');
      setStatusCard(discordDot, discordText, discordDesc, warning ? 'warning' : 'connected',
        warning ? 'Connected · Permission Needed' : 'Connected & Active',
        warning ? 'Grant Manage Webhooks on bridged channels.' : '');
    } else {
      setStatusCard(discordDot, discordText, discordDesc, 'warning', 'Configured · Disconnected', '');
    }
  }

  function renderTelegramStatus(data) {
    cachedTelegramStatus = data;
    if (!data || !data.tokenConfigured || data.status === 'not_configured') {
      setStatusCard(telegramDot, telegramText, telegramDesc, 'neutral', 'Not Configured', '');
    } else if (data.running) {
      setStatusCard(telegramDot, telegramText, telegramDesc, 'connected', 'Long Polling Active', '');
    } else {
      setStatusCard(telegramDot, telegramText, telegramDesc, 'warning', 'Configured · Polling Stopped', '');
    }
  }

  // ── Delivery ─────────────────────────────────────────────────
  const deliveryStateLabel = { healthy: 'Healthy', queued: 'Queued', retrying: 'Retrying', awaiting_replay: 'Awaiting replay', failed: 'Failed', stopped: 'Stopped' };

  async function loadDeliveryStatus() {
    if (!deliveryBody) return;
    try {
      const data = await window.API.getDeliveryStatus();
      const eps = data && Array.isArray(data.endpoints) ? data.endpoints : [];
      deliveryBody.innerHTML = eps.map(item => {
        const ledger = `Q ${item.queued} · R ${item.retrying} · A ${item.awaitingReplay} · F ${item.failed}`;
        const queue  = `${item.queueDepth}/${item.queueCapacity}`;
        const age    = item.oldestActiveAgeSeconds > 0 ? `${item.oldestActiveAgeSeconds}s` : '—';
        const fail   = item.lastFailureClass ? ` · ${escapeHtml(item.lastFailureClass)}` : '';
        const state  = deliveryStateLabel[item.laneState] || 'Unknown';
        const tBadge = item.transport === 'discord' ? 'badge-discord' : item.transport === 'telegram' ? 'badge-telegram' : 'badge-wa';
        // Endpoint create payload uses: { transport: 'telegram' } | { transport: 'discord' } | { transport: 'whatsapp' }
        return `<tr>
          <td><span class="alias-badge">${escapeHtml(item.alias)}</span></td>
          <td><span class="badge ${tBadge}">${escapeHtml(item.transport)}</span></td>
          <td>${escapeHtml(state)}${fail}</td>
          <td>${queue}</td><td>${ledger}</td><td>${age}</td>
          <td>${escapeHtml(item.transportStatus || '—')}</td>
        </tr>`;
      }).join('');
      if (deliveryEmpty) deliveryEmpty.classList.toggle('hidden', eps.length > 0);
      if (deliveryError) deliveryError.classList.add('hidden');
    } catch (err) {
      if (deliveryError) { deliveryError.textContent = 'Unable to load delivery health.'; deliveryError.classList.remove('hidden'); }
    }
  }

  function startDeliveryPolling() {
    if (deliveryPollTimer) return;
    deliveryPollTimer = setInterval(() => {
      if (window.Router.getRoute() === 'dashboard') loadDeliveryStatus();
      else stopDeliveryPolling();
    }, 3000);
  }

  function stopDeliveryPolling() {
    if (deliveryPollTimer) { clearInterval(deliveryPollTimer); deliveryPollTimer = null; }
  }

  // ══════════════════════════════════════════════════════════════
  // WhatsApp Pairing (modal-based, connection-aware)
  // ══════════════════════════════════════════════════════════════

  let activePairingConnId = 'conn-wa-1';

  function stopWaPolling() {
    if (waPollTimer) { clearInterval(waPollTimer); waPollTimer = null; }
  }

  function startWaPolling(connId, ms = 3000) {
    stopWaPolling();
    waPollTimer = setInterval(async () => {
      try {
        const data = await window.API.getConnectionStatus(connId);
        if (connId === 'conn-wa-1') renderWaStatus(data);
        if (data.status === 'connected' || (data.isConnected && data.isLoggedIn)) {
          stopWaPolling();
          stopQrCountdown();
          if (waPairStatus) { waPairStatus.className = 'alert alert-success'; waPairStatus.textContent = 'WhatsApp paired successfully!'; }
          if (waQrSection) waQrSection.classList.add('hidden');
          if (btnWaCancelPair) btnWaCancelPair.classList.add('hidden');
          if (window.Router.getRoute() === 'connections') loadConnections();
        }
      } catch (e) {}
    }, ms);
  }

  function stopQrCountdown() {
    if (waCountdownTimer) { clearInterval(waCountdownTimer); waCountdownTimer = null; }
  }

  function startQrCountdown(seconds) {
    stopQrCountdown();
    qrExpiresAt = Date.now() + seconds * 1000;
    function tick() {
      const left = Math.max(0, Math.ceil((qrExpiresAt - Date.now()) / 1000));
      if (waCountdownText) waCountdownText.textContent = `Expires in ${left}s`;
      if (left <= 0) {
        stopQrCountdown();
        if (waPairStatus) { waPairStatus.className = 'alert alert-warning'; waPairStatus.textContent = 'QR code expired. Close and try again.'; }
      }
    }
    tick();
    waCountdownTimer = setInterval(tick, 1000);
  }

  async function openWaPairModal(connId = 'conn-wa-1') {
    if (typeof connId !== 'string' || !connId) {
      const waConn = cachedConnections.find(c => c.transport === 'whatsapp');
      connId = waConn ? waConn.id : 'conn-wa-1';
    }
    activePairingConnId = connId;

    if (waPairStatus) { waPairStatus.className = 'alert alert-info'; waPairStatus.textContent = `Requesting QR code for ${connId}…`; }
    if (waQrSection) waQrSection.classList.add('hidden');
    if (btnWaCancelPair) btnWaCancelPair.classList.add('hidden');
    openModal(modalWaPair);
    closeAllDropdowns();

    try {
      const res = await window.API.pairWhatsAppConnection(connId);
      if (res.isLoggedIn || res.status === 'connected') {
        if (waPairStatus) { waPairStatus.className = 'alert alert-success'; waPairStatus.textContent = 'WhatsApp is already linked and active.'; }
        if (connId === 'conn-wa-1') renderWaStatus(res);
        if (window.Router.getRoute() === 'connections') loadConnections();
        return;
      }
      if (res.qrCode && window.QRCode && waQrCanvas) {
        window.QRCode.renderCanvas(waQrCanvas, res.qrCode, { size: 240 });
        if (waQrSection) waQrSection.classList.remove('hidden');
        if (waPairStatus) { waPairStatus.className = 'alert alert-info'; waPairStatus.textContent = 'Scan the QR code with your phone\'s WhatsApp.'; }
        if (btnWaCancelPair) btnWaCancelPair.classList.remove('hidden');
        startQrCountdown(res.timeoutSeconds || 30);
        startWaPolling(connId, 2500);
      }
    } catch (err) {
      if (waPairStatus) {
        waPairStatus.className = 'alert alert-danger';
        if (err.status === 409 || (err.message && err.message.includes('already in progress'))) {
          waPairStatus.textContent = 'WhatsApp pairing is already in progress on another connection. Please cancel the active pairing or wait for it to finish.';
        } else {
          waPairStatus.textContent = err.message || 'Failed to request QR code.';
        }
      }
    }
  }

  async function cancelWaPairing() {
    try {
      await window.API.cancelPairWhatsAppConnection(activePairingConnId);
      stopQrCountdown();
      stopWaPolling();
      if (waPairStatus) { waPairStatus.className = 'alert alert-info'; waPairStatus.textContent = 'Pairing cancelled.'; }
      if (waQrSection) waQrSection.classList.add('hidden');
      if (btnWaCancelPair) btnWaCancelPair.classList.add('hidden');
      if (window.Router.getRoute() === 'connections') loadConnections();
    } catch (err) { showToast(err.message || 'Failed to cancel pairing', 'danger'); }
  }

  function handleWaLogout(connId = 'conn-wa-1') {
    if (typeof connId !== 'string' || !connId) {
      const waConn = cachedConnections.find(c => c.transport === 'whatsapp');
      connId = waConn ? waConn.id : 'conn-wa-1';
    }
    closeAllDropdowns();
    showConfirmDialog({
      title: 'Log Out WhatsApp',
      message: `This disconnects and removes the session for WhatsApp connection '${connId}'. You will need to re-scan a QR code to reconnect.`,
      btnText: 'Log Out',
      isDanger: true,
      onConfirm: async () => {
        await window.API.logoutWhatsAppConnection(connId);
        stopQrCountdown();
        stopWaPolling();
        showToast(`WhatsApp connection '${connId}' logged out.`, 'success');
        if (connId === 'conn-wa-1') {
          try { const d = await window.API.getConnectionStatus(connId); renderWaStatus(d); } catch (e) {}
        }
        if (window.Router.getRoute() === 'connections') loadConnections();
      }
    });
  }

  // ══════════════════════════════════════════════════════════════
  // Connections Page & Secret Management
  // ══════════════════════════════════════════════════════════════

  let cachedConnections = [];
  let addConnTransport = 'whatsapp';
  let replaceTokenConnId = '';
  let discoveryConnId = '';
  let discoveredTargets = [];
  let selectedDiscoveredTarget = null;
  let reassigningEndpoint = null;

  async function loadConnections() {
    if (!connectionsContainer) return;
    try {
      const [connsRes, endpointsRes] = await Promise.all([
        window.API.listConnections(),
        window.API.getEndpoints().catch(() => [])
      ]);
      cachedConnections = Array.isArray(connsRes) ? connsRes : [];
      const endpoints = Array.isArray(endpointsRes) ? endpointsRes : [];
      cachedEndpoints = endpoints;

      if (connectionsEmpty) connectionsEmpty.classList.toggle('hidden', cachedConnections.length > 0);
      if (connectionsAlert) connectionsAlert.classList.add('hidden');

      if (cachedConnections.length === 0) {
        connectionsContainer.innerHTML = '';
        return;
      }

      // Query status safely for all connections
      const statuses = {};
      await Promise.all(cachedConnections.map(async (c) => {
        try {
          const s = await window.API.getConnectionStatus(c.id);
          statuses[c.id] = s;
        } catch (e) {
          statuses[c.id] = { status: 'error', error: 'Failed to fetch status' };
        }
      }));

      // Group endpoints by connection
      const connEndpointMap = {};
      endpoints.forEach(ep => {
        const cid = ep.connectionId;
        if (!connEndpointMap[cid]) connEndpointMap[cid] = [];
        connEndpointMap[cid].push(ep);
      });

      const transports = [
        { key: 'whatsapp', label: 'WhatsApp Accounts', icon: '<path d="M22 16.92v3a2 2 0 0 1-2.18 2 19.79 19.79 0 0 1-8.63-3.07 19.5 19.5 0 0 1-6-6 19.79 19.79 0 0 1-3.07-8.67A2 2 0 0 1 4.11 2h3a2 2 0 0 1 2 1.72 12.84 12.84 0 0 0 .7 2.81 2 2 0 0 1-.45 2.11L8.09 9.91a16 16 0 0 0 6 6l1.27-1.27a2 2 0 0 1 2.11-.45 12.84 12.84 0 0 0 2.81.7A2 2 0 0 1 22 16.92z"></path>' },
        { key: 'discord', label: 'Discord Bots', icon: '<path d="M8 9h.01"></path><path d="M16 9h.01"></path><path d="M7 15c2 1 8 1 10 0"></path><path d="M5 5c4-2 10-2 14 0 2 4 3 8 2 12-2 2-4 3-6 3l-1-2h-4l-1 2c-2 0-4-1-6-3-1-4 0-8 2-12z"></path>' },
        { key: 'telegram', label: 'Telegram Bots', icon: '<path d="M22 2L11 13"></path><path d="M22 2L15 22l-4-9-9-4 20-7z"></path>' }
      ];

      const isAdmin = currentUser && currentUser.role === 'admin';

      connectionsContainer.innerHTML = transports.map(t => {
        const matching = cachedConnections.filter(c => c.transport === t.key);
        if (matching.length === 0) return '';

        const cardsHtml = matching.map(conn => {
          const st = statuses[conn.id] || {};
          const connEps = connEndpointMap[conn.id] || [];

          let statusBadgeClass = 'badge-neutral';
          let statusText = 'Stopped';
          let detailHtml = '';
          let nextAction = '';

          if (!conn.enabled) {
            statusBadgeClass = 'badge-neutral';
            statusText = 'Disabled';
            nextAction = 'Enable connection to resume routing.';
            detailHtml = 'Connection is disabled. Ingress and delivery are suspended.';
          } else if (conn.transport === 'whatsapp') {
            const isConn = st.status === 'connected' || (st.isConnected && st.isLoggedIn);
            const isPairing = st.status === 'pairing' || (st.qrCode && st.qrCode.length > 0);
            if (isConn) {
              statusBadgeClass = 'badge-success';
              statusText = 'Connected';
              nextAction = connEps.length === 0 ? 'Discover & add groups as endpoints.' : 'Ready to sync.';
              detailHtml = `Active session (${connEps.length} linked endpoint${connEps.length === 1 ? '' : 's'}).`;
            } else if (isPairing) {
              statusBadgeClass = 'badge-warning';
              statusText = 'Pairing';
              nextAction = 'Scan QR code with WhatsApp.';
              detailHtml = 'Pairing session is waiting for mobile device scan.';
            } else {
              statusBadgeClass = 'badge-neutral';
              statusText = 'Unpaired';
              nextAction = 'Pair device to link account.';
              detailHtml = 'Account not linked. Pairing requires mobile camera scan.';
            }
          } else if (conn.transport === 'discord') {
            if (st.connected) {
              const hasPermIssue = Array.isArray(st.webhooks) && st.webhooks.some(w => w.status === 'missing_permission');
              statusBadgeClass = hasPermIssue ? 'badge-warning' : 'badge-success';
              statusText = hasPermIssue ? 'Permission Needed' : 'Connected';
              nextAction = hasPermIssue ? 'Grant Manage Webhooks permission on bridged Discord channels.' : (connEps.length === 0 ? 'Discover & add channels as endpoints.' : 'Ready to sync.');
              detailHtml = hasPermIssue ? 'Gateway active · Webhook permission degraded.' : `Gateway active (${connEps.length} endpoint${connEps.length === 1 ? '' : 's'}).`;
            } else if (st.configured) {
              statusBadgeClass = 'badge-warning';
              statusText = 'Connecting';
              nextAction = 'Connecting to Discord Gateway…';
              detailHtml = 'Token configured. Gateway connecting.';
            } else {
              statusBadgeClass = 'badge-neutral';
              statusText = 'Not Connected';
              nextAction = 'Set or replace bot token.';
              detailHtml = 'No active connection.';
            }
          } else if (conn.transport === 'telegram') {
            if (st.running) {
              const privacyDisabled = st.privacyModeEnabled === false;
              statusBadgeClass = privacyDisabled ? 'badge-success' : 'badge-warning';
              statusText = 'Running';
              nextAction = !privacyDisabled ? 'Disable Bot Privacy Mode via @BotFather to receive group messages.' : (connEps.length === 0 ? 'Discover observed group chats.' : 'Ready to sync.');
              detailHtml = `Long polling active. Bot Privacy Mode: ${privacyDisabled ? 'Disabled (can read group messages)' : 'Enabled (may miss group messages)'}.`;
            } else {
              statusBadgeClass = 'badge-neutral';
              statusText = 'Stopped';
              nextAction = 'Polling stopped. Check token or enable connection.';
              detailHtml = 'Bot is not running.';
            }
          }

          const epListText = connEps.length > 0
            ? `Endpoints (${connEps.length}): ` + connEps.map(e => escapeHtml(e.alias)).join(', ')
            : 'No endpoints configured under this connection.';

          let actionButtons = `<button class="btn btn-ghost btn-sm" onclick="window.App.openDiscovery('${escapeHtml(conn.id)}')">
            <svg class="icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="11" cy="11" r="8"></circle><line x1="21" y1="21" x2="16.65" y2="16.65"></line></svg>
            Discover
          </button>`;

          if (isAdmin) {
            actionButtons += `<button class="btn btn-ghost btn-sm admin-only" onclick="window.App.toggleConnection('${escapeHtml(conn.id)}', ${!conn.enabled})">
              ${conn.enabled ? 'Disable' : 'Enable'}
            </button>`;

            if (conn.transport === 'whatsapp') {
              actionButtons += `<button class="btn btn-ghost btn-sm admin-only" onclick="window.App.pairWhatsApp('${escapeHtml(conn.id)}')">
                Pair
              </button>`;
              actionButtons += `<button class="btn btn-ghost btn-sm danger admin-only" onclick="window.App.logoutWhatsApp('${escapeHtml(conn.id)}')">
                Logout
              </button>`;
            } else {
              actionButtons += `<button class="btn btn-ghost btn-sm admin-only" onclick="window.App.openReplaceTokenModal('${escapeHtml(conn.id)}', '${escapeHtml(conn.label)}')">
                Replace Token
              </button>`;
            }

            actionButtons += `<button class="btn btn-ghost btn-sm danger admin-only" onclick="window.App.deleteConnection('${escapeHtml(conn.id)}', ${connEps.length})">
              Delete
            </button>`;
          }

          return `<div class="connection-card" data-connection-id="${escapeHtml(conn.id)}">
            <div class="connection-card-top">
              <div class="connection-header-left">
                <div class="platform-card-icon ${conn.transport}">
                  <svg class="icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">${t.icon}</svg>
                </div>
                <div>
                  <div class="connection-title">${escapeHtml(conn.label)}</div>
                  <div class="connection-id">${escapeHtml(conn.id)}</div>
                </div>
              </div>
              <div class="connection-badges">
                <span class="badge ${conn.transport === 'discord' ? 'badge-discord' : conn.transport === 'telegram' ? 'badge-telegram' : 'badge-wa'}">${escapeHtml(conn.transport)}</span>
                <span class="badge ${conn.enabled ? 'badge-primary' : 'badge-neutral'}">${conn.enabled ? 'Enabled' : 'Disabled'}</span>
                <span class="badge ${statusBadgeClass}">${escapeHtml(statusText)}</span>
              </div>
            </div>

            <div class="connection-meta">
              <div>${escapeHtml(detailHtml)}</div>
              <div style="margin-top:4px;color:var(--text-muted);"><strong>Next:</strong> ${escapeHtml(nextAction)}</div>
            </div>

            <div class="connection-endpoints-list">${epListText}</div>

            <div class="connection-actions">
              ${actionButtons}
            </div>
          </div>`;
        }).join('');

        return `<div class="connections-section">
          <div class="connections-section-title">
            <svg class="icon" style="width:16px;height:16px;" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">${t.icon}</svg>
            ${escapeHtml(t.label)} (${matching.length})
          </div>
          <div class="connections-grid">
            ${cardsHtml}
          </div>
        </div>`;
      }).join('');

    } catch (err) {
      if (connectionsAlert) {
        connectionsAlert.textContent = 'Error loading connections: ' + (err.message || err);
        connectionsAlert.className = 'alert alert-danger';
        connectionsAlert.classList.remove('hidden');
      }
    }
  }

  function openAddConnectionModal() {
    if (addConnAlert) addConnAlert.classList.add('hidden');
    if (addConnLabel) addConnLabel.value = '';
    if (addConnId) addConnId.value = '';
    if (addConnToken) addConnToken.value = '';
    selectAddConnTransport('whatsapp');
    openModal(modalAddConnection);
  }

  function selectAddConnTransport(transport) {
    addConnTransport = transport;
    const tabs = document.querySelectorAll('#add-conn-transport-tabs .transport-tab');
    tabs.forEach(t => {
      const tp = t.getAttribute('data-transport');
      t.classList.toggle('active', tp === transport);
      t.classList.toggle(tp, tp === transport);
    });
    if (addConnTokenGroup) {
      addConnTokenGroup.classList.toggle('hidden', transport === 'whatsapp');
    }
    if (addConnToken) {
      addConnToken.required = transport !== 'whatsapp';
      addConnToken.value = '';
    }
  }

  async function handleAddConnectionSubmit(e) {
    e.preventDefault();
    const label = addConnLabel ? addConnLabel.value.trim() : '';
    const id = addConnId ? addConnId.value.trim() : '';
    const token = addConnToken ? addConnToken.value.trim() : '';

    // CRITICAL: Immediately clear token input from memory and DOM
    if (addConnToken) addConnToken.value = '';

    if (!label) {
      if (addConnAlert) { addConnAlert.textContent = 'Connection label is required.'; addConnAlert.classList.remove('hidden'); }
      return;
    }
    if (addConnTransport !== 'whatsapp' && !token) {
      if (addConnAlert) { addConnAlert.textContent = 'Bot token is required.'; addConnAlert.classList.remove('hidden'); }
      return;
    }

    setButtonLoading(btnSubmitAddConn, true);
    try {
      const payload = {
        transport: addConnTransport,
        label,
        enabled: true
      };
      if (id) payload.id = id;
      if (addConnTransport !== 'whatsapp') payload.token = token;

      await window.API.createConnection(payload);
      showToast(`Connection '${label}' created!`, 'success');
      closeModal(modalAddConnection);
      await loadConnections();
    } catch (err) {
      if (addConnAlert) {
        addConnAlert.textContent = err.message || 'Failed to create connection';
        addConnAlert.classList.remove('hidden');
      }
    } finally {
      setButtonLoading(btnSubmitAddConn, false);
      if (addConnToken) addConnToken.value = '';
    }
  }

  function openReplaceTokenModal(connId, label) {
    replaceTokenConnId = connId;
    if (replaceTokenAlert) replaceTokenAlert.classList.add('hidden');
    if (replaceTokenConnInfo) replaceTokenConnInfo.textContent = `Connection: ${label} (${connId})`;
    if (replaceTokenInput) replaceTokenInput.value = '';
    openModal(modalReplaceToken);
  }

  async function handleReplaceTokenSubmit(e) {
    e.preventDefault();
    const token = replaceTokenInput ? replaceTokenInput.value.trim() : '';
    if (replaceTokenInput) replaceTokenInput.value = '';

    if (!token || !replaceTokenConnId) {
      if (replaceTokenAlert) { replaceTokenAlert.textContent = 'New bot token is required.'; replaceTokenAlert.classList.remove('hidden'); }
      return;
    }

    setButtonLoading(btnSubmitReplaceToken, true);
    try {
      await window.API.updateConnection(replaceTokenConnId, { token });
      showToast('Bot token updated!', 'success');
      closeModal(modalReplaceToken);
      await loadConnections();
    } catch (err) {
      if (replaceTokenAlert) {
        replaceTokenAlert.textContent = err.message || 'Failed to update token';
        replaceTokenAlert.classList.remove('hidden');
      }
    } finally {
      setButtonLoading(btnSubmitReplaceToken, false);
      if (replaceTokenInput) replaceTokenInput.value = '';
    }
  }

  async function toggleConnection(connId, enabled) {
    try {
      await window.API.updateConnection(connId, { enabled });
      showToast(`Connection ${enabled ? 'enabled' : 'disabled'}.`, 'success');
      await loadConnections();
    } catch (err) {
      showToast(err.message || 'Failed to update connection', 'danger');
    }
  }

  async function deleteConnection(connId, epCount) {
    if (epCount > 0) {
      showToast(`Cannot delete connection '${connId}' because ${epCount} endpoint(s) reference it. Delete or reassign referencing endpoints first.`, 'danger');
      return;
    }

    showConfirmDialog({
      title: 'Delete Connection',
      message: `Are you sure you want to delete connection '${connId}'? All protocol stores and configuration will be permanently removed.`,
      btnText: 'Delete Connection',
      isDanger: true,
      onConfirm: async () => {
        try {
          await window.API.deleteConnection(connId);
          showToast(`Connection '${connId}' deleted.`, 'success');
          await loadConnections();
        } catch (err) {
          if (err.status === 409 || (err.message && err.message.includes('reference'))) {
            showToast('Cannot delete connection: referenced by existing endpoints.', 'danger');
          } else {
            showToast(err.message || 'Failed to delete connection', 'danger');
          }
        }
      }
    });
  }

  async function openDiscovery(connId) {
    discoveryConnId = connId;
    selectedDiscoveredTarget = null;
    const conn = cachedConnections.find(c => c.id === connId);
    const connLabel = conn ? `${conn.label} (${conn.id})` : connId;

    if (discoveryConnBadge) discoveryConnBadge.textContent = connLabel;
    if (discoveryAlert) discoveryAlert.classList.add('hidden');
    if (discoveryLoading) discoveryLoading.classList.remove('hidden');
    if (discoveryEmpty) discoveryEmpty.classList.add('hidden');
    if (discoveryItems) discoveryItems.classList.add('hidden');
    if (discoveryCreateForm) discoveryCreateForm.classList.add('hidden');

    openModal(modalDiscovery);

    try {
      const targets = await window.API.getConnectionDiscovery(connId);
      if (discoveryLoading) discoveryLoading.classList.add('hidden');
      discoveredTargets = Array.isArray(targets) ? targets : [];

      if (discoveredTargets.length === 0) {
        if (discoveryEmpty) {
          discoveryEmpty.classList.remove('hidden');
          if (conn && conn.transport === 'whatsapp') {
            if (discoveryEmptyTitle) discoveryEmptyTitle.textContent = 'No Joined WhatsApp Groups';
            if (discoveryEmptyDesc) discoveryEmptyDesc.textContent = 'Ensure WhatsApp account is connected and has joined group conversations. Communities are excluded.';
          } else if (conn && conn.transport === 'discord') {
            if (discoveryEmptyTitle) discoveryEmptyTitle.textContent = 'No Discord Channels Found';
            if (discoveryEmptyDesc) discoveryEmptyDesc.textContent = 'Ensure the bot has View Channel permissions in your Discord server.';
          } else {
            if (discoveryEmptyTitle) discoveryEmptyTitle.textContent = 'No Observed Telegram Chats';
            if (discoveryEmptyDesc) discoveryEmptyDesc.textContent = 'Send a message in a group where the bot is a member (with Bot Privacy Mode disabled), then refresh.';
          }
        }
        return;
      }

      if (discoveryItems) discoveryItems.classList.remove('hidden');
      if (discoveryTbody) {
        discoveryTbody.innerHTML = discoveredTargets.map((item, index) => {
          let name = '';
          let remoteId = '';
          if (conn && conn.transport === 'whatsapp') {
            name = item.name || item.jid || '';
            remoteId = item.jid;
          } else if (conn && conn.transport === 'discord') {
            name = '#' + (item.channelName || item.channelId) + (item.guildName ? ' [' + item.guildName + ']' : '');
            remoteId = item.channelId;
          } else if (conn && conn.transport === 'telegram') {
            name = (item.title || item.chatId) + (item.type ? ' (' + item.type + ')' : '');
            remoteId = item.chatId;
          }

          const existingEp = cachedEndpoints.find(e => e.remoteId === remoteId);
          const statusText = existingEp ? `<span class="badge badge-neutral">In use (${escapeHtml(existingEp.alias)})</span>` : '';
          const actionBtn = existingEp
            ? `<button class="btn btn-ghost btn-sm" disabled>Added</button>`
            : `<button class="btn btn-primary btn-sm" onclick="window.App.selectDiscoveredTarget(${index})">Add Endpoint</button>`;

          return `<tr>
            <td><strong>${escapeHtml(name)}</strong></td>
            <td><code class="font-mono" style="font-size:.75rem;">${escapeHtml(remoteId)}</code></td>
            <td style="text-align:right;">${statusText} ${actionBtn}</td>
          </tr>`;
        }).join('');
      }

    } catch (err) {
      if (discoveryLoading) discoveryLoading.classList.add('hidden');
      if (discoveryAlert) {
        discoveryAlert.textContent = 'Discovery failed: ' + (err.message || err);
        discoveryAlert.className = 'alert alert-danger';
        discoveryAlert.classList.remove('hidden');
      }
    }
  }

  function selectDiscoveredTarget(index) {
    const target = discoveredTargets[index];
    if (!target) return;
    selectedDiscoveredTarget = target;
    const conn = cachedConnections.find(c => c.id === discoveryConnId);
    if (!conn) return;

    let rawName = '';
    if (conn.transport === 'whatsapp') rawName = target.name || '';
    else if (conn.transport === 'discord') rawName = target.channelName || '';
    else rawName = target.title || '';

    const baseAlias = sanitizeAlias(rawName || 'endpoint');
    const existingAliases = cachedEndpoints.map(e => e.alias);
    const suggested = uniqueAlias(baseAlias, existingAliases);

    if (discoveryAliasInput) discoveryAliasInput.value = suggested;
    if (discoverySyncsetSelect) {
      discoverySyncsetSelect.innerHTML = '<option value="">— none (create unassigned) —</option>' +
        cachedSyncSets.map(s => `<option value="${escapeHtml(s.id)}">${escapeHtml(s.id)}</option>`).join('');
    }
    if (discoveryCreateForm) discoveryCreateForm.classList.remove('hidden');
  }

  async function submitDiscoveryCreate() {
    const alias = discoveryAliasInput ? discoveryAliasInput.value.trim() : '';
    const syncSetId = discoverySyncsetSelect ? discoverySyncsetSelect.value.trim() : '';
    const conn = cachedConnections.find(c => c.id === discoveryConnId);

    if (!alias || !conn || !selectedDiscoveredTarget) {
      if (discoveryAlert) { discoveryAlert.textContent = 'Alias is required.'; discoveryAlert.className = 'alert alert-danger'; discoveryAlert.classList.remove('hidden'); }
      return;
    }

    let remoteId = '';
    if (conn.transport === 'whatsapp') remoteId = selectedDiscoveredTarget.jid;
    else if (conn.transport === 'discord') remoteId = selectedDiscoveredTarget.channelId;
    else remoteId = selectedDiscoveredTarget.chatId;

    setButtonLoading(btnDiscoverySubmitCreate, true);
    try {
      await window.API.createEndpoint({
        alias,
        transport: conn.transport,
        connectionId: conn.id,
        remoteId,
        syncSetId: syncSetId || undefined
      });
      showToast(`Endpoint '${alias}' created!`, 'success');
      closeModal(modalDiscovery);
      await loadConnections();
    } catch (err) {
      if (discoveryAlert) {
        discoveryAlert.textContent = err.message || 'Failed to create endpoint';
        discoveryAlert.className = 'alert alert-danger';
        discoveryAlert.classList.remove('hidden');
      }
    } finally {
      setButtonLoading(btnDiscoverySubmitCreate, false);
    }
  }

  function openReassignModal(alias) {
    const ep = cachedEndpoints.find(e => e.alias === alias);
    if (!ep) return;
    reassigningEndpoint = ep;

    if (reassignAlert) reassignAlert.classList.add('hidden');
    if (reassignEndpointInfo) reassignEndpointInfo.textContent = `Endpoint: ${ep.alias} (Transport: ${ep.transport}, Current Connection: ${ep.connectionId})`;

    const compatible = cachedConnections.filter(c => c.transport === ep.transport && c.enabled);
    if (reassignConnSelect) {
      reassignConnSelect.innerHTML = compatible.map(c => {
        const isCurrent = c.id === ep.connectionId;
        return `<option value="${escapeHtml(c.id)}" ${isCurrent ? 'selected' : ''}>${escapeHtml(c.label)} (${escapeHtml(c.id)})${isCurrent ? ' [current]' : ''}</option>`;
      }).join('');
    }

    openModal(modalReassignEndpoint);
  }

  async function submitReassign(e) {
    if (e) e.preventDefault();
    if (!reassigningEndpoint) return;

    const newConnId = reassignConnSelect ? reassignConnSelect.value : '';
    if (!newConnId) return;

    if (newConnId === reassigningEndpoint.connectionId) {
      closeModal(modalReassignEndpoint);
      return;
    }

    setButtonLoading(btnSubmitReassign, true);
    try {
      await window.API.updateEndpoint(reassigningEndpoint.alias, {
        transport: reassigningEndpoint.transport,
        connectionId: newConnId,
        remoteId: reassigningEndpoint.remoteId,
        syncSetId: reassigningEndpoint.syncSetId || undefined
      });
      showToast(`Endpoint '${reassigningEndpoint.alias}' reassigned to '${newConnId}'!`, 'success');
      closeModal(modalReassignEndpoint);
      await loadSyncSetsPage();
      await loadConnections();
    } catch (err) {
      if (reassignAlert) {
        reassignAlert.textContent = err.message || 'Failed to reassign endpoint';
        reassignAlert.classList.remove('hidden');
      }
    } finally {
      setButtonLoading(btnSubmitReassign, false);
    }
  }

  // ══════════════════════════════════════════════════════════════
  // Sync Sets Page
  // ══════════════════════════════════════════════════════════════

  async function loadSyncSetsPage() {
    try {
      const [syncSetsRes, endpointsRes] = await Promise.all([
        window.API.getSyncSets(),
        window.API.getEndpoints()
      ]);
      cachedSyncSets   = Array.isArray(syncSetsRes)  ? syncSetsRes  : [];
      cachedEndpoints  = Array.isArray(endpointsRes) ? endpointsRes : [];
    } catch (err) {
      showToast(err.message || 'Failed to load sync sets', 'danger');
    }
    renderSyncSetList();
    if (cachedSyncSets.length > 0) {
      const targetId = (editingSyncSetId && cachedSyncSets.some(s => s.id === editingSyncSetId))
        ? editingSyncSetId
        : cachedSyncSets[0].id;
      openSyncSetEditor(targetId);
    } else {
      showEditorPlaceholder();
    }
  }

  function renderSyncSetList() {
    if (!syncsetList) return;
    if (syncsetCountBadge) syncsetCountBadge.textContent = cachedSyncSets.length;

    if (cachedSyncSets.length === 0) {
      syncsetList.innerHTML = `<div class="syncset-empty-list">No sync sets yet. Click <strong>New</strong> to create one.</div>`;
      return;
    }

    syncsetList.innerHTML = cachedSyncSets.map(set => {
      const count = Array.isArray(set.endpoints) ? set.endpoints.length : 0;
      const isActive = set.id === editingSyncSetId;
      return `<div class="syncset-list-item ${isActive ? 'active' : ''}" data-id="${escapeHtml(set.id)}">
        <span class="syncset-item-id">${escapeHtml(set.id)}</span>
        <span class="syncset-item-count">${count} ep</span>
        <button class="syncset-delete-btn" data-id="${escapeHtml(set.id)}" title="Delete">
          <svg class="icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><polyline points="3 6 5 6 21 6"></polyline><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"></path></svg>
        </button>
      </div>`;
    }).join('');

    syncsetList.querySelectorAll('.syncset-list-item').forEach(item => {
      item.addEventListener('click', e => {
        if (e.target.closest('.syncset-delete-btn')) return;
        openSyncSetEditor(item.getAttribute('data-id'));
      });
    });
    syncsetList.querySelectorAll('.syncset-delete-btn').forEach(btn => {
      btn.addEventListener('click', e => {
        e.stopPropagation();
        handleDeleteSyncSet(btn.getAttribute('data-id'));
      });
    });
  }

  function showEditorPlaceholder() {
    if (syncsetEditorPlaceholder) syncsetEditorPlaceholder.style.display = '';
    if (syncsetEditorForm) syncsetEditorForm.classList.add('hidden');
  }

  function showEditorForm() {
    if (syncsetEditorPlaceholder) syncsetEditorPlaceholder.style.display = 'none';
    if (syncsetEditorForm) { syncsetEditorForm.classList.remove('hidden'); syncsetEditorForm.style.display = 'flex'; }
  }

  function openSyncSetEditor(id) {
    const set = id ? cachedSyncSets.find(s => s.id === id) : null;
    editingSyncSetId = set ? set.id : null;

    if (syncsetEditorTitle) syncsetEditorTitle.textContent = set ? `Edit: ${set.id}` : 'New Sync Set';
    if (syncsetEditorIdDisplay) syncsetEditorIdDisplay.textContent = set ? set.id : '(new)';

    // ID field: only editable on create
    if (inputSyncsetId) { inputSyncsetId.value = set ? set.id : ''; inputSyncsetId.disabled = !!set; }
    if (syncsetIdField) syncsetIdField.style.display = set ? 'none' : '';

    if (btnDeleteSyncSet) btnDeleteSyncSet.classList.toggle('hidden', !set);

    // Populate editor endpoints from existing set
    editorEndpoints = [];
    if (set && Array.isArray(set.endpoints)) {
      set.endpoints.forEach(alias => {
        const ep = cachedEndpoints.find(e => e.alias === alias);
        if (ep) editorEndpoints.push({ alias: ep.alias, transport: ep.transport, connectionId: ep.connectionId, remoteId: ep.remoteId });
      });
    }
    renderChips();

    // Reset add-endpoint form
    resetAddEndpointForm();

    // Update list selection highlight
    renderSyncSetList();
    showEditorForm();
  }

  function handleDeleteSyncSet(id) {
    showConfirmDialog({
      title: `Delete Sync Set: ${id}`,
      message: `Delete sync set '${id}'? Member endpoints will be unlinked but not deleted.`,
      btnText: 'Delete Sync Set',
      isDanger: true,
      onConfirm: async () => {
        await window.API.deleteSyncSet(id);
        showToast(`Sync set '${id}' deleted.`, 'success');
        if (editingSyncSetId === id) { editingSyncSetId = null; showEditorPlaceholder(); }
        await loadSyncSetsPage();
      }
    });
  }

  // ── Endpoint chips renderer ──────────────────────────────────
  function transportClass(t) {
    return t === 'discord' ? 'discord' : t === 'telegram' ? 'telegram' : 'wa';
  }

  function transportBadgeClass(t) {
    return t === 'discord' ? 'badge-discord' : t === 'telegram' ? 'badge-telegram' : 'badge-wa';
  }

  let chipRenameIdx = null;

  function renderChips() {
    if (!endpointChips) return;
    if (editorEndpoints.length === 0) {
      endpointChips.innerHTML = '<span class="chips-placeholder">No endpoints yet. Add one below.</span>';
      return;
    }
    endpointChips.innerHTML = editorEndpoints.map((ep, i) => {
      if (chipRenameIdx === i) {
        return `
          <span class="endpoint-chip ${transportClass(ep.transport)}" data-idx="${i}">
            <span class="badge ${transportBadgeClass(ep.transport)}" style="padding:1px 5px;font-size:.68rem;">${escapeHtml(ep.transport)}</span>
            <span class="chip-rename-box">
              <input type="text" class="chip-rename-input font-mono" value="${escapeHtml(ep.alias)}" data-idx="${i}" maxlength="64">
              <button type="button" class="chip-rename-btn chip-rename-save" data-idx="${i}" title="Save alias">
                <svg class="icon icon-xs" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5"><polyline points="20 6 9 17 4 12"></polyline></svg>
              </button>
              <button type="button" class="chip-rename-btn chip-rename-cancel" data-idx="${i}" title="Cancel">
                <svg class="icon icon-xs" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><line x1="18" y1="6" x2="6" y2="18"></line><line x1="6" y1="6" x2="18" y2="18"></line></svg>
              </button>
            </span>
          </span>
        `;
      }
      const epConn = cachedConnections.find(c => c.id === ep.connectionId);
      const connLabel = epConn ? epConn.label : (ep.connectionId || '');
      const connBadge = connLabel ? `<span style="font-size:.7rem;color:var(--text-muted);margin:0 4px;">(${escapeHtml(connLabel)})</span>` : '';

      return `
        <span class="endpoint-chip ${transportClass(ep.transport)}" data-idx="${i}">
          <span class="badge ${transportBadgeClass(ep.transport)}" style="padding:1px 5px;font-size:.68rem;">${escapeHtml(ep.transport)}</span>
          <span class="chip-alias">${escapeHtml(ep.alias)}</span>
          ${connBadge}
          <button type="button" class="chip-reassign" data-alias="${escapeHtml(ep.alias)}" title="Reassign connection">
            <svg class="icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><polyline points="16 3 21 3 21 8"></polyline><line x1="4" y1="20" x2="21" y2="3"></line><polyline points="21 16 21 21 16 21"></polyline><line x1="15" y1="15" x2="21" y2="21"></line><line x1="4" y1="4" x2="9" y2="9"></line></svg>
          </button>
          <button type="button" class="chip-edit" data-idx="${i}" title="Rename alias">
            <svg class="icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M11 4H4a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7"></path><path d="M18.5 2.5a2.121 2.121 0 0 1 3 3L12 15l-4 1 1-4 9.5-9.5z"></path></svg>
          </button>
          <button type="button" class="chip-remove" data-idx="${i}" title="Remove endpoint">
            <svg class="icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><line x1="18" y1="6" x2="6" y2="18"></line><line x1="6" y1="6" x2="18" y2="18"></line></svg>
          </button>
        </span>
      `;
    }).join('');

    // Attach chip-reassign listeners
    endpointChips.querySelectorAll('.chip-reassign').forEach(btn => {
      btn.addEventListener('click', (e) => {
        e.stopPropagation();
        openReassignModal(btn.getAttribute('data-alias'));
      });
    });

    // Attach chip-edit listeners
    endpointChips.querySelectorAll('.chip-edit').forEach(btn => {
      btn.addEventListener('click', (e) => {
        e.stopPropagation();
        chipRenameIdx = parseInt(btn.getAttribute('data-idx'), 10);
        renderChips();
        const input = endpointChips.querySelector('.chip-rename-input');
        if (input) { input.focus(); input.select(); }
      });
    });

    // Attach chip-rename-cancel listeners
    endpointChips.querySelectorAll('.chip-rename-cancel').forEach(btn => {
      btn.addEventListener('click', (e) => {
        e.stopPropagation();
        chipRenameIdx = null;
        renderChips();
      });
    });

    // Function to execute rename
    const saveRename = async (idx) => {
      const ep = editorEndpoints[idx];
      if (!ep) return;
      const input = endpointChips.querySelector(`.chip-rename-input[data-idx="${idx}"]`);
      if (!input) return;
      const newAlias = input.value.trim();
      if (!newAlias || newAlias === ep.alias) {
        chipRenameIdx = null;
        renderChips();
        return;
      }
      if (!ALIAS_REGEX.test(newAlias)) {
        showToast('Alias must start with alphanumeric and be 1–64 chars (letters, numbers, - _).', 'warning');
        input.focus();
        return;
      }
      const allAliases = cachedEndpoints.map(e => e.alias).concat(editorEndpoints.map(e => e.alias));
      if (allAliases.filter(a => a !== ep.alias).includes(newAlias)) {
        showToast(`Alias '${newAlias}' is already in use.`, 'warning');
        input.focus();
        return;
      }
      const existingInDb = cachedEndpoints.find(e => e.alias === ep.alias);
      if (existingInDb) {
        try {
          await window.API.updateEndpoint(ep.alias, {
            alias: newAlias,
            transport: ep.transport,
            connectionId: ep.connectionId,
            remoteId: ep.remoteId,
            syncSetId: existingInDb.syncSetId || editingSyncSetId || null
          });
          const fresh = await window.API.getEndpoints().catch(() => []);
          cachedEndpoints = Array.isArray(fresh) ? fresh : cachedEndpoints;
        } catch (err) {
          showToast(err.message || 'Failed to rename alias', 'danger');
          return;
        }
      }
      ep.alias = newAlias;
      chipRenameIdx = null;
      renderChips();
      showToast(`Endpoint alias renamed to '${newAlias}'.`, 'success');
      if (editingSyncSetId) {
        const aliases = editorEndpoints.map(e => e.alias);
        window.API.updateSyncSet(editingSyncSetId, { endpoints: aliases }).catch(() => {});
        const set = cachedSyncSets.find(s => s.id === editingSyncSetId);
        if (set) set.endpoints = aliases;
        renderSyncSetList();
      }
    };

    endpointChips.querySelectorAll('.chip-rename-save').forEach(btn => {
      btn.addEventListener('click', (e) => {
        e.stopPropagation();
        const idx = parseInt(btn.getAttribute('data-idx'), 10);
        saveRename(idx);
      });
    });

    endpointChips.querySelectorAll('.chip-rename-input').forEach(input => {
      input.addEventListener('keydown', (e) => {
        if (e.key === 'Enter') {
          e.preventDefault();
          const idx = parseInt(input.getAttribute('data-idx'), 10);
          saveRename(idx);
        } else if (e.key === 'Escape') {
          chipRenameIdx = null;
          renderChips();
        }
      });
    });

    endpointChips.querySelectorAll('.chip-remove').forEach(btn => {
      btn.addEventListener('click', async (e) => {
        e.stopPropagation();
        const idx = parseInt(btn.getAttribute('data-idx'), 10);
        const ep = editorEndpoints[idx];
        if (!ep) return;

        // If in DB, delete the endpoint so it is fully unlinked and available
        if (cachedEndpoints.some(e => e.alias === ep.alias)) {
          try {
            await window.API.deleteEndpoint(ep.alias);
            const fresh = await window.API.getEndpoints().catch(() => []);
            cachedEndpoints = Array.isArray(fresh) ? fresh : [];
          } catch (err) {
            showToast(err.message || 'Failed to remove endpoint', 'danger');
            return;
          }
        }

        editorEndpoints.splice(idx, 1);
        if (chipRenameIdx === idx) chipRenameIdx = null;
        renderChips();
        await loadRemoteOptions(activeTransport);
        showToast(`Endpoint '${ep.alias}' removed.`, 'success');
      });
    });
  }

  // ── Add Endpoint Form ─────────────────────────────────────────
  async function resetAddEndpointForm() {
    activeTransport = 'whatsapp';
    transportTabs.forEach(t => {
      const tp = t.getAttribute('data-transport');
      t.classList.toggle('active', tp === 'whatsapp');
      t.classList.toggle('wa', tp === 'whatsapp');
      t.classList.toggle('discord', false);
      t.classList.toggle('telegram', false);
    });
    // Re-apply class correctly
    transportTabs.forEach(t => { const tp = t.getAttribute('data-transport'); if (t.classList.contains('active')) t.classList.add(tp); });
    if (addAliasInput) addAliasInput.value = '';
    if (addEndpointAlert) addEndpointAlert.classList.add('hidden');
    await loadRemoteOptions('whatsapp');
  }

  function suggestedTelegramAlias() {
    const base = 'telegram_group';
    let candidate = base;
    let n = 2;
    const aliases = new Set(cachedEndpoints.map(e => e.alias).concat(editorEndpoints.map(e => e.alias)));
    while (aliases.has(candidate)) candidate = `${base}_${n++}`;
    return candidate;
  }

  async function loadRemoteOptions(transport) {
    if (!addRemoteSelect) return;
    addRemoteSelect.innerHTML = '<option value="">Loading…</option>';
    addRemoteSelect.disabled = true;
    if (platformSetupNote) platformSetupNote.classList.add('hidden');

    const enabledConns = cachedConnections.filter(c => c.transport === transport && c.enabled);
    if (addEndpointConnSelect) {
      addEndpointConnSelect.innerHTML = enabledConns.map(c => `<option value="${escapeHtml(c.id)}">${escapeHtml(c.label)} (${escapeHtml(c.id)})</option>`).join('');
    }
    if (addEndpointNoConn) {
      addEndpointNoConn.classList.toggle('hidden', enabledConns.length > 0);
      if (enabledConns.length === 0) {
        addEndpointNoConn.textContent = `No enabled ${transport} connection configured. Go to Connections to add or enable one.`;
        addRemoteSelect.innerHTML = '<option value="">No connection configured</option>';
        addRemoteSelect.disabled = true;
        return;
      }
    }

    const selectedConnId = addEndpointConnSelect && addEndpointConnSelect.value ? addEndpointConnSelect.value : (enabledConns[0] ? enabledConns[0].id : '');
    if (!selectedConnId) {
      addRemoteSelect.innerHTML = '<option value="">Select a connection first</option>';
      addRemoteSelect.disabled = true;
      return;
    }

    try {
      if (transport === 'whatsapp') {
        if (addRemoteLabel) addRemoteLabel.textContent = 'WhatsApp Group';
        if (addRemoteHint) addRemoteHint.textContent = '';
        const groups = await window.API.getConnectionDiscovery(selectedConnId).catch(() => []);
        cachedWaGroups = Array.isArray(groups) ? groups : [];
        const filtered = cachedWaGroups.filter(g => GROUP_JID_REGEX.test(g.jid));
        if (filtered.length === 0) {
          addRemoteSelect.innerHTML = '<option value="">No joined groups found</option>';
          if (platformSetupNote) {
            platformSetupNote.classList.remove('hidden');
            platformSetupNoteText.textContent = 'No WhatsApp groups found for this account. Ensure WhatsApp is paired and has joined group conversations. Communities are excluded.';
          }
        } else {
          addRemoteSelect.innerHTML = '<option value="">— select group —</option>' +
            filtered.map(g => {
              const inThisSet = editorEndpoints.some(e => e.transport === 'whatsapp' && e.remoteId === g.jid);
              const ep = cachedEndpoints.find(e => e.transport === 'whatsapp' && e.remoteId === g.jid);
              const inOtherSet = ep && ep.syncSetId && ep.syncSetId !== editingSyncSetId;
              const disabled = inThisSet || inOtherSet;
              let suffix = '';
              if (inThisSet) {
                suffix = ' (already in set)';
              } else if (inOtherSet) {
                suffix = ` (in '${ep.syncSetId}')`;
              } else if (ep) {
                suffix = ` (alias: ${ep.alias})`;
              }
              const label = (g.name || g.jid) + suffix;
              const currentAlias = ep ? ep.alias : '';
              return `<option value="${escapeHtml(g.jid)}" data-name="${escapeHtml(g.name || '')}" data-existing-alias="${escapeHtml(currentAlias)}" ${disabled ? 'disabled' : ''}>${escapeHtml(label)}</option>`;
            }).join('');
        }
      } else if (transport === 'discord') {
        if (addRemoteLabel) addRemoteLabel.textContent = 'Discord Channel';
        if (addRemoteHint) addRemoteHint.textContent = '';
        const channels = await window.API.getConnectionDiscovery(selectedConnId).catch(() => null);
        if (!channels) {
          addRemoteSelect.innerHTML = '<option value="">Discord not connected</option>';
          if (platformSetupNote) {
            platformSetupNote.classList.remove('hidden');
            platformSetupNoteText.textContent = 'Discord connection is not active. Check bot token in Connections.';
          }
        } else {
          cachedDiscordChannels = Array.isArray(channels) ? channels : [];
          addRemoteSelect.innerHTML = '<option value="">— select channel —</option>' +
            cachedDiscordChannels.map(c => {
              const inThisSet = editorEndpoints.some(e => e.transport === 'discord' && e.remoteId === c.channelId);
              const ep = cachedEndpoints.find(e => e.transport === 'discord' && e.remoteId === c.channelId);
              const inOtherSet = ep && ep.syncSetId && ep.syncSetId !== editingSyncSetId;
              const disabled = inThisSet || inOtherSet;
              const guild = c.guildName || c.guildId || '';
              const ch = c.channelName || c.channelId;
              let suffix = '';
              if (inThisSet) {
                suffix = ' (already in set)';
              } else if (inOtherSet) {
                suffix = ` (in '${ep.syncSetId}')`;
              } else if (ep) {
                suffix = ` (alias: ${ep.alias})`;
              }
              const label = `#${ch}${guild ? ' [' + guild + ']' : ''}${suffix}`;
              const currentAlias = ep ? ep.alias : '';
              return `<option value="${escapeHtml(c.channelId)}" data-name="${escapeHtml(ch)}" data-guild="${escapeHtml(guild)}" data-existing-alias="${escapeHtml(currentAlias)}" ${disabled ? 'disabled' : ''}>${escapeHtml(label)}</option>`;
            }).join('');
          if (cachedDiscordChannels.length === 0) {
            if (platformSetupNote) {
              platformSetupNote.classList.remove('hidden');
              platformSetupNoteText.textContent = 'No Discord channels found. Ensure the bot has View Channel permission.';
            }
          }
        }
      } else if (transport === 'telegram') {
        if (addRemoteLabel) addRemoteLabel.textContent = 'Telegram Group/Supergroup';
        if (addRemoteHint) addRemoteHint.textContent = '';
        const chats = await window.API.getConnectionDiscovery(selectedConnId).catch(() => null);
        if (!chats) {
          addRemoteSelect.innerHTML = '<option value="">Telegram not connected</option>';
          if (platformSetupNote) {
            platformSetupNote.classList.remove('hidden');
            platformSetupNoteText.textContent = 'Telegram connection is not active. Check bot token in Connections.';
          }
        } else {
          cachedTelegramChats = Array.isArray(chats) ? chats : [];
          addRemoteSelect.innerHTML = '<option value="">— select group —</option>' +
            cachedTelegramChats.map(c => {
              const inThisSet = editorEndpoints.some(e => e.transport === 'telegram' && e.remoteId === c.chatId);
              const ep = cachedEndpoints.find(e => e.transport === 'telegram' && e.remoteId === c.chatId);
              const inOtherSet = ep && ep.syncSetId && ep.syncSetId !== editingSyncSetId;
              const disabled = inThisSet || inOtherSet;
              let suffix = '';
              if (inThisSet) {
                suffix = ' (already in set)';
              } else if (inOtherSet) {
                suffix = ` (in '${ep.syncSetId}')`;
              } else if (ep) {
                suffix = ` (alias: ${ep.alias})`;
              }
              const label = `${c.title || c.chatId} (${c.type})${suffix}`;
              const currentAlias = ep ? ep.alias : '';
              return `<option value="${escapeHtml(c.chatId)}" data-name="${escapeHtml(c.title || '')}" data-existing-alias="${escapeHtml(currentAlias)}" ${disabled ? 'disabled' : ''}>${escapeHtml(label)}</option>`;
            }).join('');
          if (cachedTelegramChats.length === 0) {
            if (platformSetupNote) {
              platformSetupNote.classList.remove('hidden');
              platformSetupNoteText.textContent = 'No Telegram groups observed yet. Add the bot to a group and send a message.';
            }
          }
        }
      }
    } catch (err) {
      addRemoteSelect.innerHTML = `<option value="">Error loading options</option>`;
    }
    addRemoteSelect.disabled = false;
  }

  // Auto-suggest alias or fill existing alias from selection
  if (addRemoteSelect) {
    addRemoteSelect.addEventListener('change', () => {
      if (!addAliasInput) return;
      const opt = addRemoteSelect.options[addRemoteSelect.selectedIndex];
      if (!opt || !opt.value) {
        addAliasInput.value = '';
        return;
      }
      const existingAlias = opt.getAttribute('data-existing-alias');
      if (existingAlias) {
        addAliasInput.value = existingAlias;
        return;
      }
      const name = opt.getAttribute('data-name') || '';
      const guild = opt.getAttribute('data-guild') || '';
      const base = sanitizeAlias(name || guild || opt.value);
      const existing = cachedEndpoints.map(e => e.alias).concat(editorEndpoints.map(e => e.alias));
      if (base) addAliasInput.value = uniqueAlias(base, existing);
    });
  }

  // Connection selector changes for Add Endpoint
  if (addEndpointConnSelect) {
    addEndpointConnSelect.addEventListener('change', () => {
      loadRemoteOptions(activeTransport);
    });
  }

  // Transport tab clicks
  transportTabs.forEach(tab => {
    tab.addEventListener('click', async () => {
      const tp = tab.getAttribute('data-transport');
      activeTransport = tp;
      transportTabs.forEach(t => {
        const ttp = t.getAttribute('data-transport');
        t.classList.toggle('active', ttp === tp);
        // remove all transport color classes then re-add active one
        t.classList.remove('wa', 'discord', 'telegram');
        if (ttp === tp) t.classList.add(tp === 'whatsapp' ? 'wa' : tp);
      });
      if (addAliasInput) addAliasInput.value = '';
      if (addEndpointAlert) addEndpointAlert.classList.add('hidden');
      await loadRemoteOptions(tp);
    });
  });

  // Add endpoint button
  if (btnAddEndpoint) {
    btnAddEndpoint.addEventListener('click', async () => {
      if (addEndpointAlert) addEndpointAlert.classList.add('hidden');
      const connectionId = addEndpointConnSelect ? addEndpointConnSelect.value.trim() : '';
      const remoteId = addRemoteSelect ? addRemoteSelect.value.trim() : '';
      const alias    = addAliasInput  ? addAliasInput.value.trim()   : '';

      if (!connectionId) {
        if (addEndpointAlert) { addEndpointAlert.textContent = 'Select an owning connection first.'; addEndpointAlert.classList.remove('hidden'); }
        return;
      }
      if (!remoteId) {
        if (addEndpointAlert) { addEndpointAlert.textContent = 'Select a group/channel first.'; addEndpointAlert.classList.remove('hidden'); }
        return;
      }
      if (!ALIAS_REGEX.test(alias)) {
        if (addEndpointAlert) { addEndpointAlert.textContent = 'Alias must start with alphanumeric and be 1–64 chars (letters, numbers, - _).'; addEndpointAlert.classList.remove('hidden'); }
        if (addAliasInput) addAliasInput.focus();
        return;
      }
      if (editorEndpoints.find(e => e.remoteId === remoteId && e.transport === activeTransport)) {
        if (addEndpointAlert) { addEndpointAlert.textContent = 'This group is already added to this sync set.'; addEndpointAlert.classList.remove('hidden'); }
        return;
      }

      // Check if this remote target already has an existing endpoint configured
      const existingByRemote = cachedEndpoints.find(e => e.remoteId === remoteId && e.transport === activeTransport);
      // Check alias uniqueness across other endpoints
      const duplicateAlias = cachedEndpoints.find(e => e.alias === alias && (!existingByRemote || existingByRemote.alias !== alias));
      if (duplicateAlias || editorEndpoints.some(e => e.alias === alias)) {
        if (addEndpointAlert) { addEndpointAlert.textContent = `Alias '${alias}' is already in use by another endpoint.`; addEndpointAlert.classList.remove('hidden'); }
        if (addAliasInput) addAliasInput.focus();
        return;
      }

      setButtonLoading(btnAddEndpoint, true);
      try {
        const syncSetId = editingSyncSetId || null;

        // For existing sets, create or update/rename endpoint immediately
        if (editingSyncSetId) {
          if (existingByRemote) {
            await window.API.updateEndpoint(existingByRemote.alias, { alias, transport: activeTransport, connectionId, remoteId, syncSetId });
          } else {
            await window.API.createEndpoint({ alias, transport: activeTransport, connectionId, remoteId, syncSetId });
          }
          const fresh = await window.API.getEndpoints();
          cachedEndpoints = Array.isArray(fresh) ? fresh : cachedEndpoints;
        }

        editorEndpoints.push({ alias, transport: activeTransport, connectionId, remoteId });
        renderChips();
        // Reset the add form
        if (addRemoteSelect) addRemoteSelect.value = '';
        if (addAliasInput) addAliasInput.value = '';
        if (addEndpointAlert) addEndpointAlert.classList.add('hidden');
        await loadRemoteOptions(activeTransport);
        showToast(`Endpoint '${alias}' added.`, 'success');
      } catch (err) {
        if (addEndpointAlert) { addEndpointAlert.textContent = err.message || 'Failed to add endpoint.'; addEndpointAlert.classList.remove('hidden'); }
      } finally {
        setButtonLoading(btnAddEndpoint, false);
      }
    });
  }

  // Save sync set
  if (btnSaveSyncSet) {
    btnSaveSyncSet.addEventListener('click', async () => {
      const id = editingSyncSetId || (inputSyncsetId ? inputSyncsetId.value.trim() : '');
      if (!SYNC_SET_ID_REGEX.test(id)) {
        showToast('Sync Set ID must start with alphanumeric and be 1–64 chars.', 'warning');
        if (inputSyncsetId) inputSyncsetId.focus();
        return;
      }
      setButtonLoading(btnSaveSyncSet, true);
      try {
        const aliases = editorEndpoints.map(e => e.alias);
        if (editingSyncSetId) {
          await window.API.updateSyncSet(editingSyncSetId, { endpoints: aliases });
          showToast(`Sync set '${editingSyncSetId}' saved.`, 'success');
        } else {
          // Create set first
          await window.API.createSyncSet({ id, endpoints: [] });
          editingSyncSetId = id;
          // Now create or update all pending endpoints
          for (const ep of editorEndpoints) {
            const existingEp = cachedEndpoints.find(e => e.remoteId === ep.remoteId && e.transport === ep.transport);
            if (existingEp) {
              await window.API.updateEndpoint(existingEp.alias, { alias: ep.alias, transport: ep.transport, connectionId: ep.connectionId || existingEp.connectionId, remoteId: ep.remoteId, syncSetId: id });
            } else {
              await window.API.createEndpoint({ alias: ep.alias, transport: ep.transport, connectionId: ep.connectionId, remoteId: ep.remoteId, syncSetId: id });
            }
          }
          // Update the sync set with endpoint aliases
          await window.API.updateSyncSet(id, { endpoints: aliases });
          showToast(`Sync set '${id}' created.`, 'success');
        }
        await loadSyncSetsPage();
        // Re-open editor for the now-saved set
        openSyncSetEditor(editingSyncSetId);
      } catch (err) {
        showToast(err.message || 'Failed to save sync set.', 'danger');
      } finally {
        setButtonLoading(btnSaveSyncSet, false);
      }
    });
  }

  if (btnNewSyncSet) {
    btnNewSyncSet.addEventListener('click', () => {
      editingSyncSetId = null;
      editorEndpoints = [];
      openSyncSetEditor(null);
    });
  }

  if (btnCancelEditor) {
    btnCancelEditor.addEventListener('click', () => {
      editingSyncSetId = null;
      showEditorPlaceholder();
      renderSyncSetList();
    });
  }

  if (btnDeleteSyncSet) {
    btnDeleteSyncSet.addEventListener('click', () => {
      if (editingSyncSetId) handleDeleteSyncSet(editingSyncSetId);
    });
  }

  // ══════════════════════════════════════════════════════════════
  // Settings
  // ══════════════════════════════════════════════════════════════

  async function loadSettings() {
    if (settingsAlert) settingsAlert.classList.add('hidden');
    try {
      const cfg = await window.API.getConfig();
      cachedConfig = cfg;
      populateSettings(cfg);
    } catch (err) {
      showToast(err.message || 'Failed to load settings', 'danger');
    }
  }

  function populateSettings(cfg) {
    if (!cfg) return;
    if (settingsUsernameMode)  settingsUsernameMode.value  = cfg.usernameMode || 'push_name';
    if (settingsMediaEnabled)  settingsMediaEnabled.checked = cfg.media ? cfg.media.enabled : false;
    if (settingsMediaMaxSize)  settingsMediaMaxSize.value  = cfg.media ? cfg.media.maxSizeMB : 50;
    if (settingsRecoveryEnabled) settingsRecoveryEnabled.checked = cfg.recovery ? cfg.recovery.enabled : true;
    if (settingsRecoveryMaxAge)  settingsRecoveryMaxAge.value   = cfg.recovery ? cfg.recovery.maxAgeHours : 72;
    if (settingsRecoveryMaxMsgs) settingsRecoveryMaxMsgs.value  = cfg.recovery ? cfg.recovery.maxMessagesPerGroup : 1000;
    if (settingsRetentionDays)   settingsRetentionDays.value    = cfg.storage ? cfg.storage.messageRetentionDays : 14;
    if (settingsPollsAggTrigger) settingsPollsAggTrigger.value  = cfg.polls ? cfg.polls.aggregationTrigger : 'aggregate-response';
    if (settingsLocalPrefix) settingsLocalPrefix.value = cfg.localPrefix || '';
    if (settingsWaCleanupEnabled) settingsWaCleanupEnabled.checked = cfg.whatsappCleanup ? cfg.whatsappCleanup.enabled : false;
    if (settingsWaCleanupRetention) settingsWaCleanupRetention.value = cfg.whatsappCleanup ? cfg.whatsappCleanup.retentionDays : 30;
    if (settingsWaDeviceName) settingsWaDeviceName.value = cfg.whatsappDeviceName || 'message-sync';
  }

  if (formSettings) {
    formSettings.addEventListener('submit', async e => {
      e.preventDefault();
      if (settingsAlert) settingsAlert.classList.add('hidden');

      const mediaMaxSize   = parseInt(settingsMediaMaxSize ? settingsMediaMaxSize.value : 50, 10);
      const recovMaxAge    = parseInt(settingsRecoveryMaxAge ? settingsRecoveryMaxAge.value : 72, 10);
      const recovMaxMsgs   = parseInt(settingsRecoveryMaxMsgs ? settingsRecoveryMaxMsgs.value : 1000, 10);
      const retentionDays  = parseInt(settingsRetentionDays ? settingsRetentionDays.value : 14, 10);
      const waRetention    = parseInt(settingsWaCleanupRetention ? settingsWaCleanupRetention.value : 30, 10);
      const aggTrigger     = settingsPollsAggTrigger ? settingsPollsAggTrigger.value.trim() : '';
      const localPrefix    = settingsLocalPrefix ? settingsLocalPrefix.value : '';
      const waDeviceName   = settingsWaDeviceName ? settingsWaDeviceName.value.trim() : '';

      if (isNaN(mediaMaxSize) || mediaMaxSize < 1)  { settingsAlert.textContent = 'Media max size must be ≥1 MB.'; settingsAlert.classList.remove('hidden'); return; }
      if (isNaN(recovMaxAge) || recovMaxAge < 1)     { settingsAlert.textContent = 'Recovery max age must be ≥1 hour.'; settingsAlert.classList.remove('hidden'); return; }
      if (isNaN(recovMaxMsgs) || recovMaxMsgs < 1)   { settingsAlert.textContent = 'Recovery max messages must be ≥1.'; settingsAlert.classList.remove('hidden'); return; }
      if (isNaN(retentionDays) || retentionDays < 1) { settingsAlert.textContent = 'Retention must be ≥1 day.'; settingsAlert.classList.remove('hidden'); return; }
      if (!aggTrigger) { settingsAlert.textContent = 'Polls aggregation trigger cannot be empty.'; settingsAlert.classList.remove('hidden'); return; }

      const payload = {
        usernameMode: settingsUsernameMode ? settingsUsernameMode.value : 'push_name',
        media: { enabled: settingsMediaEnabled ? settingsMediaEnabled.checked : false, maxSizeMB: mediaMaxSize },
        recovery: { enabled: settingsRecoveryEnabled ? settingsRecoveryEnabled.checked : true, maxAgeHours: recovMaxAge, maxMessagesPerGroup: recovMaxMsgs },
        storage: { messageRetentionDays: retentionDays },
        polls: { aggregationTrigger: aggTrigger },
        whatsappCleanup: { enabled: settingsWaCleanupEnabled ? settingsWaCleanupEnabled.checked : false, retentionDays: isNaN(waRetention) ? 30 : waRetention },
        localPrefix: localPrefix,
        whatsappDeviceName: waDeviceName || 'message-sync'
      };

      setButtonLoading(btnSaveSettings, true);
      try {
        const updated = await window.API.updateConfig(payload);
        cachedConfig = updated;
        populateSettings(updated);
        showToast('Settings saved.', 'success');
      } catch (err) {
        if (settingsAlert) { settingsAlert.textContent = err.message || 'Failed to save settings.'; settingsAlert.classList.remove('hidden'); }
      } finally {
        setButtonLoading(btnSaveSettings, false);
      }
    });
  }

  if (btnResetSettings) {
    btnResetSettings.addEventListener('click', () => {
      if (cachedConfig) populateSettings(cachedConfig);
    });
  }

  if (formChangePassword) {
    formChangePassword.addEventListener('submit', async e => {
      e.preventDefault();
      if (changePwdAlert) changePwdAlert.classList.add('hidden');
      const cur  = changePwdCurrent ? changePwdCurrent.value : '';
      const next = changePwdNew    ? changePwdNew.value    : '';
      const conf = changePwdConfirm ? changePwdConfirm.value : '';
      if (!cur) { if (changePwdAlert) { changePwdAlert.textContent = 'Current password required.'; changePwdAlert.classList.remove('hidden'); } return; }
      if (next.length < 8 || next.length > 72) { if (changePwdAlert) { changePwdAlert.textContent = 'New password must be 8–72 chars.'; changePwdAlert.classList.remove('hidden'); } return; }
      if (next !== conf) { if (changePwdAlert) { changePwdAlert.textContent = 'Passwords do not match.'; changePwdAlert.classList.remove('hidden'); } return; }
      setButtonLoading(btnSubmitChangePwd, true);
      try {
        await window.API.changePassword(cur, next);
        showToast('Password updated.', 'success');
        if (changePwdCurrent) changePwdCurrent.value = '';
        if (changePwdNew)     changePwdNew.value     = '';
        if (changePwdConfirm) changePwdConfirm.value = '';
      } catch (err) {
        if (changePwdAlert) { changePwdAlert.textContent = err.message || 'Failed to change password.'; changePwdAlert.classList.remove('hidden'); }
      } finally {
        setButtonLoading(btnSubmitChangePwd, false);
      }
    });
  }

  // ══════════════════════════════════════════════════════════════
  // Users
  // ══════════════════════════════════════════════════════════════

  async function loadUsers() {
    try {
      const users = await window.API.getUsers();
      if (!usersTableBody) return;
      usersTableBody.innerHTML = (Array.isArray(users) ? users : []).map(u => `
        <tr>
          <td>${escapeHtml(u.username)}</td>
          <td><span class="badge ${u.role === 'admin' ? 'badge-primary' : 'badge-neutral'}">${escapeHtml(u.role)}</span></td>
          <td><span class="badge ${u.active ? 'badge-success' : 'badge-neutral'}">${u.active ? 'Active' : 'Inactive'}</span></td>
          <td class="text-right">
            <div class="table-actions">
              <button class="btn btn-ghost btn-sm" onclick="App.toggleUser('${escapeHtml(u.id)}',${!u.active})">${u.active ? 'Deactivate' : 'Activate'}</button>
              <button class="btn btn-ghost btn-sm" onclick="App.resetUser('${escapeHtml(u.id)}')">Reset Token</button>
            </div>
          </td>
        </tr>
      `).join('');
    } catch (err) {
      showToast(err.message || 'Failed to load users', 'danger');
    }
  }

  if (btnCreateInvite) {
    btnCreateInvite.addEventListener('click', async () => {
      try {
        const role = inviteRole ? inviteRole.value : 'operator';
        const res = await window.API.createInvite(role, 48);
        const token = res && res.token ? res.token : (typeof res === 'string' ? res : '');
        if (inviteResult && token) {
          const inviteUrl = `${window.location.origin}/#invite?token=${encodeURIComponent(token)}`;
          inviteResult.innerHTML = `
            <div style="margin-bottom:6px;"><strong>Invite Token:</strong> <code style="user-select:all;word-break:break-all;">${escapeHtml(token)}</code></div>
            <div><strong>Redeem Link:</strong> <a href="${escapeHtml(inviteUrl)}" target="_blank" style="color:var(--primary);text-decoration:none;word-break:break-all;">${escapeHtml(inviteUrl)}</a></div>
          `;
          inviteResult.classList.remove('hidden');
        }
      } catch (err) {
        showToast(err.message || 'Failed to create invite', 'danger');
      }
    });
  }

  // ══════════════════════════════════════════════════════════════
  // Membership
  // ══════════════════════════════════════════════════════════════

  async function loadMembership() {
    const isAdmin = currentUser && currentUser.role === 'admin';
    if (!isAdmin) return;
    if (membershipPipelines) membershipPipelines.classList.remove('hidden');
    await refreshMembership();
    await loadPipelines();
  }

  async function refreshMembership() {
    if (!membershipTableBody) return;
    const filters = {};
    if (membershipStatusFilter && membershipStatusFilter.value) filters.status = membershipStatusFilter.value;
    if (membershipAgeFilter && membershipAgeFilter.value)     filters.ageHours = membershipAgeFilter.value;
    try {
      const data = await window.API.getMembershipRequests(filters);
      const items = Array.isArray(data) ? data : (data && data.requests ? data.requests : []);
      membershipTableBody.innerHTML = items.map(r => `
        <tr>
          <td>${escapeHtml(r.id || '—')}</td>
          <td><span class="badge badge-neutral">${escapeHtml(r.status || '—')}</span></td>
          <td>${escapeHtml(r.verificationStatus || '—')}</td>
          <td>${escapeHtml(r.signals || '—')}</td>
          <td class="text-right">
            <div class="table-actions">
              <button class="btn btn-ghost btn-sm" onclick="App.membershipDecide('${escapeHtml(r.id)}','approve')">Approve</button>
              <button class="btn btn-ghost btn-sm" onclick="App.membershipDecide('${escapeHtml(r.id)}','reject')">Reject</button>
            </div>
          </td>
        </tr>
      `).join('') || `<tr><td colspan="5" class="text-muted" style="padding:20px;text-align:center;">No requests</td></tr>`;
    } catch (err) {
      showToast(err.message || 'Failed to load membership requests', 'danger');
    }
  }

  async function loadPipelines() {
    if (!pipelineList) return;
    try {
      const pipelines = await window.API.getVerificationPipelines();
      pipelineList.innerHTML = (Array.isArray(pipelines) ? pipelines : []).map(p => `
        <div style="display:flex;align-items:center;gap:8px;padding:6px 0;border-bottom:1px solid var(--border);">
          <span style="flex:1;font-size:.8rem;">${escapeHtml(p.label || p.id)}</span>
          <span class="badge badge-neutral">${escapeHtml(p.transport)}</span>
          <span class="alias-badge">${escapeHtml(p.endpoint)}</span>
          <button class="btn btn-danger btn-sm" onclick="App.deletePipeline('${escapeHtml(p.id)}')">Delete</button>
        </div>
      `).join('') || '<p style="font-size:.78rem;color:var(--text-muted);padding:8px 0;">No pipelines configured.</p>';
    } catch (e) {}
  }

  if (membershipRefresh) membershipRefresh.addEventListener('click', refreshMembership);

  if (pipelineCreate) {
    pipelineCreate.addEventListener('click', async () => {
      try {
        const label    = pipelineLabel    ? pipelineLabel.value.trim()    : '';
        const transport = pipelineTransport ? pipelineTransport.value     : 'whatsapp';
        const endpoint  = pipelineEndpoint  ? pipelineEndpoint.value.trim() : '';
        const roleId    = pipelineRole      ? pipelineRole.value.trim()   : '';
        await window.API.createVerificationPipeline({ label, transport, endpoint, discordRoleId: roleId || undefined });
        showToast('Pipeline created.', 'success');
        await loadPipelines();
      } catch (err) { showToast(err.message || 'Failed to create pipeline', 'danger'); }
    });
  }

  // Exposed globals for inline onclick handlers
  window.App = {
    openDiscovery,
    selectDiscoveredTarget,
    openReassignModal,
    openReplaceTokenModal,
    toggleConnection,
    deleteConnection,
    pairWhatsApp: openWaPairModal,
    logoutWhatsApp: handleWaLogout,
    async toggleUser(id, active) {
      try { await window.API.setUserActive(id, active); await loadUsers(); } catch (err) { showToast(err.message || 'Failed', 'danger'); }
    },
    async resetUser(id) {
      try {
        const res = await window.API.createResetToken(id);
        showToast(`Reset token: ${res.token || JSON.stringify(res)}`, 'info', 8000);
      } catch (err) { showToast(err.message || 'Failed', 'danger'); }
    },
    async membershipDecide(id, action) {
      try { await window.API.decideMembership(id, action); await refreshMembership(); } catch (err) { showToast(err.message || 'Failed', 'danger'); }
    },
    async deletePipeline(id) {
      try { await window.API.deleteVerificationPipeline(id); await loadPipelines(); } catch (err) { showToast(err.message || 'Failed', 'danger'); }
    }
  };

  // ══════════════════════════════════════════════════════════════
  // Event Wiring
  // ══════════════════════════════════════════════════════════════

  function wireEvents() {
    // Nav items
    document.querySelectorAll('.nav-item[data-nav]').forEach(item => {
      item.addEventListener('click', () => {
        const nav = item.getAttribute('data-nav');
        window.Router.navigate(nav);
      });
    });

    // Logout
    if (btnLogout) btnLogout.addEventListener('click', handleLogout);

    // Auth forms
    if (formSetup)  formSetup.addEventListener('submit', handleSetupSubmit);
    if (formLogin)  formLogin.addEventListener('submit', handleLoginSubmit);
    if (formInvite) formInvite.addEventListener('submit', handleInviteSubmit);
    if (formReset)  formReset.addEventListener('submit', handleResetSubmit);

    // Dashboard refresh
    if (btnDashRefresh) btnDashRefresh.addEventListener('click', loadDashboard);

    // Platform card menus
    if (waMenuBtn && waMenu)         waMenuBtn.addEventListener('click', e => { e.stopPropagation(); toggleDropdown(waMenuBtn, waMenu); });
    if (discordMenuBtn && discordMenu) discordMenuBtn.addEventListener('click', e => { e.stopPropagation(); toggleDropdown(discordMenuBtn, discordMenu); });
    if (telegramMenuBtn && telegramMenu) telegramMenuBtn.addEventListener('click', e => { e.stopPropagation(); toggleDropdown(telegramMenuBtn, telegramMenu); });

    // Connections actions
    if (btnConnectionsRefresh) btnConnectionsRefresh.addEventListener('click', loadConnections);
    if (btnAddConnection) btnAddConnection.addEventListener('click', openAddConnectionModal);
    if (btnEmptyAddConnection) btnEmptyAddConnection.addEventListener('click', openAddConnectionModal);
    if (formAddConnection) formAddConnection.addEventListener('submit', handleAddConnectionSubmit);
    if (formReplaceToken) formReplaceToken.addEventListener('submit', handleReplaceTokenSubmit);
    if (formReassignEndpoint) formReassignEndpoint.addEventListener('submit', submitReassign);
    if (btnDiscoverySubmitCreate) btnDiscoverySubmitCreate.addEventListener('click', submitDiscoveryCreate);
    if (btnDiscoveryCancelCreate) {
      btnDiscoveryCancelCreate.addEventListener('click', () => {
        if (discoveryCreateForm) discoveryCreateForm.classList.add('hidden');
      });
    }
    const addConnTabs = document.querySelectorAll('#add-conn-transport-tabs .transport-tab');
    addConnTabs.forEach(t => {
      t.addEventListener('click', () => selectAddConnTransport(t.getAttribute('data-transport')));
    });

    // WA actions
    if (btnWaPair)   btnWaPair.addEventListener('click', openWaPairModal);
    if (btnWaLogout) btnWaLogout.addEventListener('click', handleWaLogout);
    if (btnWaCancelPair) btnWaCancelPair.addEventListener('click', cancelWaPairing);

    // Discord refresh
    const btnDiscordRefreshStatus = document.getElementById('btn-discord-refresh-status');
    if (btnDiscordRefreshStatus) {
      btnDiscordRefreshStatus.addEventListener('click', async () => {
        closeAllDropdowns();
        try {
          const s = await window.API.getDiscordStatus();
          renderDiscordStatus(s);
        } catch (e) {
          renderDiscordStatus(null);
        }
      });
    }

    // Telegram refresh
    const btnTelegramRefreshStatus = document.getElementById('btn-telegram-refresh-status');
    if (btnTelegramRefreshStatus) {
      btnTelegramRefreshStatus.addEventListener('click', async () => {
        closeAllDropdowns();
        try {
          const s = await window.API.getTelegramStatus();
          renderTelegramStatus(s);
        } catch (e) {
          renderTelegramStatus(null);
        }
      });
    }

    // Auth: unauthorized event
    window.addEventListener('auth:unauthorized', () => {
      isAuthenticated = false;
      currentUser = null;
      updateSessionDisplay();
      window.Router.navigate('login');
    });

    const linkGotoInvite = document.getElementById('link-goto-invite');
    if (linkGotoInvite) {
      linkGotoInvite.addEventListener('click', (e) => {
        e.preventDefault();
        window.Router.navigate('invite');
      });
    }

    const linkGotoLogin = document.getElementById('link-goto-login');
    if (linkGotoLogin) {
      linkGotoLogin.addEventListener('click', (e) => {
        e.preventDefault();
        window.Router.navigate('login');
      });
    }
  }

  // ══════════════════════════════════════════════════════════════
  // Router Setup
  // ══════════════════════════════════════════════════════════════

  function setupRouter() {
    const authRequiredRoutes = new Set(['dashboard', 'connections', 'sync-sets', 'settings', 'users', 'membership']);

    window.Router.beforeEach(async (route) => {
      if (isSetup === null) {
        try {
          const status = await window.API.getAuthStatus();
          isSetup = status.isSetup;
          // /api/auth/status only tells us whether setup is done.
          // Whether the session is authenticated is determined by trying /api/auth/me.
          if (isSetup) {
            try {
              const me = await window.API.getCurrentUser();
              isAuthenticated = true;
              currentUser = me;
              updateSessionDisplay();
            } catch (e) {
              isAuthenticated = false;
            }
          }
        } catch (e) {
          isSetup = false;
          isAuthenticated = false;
        }
      } else if (isAuthenticated && !currentUser) {
        try {
          const me = await window.API.getCurrentUser();
          currentUser = me;
          updateSessionDisplay();
        } catch (e) {}
      }

      // Handle invite/reset deep links
      if (route === 'invite' || route === 'reset') return route;

      if (!isSetup) return 'setup';
      if (route === 'setup') return isAuthenticated ? 'dashboard' : 'login';
      if (!isAuthenticated) return 'login';
      if (!authRequiredRoutes.has(route)) return 'dashboard';
      return route;
    });

    const routeNames = ['dashboard', 'connections', 'sync-sets', 'settings', 'users', 'membership',
                        'loading', 'setup', 'login', 'invite', 'reset'];
    routeNames.forEach(name => {
      window.Router.addRoute(name, () => switchView(name));
    });
    window.Router.addRoute('*', () => switchView('dashboard'));

    window.Router.init();
  }

  // ══════════════════════════════════════════════════════════════
  // Init
  // ══════════════════════════════════════════════════════════════

  function init() {
    setupPasswordToggles();
    setupFormValidation();
    setupModalListeners();
    wireEvents();
    setupRouter();
  }

  init();

})();
