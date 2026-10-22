// Forms work without JavaScript. GitHub work runs independently on the server.
document.querySelectorAll("form[data-pending]").forEach((form) => {
  form.addEventListener("submit", (event) => {
    if (form.dataset.submitting) {
      event.preventDefault();
      return;
    }
    form.dataset.submitting = "true";
    form.setAttribute("aria-busy", "true");
    const button = event.submitter;
    if (button) {
      button.dataset.originalText = button.textContent;
      button.textContent = form.dataset.pending;
      button.disabled = true;
    }
    const status = form.querySelector(".submit-status");
    if (status) status.textContent = form.dataset.pending;
  });
});

window.addEventListener("pageshow", () => {
  document.querySelectorAll("form[data-submitting]").forEach((form) => {
    delete form.dataset.submitting;
    form.removeAttribute("aria-busy");
    form.querySelectorAll("button[data-original-text]").forEach((button) => {
      button.textContent = button.dataset.originalText;
      button.disabled = false;
      delete button.dataset.originalText;
    });
    const status = form.querySelector(".submit-status");
    if (status) status.textContent = "";
  });
});

const bulkAcceptForm = document.querySelector("#bulk-accept-form");
if (bulkAcceptForm) {
  const rows = Array.from(document.querySelectorAll('input[name="alerts"][form="bulk-accept-form"]'));
  const selectAll = document.querySelector("[data-select-all]");
  const button = bulkAcceptForm.querySelector("[data-accept-selected]");
  const count = bulkAcceptForm.querySelector("[data-selection-count]");
  document.querySelector("[data-select-all-control]").hidden = false;

  function updateSelection() {
    const selected = rows.filter((row) => row.checked).length;
    button.disabled = selected === 0;
    count.textContent = `${selected} of ${rows.length} open alerts selected on this page`;
    selectAll.checked = selected === rows.length;
    selectAll.indeterminate = selected > 0 && selected < rows.length;
  }

  selectAll.addEventListener("change", () => {
    rows.forEach((row) => { row.checked = selectAll.checked; });
    updateSelection();
  });
  rows.forEach((row) => row.addEventListener("change", updateSelection));
  window.addEventListener("pageshow", updateSelection);
  updateSelection();
}

const syncStatus = document.querySelector("[data-sync-status]");
if (syncStatus) {
  const initialVersion = syncStatus.dataset.version;
  const message = syncStatus.querySelector("[data-sync-message]");
  const reload = syncStatus.querySelector("[data-sync-reload]");
  const hasSelectedRows = () => Boolean(document.querySelector('input[name="alerts"][form="bulk-accept-form"]:checked'));
  let edited = hasSelectedRows();
  let submitting = false;
  let wasRefreshing = syncStatus.dataset.refreshing === "true";
  document.addEventListener("input", () => { edited = true; });
  document.addEventListener("change", () => { edited = true; });
  document.addEventListener("submit", () => { submitting = true; });
  window.addEventListener("pageshow", () => {
    submitting = false;
    edited = edited || hasSelectedRows();
  });

  async function checkRefresh() {
    let delay = wasRefreshing ? 2000 : 10000;
    try {
      const response = await fetch("/api/status", {
        cache: "no-store",
        signal: AbortSignal.timeout(5000),
      });
      if (!response.ok) throw new Error("Status unavailable");
      const status = await response.json();
      if (status.refreshing) {
        wasRefreshing = true;
        delay = 2000;
        syncStatus.hidden = false;
        message.textContent = "Refreshing alerts in the background. You can keep using this page.";
        reload.hidden = true;
      } else if (wasRefreshing || String(status.version) !== initialVersion) {
        if (!edited && !submitting) {
          window.location.reload();
          return;
        }
        syncStatus.hidden = false;
        message.textContent = "Refresh complete. Updated results are available.";
        reload.hidden = false;
        return;
      } else {
        syncStatus.hidden = true;
      }
    } catch {
      syncStatus.hidden = false;
      message.textContent = "Could not check refresh status. Reload this page to check the results.";
      reload.hidden = false;
      delay = 10000;
    }
    window.setTimeout(checkRefresh, delay);
  }
  window.setTimeout(checkRefresh, wasRefreshing ? 1000 : 10000);
}
