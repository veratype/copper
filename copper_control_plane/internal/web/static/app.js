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

  // Mount ISO: pick agent media file or type a URL manually.
  // Runs in capture before the busy overlay so image_url is filled first.
  (function initMountForm() {
    var form = document.getElementById("mount-form");
    if (!form) return;

    var libraryPanel = document.getElementById("mount-library");
    var manualPanel = document.getElementById("mount-manual");
    var fileSelect = document.getElementById("media-file-select");
    var baseInput = document.getElementById("media-base-url");
    var manualInput = document.getElementById("image-url-manual");
    var hiddenURL = document.getElementById("image-url");
    var radios = form.querySelectorAll('input[name="url_source"]');

    function source() {
      var checked = form.querySelector('input[name="url_source"]:checked');
      return checked ? checked.value : "manual";
    }

    function joinURL(base, path) {
      base = (base || "").replace(/\/+$/, "");
      path = (path || "").replace(/^\/+/, "");
      if (!base || !path) return "";
      return base + "/" + path;
    }

    function syncHidden() {
      if (source() === "library") {
        var opt = fileSelect && fileSelect.options[fileSelect.selectedIndex];
        var url = "";
        if (opt && opt.value) {
          url = opt.getAttribute("data-url") || "";
          if (!url) {
            url = joinURL(baseInput && baseInput.value, opt.getAttribute("data-path") || opt.value);
          }
        }
        if (hiddenURL) hiddenURL.value = url;
      } else if (hiddenURL && manualInput) {
        hiddenURL.value = (manualInput.value || "").trim();
      }
    }

    function toggle() {
      var lib = source() === "library";
      if (libraryPanel) {
        libraryPanel.hidden = !lib;
        libraryPanel.setAttribute("aria-hidden", lib ? "false" : "true");
      }
      if (manualPanel) {
        manualPanel.hidden = lib;
        manualPanel.setAttribute("aria-hidden", lib ? "true" : "false");
      }
      syncHidden();
    }

    radios.forEach(function (r) {
      r.addEventListener("change", toggle);
    });
    if (fileSelect) fileSelect.addEventListener("change", syncHidden);
    if (baseInput) baseInput.addEventListener("input", syncHidden);
    if (manualInput) manualInput.addEventListener("input", syncHidden);

    document.addEventListener(
      "submit",
      function (e) {
        if (e.target !== form) return;
        syncHidden();
        if (!hiddenURL || !(hiddenURL.value || "").trim()) {
          e.preventDefault();
          window.alert(
            source() === "library"
              ? "Select a media file (and OOB base URL if prompted)."
              : "Enter an image URL."
          );
        }
      },
      true
    );

    toggle();
  })();

  document.addEventListener(
    "submit",
    function (e) {
      var form = e.target;
      if (!(form instanceof HTMLFormElement) || !form.classList.contains("busy")) {
        return;
      }
      if (e.defaultPrevented) return;

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

  // Servers list: batch select + per-row progress + floating activity log.
  (function initBatchServers() {
    var page = document.getElementById("servers-page");
    var table = document.getElementById("servers-table");
    if (!page || !table) return;

    var checks = Array.prototype.slice.call(table.querySelectorAll(".batch-check"));
    var selectAll = document.getElementById("batch-select-all");
    var selectNone = document.getElementById("batch-select-none");
    var selectBound = document.getElementById("batch-select-bound");
    var countEl = document.getElementById("batch-count");
    var btnInv = document.getElementById("batch-inventory");
    var btnReboot = document.getElementById("batch-reboot");
    var btnMount = document.getElementById("batch-mount");
    var zipLink = document.getElementById("batch-zip");
    var mountPanel = document.getElementById("batch-mount-panel");
    var floatLog = document.getElementById("float-log");
    var floatBody = document.getElementById("float-log-body");
    var floatStatus = document.getElementById("float-log-status");
    var floatClear = document.getElementById("float-log-clear");
    var floatToggle = document.getElementById("float-log-toggle");
    var running = false;
    var activitySince = 0;
    var activityTimer = null;
    var lastZipIds = [];

    function eligibleChecks() {
      return checks.filter(function (c) {
        return !c.disabled;
      });
    }

    function selectedChecks() {
      return checks.filter(function (c) {
        return c.checked && !c.disabled;
      });
    }

    function updateCount() {
      var n = selectedChecks().length;
      if (countEl) countEl.textContent = n + " selected";
      if (btnInv) btnInv.disabled = running || n === 0;
      if (btnReboot) btnReboot.disabled = running || n === 0;
      if (btnMount) btnMount.disabled = running || n === 0;
      if (selectAll) {
        var elig = eligibleChecks();
        selectAll.checked = elig.length > 0 && elig.every(function (c) {
          return c.checked;
        });
        selectAll.indeterminate =
          elig.some(function (c) {
            return c.checked;
          }) && !selectAll.checked;
      }
    }

    function showFloat() {
      if (floatLog) floatLog.hidden = false;
    }

    function appendLocal(msg, level) {
      showFloat();
      if (!floatBody) return;
      var line = document.createElement("div");
      line.className = "log-line level-" + (level || "local");
      var t = new Date().toISOString().replace("T", " ").replace("Z", "Z");
      line.textContent = t + "  [batch]  " + msg;
      floatBody.appendChild(line);
      while (floatBody.childNodes.length > 400) {
        floatBody.removeChild(floatBody.firstChild);
      }
      floatBody.scrollTop = floatBody.scrollHeight;
    }

    function setFloatStatus(text, isError) {
      if (!floatStatus) return;
      floatStatus.textContent = text;
      floatStatus.classList.toggle("is-error", !!isError);
    }

    function startActivityFeed() {
      showFloat();
      if (activityTimer) return;
      function poll() {
        var q = "since=" + encodeURIComponent(String(activitySince)) + "&limit=100";
        if (activitySince === 0) q = "since=0&limit=40";
        fetch("/api/agent/activity?" + q, { credentials: "same-origin" })
          .then(function (resp) {
            return resp.json().then(function (body) {
              if (!resp.ok) throw new Error((body && body.error) || resp.statusText);
              return body;
            });
          })
          .then(function (body) {
            var events = body.events || [];
            events.forEach(function (ev) {
              if (!floatBody) return;
              var line = document.createElement("div");
              line.className = "log-line level-" + (ev.level || "info");
              var t = ev.time ? String(ev.time).replace("T", " ").replace("Z", "Z") : "";
              line.textContent =
                t + "  [" + (ev.component || "?") + "]  " + (ev.message || "");
              floatBody.appendChild(line);
            });
            if (events.length && floatBody) {
              while (floatBody.childNodes.length > 400) {
                floatBody.removeChild(floatBody.firstChild);
              }
              floatBody.scrollTop = floatBody.scrollHeight;
            }
            if (typeof body.next_since === "number" && body.next_since > activitySince) {
              activitySince = body.next_since;
            } else if (events.length) {
              activitySince = events[events.length - 1].id;
            }
            if (!running) setFloatStatus("live · last id " + activitySince, false);
          })
          .catch(function (err) {
            setFloatStatus(err && err.message ? err.message : String(err), true);
          });
      }
      poll();
      activityTimer = setInterval(poll, 2000);
    }

    function rowFor(id) {
      return table.querySelector('tr[data-server-id="' + id + '"]');
    }

    function setRowBusy(id, busy, msg, ok) {
      var row = rowFor(id);
      if (!row) return;
      var spin = row.querySelector(".row-status");
      var note = row.querySelector(".row-msg");
      if (spin) {
        spin.hidden = !busy;
        spin.setAttribute("aria-hidden", busy ? "false" : "true");
      }
      if (note) {
        if (msg) {
          note.hidden = false;
          note.textContent = msg;
          note.classList.toggle("is-error", ok === false);
          note.classList.toggle("is-ok", ok === true);
        } else if (!busy) {
          note.hidden = true;
          note.textContent = "";
          note.classList.remove("is-error", "is-ok");
        }
      }
    }

    function setZipLink(ids) {
      lastZipIds = ids || [];
      if (!zipLink) return;
      if (!lastZipIds.length) {
        zipLink.hidden = true;
        zipLink.removeAttribute("href");
        return;
      }
      zipLink.hidden = false;
      zipLink.href = "/api/servers/inventory.zip?ids=" + lastZipIds.join(",");
    }

    var runtimeHint = document.getElementById("batch-runtime-hint");

    function defaultBatchSettings() {
      return {
        executors: 3,
        timeouts: { inventory: 180, reboot: 60, mount: 180 },
      };
    }

    function loadBatchSettings() {
      return fetch("/api/settings/batch", { credentials: "same-origin" })
        .then(function (resp) {
          return resp.json().then(function (body) {
            if (!resp.ok || !body.ok || !body.settings) {
              throw new Error((body && body.error) || "settings unavailable");
            }
            return body.settings;
          });
        })
        .catch(function () {
          return defaultBatchSettings();
        });
    }

    function refreshRuntimeHint() {
      loadBatchSettings().then(function (s) {
        if (!runtimeHint) return;
        runtimeHint.innerHTML =
          '<a href="/settings">Settings</a>' +
          " · executors " +
          s.executors +
          " · timeouts inv/reboot/mount " +
          s.timeouts.inventory +
          "/" +
          s.timeouts.reboot +
          "/" +
          s.timeouts.mount +
          "s";
      });
    }

    function timeoutForAction(settings, action) {
      var t = settings && settings.timeouts ? settings.timeouts : {};
      if (action === "inventory") return (t.inventory || 180) * 1000;
      if (action === "reboot") return (t.reboot || 60) * 1000;
      if (action === "mount") return (t.mount || 180) * 1000;
      return 180000;
    }

    function postJSON(url, body, timeoutMs) {
      var ctrl = typeof AbortController !== "undefined" ? new AbortController() : null;
      var timer = null;
      if (ctrl && timeoutMs > 0) {
        timer = setTimeout(function () {
          ctrl.abort();
        }, timeoutMs);
      }
      return fetch(url, {
        method: "POST",
        credentials: "same-origin",
        headers: body ? { "Content-Type": "application/json" } : undefined,
        body: body ? JSON.stringify(body) : undefined,
        signal: ctrl ? ctrl.signal : undefined,
      })
        .then(function (resp) {
          return resp.json().then(function (data) {
            if (!resp.ok || (data && data.ok === false)) {
              throw new Error((data && data.error) || resp.statusText || "request failed");
            }
            return data;
          });
        })
        .catch(function (err) {
          if (err && err.name === "AbortError") {
            throw new Error("timed out after " + Math.round(timeoutMs / 1000) + "s");
          }
          throw err;
        })
        .finally(function () {
          if (timer) clearTimeout(timer);
        });
    }

    async function runPool(items, concurrency, workerFn) {
      var next = 0;
      var workers = [];
      concurrency = Math.max(1, Math.min(concurrency || 1, items.length || 1));
      async function worker() {
        while (true) {
          var idx = next++;
          if (idx >= items.length) return;
          await workerFn(items[idx], idx);
        }
      }
      for (var w = 0; w < concurrency; w++) workers.push(worker());
      await Promise.all(workers);
    }

    async function runBatch(label, action, actionFn) {
      if (running) return;
      var selected = selectedChecks();
      if (!selected.length) return;
      running = true;
      updateCount();
      startActivityFeed();
      var settings = await loadBatchSettings();
      var executors = Math.max(1, Math.min(settings.executors || 3, 16));
      var timeoutMs = timeoutForAction(settings, action);
      appendLocal(
        "starting " +
          label +
          " on " +
          selected.length +
          " server(s) · executors=" +
          executors +
          " · timeout=" +
          Math.round(timeoutMs / 1000) +
          "s",
        "local"
      );
      setFloatStatus("batch running: " + label + " ×" + executors, false);
      var okIds = [];
      var fail = 0;
      var done = 0;
      await runPool(selected, executors, async function (c) {
        var id = c.value;
        var name = c.getAttribute("data-name") || id;
        setRowBusy(id, true, "working…");
        appendLocal(name + " — " + label + " started", "local");
        try {
          await actionFn(id, name, timeoutMs);
          setRowBusy(id, false, "ok", true);
          okIds.push(id);
          appendLocal(name + " — ok", "local");
        } catch (err) {
          fail++;
          var msg = err && err.message ? err.message : String(err);
          setRowBusy(id, false, "failed", false);
          appendLocal(name + " — failed: " + msg, "error");
        } finally {
          done++;
          setFloatStatus(
            "batch " + label + " · " + done + "/" + selected.length + " · ×" + executors,
            false
          );
        }
      });
      running = false;
      updateCount();
      appendLocal(
        label + " finished: " + okIds.length + " ok, " + fail + " failed",
        fail ? "warn" : "local"
      );
      setFloatStatus(
        "batch done · " + okIds.length + " ok · " + fail + " failed",
        fail > 0
      );
      return okIds;
    }

    checks.forEach(function (c) {
      c.addEventListener("change", updateCount);
    });
    if (selectAll) {
      selectAll.addEventListener("change", function () {
        var on = selectAll.checked;
        eligibleChecks().forEach(function (c) {
          c.checked = on;
        });
        updateCount();
      });
    }
    if (selectNone) {
      selectNone.addEventListener("click", function () {
        checks.forEach(function (c) {
          c.checked = false;
        });
        updateCount();
      });
    }
    if (selectBound) {
      selectBound.addEventListener("click", function () {
        checks.forEach(function (c) {
          c.checked = !c.disabled && c.getAttribute("data-status") === "bound";
        });
        updateCount();
      });
    }

    if (btnInv) {
      btnInv.addEventListener("click", function () {
        runBatch("inventory", "inventory", function (id, _name, timeoutMs) {
          return postJSON("/api/servers/" + id + "/actions/inventory", null, timeoutMs);
        }).then(function (okIds) {
          if (okIds && okIds.length) setZipLink(okIds);
        });
      });
    }

    if (btnReboot) {
      btnReboot.addEventListener("click", function () {
        var n = selectedChecks().length;
        if (!window.confirm("Reboot " + n + " selected server(s)?")) return;
        runBatch("reboot", "reboot", function (id, _name, timeoutMs) {
          return postJSON("/api/servers/" + id + "/actions/reset", null, timeoutMs);
        });
      });
    }

    // Batch mount panel
    var batchLib = document.getElementById("batch-mount-library");
    var batchMan = document.getElementById("batch-mount-manual");
    var batchSelect = document.getElementById("batch-media-select");
    var batchBase = document.getElementById("batch-media-base");
    var batchBaseWrap = document.getElementById("batch-media-base-wrap");
    var batchManual = document.getElementById("batch-image-url-manual");
    var batchMeta = document.getElementById("batch-media-meta");
    var batchRadios = page.querySelectorAll('input[name="batch_url_source"]');
    var mediaBaseURL = "";

    function batchSource() {
      var checked = page.querySelector('input[name="batch_url_source"]:checked');
      return checked ? checked.value : "manual";
    }

    function toggleBatchMountSource() {
      var lib = batchSource() === "library";
      if (batchLib) batchLib.hidden = !lib;
      if (batchMan) batchMan.hidden = lib;
    }

    function resolveBatchImageURL() {
      if (batchSource() === "manual") {
        return ((batchManual && batchManual.value) || "").trim();
      }
      var opt = batchSelect && batchSelect.options[batchSelect.selectedIndex];
      if (!opt || !opt.value) return "";
      var url = opt.getAttribute("data-url") || "";
      if (url) return url;
      var base = ((batchBase && batchBase.value) || mediaBaseURL || "").replace(/\/+$/, "");
      var path = (opt.getAttribute("data-path") || opt.value || "").replace(/^\/+/, "");
      if (!base || !path) return "";
      return base + "/" + path;
    }

    function loadMediaCatalog() {
      if (!batchSelect) return Promise.resolve();
      batchSelect.innerHTML = '<option value="">— loading —</option>';
      return fetch("/api/media/files", { credentials: "same-origin" })
        .then(function (resp) {
          return resp.json().then(function (body) {
            if (!resp.ok || !body.ok) {
              throw new Error((body && body.error) || resp.statusText);
            }
            return body.catalog || {};
          });
        })
        .then(function (cat) {
          mediaBaseURL = (cat.base_url || "").replace(/\/+$/, "");
          if (batchMeta) {
            batchMeta.textContent =
              "Folder " +
              (cat.folder || "?") +
              (mediaBaseURL ? " · base " + mediaBaseURL : "") +
              (cat.note ? " — " + cat.note : "");
          }
          if (batchBaseWrap) batchBaseWrap.hidden = !!mediaBaseURL;
          if (batchBase && mediaBaseURL) batchBase.value = mediaBaseURL;
          var files = cat.files || [];
          batchSelect.innerHTML = "";
          var ph = document.createElement("option");
          ph.value = "";
          ph.textContent = files.length ? "— select a file —" : "— no files —";
          batchSelect.appendChild(ph);
          files.forEach(function (f) {
            var opt = document.createElement("option");
            opt.value = f.path;
            opt.setAttribute("data-path", f.path);
            var full = mediaBaseURL ? mediaBaseURL + "/" + String(f.path).replace(/^\/+/, "") : "";
            if (full) opt.setAttribute("data-url", full);
            var size = typeof f.size === "number" ? " (" + Math.round(f.size / (1024 * 1024)) + " MiB)" : "";
            opt.textContent = f.path + size;
            batchSelect.appendChild(opt);
          });
        })
        .catch(function (err) {
          batchSelect.innerHTML = '<option value="">— failed to load —</option>';
          if (batchMeta) batchMeta.textContent = err && err.message ? err.message : String(err);
          appendLocal("media catalog: " + (err && err.message ? err.message : String(err)), "error");
        });
    }

    batchRadios.forEach(function (r) {
      r.addEventListener("change", toggleBatchMountSource);
    });
    toggleBatchMountSource();

    if (btnMount) {
      btnMount.addEventListener("click", function () {
        if (!mountPanel) return;
        mountPanel.hidden = false;
        startActivityFeed();
        loadMediaCatalog();
        mountPanel.scrollIntoView({ behavior: "smooth", block: "nearest" });
      });
    }
    var mountCancel = document.getElementById("batch-mount-cancel");
    if (mountCancel) {
      mountCancel.addEventListener("click", function () {
        if (mountPanel) mountPanel.hidden = true;
      });
    }
    var mountRun = document.getElementById("batch-mount-run");
    if (mountRun) {
      mountRun.addEventListener("click", function () {
        var imageURL = resolveBatchImageURL();
        if (!imageURL) {
          window.alert(
            batchSource() === "library"
              ? "Select a media file (and OOB base URL if prompted)."
              : "Enter an image URL."
          );
          return;
        }
        var bootOnceEl = document.getElementById("batch-mount-boot-once");
        var rebootEl = document.getElementById("batch-mount-reboot");
        var bootOnce = !!(bootOnceEl && bootOnceEl.checked);
        var reboot = !!(rebootEl && rebootEl.checked);
        var n = selectedChecks().length;
        if (!window.confirm("Mount & boot on " + n + " server(s)?\n" + imageURL)) return;
        if (mountPanel) mountPanel.hidden = true;
        runBatch("mount & boot", "mount", function (id, _name, timeoutMs) {
          return postJSON(
            "/api/servers/" + id + "/actions/mount",
            {
              image_url: imageURL,
              boot_once: bootOnce,
              reboot: reboot,
            },
            timeoutMs
          );
        });
      });
    }

    if (floatClear) {
      floatClear.addEventListener("click", function () {
        if (floatBody) floatBody.textContent = "";
      });
    }
    if (floatToggle) {
      floatToggle.addEventListener("click", function () {
        if (!floatLog) return;
        floatLog.classList.toggle("is-minimized");
        floatToggle.textContent = floatLog.classList.contains("is-minimized")
          ? "Expand"
          : "Minimize";
      });
    }

    (function initFloatLogDrag() {
      if (!floatLog) return;
      var handle = document.getElementById("float-log-drag");
      if (!handle) return;
      var storageKey = "copper_float_log_pos";
      var dragging = false;
      var startX = 0;
      var startY = 0;
      var originLeft = 0;
      var originTop = 0;

      function clamp(n, min, max) {
        return Math.max(min, Math.min(max, n));
      }

      function applyPos(left, top) {
        var rect = floatLog.getBoundingClientRect();
        var maxLeft = Math.max(0, window.innerWidth - rect.width);
        var maxTop = Math.max(0, window.innerHeight - rect.height);
        left = clamp(left, 0, maxLeft);
        top = clamp(top, 0, maxTop);
        floatLog.style.left = left + "px";
        floatLog.style.top = top + "px";
        floatLog.style.right = "auto";
        floatLog.style.bottom = "auto";
        return { left: left, top: top };
      }

      function ensureRestored() {
        if (floatLog.dataset.posReady === "1") return;
        floatLog.dataset.posReady = "1";
        try {
          var raw = localStorage.getItem(storageKey);
          if (!raw) return;
          var pos = JSON.parse(raw);
          if (typeof pos.left === "number" && typeof pos.top === "number") {
            applyPos(pos.left, pos.top);
          }
        } catch (_e) {}
      }

      var showObserver = new MutationObserver(function () {
        if (!floatLog.hidden) ensureRestored();
      });
      showObserver.observe(floatLog, { attributes: true, attributeFilter: ["hidden"] });

      handle.addEventListener("pointerdown", function (e) {
        if (e.button != null && e.button !== 0) return;
        if (e.target && e.target.closest && e.target.closest("button, a, input, select, textarea")) {
          return;
        }
        ensureRestored();
        var rect = floatLog.getBoundingClientRect();
        applyPos(rect.left, rect.top);
        dragging = true;
        floatLog.classList.add("is-dragging");
        startX = e.clientX;
        startY = e.clientY;
        originLeft = rect.left;
        originTop = rect.top;
        handle.setPointerCapture(e.pointerId);
        e.preventDefault();
      });

      handle.addEventListener("pointermove", function (e) {
        if (!dragging) return;
        applyPos(originLeft + (e.clientX - startX), originTop + (e.clientY - startY));
      });

      function endDrag(e) {
        if (!dragging) return;
        dragging = false;
        floatLog.classList.remove("is-dragging");
        try {
          handle.releasePointerCapture(e.pointerId);
        } catch (_e) {}
        var rect = floatLog.getBoundingClientRect();
        var pos = applyPos(rect.left, rect.top);
        try {
          localStorage.setItem(storageKey, JSON.stringify(pos));
        } catch (_e) {}
      }

      handle.addEventListener("pointerup", endDrag);
      handle.addEventListener("pointercancel", endDrag);
    })();

    updateCount();
    refreshRuntimeHint();
  })();
})();
