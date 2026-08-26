// Copy-to-clipboard helper for YAML viewers and read-only fields.
// Falls back to select+execCommand on non-HTTPS origins.
function tellarrCopy(btn) {
  var ta = document.getElementById(btn.getAttribute("data-yml-id"));
  if (!ta) return;
  var prev = btn.innerHTML;
  var done = function () {
    btn.innerHTML = "Copied!";
    setTimeout(function () {
      btn.innerHTML = prev;
    }, 1500);
  };
  if (navigator.clipboard && navigator.clipboard.writeText) {
    navigator.clipboard.writeText(ta.value).then(done, function () {
      fallbackCopy(ta);
      done();
    });
  } else {
    fallbackCopy(ta);
    done();
  }
}

function fallbackCopy(ta) {
  ta.focus();
  ta.select();
  try {
    document.execCommand("copy");
  } catch (e) {}
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
  // on the X right after opening).
  var f = d.querySelector("input:not([type=hidden]), textarea, select");
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
document.addEventListener("htmx:afterSwap", function (e) {
  var t = e.detail && e.detail.target;
  if (!t) return;
  if (t.id === "yml-viewer" && t.firstElementChild) {
    openModal("modal-yml");
  }
  if (t.id === "downloads-table") {
    applyFilter("downloads-table");
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
