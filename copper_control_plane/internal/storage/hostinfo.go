package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// BlockDisk is one lsblk disk/partition row used for direct install targets.
type BlockDisk struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Size   string `json:"size"`
	Type   string `json:"type"` // disk | part | …
	Tran   string `json:"tran,omitempty"`
	Model  string `json:"model,omitempty"`
	Serial string `json:"serial,omitempty"`
	WWN    string `json:"wwn,omitempty"` // lsblk WWN / NAA (stable across /dev letter reshuffles)
	Rota   bool   `json:"rota"`
}

// NICInfo is a data-plane network interface on the live OS.
type NICInfo struct {
	Name       string   `json:"name"`
	MAC        string   `json:"mac,omitempty"`
	Addresses  []string `json:"addresses,omitempty"`
	State      string   `json:"state,omitempty"`
	Type       string   `json:"type,omitempty"`
}

// ListBlockDisks returns non-partition disks via lsblk JSON.
func ListBlockDisks(ctx context.Context, h Host) ([]BlockDisk, error) {
	// WWN may be unsupported on older util-linux; fall back without it.
	script := `lsblk -J -b -o NAME,PATH,SIZE,TYPE,TRAN,MODEL,SERIAL,WWN,ROTA 2>/dev/null || lsblk -J -o NAME,PATH,SIZE,TYPE,TRAN,MODEL,SERIAL,WWN,ROTA 2>/dev/null || lsblk -J -b -o NAME,PATH,SIZE,TYPE,TRAN,MODEL,SERIAL,ROTA 2>/dev/null || lsblk -J -o NAME,PATH,SIZE,TYPE,TRAN,MODEL,SERIAL,ROTA`
	raw, err := RunSSH(ctx, h.IP, h.Creds, "bash -lc "+ShellQuote(script))
	if err != nil {
		return nil, err
	}
	var tree struct {
		Blockdevices []lsblkNode `json:"blockdevices"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &tree); err != nil {
		return nil, fmt.Errorf("parse lsblk: %w", err)
	}
	var out []BlockDisk
	var walk func([]lsblkNode)
	walk = func(nodes []lsblkNode) {
		for _, n := range nodes {
			if strings.EqualFold(n.Type, "disk") {
				path := n.Path
				if path == "" {
					path = "/dev/" + n.Name
				}
				size := n.SizeStr()
				out = append(out, BlockDisk{
					Name: n.Name, Path: path, Size: size, Type: "disk",
					Tran: n.Tran, Model: strings.TrimSpace(n.Model), Serial: strings.TrimSpace(n.Serial),
					WWN: strings.TrimSpace(n.WWN), Rota: n.Rota,
				})
			}
			if len(n.Children) > 0 {
				walk(n.Children)
			}
		}
	}
	walk(tree.Blockdevices)
	return out, nil
}

// ListNICs returns non-loopback ethernet-like interfaces.
func ListNICs(ctx context.Context, h Host) ([]NICInfo, error) {
	script := `
python3 - <<'PY'
import json, os, glob
out=[]
for path in sorted(glob.glob("/sys/class/net/*")):
    name=os.path.basename(path)
    if name=="lo":
        continue
    typ="ether"
    try:
        with open(os.path.join(path,"type")) as f:
            t=f.read().strip()
            if t!="1":
                typ="other"
    except Exception:
        pass
    mac=""
    try:
        with open(os.path.join(path,"address")) as f:
            mac=f.read().strip()
    except Exception:
        pass
    state=""
    try:
        with open(os.path.join(path,"operstate")) as f:
            state=f.read().strip()
    except Exception:
        pass
    addrs=[]
    try:
        import subprocess
        r=subprocess.run(["ip","-4","-o","addr","show","dev",name],capture_output=True,text=True)
        for line in r.stdout.splitlines():
            parts=line.split()
            if len(parts)>=4:
                addrs.append(parts[3])
    except Exception:
        pass
    out.append({"name":name,"mac":mac,"addresses":addrs,"state":state,"type":typ})
print(json.dumps(out))
PY`
	raw, err := RunSSH(ctx, h.IP, h.Creds, "bash -lc "+ShellQuote(script))
	if err != nil {
		// Fallback: ip -j
		raw2, err2 := RunSSH(ctx, h.IP, h.Creds, `bash -lc 'ip -j link show 2>/dev/null || true'`)
		if err2 != nil {
			return nil, err
		}
		return parseIPLinkJSON(raw2)
	}
	var nics []NICInfo
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &nics); err != nil {
		return nil, fmt.Errorf("parse nics: %w", err)
	}
	return nics, nil
}

func parseIPLinkJSON(raw string) ([]NICInfo, error) {
	var rows []struct {
		IfName string `json:"ifname"`
		OperState string `json:"operstate"`
		Address string `json:"address"`
		LinkType string `json:"link_type"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &rows); err != nil {
		return nil, err
	}
	var out []NICInfo
	for _, r := range rows {
		if r.IfName == "lo" {
			continue
		}
		out = append(out, NICInfo{
			Name: r.IfName, MAC: r.Address, State: r.OperState, Type: r.LinkType,
		})
	}
	return out, nil
}

type lsblkNode struct {
	Name     string      `json:"name"`
	Path     string      `json:"path"`
	Size     json.Number `json:"size"`
	Type     string      `json:"type"`
	Tran     string      `json:"tran"`
	Model    string      `json:"model"`
	Serial   string      `json:"serial"`
	WWN      string      `json:"wwn"`
	Rota     bool        `json:"rota"`
	Children []lsblkNode `json:"children"`
}

func (n lsblkNode) SizeStr() string {
	if n.Size == "" {
		return ""
	}
	// lsblk -b yields raw bytes. Always emit a unit so size matching against
	// controller strings ("400.000 MB", "838.33 GB") works for both MegaRAID
	// VDs and Smart Array LDs — including volumes under 1 GiB.
	if v, err := n.Size.Int64(); err == nil && v > 0 {
		return formatBytes(v)
	}
	return formatByteCount(n.Size.String())
}
