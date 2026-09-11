from pathlib import Path
root=Path('.')

def rep(path, old, new, label):
    p=root/path
    s=p.read_text()
    if old not in s:
        raise SystemExit(f'missing marker: {label}')
    p.write_text(s.replace(old,new,1))

def append_once(path, marker, text):
    p=root/path
    s=p.read_text()
    if marker not in s:
        p.write_text(s+text)

# ---------- Backend hardening: integration mode is immutable; logout scrubs account-bound caches. ----------
rep('internal/api/connections.go',
'''type UpdateConnectionRequest struct {\n\tLabel   *string `json:"label,omitempty"`\n\tEnabled *bool   `json:"enabled,omitempty"`\n\tToken   *string `json:"token,omitempty"`\n}\n''',
'''type UpdateConnectionRequest struct {\n\tLabel           *string `json:"label,omitempty"`\n\tEnabled         *bool   `json:"enabled,omitempty"`\n\tToken           *string `json:"token,omitempty"`\n\tIntegrationMode *string `json:"integrationMode,omitempty"`\n}\n''','update request integration mode')
rep('internal/api/connections.go',
'''\tconn.EncryptedCredential = encCred\n\tconn.CredentialNonce = nonce\n\tconn.IntegrationMode = controlstore.NormalizeIntegrationMode(conn.Transport, conn.IntegrationMode)\n\toldEncCred := append([]byte(nil), encCred...)\n''',
'''\tconn.EncryptedCredential = encCred\n\tconn.CredentialNonce = nonce\n\tconn.IntegrationMode = controlstore.NormalizeIntegrationMode(conn.Transport, conn.IntegrationMode)\n\tif req.IntegrationMode != nil {\n\t\trequestedMode := strings.TrimSpace(*req.IntegrationMode)\n\t\tif err := controlstore.ValidateIntegrationMode(conn.Transport, requestedMode); err != nil {\n\t\t\tWriteError(w, http.StatusBadRequest, err.Error())\n\t\t\treturn\n\t\t}\n\t\trequestedMode = controlstore.NormalizeIntegrationMode(conn.Transport, requestedMode)\n\t\tif requestedMode != conn.IntegrationMode {\n\t\t\tWriteError(w, http.StatusConflict, "connection integration mode cannot be changed in place; create a new connection and reassign endpoints")\n\t\t\treturn\n\t\t}\n\t}\n\toldEncCred := append([]byte(nil), encCred...)\n''','immutable integration mode')

rep('internal/transport/telegram/mtproto_live.go',
'''func (l *mtprotoLiveState) setSelfID(id int64) {\n\tl.mu.Lock()\n\tl.selfID = id\n\tl.mu.Unlock()\n}\n''',
'''func (l *mtprotoLiveState) setSelfID(id int64) {\n\tl.mu.Lock()\n\tl.selfID = id\n\tl.mu.Unlock()\n}\n\nfunc (l *mtprotoLiveState) clearAccountState() {\n\tif l == nil {\n\t\treturn\n\t}\n\tl.mu.Lock()\n\tl.selfID = 0\n\tl.peers = make(map[string]mtprotoPeerState)\n\tl.messageEndpoints = make(map[int]transport.EndpointID)\n\tl.messageOrder = nil\n\tl.reactions = make(map[string]map[int64]mtprotoReactionState)\n\tl.pendingSends = make(map[string]int)\n\tl.pendingMutations = make(map[string]int)\n\tl.mu.Unlock()\n}\n''','clear account runtime state')
rep('internal/transport/telegram/mtproto.go',
'''\tstate.Session = nil\n\tstate.Peers = nil\n\tif a.live != nil {\n\t\ta.live.loadPeers(nil)\n\t}\n''',
'''\tstate.Session = nil\n\tstate.Peers = nil\n\tstate.Polls = nil\n\tif a.live != nil {\n\t\ta.live.clearAccountState()\n\t}\n''','logout clears session peers polls')

# ---------- Admin HTML: Telegram mode choice and full MTProto management UI. ----------
rep('internal/api/web/index.html',
'''          <div id="add-conn-token-group" class="form-group hidden">\n            <label for="conn-bot-token" class="form-label">Bot Token (Paste once)</label>\n            <input type="password" id="conn-bot-token" class="form-input" autocomplete="new-password" placeholder="Paste bot token">\n            <p class="form-hint">Token is encrypted immediately with a domain-separated AES key. It is never logged or exposed in read APIs.</p>\n          </div>\n''',
'''          <div id="add-conn-telegram-mode-group" class="form-group hidden">\n            <label for="add-conn-telegram-mode" class="form-label">Telegram Integration Method</label>\n            <select id="add-conn-telegram-mode" class="form-select">\n              <option value="bot">Bot API</option>\n              <option value="mtproto">Phone / MTProto</option>\n            </select>\n            <p class="form-hint">A Telegram connection uses exactly one method. Change methods by creating a new connection and reassigning endpoints.</p>\n          </div>\n\n          <div id="add-conn-token-group" class="form-group hidden">\n            <label for="conn-bot-token" class="form-label">Bot Token (Paste once)</label>\n            <input type="password" id="conn-bot-token" class="form-input" autocomplete="new-password" placeholder="Paste bot token">\n            <p class="form-hint">Token is encrypted immediately with a domain-separated AES key. It is never logged or exposed in read APIs.</p>\n          </div>\n\n          <div id="add-conn-mtproto-group" class="hidden" style="display:flex;flex-direction:column;gap:12px;">\n            <div class="form-group">\n              <label for="add-conn-mtproto-api-id" class="form-label">Telegram API ID</label>\n              <input type="number" min="1" id="add-conn-mtproto-api-id" class="form-input" inputmode="numeric" placeholder="123456">\n            </div>\n            <div class="form-group">\n              <label for="add-conn-mtproto-api-hash" class="form-label">Telegram API Hash</label>\n              <input type="password" id="add-conn-mtproto-api-hash" class="form-input" autocomplete="new-password" placeholder="API hash">\n            </div>\n            <div class="form-group">\n              <label for="add-conn-mtproto-phone" class="form-label">Phone Number</label>\n              <input type="tel" id="add-conn-mtproto-phone" class="form-input" autocomplete="off" placeholder="+15551234567">\n              <p class="form-hint">The API hash, phone and resulting Telegram session are stored only inside the encrypted connection state in control.db.</p>\n            </div>\n          </div>\n''','telegram creation fields')

rep('internal/api/web/index.html',
'''          <div id="add-conn-help-telegram" class="form-hint hidden">\n            <strong>Telegram setup</strong><br>\n            In Telegram, message the official <a href="https://t.me/BotFather" target="_blank" rel="noopener noreferrer">@BotFather</a>, run <code>/newbot</code>, and paste the token here. Add the bot to the intended groups/supergroups; disable Bot Privacy Mode with BotFather when ordinary group-message discovery requires it, then send a message and use Discover. Forum topics are automatic child scopes. Broadcast channels are not supported. See the <a href="https://core.telegram.org/bots/faq#what-messages-will-my-bot-get" target="_blank" rel="noopener noreferrer">official Bot API visibility guidance</a> for privacy behavior.\n          </div>\n''',
'''          <div id="add-conn-help-telegram" class="form-hint hidden">\n            <div id="add-conn-help-telegram-bot">\n              <strong>Telegram Bot API</strong><br>\n              Create a bot with <a href="https://t.me/BotFather" target="_blank" rel="noopener noreferrer">@BotFather</a>. Discovery is observation-based; Bot Privacy Mode/admin visibility controls which group messages the bot can observe.\n            </div>\n            <div id="add-conn-help-telegram-mtproto" class="hidden">\n              <strong>Telegram Phone / MTProto</strong><br>\n              Obtain an API ID/hash from Telegram's application-development page, then authenticate the phone account with the one-time code and, when enabled, its 2FA password. Joined-group/topic discovery is complete and bounded history recovery is available after login.\n            </div>\n            <div style="margin-top:6px;">Telegram private DMs and broadcast channels are not synchronized.</div>\n          </div>\n''','telegram mode help')

