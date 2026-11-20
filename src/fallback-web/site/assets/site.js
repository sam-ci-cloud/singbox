/* Folder links still jump to their first row without JavaScript. */
(() => {
  const input = document.getElementById('filter');
  const rows = Array.from(document.querySelectorAll('[data-file-row]'));
  const folders = Array.from(document.querySelectorAll('.folders a'));
  const count = document.getElementById('filter-count');
  const empty = document.getElementById('filter-empty');

  function apply() {
    const current = folders.find(link => link.hash === location.hash) || folders[0];
    const folder = current.dataset.folder;
    const query = input.value.trim().toLowerCase();
    let shown = 0;

    for (const link of folders) {
      if (link === current) link.setAttribute('aria-current', 'location');
      else link.removeAttribute('aria-current');
    }
    for (const row of rows) {
      row.hidden = (folder && row.dataset.folder !== folder) || !row.dataset.search.includes(query);
      if (!row.hidden) shown++;
    }
    count.textContent = shown + ' of ' + rows.length + ' files';
    empty.hidden = shown > 0;
  }

  input.addEventListener('input', apply);
  window.addEventListener('hashchange', apply);
  document.getElementById('filter-tools').hidden = false;
  apply();
})();
