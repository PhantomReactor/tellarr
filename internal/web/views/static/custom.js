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
