# Copper — Project Structure

This document describes how the **felez / Copper** workspace is organized: what each module does, how the pieces talk to each other, and where important code lives.

Copper is a lightweight bare-metal control stack: inventory servers, drive BMCs over Redfish, serve install media on the OOB network, and manage DHCP for both OOB (BMC addressing) and data NIC (PXE) — without Ironic.

---

## Workspace layout

Go workspace (`go.work`) with three modules:

```
felez/
├── go.work                          # Workspace: copper_bmc, copper_control_plane, copper_dhcp
├── README.md                        # Operator-facing overview and run instructions
├── structure.md                     # This file
│
├── copper_dhcp/                     # Shared DHCPv4 library (no binary)
├── copper_bmc/                      # Bare-metal agent binary
└── copper_control_plane/            # Inventory UI + SQLite + policy owner binary
```

| Module | Role | Binary? |
|--------|------|---------|
| `copper_dhcp` | Shared DHCPv4 server (pool, MAC filters, reservations, optional PXE next-server/boot-file) | No — library only |
| `copper_bmc` | Agent near hardware: media server + Redfish proxy + optional OOB DHCP | Yes — `cmd/copper_bmc` |
| `copper_control_plane` | Web UI, SQLite inventory/credentials, OOB DHCP policy push, local data DHCP | Yes — `cmd/copper_control_plane` |

Both binaries `replace copper_dhcp => ../copper_dhcp`.

---

## Architecture (network planes)

Copper deliberately splits networks:

```
                    ┌─────────────────────────────────────┐
  Browser ─────────►│  copper_control_plane               │
                    │  :9446  Web UI + SQLite             │
                    │  Data DHCP (optional, UDP :67)      │
                    └──────────────┬──────────────────────┘
                                   │ HTTP command API
                                   │ (control-plane NIC)
                    ┌──────────────▼──────────────────────┐
                    │  copper_bmc                         │
                    │  Command API :9090                  │
                    │  Media HTTP  :8080  (OOB NIC)       │
                    │  OOB DHCP (optional, if role on)    │
                    └───────┬───────────────┬─────────────┘
                            │               │
                     Redfish│               │ HTTP GET ISOs
                            ▼               ▼
                         BMCs            BMCs (virtual media)
```

1. **OOB / BMC plane** — BMCs reach `copper_bmc` media (ISO download) and optional OOB DHCPv4 on the agent.
2. **Control-plane plane** — two talk modes:
   - **listen** (default) — CP dials `copper_bmc` command API; credentials stay in the control plane and are sent per request (`X-Copper-BMC-*` headers).
   - **poll** — `copper_bmc` dials CP (`/api/agent/poll/*`): heartbeat updates a last-known cache (`as_of` / `last_seen_at`), long-polls for enqueued commands, posts results. Same Redfish handlers run in-process on the agent; CP never opens `:9090`.
3. **Data NIC plane** — Host PXE DHCP runs **inside** `copper_control_plane` (not on the agent), with next-server + boot-file.

---

## Module: `copper_dhcp`

