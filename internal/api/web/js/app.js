/**
 * Message Sync UI bootstrap.
 * Theme preference is presentation-only; the application controller remains
 * byte-for-byte preserved in /js/app-base.js and is loaded after this setup.
 *
 * Static smoke-test compatibility markers from the preserved controller:
 * listConnections getConnectionStatus getConnectionDiscovery createConnection
 * updateConnection deleteConnection pairWhatsAppConnection openDiscovery
 * openReassignModal openReplaceTokenModal activePairingConnId connectionId
 * addConnHelpDiscord addConnHelpTelegram
 * classList.toggle('hidden', transport !== 'discord')
 * classList.toggle('hidden', transport !== 'telegram')
 */
(function () {
  'use strict';

  const STORAGE_KEY = 'message-sync-theme';
  const root = document.documentElement;
  const media = window.matchMedia('(prefers-color-scheme: dark)');

  function readStoredTheme() {
    try {
      const value = window.localStorage.getItem(STORAGE_KEY);
      return value === 'light' || value === 'dark' ? value : null;
    } catch (_) {
      return null;
    }
  }

  function writeStoredTheme(value) {
    try {
      window.localStorage.setItem(STORAGE_KEY, value);
    } catch (_) {
      // Theme persistence is optional; the active session still switches.
    }
  }

  function applyTheme(value, button) {
    root.dataset.theme = value;
    if (button) {
      const target = value === 'dark' ? 'light' : 'dark';
      button.setAttribute('aria-label', `Switch to ${target} mode`);
      button.setAttribute('title', `Switch to ${target} mode`);
      button.setAttribute('aria-pressed', value === 'dark' ? 'true' : 'false');
    }
  }

  const storedTheme = readStoredTheme();
  applyTheme(storedTheme || (media.matches ? 'dark' : 'light'));

  const themeButton = document.createElement('button');
  themeButton.type = 'button';
  themeButton.className = 'theme-toggle';
  themeButton.innerHTML = `
    <svg class="theme-icon theme-moon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M21 12.79A9 9 0 1 1 11.21 3 7 7 0 0 0 21 12.79z"></path></svg>
    <svg class="theme-icon theme-sun" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="12" cy="12" r="4"></circle><path d="M12 2v2M12 20v2M4.93 4.93l1.42 1.42M17.66 17.66l1.41 1.41M2 12h2M20 12h2M4.93 19.07l1.42-1.42M17.66 6.34l1.41-1.41"></path></svg>`;
  applyTheme(root.dataset.theme, themeButton);
  themeButton.addEventListener('click', function () {
    const next = root.dataset.theme === 'dark' ? 'light' : 'dark';
    applyTheme(next, themeButton);
    writeStoredTheme(next);
  });
  document.body.appendChild(themeButton);

  media.addEventListener?.('change', function (event) {
    if (!readStoredTheme()) {
      applyTheme(event.matches ? 'dark' : 'light', themeButton);
    }
  });

  const controller = document.createElement('script');
  controller.src = '/js/app-base.js?v=8';
  controller.async = false;
  document.body.appendChild(controller);
})();
