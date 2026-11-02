// CopperJobs — centralized task dispatcher (long-lived across SPA navigations).
// Source of truth for the UI: /api/jobs (+ lock holders in response.busy).
// Pages subscribe; they must not invent their own busy / sort authority.
window.CopperJobs = (function () {
  var byId = {}; // id -> job
  var lockHolders = {}; // serverId string -> holder id
  var listeners = [];
  var pollTimer = null;
  var pollInFlight = false;
  var started = false;
  var reconnectBound = false;

  function sleep(ms) {
    return new Promise(function (resolve) {
      setTimeout(resolve, ms);
    });
  }

  function parseResult(job) {
    var result = job && job.result;
    if (typeof result === "string") {
      try {
        result = JSON.parse(result);
      } catch (_e) {}
    }
    return result && typeof result === "object" ? result : {};
  }

  function isActive(job) {
    return job && (job.status === "queued" || job.status === "running");
  }

  function createdMs(job) {
    if (!job || !job.created_at) return 0;
    var t = Date.parse(job.created_at);
    return isNaN(t) ? 0 : t;
  }

  function emit(name, detail) {
    try {
      window.dispatchEvent(new CustomEvent(name, { detail: detail || {} }));
    } catch (_e) {}
    listeners.forEach(function (fn) {
      try {
        fn(name, detail || {});
      } catch (_e2) {}
    });
  }

  function notify() {
    emit("copper-jobs-changed", { jobs: listSorted(), busy: busyMap() });
  }

  function readJSON(resp) {
    return resp.text().then(function (text) {
      var trimmed = (text || "").trim();
      if (!trimmed) {
        throw new Error("empty response");
      }
      if (trimmed.charAt(0) === "<") {
        throw new Error(
          resp.redirected || /login/i.test(resp.url)
            ? "session expired — reload and log in"
            : "server returned HTML instead of JSON (HTTP " + resp.status + ")"
        );
      }
      try {
        return JSON.parse(trimmed);
      } catch (_err) {
        throw new Error("invalid JSON from " + resp.url);
      }
    });
  }

  function remember(job) {
    if (!job || !job.id) return false;
    var prev = byId[job.id];
    var prevStatus = prev && prev.status;
    byId[job.id] = job;
    var changed = !prev || prev.status !== job.status || prev.phase !== job.phase || prev.progress !== job.progress;
    if (prevStatus && prevStatus !== job.status) {
      emit("copper-job-status", { prev: prevStatus, job: job, result: parseResult(job) });
    }
    if (
      (job.status === "ok" || job.status === "error" || job.status === "cancelled") &&
      prevStatus !== job.status
    ) {
      emit("copper-job-done", { job: job, result: parseResult(job) });
    }
    return changed;
  }

  function track(jobOrId) {
    var id = typeof jobOrId === "string" ? jobOrId : jobOrId && jobOrId.id;
    if (!id) return;
    if (!byId[id]) {
      byId[id] = {
        id: id,
        status: (jobOrId && jobOrId.status) || "queued",
        type: (jobOrId && jobOrId.type) || "",
        server_id: jobOrId && jobOrId.server_id,
        server_name: jobOrId && jobOrId.server_name,
        created_at: (jobOrId && jobOrId.created_at) || new Date().toISOString(),
        phase: (jobOrId && jobOrId.phase) || "queued",
      };
    } else if (jobOrId && typeof jobOrId === "object") {
      remember(Object.assign({}, byId[id], jobOrId, { id: id }));
    }
    bumpPoll();
    notify();
  }

  function trackMany(jobs) {
    (jobs || []).forEach(function (j) {
      if (!j) return;
      if (typeof j === "string") track(j);
      else track(j.job_id || j.id || j);
    });
  }

  function applyBusy(busy) {
    var next = {};
    if (busy && typeof busy === "object") {
      Object.keys(busy).forEach(function (k) {
        next[String(k)] = String(busy[k]);
      });
    }
    var same =
      Object.keys(next).length === Object.keys(lockHolders).length &&
      Object.keys(next).every(function (k) {
        return lockHolders[k] === next[k];
      });
    lockHolders = next;
    return !same;
  }

  function listSorted(filter) {
    filter = filter || {};
    var out = Object.keys(byId).map(function (id) {
      return byId[id];
    });
    if (filter.status) {
      out = out.filter(function (j) {
        return j.status === filter.status;
      });
    }
    if (filter.type) {
      out = out.filter(function (j) {
        return j.type === filter.type;
      });
    }
    if (filter.server_id) {
      var sid = Number(filter.server_id);
      out = out.filter(function (j) {
        return Number(j.server_id) === sid;
      });
    }
    if (filter.activeOnly) {
      out = out.filter(isActive);
    }
    out.sort(function (a, b) {
      var d = createdMs(b) - createdMs(a);
      if (d !== 0) return d;
      return String(b.id).localeCompare(String(a.id));
    });
    if (filter.limit > 0 && out.length > filter.limit) {
      out = out.slice(0, filter.limit);
    }
    return out;
  }

  function busyMap() {
    var map = {};
    Object.keys(lockHolders).forEach(function (sid) {
      map[sid] = {
        server_id: Number(sid),
        holder: lockHolders[sid],
        sync: String(lockHolders[sid]).indexOf("sync:") === 0,
        job: byId[lockHolders[sid]] || null,
        type: (byId[lockHolders[sid]] && byId[lockHolders[sid]].type) || "sync",
        phase: (byId[lockHolders[sid]] && byId[lockHolders[sid]].phase) || "busy",
        status: (byId[lockHolders[sid]] && byId[lockHolders[sid]].status) || "running",
      };
    });
    listSorted({ activeOnly: true }).forEach(function (job) {
      if (!job.server_id) return;
      var sid = String(job.server_id);
      if (map[sid]) return; // lock holder already set
      map[sid] = {
        server_id: job.server_id,
        holder: job.id,
        sync: false,
        job: job,
        type: job.type,
        phase: job.phase || job.status,
        status: job.status,
      };
    });
    return map;
  }

  function getBusy(serverId) {
    if (serverId == null || serverId === "") return null;
    return busyMap()[String(serverId)] || null;
  }

  function isServerBusy(serverId) {
    return !!getBusy(serverId);
  }

  function onLoginPage() {
    try {
      return window.location.pathname.indexOf("/login") === 0;
    } catch (_e) {
      return false;
    }
  }

  function redirectToLogin() {
    // Never reload /login in a loop when /api/jobs returns 401.
    if (onLoginPage()) return;
    try {
      var here = window.location.pathname + window.location.search;
      if (here.indexOf("/login") !== 0) {
        localStorage.setItem("copper_last_path", here);
      }
    } catch (_e) {}
    var next = window.location.pathname + window.location.search;
    if (next.indexOf("/login") === 0) next = "/";
    window.location.href =
      "/login?next=" + encodeURIComponent(next || "/");
  }

  function list(params) {
    var q = new URLSearchParams();
    params = params || {};
    if (params.status) q.set("status", params.status);
    if (params.type) q.set("type", params.type);
    if (params.server_id) q.set("server_id", String(params.server_id));
    if (params.limit) q.set("limit", String(params.limit));
    var url = "/api/jobs" + (q.toString() ? "?" + q.toString() : "");
    return fetch(url, {
      credentials: "same-origin",
      headers: { Accept: "application/json" },
    }).then(function (resp) {
      if (resp.status === 401) {
        redirectToLogin();
        throw new Error("unauthorized — log in again");
      }
      return readJSON(resp).then(function (data) {
        if (!resp.ok || data.ok === false) {
          throw new Error((data && data.error) || "jobs list failed");
        }
        var changed = applyBusy(data.busy);
        (data.jobs || []).forEach(function (j) {
          if (remember(j)) changed = true;
        });
        if (changed) notify();
        return data.jobs || [];
      });
    });
  }

  // Pull full job/busy snapshot from the control plane (reconnect / tab focus).
  function hydrate(reason) {
    if (onLoginPage()) {
      return Promise.resolve({
        reason: reason || "hydrate",
        jobs: [],
        active: [],
        busy: {},
        activeCount: 0,
      });
    }
    return list({ limit: 100 }).then(function () {
      var active = listSorted({ activeOnly: true });
      var busy = busyMap();
      var detail = {
        reason: reason || "hydrate",
        jobs: listSorted(),
        active: active,
        busy: busy,
        activeCount: active.length,
      };
      emit("copper-jobs-hydrated", detail);
      notify();
      return detail;
    });
  }

  function pollUntilDone(jobId, timeoutMs, onUpdate) {
    track(jobId);
    var deadline = Date.now() + (timeoutMs > 0 ? timeoutMs : 600000);
    return (async function () {
      for (;;) {
        var resp = await fetch("/api/jobs/" + encodeURIComponent(jobId), {
          credentials: "same-origin",
          headers: { Accept: "application/json" },
        });
        var data = await readJSON(resp);
        if (!resp.ok) {
          throw new Error((data && data.error) || "job poll failed");
        }
        var job = data.job || {};
        remember(job);
        notify();
        if (typeof onUpdate === "function") onUpdate(job);
        if (job.status === "ok") return job;
        if (job.status === "error" || job.status === "cancelled") {
          throw new Error(job.error || "job " + job.status);
        }
        if (Date.now() > deadline) {
          throw new Error("job timed out after " + Math.round(timeoutMs / 1000) + "s");
        }
        await sleep(1000);
      }
    })();
  }

  function bumpPoll() {
    if (!started) start();
    else refreshSoon();
  }

  function refreshSoon() {
    if (pollTimer) {
      clearTimeout(pollTimer);
      pollTimer = null;
    }
    pollTimer = setTimeout(tick, 200);
  }

  function hasActive() {
    return listSorted({ activeOnly: true }).length > 0 || Object.keys(lockHolders).length > 0;
  }

  function tick() {
    if (pollInFlight) {
      pollTimer = setTimeout(tick, 500);
      return;
    }
    pollInFlight = true;
    list({ limit: 100 })
      .catch(function () {})
      .finally(function () {
        pollInFlight = false;
        var ms = hasActive() ? 2000 : 8000;
        pollTimer = setTimeout(tick, ms);
      });
  }

  function bindReconnect() {
    if (reconnectBound) return;
    reconnectBound = true;
    document.addEventListener("visibilitychange", function () {
      if (document.visibilityState === "visible") hydrate("visible");
    });
    window.addEventListener("online", function () {
      hydrate("online");
    });
  }

  function start() {
    if (onLoginPage()) {
      return Promise.resolve({
        reason: "start",
        jobs: [],
        active: [],
        busy: {},
        activeCount: 0,
      });
    }
    bindReconnect();
    if (!started) {
      started = true;
      tick();
    }
    return hydrate("start");
  }

  function subscribe(fn) {
    if (typeof fn !== "function") return function () {};
    listeners.push(fn);
    try {
      fn("copper-jobs-changed", { jobs: listSorted(), busy: busyMap() });
    } catch (_e) {}
    return function unsubscribe() {
      listeners = listeners.filter(function (x) {
        return x !== fn;
      });
    };
  }

  // Enqueue helper: POST JSON, track returned job_id(s), return body.
  function run(opts) {
    opts = opts || {};
    var url = opts.url;
    if (!url) return Promise.reject(new Error("CopperJobs.run: url required"));
    var init = {
      method: opts.method || "POST",
      credentials: "same-origin",
      headers: { Accept: "application/json" },
    };
    if (opts.body != null) {
      init.headers["Content-Type"] = "application/json";
      init.body = typeof opts.body === "string" ? opts.body : JSON.stringify(opts.body);
    }
    return fetch(url, init).then(function (resp) {
      return readJSON(resp).then(function (data) {
        if (!resp.ok || (data && data.ok === false)) {
          throw new Error((data && data.error) || resp.statusText || "request failed");
        }
        if (data && data.job_id) track(data.job_id);
        if (data && data.jobs) trackMany(data.jobs);
        if (data && data.job) remember(data.job);
        notify();
        return data;
      });
    });
  }

  return {
    track: track,
    trackMany: trackMany,
    remember: remember,
    pollUntilDone: pollUntilDone,
    list: list,
    listSorted: listSorted,
    parseResult: parseResult,
    getBusy: getBusy,
    isServerBusy: isServerBusy,
    busyMap: busyMap,
    isActive: isActive,
    subscribe: subscribe,
    start: start,
    hydrate: hydrate,
    hasActive: hasActive,
    run: run,
    on: subscribe,
    knownIds: function () {
      return Object.keys(byId);
    },
    get: function (id) {
      return byId[id] || null;
    },
  };
})();