Shared library used by both runtimes. Built on [`insomniacslk/dhcp`](https://github.com/insomniacslk/dhcp).

```
copper_dhcp/
├── go.mod
├── config.go          # Config, Normalize, MAC allow/deny/reservations
├── config_test.go
├── lease.go           # In-memory lease table + pool allocation
├── manager.go         # Runtime Apply/Status/Leases (start/stop without process restart)
└── server.go          # DHCPv4 Serve / handle Discover/Offer/Request/Ack
```

**Key types**

- `Config` — declarative policy (subnet, pool, gateway, DNS, lease time, next-server, boot-file, MAC filters, reservations).
- `Manager` — owns a running `Server`; `Apply(cfg)` validates, restarts or stops.
- `Server` — UDP listener (typically port 67); needs root or `CAP_NET_BIND_SERVICE`.

**Usage split**

| Consumer | Purpose | PXE fields |
|----------|---------|------------|
| `copper_bmc` (if DHCP role enabled) | OOB/BMC addressing only | Leave empty |
| `copper_control_plane` | Data NIC + PXE | `next_server` + `boot_file` |

---

## Module: `copper_bmc`

Agent that sits near the hardware. Config is **interactive at startup** (no YAML for listeners). Redfish credentials are **never stored** on the agent.

```
copper_bmc/
├── cmd/copper_bmc/main.go           # Prompt config → app.Run
├── internal/
│   ├── app/app.go                   # Wires media + command (+ optional DHCP) listeners
│   ├── config/config.go             # Interactive Prompt: media/command NICs, DHCP role
│   ├── media/server.go              # HTTP file server for ISOs (OOB plane)
│   ├── command/api.go               # Northbound JSON API for control plane
│   ├── redfishx/client.go           # Southbound Redfish via gofish
│   ├── activity/buffer.go           # Ring buffer of events (console + /v1/activity)
│   └── netx/arp.go                  # IP → MAC via /proc/net/arp (OOB L2)
├── iso/                             # Sample / local media directory (operator-provided)
├── bin/                             # Built binary (optional)
└── go.mod
```

### Startup planes

| Plane | Default | Purpose |
|-------|---------|---------|
| Media | `:8080` on OOB-facing NIC | Static files BMCs pull for virtual media |
| Command | `:9090` on CP-facing NIC | **Listen mode:** control plane issues Redfish/DHCP/activity ops |
| Poll | outbound to CP URL | **Poll mode:** heartbeat + long-poll instead of opening `:9090` |
| DHCP role | Off unless prompted `Y` | Accepts DHCP config **only** from control plane |

### Command API (`internal/command`)

| Method | Path | Notes |
|--------|------|--------|
| GET | `/v1/health` | Service + plane + DHCP role status |
| GET | `/v1/activity` | Poll agent events (`since`, `limit`, `component`) |
| GET | `/v1/system` | Full Redfish inventory |
| GET | `/v1/system/storage` | Storage-focused inventory |
| GET/POST | `/v1/system/boot` | Read / set boot override |
| GET | `/v1/system/virtual-media` | List virtual media devices |
| POST | `/v1/system/virtual-media/mount` | Mount image URL + boot |
| POST | `/v1/system/actions/reset` | Power reset |
| GET/PUT | `/v1/dhcp/config` | OOB DHCP policy (role must be enabled) |
| GET | `/v1/dhcp/leases` | Current OOB leases |
| GET | `/v1/net/arp?ip=` | Resolve BMC MAC on agent’s OOB L2 |

BMC target per request: headers `X-Copper-BMC-Endpoint|Username|Password|Insecure`, or JSON `"bmc"` on POST bodies.

### Redfish client (`internal/redfishx`)

Wraps `github.com/stmcginnis/gofish`. Main operations: `Inventory`, `Storage`, `Boot` / `SetBoot`, `ListVirtualMedia`, `MountBootMedia`, `Reset`. Inventory aggregates system, CPU, RAM, storage, network, GPU, PCIe, PSU, fans, BMC.

---

## Module: `copper_control_plane`

Admin UI and source of truth for server inventory, BMC credentials, and DHCP policy.

```
copper_control_plane/
├── cmd/copper_control_plane/main.go # Load YAML, open SQLite, wire agent + local DHCP + UI + jobs
├── config.yaml                      # master_password, listen, database, optional agent host
├── data/                            # Runtime DB / scratch (control_plane.db, etc.)
├── internal/
│   ├── config/config.go             # YAML load + optional agent host prompt
│   ├── store/                       # SQLite: servers, settings, snapshots, DHCP JSON, jobs
│   ├── agent/client.go              # HTTP client → copper_bmc command API
│   ├── jobs/jobs.go                 # In-process job registry (per-server serialize + history)
│   ├── provision/                   # Live-OS image write / preflight / cidata
│   ├── raidstate/                   # Vendor-neutral RAID topology + drive state machine (MegaRAID/Smart Array)
│   ├── storage/                     # RAID detect/config + SSH host helpers (lsblk/sysfs partitions, wipe); wraps raidstate.Controller
│   ├── datamedia/                   # Data-plane cloud-image / ISO HTTP
│   └── web/
│       ├── server.go                # Routes, auth, form Redfish actions
│       ├── jobhandlers.go           # Job type handlers + withServerLock helper
│       ├── jobs_api.go              # GET /api/jobs…
│       ├── provision.go / storage.go / batch.go
│       ├── embed.go                 # embed templates + static
│       └── templates/               # HTML UI pages (incl. jobs, deploy, storage)
└── go.mod
```

### Config (`config.yaml`)

- `master_password` — login for the web UI (required)
- `listen` — UI bind (default in example: `0.0.0.0:9446`)
- `database` — SQLite path (default `data/control_plane.db`)
- `agent_mode` — `listen` (default) or `poll`; prompted at startup
- `copper_bmc_host` / `copper_bmc_port` — listen mode only; prompted at startup if missing

DHCP pool policy is **not** in YAML; it lives in the UI and SQLite settings.

**Poll mode tables/settings:** `agent_commands` (queued ops with `queued_at` / `delivered_at` / `completed_at` / `expires_at`) and settings key `agent_poll_cache` (last heartbeat: OS, DHCP/LLDP, leases, neighbors, media catalog, activity).

### SQLite schema (`internal/store`)

| Table | Purpose |
|-------|---------|
| `servers` | Name, Redfish endpoint, username/password, insecure flag, BMC MAC, notes |
| `settings` | Key/value: agent host/port, `agent_dhcp_config`, `local_dhcp_config`, batch runtime |
| `snapshots` | Cached Redfish JSON / storage detect / layout per server + kind |
| `jobs` | Durable job history (status, phase, progress, log, payload/result) |

### Agent client (`internal/agent`)

Thin HTTP wrapper: attaches BMC headers from a `store.Server`, calls the same `/v1/*` routes as above. Also: health, DHCP get/put/leases, activity poll, ARP lookup.

### Jobs (`internal/jobs`) — concurrency rules

Background work (probe, inventory, reset, mount, RAID `storage.detect` / `storage.config` / `storage.exec` / `storage.wipe-partitions`, provision preflight/write, inventory claim-nics) goes through an **in-process job registry**, not a separate broker.

**Two hard rules**

1. **Same baremetal (`ServerID`) → serialize.** At most one running op per server. Probe then RAID on server1 must queue; they never overlap.
2. **Different baremetals → parallel.** Server1 and server2 may run at once (OS install, partition scan, RAID, Redfish), capped by a global executor limit (settings / `SetExecutors`, default 3).

**How it works**

| Piece | Role |
|-------|------|
| `Registry.Enqueue` | Create `queued` job → persist → `go run()` |
| Per-server lock | Acquired **before** a global executor slot (so waiters do not starve other servers) |
| Global executors | Cap cross-server parallelism |
| `Persist` (SQLite) | Upsert on every status/log change; on CP restart, leftover `queued`/`running` → `error` (“interrupted”) — **no auto-resume / no auto-retry** |
| Status | `queued` → `running` → `ok` \| `error` \| `cancelled` |

**Sync paths share the same lock.** Anything that touches a host/BMC **outside** `Enqueue` must call `Registry.WithServer` (via `web.withServerLock`), including:

- Form Redfish actions on the server detail page (`runAction` / probe)
- Live SSH APIs: `/host/partitions`, `/host/disks`, `/host/nics`, layout refresh / pin-direct
- Provision gate: batch preflight, cidata NIC resolve, target-disk resolve

Nested work inside a job (e.g. mount → live-OS NIC claim, provision → boot/eject/reboot) stays under that job’s server lock — do not take the lock again (not reentrant).

**UI:** enqueue returns immediately with `job_id`; the Jobs dock / `/jobs` / `GET /api/jobs` poll progress. Batch actions enqueue one job per selected server (cross-server parallel; same server never selected twice in one batch row).

### Web UI routes (`internal/web`)

Auth: cookie session after login with `master_password`.

| Area | Paths | Behavior |
|------|-------|----------|
| Auth | `/login`, `/logout` | Master password gate |
| Servers | `/`, `/servers`, `/servers/new`, `/servers/{id}`, edit/delete | CRUD inventory |
| Import | `/servers/import` | CSV declared inventory (`chassis_id,bmc_ip,mac_address,username,password`) |
| Sync | `POST /servers/sync-dhcp` | Match OOB DHCP leases → fill MAC/IP, mark `bound` |
| Actions (forms) | `POST .../actions/{probe,inventory,boot,virtual-media,reset,mount,…}` | Sync Redfish under **per-server lock** |
| Actions (API) | `POST /api/servers/{id}/actions/{probe,inventory,reset,mount,detect-raid}` | **Enqueue** jobs |
| Storage / Deploy | `/storage`, `/servers/{id}/storage`, `/deploy`, related `/api/...` | RAID detect/config/exec + partition wipe via **jobs**; live SSH scans (`GET .../host/partitions`, disks, nics) via **`WithServer`** |
| Jobs | `/jobs`, `GET /api/jobs`, `GET /api/jobs/{id}` | List / poll job status |
| OOB DHCP | `/dhcp/oob` (+ save / push) | Policy saved locally; **push** → agent `PUT /v1/dhcp/config` |
| Data DHCP | `/dhcp/data` (+ save / apply), `POST /api/dhcp/data/claim-nics` | Runs `copper_dhcp.Manager` **in this process**; Resolve on unknown leases enqueues `inventory.claim-nics` |
| Activity | `/activity`, `/api/agent/activity` | Live mirror of agent event buffer |

**CSV import / binding:** rows are stored as `status=declared` (not assumed online). Username/password required; each row needs at least one of chassis_id, bmc_ip, mac_address. Become `bound` via: (1) **Probe** — admin sets BMC IP if needed, then verifies Redfish with stored creds; or (2) OOB DHCP lease match (MAC preferred, then IP). While `declared`, Inventory / Boot / Virtual media / Reboot / Mount stay disabled.

Templates: `layout`, `login`, `servers`, `server_form`, `server_detail`, `dhcp`, `dhcp_data`, `activity`, `jobs`, `deploy`, `storage`, `settings`.

---

## Data flow (typical ops)

### Inspect / act on a server

1. Admin adds server (endpoint + credentials) in UI → SQLite.
2. **Preferred:** API/batch action → `jobs.Enqueue` → handler → `agent.Client` → `copper_bmc` `/v1/...` with BMC headers (serialized per server).
3. **Detail-page forms** still call the agent synchronously, but under the same per-server lock as jobs.
4. Agent opens Redfish to that BMC via `redfishx`, returns JSON.
5. Control plane may cache inventory in `snapshots`; job state lands in `jobs` + the Jobs UI.

### Virtual media install

1. ISO placed in agent media folder; served on OOB `:8080`.
2. Control plane asks agent to mount `image_url` pointing at that OOB media URL (boot override; Supermicro prefers `UsbCd` when advertised).
3. BMC pulls the file from the media plane and boots from it.
4. **After reboot (same `redfish.mount` job, still under that server’s lock):** claim missing data NICs. Data DHCP leases for **known** inventory MACs are preferred SSH targets; **orphan** leases (MAC not in any inventory — e.g. external-only cable) are tried next. ≤2 SSH attempts per IP. On the host, list NICs (`ip -j` / sysfs); if a known inventory MAC (or DMI serial/uuid) matches this server, merge new MACs into inventory (`source: live_os`) so “Host NICs from Inventory” updates. Forgiving on SSH/mismatch; mount stays `ok` if claim fails.
5. **Manual Resolve (Data DHCP → Leases on this host):** unknown rows expose **Resolve** → job `inventory.claim-nics` (ServerID 0 for discovery; inventory write under `WithServer` for the matched host). Same SSH/match/merge rules; after success the lease binds to that server and stops looking orphaned.

### OOB DHCP

1. Agent started with DHCP role enabled (empty until configured).
2. Admin sets pool/MAC filters in **OOB DHCP** UI → saved in SQLite.
3. **Save & push** → agent applies via `Manager.Apply`.

### Data / PXE DHCP

1. Admin sets pool + **next-server** + **boot file** in **Data DHCP** UI.
2. **Apply** starts DHCP inside the control-plane process (port 67).
3. Hosts on the data NIC get addressing + PXE instructions; TFTP/HTTP boot files are typically elsewhere (`next_server`).

---

## Compatibility policy (multi-appliance)

Copper must drive **several different appliances** (BMC vendors, MegaRAID generations, Smart Array, HBA/tri-mode cards, OS images). There is often **no single generic command** that works everywhere for the same intent (e.g. “expose free disks to the OS as JBOD”).

**Rules of thumb**

1. **Prefer intent over one CLI.** Callers ask for outcomes (expose passthrough, make UGood, create VD). Drivers translate intent using whatever the live hardware supports.
2. **Probe, then choose.** Read controller/firmware capabilities first (`personality`, `jbod`, `autoconfig`, family detect). Do not assume every MegaRAID speaks the same dialect.
3. **Try known methods in a compatibility matrix** when one approach is not universal. Document which generation uses which path; implement the matrix in code (planner / caps), not as ad-hoc one-offs in the UI.
4. **Sequence + re-check.** A controller-level method may already achieve the goal (e.g. `autoconfig=JBOD` auto-converts UGood). Always re-inventory before failing a later per-drive method; treat “already in desired state” as success, not as a hard error that confuses operators.
5. **If it cannot be solved generically**, keep an explicit matrix (vendor × generation × action → commands / reboot / limitations) in code comments and this doc, and grow it when a new appliance is proven — do not silently pretend one path covers all.

**Example — MegaRAID “expose free as JBOD”** (`internal/storage/storcli_state.go`, `storcli.go`)

| Generation (examples) | Controller-level method | Per-drive | Notes |
|----------------------|-------------------------|-----------|--------|
| Legacy (9260 / 9271 / 9361) | `set jbod=on` | `… set jbod` | Immediate |
| SAS3.5 (9500 / 9600) | `set autoconfig=JBOD` | `… set jbod` if still UGood | Autoconfig often converts drives; redundant `set jbod` may return ErrCd 50 — verify state and treat already-JBOD as ok |
| Tri-mode (9460 / 9560) | `set personality=JBOD` | after reboot | Needs opt-in + reboot before per-drive |

Smart Array and other families keep their own drivers; same policy — detect, matrix, verify — not one shared CLI string.

---

## Dependencies (notable)

| Package | Used by | For |
|---------|---------|-----|
| `github.com/stmcginnis/gofish` | `copper_bmc` | Redfish |
| `github.com/insomniacslk/dhcp` | `copper_dhcp` | DHCPv4 |
| `modernc.org/sqlite` | `copper_control_plane` | Pure-Go SQLite |
| `gopkg.in/yaml.v3` | `copper_control_plane` | `config.yaml` |

Go version: **1.26+** (workspace and modules).

---

## Mental model (short)

- **`copper_control_plane`** = brain + inventory + credentials + DHCP policy owner + data-plane DHCP + **job queue**.
- **`copper_bmc`** = hands near metal: serve media, speak Redfish to many BMCs, optionally run OOB DHCP when told.
- **`copper_dhcp`** = one DHCP implementation, two deployments (OOB on agent, data/PXE on control plane).
- **Jobs:** one op at a time per baremetal; many baremetals in parallel. Sync SSH/Redfish must use the same lock (`WithServer`).

Credentials and policy live north of the agent; the agent is a multi-BMC executor with a split network face.
