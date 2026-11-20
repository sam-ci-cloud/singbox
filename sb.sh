#!/usr/bin/env bash
# singbox public Fresh bootstrap. The embedded signature authenticates the release TAR.
set +xv
set -Eeuo pipefail
umask 077

usage() {
    cat <<'HELP'
singbox Fresh 1.11.2
  sb.sh                     Interactive menu (requires a terminal)
  sb.sh verify              Verify the signed Fresh runtime (no root needed)
  sb.sh install [options]   Guided Fresh setup or the native installer options
  sb.sh upgrade             Back up the installed Fresh system, then upgrade
  sb.sh doctor              Check the installed system and output JSON
  sb.sh recover             Recover an interrupted Fresh transaction
  sb.sh backup              Back up configuration and accounts
  sb.sh rollback [archive] [--force]
  sb.sh uninstall [--force]
  sb.sh preflight [options] Check host readiness
  sb.sh version | help
  SBOX_INSTALL_ARCHIVE=/absolute/file.tar.gz sb.sh verify    Offline verification
HELP
}
die() { printf 'singbox: %s\n' "$*" >&2; return 1; }
menu() {
    cat >/dev/tty <<'MENU'

singbox Fresh 1.11.2
  1  Install Fresh             2  Upgrade Fresh
  3  Doctor                    4  Recover interrupted change
  5  Create backup             6  Roll back from backup
  7  Uninstall                 8  Verify signed release
  9  Help and version          0  Exit
MENU
    printf 'Select [0-9]: ' >/dev/tty
    local choice
    IFS= read -r choice </dev/tty || { die 'No menu selection received.'; return 1; }
    case "$choice" in
        1) action=install ;; 2) action=upgrade ;; 3) action=doctor ;;
        4) action=recover ;; 5) action=backup ;; 6) action=rollback ;;
        7) action=uninstall ;; 8) action=verify ;; 9) action=help ;;
        0) action='exit' ;; *) die 'Invalid menu selection.'; return 2 ;;
    esac
}
fresh_state() {
    python3 -I - <<'STATE'
import json, os, re, stat
from pathlib import Path
p=Path('/etc/sbox-manager/state.json')
if p.exists() or p.is_symlink():
    s=p.lstat()
    if not stat.S_ISREG(s.st_mode) or s.st_uid or s.st_mode & 0o022:
        raise SystemExit('Unsafe installed state permissions.')
    try:
        with p.open() as src: state=json.load(src)
        valid=(re.fullmatch(r'[0-9]+\.[0-9]+\.[0-9]+',str(state.get('manager_version','')))
               and ('zgo_legacy_compat' not in state or state['zgo_legacy_compat'] is False)
               and isinstance(state['protocols'],dict))
    except (OSError, ValueError, KeyError, TypeError): valid=False
    if not valid: raise SystemExit('Protected or unrecognized installation. Use its owner installer.')
STATE
}
extract_runtime() {
    python3 -I - "$1" "$2" <<'EXTRACT'
from pathlib import Path
import os, shutil, tarfile, sys
archive, target = map(Path, sys.argv[1:])
prefix='singbox-fresh-1.11.2/'
limits={'core.gz':32<<20,'manager.gz':4<<20,'fresh.sh':128<<10,'install.sh':128<<10,
        'manifest.json':8192,'SHA256SUMS':8192,'LICENSE':128<<10,'LICENSE.sing-box':128<<10,
        'NOTICE':16384,'THIRD-PARTY-NOTICES.txt':4<<20}
seen=set();target.mkdir(mode=0o700)
with tarfile.open(archive,'r|gz') as tf:
    for member in tf:
        name=member.name.removeprefix(prefix)
        if (member.name!=prefix+name or name not in limits or name in seen or
            member.type not in (tarfile.REGTYPE,tarfile.AREGTYPE) or member.linkname or
            member.uid or member.gid or member.pax_headers or member.size<1 or
            member.size>limits[name] or member.mode!=(0o755 if name in ('fresh.sh','install.sh') else 0o644)):
            raise SystemExit('Invalid runtime archive member.')
        seen.add(name)
        with (target/name).open('xb') as dest: shutil.copyfileobj(tf.extractfile(member),dest)
        if (target/name).stat().st_size!=member.size: raise SystemExit('Incomplete runtime member.')
        os.chmod(target/name,0o700 if name.endswith('.sh') else 0o600)
if seen!=set(limits): raise SystemExit('Required runtime member missing.')
EXTRACT
}
worker() {
    set -Eeuo pipefail
    umask 077
    export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
    local op="$1" offline="$2"
    shift 2
    if [[ "$op" != verify ]]; then fresh_state; fi
    if [[ "$op" == backup || "$op" == doctor ]]; then
        [[ -f /etc/sbox-manager/state.json && -x /usr/local/bin/sbox ]] || die 'Fresh is not installed.'
        if [[ "$op" == doctor ]]; then exec /usr/local/bin/sbox doctor --json; fi
        exec /usr/local/bin/sbox backup
    fi
    local work_root=/var/tmp expected='34061a428f13d55f53d605033d6830504908aa99837df89f013baaf256cf86bb' tar_name=singbox-fresh-1.11.2.tar.gz
    [[ ! -L "$work_root" && -d "$work_root" && "$(stat -c %u "$work_root")" == 0 && -w "$work_root" ]] || die 'Untrusted or unavailable /var/tmp.'
    local mode available
    mode="$(stat -c %a "$work_root")"
    (( (8#$mode & 0022) == 0 || (8#$mode & 01000) != 0 )) || die 'Unsafe temporary-directory permissions.'
    available="$(df -Pk "$work_root" | awk 'NR==2 {print $4}')"
    (( available >= 262144 )) || die 'At least 256 MiB free staging space is required.'
    # Global by design: a pending EXIT trap may run after this function returns.
    stage="$(mktemp -d "$work_root/singbox-bootstrap.XXXXXXXX")"
    trap 'rm -rf -- "$stage"' EXIT
    trap 'exit 129' HUP
    trap 'exit 130' INT
    trap 'exit 143' TERM
    if [[ -n "$offline" ]]; then
        [[ -f "$offline" && ! -L "$offline" && "$(stat -c %s -- "$offline")" -le 45000000 ]] || die 'Invalid offline runtime.'
        cp -- "$offline" "$stage/payload"
    else
        curl -q -fsSL --proto '=https' --proto-redir '=https' --retry 3 --connect-timeout 15 \
            --max-time 600 --max-filesize 45000000 --output "$stage/payload" \
            "https://github.com/sam-ci-cloud/singbox/releases/download/v1.11.2-public.1/$tar_name"
    fi
    printf '%s  %s\n' "$expected" "$stage/payload" | sha256sum --check --status || die 'Runtime SHA-256 verification failed.'
    cat > "$stage/public.pem" <<'PUBLIC_KEY'
-----BEGIN PUBLIC KEY-----
MCowBQYDK2VwAyEANOHO5QHE5H1igVJ6XGr3O0yJwqI4dIiPDxFeNigIUZA=
-----END PUBLIC KEY-----
PUBLIC_KEY
    printf '%s' 'SnYplpJ5iJ9VkAEG2VEetfxbFbTKHrgcPShIUQi6VrFNZ8C1kvQSe8kp+KDMb/svGBsuaDCTTa6EdfWGtYaTBw==' | base64 -d > "$stage/payload.sig"
    openssl pkeyutl -verify -rawin -pubin -inkey "$stage/public.pem" -sigfile "$stage/payload.sig" \
        -in "$stage/payload" >/dev/null || die 'Runtime Ed25519 signature failed.'
    printf 'Verified %s (SHA-256 and Ed25519).\n' "$tar_name" >&2
    extract_runtime "$stage/payload" "$stage/fresh"
    case "$op" in
        install|upgrade|recover|preflight|verify) set -- "$op" generic "$@" ;;
        rollback|uninstall) set -- "$op" "$@" ;;
        *) die 'Invalid operation.' ;;
    esac
    # Keep the native terminal reader as the foreground command. For no-TTY
    # installations this parent owns cleanup through the idempotence receipt.
    if { : </dev/tty; } 2>/dev/null; then
        export SBOX_BOOTSTRAP_STAGE="$stage"
        exec env --default-signal=INT,QUIT,TSTP,TTIN,TTOU bash "$stage/fresh/install.sh" "$@" </dev/tty
    else
        SBOX_BOOTSTRAP_STAGE='' bash "$stage/fresh/install.sh" "$@" </dev/null
    fi
}
main() {
    local action="${1:-}" offline="${SBOX_INSTALL_ARCHIVE:-}" script
    if [[ -z "$action" ]]; then
        { : </dev/tty; } 2>/dev/null || { die 'No controlling terminal. Specify verify, help, or an explicit operation.'; return 2; }
        menu
    else
        shift
    fi
    case "$action" in
        exit) return 0 ;; help|-h|--help) usage; return 0 ;; version|--version) printf '1.11.2 (v1.11.2-public.1)\n'; return 0 ;;
        install|preflight|rollback|uninstall) ;;
        upgrade|recover|backup|doctor|verify) [[ $# == 0 ]] || { die 'This action accepts no arguments.'; return 2; } ;;
        *) die 'Unknown operation. Run sb.sh help.'; return 2 ;;
    esac
    if [[ -n "$offline" ]]; then
        [[ ! -L "$offline" ]] || die 'Refusing symbolic-link archive.'
        offline="$(realpath -e -- "$offline")" || { die 'Offline archive does not exist.'; return 1; }
    fi
    if [[ "$action" != verify && $EUID != 0 ]]; then
        command -v sudo >/dev/null || die 'This action requires sudo or an existing root shell.'
        printf 'Administrator access is required for %s.\n' "$action" >&2
        script="$(declare -f die fresh_state extract_runtime worker)"$'\nworker "$@"'
        if { : </dev/tty; } 2>/dev/null; then
            exec sudo -- /usr/bin/env -i HOME=/root TERM="${TERM:-dumb}" PATH=/usr/sbin:/usr/bin:/sbin:/bin \
                /bin/bash -c "$script" sb.sh "$action" "$offline" "$@" </dev/tty
        fi
        exec sudo -n -- /usr/bin/env -i HOME=/root PATH=/usr/sbin:/usr/bin:/sbin:/bin \
            /bin/bash -c "$script" sb.sh "$action" "$offline" "$@"
    fi
    worker "$action" "$offline" "$@"
}
main "$@"