# Insert management modal before discovery modal.
rep('internal/api/web/index.html',
'''  <!-- Discovery Modal -->\n  <div id="modal-discovery" class="modal-overlay hidden" role="dialog" aria-modal="true">\n''',
'''  <!-- Telegram MTProto Session Modal -->\n  <div id="modal-telegram-mtproto" class="modal-overlay hidden" role="dialog" aria-modal="true">\n    <div class="modal-card" style="max-width:620px;">\n      <div class="modal-header">\n        <div><h3 class="modal-title">Telegram Phone / MTProto</h3><div id="mtproto-conn-info" class="alias-badge" style="margin-top:4px;"></div></div>\n        <button type="button" class="btn-modal-close" data-modal="modal-telegram-mtproto" aria-label="Close"><svg class="icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><line x1="18" y1="6" x2="6" y2="18"></line><line x1="6" y1="6" x2="18" y2="18"></line></svg></button>\n      </div>\n      <div class="modal-body" style="display:flex;flex-direction:column;gap:14px;">\n        <div id="mtproto-alert" class="alert hidden"></div>\n        <div><span class="form-label">Session state</span> <span id="mtproto-status" class="badge badge-neutral">Disconnected</span></div>\n        <div id="mtproto-setup-group" style="display:flex;flex-direction:column;gap:10px;">\n          <div class="form-group"><label for="mtproto-api-id" class="form-label">API ID</label><input type="number" min="1" id="mtproto-api-id" class="form-input" inputmode="numeric"></div>\n          <div class="form-group"><label for="mtproto-api-hash" class="form-label">API Hash</label><input type="password" id="mtproto-api-hash" class="form-input" autocomplete="new-password"></div>\n          <div class="form-group"><label for="mtproto-phone" class="form-label">Phone</label><input type="tel" id="mtproto-phone" class="form-input" autocomplete="off" placeholder="+15551234567"></div>\n          <button type="button" id="btn-mtproto-setup" class="btn btn-primary"><span class="btn-text">Save &amp; Request Code</span><div class="btn-spinner hidden"></div></button>\n        </div>\n        <button type="button" id="btn-mtproto-request-code" class="btn btn-primary hidden"><span class="btn-text">Request Login Code</span><div class="btn-spinner hidden"></div></button>\n        <div id="mtproto-code-group" class="form-group hidden"><label for="mtproto-code" class="form-label">One-time Login Code</label><div style="display:flex;gap:8px;"><input type="text" id="mtproto-code" class="form-input" autocomplete="one-time-code" inputmode="numeric"><button type="button" id="btn-mtproto-code" class="btn btn-primary"><span class="btn-text">Verify Code</span><div class="btn-spinner hidden"></div></button></div></div>\n        <div id="mtproto-password-group" class="form-group hidden"><label for="mtproto-password" class="form-label">Telegram 2FA Password</label><div style="display:flex;gap:8px;"><input type="password" id="mtproto-password" class="form-input" autocomplete="current-password"><button type="button" id="btn-mtproto-password" class="btn btn-primary"><span class="btn-text">Verify 2FA</span><div class="btn-spinner hidden"></div></button></div></div>\n        <div id="mtproto-backfill-group" class="hidden" style="border-top:1px solid var(--border);padding-top:12px;">\n          <div class="form-label">Bounded History Backfill</div>\n          <div style="display:grid;grid-template-columns:2fr 1fr 1fr;gap:8px;margin-top:8px;">\n            <select id="mtproto-backfill-endpoint" class="form-select"></select>\n            <input type="number" id="mtproto-backfill-events" class="form-input" min="1" max="1000" value="200" title="Maximum events">\n            <input type="number" id="mtproto-backfill-hours" class="form-input" min="1" max="720" value="24" title="Maximum age in hours">\n          </div>\n          <button type="button" id="btn-mtproto-backfill" class="btn btn-ghost btn-sm" style="margin-top:8px;"><span class="btn-text">Run Bounded Backfill</span><div class="btn-spinner hidden"></div></button>\n        </div>\n      </div>\n      <div class="modal-footer"><button type="button" class="btn btn-ghost" data-modal="modal-telegram-mtproto">Close</button><button type="button" id="btn-mtproto-logout" class="btn btn-danger hidden">Log Out Telegram Session</button></div>\n    </div>\n  </div>\n\n  <!-- Discovery Modal -->\n  <div id="modal-discovery" class="modal-overlay hidden" role="dialog" aria-modal="true">\n''','mtproto management modal')

# ---------- API client methods. ----------
rep('internal/api/web/js/api.js',
'''    async getConnectionDiscovery(id) {\n      return this.request(`/api/connections/${encodeURIComponent(id)}/discovery`);\n    },\n\n    async pairWhatsAppConnection(id) {\n''',
'''    async getConnectionDiscovery(id) {\n      return this.request(`/api/connections/${encodeURIComponent(id)}/discovery`);\n    },\n\n    async setupTelegramMTProto(id, apiId, apiHash, phone) {\n      return this.request(`/api/connections/${encodeURIComponent(id)}/telegram/mtproto/setup`, { method: 'POST', body: { apiId, apiHash, phone } });\n    },\n    async requestTelegramMTProtoCode(id) {\n      return this.request(`/api/connections/${encodeURIComponent(id)}/telegram/mtproto/send-code`, { method: 'POST' });\n    },\n    async submitTelegramMTProtoCode(id, code) {\n      return this.request(`/api/connections/${encodeURIComponent(id)}/telegram/mtproto/code`, { method: 'POST', body: { code } });\n    },\n    async submitTelegramMTProtoPassword(id, password) {\n      return this.request(`/api/connections/${encodeURIComponent(id)}/telegram/mtproto/password`, { method: 'POST', body: { password } });\n    },\n    async logoutTelegramMTProto(id) {\n      return this.request(`/api/connections/${encodeURIComponent(id)}/logout`, { method: 'POST' });\n    },\n    async getTelegramTopics(id, remoteId) {\n      const query = new URLSearchParams({ remoteId });\n      return this.request(`/api/connections/${encodeURIComponent(id)}/telegram/topics?${query.toString()}`);\n    },\n    async backfillTelegramMTProto(id, endpoint, maxEvents, maxAgeHours) {\n      return this.request(`/api/connections/${encodeURIComponent(id)}/telegram/backfill`, { method: 'POST', body: { endpoint, maxEvents, maxAgeHours } });\n    },\n\n    async pairWhatsAppConnection(id) {\n''','mtproto api client methods')

