// Forms and navigation work without JavaScript. This only adds submit feedback.
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
