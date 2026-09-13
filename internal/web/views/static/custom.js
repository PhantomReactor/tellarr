// Copy-to-clipboard helper. Source is either data-copy-text on the button
// itself or a textarea referenced via data-yml-id. navigator.clipboard only
// exists on secure origins (https or localhost), so on plain-http LAN setups
// every copy must go through a hidden textarea + execCommand fallback.
function tellarrCopy(btn) {
  var text = btn.getAttribute("data-copy-text");
  var ta = null;
  if (text === null || text === "") {
    ta = document.getElementById(btn.getAttribute("data-yml-id"));
    if (!ta) return;
    text = ta.value;
  }
  var prev = btn.innerHTML;
  var done = function () {
    if (btn.classList.contains("icon-btn")) {
      btn.classList.add("copy-ok");
      setTimeout(function () {
        btn.classList.remove("copy-ok");
      }, 1500);
    } else {
      btn.innerHTML = "Copied!";
      setTimeout(function () {
        btn.innerHTML = prev;
      }, 1500);
    }
  };
  var fallback = function () {
    // Reuse the visible textarea when there is one; otherwise stage a hidden
    // one (must be focusable and rendered off-screen, display:none won't copy).
    var stage = ta;
    var created = false;
    if (!stage) {
      stage = document.createElement("textarea");
      stage.value = text;
      stage.setAttribute("readonly", "");
      stage.style.position = "fixed";
      stage.style.top = "-9999px";
      document.body.appendChild(stage);
      created = true;
    }
    var active = document.activeElement;
    stage.focus();
    stage.select();
    try {
      stage.setSelectionRange(0, text.length);
    } catch (e) {}
    var ok = false;
    try {
      ok = document.execCommand("copy");
    } catch (e) {}
    if (created) document.body.removeChild(stage);
    if (active && typeof active.focus === "function") active.focus();
    done();
  };  if (navigator.clipboard && navigator.clipboard.writeText && window.isSecureContext) {
    navigator.clipboard.writeText(text).then(done, fallback);
  } else {
    fallback();
  }
}

// --- Accent theme ----------------------------------------------------------------

var ACCENTS = ["amber", "green", "gruvbox", "ice", "cyan", "rose", "ember", "nord", "mono"];

function setAccent(v) {
  if (ACCENTS.indexOf(v) === -1) v = "amber";
  document.documentElement.setAttribute("data-accent", v);
  try {
    localStorage.setItem("tl-accent", v);
  } catch (e) {}
  var s = document.getElementById("accent-select");
  if (s) s.value = v;
}

(function initAccent() {
  var v = document.documentElement.getAttribute("data-accent");
  if (!v) {
    try {
      v = localStorage.getItem("tl-accent");
    } catch (e) {}
    if (v) document.documentElement.setAttribute("data-accent", v);
  }
  setAccent(v || "amber");
})();

// --- Mobile nav ----------------------------------------------------------------

function toggleNav(btn) {
  var el = document.getElementById("nav-links");
  if (!el) return;
  var open = el.classList.toggle("open");
  if (btn) btn.setAttribute("aria-expanded", open ? "true" : "false");
}

// --- Modals -----------------------------------------------------------------

function openModal(id) {
  var d = document.getElementById(id);
  if (!d || typeof d.showModal !== "function") return;
  d.showModal();
  // Focus the first field instead of the close button (avoids a focus ring
  // on the X right after opening). Checkboxes/radios are skipped — they're
  // part of a group, not a single field to land focus on.
  var f = d.querySelector(
    "input:not([type=hidden]):not([type=checkbox]):not([type=radio]), textarea, select"
  );
  if (f) {
    f.focus();
    return;
  }
  if (document.activeElement && d.contains(document.activeElement)) {
    document.activeElement.blur();
  }
}

function closeModal(id) {
  var d = document.getElementById(id);
  if (d && typeof d.close === "function") d.close();
}

// Click on the backdrop (the dialog element itself) closes the modal.
document.addEventListener("click", function (e) {
  var d = e.target instanceof Element ? e.target.closest("dialog") : null;
  if (d && e.target === d) d.close();
});

// Category editor: close any open downloads-table <details> editor when
// clicking anywhere outside it, so the edit view stays up until then.
document.addEventListener("click", function (e) {
  if (e.target instanceof Element) {
    document.querySelectorAll("#downloads-table details.cat-edit[open]").forEach(function (d) {
      if (!d.contains(e.target)) d.open = false;
    });
  }
});

// Mobile row expand: tapping anywhere on a downloads row toggles its detail
// cells (category, size, speed, ETA, state, actions). Taps on interactive
// elements inside the row are ignored. The 2s poll pauses while open.
function toggleRowExpand(tr) {
  if (tr) tr.classList.toggle("row-open");
}

