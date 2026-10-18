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
2. **Control-plane plane** — UI talks to `copper_bmc` command API; credentials stay in the control plane and are sent per request (`X-Copper-BMC-*` headers).
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
| Command | `:9090` on CP-facing NIC | Control plane issues Redfish/DHCP/activity ops |
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
├── cmd/copper_control_plane/main.go # Load YAML, open SQLite, wire agent + local DHCP + UI
├── config.yaml                      # master_password, listen, database, optional agent host
├── data/                            # Runtime DB / scratch (control_plane.db, etc.)
├── internal/
│   ├── config/config.go             # YAML load + optional agent host prompt
│   ├── store/store.go               # SQLite: servers, settings, snapshots, DHCP JSON
│   ├── agent/client.go              # HTTP client → copper_bmc command API
│   └── web/
│       ├── server.go                # Routes, auth, handlers
│       ├── embed.go                 # embed templates + static
│       └── templates/               # HTML UI pages
└── go.mod
```

### Config (`config.yaml`)

- `master_password` — login for the web UI (required)
- `listen` — UI bind (default in example: `0.0.0.0:9446`)
- `database` — SQLite path (default `data/control_plane.db`)
- `copper_bmc_host` / `copper_bmc_port` — optional; prompted at startup if missing

DHCP pool policy is **not** in YAML; it lives in the UI and SQLite settings.

### SQLite schema (`internal/store`)

| Table | Purpose |
|-------|---------|
| `servers` | Name, Redfish endpoint, username/password, insecure flag, BMC MAC, notes |
| `settings` | Key/value: agent host/port, `agent_dhcp_config`, `local_dhcp_config` |
| `snapshots` | Cached Redfish JSON per server + kind (e.g. inventory) |

### Agent client (`internal/agent`)

Thin HTTP wrapper: attaches BMC headers from a `store.Server`, calls the same `/v1/*` routes as above. Also: health, DHCP get/put/leases, activity poll, ARP lookup.

### Web UI routes (`internal/web`)

Auth: cookie session after login with `master_password`.

| Area | Paths | Behavior |
|------|-------|----------|
| Auth | `/login`, `/logout` | Master password gate |
| Servers | `/`, `/servers`, `/servers/new`, `/servers/{id}`, edit/delete | CRUD inventory |
| Import | `/servers/import` | CSV declared inventory (`chassis_id,bmc_ip,mac_address,username,password`) |
| Sync | `POST /servers/sync-dhcp` | Match OOB DHCP leases → fill MAC/IP, mark `bound` |
| Actions | `POST .../actions/{inventory,boot,virtual-media,reset,mount,lookup-mac}` | Proxy to agent |
| OOB DHCP | `/dhcp/oob` (+ save / push) | Policy saved locally; **push** → agent `PUT /v1/dhcp/config` |
| Data DHCP | `/dhcp/data` (+ save / apply) | Runs `copper_dhcp.Manager` **in this process** |
| Activity | `/activity`, `/api/agent/activity` | Live mirror of agent event buffer |

**CSV import / binding:** rows are stored as `status=declared` (not assumed online). Username/password required; each row needs at least one of chassis_id, bmc_ip, mac_address. Become `bound` via: (1) **Probe** — admin sets BMC IP if needed, then verifies Redfish with stored creds; or (2) OOB DHCP lease match (MAC preferred, then IP). While `declared`, Inventory / Boot / Virtual media / Reboot / Mount stay disabled.

Templates: `layout`, `login`, `servers`, `server_form`, `server_detail`, `dhcp`, `dhcp_data`, `activity`.

---

## Data flow (typical ops)

### Inspect / act on a server

1. Admin adds server (endpoint + credentials) in UI → SQLite.
2. UI action → `agent.Client` → `copper_bmc` `/v1/...` with BMC headers.
3. Agent opens Redfish to that BMC via `redfishx`, returns JSON.
4. Control plane may cache inventory in `snapshots`.

### Virtual media install

1. ISO placed in agent media folder; served on OOB `:8080`.
2. Control plane asks agent to mount `image_url` pointing at that OOB media URL.
3. BMC pulls the file from the media plane and boots from it.

### OOB DHCP

1. Agent started with DHCP role enabled (empty until configured).
2. Admin sets pool/MAC filters in **OOB DHCP** UI → saved in SQLite.
3. **Save & push** → agent applies via `Manager.Apply`.

### Data / PXE DHCP

1. Admin sets pool + **next-server** + **boot file** in **Data DHCP** UI.
2. **Apply** starts DHCP inside the control-plane process (port 67).
3. Hosts on the data NIC get addressing + PXE instructions; TFTP/HTTP boot files are typically elsewhere (`next_server`).

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

- **`copper_control_plane`** = brain + inventory + credentials + DHCP policy owner + data-plane DHCP.
- **`copper_bmc`** = hands near metal: serve media, speak Redfish to many BMCs, optionally run OOB DHCP when told.
- **`copper_dhcp`** = one DHCP implementation, two deployments (OOB on agent, data/PXE on control plane).

Credentials and policy live north of the agent; the agent is a multi-BMC executor with a split network face.
