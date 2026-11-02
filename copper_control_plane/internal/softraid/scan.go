package softraid

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"copper_control_plane/internal/storage"
)

// Scan SSHs into the live OS and returns mdadm arrays + eligible disks.
func Scan(ctx context.Context, h storage.Host) Topology {
	now := time.Now().UTC()
	out := Topology{DataIP: h.IP, ScannedAt: now, Arrays: []Array{}, Disks: []Disk{}}

	raw, err := storage.RunSSH(ctx, h.IP, h.Creds, storage.SudoBashLC(scanScript))
	if err != nil {
		out.Error = err.Error()
		out.Detail = trimTail(raw, 40)
		return out
	}
	raw = strings.TrimSpace(raw)
	// Script prints a single JSON object; ignore leading noise.
	if i := strings.Index(raw, "{"); i >= 0 {
		raw = raw[i:]
	}
	if j := strings.LastIndex(raw, "}"); j >= 0 {
		raw = raw[:j+1]
	}
	var parsed Topology
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		out.Error = fmt.Sprintf("parse softraid scan: %v", err)
		out.Detail = trimTail(raw, 60)
		return out
	}
	parsed.DataIP = h.IP
	parsed.ScannedAt = now
	if parsed.Arrays == nil {
		parsed.Arrays = []Array{}
	}
	if parsed.Disks == nil {
		parsed.Disks = []Disk{}
	}
	if strings.TrimSpace(parsed.Tool) == "" {
		parsed.OK = false
		if parsed.Error == "" {
			parsed.Error = "mdadm not found on live OS"
		}
		return parsed
	}
	parsed.OK = parsed.Error == ""
	parsed.BatchFingerprint = BatchFingerprint(parsed)
	return parsed
}

// ResolveArrayPath finds the live /dev/md* path for an install target (by UUID then name).
func ResolveArrayPath(ctx context.Context, h storage.Host, t storage.InstallTarget) string {
	top := Scan(ctx, h)
	id := strings.TrimSpace(t.DeviceID)
	key := strings.TrimSpace(t.Key) // md:md0 or md:uuid
	name := ""
	if strings.HasPrefix(key, "md:") {
		name = strings.TrimPrefix(key, "md:")
	}
	dev := strings.TrimSpace(t.Device)
	for _, a := range top.Arrays {
		if id != "" && strings.EqualFold(a.UUID, id) {
			return a.Path
		}
	}
	for _, a := range top.Arrays {
		if name != "" && (a.Name == name || a.UUID == name) {
			return a.Path
		}
		if dev != "" && (a.Path == dev || "/dev/"+a.Name == dev) {
			return a.Path
		}
	}
	if dev != "" && strings.HasPrefix(dev, "/dev/md") {
		return dev
	}
	return ""
}

