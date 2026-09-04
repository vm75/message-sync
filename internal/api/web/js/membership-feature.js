/**
 * Client-only membership review feature flag.
 *
 * This preference intentionally lives in localStorage only. It does not alter
 * server configuration, membership APIs, or persisted application state.
 */
(function () {
  'use strict';

  const STORAGE_KEY = 'message-sync-membership-review-enabled';
  const root = document.documentElement;

  function isEnabled() {
    try {
      return window.localStorage.getItem(STORAGE_KEY) === 'true';
    } catch (_) {
      return false;
    }
  }

  function saveEnabled(enabled) {
    try {
      if (enabled) {
        window.localStorage.setItem(STORAGE_KEY, 'true');
      } else {
        window.localStorage.removeItem(STORAGE_KEY);
      }
    } catch (_) {
      // localStorage is optional. The feature remains disabled if persistence
      // is unavailable.
    }
  }

  function installStyles() {
    if (document.getElementById('membership-feature-styles')) return;
    const style = document.createElement('style');
    style.id = 'membership-feature-styles';
    style.textContent = `
      html.membership-review-disabled #nav-membership,
      html.membership-review-disabled #view-membership,
      html.membership-review-disabled #view-sync-sets .syncset-membership-panel,
      html.membership-review-disabled #view-sync-sets .settings-card:has(#membership-applicant-instructions) {
        display: none !important;
      }

      .membership-feature-setting .settings-card-body {
        display: flex;
        flex-direction: column;
        gap: 12px;
      }

      .membership-feature-setting .membership-feature-note {
        margin: 0;
        color: var(--text-muted);
        font-size: .76rem;
        line-height: 1.5;
      }

      .membership-feature-setting .toggle-wrapper {
        align-items: flex-start;
      }
    `;
    document.head.appendChild(style);
  }

  function applyVisibility() {
    const enabled = isEnabled();
    root.classList.toggle('membership-review-enabled', enabled);
    root.classList.toggle('membership-review-disabled', !enabled);

    const toggle = document.getElementById('settings-membership-review-enabled');
    if (toggle) toggle.checked = enabled;

    if (!enabled && window.location.hash.replace(/^#/, '').split('?')[0] === 'membership') {
      window.location.hash = '#settings';
    }
  }

  function installSettingsControl() {
    const form = document.getElementById('form-settings');
    if (!form || document.getElementById('settings-membership-review-enabled')) return;

    const card = document.createElement('div');
    card.className = 'settings-card membership-feature-setting';
    card.innerHTML = `
      <div class="settings-card-header">
        <div class="settings-card-icon primary" aria-hidden="true">
          <svg class="icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
            <path d="M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z"></path>
            <path d="M9 12l2 2 4-4"></path>
          </svg>
        </div>
        <div class="settings-card-meta">
          <div class="settings-card-title">Membership review</div>
          <div class="settings-card-subtitle">Control whether membership review appears in this browser.</div>
        </div>
      </div>
      <div class="settings-card-body">
        <label class="toggle-wrapper" for="settings-membership-review-enabled">
          <input type="checkbox" id="settings-membership-review-enabled" style="width:auto;flex-shrink:0;margin-top:3px;">
          <span>
            <span class="toggle-title">Enable membership review</span>
            <span class="form-hint">Shows the Membership navigation tab and the Membership application section inside sync-set settings.</span>
          </span>
        </label>
        <p class="membership-feature-note">Disabled by default. This is a client-only preference stored in this browser and does not change server-side membership configuration.</p>
      </div>`;

    const actions = form.querySelector('.settings-form-actions');
    if (actions) {
      form.insertBefore(card, actions);
    } else {
      form.appendChild(card);
    }

    const toggle = card.querySelector('#settings-membership-review-enabled');
    toggle.checked = isEnabled();
    toggle.addEventListener('change', () => {
      saveEnabled(toggle.checked);
      applyVisibility();
    });
  }

  installStyles();
  installSettingsControl();
  applyVisibility();

  window.addEventListener('hashchange', applyVisibility);
  window.addEventListener('storage', (event) => {
    if (event.key === STORAGE_KEY) applyVisibility();
  });

  // app-base.js can remove the Membership nav's generic hidden class after
  // authentication. Re-apply the client-side feature flag whenever that or the
  // sync-set card structure changes so disabled really means hidden.
  const observer = new MutationObserver(() => {
    installSettingsControl();
    applyVisibility();
  });
  observer.observe(document.body, { childList: true, subtree: true, attributes: true, attributeFilter: ['class'] });
})();
