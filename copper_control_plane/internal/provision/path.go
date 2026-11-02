package provision

// remotePATH ensures /usr/sbin and /sbin are searchable.
// Non-root SSH sessions (e.g. user "provisioner") often omit sbin from PATH,
// so command -v gdisk/sgdisk/efibootmgr falsely reports MISSING even when
// /usr/sbin/gdisk exists. sudo's secure_path usually includes sbin — which is
// why interactive root checks "worked" before.
const remotePATH = `export PATH="/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin${PATH:+:$PATH}"`

// remoteFindBin is a small shell function: prints absolute path or empty.
// Prefer command -v after PATH fix; also probe common sbin locations.
const remoteFindBin = `
copper_bin() {
  _n="$1"
  if command -v "$_n" >/dev/null 2>&1; then command -v "$_n"; return 0; fi
  for _d in /usr/sbin /sbin /usr/local/sbin /usr/bin /bin; do
    if [ -x "$_d/$_n" ]; then printf '%s\n' "$_d/$_n"; return 0; fi
  done
  return 1
}
`

func remotePrelude() string {
	return remotePATH + "\n" + remoteFindBin + "\n"
}