const scanScript = `
set -e
MDADM=$(command -v mdadm 2>/dev/null || true)
if [ -z "$MDADM" ]; then
  for p in /sbin/mdadm /usr/sbin/mdadm; do [ -x "$p" ] && MDADM=$p && break; done
fi
python3 - "$MDADM" <<'PY'
import json, os, re, subprocess, sys

mdadm = sys.argv[1] if len(sys.argv) > 1 else ""
out = {"ok": False, "tool": mdadm, "arrays": [], "disks": [], "error": ""}

def run(cmd):
    try:
        r = subprocess.run(cmd, capture_output=True, text=True, timeout=60)
        return r.returncode, (r.stdout or "") + (r.stderr or "")
    except Exception as e:
        return 1, str(e)

def human_size(n):
    try:
        n = int(n)
    except Exception:
        return str(n)
    for unit, div in (("T", 1<<40), ("G", 1<<30), ("M", 1<<20), ("K", 1<<10)):
        if n >= div:
            return f"{n/div:.1f}{unit}"
    return str(n)

if not mdadm:
    out["error"] = "mdadm not found"
    print(json.dumps(out))
    raise SystemExit(0)

# --- arrays via mdadm --detail --scan + per-array --detail --export ---
arrays = []
member_paths = set()
code, scan_txt = run([mdadm, "--detail", "--scan"])
# Also list /dev/md* and /dev/md/*
import glob
devs = sorted(set(glob.glob("/dev/md?*") + glob.glob("/dev/md/*")))
# Prefer numeric mdN and md/name symlinks resolved
seen = set()
candidates = []
for d in sorted(glob.glob("/dev/md[0-9]*")):
    if re.match(r"^/dev/md\d+$", d):
        candidates.append(d)
for d in sorted(glob.glob("/dev/md/*")):
    if os.path.exists(d):
        candidates.append(d)
# From scan lines: ARRAY /dev/md/foo metadata=1.2 UUID=...
for line in scan_txt.splitlines():
    m = re.match(r"^ARRAY\s+(\S+)", line.strip())
    if m:
        candidates.append(m.group(1))

for path in candidates:
    real = os.path.realpath(path)
    if real in seen:
        continue
    # skip partition nodes md0p1
    base = os.path.basename(real)
    if re.search(r"p\d+$", base) and re.match(r"md\d+p\d+$", base):
        continue
    seen.add(real)
    code, det = run([mdadm, "--detail", "--export", path])
    if code != 0:
        code2, det2 = run([mdadm, "--detail", "--export", real])
        if code2 != 0:
            continue
        det = det2
        path = real
    kv = {}
    for line in det.splitlines():
        line = line.strip()
        if "=" in line:
            k, v = line.split("=", 1)
            kv[k] = v
    name = kv.get("MD_DEVICE", os.path.basename(path))
    if name.startswith("/dev/"):
        name = os.path.basename(name)
    level = kv.get("MD_LEVEL", "")
    state = kv.get("MD_METADATA") and kv.get("MD_DEVICES")  # placeholder
    # Prefer human detail for state/size/members
    code_h, human = run([mdadm, "--detail", path])
    state_s = ""
    size_s = ""
    raid_devices = 0
    total_devices = 0
    spare_devices = 0
    metadata = kv.get("MD_METADATA", "")
    uuid = kv.get("MD_UUID", "")
    members = []
    for line in human.splitlines():
        ls = line.strip()
        if ls.lower().startswith("state :"):
            state_s = ls.split(":", 1)[1].strip()
        elif ls.lower().startswith("array size :"):
            size_s = ls.split(":", 1)[1].strip().split("(")[0].strip()
        elif ls.lower().startswith("raid devices :"):
            try: raid_devices = int(ls.split(":",1)[1].strip())
            except Exception: pass
        elif ls.lower().startswith("total devices :"):
            try: total_devices = int(ls.split(":",1)[1].strip())
            except Exception: pass
        elif ls.lower().startswith("spare devices :"):
            try: spare_devices = int(ls.split(":",1)[1].strip())
            except Exception: pass
        elif ls.lower().startswith("version :") and not metadata:
            metadata = ls.split(":",1)[1].strip()
        elif ls.lower().startswith("uuid :") and not uuid:
            uuid = ls.split(":",1)[1].strip()
        # member lines: "    0       8       16        0      active sync   /dev/sdb"
        m = re.match(r"^(\d+)\s+\d+\s+\d+\s+\d+\s+(.+?)\s+(/\S+)\s*$", ls)
        if m:
            role = m.group(2).strip()
            dev = m.group(3).strip()
            slot = int(m.group(1))
            role_l = role.lower()
            kind = "active"
            if "faulty" in role_l:
                kind = "faulty"
            elif "spare" in role_l:
                kind = "spare"
            elif "journal" in role_l:
                kind = "journal"
            members.append({"device": dev, "role": kind, "slot": slot})
            member_paths.add(os.path.realpath(dev) if os.path.exists(dev) else dev)
            # also basename aliases
            member_paths.add(dev)

    if not level:
        level = kv.get("MD_LEVEL", "unknown")
    arr_path = path if path.startswith("/dev/") else "/dev/" + name
    if not arr_path.startswith("/dev/"):
        arr_path = "/dev/" + arr_path
    # normalize name
    if re.match(r"^md\d+$", os.path.basename(os.path.realpath(arr_path))):
        name = os.path.basename(os.path.realpath(arr_path))
        arr_path = "/dev/" + name
    arrays.append({
        "name": name,
        "path": arr_path,
        "uuid": uuid,
        "level": level,
        "state": state_s or "unknown",
        "size": size_s,
        "raid_devices": raid_devices,
        "total_devices": total_devices,
        "spare_devices": spare_devices,
        "metadata": metadata,
        "members": members,
    })

out["arrays"] = arrays

# --- physical disks via lsblk ---
code, lsblk_raw = run(["lsblk", "-J", "-b", "-o", "NAME,PATH,SIZE,TYPE,TRAN,MODEL,SERIAL,WWN"])
disks = []
try:
    tree = json.loads(lsblk_raw)
except Exception:
    tree = {"blockdevices": []}

def walk(nodes):
    for n in nodes or []:
        typ = (n.get("type") or "").lower()
        if typ == "disk":
            path = n.get("path") or ("/dev/" + n.get("name", ""))
            real = os.path.realpath(path) if os.path.exists(path) else path
            state = "free"
            arr = ""
            # check if in any array members
            for a in arrays:
                for m in a.get("members") or []:
                    md = m.get("device") or ""
                    if md == path or (os.path.exists(md) and os.path.realpath(md) == real):
                        state = m.get("role") or "member"
                        if state == "active":
                            state = "member"
                        arr = a.get("name") or ""
                        break
                if arr:
                    break
            # foreign md superblock on free disk?
            if state == "free" and mdadm:
                c2, ex = run([mdadm, "--examine", "--export", path])
                if c2 == 0 and "MD_UUID=" in ex:
                    state = "foreign"
            size = human_size(n.get("size") or 0)
            disks.append({
                "name": n.get("name") or "",
                "path": path,
                "size": size,
                "tran": n.get("tran") or "",
                "model": (n.get("model") or "").strip(),
                "serial": (n.get("serial") or "").strip(),
                "wwn": (n.get("wwn") or "").strip(),
                "state": state,
                "array": arr,
            })
        walk(n.get("children"))

walk(tree.get("blockdevices"))
out["disks"] = disks
out["ok"] = True
print(json.dumps(out))
PY
`

func trimTail(s string, n int) string {
	s = strings.TrimSpace(s)
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}
