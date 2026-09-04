/**
 * Message Sync UI bootstrap.
 * Presentation-only enhancements; application behavior stays in app-base.js.
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

  function storedTheme() {
    try {
      const value = window.localStorage.getItem(STORAGE_KEY);
      return value === 'light' || value === 'dark' ? value : null;
    } catch (_) {
      return null;
    }
  }

  function saveTheme(value) {
    try { window.localStorage.setItem(STORAGE_KEY, value); } catch (_) { /* optional */ }
  }

  function themeIcons() {
    return `
      <svg class="theme-icon theme-moon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M21 12.79A9 9 0 1 1 11.21 3 7 7 0 0 0 21 12.79z"></path></svg>
      <svg class="theme-icon theme-sun" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="12" cy="12" r="4"></circle><path d="M12 2v2M12 20v2M4.93 4.93l1.42 1.42M17.66 17.66l1.41 1.41M2 12h2M20 12h2M4.93 19.07l1.42-1.42M17.66 6.34l1.41-1.41"></path></svg>`;
  }

  function refreshThemeButtons(value) {
    const target = value === 'dark' ? 'light' : 'dark';
    document.querySelectorAll('[data-theme-toggle]').forEach((button) => {
      button.setAttribute('aria-label', `Switch to ${target} mode`);
      button.setAttribute('title', `Switch to ${target} mode`);
      button.setAttribute('aria-pressed', value === 'dark' ? 'true' : 'false');
      const label = button.querySelector('[data-theme-label]');
      if (label) label.textContent = `${target[0].toUpperCase()}${target.slice(1)} mode`;
    });
  }

  function applyTheme(value) {
    root.dataset.theme = value;
    refreshThemeButtons(value);
  }

  function toggleTheme() {
    const next = root.dataset.theme === 'dark' ? 'light' : 'dark';
    applyTheme(next);
    saveTheme(next);
  }

  function installThemeControls() {
    const railNav = document.querySelector('#side-rail .rail-nav');
    if (railNav) {
      const button = document.createElement('button');
      button.type = 'button';
      button.className = 'nav-item theme-nav-item';
      button.dataset.themeToggle = 'true';
      button.innerHTML = `${themeIcons()}<span class="nav-item-label" data-theme-label>Theme</span>`;
      button.addEventListener('click', toggleTheme);
      railNav.appendChild(button);
    }

    document.querySelectorAll('.auth-card').forEach((card) => {
      const row = document.createElement('div');
      row.className = 'theme-auth-row';
      const button = document.createElement('button');
      button.type = 'button';
      button.className = 'btn btn-ghost btn-sm theme-auth-button';
      button.dataset.themeToggle = 'true';
      button.innerHTML = `${themeIcons()}<span data-theme-label>Theme</span>`;
      button.addEventListener('click', toggleTheme);
      row.appendChild(button);
      card.prepend(row);
    });
  }

  function replaceButtonText(button, text, className) {
    if (!button) return;
    const svg = button.querySelector('svg');
    const spinner = button.querySelector('.btn-spinner');
    button.replaceChildren();
    if (svg) button.appendChild(svg);
    const label = document.createElement('span');
    if (className) label.className = className;
    label.textContent = text;
    button.appendChild(label);
    if (spinner) button.appendChild(spinner);
  }

  function addSectionHelp(section, text) {
    if (!section || section.querySelector('.syncset-section-help')) return;
    const label = section.querySelector(':scope > .section-label');
    if (!label) return;
    const help = document.createElement('p');
    help.className = 'syncset-section-help';
    help.textContent = text;
    label.insertAdjacentElement('afterend', help);
  }

  function enhanceSyncSetStaticUI() {
    const view = document.getElementById('view-sync-sets');
    if (!view) return;

    const headerText = view.querySelector('.page-header > div');
    if (headerText && !headerText.querySelector('.syncset-page-subtitle')) {
      const subtitle = document.createElement('p');
      subtitle.className = 'syncset-page-subtitle';
      subtitle.textContent = 'A sync set keeps selected conversations mirrored. Messages sent in one conversation are forwarded to the others in the same set.';
      headerText.appendChild(subtitle);
    }

    const btnNew = document.getElementById('btn-new-sync-set');
    replaceButtonText(btnNew, 'Create sync set', 'syncset-create-label');

    const listTitle = view.querySelector('.syncset-list-title');
    const countBadge = document.getElementById('syncset-count-badge');
    if (listTitle && countBadge) listTitle.replaceChildren(document.createTextNode('Your sync sets'), countBadge);

    const placeholder = document.getElementById('syncset-editor-placeholder');
    if (placeholder) {
      const copy = placeholder.querySelector('p');
      if (copy) {
        copy.className = 'syncset-placeholder-copy';
        copy.textContent = 'Choose a sync set above to manage its conversations, or create a new one.';
        if (!placeholder.querySelector('.syncset-placeholder-title')) {
          const title = document.createElement('div');
          title.className = 'syncset-placeholder-title';
          title.textContent = 'Choose a sync set';
          copy.before(title);
        }
      }
      if (btnNew && !placeholder.querySelector('.syncset-placeholder-action')) {
        const action = document.createElement('button');
        action.type = 'button';
        action.className = 'btn btn-primary syncset-placeholder-action';
        action.textContent = 'Create sync set';
        action.addEventListener('click', () => btnNew.click());
        placeholder.appendChild(action);
      }
    }

    const editorTitle = document.getElementById('syncset-editor-title');
    if (editorTitle) {
      const idDisplay = document.getElementById('syncset-editor-id-display');
      if (idDisplay?.parentElement) idDisplay.parentElement.classList.add('syncset-internal-id');
      if (!editorTitle.parentElement?.querySelector('.syncset-editor-subtitle')) {
        const subtitle = document.createElement('p');
        subtitle.className = 'syncset-editor-subtitle';
        subtitle.textContent = 'Choose which conversations should stay synchronized.';
        editorTitle.insertAdjacentElement('afterend', subtitle);
      }
    }

    const idField = document.getElementById('syncset-id-field');
    if (idField) {
      const label = idField.querySelector('label');
      const hint = idField.querySelector('.form-hint');
      const input = document.getElementById('input-syncset-id');
      if (label) label.textContent = 'Sync set name';
      if (hint) hint.textContent = 'Use a short unique name with letters, numbers, dashes, or underscores. The name cannot be changed after creation.';
      if (input) input.placeholder = 'e.g. family-chat';
    }

    const editorBody = view.querySelector('.syncset-editor-body');
    if (editorBody) {
      const children = Array.from(editorBody.children);
      const members = children.find((child) => child !== idField && child.querySelector('#endpoint-chips'));
      const add = children.find((child) => child.querySelector('.add-endpoint-form'));

      if (members) {
        members.classList.add('syncset-section', 'syncset-members-section');
        const label = members.querySelector(':scope > .section-label');
        if (label) label.textContent = 'Synced conversations';
        addSectionHelp(members, 'Every conversation below receives messages from the others in this sync set.');
      }

      if (add) {
        add.classList.add('syncset-section', 'syncset-add-section');
        const label = add.querySelector(':scope > .section-label');
        if (label) label.textContent = 'Add a conversation';
        addSectionHelp(add, 'Pick a platform and connection, then choose the group or channel to include.');
      }
    }

    const addForm = view.querySelector('.add-endpoint-form');
    if (addForm) {
      const platformLabel = addForm.querySelector(':scope > div:first-child .section-label');
      const connectionLabel = document.querySelector('label[for="add-endpoint-conn-select"]');
      const aliasLabel = document.querySelector('label[for="add-alias-input"]');
      const aliasInput = document.getElementById('add-alias-input');
      if (platformLabel) platformLabel.textContent = 'Choose platform';
      if (connectionLabel) connectionLabel.textContent = 'Connection';
      if (aliasLabel) aliasLabel.textContent = 'Conversation label';
      if (aliasInput) aliasInput.placeholder = 'e.g. family-whatsapp';
    }

    const addText = document.querySelector('#btn-add-endpoint .btn-text');
    const saveText = document.querySelector('#btn-save-sync-set .btn-text');
    if (addText) addText.textContent = 'Add conversation';
    if (saveText) saveText.textContent = 'Save changes';
  }

  function enhanceSyncSetListItems() {
    const list = document.getElementById('syncset-list');
    if (!list) return;

    list.querySelectorAll('.syncset-list-item').forEach((item) => {
      const count = item.querySelector('.syncset-item-count');
      if (count && !count.dataset.friendlyCount) {
        const match = count.textContent.trim().match(/^(\d+)\s+ep$/);
        if (match) {
          const n = Number(match[1]);
          count.textContent = `${n} ${n === 1 ? 'conversation' : 'conversations'}`;
        }
        count.dataset.friendlyCount = 'true';
      }

      if (!item.dataset.keyboardReady) {
        item.tabIndex = 0;
        item.setAttribute('role', 'button');
        item.addEventListener('keydown', (event) => {
          if ((event.key === 'Enter' || event.key === ' ') && !event.target.closest('.syncset-delete-btn')) {
            event.preventDefault();
            item.click();
          }
        });
        item.dataset.keyboardReady = 'true';
      }

      const deleteButton = item.querySelector('.syncset-delete-btn');
      if (deleteButton) {
        deleteButton.title = 'Delete sync set';
        deleteButton.setAttribute('aria-label', 'Delete sync set');
      }
    });

    const empty = list.querySelector('.syncset-empty-list');
    const friendlyEmpty = 'No sync sets yet. Create one to start mirroring conversations.';
    if (empty && empty.textContent.trim() !== friendlyEmpty) empty.textContent = friendlyEmpty;
  }

  function enhanceEndpointRows() {
    const chips = document.getElementById('endpoint-chips');
    if (!chips) return;

    const placeholder = chips.querySelector('.chips-placeholder');
    const friendlyEmpty = 'No conversations in this sync set yet. Add one below.';
    if (placeholder && placeholder.textContent.trim() !== friendlyEmpty) placeholder.textContent = friendlyEmpty;

    chips.querySelectorAll('.endpoint-chip').forEach((row) => {
      row.classList.add('endpoint-row');
      const move = row.querySelector('.chip-reassign');
      const rename = row.querySelector('.chip-edit');
      const remove = row.querySelector('.chip-remove');
      if (move) move.setAttribute('aria-label', 'Move to another connection');
      if (rename) rename.setAttribute('aria-label', 'Rename conversation label');
      if (remove) remove.setAttribute('aria-label', 'Remove conversation from sync set');
    });
  }

  function makeEditorTitleFriendly() {
    const title = document.getElementById('syncset-editor-title');
    if (!title) return;
    const value = title.textContent.trim();
    if (value === 'New Sync Set') title.textContent = 'Create sync set';
    else if (value.startsWith('Edit: ')) title.textContent = value.slice(6);
  }

  function observeSyncSetUI() {
    const list = document.getElementById('syncset-list');
    const chips = document.getElementById('endpoint-chips');
    const title = document.getElementById('syncset-editor-title');

    if (list) {
      new MutationObserver(enhanceSyncSetListItems).observe(list, { childList: true, subtree: true, characterData: true });
      enhanceSyncSetListItems();
    }
    if (chips) {
      new MutationObserver(enhanceEndpointRows).observe(chips, { childList: true, subtree: true });
      enhanceEndpointRows();
    }
    if (title) {
      new MutationObserver(makeEditorTitleFriendly).observe(title, { childList: true, characterData: true, subtree: true });
      makeEditorTitleFriendly();
    }
  }

  const initialTheme = storedTheme() || (media.matches ? 'dark' : 'light');
  root.dataset.theme = initialTheme;
  installThemeControls();
  refreshThemeButtons(initialTheme);
  enhanceSyncSetStaticUI();
  observeSyncSetUI();

  media.addEventListener?.('change', (event) => {
    if (!storedTheme()) applyTheme(event.matches ? 'dark' : 'light');
  });

  // IDs remain available to the preserved controller, but are not user-facing.
  document.getElementById('add-conn-id')?.closest('.form-group')?.classList.add('hidden');

  const controller = document.createElement('script');
  controller.src = '/js/app-base.js?v=8';
  controller.async = false;
  document.body.appendChild(controller);
})();