document.addEventListener("click", function (e) {
  if (!(e.target instanceof Element)) return;
  if (window.matchMedia("(min-width: 721px)").matches) return;
  var tr = e.target.closest("#downloads-table tr");
  if (!tr) return;
  if (e.target.closest("button, a, select, input, summary, label, form")) return;
  toggleRowExpand(tr);
});

// Add-to-Prowlarr category picker: "Add to Prowlarr" buttons carry
// data-channel and a comma-separated data-categories (the channel's
// currently saved torznabcats keys, defaulting server-side when unset) —
// rewire #modal-prowlarr-cat's hidden name field and check exactly those
// boxes before showing it.
function openProwlarrCategoryModal(btn, ev) {
  var name = btn.getAttribute("data-channel");
  if (!name) return;
  var nameInput = document.getElementById("prowlarr-cat-name");
  var title = document.getElementById("prowlarr-cat-title");
  if (nameInput) nameInput.value = name;
  if (title) title.textContent = 'Add "' + name + '" to Prowlarr';
  var selected = (btn.getAttribute("data-categories") || "").split(",");
  document.querySelectorAll('#modal-prowlarr-cat input[name="category"]').forEach(function (cb) {
    cb.checked = selected.indexOf(cb.value) !== -1;
  });
  openModal("modal-prowlarr-cat");
}

// Delete confirmation: trash buttons carry data-del-id and rewire the two
// forms inside #modal-delete before showing it.
function askDelete(btn, ev) {
  ev.preventDefault();
  var id = btn.getAttribute("data-del-id");
  if (!id) return;
  var base = "/ui/downloads/" + encodeURIComponent(id) + "/";
  var rec = document.getElementById("del-form-record");
  var files = document.getElementById("del-form-files");
  if (rec) rec.action = base + "delete";
  if (files) files.action = base + "delete-files";
  openModal("modal-delete");
}

// Restart confirmation: buttons carry data-restart-id and rewire the form
// inside #modal-restart before showing it.
function askRestart(btn, ev) {
  ev.preventDefault();
  var id = btn.getAttribute("data-restart-id");
  if (!id) return;
  var form = document.getElementById("restart-form");
  if (form) form.action = "/ui/downloads/" + encodeURIComponent(id) + "/restart";
  openModal("modal-restart");
}

// --- Client-side search ------------------------------------------------------

var tellarrFilters = {};

function tellarrFilter(input) {
  var target = input.getAttribute("data-target");
  if (!target) return;
  tellarrFilters[target] = input.value.trim().toLowerCase();
  applyFilter(target);
}

function applyFilter(tableId) {
  var root = document.getElementById(tableId);
  if (!root) return;
  var q = tellarrFilters[tableId] || "";
  var rows = root.querySelectorAll("tr[data-search]");
  var visible = 0;
  rows.forEach(function (row) {
    var match = !q || row.getAttribute("data-search").indexOf(q) !== -1;
    row.style.display = match ? "" : "none";
    if (match) visible++;
  });
  var empty = document.getElementById(tableId + "-noresults");
  if (empty) empty.hidden = !(q && rows.length > 0 && visible === 0);
}

// Make htmx swaps work with modals/filters: surface failed requests, open the
// YAML modal once its content arrives, and reapply active search filters.
// The downloads poll swaps only the <tbody> (#downloads-rows), so the scroll
// container (.table-wrap) is never replaced and horizontal scroll/drags stay
// put across refreshes.
document.addEventListener("htmx:afterSwap", function (e) {
  var t = e.detail && e.detail.target;
  if (!t) return;
  if (t.id === "downloads-rows") {
    applyFilter("downloads-table");
  }
  if (t.id === "yml-viewer" && t.firstElementChild) {
    openModal("modal-yml");
  }
});

document.addEventListener("htmx:responseError", function (e) {
  var xhr = e.detail.xhr;
  var msg =
    (xhr && xhr.responseText && xhr.responseText.replace(/<[^>]*>/g, "").trim()) ||
    "request failed";
  showToast(msg.slice(0, 200), true);
});

document.addEventListener("htmx:sendError", function () {
  showToast("network error — is the server reachable?", true);
});

function showToast(message, isError) {
  var el = document.getElementById("toast");
  if (!el) {
    el = document.createElement("div");
    el.id = "toast";
    document.body.appendChild(el);
  }
  el.textContent = message;
  el.className = "toast show" + (isError ? " toast-error" : "");
  clearTimeout(el._timer);
  el._timer = setTimeout(function () {
    el.className = "toast";
  }, 4000);
}

// Flash messages (ok/error banners after redirects) dismiss themselves.
window.addEventListener("DOMContentLoaded", function () {
  setTimeout(function () {
    document.querySelectorAll(".flash.auto-dismiss").forEach(function (el) {
      el.classList.add("dismissed");
      setTimeout(function () {
        el.remove();
      }, 350);
    });
  }, 5000);
});