# ---------- Admin controller DOM references. ----------
rep('internal/api/web/js/app-base.js',
'''  const addConnToken          = document.getElementById('conn-bot-token');\n  const addConnTokenGroup     = document.getElementById('add-conn-token-group');\n  const addConnHelpDiscord    = document.getElementById('add-conn-help-discord');\n  const addConnHelpTelegram   = document.getElementById('add-conn-help-telegram');\n''',
'''  const addConnToken          = document.getElementById('conn-bot-token');\n  const addConnTokenGroup     = document.getElementById('add-conn-token-group');\n  const addConnTelegramModeGroup = document.getElementById('add-conn-telegram-mode-group');\n  const addConnTelegramMode   = document.getElementById('add-conn-telegram-mode');\n  const addConnMTProtoGroup   = document.getElementById('add-conn-mtproto-group');\n  const addConnMTProtoAPIID   = document.getElementById('add-conn-mtproto-api-id');\n  const addConnMTProtoAPIHash = document.getElementById('add-conn-mtproto-api-hash');\n  const addConnMTProtoPhone   = document.getElementById('add-conn-mtproto-phone');\n  const addConnHelpDiscord    = document.getElementById('add-conn-help-discord');\n  const addConnHelpTelegram   = document.getElementById('add-conn-help-telegram');\n  const addConnHelpTelegramBot = document.getElementById('add-conn-help-telegram-bot');\n  const addConnHelpTelegramMTProto = document.getElementById('add-conn-help-telegram-mtproto');\n''','creation dom refs')
rep('internal/api/web/js/app-base.js',
'''  const btnSubmitReplaceToken = document.getElementById('btn-submit-replace-token');\n\n  const modalDiscovery        = document.getElementById('modal-discovery');\n''',
'''  const btnSubmitReplaceToken = document.getElementById('btn-submit-replace-token');\n\n  const modalTelegramMTProto = document.getElementById('modal-telegram-mtproto');\n  const mtprotoConnInfo = document.getElementById('mtproto-conn-info');\n  const mtprotoAlert = document.getElementById('mtproto-alert');\n  const mtprotoStatus = document.getElementById('mtproto-status');\n  const mtprotoSetupGroup = document.getElementById('mtproto-setup-group');\n  const mtprotoAPIID = document.getElementById('mtproto-api-id');\n  const mtprotoAPIHash = document.getElementById('mtproto-api-hash');\n  const mtprotoPhone = document.getElementById('mtproto-phone');\n  const btnMTProtoSetup = document.getElementById('btn-mtproto-setup');\n  const btnMTProtoRequestCode = document.getElementById('btn-mtproto-request-code');\n  const mtprotoCodeGroup = document.getElementById('mtproto-code-group');\n  const mtprotoCode = document.getElementById('mtproto-code');\n  const btnMTProtoCode = document.getElementById('btn-mtproto-code');\n  const mtprotoPasswordGroup = document.getElementById('mtproto-password-group');\n  const mtprotoPassword = document.getElementById('mtproto-password');\n  const btnMTProtoPassword = document.getElementById('btn-mtproto-password');\n  const mtprotoBackfillGroup = document.getElementById('mtproto-backfill-group');\n  const mtprotoBackfillEndpoint = document.getElementById('mtproto-backfill-endpoint');\n  const mtprotoBackfillEvents = document.getElementById('mtproto-backfill-events');\n  const mtprotoBackfillHours = document.getElementById('mtproto-backfill-hours');\n  const btnMTProtoBackfill = document.getElementById('btn-mtproto-backfill');\n  const btnMTProtoLogout = document.getElementById('btn-mtproto-logout');\n\n  const modalDiscovery        = document.getElementById('modal-discovery');\n''','mtproto dom refs')

# ---------- Connection cards: mode/capability-aware Telegram state and actions. ----------
rep('internal/api/web/js/app-base.js',
'''        } else if (conn.transport === 'telegram') {\n          if (st.running) {\n            const privacyDisabled = st.privacyModeEnabled === false;\n            statusBadgeClass = privacyDisabled ? 'badge-success' : 'badge-warning';\n            statusText = 'Running';\n            nextAction = !privacyDisabled ? 'Disable Bot Privacy Mode via @BotFather to receive group messages.' : (connEps.length === 0 ? 'Discover observed group chats.' : 'Ready to sync.');\n            detailHtml = `Long polling active. Bot Privacy Mode: ${privacyDisabled ? 'Disabled (can read group messages)' : 'Enabled (may miss group messages)'}.`;\n          } else {\n            statusBadgeClass = 'badge-neutral';\n            statusText = 'Stopped';\n            nextAction = 'Polling stopped. Check token or enable connection.';\n            detailHtml = 'Bot is not running.';\n          }\n        }\n''',
'''        } else if (conn.transport === 'telegram') {\n          const caps = conn.capabilities || st.capabilities || {};\n          if (caps.historyRecovery) {\n            const connected = st.status === 'connected';\n            statusBadgeClass = connected ? 'badge-success' : (st.status === 'error' ? 'badge-danger' : 'badge-warning');\n            statusText = connected ? 'Connected' : (st.status || 'Disconnected');\n            nextAction = connected ? (connEps.length === 0 ? 'Discover joined groups.' : 'Ready to sync; bounded history is available.') : 'Complete or resume phone-account authentication.';\n            detailHtml = connected ? 'MTProto user session active. Full group/topic discovery is available.' : 'Phone / MTProto connection is not authorized yet.';\n          } else if (st.running) {\n            const privacyApplicable = caps.privacyModeStatus === true;\n            const privacyDisabled = st.privacyModeEnabled === false;\n            statusBadgeClass = !privacyApplicable || privacyDisabled ? 'badge-success' : 'badge-warning';\n            statusText = 'Running';\n            nextAction = privacyApplicable && !privacyDisabled ? 'Adjust Bot Privacy Mode/admin visibility to observe ordinary group messages.' : (connEps.length === 0 ? 'Discover observed group chats.' : 'Ready to sync.');\n            detailHtml = privacyApplicable ? `Bot API polling active. Bot Privacy Mode: ${privacyDisabled ? 'Disabled' : 'Enabled'}.` : 'Telegram connection running.';\n          } else {\n            statusBadgeClass = 'badge-neutral';\n            statusText = 'Stopped';\n            nextAction = 'Check credentials or enable the connection.';\n            detailHtml = 'Telegram connection is not running.';\n          }\n        }\n''','telegram capability status')

rep('internal/api/web/js/app-base.js',
'''        let actionButtons = `<button class="btn btn-ghost btn-sm" onclick="window.App.openDiscovery('${escapeHtml(conn.id)}')">\n          <svg class="icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="11" cy="11" r="8"></circle><line x1="21" y1="21" x2="16.65" y2="16.65"></line></svg>\n          Discover\n        </button>`;\n''',
'''        const caps = conn.capabilities || st.capabilities || {};\n        const discoveryLabel = conn.transport === 'telegram' && caps.chatDiscovery === 'full' ? 'Discover All' : 'Discover';\n        let actionButtons = `<button class="btn btn-ghost btn-sm" onclick="window.App.openDiscovery('${escapeHtml(conn.id)}')">\n          <svg class="icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="11" cy="11" r="8"></circle><line x1="21" y1="21" x2="16.65" y2="16.65"></line></svg>\n          ${discoveryLabel}\n        </button>`;\n''','capability discovery label')
rep('internal/api/web/js/app-base.js',
'''          if (conn.transport === 'whatsapp') {\n            actionButtons += `<button class="btn btn-ghost btn-sm admin-only" onclick="window.App.pairWhatsApp('${escapeHtml(conn.id)}')">\n              Pair\n            </button>`;\n            actionButtons += `<button class="btn btn-ghost btn-sm danger admin-only" onclick="window.App.logoutWhatsApp('${escapeHtml(conn.id)}')">\n              Logout\n            </button>`;\n          } else {\n            actionButtons += `<button class="btn btn-ghost btn-sm admin-only" onclick="window.App.openReplaceTokenModal('${escapeHtml(conn.id)}', '${escapeHtml(conn.label)}')">\n              Replace Token\n            </button>`;\n          }\n''',
'''          if (conn.transport === 'whatsapp') {\n            actionButtons += `<button class="btn btn-ghost btn-sm admin-only" onclick="window.App.pairWhatsApp('${escapeHtml(conn.id)}')">Pair</button>`;\n            actionButtons += `<button class="btn btn-ghost btn-sm danger admin-only" onclick="window.App.logoutWhatsApp('${escapeHtml(conn.id)}')">Logout</button>`;\n          } else if (conn.transport === 'telegram' && caps.historyRecovery) {\n            actionButtons += `<button class="btn btn-ghost btn-sm admin-only" onclick="window.App.openTelegramMTProto('${escapeHtml(conn.id)}')">Manage Login</button>`;\n          } else {\n            actionButtons += `<button class="btn btn-ghost btn-sm admin-only" onclick="window.App.openReplaceTokenModal('${escapeHtml(conn.id)}', '${escapeHtml(conn.label)}')">Replace Token</button>`;\n          }\n''','mode-specific actions')
rep('internal/api/web/js/app-base.js',
'''              <span class="badge ${conn.transport === 'discord' ? 'badge-discord' : conn.transport === 'telegram' ? 'badge-telegram' : 'badge-wa'}">${escapeHtml(conn.transport)}</span>\n              <span class="badge ${conn.enabled ? 'badge-primary' : 'badge-neutral'}">${conn.enabled ? 'Enabled' : 'Disabled'}</span>\n''',
'''              <span class="badge ${conn.transport === 'discord' ? 'badge-discord' : conn.transport === 'telegram' ? 'badge-telegram' : 'badge-wa'}">${escapeHtml(conn.transport)}</span>\n              ${conn.transport === 'telegram' ? `<span class="badge badge-neutral">${escapeHtml(conn.integrationMode === 'mtproto' ? 'Phone / MTProto' : 'Bot API')}</span>` : ''}\n              <span class="badge ${conn.enabled ? 'badge-primary' : 'badge-neutral'}">${conn.enabled ? 'Enabled' : 'Disabled'}</span>\n''','mode badge')

