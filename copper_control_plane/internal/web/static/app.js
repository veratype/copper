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

  // Live agent activity console (Activity page + DHCP page).
  (function initActivityConsole() {
    var el = document.getElementById("activity-console");
    if (!el) return;

    var statusEl = document.getElementById("activity-status");
    var filterEl = document.getElementById("activity-component");
    var clearBtn = document.getElementById("activity-clear");
    var baseURL = el.getAttribute("data-activity-url") || "/api/agent/activity";
    var fixedComponent = el.getAttribute("data-component") || "";
    var pollMs = parseInt(el.getAttribute("data-poll-ms") || "2000", 10);
    if (!pollMs || pollMs < 500) pollMs = 2000;

    var since = 0;
    var timer = null;
    var maxLines = 500;

    function component() {
      if (fixedComponent) return fixedComponent;
      return filterEl ? filterEl.value : "";
    }

    function setStatus(text, isError) {
      if (!statusEl) return;
      statusEl.textContent = text;
      statusEl.classList.toggle("is-error", !!isError);
    }

    function appendEvents(events) {
      if (!events || !events.length) return;
      var frag = document.createDocumentFragment();
      events.forEach(function (ev) {
        var line = document.createElement("div");
        line.className = "activity-line level-" + (ev.level || "info");
        var t = ev.time ? String(ev.time).replace("T", " ").replace("Z", "Z") : "";
        line.textContent =
          t + "  [" + (ev.component || "?") + "]  " + (ev.message || "");
        frag.appendChild(line);
      });
      el.appendChild(frag);
      while (el.childNodes.length > maxLines) {
        el.removeChild(el.firstChild);
      }
      el.scrollTop = el.scrollHeight;
    }

    function poll() {
      var q = "since=" + encodeURIComponent(String(since)) + "&limit=200";
      var c = component();
      if (c) q += "&component=" + encodeURIComponent(c);
      // First paint: empty since=0 would dump whole buffer; ask for a tail.
      if (since === 0) {
        q = "since=0&limit=100";
        if (c) q += "&component=" + encodeURIComponent(c);
      }

      fetch(baseURL + "?" + q, { credentials: "same-origin" })
        .then(function (resp) {
          return resp.json().then(function (body) {
            if (!resp.ok) {
              throw new Error((body && body.error) || resp.statusText || "fetch failed");
            }
            return body;
          });
        })
        .then(function (body) {
          var events = body.events || [];
          if (since === 0 && events.length) {
            // Snapshot: show all returned, then advance cursor.
            el.textContent = "";
            appendEvents(events);
          } else {
            appendEvents(events);
          }
          if (typeof body.next_since === "number" && body.next_since > since) {
            since = body.next_since;
          } else if (events.length) {
            since = events[events.length - 1].id;
          }
          setStatus("live · last id " + since, false);
        })
        .catch(function (err) {
          setStatus(err && err.message ? err.message : String(err), true);
        });
    }

    if (filterEl) {
      filterEl.addEventListener("change", function () {
        since = 0;
        el.textContent = "";
        setStatus("reloading…", false);
        poll();
      });
    }
    if (clearBtn) {
      clearBtn.addEventListener("click", function () {
        el.textContent = "";
      });
    }

    poll();
    timer = setInterval(poll, pollMs);
    window.addEventListener("beforeunload", function () {
      if (timer) clearInterval(timer);
    });
  })();

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
