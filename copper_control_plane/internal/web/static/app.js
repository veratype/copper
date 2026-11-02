// Job dispatcher lives in dispatcher.js (window.CopperJobs). Do not redefine it here.

// Caution helpers for servers labeled "installed" (OS already provisioned).
window.CopperInstalled = (function () {
  var LABEL = "installed";

  function normList(labels) {
    if (!labels) return [];
    if (Array.isArray(labels)) return labels;
    return String(labels)
      .split(/[,;\n\r]+/)
      .map(function (s) {
        return s.replace(/\s+/g, " ").trim();
      })
      .filter(Boolean);
  }

  function hasLabel(labels) {
    return normList(labels).some(function (l) {
      return String(l).toLowerCase() === LABEL;
    });
  }

  function installedNamesFromChecks(checks) {
    var names = [];
    (checks || []).forEach(function (c) {
      var row = c.closest ? c.closest("tr") : null;
      var raw =
        (c.getAttribute("data-labels") || "") ||
        (row && row.getAttribute("data-labels")) ||
        "";
      if (!hasLabel(raw)) return;
      names.push(c.getAttribute("data-name") || (row && row.getAttribute("data-server-name")) || c.value);
    });
    return names;
  }

  function installedNamesFromMeta(meta) {
    var names = [];
    (meta || []).forEach(function (m) {
      if (!m || !hasLabel(m.labels)) return;
      names.push(m.name || ("#" + m.id));
    });
    return names;
  }

  function cautionBody(names, action) {
    return (
      "CAUTION — labeled \"" +
      LABEL +
      "\" (OS already provisioned):\n\n• " +
      names.join("\n• ") +
      "\n\n" +
      action +
      " can interrupt or destroy a running system.\n\nContinue only if you intend this."
    );
  }

  function confirmDestructive(names, action) {
    if (!names || !names.length) return true;
    return window.confirm(cautionBody(names, action));
  }

  function confirmRemoveInstalled(names) {
    var who =
      names && names.length
        ? "\n\n• " + names.join("\n• ")
        : "";
    return window.confirm(
      "CAUTION — remove the \"" +
        LABEL +
        "\" label?" +
        who +
        "\n\nThat label marks a finished OS install. Removing it hides reboot / storage / deploy warnings.\n\nContinue only if the host was rebuilt, decommissioned, or the label was wrong."
    );
  }

  return {
    LABEL: LABEL,
    hasLabel: hasLabel,
    installedNamesFromChecks: installedNamesFromChecks,
    installedNamesFromMeta: installedNamesFromMeta,
    confirmDestructive: confirmDestructive,
    confirmRemoveInstalled: confirmRemoveInstalled,
  };
})();