# ---------- Creation form behavior. ----------
rep('internal/api/web/js/app-base.js',
'''    if (addConnToken) addConnToken.value = '';\n    selectAddConnTransport('whatsapp');\n''',
'''    if (addConnToken) addConnToken.value = '';\n    if (addConnTelegramMode) addConnTelegramMode.value = 'bot';\n    if (addConnMTProtoAPIID) addConnMTProtoAPIID.value = '';\n    if (addConnMTProtoAPIHash) addConnMTProtoAPIHash.value = '';\n    if (addConnMTProtoPhone) addConnMTProtoPhone.value = '';\n    selectAddConnTransport('whatsapp');\n''','reset creation secrets')

rep('internal/api/web/js/app-base.js',
'''  function selectAddConnTransport(transport) {\n    addConnTransport = transport;\n''',
'''  function selectAddTelegramMode(mode) {\n    const isMTProto = addConnTransport === 'telegram' && mode === 'mtproto';\n    if (addConnTokenGroup) addConnTokenGroup.classList.toggle('hidden', addConnTransport === 'whatsapp' || isMTProto);\n    if (addConnMTProtoGroup) addConnMTProtoGroup.classList.toggle('hidden', !isMTProto);\n    if (addConnHelpTelegramBot) addConnHelpTelegramBot.classList.toggle('hidden', isMTProto);\n    if (addConnHelpTelegramMTProto) addConnHelpTelegramMTProto.classList.toggle('hidden', !isMTProto);\n    if (addConnToken) addConnToken.required = addConnTransport !== 'whatsapp' && !isMTProto;\n    if (addConnMTProtoAPIID) addConnMTProtoAPIID.required = isMTProto;\n    if (addConnMTProtoAPIHash) addConnMTProtoAPIHash.required = isMTProto;\n    if (addConnMTProtoPhone) addConnMTProtoPhone.required = isMTProto;\n  }\n\n  function selectAddConnTransport(transport) {\n    addConnTransport = transport;\n''','telegram mode helper')
rep('internal/api/web/js/app-base.js',
'''    if (addConnTokenGroup) {\n      addConnTokenGroup.classList.toggle('hidden', transport === 'whatsapp');\n    }\n    if (addConnHelpDiscord) addConnHelpDiscord.classList.toggle('hidden', transport !== 'discord');\n''',
'''    if (addConnTelegramModeGroup) addConnTelegramModeGroup.classList.toggle('hidden', transport !== 'telegram');\n    if (addConnHelpDiscord) addConnHelpDiscord.classList.toggle('hidden', transport !== 'discord');\n''','transport mode group')
rep('internal/api/web/js/app-base.js',
'''    if (addConnToken) {\n      addConnToken.required = transport !== 'whatsapp';\n      addConnToken.value = '';\n    }\n  }\n''',
'''    if (addConnToken) addConnToken.value = '';\n    selectAddTelegramMode(addConnTelegramMode ? addConnTelegramMode.value : 'bot');\n  }\n''','transport token requirement')

# Replace submit handler wholesale through next function marker.
p=root/'internal/api/web/js/app-base.js'; s=p.read_text(); start=s.index('  async function handleAddConnectionSubmit(e) {'); end=s.index('  function openReplaceTokenModal', start)
new_submit=r'''  async function handleAddConnectionSubmit(e) {
    e.preventDefault();
    const label = addConnLabel ? addConnLabel.value.trim() : '';
    const id = addConnId ? addConnId.value.trim() : '';
    let token = addConnToken ? addConnToken.value.trim() : '';
    const telegramMode = addConnTransport === 'telegram' && addConnTelegramMode ? addConnTelegramMode.value : 'bot';
    const isMTProto = addConnTransport === 'telegram' && telegramMode === 'mtproto';
    const apiId = isMTProto && addConnMTProtoAPIID ? parseInt(addConnMTProtoAPIID.value, 10) : 0;
    let apiHash = isMTProto && addConnMTProtoAPIHash ? addConnMTProtoAPIHash.value.trim() : '';
    let phone = isMTProto && addConnMTProtoPhone ? addConnMTProtoPhone.value.trim() : '';

    if (addConnToken) addConnToken.value = '';
    if (addConnMTProtoAPIHash) addConnMTProtoAPIHash.value = '';
    if (addConnMTProtoPhone) addConnMTProtoPhone.value = '';

    if (!label) {
      if (addConnAlert) { addConnAlert.textContent = 'Connection label is required.'; addConnAlert.classList.remove('hidden'); }
      return;
    }
    if (isMTProto && (!Number.isInteger(apiId) || apiId <= 0 || !apiHash || !phone)) {
      if (addConnAlert) { addConnAlert.textContent = 'API ID, API hash, and phone are required for Phone / MTProto.'; addConnAlert.classList.remove('hidden'); }
      return;
    }
    if (!isMTProto && addConnTransport !== 'whatsapp' && !token) {
      if (addConnAlert) { addConnAlert.textContent = 'Bot token is required.'; addConnAlert.classList.remove('hidden'); }
      return;
    }

    setButtonLoading(btnSubmitAddConn, true);
    try {
      const payload = { transport: addConnTransport, label, enabled: true };
      if (id) payload.id = id;
      if (addConnTransport === 'telegram') payload.integrationMode = telegramMode;
      if (!isMTProto && addConnTransport !== 'whatsapp') payload.token = token;

      const created = await window.API.createConnection(payload);
      const createdID = created && created.id ? created.id : id;
      if (!createdID) throw new Error('created connection did not return an ID');
      if (addConnTransport === 'whatsapp') {
        await loadConnections();
        await requestWaPairing(createdID, true);
      } else if (isMTProto) {
        await window.API.setupTelegramMTProto(createdID, apiId, apiHash, phone);
        apiHash = ''; phone = ''; token = '';
        await window.API.requestTelegramMTProtoCode(createdID);
        closeModal(modalAddConnection);
        await loadConnections();
        await openTelegramMTProto(createdID);
      } else {
        closeModal(modalAddConnection);
        await loadConnections();
      }
    } catch (err) {
      if (addConnAlert) {
        addConnAlert.textContent = err.message || 'Failed to create connection';
        addConnAlert.classList.remove('hidden');
      }
    } finally {
      token = ''; apiHash = ''; phone = '';
      setButtonLoading(btnSubmitAddConn, false);
      if (addConnToken) addConnToken.value = '';
      if (addConnMTProtoAPIHash) addConnMTProtoAPIHash.value = '';
      if (addConnMTProtoPhone) addConnMTProtoPhone.value = '';
    }
  }

'''
p.write_text(s[:start]+new_submit+s[end:])

