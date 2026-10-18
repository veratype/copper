# Copper

Copper is a small toolkit for managing physical (bare-metal) servers. It keeps track of which servers you have and lets you control them remotely through their BMC, the little management computer built into most servers. It talks to BMCs using Redfish, the standard API they speak, and it does all of this without needing OpenStack Ironic.

## Why we built it

Installing operating systems on physical servers kept going wrong because of network misconfiguration. A common culprit was PortFast being left switched off at the switch. On top of that, we never had a clear, reliable view of which servers we actually had. We looked at Ironic, the usual tool for this job, but its way of doing things didn't suit our setup. So we wrote our own.

Copper has two parts.

**copper_bmc** is an agent you run close to the hardware. It works on two networks at once. On the out-of-band (OOB) network, where the BMCs live, it hosts the install images (ISOs) that servers boot from. On the control-plane network, it offers an API that accepts commands. If you switch the option on, it can also hand out IP addresses to BMCs over DHCP.

**copper_control_plane** is the part you actually interact with. It's a web interface backed by a small SQLite database that stores your server list and BMC login details. When you want to do something to a server, such as look up its details, attach an install image as a virtual CD, change the boot order, or restart it, the control plane asks copper_bmc to do it. The control plane also decides how DHCP should behave on the OOB network and sends those settings to the agent. Separately, it runs its own DHCP server for the servers' regular data network cards, including the settings servers need to boot over the network (PXE).

## What you need

You need Go 1.26 or newer.

The machines also need to be able to reach each other.

- BMCs need to reach copper_bmc's media server so they can download ISOs and images.
- The control plane needs to reach copper_bmc's command API.
- Your browser needs to reach the control plane's web interface.
- If you turn on Data DHCP, the servers' data network cards need to reach the control plane's DHCP server on port 67.

## Getting it running

Start copper_bmc first. The control plane needs to know where the agent is, so the agent should already be up when the control plane starts.

### Step 1. Start copper_bmc

```bash
cd copper_bmc
go run ./cmd/copper_bmc
```

It asks you a few questions as it starts.

| Question | Default | What it's for |
|----------|---------|---------------|
| Media listen port | `8080` | Port for the file server that hands ISOs to BMCs on the OOB network |
| Folder to serve | None, you have to provide one | Folder containing the images BMCs can download |
| Media bind NIC | `0.0.0.0` | Network card or IP address facing the BMC network |
| Command API listen port | `9090` | Port the control plane uses to send commands |
| Command bind NIC | `0.0.0.0` | Network card or IP address facing the control-plane network |
| Enable DHCP server role? | `N` | If you say yes, the agent will run DHCP, but only with settings sent by the control plane |

The agent never stores Redfish usernames or passwords. Every time the control plane sends a request, it includes the BMC's address and login details along with it.

Once the agent is running, you should see both networks listed, something like this.

- Media server at `http://<oob-ip>:8080/`
- Command API at `http://<cp-ip>:9090/`, which offers `GET /v1/health`, `GET /v1/activity`, and routes for system info, boot settings, virtual media and resets. If you enabled the DHCP role, DHCP routes are there too.

Everything the agent does is printed in its console. That includes DHCP offers and acknowledgements, LLDP neighbors it spots, image downloads, and Redfish operations. The same log also appears on the control plane's **Activity** page, and DHCP events on their own stream live on the **OOB DHCP** page. When the DHCP role is on, the agent keeps `data/oob_dhcp_config.json` and `data/oob_dhcp_leases.json` so a BMC restart restores the last policy and leases by itself. Separately, the control plane watches agent health and auto-repushes the saved OOB DHCP policy if the agent comes back with DHCP not running — no manual push required.

**Finding out where servers are plugged in (LLDP).** Network switches regularly announce themselves using a protocol called LLDP. copper_bmc quietly listens for these announcements on the OOB media network card and makes them available at `GET /v1/net/lldp`. Listening this way requires the `CAP_NET_RAW` permission. In the control plane, the **Sync OOB DHCP + LLDP** button and the per-server **Lookup chassis (LLDP)** button use this data to fill in each server's `chassis_id` and any missing MAC addresses. They match a server to an announcement by its source MAC, chassis ID, or management IP.

### Step 2. Start copper_control_plane

Open `copper_control_plane/config.yaml` and set at least `master_password`.

```yaml
master_password: "changeme"
listen: "0.0.0.0:9446"
database: "data/control_plane.db"
# Optional. If you leave these out, the program asks for them at startup.
# copper_bmc_host: "192.168.10.20"
# copper_bmc_port: 9090
```

Then run it.

```bash
cd copper_control_plane
go run ./cmd/copper_control_plane -config config.yaml
```

Open `http://<host>:9446/` in your browser and log in with your `master_password`.

The interface has two DHCP pages, one for each network.

| Page | Network | What it does |
|------|---------|--------------|
| **OOB DHCP** | BMC / management network | Sets the address pool and MAC address filters. **Save & push** sends the settings to copper_bmc (DHCP role must be enabled). Shows live MAC→IP leases from the agent. **Sync from agent** pulls leases + LLDP into inventory. Addressing only — no PXE settings here. |
| **Data DHCP** | Servers' data network cards | Runs a DHCPv4 server inside the control plane itself, using `copper_dhcp`. Includes the **next-server** and **boot file** settings for network booting (PXE). Using port 67 needs root or the `CAP_NET_BIND_SERVICE` permission. |

### Next-server and boot file, explained

When a server boots over the network, it needs to know two things. Where should it fetch its boot file from, and which file should it fetch? These two settings answer those questions together.

**Next-server** is the "where". It's the IP address of the machine holding the boot files, usually a TFTP server. It's sent in the DHCP `siaddr` field and in option 66. This is often a different machine from the DHCP server. If you leave it blank, many servers simply fall back to the DHCP server's IP.

**Boot file** is the "what". It's the name, path or URL of the file to download, such as `pxelinux.0`, `ipxe.efi` or `http://…/boot.ipxe`. It's sent in option 67, and also in the older `file` header field when the name is short enough to fit.

For example, if next-server is `10.0.0.5` and boot file is `ipxe.efi`, the server downloads `ipxe.efi` from `10.0.0.5` over TFTP.

## Who handles DHCP

| Part | Responsibility |
|------|----------------|
| copper_bmc startup | The admin decides whether this agent is allowed to run OOB DHCP at all |
| **OOB DHCP** page | Decides how BMCs get their addresses. Pushing applies those settings on the agent |
| **Data DHCP** page | Decides addressing and PXE settings for the data network cards. Applying starts DHCP inside the control plane |
| `copper_dhcp` | Shared DHCPv4 library, built on `insomniacslk/dhcp`, used by both of the above |

## Building standalone binaries (optional)

If you'd rather have compiled programs than use `go run`, build them like this.

```bash
cd copper_bmc && go build -o copper_bmc ./cmd/copper_bmc
cd ../copper_control_plane && go build -o copper_control_plane ./cmd/copper_control_plane
```

## Project layout

```
root_dir/
├── copper_dhcp/             # Shared DHCPv4 library
├── copper_bmc/              # Agent near the hardware (image hosting, Redfish, optional OOB DHCP)
└── copper_control_plane/    # Web UI, SQLite inventory, OOB DHCP settings, data-network DHCP
```
