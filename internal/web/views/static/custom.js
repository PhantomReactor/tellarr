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

// Make htmx swaps visible: scroll swapped-in content into view and surface
// failed requests instead of failing silently.
document.addEventListener("htmx:afterSwap", function (e) {
  var t = e.detail && e.detail.target;
  if (t && t.id === "yml-viewer" && t.firstElementChild) {
    t.scrollIntoView({ behavior: "smooth", block: "nearest" });
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
