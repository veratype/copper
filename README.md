# Copper

A lightweight bare-metal control stack: keep a server inventory and drive BMCs over Redfish without leaning on Ironic.

## Why this exists

While installing OSes on bare metal, we repeatedly hit bad network configs (for example PortFast left on) and needed a clear, trustworthy picture of our server inventory. We looked at how Ironic approaches this and did not like that model for our setup, so we built our own solution:

- **`copper_bmc`** — agent that sits near the hardware: serves install media on the OOB/BMC network and exposes a command API on the control-plane network
- **`copper_control_plane`** — web UI + SQLite inventory of servers and BMC credentials; talks to `copper_bmc` to inspect systems, mount virtual media, set boot order, and reset hosts

## Requirements

- Go 1.26+
- Network reachability:
  - BMCs → `copper_bmc` media listener (ISO / image download)
  - Control plane → `copper_bmc` command API
  - Your browser → `copper_control_plane` web UI

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

Redfish credentials are **not** stored on the agent — the control plane sends a BMC target with each request.

When it is up you should see both planes, for example:

- Media: `http://<oob-ip>:8080/`
- Command API: `http://<cp-ip>:9090/` (`GET /v1/health`, system/boot/virtual-media/reset routes)

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

If `copper_bmc_host` is empty, you will be prompted for the agent IP/hostname and command port (default `9090`).

Open the UI at `http://<host>:9446/` and log in with `master_password` from the config. Add servers (BMC endpoint + credentials) in the inventory UI; operations go through the configured `copper_bmc` agent.

### Build binaries (optional)

```bash
cd copper_bmc && go build -o copper_bmc ./cmd/copper_bmc
cd ../copper_control_plane && go build -o copper_control_plane ./cmd/copper_control_plane
```

## Layout

```
root_dir/
├── copper_bmc/              # Bare-metal agent (media + Redfish command API)
└── copper_control_plane/    # Inventory UI + SQLite store
```