# ---------- Full MTProto modal behavior before token replacement handler. ----------
p=root/'internal/api/web/js/app-base.js'; s=p.read_text(); marker='  function openReplaceTokenModal(connId, label) {'
insert=r'''  let mtprotoConnId = '';

  function mtprotoCapabilities() {
    const conn = (cachedConnections || []).find(c => c.id === mtprotoConnId);
    return conn && conn.capabilities ? conn.capabilities : {};
  }

  function renderMTProtoStatus(status) {
    const state = status && status.status ? status.status : 'disconnected';
    const configured = Boolean(status && status.configured);
    const connected = state === 'connected';
    if (mtprotoStatus) {
      mtprotoStatus.textContent = state.replaceAll('_', ' ');
      mtprotoStatus.className = `badge ${connected ? 'badge-success' : state === 'error' ? 'badge-danger' : 'badge-warning'}`;
    }
    if (mtprotoSetupGroup) mtprotoSetupGroup.classList.toggle('hidden', configured);
    if (btnMTProtoRequestCode) btnMTProtoRequestCode.classList.toggle('hidden', !configured || connected || state === 'password_required');
    if (mtprotoCodeGroup) mtprotoCodeGroup.classList.toggle('hidden', state !== 'code_required');
    if (mtprotoPasswordGroup) mtprotoPasswordGroup.classList.toggle('hidden', state !== 'password_required');
    if (btnMTProtoLogout) btnMTProtoLogout.classList.toggle('hidden', !configured);
    const caps = mtprotoCapabilities();
    if (mtprotoBackfillGroup) mtprotoBackfillGroup.classList.toggle('hidden', !connected || caps.historyRecovery !== true);
    if (mtprotoBackfillEndpoint) {
      const eps = (cachedEndpoints || []).filter(e => e.connectionId === mtprotoConnId && e.transport === 'telegram');
      mtprotoBackfillEndpoint.innerHTML = eps.length ? eps.map(e => `<option value="${escapeHtml(e.alias)}">${escapeHtml(e.alias)}</option>`).join('') : '<option value="">No endpoints configured</option>';
    }
  }

  async function refreshMTProtoModal() {
    if (!mtprotoConnId) return;
    try {
      const status = await window.API.getConnectionStatus(mtprotoConnId);
      renderMTProtoStatus(status || {});
      if (mtprotoAlert) mtprotoAlert.classList.add('hidden');
    } catch (err) {
      if (mtprotoAlert) { mtprotoAlert.textContent = err.message || 'Unable to read Telegram session status.'; mtprotoAlert.className = 'alert alert-danger'; mtprotoAlert.classList.remove('hidden'); }
    }
  }

  async function openTelegramMTProto(connId) {
    const conn = (cachedConnections || []).find(c => c.id === connId);
    if (!conn || conn.transport !== 'telegram' || !(conn.capabilities && conn.capabilities.historyRecovery)) {
      showToast('This connection does not support the Phone / MTProto session flow.', 'danger');
      return;
    }
    mtprotoConnId = connId;
    if (mtprotoConnInfo) mtprotoConnInfo.textContent = `${conn.label} (${conn.id}) · Phone / MTProto`;
    if (mtprotoAlert) mtprotoAlert.classList.add('hidden');
    if (mtprotoAPIID) mtprotoAPIID.value = '';
    if (mtprotoAPIHash) mtprotoAPIHash.value = '';
    if (mtprotoPhone) mtprotoPhone.value = '';
    if (mtprotoCode) mtprotoCode.value = '';
    if (mtprotoPassword) mtprotoPassword.value = '';
    openModal(modalTelegramMTProto);
    await refreshMTProtoModal();
  }

  async function setupMTProtoSession() {
    const apiId = parseInt(mtprotoAPIID ? mtprotoAPIID.value : '', 10);
    let apiHash = mtprotoAPIHash ? mtprotoAPIHash.value.trim() : '';
    let phone = mtprotoPhone ? mtprotoPhone.value.trim() : '';
    if (mtprotoAPIHash) mtprotoAPIHash.value = '';
    if (mtprotoPhone) mtprotoPhone.value = '';
    if (!mtprotoConnId || !Number.isInteger(apiId) || apiId <= 0 || !apiHash || !phone) {
      if (mtprotoAlert) { mtprotoAlert.textContent = 'API ID, API hash, and phone are required.'; mtprotoAlert.className = 'alert alert-danger'; mtprotoAlert.classList.remove('hidden'); }
      return;
    }
    setButtonLoading(btnMTProtoSetup, true);
    try {
      await window.API.setupTelegramMTProto(mtprotoConnId, apiId, apiHash, phone);
      apiHash = ''; phone = '';
      await window.API.requestTelegramMTProtoCode(mtprotoConnId);
      await refreshMTProtoModal();
    } catch (err) {
      if (mtprotoAlert) { mtprotoAlert.textContent = err.message || 'Telegram setup failed.'; mtprotoAlert.className = 'alert alert-danger'; mtprotoAlert.classList.remove('hidden'); }
    } finally {
      apiHash = ''; phone = '';
      setButtonLoading(btnMTProtoSetup, false);
    }
  }

  async function requestMTProtoCode() {
    if (!mtprotoConnId) return;
    setButtonLoading(btnMTProtoRequestCode, true);
    try { await window.API.requestTelegramMTProtoCode(mtprotoConnId); await refreshMTProtoModal(); }
    catch (err) { if (mtprotoAlert) { mtprotoAlert.textContent = err.message || 'Could not request login code.'; mtprotoAlert.className='alert alert-danger'; mtprotoAlert.classList.remove('hidden'); } }
    finally { setButtonLoading(btnMTProtoRequestCode, false); }
  }

  async function submitMTProtoCode() {
    let code = mtprotoCode ? mtprotoCode.value.trim() : '';
    if (mtprotoCode) mtprotoCode.value = '';
    if (!code) return;
    setButtonLoading(btnMTProtoCode, true);
    try { await window.API.submitTelegramMTProtoCode(mtprotoConnId, code); code=''; await refreshMTProtoModal(); }
    catch (err) { if (mtprotoAlert) { mtprotoAlert.textContent = err.message || 'Telegram login code was rejected.'; mtprotoAlert.className='alert alert-danger'; mtprotoAlert.classList.remove('hidden'); } }
    finally { code=''; setButtonLoading(btnMTProtoCode, false); }
  }

  async function submitMTProtoPassword() {
    let password = mtprotoPassword ? mtprotoPassword.value : '';
    if (mtprotoPassword) mtprotoPassword.value = '';
    if (!password) return;
    setButtonLoading(btnMTProtoPassword, true);
    try { await window.API.submitTelegramMTProtoPassword(mtprotoConnId, password); password=''; await refreshMTProtoModal(); await loadConnections(); }
    catch (err) { if (mtprotoAlert) { mtprotoAlert.textContent = err.message || 'Telegram 2FA password was rejected.'; mtprotoAlert.className='alert alert-danger'; mtprotoAlert.classList.remove('hidden'); } }
    finally { password=''; setButtonLoading(btnMTProtoPassword, false); }
  }

  async function logoutMTProtoSession() {
    if (!mtprotoConnId) return;
    try { await window.API.logoutTelegramMTProto(mtprotoConnId); await refreshMTProtoModal(); await loadConnections(); }
    catch (err) { showToast(err.message || 'Failed to log out Telegram session.', 'danger'); }
  }

  async function runMTProtoBackfill() {
    const endpoint = mtprotoBackfillEndpoint ? mtprotoBackfillEndpoint.value : '';
    const maxEvents = parseInt(mtprotoBackfillEvents ? mtprotoBackfillEvents.value : '200', 10);
    const maxAgeHours = parseInt(mtprotoBackfillHours ? mtprotoBackfillHours.value : '24', 10);
    if (!endpoint || !Number.isInteger(maxEvents) || maxEvents < 1 || maxEvents > 1000 || !Number.isInteger(maxAgeHours) || maxAgeHours < 1 || maxAgeHours > 720) {
      showToast('Select an endpoint and valid bounded history limits.', 'danger'); return;
    }
    setButtonLoading(btnMTProtoBackfill, true);
    try { await window.API.backfillTelegramMTProto(mtprotoConnId, endpoint, maxEvents, maxAgeHours); }
    catch (err) { showToast(err.message || 'Telegram history backfill failed.', 'danger'); }
    finally { setButtonLoading(btnMTProtoBackfill, false); }
  }

  async function showTelegramTopics(connId, remoteId, title) {
    const conn = (cachedConnections || []).find(c => c.id === connId);
    const caps = conn && conn.capabilities ? conn.capabilities : {};
    if (caps.topicDiscovery !== 'full') return;
    try {
      const topics = await window.API.getTelegramTopics(connId, remoteId);
      const items = Array.isArray(topics) ? topics : [];
      if (discoveryAlert) {
        discoveryAlert.className = 'alert alert-info';
        discoveryAlert.innerHTML = `<strong>${escapeHtml(title || remoteId)} topics:</strong> ` + (items.length ? items.map(t => `${escapeHtml(t.label || (t.general ? 'General' : 'Topic'))} <code>${escapeHtml(t.remoteId)}</code>`).join(' · ') : 'No forum topics found.');
        discoveryAlert.classList.remove('hidden');
      }
    } catch (err) { showToast(err.message || 'Topic discovery failed.', 'danger'); }
  }

'''
if marker not in s: raise SystemExit('missing mtproto function insertion marker')
p.write_text(s.replace(marker, insert+marker,1))

