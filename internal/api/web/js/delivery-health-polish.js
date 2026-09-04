/* Compact, sync-set-aware presentation for the Dashboard delivery health table. */
(function () {
  'use strict';

  const tbody = document.getElementById('delivery-health-body');
  if (!tbody) return;

  const table = tbody.closest('table');
  if (!table) return;

  table.classList.add('delivery-health-table', 'delivery-polish-pending');

  const headerRow = table.tHead && table.tHead.rows ? table.tHead.rows[0] : null;
  if (headerRow) {
    headerRow.innerHTML = `
      <th>Sync set</th>
      <th>Endpoint</th>
      <th class="delivery-platform-column">Platform</th>
      <th>Status</th>
      <th class="delivery-column">Delivery</th>`;
  }

  let applying = false;
  let syncSetsReady = false;
  let aliasToSet = new Map();
  let latestEntries = [];

  function escapeHtml(value) {
    const div = document.createElement('div');
    div.textContent = value == null ? '' : String(value);
    return div.innerHTML;
  }

  function friendlyStatus(value) {
    const text = String(value || '').trim();
    if (!text || text === '—') return '—';
    return text
      .replace(/[_-]+/g, ' ')
      .replace(/\b\w/g, (char) => char.toUpperCase());
  }

  function platformIcon(transport) {
    const key = String(transport || '').trim().toLowerCase();
    if (key === 'discord') {
      return `<span class="delivery-platform-icon discord" role="img" aria-label="Discord" title="Discord">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="9" cy="12" r="1"></circle><circle cx="15" cy="12" r="1"></circle><path d="M7.5 7.5c3.5-1 5.5-1 9 0 1 2.5 1.5 6 1 8.5-2 1.5-4 1.5-5.5 1.5l-.5-.5c1-.5 1.5-1 1.5-1-1.5.5-3 .5-4.5 0 0 0 .5.5 1.5 1l-.5.5C8.5 17.5 6.5 17.5 4.5 16c-.5-2.5 0-6 1-8.5z"></path></svg>
      </span>`;
    }
    if (key === 'telegram') {
      return `<span class="delivery-platform-icon telegram" role="img" aria-label="Telegram" title="Telegram">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M22 2L11 13"></path><path d="M22 2L15 22l-4-9-9-4 20-7z"></path></svg>
      </span>`;
    }
    return `<span class="delivery-platform-icon wa" role="img" aria-label="WhatsApp" title="WhatsApp">
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M22 16.92v3a2 2 0 0 1-2.18 2 19.79 19.79 0 0 1-8.63-3.07 19.5 19.5 0 0 1-6-6 19.79 19.79 0 0 1-3.07-8.67A2 2 0 0 1 4.11 2h3a2 2 0 0 1 2 1.72 12.84 12.84 0 0 0 .7 2.81 2 2 0 0 1-.45 2.11L8.09 9.91a16 16 0 0 0 6 6l1.27-1.27a2 2 0 0 1 2.11-.45 12.84 12.84 0 0 1 2.81.7A2 2 0 0 1 22 16.92z"></path></svg>
    </span>`;
  }

  function deliverySummary(ledgerText) {
    const text = String(ledgerText || '').trim();
    const match = text.match(/Q\s*(\d+)\s*·\s*R\s*(\d+)\s*·\s*A\s*(\d+)\s*·\s*F\s*(\d+)/i);
    if (!match) return escapeHtml(text || '—');
    return `Pending ${match[1]} · Retrying ${match[2]} · Replay ${match[3]} · Failed ${match[4]}`;
  }

  function naturalCompare(a, b) {
    return String(a).localeCompare(String(b), undefined, { numeric: true, sensitivity: 'base' });
  }

  function extractRawEntries() {
    return Array.from(tbody.rows)
      .filter((row) => row.cells.length === 6)
      .map((row, originalIndex) => {
        const cells = row.cells;
        return {
          alias: cells[0].textContent.trim(),
          transport: cells[1].textContent.trim(),
          stateText: cells[2].textContent.trim(),
          ledgerText: cells[4].textContent.trim(),
          transportStatus: cells[5].textContent.trim(),
          originalIndex,
        };
      });
  }

  function renderEntries(sourceEntries) {
    if (!syncSetsReady || applying || sourceEntries.length === 0) return;

    applying = true;
    try {
      const entries = sourceEntries.map((entry) => ({
        ...entry,
        syncSet: aliasToSet.get(entry.alias) || '',
      }));

      entries.sort((a, b) => {
        if (!!a.syncSet !== !!b.syncSet) return a.syncSet ? -1 : 1;
        const setOrder = naturalCompare(a.syncSet || 'zzzz', b.syncSet || 'zzzz');
        if (setOrder !== 0) return setOrder;
        const aliasOrder = naturalCompare(a.alias, b.alias);
        return aliasOrder !== 0 ? aliasOrder : a.originalIndex - b.originalIndex;
      });

      const groupOrder = [];
      entries.forEach((entry) => {
        const key = entry.syncSet || '__unassigned__';
        if (!groupOrder.includes(key)) groupOrder.push(key);
      });
      const groupIndex = new Map(groupOrder.map((key, index) => [key, index]));

      let previousGroup = null;
      const fragment = document.createDocumentFragment();
      entries.forEach((entry) => {
        const groupKey = entry.syncSet || '__unassigned__';
        const index = groupIndex.get(groupKey) || 0;
        const healthy = entry.stateText.toLowerCase().startsWith('healthy');
        const healthLabel = healthy ? 'Healthy' : (entry.stateText || 'Unhealthy');
        const row = document.createElement('tr');

        row.className = index % 2 === 0 ? 'delivery-group-a' : 'delivery-group-b';
        if (groupKey !== previousGroup) row.classList.add('delivery-group-start');
        row.dataset.syncSet = entry.syncSet || '';
        previousGroup = groupKey;

        row.innerHTML = `
          <td class="delivery-syncset-cell"><span class="delivery-syncset-label">${escapeHtml(entry.syncSet || '—')}</span></td>
          <td><span class="alias-badge">${escapeHtml(entry.alias)}</span></td>
          <td class="delivery-platform-column delivery-platform-cell">${platformIcon(entry.transport)}</td>
          <td class="delivery-status-cell">
            <span class="delivery-health-dot ${healthy ? 'healthy' : 'unhealthy'}" role="img" aria-label="${escapeHtml(healthLabel)}" title="${escapeHtml(healthLabel)}"></span>
            <span class="delivery-transport-state">${escapeHtml(friendlyStatus(entry.transportStatus))}</span>
          </td>
          <td class="delivery-column"><span class="delivery-summary">${deliverySummary(entry.ledgerText)}</span></td>`;
        fragment.appendChild(row);
      });

      tbody.replaceChildren(fragment);
      table.classList.remove('delivery-polish-pending');
    } finally {
      applying = false;
    }
  }

  async function refreshSyncSets() {
    try {
      const result = await window.API.getSyncSets();
      const nextMap = new Map();
      (Array.isArray(result) ? result : []).forEach((set) => {
        const setID = set && set.id ? String(set.id) : '';
        (Array.isArray(set && set.endpoints) ? set.endpoints : []).forEach((alias) => {
          if (alias && !nextMap.has(String(alias))) nextMap.set(String(alias), setID);
        });
      });
      aliasToSet = nextMap;
    } catch (_) {
      aliasToSet = new Map();
    } finally {
      syncSetsReady = true;
      renderEntries(latestEntries);
      if (latestEntries.length === 0) table.classList.remove('delivery-polish-pending');
    }
  }

  const observer = new MutationObserver(() => {
    if (applying) return;
    const rawEntries = extractRawEntries();
    if (rawEntries.length === 0) return;

    // app-base.js replaces the body every three seconds. Capture that legacy
    // snapshot and immediately render the polished rows in the same microtask,
    // before the browser paints the intermediate six-column table.
    latestEntries = rawEntries;
    table.classList.add('delivery-polish-pending');
    renderEntries(latestEntries);
  });
  observer.observe(tbody, { childList: true });

  window.addEventListener('hashchange', () => {
    if (window.location.hash.replace(/^#/, '').split('?')[0] === 'dashboard') refreshSyncSets();
  });
  document.addEventListener('syncset:saved', refreshSyncSets);

  latestEntries = extractRawEntries();
  refreshSyncSets();
})();
