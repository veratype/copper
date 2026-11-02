# Install (open source)

```bash
make cp-linux
```

```bash
./copper_control_plane/bin/copper_control_plane -config copper_control_plane/config.yaml
```

Hardware RAID (storcli / ssacli) is not included in this build. SoftRAID and the rest of the control plane are available.

```bash
./copper_control_plane/bin/copper_control_plane fingerprint
```