# Discovery messaging/actions from capabilities.
rep('internal/api/web/js/app-base.js',
'''          } else {\n            if (discoveryEmptyTitle) discoveryEmptyTitle.textContent = 'No Observed Telegram Chats';\n            if (discoveryEmptyDesc) discoveryEmptyDesc.textContent = 'Send a message in a group where the bot is a member (with Bot Privacy Mode disabled), then refresh.';\n          }\n''',
'''          } else {\n            const caps = conn && conn.capabilities ? conn.capabilities : {};\n            if (discoveryEmptyTitle) discoveryEmptyTitle.textContent = caps.chatDiscovery === 'full' ? 'No Joined Telegram Groups' : 'No Observed Telegram Chats';\n            if (discoveryEmptyDesc) discoveryEmptyDesc.textContent = caps.chatDiscovery === 'full' ? 'The authenticated phone account has no joined supported groups/supergroups.' : 'Send a message in a group the bot can observe, then refresh discovery. Bot Privacy Mode/admin visibility may limit observation.';\n          }\n''','capability discovery empty state')
rep('internal/api/web/js/app-base.js',
'''          const actionBtn = existingEp\n            ? `<button class="btn btn-ghost btn-sm" disabled>Added</button>`\n            : `<button class="btn btn-primary btn-sm" onclick="window.App.selectDiscoveredTarget(${index})">Add Endpoint</button>`;\n\n          return `<tr>\n''',
'''          const actionBtn = existingEp\n            ? `<button class="btn btn-ghost btn-sm" disabled>Added</button>`\n            : `<button class="btn btn-primary btn-sm" onclick="window.App.selectDiscoveredTarget(${index})">Add Endpoint</button>`;\n          const caps = conn && conn.capabilities ? conn.capabilities : {};\n          const topicsBtn = conn && conn.transport === 'telegram' && item.forum && caps.topicDiscovery === 'full'\n            ? `<button class="btn btn-ghost btn-sm" onclick="window.App.showTelegramTopics('${escapeHtml(conn.id)}','${escapeHtml(remoteId)}','${escapeHtml(item.title || remoteId)}')">Topics</button>` : '';\n\n          return `<tr>\n''','topic action')
rep('internal/api/web/js/app-base.js',
'''            <td style="text-align:right;">${statusText} ${actionBtn}</td>\n''',
'''            <td style="text-align:right;">${statusText} ${topicsBtn} ${actionBtn}</td>\n''','topic button render')

# Globals and event wiring.
rep('internal/api/web/js/app-base.js',
'''    openReplaceTokenModal,\n    toggleConnection,\n''',
'''    openReplaceTokenModal,\n    openTelegramMTProto,\n    showTelegramTopics,\n    toggleConnection,\n''','expose mtproto functions')
rep('internal/api/web/js/app-base.js',
'''    if (formReplaceToken) formReplaceToken.addEventListener('submit', handleReplaceTokenSubmit);\n''',
'''    if (formReplaceToken) formReplaceToken.addEventListener('submit', handleReplaceTokenSubmit);\n    if (addConnTelegramMode) addConnTelegramMode.addEventListener('change', () => selectAddTelegramMode(addConnTelegramMode.value));\n    if (btnMTProtoSetup) btnMTProtoSetup.addEventListener('click', setupMTProtoSession);\n    if (btnMTProtoRequestCode) btnMTProtoRequestCode.addEventListener('click', requestMTProtoCode);\n    if (btnMTProtoCode) btnMTProtoCode.addEventListener('click', submitMTProtoCode);\n    if (btnMTProtoPassword) btnMTProtoPassword.addEventListener('click', submitMTProtoPassword);\n    if (btnMTProtoLogout) btnMTProtoLogout.addEventListener('click', logoutMTProtoSession);\n    if (btnMTProtoBackfill) btnMTProtoBackfill.addEventListener('click', runMTProtoBackfill);\n''','wire mtproto ui')

# ---------- Tests: UI smoke, immutability/delete, logout scrubbing, log privacy. ----------
rep('internal/api/static_test.go',
'''\t\t\t`id="add-conn-help-telegram"`,\n\t\t\t`id="add-conn-wa-help"`,\n''',
'''\t\t\t`id="add-conn-help-telegram"`,\n\t\t\t`id="add-conn-telegram-mode"`,\n\t\t\t`id="add-conn-mtproto-api-id"`,\n\t\t\t`id="add-conn-mtproto-api-hash"`,\n\t\t\t`id="add-conn-mtproto-phone"`,\n\t\t\t`id="modal-telegram-mtproto"`,\n\t\t\t`id="mtproto-code"`,\n\t\t\t`id="mtproto-password"`,\n\t\t\t`id="mtproto-backfill-group"`,\n\t\t\t`id="add-conn-wa-help"`,\n''','static html mtproto markers')
rep('internal/api/static_test.go',
'''\t\tif strings.Contains(appJS, "closeModal(modalAddConnection);\\n        await openWaPairModal(createdID)") {\n\t\t\tt.Fatal("WhatsApp create flow still leaves Add Connection before pairing")\n\t\t}\n''',
'''\t\tif strings.Contains(appJS, "closeModal(modalAddConnection);\\n        await openWaPairModal(createdID)") {\n\t\t\tt.Fatal("WhatsApp create flow still leaves Add Connection before pairing")\n\t\t}\n\n\t\tresp, err = client.Get(ts.URL + "/js/app-base.js")\n\t\tif err != nil { t.Fatalf("GET /js/app-base.js failed: %v", err) }\n\t\tbaseBytes, _ := io.ReadAll(resp.Body); resp.Body.Close(); baseJS := string(baseBytes)\n\t\tfor _, expected := range []string{"selectAddTelegramMode", "openTelegramMTProto", "setupTelegramMTProto", "submitTelegramMTProtoCode", "submitTelegramMTProtoPassword", "backfillTelegramMTProto", "caps.historyRecovery", "caps.topicDiscovery"} {\n\t\t\tif !strings.Contains(baseJS, expected) { t.Fatalf("Admin base JS missing Telegram dual-mode symbol %q", expected) }\n\t\t}\n\t\tfor _, forbidden := range []string{"localStorage.setItem('apiHash'", "localStorage.setItem('phone'", "localStorage.setItem('code'", "localStorage.setItem('password'", "sessionStorage.setItem('apiHash'", "sessionStorage.setItem('phone'"} {\n\t\t\tif strings.Contains(baseJS, forbidden) { t.Fatalf("Admin JS persists MTProto secret %q", forbidden) }\n\t\t}\n''','static mtproto js checks')

