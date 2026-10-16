(function () {
  var overlay = document.getElementById("busy-overlay");
  var label = overlay ? overlay.querySelector(".busy-label") : null;

  function showBusy(message) {
    if (!overlay) return;
    if (label) label.textContent = message || "Working…";
    overlay.hidden = false;
    document.body.classList.add("is-busy");
  }

  function hideBusy() {
    if (!overlay) return;
    overlay.hidden = true;
    document.body.classList.remove("is-busy");
  }

  document.addEventListener(
    "submit",
    function (e) {
      var form = e.target;
      if (!(form instanceof HTMLFormElement) || !form.classList.contains("busy")) {
        return;
      }

      var confirmMsg = form.getAttribute("data-confirm");
      if (confirmMsg && !window.confirm(confirmMsg)) {
        e.preventDefault();
        return;
      }

      if (typeof form.checkValidity === "function" && !form.checkValidity()) {
        return;
      }

      showBusy(form.getAttribute("data-busy") || "Working…");
    },
    true
  );

  // Fetch-based download so the same export URL works for curl loops and the UI,
  // without leaving the page stuck on the busy overlay.
  document.addEventListener("click", function (e) {
    var btn = e.target.closest("[data-export-url]");
    if (!btn) return;
    e.preventDefault();

    var url = btn.getAttribute("data-export-url");
    if (!url) return;

    showBusy(btn.getAttribute("data-busy") || "Exporting…");

    fetch(url, { credentials: "same-origin" })
      .then(function (resp) {
        if (!resp.ok) {
          return resp.text().then(function (t) {
            throw new Error(t || resp.statusText || "export failed");
          });
        }
        var cd = resp.headers.get("Content-Disposition") || "";
        var match = /filename\*?=(?:UTF-8''|")?([^\";]+)/i.exec(cd);
        var filename = match ? decodeURIComponent(match[1].replace(/"/g, "")) : "inventory.json";
        return resp.blob().then(function (blob) {
          return { blob: blob, filename: filename };
        });
      })
      .then(function (file) {
        var objectUrl = URL.createObjectURL(file.blob);
        var a = document.createElement("a");
        a.href = objectUrl;
        a.download = file.filename;
        document.body.appendChild(a);
        a.click();
        a.remove();
        URL.revokeObjectURL(objectUrl);
      })
      .catch(function (err) {
        window.alert(err && err.message ? err.message : String(err));
      })
      .finally(hideBusy);
  });
})();
