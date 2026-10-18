# Copper

A lightweight bare-metal control stack: keep a server inventory and drive BMCs over Redfish without leaning on Ironic.

## Why this exists

While installing OSes on bare metal, we repeatedly hit bad network configs (for example PortFast left on) and needed a clear, trustworthy picture of our server inventory. We looked at how Ironic approaches this and did not like that model for our setup, so we built our own solution:

- **`copper_bmc`** — agent that sits near the hardware: serves install media on the OOB/BMC network and exposes a command API on the control-plane network; optional DHCPv4 role for BMC IPs
- **`copper_control_plane`** — web UI + SQLite inventory of servers and BMC credentials; talks to `copper_bmc` to inspect systems, mount virtual media, set boot order, reset hosts; owns OOB DHCP policy for the agent and runs data-NIC DHCP (with PXE next-server / boot-file) locally

## Requirements

- Go 1.26+
- Network reachability:
  - BMCs → `copper_bmc` media listener (ISO / image download)
  - Control plane → `copper_bmc` command API
  - Your browser → `copper_control_plane` web UI
  - Hosts on the data NIC → control plane DHCP (port 67) when Data DHCP is enabled

## How to run

Run **`copper_bmc` first**, then the control plane so it can point at the agent.

### 1. copper_bmc

```bash
cd copper_bmc
go run ./cmd/copper_bmc
```

Startup is interactive. You will be asked for:

| Prompt | Default | Purpose |
|--------|---------|---------|
| Media listen port | `8080` | HTTP file server for ISOs (OOB / BMC plane) |
| Folder to serve | _(required)_ | Directory of images BMCs can pull |
| Media bind NIC | `0.0.0.0` | NIC/IP facing the BMC network |
| Command API listen port | `9090` | API for the control plane |
| Command bind NIC | `0.0.0.0` | NIC/IP facing the control-plane network |
| Enable DHCP server role? | `N` | If yes, agent accepts DHCP config from the control plane only |

Redfish credentials are **not** stored on the agent — the control plane sends a BMC target with each request.

When it is up you should see both planes, for example:

- Media: `http://<oob-ip>:8080/`
- Command API: `http://<cp-ip>:9090/` (`GET /v1/health`, `GET /v1/activity`, system/boot/virtual-media/reset routes; DHCP routes if role enabled)

Agent activity (DHCP offers/ACKs, LLDP neighbors, media hits, Redfish ops) is printed on the `copper_bmc` console and mirrored to the control plane **Activity** page (and a DHCP-only live console on **OOB DHCP**).

**LLDP:** `copper_bmc` passively captures LLDP on the OOB media NIC (`GET /v1/net/lldp`, needs `CAP_NET_RAW`). Control plane **Sync OOB DHCP + LLDP** and per-server **Lookup chassis (LLDP)** fill `chassis_id` (and MAC gaps) by matching neighbor src MAC / chassis id / mgmt IP.

### 2. copper_control_plane

Edit `copper_control_plane/config.yaml` (at least set `master_password`):

```yaml
master_password: "changeme"
listen: "0.0.0.0:9446"
database: "data/control_plane.db"
# Optional — if omitted, the CLI asks at startup:
# copper_bmc_host: "192.168.10.20"
# copper_bmc_port: 9090
```

Then:

```bash
cd copper_control_plane
go run ./cmd/copper_control_plane -config config.yaml
```

Open the UI at `http://<host>:9446/` and log in with `master_password`.

| UI page | Plane | What it does |
|---------|--------|----------------|
| **OOB DHCP** | BMC / management | Addressing-only pool + MAC filters; **Save & push** to `copper_bmc` (agent DHCP role must be enabled). No PXE fields. |
| **Data DHCP** | Host data NIC | Runs DHCPv4 **in this process** via `copper_dhcp`. Includes **next-server** (where to go) and **boot file** (what to download) for PXE. Port 67 needs root or `CAP_NET_BIND_SERVICE`. |

### Next-server vs boot file (Data DHCP)

They are two halves of the same PXE instruction:

- **Next-server** — IP of the host holding boot files (usually TFTP). Sent as DHCP `siaddr` and option 66. Often a different machine than DHCP; if unset, many clients fall back to the DHCP server IP.
- **Boot file** — name/path/URL to download from there (`pxelinux.0`, `ipxe.efi`, or `http://…/boot.ipxe`). Option 67 (and the legacy header `file` field when short enough).

Example: next-server `10.0.0.5` + boot file `ipxe.efi` → client does TFTP get of `ipxe.efi` from `10.0.0.5`.

## DHCP model

| Piece | Role |
|-------|------|
| `copper_bmc` startup | Admin chooses whether this agent **may** run OOB DHCP |
| **OOB DHCP** UI | Owns BMC addressing policy; push applies it on the agent |
| **Data DHCP** UI | Owns data-NIC / PXE policy; apply starts DHCP in the control plane |
| `copper_dhcp` | Shared library (`insomniacslk/dhcp`) used by both |

## Build binaries (optional)

```bash
cd copper_bmc && go build -o copper_bmc ./cmd/copper_bmc
cd ../copper_control_plane && go build -o copper_control_plane ./cmd/copper_control_plane
```

## Layout

```
root_dir/
├── copper_dhcp/             # Shared DHCPv4 library
├── copper_bmc/              # Bare-metal agent (media + Redfish + optional OOB DHCP)
└── copper_control_plane/    # Inventory UI + SQLite + OOB policy + local data DHCP
```