// Shared Host partitions panel (Storage + Soft RAID).
// Mount: CopperHostPartitions.mount({ root, serverIds, nameById, ...hooks })
window.CopperHostPartitions = (function () {
  var ROW_H = 36;
  var SCAN_CONCURRENCY = 8;

  function esc(s) {
    return String(s == null ? "" : s).replace(/[&<>"']/g, function (c) {
      return { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c];
    });
  }

  function mapPool(items, concurrency, worker) {
    var i = 0,
      active = 0,
      rejected = null;
    return new Promise(function (resolve, reject) {
      function pump() {
        if (rejected) return;
        if (i >= items.length && active === 0) {
          resolve();
          return;
        }
        while (active < concurrency && i < items.length) {
          (function (item) {
            active++;
            Promise.resolve(worker(item))
              .then(function () {
                active--;
                pump();
              })
              .catch(function (err) {
                rejected = err;
                reject(err);
              });
          })(items[i]);
          i++;
        }
      }
      pump();
    });
  }

  function mount(opts) {
    opts = opts || {};
    var root =
      typeof opts.root === "string"
        ? document.querySelector(opts.root)
        : opts.root;
    if (!root) return null;
    if (root.getAttribute("data-hp-mounted") === "1") return root._copperHP || null;

    var serverIds = (opts.serverIds || []).slice();
    var nameById = opts.nameById || {};
    function isBusy() {
      return opts.isBusy ? !!opts.isBusy() : !!root._hpBusy;
    }
    function setBusy(v) {
      root._hpBusy = !!v;
      if (opts.setBusy) opts.setBusy(!!v);
    }
    function log(msg, level) {
      if (opts.log) opts.log(msg, level || "local");
    }
    function notify(msg, isError) {
      if (opts.notify) opts.notify(msg, !!isError);
    }
    function showActivity() {
      if (opts.showActivity) opts.showActivity();
    }
    function setActivityStatus(text, isError) {
      if (opts.setActivityStatus) opts.setActivityStatus(text, !!isError);
    }
    function clearBanner() {
      if (opts.clearBanner) opts.clearBanner();
    }
    function confirmWipe(title, body, cmd) {
      if (opts.confirm) return Promise.resolve(opts.confirm(title, body, cmd));
      return Promise.resolve(window.confirm(title + "\n\n" + body));
    }
    function waitJob(jobId) {
      if (opts.waitJob) return opts.waitJob(jobId);
      if (window.CopperJobs && CopperJobs.pollUntilDone) {
        return CopperJobs.pollUntilDone(jobId, 600000);
      }
      return Promise.reject(new Error("job poller missing"));
    }
    function diskExtraHTML(d) {
      return opts.diskExtraHTML ? opts.diskExtraHTML(d) || "" : "";
    }
    function footerHTML(scan) {
      return opts.footerHTML ? opts.footerHTML(scan) || "" : "";
    }

    function $(sel) {
      return root.querySelector(sel);
    }

    var partsById = Object.create(null);
    var partsChecked = new Set();
    var partsFocusId = null;
    var partsFilter = "";
    var partsListEls = null;
    var partsScanGen = 0;

    function partsSummary(scan) {
      var disks = 0,
        parts = 0,
        esp = 0,
        wipeable = false;
      (scan && scan.disks ? scan.disks : []).forEach(function (d) {
        disks++;
        if (!d.protected) wipeable = true;
        (d.partitions || []).forEach(function (p) {
          parts++;
          if (p.esp) esp++;
        });
      });
      return { disks: disks, parts: parts, esp: esp, wipeable: wipeable };
    }

    function partsEntryMeta(entry) {
      if (!entry) return { cls: "", text: "—" };
      if (entry.error) return { cls: "err", text: "error" };
      if (!entry.scan) return { cls: "", text: "…" };
      if (entry.esp) return { cls: "esp", text: entry.esp + " ESP" };
      if (entry.parts)
        return {
          cls: "ok",
          text: entry.parts + " part" + (entry.parts === 1 ? "" : "s"),
        };
      return {
        cls: "ok",
        text: entry.disks + " disk" + (entry.disks === 1 ? "" : "s"),
      };
    }

    function partsFilteredIds() {
      var q = (partsFilter || "").trim().toLowerCase();
      var out = [];
      for (var i = 0; i < serverIds.length; i++) {
        var id = serverIds[i];
        if (!partsById[id]) continue;
        if (!q) {
          out.push(id);
          continue;
        }
        var name = (nameById[id] || String(id)).toLowerCase();
        var err = (partsById[id].error || "").toLowerCase();
        if (
          name.indexOf(q) >= 0 ||
          String(id).indexOf(q) >= 0 ||
          err.indexOf(q) >= 0
        )
          out.push(id);
      }
      return out;
    }

    function partsUpdateStats() {
      var scanned = 0,
        errors = 0,
        withEsp = 0,
        wipeableN = 0;
      Object.keys(partsById).forEach(function (k) {
        var e = partsById[k];
        scanned++;
        if (e.error) errors++;
        if (e.esp) withEsp++;
        if (e.wipeable) wipeableN++;
      });
      var checked = 0;
      partsChecked.forEach(function () {
        checked++;
      });
      var el = $('[data-hp="stats"]');
      if (el) {
        el.innerHTML =
          "<b>" +
          checked +
          "</b> selected · <b>" +
          scanned +
          "</b>/" +
          serverIds.length +
          " scanned" +
          (errors ? " · <b>" + errors + "</b> errors" : "") +
          (withEsp ? " · <b>" + withEsp + "</b> with ESP" : "") +
          " · <b>" +
          wipeableN +
          "</b> wipeable";
      }
      var hasScanned = scanned > 0;
      ['[data-hp="select-all"]', '[data-hp="select-esp"]', '[data-hp="select-none"]'].forEach(
        function (sel) {
          var b = $(sel);
          if (b) b.disabled = !hasScanned;
        }
      );
      var wipeBtn = $('[data-hp="wipe"]');
      if (wipeBtn) {
        var can = false;
        partsChecked.forEach(function (id) {
          var e = partsById[id];
          if (e && e.wipeable && !e.error) can = true;
        });
        wipeBtn.disabled = !can;
        wipeBtn.textContent = checked
          ? "Clear selected partitions (" + checked + ")"
          : "Clear selected partitions";
      }
      var tb = $('[data-hp="toolbar"]');
      if (tb) tb.hidden = !hasScanned;
    }

    function partsTableHTML(scan) {
      if (!scan || !(scan.disks || []).length) {
        return '<p class="parts-empty">No disks reported by lsblk.</p>';
      }
      var rows = [];
      scan.disks.forEach(function (d) {
        var link = diskExtraHTML(d);
        var prot = d.protected
          ? ' <span class="prot">protected' +
            (d.protect_reason ? ": " + esc(d.protect_reason) : "") +
            "</span>"
          : "";
        rows.push(
          '<tr class="disk-row"><td colspan="6"><code>' +
            esc(d.path) +
            "</code> · " +
            esc(d.size || "?") +
            (d.model ? " · " + esc(d.model) : "") +
            (d.tran ? " · " + esc(d.tran) : "") +
            link +
            prot +
            "</td></tr>"
        );
        var parts = d.partitions || [];
        if (!parts.length) {
          rows.push('<tr><td colspan="6" class="sub">no partitions</td></tr>');
          return;
        }
        parts.forEach(function (p) {
          var tags = [];
          if (p.esp) tags.push('<span class="esp">ESP</span>');
          if (p.mountpoint) tags.push("mnt " + esc(p.mountpoint));
          rows.push(
            "<tr><td><code>" +
              esc(p.path) +
              "</code></td><td>" +
              esc(p.size || "") +
              "</td><td>" +
              esc(p.fstype || p.type || "") +
              "</td>" +
              "<td>" +
              esc(p.partlabel || p.label || "") +
              "</td><td><code>" +
              esc(p.partuuid || "") +
              "</code></td><td>" +
              tags.join(" · ") +
              "</td></tr>"
          );
        });
      });
      return (
        '<table class="parts-table"><thead><tr><th>Device</th><th>Size</th><th>FS/type</th><th>Label</th><th>PARTUUID</th><th></th></tr></thead><tbody>' +
        rows.join("") +
        "</tbody></table>" +
        (scan.live_disk
          ? '<p class="hint">Live disk: <code>' + esc(scan.live_disk) + "</code></p>"
          : "") +
        footerHTML(scan)
      );
    }

    function partsRenderDetail() {
      if (!partsListEls || !partsListEls.detail) return;
      var detail = partsListEls.detail;
      var id = partsFocusId;
      if (id == null) {
        detail.innerHTML =
          '<p class="parts-empty">Select a server in the list to inspect partitions.</p>';
        return;
      }
      var entry = partsById[id];
      var title =
        "<h3>" +
        esc(nameById[id] || String(id)) +
        ' <span class="sub">#' +
        id +
        "</span></h3>";
      if (!entry) {
        detail.innerHTML = title + '<p class="parts-empty">Not scanned yet.</p>';
        return;
      }
      if (entry.error) {
        detail.innerHTML = title + '<p class="error">' + esc(entry.error) + "</p>";
        return;
      }
      detail.innerHTML = title + partsTableHTML(entry.scan);
    }

    function partsPaintList() {
      if (!partsListEls || !partsListEls.list || !partsListEls.inner) return;
      var ids = partsFilteredIds();
      var list = partsListEls.list;
      var inner = partsListEls.inner;
      inner.style.height = ids.length * ROW_H + "px";
      var scrollTop = list.scrollTop;
      var viewH = list.clientHeight || 280;
      var start = Math.max(0, Math.floor(scrollTop / ROW_H) - 4);
      var end = Math.min(ids.length, Math.ceil((scrollTop + viewH) / ROW_H) + 4);
      var html = [];
      for (var i = start; i < end; i++) {
        var id = ids[i];
        var entry = partsById[id];
        var meta = partsEntryMeta(entry);
        var checked = partsChecked.has(id);
        var active = id === partsFocusId;
        html.push(
          '<div class="parts-row' +
            (active ? " active" : "") +
            '" data-id="' +
            id +
            '" style="top:' +
            i * ROW_H +
            "px;height:" +
            ROW_H +
            'px">' +
            '<input type="checkbox" data-check="' +
            id +
            '"' +
            (checked ? " checked" : "") +
            (entry && entry.wipeable && !entry.error ? "" : " disabled") +
            ' aria-label="Select for wipe">' +
            '<span class="nm" title="' +
            esc(nameById[id] || String(id)) +
            '">' +
            esc(nameById[id] || String(id)) +
            "</span>" +
            '<span class="meta ' +
            meta.cls +
            '">' +
            esc(meta.text) +
            "</span></div>"
        );
      }
      inner.innerHTML = html.join("");
    }

    function partsEnsureShell() {
      var el = $('[data-hp="view"]');
      if (!el) return;
      if (el.dataset.shell === "1" && partsListEls) return;
      el.className = "";
      el.dataset.shell = "1";
      el.innerHTML =
        '<div class="parts-layout">' +
        '<div class="parts-list-wrap">' +
        '<div class="parts-list-head">' +
        '<label><input type="checkbox" data-hp="head-check"> visible</label>' +
        "<span>Click a row to inspect · check to wipe</span>" +
        "</div>" +
        '<div class="parts-list" data-hp="list"><div class="parts-list-inner" data-hp="list-inner"></div></div>' +
        "</div>" +
        '<div class="parts-detail" data-hp="detail"></div>' +
        "</div>";
      partsListEls = {
        list: $('[data-hp="list"]'),
        inner: $('[data-hp="list-inner"]'),
        detail: $('[data-hp="detail"]'),
      };
      partsListEls.list.addEventListener(
        "scroll",
        function () {
          partsPaintList();
        },
        { passive: true }
      );
      partsListEls.list.addEventListener("click", function (e) {
        var t = e.target;
        if (t && t.getAttribute && t.getAttribute("data-check") != null) {
          var cid = +t.getAttribute("data-check");
          if (t.checked) partsChecked.add(cid);
          else partsChecked.delete(cid);
          partsUpdateStats();
          e.stopPropagation();
          return;
        }
        var row = t && t.closest ? t.closest(".parts-row") : null;
        if (!row) return;
        partsFocusId = +row.getAttribute("data-id");
        partsPaintList();
        partsRenderDetail();
      });
      var head = $('[data-hp="head-check"]');
      if (head) {
        head.addEventListener("change", function () {
          var ids = partsFilteredIds();
          if (head.checked) {
            ids.forEach(function (id) {
              var e = partsById[id];
              if (e && e.wipeable && !e.error) partsChecked.add(id);
            });
          } else {
            ids.forEach(function (id) {
              partsChecked.delete(id);
            });
          }
          partsUpdateStats();
          partsPaintList();
        });
      }
    }

    function partsRefreshUI() {
      partsEnsureShell();
      partsUpdateStats();
      partsPaintList();
      partsRenderDetail();
    }

    function partsStoreResult(id, scan, error, storeOpts) {
      storeOpts = storeOpts || {};
      if (error) {
        partsById[id] = {
          scan: null,
          error: String(error),
          disks: 0,
          parts: 0,
          esp: 0,
          wipeable: false,
        };
        partsChecked.delete(id);
        return;
      }
      var sum = partsSummary(scan);
      partsById[id] = {
        scan: scan,
        error: null,
        disks: sum.disks,
        parts: sum.parts,
        esp: sum.esp,
        wipeable: sum.wipeable,
      };
      if (storeOpts.autoCheck && sum.wipeable) partsChecked.add(id);
      else if (!sum.wipeable) partsChecked.delete(id);
    }

    async function scanPartsOnServer(id) {
      var r = await fetch("/api/servers/" + id + "/host/partitions", {
        credentials: "same-origin",
      });
      var j = await r.json().catch(function () {
        return {};
      });
      if (!r.ok || j.ok === false)
        throw new Error(j.error || "partitions " + r.status);
      return j.scan || {};
    }

    async function scanParts() {
      if (isBusy()) return;
      setBusy(true);
      clearBanner();
      showActivity();
      var gen = ++partsScanGen;
      partsById = Object.create(null);
      partsChecked = new Set();
      partsFocusId = serverIds[0] || null;
      partsEnsureShell();
      partsRefreshUI();
      setActivityStatus("scanning partitions…", false);
      var done = 0,
        okN = 0,
        failN = 0;
      var total = serverIds.length;
      try {
        log(
          "scanning partitions on " +
            total +
            " server(s) (concurrency " +
            SCAN_CONCURRENCY +
            ")…",
          "local"
        );
        serverIds.forEach(function (id) {
          partsById[id] = {
            scan: null,
            error: null,
            disks: 0,
            parts: 0,
            esp: 0,
            wipeable: false,
          };
        });
        partsRefreshUI();
        var lastPaint = 0;
        await mapPool(serverIds.slice(), SCAN_CONCURRENCY, async function (id) {
          if (gen !== partsScanGen) return;
          var name = nameById[id] || String(id);
          try {
            var scan = await scanPartsOnServer(id);
            if (gen !== partsScanGen) return;
            partsStoreResult(id, scan, null, { autoCheck: total <= 20 });
            okN++;
            var sum = partsSummary(scan);
            if (
              total <= 40 ||
              okN === 1 ||
              okN === total ||
              okN % 25 === 0 ||
              sum.esp
            ) {
              log(
                name +
                  " — " +
                  sum.disks +
                  " disks / " +
                  sum.parts +
                  " parts" +
                  (sum.esp ? " · " + sum.esp + " ESP" : ""),
                "local"
              );
            }
          } catch (e) {
            if (gen !== partsScanGen) return;
            partsStoreResult(id, null, e.message || e, {});
            failN++;
            log(name + " — scan failed: " + (e.message || e), "error");
          }
          done++;
          setActivityStatus(
            "scan " + done + "/" + total + (failN ? " · " + failN + " failed" : ""),
            failN > 0
          );
          var now = Date.now();
          if (now - lastPaint > 200 || done === total) {
            lastPaint = now;
            partsUpdateStats();
            partsPaintList();
            if (partsFocusId === id) partsRenderDetail();
          }
        });
        if (gen !== partsScanGen) return;
        if (partsFocusId == null || !partsById[partsFocusId]) {
          partsFocusId = serverIds[0] || null;
        }
        partsRefreshUI();
        notify(
          "Scanned " +
            okN +
            " server(s)" +
            (failN ? " · " + failN + " failed" : "") +
            " — check servers to wipe, then Clear selected"
        );
        log("scan finished: " + okN + " ok, " + failN + " failed", failN ? "warn" : "local");
        setActivityStatus("scan done · " + okN + " ok · " + failN + " failed", failN > 0);
      } catch (e) {
        notify(String(e.message || e), true);
        log(String(e.message || e), "error");
        setActivityStatus("scan failed", true);
        partsRefreshUI();
      } finally {
        setBusy(false);
      }
    }

    async function wipePartsOnServer(id) {
      var r = await fetch("/api/servers/" + id + "/storage/wipe-partitions", {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: "{}",
      });
      var j = await r.json().catch(function () {
        return {};
      });
      if (!r.ok || j.ok === false)
        return { ok: false, error: j.error || "HTTP " + r.status };
      if (j.job_id) {
        try {
          await waitJob(j.job_id);
          return { ok: true };
        } catch (e) {
          return { ok: false, error: String(e && e.message ? e.message : e) };
        }
      }
      return { ok: true };
    }

    async function wipeParts() {
      if (isBusy()) return;
      var ids = [];
      partsChecked.forEach(function (id) {
        var e = partsById[id];
        if (e && e.wipeable && !e.error) ids.push(id);
      });
      if (!ids.length) {
        notify(
          "No wipeable servers selected — scan first, then check servers in the list",
          true
        );
        return;
      }
      var body =
        "Deletes ALL partition tables (including ESP) on every non-protected disk on " +
        ids.length +
        " selected server(s). Live-OS / install media is skipped. Does not zero the whole disk. This cannot be undone.";
      var ok = await confirmWipe(
        "Clear partitions on " + ids.length + " server(s)?",
        body,
        "sgdisk -Z / wipefs -a on each disk (except live)"
      );
      if (!ok) return;
      setBusy(true);
      clearBanner();
      showActivity();
      setActivityStatus("wiping partitions…", false);
      try {
        var okN = 0,
          failN = 0,
          done = 0;
        var total = ids.length;
        // Different servers run in parallel (job registry serializes only per-server).
        // Same pool width as partition scan so large batches don't enqueue one-by-one.
        log(
          "wiping partitions on " +
            total +
            " server(s) (concurrency " +
            SCAN_CONCURRENCY +
            ")…",
          "local"
        );
        await mapPool(ids.slice(), SCAN_CONCURRENCY, async function (id) {
          var name = nameById[id] || String(id);
          log(name + " — wipe partitions started", "local");
          var res = await wipePartsOnServer(id);
          if (res.ok) {
            okN++;
            log(name + " — wipe ok", "local");
            try {
              var scan = await scanPartsOnServer(id);
              partsStoreResult(id, scan, null, { autoCheck: false });
              partsChecked.delete(id);
            } catch (e) {
              partsStoreResult(id, null, e.message || e, {});
            }
          } else {
            failN++;
            log(name + " — wipe failed: " + (res.error || "error"), "error");
          }
          done++;
          setActivityStatus(
            "wipe " + done + "/" + total + (failN ? " · " + failN + " failed" : ""),
            failN > 0
          );
          partsUpdateStats();
          partsPaintList();
          if (partsFocusId === id) partsRenderDetail();
        });
        log("wipe finished: " + okN + " ok, " + failN + " failed", failN ? "warn" : "local");
        setActivityStatus("wipe done · " + okN + " ok · " + failN + " failed", failN > 0);
        if (failN) notify("Partition wipe finished with errors — see log", true);
        else notify("Partitions cleared on " + okN + " server(s)");
        partsRefreshUI();
      } finally {
        setBusy(false);
      }
    }

    function partsSelectAllScanned() {
      Object.keys(partsById).forEach(function (k) {
        var id = +k;
        var e = partsById[id];
        if (e && e.wipeable && !e.error) partsChecked.add(id);
      });
      partsUpdateStats();
      partsPaintList();
    }
    function partsSelectWithEsp() {
      partsChecked = new Set();
      Object.keys(partsById).forEach(function (k) {
        var id = +k;
        var e = partsById[id];
        if (e && e.wipeable && !e.error && e.esp > 0) partsChecked.add(id);
      });
      partsUpdateStats();
      partsPaintList();
    }
    function partsSelectNone() {
      partsChecked = new Set();
      partsUpdateStats();
      partsPaintList();
    }

    var scanBtn = $('[data-hp="scan"]');
    var wipeBtn = $('[data-hp="wipe"]');
    var selAll = $('[data-hp="select-all"]');
    var selEsp = $('[data-hp="select-esp"]');
    var selNone = $('[data-hp="select-none"]');
    if (scanBtn) scanBtn.addEventListener("click", scanParts);
    if (wipeBtn) wipeBtn.addEventListener("click", wipeParts);
    if (selAll) selAll.addEventListener("click", partsSelectAllScanned);
    if (selEsp) selEsp.addEventListener("click", partsSelectWithEsp);
    if (selNone) selNone.addEventListener("click", partsSelectNone);
    var filterEl = $('[data-hp="filter"]');
    if (filterEl) {
      var filterTimer = null;
      filterEl.addEventListener("input", function () {
        partsFilter = filterEl.value || "";
        if (filterTimer) clearTimeout(filterTimer);
        filterTimer = setTimeout(function () {
          partsPaintList();
        }, 80);
      });
    }

    root.setAttribute("data-hp-mounted", "1");
    var api = {
      refreshAnnotations: function () {
        if (partsFocusId != null) partsRenderDetail();
      },
      setServerIds: function (ids, names) {
        serverIds = (ids || []).slice();
        if (names) nameById = names;
      },
    };
    root._copperHP = api;
    return api;
  }

  return { mount: mount };
})();

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

  function escText(s) {
    var d = document.createElement("div");
    d.textContent = s == null ? "" : String(s);
    return d.innerHTML;
  }

  function shortType(t) {
    t = String(t || "");
    var i = t.lastIndexOf(".");
    return i >= 0 ? t.slice(i + 1) : t;
  }

  function fmtJobTime(iso) {
    if (!iso) return "—";
    try {
      var d = new Date(iso);
      if (isNaN(d.getTime())) return iso;
      return d.toLocaleTimeString();
    } catch (_e) {
      return iso;
    }
  }

  // Steady job queue dock — in-place updates, stable created_at order (no status re-sort).
  (function initJobsDock() {
    var dock = document.getElementById("jobs-dock");
    if (!dock || !window.CopperJobs || dock.dataset.copperInited) return;
    dock.dataset.copperInited = "1";
    var toggle = document.getElementById("jobs-dock-toggle");
    var panel = document.getElementById("jobs-dock-panel");
    var countEl = document.getElementById("jobs-dock-count");
    var statusEl = document.getElementById("jobs-dock-status");
    var listEl = document.getElementById("jobs-dock-list");
    var minimize = document.getElementById("jobs-dock-minimize");
    var emptyEl = null;
    var rowEls = {}; // id -> element

    function setOpen(on) {
      if (!panel) return;
      panel.hidden = !on;
      dock.classList.toggle("is-open", !!on);
    }

    function ensureEmpty() {
      if (emptyEl) return emptyEl;
      emptyEl = document.createElement("p");
      emptyEl.className = "muted";
      emptyEl.textContent = "No jobs yet. Batch / Deploy / Storage actions enqueue here.";
      return emptyEl;
    }

    function jobHref(id) {
      return "/jobs?id=" + encodeURIComponent(id);
    }

    function fillRow(el, j) {
      el.className = "jobs-dock-item st-" + (j.status || "");
      el.setAttribute("href", jobHref(j.id));
      el.setAttribute("data-copper-nav", "");
      el.setAttribute("data-job-id", j.id);
      var title = (j.server_name || "job") + " · " + shortType(j.type);
      var phase = j.phase || j.status || "";
      var titleEl = el.querySelector(".jobs-dock-item-title");
      var pillEl = el.querySelector(".pill");
      var phaseEl = el.querySelector(".jobs-dock-item-phase");
      if (!titleEl) {
        el.innerHTML =
          '<span class="jobs-dock-item-title"></span>' +
          '<span class="pill"></span>' +
          '<span class="jobs-dock-item-phase"></span>';
        titleEl = el.querySelector(".jobs-dock-item-title");
        pillEl = el.querySelector(".pill");
        phaseEl = el.querySelector(".jobs-dock-item-phase");
      }
      titleEl.textContent = title;
      pillEl.className = "pill st-" + (j.status || "");
      pillEl.textContent = j.status || "";
      phaseEl.textContent = phase;
    }

    function render() {
      var jobs = window.CopperJobs.listSorted({ limit: 40 });
      var active = jobs.filter(window.CopperJobs.isActive);
      if (countEl) {
        if (active.length) {
          countEl.hidden = false;
          countEl.textContent = String(active.length);
        } else {
          countEl.hidden = true;
        }
      }
      dock.classList.toggle("has-active", active.length > 0);
      if (statusEl) {
        statusEl.textContent = active.length
          ? active.length + " active · newest first"
          : jobs.length
            ? "queue idle · " + jobs.length + " recent"
            : "no jobs yet";
      }
      if (!listEl) return;

      // Show up to 16 newest; keep finished rows in place (no active-first reshuffle).
      var show = jobs.slice(0, 16);
      var keep = {};
      if (!show.length) {
        Object.keys(rowEls).forEach(function (id) {
          rowEls[id].remove();
          delete rowEls[id];
        });
        listEl.innerHTML = "";
        listEl.appendChild(ensureEmpty());
        return;
      }
      if (emptyEl && emptyEl.parentNode) emptyEl.remove();

      show.forEach(function (j, idx) {
        keep[j.id] = true;
        var el = rowEls[j.id];
        if (!el) {
          el = document.createElement("a");
          rowEls[j.id] = el;
        }
        fillRow(el, j);
        var current = listEl.children[idx];
        if (current !== el) {
          listEl.insertBefore(el, current || null);
        }
      });
      Object.keys(rowEls).forEach(function (id) {
        if (!keep[id]) {
          rowEls[id].remove();
          delete rowEls[id];
        }
      });
    }

    if (toggle) {
      toggle.addEventListener("click", function () {
        setOpen(panel.hidden);
      });
    }
    if (minimize) {
      minimize.addEventListener("click", function () {
        setOpen(false);
      });
    }
    window.CopperJobs.subscribe(function () {
      render();
    });
    window.addEventListener("copper-jobs-hydrated", function (ev) {
      render();
      var n = (ev.detail && ev.detail.activeCount) || 0;
      // After reconnect/login hydrate: surface the live queue without hunting for it.
      if (n > 0) setOpen(true);
    });
    window.CopperJobs.start();
  })();

  // Full Jobs page — stable newest-first order; patch rows in place.
  function initJobsPage() {
    var body = document.getElementById("jobs-body");
    if (!body || !window.CopperJobs) return;
    if (body.dataset.copperInited) return;
    body.dataset.copperInited = "1";
    var statusSel = document.getElementById("jobs-status");
    var typeSel = document.getElementById("jobs-type");
    var statusLine = document.getElementById("jobs-status-line");
    var detail = document.getElementById("jobs-detail");
    var detailTitle = document.getElementById("jobs-detail-title");
    var detailMeta = document.getElementById("jobs-detail-meta");
    var detailError = document.getElementById("jobs-detail-error");
    var detailLog = document.getElementById("jobs-detail-log");
    var selectedId = "";
    var params = new URLSearchParams(window.location.search);
    if (params.get("id")) selectedId = params.get("id");
    var rowEls = {};
    var unsub = null;

    function selectJob(id) {
      selectedId = id || "";
      Object.keys(rowEls).forEach(function (jid) {
        rowEls[jid].classList.toggle("is-selected", jid === selectedId);
      });
      if (!selectedId) {
        if (detail) detail.hidden = true;
        return;
      }
      var cached = window.CopperJobs.get(selectedId);
      if (cached) paintDetail(cached);
      fetch("/api/jobs/" + encodeURIComponent(selectedId), { credentials: "same-origin" })
        .then(function (r) {
          return r.json().then(function (j) {
            if (!r.ok) throw new Error((j && j.error) || "not found");
            return j.job;
          });
        })
        .then(function (job) {
          window.CopperJobs.remember(job);
          paintDetail(job);
        })
        .catch(function (err) {
          if (detailError) {
            detailError.hidden = false;
            detailError.textContent = String(err.message || err);
          }
        });
    }

    function paintDetail(job) {
      if (!job) return;
      if (detail) detail.hidden = false;
      if (detailTitle) detailTitle.textContent = shortType(job.type) + " · " + (job.server_name || job.id);
      if (detailMeta) {
        detailMeta.textContent =
          job.id +
          " · " +
          job.status +
          (job.phase ? " · " + job.phase : "") +
          (job.progress ? " · " + job.progress : "");
      }
      if (detailError) {
        if (job.error) {
          detailError.hidden = false;
          detailError.textContent = job.error;
        } else {
          detailError.hidden = true;
          detailError.textContent = "";
        }
      }
      if (detailLog) {
        detailLog.textContent = (job.log || []).join("\n") || "(no log yet)";
        detailLog.scrollTop = detailLog.scrollHeight;
      }
    }

    function fillRow(tr, j) {
      tr.className = "st-" + (j.status || "") + (j.id === selectedId ? " is-selected" : "");
      tr.setAttribute("data-id", j.id);
      var cells = tr.children;
      if (cells.length < 7) {
        tr.innerHTML =
          "<td><code></code></td><td></td><td></td>" +
          '<td><span class="pill"></span></td><td></td><td></td>' +
          '<td><button type="button" class="linkish jobs-open">Log</button></td>';
        cells = tr.children;
      }
      cells[0].querySelector("code").textContent = String(j.id).slice(0, 8);
      cells[1].textContent = j.type || "";
      cells[2].textContent = j.server_name || "—";
      var pill = cells[3].querySelector(".pill");
      pill.className = "pill st-" + (j.status || "");
      pill.textContent = j.status || "";
      cells[4].textContent = j.phase || "—";
      cells[5].textContent = fmtJobTime(j.started_at || j.created_at);
      cells[6].querySelector("button").setAttribute("data-id", j.id);
    }

    function render() {
      var filter = { limit: 100 };
      var st = statusSel && statusSel.value;
      var ty = typeSel && typeSel.value;
      if (st) filter.status = st;
      if (ty) filter.type = ty;
      var jobs = window.CopperJobs.listSorted(filter);
      var nActive = jobs.filter(window.CopperJobs.isActive).length;
      if (statusLine) {
        statusLine.textContent =
          jobs.length + " shown · " + nActive + " active · newest first";
      }
      var keep = {};
      jobs.forEach(function (j, idx) {
        keep[j.id] = true;
        var tr = rowEls[j.id];
        if (!tr) {
          tr = document.createElement("tr");
          rowEls[j.id] = tr;
        }
        fillRow(tr, j);
        var current = body.children[idx];
        if (current !== tr) body.insertBefore(tr, current || null);
      });
      Object.keys(rowEls).forEach(function (id) {
        if (!keep[id]) {
          rowEls[id].remove();
          delete rowEls[id];
        }
      });
      if (selectedId) {
        var job = window.CopperJobs.get(selectedId);
        if (job) paintDetail(job);
      }
    }

    body.addEventListener("click", function (ev) {
      var btn = ev.target.closest(".jobs-open, tr[data-id]");
      if (!btn) return;
      var id = btn.getAttribute("data-id");
      if (!id) return;
      selectJob(id);
    });
    if (statusSel) statusSel.addEventListener("change", render);
    if (typeSel) typeSel.addEventListener("change", render);
    var refreshBtn = document.getElementById("jobs-refresh");
    if (refreshBtn) {
      refreshBtn.addEventListener("click", function () {
        window.CopperJobs.list({ limit: 100 }).then(render).catch(function () {});
      });
    }
    unsub = window.CopperJobs.subscribe(function () {
      render();
    });
    window.CopperJobs.start();
    window.CopperJobs.list({ limit: 100 }).catch(function () {});
    if (selectedId) selectJob(selectedId);
    body._copperJobsCleanup = function () {
      if (unsub) unsub();
      unsub = null;
    };
  }
  window.CopperPages = window.CopperPages || {};
  window.CopperPages.initJobsPage = initJobsPage;
  initJobsPage();

  // DHCP pages: "Select all" for inventory MAC allow checkboxes.
  (function initMacSelectAll() {
    function syncToggle(master, boxes) {
      if (!boxes.length) {
        master.checked = false;
        master.indeterminate = false;
        return;
      }
      var checked = 0;
      boxes.forEach(function (b) {
        if (b.checked) checked++;
      });
      master.checked = checked === boxes.length;
      master.indeterminate = checked > 0 && checked < boxes.length;
    }

    document.querySelectorAll(".mac-select-all-toggle").forEach(function (master) {
      var group = master.getAttribute("data-mac-group") || "allow_mac_known";
      var fieldset = master.closest(".mac-pick");
      if (!fieldset) return;

      function items() {
        return fieldset.querySelectorAll('input[type="checkbox"][name="' + group + '"]');
      }

      master.addEventListener("change", function () {
        var on = master.checked;
        items().forEach(function (b) {
          b.checked = on;
        });
        master.indeterminate = false;
        fieldset.querySelectorAll(".mac-select-server-toggle").forEach(function (t) {
          t.checked = on;
          t.indeterminate = false;
        });
      });
      items().forEach(function (b) {
        b.addEventListener("change", function () {
          syncToggle(master, Array.prototype.slice.call(items()));
          var details = b.closest("details.host-server");
          if (details) {
            var local = details.querySelector(".mac-select-server-toggle");
            if (local) {
              syncToggle(
                local,
                Array.prototype.slice.call(
                  details.querySelectorAll('input[type="checkbox"][name="' + group + '"]')
                )
              );
            }
          }
        });
      });
      syncToggle(master, Array.prototype.slice.call(items()));
    });

    document.querySelectorAll(".mac-select-server-toggle").forEach(function (master) {
      var group = master.getAttribute("data-mac-group") || "allow_mac_known";
      var details = master.closest("details.host-server");
      if (!details) return;

      function items() {
        return details.querySelectorAll('input[type="checkbox"][name="' + group + '"]');
      }

      master.addEventListener("change", function () {
        var on = master.checked;
        items().forEach(function (b) {
          b.checked = on;
        });
        master.indeterminate = false;
        var global = document.querySelector(".mac-select-all-toggle");
        var fieldset = master.closest(".mac-pick");
        if (global && fieldset) {
          syncToggle(
            global,
            Array.prototype.slice.call(
              fieldset.querySelectorAll('input[type="checkbox"][name="' + group + '"]')
            )
          );
        }
      });
      syncToggle(master, Array.prototype.slice.call(items()));
    });
  })();

  // Data DHCP: filter accordion groups by name / MAC / IP; Bound mode.
  // Re-run on SPA navigations (copper:navigated / activatePage).
  function initHostNICFilter() {
    var list = document.getElementById("host-server-list");
    var input = document.getElementById("host-nic-filter");
    var empty = document.getElementById("host-nic-empty");
    if (!list || list.dataset.copperInited === "1") return;
    list.dataset.copperInited = "1";

    var mode = "all";
    var modeBtns = document.querySelectorAll(".host-filter-mode");

    function apply() {
      var q = input ? String(input.value || "").trim().toLowerCase() : "";
      var groups = list.querySelectorAll("details.host-server");
      var visible = 0;
      groups.forEach(function (el) {
        var hay = (el.getAttribute("data-search") || "").toLowerCase();
        var hasLease = el.getAttribute("data-has-lease") === "1";
        var ok = (!q || hay.indexOf(q) !== -1) && (mode !== "bound" || hasLease);
        el.hidden = !ok;
        if (ok) visible++;
      });
      if (empty) empty.hidden = visible > 0;
    }

    if (input) input.addEventListener("input", apply);
    modeBtns.forEach(function (btn) {
      btn.addEventListener("click", function () {
        mode = btn.getAttribute("data-mode") || "all";
        modeBtns.forEach(function (b) {
          b.classList.toggle("is-active", b === btn);
        });
        apply();
      });
    });
    apply();
  }
  window.CopperPages = window.CopperPages || {};
  window.CopperPages.initHostNICFilter = initHostNICFilter;
  initHostNICFilter();

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

  // Small popup: Server / Storage / Deploy when clicking a named server (Data DHCP).
  (function initServerJumpMenu() {
    var menu = null;
    var openBtn = null;

    function ensureMenu() {
      if (menu) return menu;
      menu = document.createElement("div");
      menu.id = "server-jump-menu";
      menu.className = "server-jump-menu";
      menu.hidden = true;
      menu.setAttribute("role", "menu");
      document.body.appendChild(menu);
      return menu;
    }

    function closeMenu() {
      if (!menu || menu.hidden) return;
      menu.hidden = true;
      menu.innerHTML = "";
      if (openBtn) {
        openBtn.setAttribute("aria-expanded", "false");
        openBtn = null;
      }
    }

    function openMenu(btn) {
      var id = btn.getAttribute("data-server-id") || "";
      var name = btn.getAttribute("data-server-name") || "server";
      if (!id) return;
      var m = ensureMenu();
      if (openBtn === btn && !m.hidden) {
        closeMenu();
        return;
      }
      closeMenu();
      openBtn = btn;
      btn.setAttribute("aria-expanded", "true");
      m.innerHTML =
        '<span class="server-jump-title">' +
        String(name)
          .replace(/&/g, "&amp;")
          .replace(/</g, "&lt;")
          .replace(/>/g, "&gt;") +
        "</span>" +
        '<a role="menuitem" href="/servers/' +
        encodeURIComponent(id) +
        '">Server</a>' +
        '<a role="menuitem" href="/servers/' +
        encodeURIComponent(id) +
        '/storage">Storage</a>' +
        '<a role="menuitem" href="/servers/' +
        encodeURIComponent(id) +
        '/deploy">Deploy</a>';
      m.hidden = false;
      var rect = btn.getBoundingClientRect();
      var pad = 8;
      var top = rect.bottom + 6;
      var left = rect.left;
      m.style.top = Math.max(pad, top) + "px";
      m.style.left = Math.max(pad, left) + "px";
      // Keep inside viewport after paint.
      requestAnimationFrame(function () {
        var box = m.getBoundingClientRect();
        var maxLeft = window.innerWidth - box.width - pad;
        var maxTop = window.innerHeight - box.height - pad;
        if (box.right > window.innerWidth - pad) {
          m.style.left = Math.max(pad, maxLeft) + "px";
        }
        if (box.bottom > window.innerHeight - pad) {
          m.style.top = Math.max(pad, rect.top - box.height - 6, maxTop) + "px";
        }
      });
    }

    document.addEventListener("click", function (ev) {
      var btn = ev.target && ev.target.closest && ev.target.closest(".server-jump-btn");
      if (btn) {
        ev.preventDefault();
        ev.stopPropagation();
        openMenu(btn);
        return;
      }
      if (menu && !menu.hidden && ev.target && menu.contains(ev.target)) {
        return; // let link navigate
      }
      closeMenu();
    }, true);

    document.addEventListener("keydown", function (ev) {
      if (ev.key === "Escape") closeMenu();
    });

    window.addEventListener("scroll", closeMenu, true);
    window.addEventListener("resize", closeMenu);
  })();

  // Data DHCP: live lease table (independent of Probe/Inventory).
  // Must re-init after SPA navigations — otherwise Resolve never binds until full refresh.
  function initDataDHCPLeases() {
    var wrap = document.getElementById("leases-table-wrap");
    if (!wrap || wrap.dataset.copperInited === "1") return;
    wrap.dataset.copperInited = "1";
    var url = wrap.getAttribute("data-leases-url") || "/api/local/dhcp/leases";
    var pollMs = parseInt(wrap.getAttribute("data-poll-ms") || "60000", 10);
    if (!pollMs || pollMs < 5000) pollMs = 60000; // floor 5s; default 60s (Settings agent watch)
    var statusEl = document.getElementById("leases-status");
    var table = document.getElementById("leases-table");
    var tbody = document.getElementById("leases-tbody");
    var emptyEl = document.getElementById("leases-empty");
    var filterEl = document.getElementById("leases-filter");
    var filterMeta = document.getElementById("leases-filter-meta");
    var timer = null;
    var filterTimer = null;
    var latestLeases = [];
    var latestServers = []; // [{id,name}] for manual Assign
    var filterQ = "";
    var dead = false;

    function setStatus(text, isError) {
      if (!statusEl) return;
      statusEl.textContent = text;
      statusEl.classList.toggle("is-error", !!isError);
    }

    function esc(s) {
      return String(s == null ? "" : s)
        .replace(/&/g, "&amp;")
        .replace(/</g, "&lt;")
        .replace(/>/g, "&gt;")
        .replace(/"/g, "&quot;");
    }

    function sortLeases(leases) {
      return (leases || []).slice().sort(function (a, b) {
        var ma = String(a.mac || "").toLowerCase();
        var mb = String(b.mac || "").toLowerCase();
        if (ma !== mb) return ma < mb ? -1 : 1;
        var ia = String(a.ip || "");
        var ib = String(b.ip || "");
        return ia < ib ? -1 : ia > ib ? 1 : 0;
      });
    }

    function leaseSearchBlob(l) {
      return [
        l.server_name,
        l.hardware,
        l.nic,
        l.mac,
        l.ip,
        l.expires_at,
        l.reserved ? "reserved" : "",
      ]
        .join(" ")
        .toLowerCase();
    }

    function applyFilter(leases) {
      var q = (filterQ || "").trim().toLowerCase();
      if (!q) return leases;
      return leases.filter(function (l) {
        return leaseSearchBlob(l).indexOf(q) >= 0;
      });
    }

    function updateFilterMeta(shown, total) {
      if (!filterMeta) return;
      if (!total) {
        filterMeta.textContent = "";
        return;
      }
      if ((filterQ || "").trim()) {
        filterMeta.textContent = "showing " + shown + " of " + total;
      } else {
        filterMeta.textContent = total + " lease" + (total === 1 ? "" : "s");
      }
    }

    function render() {
      if (!tbody || !table) return;
      var sorted = sortLeases(latestLeases);
      var leases = applyFilter(sorted);
      updateFilterMeta(leases.length, sorted.length);
      if (!sorted.length) {
        tbody.innerHTML = "";
        table.hidden = true;
        if (emptyEl) {
          emptyEl.hidden = false;
          emptyEl.textContent = "No active leases yet.";
        } else {
          emptyEl = document.createElement("p");
          emptyEl.id = "leases-empty";
          emptyEl.className = "muted";
          emptyEl.textContent = "No active leases yet.";
          wrap.appendChild(emptyEl);
        }
        return;
      }
      if (emptyEl) emptyEl.hidden = true;
      table.hidden = false;
      if (!leases.length) {
        tbody.innerHTML =
          '<tr><td colspan="7" class="muted">No leases match this filter.</td></tr>';
        return;
      }
      var html = "";
      leases.forEach(function (l) {
        var server;
        if (l.server_name && l.server_id) {
          server =
            '<button type="button" class="server-jump-btn" data-server-id="' +
            esc(String(l.server_id)) +
            '" data-server-name="' +
            esc(l.server_name) +
            '" title="Open server pages" aria-haspopup="menu" aria-expanded="false">' +
            esc(l.server_name) +
            "</button>";
        } else if (l.server_name) {
          server = esc(l.server_name);
        } else {
          server = '<span class="muted">unknown</span>';
        }
        var hw = l.hardware ? esc(l.hardware) : "—";
        var nic = l.nic ? esc(l.nic) : "—";
        var reserved = l.reserved
          ? '<span class="badge badge-bound">reserved</span> '
          : "";
        var actions = reserved;
        if (!l.server_name) {
          var opts =
            '<option value="">Assign to…</option>' +
            latestServers
              .map(function (sv) {
                return (
                  '<option value="' +
                  esc(String(sv.id)) +
                  '">' +
                  esc(sv.name || "#" + sv.id) +
                  "</option>"
                );
              })
              .join("");
          actions +=
            '<span class="row-status lease-claim-spin" hidden aria-hidden="true">' +
            '<svg class="row-spinner" viewBox="0 0 24 24" width="16" height="16" aria-hidden="true">' +
            '<circle class="busy-track" cx="12" cy="12" r="9" fill="none" stroke-width="2.5"/>' +
            '<circle class="busy-arc row-arc" cx="12" cy="12" r="9" fill="none" stroke-width="2.5" stroke-linecap="round" stroke-dasharray="40 56"/>' +
            "</svg></span> " +
            '<button type="button" class="btn ghost btn-sm btn-claim-nics" data-ip="' +
            esc(l.ip) +
            '" data-mac="' +
            esc(l.mac) +
            '" title="SSH this lease and attach NICs to the matching inventory server">Resolve</button> ' +
            '<span class="lease-assign-wrap">' +
            '<select class="lease-assign-select" aria-label="Assign lease to server">' +
            opts +
            "</select> " +
            '<button type="button" class="btn ghost btn-sm btn-assign-lease" data-ip="' +
            esc(l.ip) +
            '" data-mac="' +
            esc(l.mac) +
            '" title="Manually assign this lease MAC to a baremetal">Assign</button>' +
            "</span> " +
            '<span class="row-msg muted lease-claim-msg" hidden></span>';
        } else if (l.server_id) {
          actions +=
            '<button type="button" class="btn ghost btn-sm btn-unassign-lease" data-ip="' +
            esc(l.ip) +
            '" data-mac="' +
            esc(l.mac) +
            '" data-server-id="' +
            esc(String(l.server_id)) +
            '" title="Remove this lease MAC from the baremetal (wrong match)">Unassign</button> ' +
            '<span class="row-msg muted lease-claim-msg" hidden></span>';
        }
        var search = esc(leaseSearchBlob(l));
        html +=
          '<tr data-search="' +
          search +
          '" data-mac="' +
          esc(l.mac) +
          '" data-ip="' +
          esc(l.ip) +
          '"' +
          (l.server_id ? ' data-server-id="' + esc(String(l.server_id)) + '"' : "") +
          ">" +
          "<td>" +
          server +
          "</td>" +
          "<td>" +
          hw +
          "</td>" +
          "<td>" +
          nic +
          "</td>" +
          "<td><code>" +
          esc(l.mac) +
          "</code></td>" +
          "<td><code>" +
          esc(l.ip) +
          "</code></td>" +
          "<td>" +
          (l.expires_at ? esc(l.expires_at) : "—") +
          "</td>" +
          '<td class="lease-actions">' +
          actions +
          "</td>" +
          "</tr>";
      });
      tbody.innerHTML = html;
    }

    function setClaimUI(tr, state, msg) {
      if (!tr) return;
      var spin = tr.querySelector(".lease-claim-spin");
      var btn = tr.querySelector(".btn-claim-nics");
      var assignBtn = tr.querySelector(".btn-assign-lease");
      var unBtn = tr.querySelector(".btn-unassign-lease");
      var sel = tr.querySelector(".lease-assign-select");
      var msgEl = tr.querySelector(".lease-claim-msg");
      if (spin) {
        spin.hidden = state !== "running";
        spin.setAttribute("aria-hidden", state !== "running" ? "true" : "false");
      }
      var busy = state === "running";
      if (btn) btn.disabled = busy;
      if (assignBtn) assignBtn.disabled = busy;
      if (unBtn) unBtn.disabled = busy;
      if (sel) sel.disabled = busy;
      if (msgEl) {
        if (msg) {
          msgEl.hidden = false;
          msgEl.textContent = msg;
          msgEl.classList.toggle("is-error", state === "error");
          msgEl.classList.toggle("is-ok", state === "ok");
        } else {
          msgEl.hidden = true;
          msgEl.textContent = "";
        }
      }
    }

    function onLeaseActionClick(ev) {
      if (dead) return;
      var assignBtn = ev.target && ev.target.closest && ev.target.closest(".btn-assign-lease");
      if (assignBtn && !assignBtn.disabled) {
        var trA = assignBtn.closest("tr");
        var sel = trA && trA.querySelector(".lease-assign-select");
        var sid = sel && sel.value ? parseInt(sel.value, 10) : 0;
        var ipA = assignBtn.getAttribute("data-ip") || "";
        var macA = assignBtn.getAttribute("data-mac") || "";
        if (!sid) {
          setClaimUI(trA, "error", "pick a server");
          return;
        }
        setClaimUI(trA, "running", "assigning…");
        assignBtn.disabled = true;
        fetch("/api/dhcp/data/assign-lease", {
          method: "POST",
          credentials: "same-origin",
          headers: { "Content-Type": "application/json", Accept: "application/json" },
          body: JSON.stringify({ ip: ipA, mac: macA, server_id: sid }),
        })
          .then(function (resp) {
            return resp.json().then(function (body) {
              if (!resp.ok) throw new Error((body && body.error) || resp.statusText || "failed");
              return body;
            });
          })
          .then(function (body) {
            setClaimUI(trA, "ok", "→ " + ((body && body.server_name) || "server"));
            poll();
          })
          .catch(function (err) {
            assignBtn.disabled = false;
            setClaimUI(trA, "error", (err && err.message) || String(err));
          });
        return;
      }

      var unBtn = ev.target && ev.target.closest && ev.target.closest(".btn-unassign-lease");
      if (unBtn && !unBtn.disabled) {
        var trU = unBtn.closest("tr");
        var ipU = unBtn.getAttribute("data-ip") || "";
        var macU = unBtn.getAttribute("data-mac") || "";
        var sidU = parseInt(unBtn.getAttribute("data-server-id") || "0", 10) || 0;
        if (!window.confirm("Unassign this lease MAC from the baremetal? It will show as unknown until Resolve/Assign again.")) {
          return;
        }
        setClaimUI(trU, "running", "unassigning…");
        unBtn.disabled = true;
        fetch("/api/dhcp/data/unassign-lease", {
          method: "POST",
          credentials: "same-origin",
          headers: { "Content-Type": "application/json", Accept: "application/json" },
          body: JSON.stringify({ ip: ipU, mac: macU, server_id: sidU }),
        })
          .then(function (resp) {
            return resp.json().then(function (body) {
              if (!resp.ok) throw new Error((body && body.error) || resp.statusText || "failed");
              return body;
            });
          })
          .then(function () {
            setClaimUI(trU, "ok", "unassigned");
            poll();
          })
          .catch(function (err) {
            unBtn.disabled = false;
            setClaimUI(trU, "error", (err && err.message) || String(err));
          });
        return;
      }

      var btn = ev.target && ev.target.closest && ev.target.closest(".btn-claim-nics");
      if (!btn || btn.disabled) return;
      var tr = btn.closest("tr");
      var ip = btn.getAttribute("data-ip") || "";
      var mac = btn.getAttribute("data-mac") || "";
      if (!ip) return;
      setClaimUI(tr, "running", "resolving…");
      fetch("/api/dhcp/data/claim-nics", {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json", Accept: "application/json" },
        body: JSON.stringify({ ip: ip, mac: mac }),
      })
        .then(function (resp) {
          return resp.json().then(function (body) {
            if (!resp.ok) throw new Error((body && body.error) || resp.statusText || "failed");
            return body;
          });
        })
        .then(function (body) {
          var jobId = body && body.job_id;
          if (!jobId || !window.CopperJobs) {
            setClaimUI(tr, "ok", "queued");
            return;
          }
          window.CopperJobs.track(jobId);
          return window.CopperJobs.pollUntilDone(jobId, 120000, function (job) {
            var line = (job && (job.progress || job.phase)) || "running";
            setClaimUI(tr, "running", line);
          }).then(function (job) {
            var result = window.CopperJobs.parseResult
              ? window.CopperJobs.parseResult(job)
              : {};
            if (job.status === "ok") {
              var name = (result && result.server_name) || "server";
              var added = result && typeof result.added === "number" ? result.added : "?";
              setClaimUI(tr, "ok", "→ " + name + " (+" + added + ")");
              poll();
            } else {
              setClaimUI(tr, "error", (job && job.error) || "failed");
            }
          });
        })
        .catch(function (err) {
          setClaimUI(tr, "error", (err && err.message) || String(err));
        });
    }
    if (tbody) tbody.addEventListener("click", onLeaseActionClick);

    function schedulePoll(ms) {
      if (timer) clearInterval(timer);
      if (dead) return;
      pollMs = ms;
      timer = setInterval(poll, pollMs);
    }

    function poll() {
      if (dead) return;
      fetch(url, { credentials: "same-origin" })
        .then(function (resp) {
          return resp.json().then(function (body) {
            if (!resp.ok) {
              throw new Error((body && body.error) || resp.statusText || "fetch failed");
            }
            return body;
          });
        })
        .then(function (body) {
          if (dead) return;
          latestLeases = body.leases || [];
          latestServers = body.servers || [];
          render();
          var n = latestLeases.length;
          var run = body.running ? "running" : "stopped";
          var watchSec = parseInt(body.agent_watch_seconds, 10);
          var nextMs = pollMs;
          if (watchSec && watchSec >= 5) {
            nextMs = watchSec * 1000;
          }
          var intervalLabel = Math.round(nextMs / 1000) + "s";
          setStatus(
            "live · " +
              n +
              " lease" +
              (n === 1 ? "" : "s") +
              " · dhcp " +
              run +
              " · refresh " +
              intervalLabel,
            false
          );
          if (nextMs !== pollMs) {
            schedulePoll(nextMs);
          }
        })
        .catch(function (err) {
          if (dead) return;
          setStatus(err && err.message ? err.message : String(err), true);
        });
    }

    function onFilterInput() {
      filterQ = filterEl.value || "";
      if (filterTimer) clearTimeout(filterTimer);
      filterTimer = setTimeout(function () {
        render();
      }, 80);
    }
    if (filterEl) filterEl.addEventListener("input", onFilterInput);

    function cleanup() {
      dead = true;
      if (timer) clearInterval(timer);
      timer = null;
      if (filterTimer) clearTimeout(filterTimer);
      filterTimer = null;
          if (tbody) tbody.removeEventListener("click", onLeaseActionClick);
      if (filterEl) filterEl.removeEventListener("input", onFilterInput);
      try {
        delete wrap.dataset.copperInited;
      } catch (_e) {
        wrap.dataset.copperInited = "";
      }
    }
    wrap._copperCleanup = cleanup;

    poll();
    schedulePoll(pollMs);
  }
  window.CopperPages = window.CopperPages || {};
  window.CopperPages.initDataDHCPLeases = initDataDHCPLeases;
  initDataDHCPLeases();

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
  function initBatchServers() {
    var page = document.getElementById("servers-page");
    var table = document.getElementById("servers-table");
    if (!page || !table) return;
    if (page.dataset.copperInited) return;
    page.dataset.copperInited = "1";

    var checks = Array.prototype.slice.call(table.querySelectorAll(".batch-check"));
    var selectAll = document.getElementById("batch-select-all");
    var selectNone = document.getElementById("batch-select-none");
    var selectBound = document.getElementById("batch-select-bound");
    var selectDeclared = document.getElementById("batch-select-declared");
    var countEl = document.getElementById("batch-count");
    var btnProbe = document.getElementById("batch-probe");
    var btnInv = document.getElementById("batch-inventory");
    var btnPowerOn = document.getElementById("batch-power-on");
    var btnPowerOff = document.getElementById("batch-power-off");
    var btnReboot = document.getElementById("batch-reboot");
    var btnConvertUEFI = document.getElementById("batch-convert-uefi");
    var btnMount = document.getElementById("batch-mount");
    var btnDetectRaid = document.getElementById("batch-detect-raid");
    var btnStorage = document.getElementById("batch-storage");
    var btnSoftRaid = document.getElementById("batch-softraid");
    var btnDeploy = document.getElementById("batch-deploy");
    var btnDelete = document.getElementById("batch-delete");
    var btnLabels = document.getElementById("batch-labels");
    var zipLink = document.getElementById("batch-zip");
    var mountPanel = document.getElementById("batch-mount-panel");
    var labelsPanel = document.getElementById("batch-labels-panel");
    var labelsInput = document.getElementById("batch-labels-input");
    var labelsCancel = document.getElementById("batch-labels-cancel");
    var labelsRun = document.getElementById("batch-labels-run");
    var floatLog = document.getElementById("float-log");
    var floatBody = document.getElementById("float-log-body");
    var floatStatus = document.getElementById("float-log-status");
    var floatClear = document.getElementById("float-log-clear");
    var floatToggle = document.getElementById("float-log-toggle");
    var running = false;
    var activitySince = 0;
    var activityTimer = null;
    var lastZipIds = [];

    function refreshChecks() {
      checks = Array.prototype.slice.call(table.querySelectorAll(".batch-check"));
    }

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

    function selectionCapabilities(selected) {
      // Actions available for the whole selection (intersection of what each row allows).
      // Delete: any status. Probe: needs endpoint. Bound-only Redfish: Inventory/Reboot/Mount.
      // Detect RAID / Storage / Deploy: any selection (live OS reachability checked server-side).
      var n = selected.length;
      var caps = {
        delete: n > 0,
        labels: n > 0,
        probe: n > 0,
        boundOps: n > 0,
        detectRaid: n > 0,
        storage: n > 0,
        softRaid: n > 0,
        deploy: n > 0,
      };
      selected.forEach(function (c) {
        var status = c.getAttribute("data-status") || "";
        var endpoint = (c.getAttribute("data-endpoint") || "").trim();
        if (!endpoint) caps.probe = false;
        if (status !== "bound" || !endpoint) caps.boundOps = false;
      });
      return caps;
    }

    function setActionVisible(btn, visible) {
      if (!btn) return;
      btn.hidden = !visible;
      btn.disabled = running || !visible;
    }

    function updateCount() {
      var selected = selectedChecks();
      var n = selected.length;
      if (countEl) countEl.textContent = n + " selected";
      var caps = selectionCapabilities(selected);
      setActionVisible(btnDelete, caps.delete);
      setActionVisible(btnLabels, caps.labels);
      setActionVisible(btnProbe, caps.probe);
      setActionVisible(btnInv, caps.boundOps);
      setActionVisible(btnPowerOn, caps.boundOps);
      setActionVisible(btnPowerOff, caps.boundOps);
      setActionVisible(btnReboot, caps.boundOps);
      setActionVisible(btnConvertUEFI, caps.boundOps);
      setActionVisible(btnMount, caps.boundOps);
      setActionVisible(btnDetectRaid, caps.detectRaid);
      setActionVisible(btnStorage, caps.storage);
      setActionVisible(btnSoftRaid, caps.softRaid);
      setActionVisible(btnDeploy, caps.deploy);
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

    function markRowBound(id) {
      var row = rowFor(id);
      if (!row) return;
      row.setAttribute("data-status", "bound");
      var check = row.querySelector(".batch-check");
      if (check) check.setAttribute("data-status", "bound");
      var badgeCell = row.querySelector(".badge-declared, .badge-bound");
      if (badgeCell && badgeCell.classList.contains("badge-declared")) {
        badgeCell.className = "badge badge-bound";
        badgeCell.textContent = "bound";
      }
    }

    function parseLabelInput(raw) {
      return String(raw || "")
        .split(/[,;\n\r]+/)
        .map(function (s) {
          return s.replace(/\s+/g, " ").trim();
        })
        .filter(Boolean);
    }

    function renderRowLabels(row, labels) {
      if (!row) return;
      var joined = (labels || []).join(",");
      row.setAttribute("data-labels", joined);
      var check = row.querySelector(".batch-check");
      if (check) check.setAttribute("data-labels", joined);
      var cell = row.querySelector(".labels-cell");
      if (!cell) return;
      if (!labels || !labels.length) {
        cell.innerHTML = '<span class="muted">—</span>';
        return;
      }
      var wrap = document.createElement("span");
      wrap.className = "label-chip-row";
      labels.forEach(function (name) {
        var b = document.createElement("span");
        b.className = "badge badge-label";
        b.textContent = name;
        wrap.appendChild(b);
      });
      cell.innerHTML = "";
      cell.appendChild(wrap);
    }

    function mergeLabels(existing, add, mode) {
      var map = {};
      (existing || []).forEach(function (l) {
        map[String(l).toLowerCase()] = l;
      });
      (add || []).forEach(function (l) {
        var key = String(l).toLowerCase();
        if (mode === "remove") delete map[key];
        else map[key] = l;
      });
      return Object.keys(map)
        .sort()
        .map(function (k) {
          return map[k];
        });
    }

    function removeRow(id) {
      var row = rowFor(id);
      if (row) row.remove();
      refreshChecks();
      updateCount();
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
        timeouts: { inventory: 180, reboot: 60, mount: 180, probe: 60, delete: 30, detect: 90 },
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
          " · timeouts inv/reboot/mount/probe " +
          s.timeouts.inventory +
          "/" +
          s.timeouts.reboot +
          "/" +
          s.timeouts.mount +
          "/" +
          (s.timeouts.probe || 60) +
          "s";
      });
    }

    function timeoutForAction(settings, action) {
      var t = settings && settings.timeouts ? settings.timeouts : {};
      if (action === "inventory") return (t.inventory || 180) * 1000;
      if (action === "reboot" || action === "power-on" || action === "power-off")
        return (t.reboot || 60) * 1000;
      if (action === "mount") return (t.mount || 180) * 1000;
      if (action === "probe") return (t.probe || 60) * 1000;
      if (action === "delete") return (t.delete || 30) * 1000;
      if (action === "detect") return (t.detect || 90) * 1000;
      return 180000;
    }

    function setRowRaid(id, family) {
      var row = rowFor(id);
      if (!row) return;
      row.setAttribute("data-raid", family || "");
      var check = row.querySelector(".batch-check");
      if (check) check.setAttribute("data-raid", family || "");
      var cell = row.querySelector(".raid-cell");
      if (!cell) return;
      if (family) {
        cell.innerHTML = '<span class="badge badge-raid"></span>';
        cell.querySelector(".badge-raid").textContent = family;
      } else {
        cell.innerHTML = '<span class="muted">—</span>';
      }
    }

    function postJSON(url, body, timeoutMs) {
      var ctrl = typeof AbortController !== "undefined" ? new AbortController() : null;
      var timer = null;
      // Enqueue is fast; job work is polled separately. Cap the POST wait.
      var postTimeout = timeoutMs > 0 ? Math.min(timeoutMs, 60000) : 60000;
      if (ctrl && postTimeout > 0) {
        timer = setTimeout(function () {
          ctrl.abort();
        }, postTimeout);
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
            throw new Error("timed out after " + Math.round(postTimeout / 1000) + "s");
          }
          throw err;
        })
        .finally(function () {
          if (timer) clearTimeout(timer);
        });
    }

    // Enqueue a background job and return immediately (tracked in Jobs dock /api/jobs).
    function postJobTrack(url, body) {
      return postJSON(url, body, 60000).then(function (data) {
        if (data && data.job_id && window.CopperJobs) {
          window.CopperJobs.track(data.job_id);
        }
        if (data && data.jobs && window.CopperJobs) {
          window.CopperJobs.trackMany(data.jobs);
        }
        return data;
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

    // Queue I/O work in the background — do not hold the page until jobs finish.
    async function runBatch(label, action, actionFn) {
      if (running) return;
      var selected = selectedChecks();
      if (!selected.length) return;
      running = true;
      updateCount();
      startActivityFeed();
      var settings = await loadBatchSettings();
      var executors = Math.max(1, Math.min(settings.executors || 3, 16));
      appendLocal(
        "queueing " +
          label +
          " on " +
          selected.length +
          " server(s) · submit pool ×" +
          executors +
          " (jobs continue in background)",
        "local"
      );
      setFloatStatus("queueing " + label + "…", false);
      var queuedIds = [];
      var fail = 0;
      var done = 0;
      await runPool(selected, executors, async function (c) {
        var id = c.value;
        var name = c.getAttribute("data-name") || id;
        setRowBusy(id, true, "queueing…");
        try {
          var data = await actionFn(id, name);
          var jobId = data && data.job_id;
          // Keep row busy; dispatcher subscribe owns the steady busy badge.
          setRowBusy(id, true, jobId ? "queued · " + String(jobId).slice(0, 8) : "queued");
          rowFor(id) && rowFor(id).classList.add("is-server-busy");
          queuedIds.push(id);
          appendLocal(
            name + " — queued" + (jobId ? " job " + jobId : ""),
            "local"
          );
        } catch (err) {
          fail++;
          var msg = err && err.message ? err.message : String(err);
          setRowBusy(id, false, "failed", false);
          appendLocal(name + " — queue failed: " + msg, "error");
        } finally {
          done++;
          setFloatStatus(
            "queued " + label + " · " + done + "/" + selected.length,
            false
          );
        }
      });
      running = false;
      updateCount();
      appendLocal(
        label +
          " submitted: " +
          queuedIds.length +
          " queued, " +
          fail +
          " failed — follow progress under Jobs",
        fail ? "warn" : "local"
      );
      setFloatStatus(
        "queued · " + queuedIds.length + " · open Jobs dock",
        fail > 0
      );
      return queuedIds;
    }

    function paintBusyFromDispatcher() {
      if (!window.CopperJobs || !window.CopperJobs.busyMap) return;
      var busy = window.CopperJobs.busyMap();
      table.querySelectorAll("tr[data-server-id]").forEach(function (row) {
        var id = row.getAttribute("data-server-id");
        var info = busy[id];
        if (info) {
          row.classList.add("is-server-busy");
          setRowBusy(
            id,
            true,
            shortType(info.type) + " · " + (info.phase || info.status || "busy")
          );
        } else if (row.classList.contains("is-server-busy")) {
          row.classList.remove("is-server-busy");
          var note = row.querySelector(".row-msg");
          var keepMsg = note && !note.hidden && (note.classList.contains("is-ok") || note.classList.contains("is-error"));
          if (!keepMsg) setRowBusy(id, false);
          else {
            var spin = row.querySelector(".row-status");
            if (spin) {
              spin.hidden = true;
              spin.setAttribute("aria-hidden", "true");
            }
          }
        }
      });
    }

    if (window.CopperJobs && window.CopperJobs.subscribe) {
      window.CopperJobs.subscribe(function () {
        paintBusyFromDispatcher();
      });
    }

    // One global handler (SPA may remount this page); resolve table at event time.
    if (!window.__copperBatchJobDone) {
      window.__copperBatchJobDone = true;
      window.addEventListener("copper-job-done", function (ev) {
        var tbl = document.getElementById("servers-table");
        if (!tbl) return;
        var detail = ev.detail || {};
        var job = detail.job || {};
        var result = detail.result || {};
        var sid = job.server_id;
        if (!sid) return;
        var row = tbl.querySelector('tr[data-server-id="' + sid + '"]');
        if (!row) return;
        var spin = row.querySelector(".row-status");
        var note = row.querySelector(".row-msg");
        function setMsg(busy, msg, ok) {
          if (spin) {
            spin.hidden = !busy;
            spin.setAttribute("aria-hidden", busy ? "false" : "true");
          }
          if (note && msg) {
            note.hidden = false;
            note.textContent = msg;
            note.classList.toggle("is-error", ok === false);
            note.classList.toggle("is-ok", ok === true);
          }
        }
        if (job.status === "ok") {
          if (job.type === "redfish.probe") {
            row.setAttribute("data-status", "bound");
            var check = row.querySelector(".batch-check");
            if (check) check.setAttribute("data-status", "bound");
            var badge = row.querySelector(".badge-declared, .badge-bound");
            if (badge) {
              badge.className = "badge badge-bound";
              badge.textContent = "bound";
            }
          }
          if (job.type === "storage.detect" && result.family) {
            // Keep row + checkbox in sync — batch Storage reads data-raid on the checkbox.
            var fam = String(result.family || "").trim();
            row.setAttribute("data-raid", fam);
            var raidCheck = row.querySelector(".batch-check");
            if (raidCheck) raidCheck.setAttribute("data-raid", fam);
            var cell = row.querySelector(".raid-cell");
            if (cell) {
              if (fam && fam !== "unknown" && fam !== "none") {
                cell.innerHTML = '<span class="badge badge-raid"></span>';
                cell.querySelector(".badge-raid").textContent = fam;
              } else {
                cell.innerHTML = '<span class="muted">—</span>';
              }
            }
          }
          if (job.type === "redfish.inventory") {
            var zip = document.getElementById("batch-zip");
            if (zip) {
              var ids = [];
              try {
                var u = new URL(zip.href || window.location.href, window.location.origin);
                ids = (u.searchParams.get("ids") || "")
                  .split(",")
                  .map(function (x) {
                    return x.trim();
                  })
                  .filter(Boolean);
              } catch (_e) {}
              if (ids.indexOf(String(sid)) < 0) ids.push(String(sid));
              zip.hidden = false;
              zip.href = "/api/servers/inventory.zip?ids=" + ids.join(",");
            }
          }
          setMsg(false, shortType(job.type) + " ok", true);
        } else if (job.status === "error" || job.status === "cancelled") {
          setMsg(false, (job.error || job.status).split("\n")[0], false);
        }
      });
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
    if (selectDeclared) {
      selectDeclared.addEventListener("click", function () {
        checks.forEach(function (c) {
          c.checked = !c.disabled && c.getAttribute("data-status") === "declared";
        });
        updateCount();
      });
    }

    if (btnProbe) {
      btnProbe.addEventListener("click", function () {
        runBatch("probe", "probe", function (id) {
          return postJobTrack("/api/servers/" + id + "/actions/probe", null);
        });
      });
    }

    if (btnInv) {
      btnInv.addEventListener("click", function () {
        runBatch("inventory", "inventory", function (id) {
          return postJobTrack("/api/servers/" + id + "/actions/inventory", null);
        });
      });
    }

    if (btnPowerOn) {
      btnPowerOn.addEventListener("click", function () {
        var n = selectedChecks().length;
        if (!window.confirm("Power on " + n + " selected server(s)?")) return;
        runBatch("power on", "power-on", function (id) {
          return postJobTrack("/api/servers/" + id + "/actions/reset", {
            reset_type: "On",
          });
        });
      });
    }

    function confirmInstalledThen(action, firstConfirm) {
      if (firstConfirm === false) return false;
      var installed = window.CopperInstalled
        ? window.CopperInstalled.installedNamesFromChecks(selectedChecks())
        : [];
      if (
        window.CopperInstalled &&
        !window.CopperInstalled.confirmDestructive(installed, action)
      ) {
        return false;
      }
      return true;
    }

    if (btnPowerOff) {
      btnPowerOff.addEventListener("click", function () {
        var n = selectedChecks().length;
        if (
          !window.confirm(
            "Power off " + n + " selected server(s)?\nUses ForceOff (immediate)."
          )
        ) {
          return;
        }
        if (!confirmInstalledThen("Force power-off")) return;
        runBatch("power off", "power-off", function (id) {
          return postJobTrack("/api/servers/" + id + "/actions/reset", {
            reset_type: "ForceOff",
          });
        });
      });
    }

    if (btnReboot) {
      btnReboot.addEventListener("click", function () {
        var n = selectedChecks().length;
        if (!window.confirm("Reboot " + n + " selected server(s)?")) return;
        if (!confirmInstalledThen("Reboot")) return;
        runBatch("reboot", "reboot", function (id) {
          return postJobTrack("/api/servers/" + id + "/actions/reset", {
            reset_type: "ForceRestart",
          });
        });
      });
    }

    if (btnConvertUEFI) {
      btnConvertUEFI.addEventListener("click", function () {
        var n = selectedChecks().length;
        if (
          !window.confirm(
            "Convert " +
              n +
              " selected server(s) to persistent BIOS UEFI mode and reboot?\n\n" +
              "Supermicro may require SFT-DCMS / OOB license for BIOS Redfish; " +
              "if rejected, set Boot Mode in the BIOS UI."
          )
        ) {
          return;
        }
        if (!confirmInstalledThen("UEFI convert + reboot")) return;
        runBatch("convert-uefi", "convert-uefi", function (id) {
          return postJobTrack("/api/servers/" + id + "/actions/convert-uefi", {
            reboot: true,
          });
        });
      });
    }

    if (btnDelete) {
      btnDelete.addEventListener("click", function () {
        var n = selectedChecks().length;
        if (
          !window.confirm(
            "Delete " + n + " selected server(s) from inventory?\nThis cannot be undone."
          )
        ) {
          return;
        }
        runBatch("delete", "delete", function (id, _name, timeoutMs) {
          return postJSON("/api/servers/" + id + "/delete", null, timeoutMs).then(function (data) {
            removeRow(id);
            return data;
          });
        });
      });
    }

    function hideLabelsPanel() {
      if (labelsPanel) labelsPanel.hidden = true;
    }

    if (btnLabels) {
      btnLabels.addEventListener("click", function () {
        if (!selectedChecks().length) return;
        if (mountPanel) mountPanel.hidden = true;
        if (labelsPanel) {
          labelsPanel.hidden = false;
          if (labelsInput) labelsInput.focus();
        }
      });
    }
    if (labelsCancel) {
      labelsCancel.addEventListener("click", hideLabelsPanel);
    }
    if (labelsRun) {
      labelsRun.addEventListener("click", function () {
        var selected = selectedChecks();
        if (!selected.length) return;
        var labels = parseLabelInput(labelsInput && labelsInput.value);
        if (!labels.length) {
          window.alert("Enter at least one label.");
          return;
        }
        var modeEl = document.querySelector('input[name="batch_label_mode"]:checked');
        var mode = modeEl && modeEl.value === "remove" ? "remove" : "add";
        var ids = selected.map(function (c) {
          return Number(c.value);
        });
        if (
          mode === "remove" &&
          window.CopperInstalled &&
          window.CopperInstalled.hasLabel(labels)
        ) {
          var rmNames = selected.map(function (c) {
            return c.getAttribute("data-name") || c.value;
          });
          if (!window.CopperInstalled.confirmRemoveInstalled(rmNames)) return;
        }
        var timeoutMs = 30000;
        setFloatStatus(
          (mode === "remove" ? "Removing" : "Adding") +
            " labels on " +
            ids.length +
            " server(s)…",
          false
        );
        appendLocal(
          (mode === "remove" ? "remove" : "add") +
            " labels [" +
            labels.join(", ") +
            "] → " +
            ids.length +
            " server(s)",
          "local"
        );
        postJSON(
          "/api/servers/labels",
          { ids: ids, labels: labels, mode: mode },
          timeoutMs
        )
          .then(function () {
            ids.forEach(function (id) {
              var row = rowFor(id);
              if (!row) return;
              var existing = parseLabelInput(row.getAttribute("data-labels") || "");
              renderRowLabels(row, mergeLabels(existing, labels, mode));
            });
            hideLabelsPanel();
            if (labelsInput) labelsInput.value = "";
            setFloatStatus("labels updated", false);
            appendLocal("labels ok", "local");
          })
          .catch(function (err) {
            setFloatStatus(err && err.message ? err.message : String(err), true);
            appendLocal(err && err.message ? err.message : String(err), "error");
          });
      });
    }

    if (btnDetectRaid) {
      btnDetectRaid.addEventListener("click", function () {
        runBatch("detect RAID", "detect", function (id) {
          return postJobTrack("/api/servers/" + id + "/actions/detect-raid", null);
        });
      });
    }

    function raidFamilyForCheck(c) {
      var f = (c.getAttribute("data-raid") || "").trim();
      if (f) return f;
      var row = c.closest ? c.closest("tr") : null;
      if (row) return (row.getAttribute("data-raid") || "").trim();
      return "";
    }

    if (btnStorage) {
      btnStorage.addEventListener("click", function () {
        var selected = selectedChecks();
        if (!selected.length) return;
        var ids = selected.map(function (c) {
          return c.value;
        });
        var installed = window.CopperInstalled
          ? window.CopperInstalled.installedNamesFromChecks(selected)
          : [];
        if (
          window.CopperInstalled &&
          !window.CopperInstalled.confirmDestructive(
            installed,
            "Opening Storage / changing RAID layout"
          )
        ) {
          return;
        }
        if (ids.length === 1) {
          window.location.href = "/servers/" + ids[0] + "/storage";
          return;
        }
        var missing = false;
        var missingNames = [];
        var families = {};
        selected.forEach(function (c) {
          var f = raidFamilyForCheck(c);
          // Heal stale checkbox if the row already has a family (post-detect without refresh).
          if (f && f !== (c.getAttribute("data-raid") || "").trim()) {
            c.setAttribute("data-raid", f);
          }
          if (!f || f === "unknown" || f === "none") {
            missing = true;
            missingNames.push(c.getAttribute("data-name") || c.value);
          } else {
            families[f] = true;
          }
        });
        if (missing) {
          window.alert(
            "Run Detect RAID on every selected server first (and ensure a controller was found)." +
              (missingNames.length
                ? "\n\nStill missing: " + missingNames.join(", ")
                : "")
          );
          return;
        }
        if (Object.keys(families).length > 1) {
          window.alert(
            "Selected servers have different RAID families (" +
              Object.keys(families).join(", ") +
              "). Batch storage needs the same controller family."
          );
          return;
        }
        window.location.href = "/storage?ids=" + ids.join(",");
      });
    }

    if (btnSoftRaid) {
      btnSoftRaid.addEventListener("click", function () {
        var selected = selectedChecks();
        if (!selected.length) return;
        var ids = selected.map(function (c) {
          return c.value;
        });
        var installed = window.CopperInstalled
          ? window.CopperInstalled.installedNamesFromChecks(selected)
          : [];
        if (
          window.CopperInstalled &&
          !window.CopperInstalled.confirmDestructive(
            installed,
            "Opening Soft RAID (layout changes can destroy a provisioned OS)"
          )
        ) {
          return;
        }
        if (ids.length === 1) {
          window.location.href = "/servers/" + ids[0] + "/softraid";
          return;
        }
        window.location.href = "/softraid?ids=" + ids.join(",");
      });
    }

    if (btnDeploy) {
      btnDeploy.addEventListener("click", function () {
        var selected = selectedChecks();
        if (!selected.length) return;
        var ids = selected.map(function (c) {
          return c.value;
        });
        var installed = window.CopperInstalled
          ? window.CopperInstalled.installedNamesFromChecks(selected)
          : [];
        if (
          window.CopperInstalled &&
          !window.CopperInstalled.confirmDestructive(
            installed,
            "Opening Deploy OS (can overwrite disks)"
          )
        ) {
          return;
        }
        if (ids.length === 1) {
          window.location.href = "/servers/" + ids[0] + "/deploy";
          return;
        }
        window.location.href = "/deploy?ids=" + ids.join(",");
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
        if (
          window.CopperInstalled &&
          !window.CopperInstalled.confirmDestructive(
            window.CopperInstalled.installedNamesFromChecks(selectedChecks()),
            "Mount ISO + one-time boot (may reboot)"
          )
        ) {
          return;
        }
        if (mountPanel) mountPanel.hidden = true;
        runBatch("mount & boot", "mount", function (id) {
          return postJobTrack("/api/servers/" + id + "/actions/mount", {
            image_url: imageURL,
            boot_once: bootOnce,
            reboot: reboot,
          });
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
    paintBusyFromDispatcher();
  }
  window.CopperPages = window.CopperPages || {};
  window.CopperPages.initBatchServers = initBatchServers;
  initBatchServers();

  // Global busy strip under nav — reflects dispatcher busy map on every page.
  (function initServerBusyBar() {
    var bar = document.getElementById("server-busy-bar");
    if (!bar || !window.CopperJobs || bar.dataset.copperInited) return;
    bar.dataset.copperInited = "1";

    function pageServerIds() {
      var ids = [];
      var nodes = [
        document.getElementById("storage-ids-json"),
        document.getElementById("page-server-ids-json"),
      ];
      nodes.forEach(function (node) {
        if (!node) return;
        try {
          var parsed = JSON.parse(node.textContent || "[]");
          if (Array.isArray(parsed)) {
            parsed.forEach(function (id) {
              ids.push(String(id));
            });
          }
        } catch (_e) {}
      });
      document.querySelectorAll("[data-server-id]").forEach(function (el) {
        var id = el.getAttribute("data-server-id");
        if (id) ids.push(String(id));
      });
      return ids.filter(function (v, i, a) {
        return a.indexOf(v) === i;
      });
    }

    function paint() {
      var busy = window.CopperJobs.busyMap() || {};
      var ids = Object.keys(busy);
      if (!ids.length) {
        bar.hidden = true;
        bar.textContent = "";
        document.body.classList.remove("has-server-busy");
      } else {
        document.body.classList.add("has-server-busy");
        bar.hidden = false;
        bar.innerHTML = ids
          .map(function (id) {
            var info = busy[id] || {};
            var name = (info.job && info.job.server_name) || "";
            if (!name) {
              var row = document.querySelector(
                '[data-server-id="' + id + '"][data-server-name], tr[data-server-id="' + id + '"]'
              );
              if (row) name = row.getAttribute("data-server-name") || "";
            }
            if (!name) name = "server #" + id;
            var label = shortType(info.type) + " · " + (info.phase || info.status || "busy");
            return (
              '<span class="server-busy-chip">' +
              escText(name) +
              " <em>" +
              escText(label) +
              "</em></span>"
            );
          })
          .join("");
      }

      var note = document.getElementById("copper-page-busy");
      if (note) {
        var local = pageServerIds().filter(function (id) {
          return !!busy[id];
        });
        if (!local.length) {
          note.hidden = true;
          note.textContent = "";
        } else {
          note.hidden = false;
          note.textContent =
            "Busy: " +
            local
              .map(function (id) {
                var info = busy[id];
                return (
                  ((info.job && info.job.server_name) || "#" + id) +
                  " · " +
                  shortType(info.type) +
                  " · " +
                  (info.phase || info.status)
                );
              })
              .join(" · ");
        }
      }
    }

    window.CopperJobs.subscribe(function () {
      paint();
    });
    window.addEventListener("copper:navigated", paint);
    window.CopperJobs.start();
  })();

  // Remember last authed path so login / new station can return to it.
  (function initSessionResume() {
    var PATH_KEY = "copper_last_path";
    var note = document.getElementById("copper-session-note");
    var noteTimer = null;

    function currentPath() {
      return window.location.pathname + window.location.search;
    }

    function savePath(path) {
      path = path || currentPath();
      if (!path || path.indexOf("/login") === 0 || path.indexOf("/logout") === 0) return;
      try {
        localStorage.setItem(PATH_KEY, path);
      } catch (_e) {}
    }

    function showNote(html, ms) {
      if (!note) return;
      note.innerHTML = html;
      note.hidden = false;
      if (noteTimer) clearTimeout(noteTimer);
      noteTimer = setTimeout(function () {
        note.hidden = true;
        note.innerHTML = "";
      }, ms > 0 ? ms : 8000);
    }

    window.CopperSession = {
      savePath: savePath,
      currentPath: currentPath,
      showNote: showNote,
    };

    // Authed shell only.
    if (!document.getElementById("jobs-dock")) return;

    savePath();
    window.addEventListener("copper:navigated", function () {
      savePath();
    });
    window.addEventListener("beforeunload", function () {
      savePath();
    });

    window.addEventListener("copper-jobs-hydrated", function (ev) {
      var detail = ev.detail || {};
      var n = detail.activeCount || 0;
      var reason = detail.reason || "";
      if (n <= 0) return;
      if (reason === "start" || reason === "visible" || reason === "online") {
        showNote(
          "Restored <b>" +
            n +
            "</b> active job" +
            (n === 1 ? "" : "s") +
            " from the control plane. " +
            '<a href="/jobs" data-copper-nav>Open Jobs</a>',
          10000
        );
      }
    });

    // After login, if we somehow landed on / but had a deeper path saved, go there once.
    try {
      var here = currentPath();
      var saved = localStorage.getItem(PATH_KEY) || "";
      var fromLogin =
        document.referrer && document.referrer.indexOf("/login") >= 0;
      if (
        fromLogin &&
        (here === "/" || here === "") &&
        saved &&
        saved !== "/" &&
        saved.indexOf("/login") !== 0
      ) {
        window.location.replace(saved);
      }
    } catch (_e) {}
  })();

  // Thin SPA shell: keep layout + jobs dock + dispatcher; swap #app-main only.
  (function initSPA() {
    if (window.__copperSPA) return;
    // Login / unauthenticated shell has no jobs dock — do not poll or SPA-nav.
    if (!document.getElementById("jobs-dock")) return;
    window.__copperSPA = true;
    var main = document.getElementById("app-main");
    if (!main) return;
    var navigating = false;

    function sameOrigin(url) {
      try {
        var u = new URL(url, window.location.href);
        return u.origin === window.location.origin;
      } catch (_e) {
        return false;
      }
    }

    function shouldIntercept(a) {
      if (!a || a.target === "_blank" || a.hasAttribute("download")) return false;
      if (a.getAttribute("rel") === "external") return false;
      var href = a.getAttribute("href");
      if (!href || href.charAt(0) === "#" || href.indexOf("mailto:") === 0) return false;
      if (href.indexOf("/logout") === 0 || href.indexOf("/login") === 0) return false;
      if (href.indexOf("/api/") === 0) return false;
      if (href.indexOf("/static/") === 0) return false;
      if (!sameOrigin(href)) return false;
      // Only SPA-nav when marked or same-site path GET navigation.
      if (a.hasAttribute("data-copper-nav")) return true;
      var u = new URL(href, window.location.href);
      if (u.pathname.indexOf("/export/") >= 0) return false;
      if (/\.(json|zip|iso|img)(\?|$)/i.test(u.pathname)) return false;
      return true;
    }

    function runScripts(container) {
      var scripts = Array.prototype.slice.call(container.querySelectorAll("script"));
      scripts.forEach(function (old) {
        var typ = (old.getAttribute("type") || "").toLowerCase();
        // Keep JSON data islands in place. Replacing them with a bare <script>
        // drops id/type; storage/deploy then parse [] and show "No server selected".
        if (typ === "application/json" || typ === "application/ld+json") {
          return;
        }
        var s = document.createElement("script");
        Array.prototype.forEach.call(old.attributes || [], function (attr) {
          if (attr && attr.name) s.setAttribute(attr.name, attr.value);
        });
        if (old.src) {
          s.src = old.src;
          s.async = false;
        } else {
          s.textContent = old.textContent;
        }
        old.parentNode.replaceChild(s, old);
      });
    }

    function activatePage() {
      if (window.CopperPages) {
        if (window.CopperPages.initJobsPage) window.CopperPages.initJobsPage();
        if (window.CopperPages.initBatchServers) window.CopperPages.initBatchServers();
        if (window.CopperPages.initDataDHCPLeases) window.CopperPages.initDataDHCPLeases();
        if (window.CopperPages.initHostNICFilter) window.CopperPages.initHostNICFilter();
      }
      if (window.CopperSession && window.CopperSession.savePath) {
        window.CopperSession.savePath();
      }
      try {
        window.dispatchEvent(new CustomEvent("copper:navigated", { detail: { path: location.pathname } }));
      } catch (_e) {}
      if (window.CopperJobs && window.CopperJobs.hydrate) {
        window.CopperJobs.hydrate("navigate");
      }
    }

    function goLogin(returnTo) {
      returnTo = returnTo || window.location.pathname + window.location.search;
      try {
        if (returnTo.indexOf("/login") !== 0) {
          localStorage.setItem("copper_last_path", returnTo);
        }
      } catch (_e) {}
      window.location.href =
        "/login?next=" + encodeURIComponent(returnTo.indexOf("/login") === 0 ? "/" : returnTo);
    }

    function navigate(url, push) {
      if (navigating) return;
      navigating = true;
      document.body.classList.add("is-navigating");
      fetch(url, {
        credentials: "same-origin",
        headers: { Accept: "text/html", "X-Copper-SPA": "1" },
      })
        .then(function (resp) {
          if (resp.status === 401 || (resp.redirected && /\/login/i.test(resp.url))) {
            goLogin(window.location.pathname + window.location.search);
            return null;
          }
          if (!resp.ok) throw new Error("navigation failed (" + resp.status + ")");
          return resp.text().then(function (html) {
            return { html: html, finalURL: resp.url };
          });
        })
        .then(function (pack) {
          if (!pack) return;
          var doc = new DOMParser().parseFromString(pack.html, "text/html");
          var nextMain = doc.getElementById("app-main") || doc.querySelector("main");
          if (!nextMain) {
            window.location.href = url;
            return;
          }
          // Drop leftover jobs-page / Data DHCP lease pollers before swapping DOM.
          var prevBody = document.getElementById("jobs-body");
          if (prevBody && typeof prevBody._copperJobsCleanup === "function") {
            try {
              prevBody._copperJobsCleanup();
            } catch (_e) {}
          }
          var prevLeases = document.getElementById("leases-table-wrap");
          if (prevLeases && typeof prevLeases._copperCleanup === "function") {
            try {
              prevLeases._copperCleanup();
            } catch (_e2) {}
          }
          main.innerHTML = nextMain.innerHTML;
          if (doc.title) document.title = doc.title;
          runScripts(main);
          if (push) history.pushState({ copperSPA: true }, "", pack.finalURL || url);
          window.scrollTo(0, 0);
          activatePage();
        })
        .catch(function () {
          window.location.href = url;
        })
        .finally(function () {
          navigating = false;
          document.body.classList.remove("is-navigating");
        });
    }

    document.addEventListener(
      "click",
      function (ev) {
        if (ev.defaultPrevented || ev.button !== 0) return;
        if (ev.metaKey || ev.ctrlKey || ev.shiftKey || ev.altKey) return;
        var a = ev.target.closest && ev.target.closest("a[href]");
        if (!a || !shouldIntercept(a)) return;
        var url = a.href;
        if (url === window.location.href) {
          ev.preventDefault();
          return;
        }
        ev.preventDefault();
        navigate(url, true);
      },
      true
    );

    window.addEventListener("popstate", function () {
      navigate(window.location.href, false);
    });

    if (window.CopperJobs) window.CopperJobs.start();
  })();
})();