append_once('internal/api/connections_test.go','TestConnections_TelegramIntegrationModeIsImmutableAndDeleteRemovesState',r'''

func TestConnections_TelegramIntegrationModeIsImmutableAndDeleteRemovesState(t *testing.T) {
    srv, _, db, adminToken, _ := setupConnectionsTestEnv(t)
    create := authenticatedConnectionRequest(t, srv, adminToken, http.MethodPost, "/api/connections", `{"id":"conn-mt-hard","transport":"telegram","integrationMode":"mtproto","label":"Phone Telegram"}`)
    if create.Code != http.StatusCreated { t.Fatalf("create MTProto: %d %s", create.Code, create.Body.String()) }
    patch := authenticatedConnectionRequest(t, srv, adminToken, http.MethodPatch, "/api/connections/conn-mt-hard", `{"integrationMode":"bot"}`)
    if patch.Code != http.StatusConflict { t.Fatalf("mode change status=%d body=%s", patch.Code, patch.Body.String()) }
    var mode string
    if err := db.QueryRow(`SELECT integration_mode FROM transport_connections WHERE id='conn-mt-hard'`).Scan(&mode); err != nil || mode != "mtproto" { t.Fatalf("mode=%q err=%v", mode, err) }
    del := authenticatedConnectionRequest(t, srv, adminToken, http.MethodDelete, "/api/connections/conn-mt-hard", "")
    if del.Code != http.StatusOK { t.Fatalf("delete status=%d body=%s", del.Code, del.Body.String()) }
    var count int
    if err := db.QueryRow(`SELECT count(*) FROM transport_connections WHERE id='conn-mt-hard'`).Scan(&count); err != nil || count != 0 { t.Fatalf("deleted MTProto state remains count=%d err=%v", count, err) }
}
''')

# Strengthen auth lifecycle logout assertions with peer + poll correlation.
rep('internal/transport/telegram/mtproto_test.go',
'''\tstate.Session = []byte("reusable-session")\n\tif err := a.state.store(ctx, state); err != nil {\n''',
'''\tstate.Session = []byte("reusable-session")\n\tstate.Peers = map[string]mtprotoPeerState{"-1000000000042": {RemoteID: "-1000000000042", Kind: "channel", ID: 42, AccessHash: 9}}\n\tstate.Polls = map[string]mtprotoPollState{"99": {RemoteID: "-1000000000042", MessageID: 7, OptionKeys: []string{"MA", "MQ"}}}\n\tif err := a.state.store(ctx, state); err != nil {\n''','seed account-bound state')
rep('internal/transport/telegram/mtproto_test.go',
'''\tif len(cleared.Session) != 0 {\n\t\tt.Fatal("logout retained reusable session")\n\t}\n}\n''',
'''\tif len(cleared.Session) != 0 {\n\t\tt.Fatal("logout retained reusable session")\n\t}\n\tif len(cleared.Peers) != 0 || len(cleared.Polls) != 0 {\n\t\tt.Fatalf("logout retained account-bound peer/poll state: peers=%d polls=%d", len(cleared.Peers), len(cleared.Polls))\n\t}\n\tif b.live != nil && len(b.live.peerSnapshot()) != 0 {\n\t\tt.Fatal("logout retained live peer cache")\n\t}\n}\n''','assert logout scrubbing')

# Capture handler logs and ensure auth secrets are not emitted.
rep('internal/api/telegram_mtproto_test.go',
'''import (\n\t"context"\n\t"encoding/json"\n\t"net/http"\n\t"testing"\n)\n''',
'''import (\n\t"bytes"\n\t"context"\n\t"encoding/json"\n\t"log/slog"\n\t"net/http"\n\t"testing"\n)\n''','mtproto api test imports')
rep('internal/api/telegram_mtproto_test.go',
'''func TestMTProtoAdminAPIFlow(t *testing.T) {\n\tsrv, _, db, token, _ := setupConnectionsTestEnv(t)\n''',
'''func TestMTProtoAdminAPIFlow(t *testing.T) {\n\tsrv, _, db, token, _ := setupConnectionsTestEnv(t)\n\tvar logs bytes.Buffer\n\tsrv.logger = slog.New(slog.NewTextHandler(&logs, nil))\n''','capture mtproto logs')
rep('internal/api/telegram_mtproto_test.go',
'''\t\tif containsString(body, "super-secret") || containsString(body, "+1555") || containsString(body, "top-secret") || containsString(body, "12345") {\n\t\t\tt.Fatalf("secret reflected in response %q", body)\n\t\t}\n\t}\n}\n''',
'''\t\tif containsString(body, "super-secret") || containsString(body, "+1555") || containsString(body, "top-secret") || containsString(body, "12345") {\n\t\t\tt.Fatalf("secret reflected in response %q", body)\n\t\t}\n\t}\n\tlogText := logs.String()\n\tfor _, secret := range []string{"super-secret", "+15551234567", "top-secret", "12345"} {\n\t\tif containsString(logText, secret) { t.Fatalf("MTProto auth secret leaked to logs: %q", secret) }\n\t}\n}\n''','assert mtproto logs private')

# ---------- Permanent Telegram documentation. ----------
(root/'docs/TELEGRAM.md').write_text(r'''# Telegram integrations

A Telegram connection uses exactly one integration method: **Bot API** or **Phone / MTProto**. Both appear to the router as the canonical `telegram` transport. There is no automatic fallback between methods, and an endpoint always names one `connection_id`, so it can use only that connection's integration method.

## Choosing a method

| Capability | Bot API | Phone / MTProto |
|---|---|---|
| Authentication | BotFather token | Telegram API ID/hash + phone login code + optional 2FA |
| Group discovery | Observed groups only | Complete joined supported groups/supergroups |
| Forum-topic discovery | Observed topics | Complete forum-topic enumeration |
| Bot Privacy Mode | Applicable; may restrict ordinary group updates | Not applicable |
| Bounded history recovery | No arbitrary Telegram history reads | Yes, through the normal recovery coordinator |
| Text/media/replies/topics | Yes | Yes |
| Reactions/edits/deletes | Yes | Yes |
| Native polls + aggregate live results | Yes | Yes |
| Private DMs | Not supported | Not supported |
| Broadcast channels | Not supported by current endpoint policy | Not supported by current endpoint policy |

Use **Bot API** when a bot identity and observation-based discovery are sufficient. Use **Phone / MTProto** when the deployment needs complete joined-group/topic discovery or bounded recovery of missed ordinary messages. Telegram account automation can be subject to Telegram terms and anti-abuse controls; use an account and API application appropriate for the deployment.

## Bot API setup

1. Create a bot with the official **@BotFather** and obtain its token.
2. In **Connections**, create a Telegram connection and choose **Bot API**.
3. Paste the token once. The server encrypts it in `control.db`; read APIs never return it.
4. Add the bot to intended groups/supergroups. Bot Privacy Mode and administrator visibility determine which ordinary group updates are observable.
5. Send a group message, then use **Discover**. Discovery only lists chats the bot has actually observed.

If ordinary group messages are missing, review Bot Privacy Mode and bot administrator permissions. This guidance is intentionally not shown for Phone / MTProto connections.

## Phone / MTProto setup

Create a Telegram API application through Telegram's official application-development portal and obtain an **API ID** and **API hash**. Then:

1. In **Connections**, create a Telegram connection and choose **Phone / MTProto**.
2. Enter API ID, API hash, and the account phone number. These values are sent only to the authenticated local management API.
3. Request the login code and enter the one-time code delivered by Telegram.
4. If Telegram reports that two-step verification is required, enter the account's 2FA password.
5. When the connection state is **connected**, use **Discover All** to enumerate joined supported groups. Forum groups expose a **Topics** action.
6. The **Manage Login** dialog can log the Telegram session out or run an explicitly bounded history backfill for a configured endpoint.

A logged-out MTProto connection retains its encrypted API application configuration/phone so a new code can be requested, but the reusable Telegram session, peer/access-hash cache, and poll-correlation cache are cleared.

## Security model

MTProto session material is stored only in the connection's encrypted credential blob in mode-`0600` `control.db`, using the same credential cipher derived from `IDENTITY_SECRET`. The reusable gotd session, API hash, phone number, peer access hashes, and operational poll correlation are inside that encrypted boundary.

One-time login codes, the temporary phone-code hash, and 2FA passwords are **not persisted**. The web UI clears secret input elements after submission. The server sanitizes authentication failures and must not log raw Telegram objects, credentials, phone numbers, message bodies, OTPs, 2FA passwords, session/auth keys, or API hashes. `/data/sync.db` remains the content-free canonical routing store and does not contain MTProto authentication state.

Keep `IDENTITY_SECRET` stable and protect backups of `control.db`. Losing the secret makes encrypted connection credentials unusable; exposing both the secret and `control.db` exposes provider credentials/session state.

## Discovery, topics, and history

The management API returns capabilities with each Telegram connection. The UI uses those capability flags instead of assuming all Telegram connections behave alike:

- `chatDiscovery=observed` vs `full`
- `topicDiscovery=observed` vs `full`
- `historyRecovery=false` vs `true`
- `privacyModeStatus=true` only where Bot Privacy Mode is relevant
- `polls=true` when native canonical poll support is available

MTProto history is always bounded by maximum event count and maximum age and is normalized through the existing `transport.RecoverySource` coordinator. It is not an account-export facility. Live ingress and recovered events share connection-scoped recovery stream keys/cursors, so overlap is idempotent.

## Endpoint exclusivity

An endpoint stores a transport, one `connection_id`, and one provider remote ID. The server rejects a connection whose transport does not match the endpoint transport. Changing a Telegram connection from Bot API to MTProto (or the reverse) in place is deliberately rejected: create another connection, validate/discover the target there, and explicitly reassign the endpoint.

This keeps bot/user sessions, peer caches, poll provider references, recovery cursors, and loop-prevention state connection-scoped and prevents silent fallback between Telegram integration methods.

## Troubleshooting

- **Bot discovery is empty:** make sure the bot is a group member and has received a group update; review Bot Privacy Mode/admin visibility.
- **MTProto discovery is empty:** confirm the session state is `connected` and the phone account has joined supported groups/supergroups.
- **Login code rejected:** request a fresh code; codes and temporary code hashes are intentionally not persisted across process restarts.
- **2FA requested:** submit the Telegram two-step-verification password in **Manage Login**. It is used transiently only.
- **MTProto send after restart cannot resolve a group:** run discovery once if the account has never observed/discovered that peer; resolved peer/access-hash metadata is thereafter kept inside encrypted connection state.
- **Backfill unavailable:** only Phone / MTProto advertises arbitrary history recovery, and the connection must be authenticated with a configured endpoint and explicit bounds.
- **Endpoint cannot be created/reassigned:** confirm its transport matches the selected connection and that the target is a supported group.
''')

rep('README.md',
'''### Telegram\n\nCreate a bot with BotFather, disable Bot Privacy Mode, add it to target groups, and make it an administrator when per-user reaction updates are required. Disable anonymous reactions in those groups. Telegram discovery is observation-based, so send a message after adding the bot before refreshing discovered chats. Broadcast channels are not supported.\n\nSee the [manual testing guide](docs/TESTING_GUIDE.md) for detailed provider setup and end-to-end checks.\n''',
'''### Telegram\n\nTelegram connections support two mutually exclusive methods: **Bot API** (BotFather token, observation-based discovery, Bot Privacy Mode applies) and **Phone / MTProto** (Telegram API ID/hash + phone login, complete joined-group/forum-topic discovery, and bounded history recovery). Both use the canonical `telegram` transport, but every endpoint names exactly one connection and never falls back to the other method. Private DMs and broadcast channels are not synchronized.\n\nSee [Telegram integrations](docs/TELEGRAM.md) for the capability comparison, secure phone/code/2FA setup, session storage model, recovery behavior, and troubleshooting. See the [manual testing guide](docs/TESTING_GUIDE.md) for broader end-to-end checks.\n''','README Telegram dual-mode section')
rep('README.md',
'''- `/data/control.db` is the explicit sensitive exception for accounts, sessions, audit records, encrypted Discord/Telegram credentials, and experimental membership verification.\n''',
'''- `/data/control.db` is the explicit sensitive exception for accounts, sessions, audit records, encrypted Discord/Telegram Bot API credentials, encrypted Telegram MTProto API/session/peer state, and experimental membership verification. OTPs and Telegram 2FA passwords are never persisted.\n''','README privacy MTProto')
rep('README.md',
'''Discord and Telegram tokens are configured dynamically, encrypted with AES-256-GCM in `control.db`, and never belong in `.env`.\n''',
'''Discord/Telegram Bot API tokens and Telegram MTProto application/session state are configured dynamically and encrypted with AES-256-GCM in `control.db`; they never belong in `.env`.\n''','README credentials text')
rep('README.md',
'''- [Manual provider testing](docs/TESTING_GUIDE.md)\n- [Container images](DOCKERHUB.md)\n''',
'''- [Manual provider testing](docs/TESTING_GUIDE.md)\n- [Telegram Bot API and Phone/MTProto setup](docs/TELEGRAM.md)\n- [Container images](DOCKERHUB.md)\n''','README docs link')

# Update temporary tracker for #108 but keep it until the issue is closed and cleanup commit runs.
tracker=root/'docs/TELEGRAM_MTPROTO_SUPPORT_PROGRESS.md'
text=tracker.read_text().replace('- [ ] #108 — Complete Telegram integration-mode UI, end-to-end hardening, documentation, and tracker cleanup','- [x] #108 — Complete Telegram integration-mode UI, end-to-end hardening, documentation, and tracker cleanup')
log='''\n### #108 — implementation complete; tracker pending post-close cleanup\n\n- Admin creation now explicitly selects Bot API or Phone/MTProto. Existing connections display the fixed mode; in-place mode changes are rejected.\n- The MTProto management UI supports API ID/hash/phone setup, code request/submission, optional 2FA, sanitized status, logout, complete group/topic discovery, and explicitly bounded backfill. Capability flags drive discovery/history/privacy controls.\n- Logout scrubs reusable session, peer/access-hash runtime/cache state and poll correlation; delete removes the encrypted connection row. Endpoint transport/connection matching remains enforced server-side.\n- Added UI/API/logout/log-privacy hardening tests and permanent `docs/TELEGRAM.md`; README now documents both integration methods and their security/capability differences.\n- Verification target: focused tests plus full `go test ./...` and `go vet ./...`. After #108 closes, this tracker and temporary branch-only CI helpers will be deleted in a final tested cleanup commit.\n'''
marker='\n## Completion rule\n'
if '### #108 — implementation complete' not in text:
    text=text.replace(marker,log+marker)
tracker.write_text(text)
