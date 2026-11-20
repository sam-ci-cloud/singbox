#!/usr/bin/env bash
set +xv
set -Eeuo pipefail
# Noninteractive stages must share the foreground group even with inherited monitor.
set +m
IFS=$'\n\t'
umask 077
WORK=''; CHILD=''; CHILD_GROUP=false; COMPLETE=false
# The public bootstrap execs this entrypoint so sudo observes terminal stops.
# Only its private, owned, narrowly named staging directory may be removed.
cleanup_bootstrap_stage() {
  local stage="${SBOX_BOOTSTRAP_STAGE:-}"
  [ -n "$stage" ] || return 0
  case "$stage" in /*/singbox-bootstrap.????????) ;; *) return 0 ;; esac
  [ ! -L "$stage" ] && [ -d "$stage" ] || return 0
  [ "$(stat -c '%u %a' -- "$stage")" = "$(id -u) 700" ] || return 0
  rm -rf -- "$stage"
}
log() { printf '[SBOX] %s\n' "$*" >&2; }
die() { log "ERROR: $*"; exit 1; }
cleanup() {
  local rc=$?
  trap - EXIT
  if [ "$COMPLETE" != true ] && [ "$rc" -eq 0 ]; then log 'ERROR: incomplete installer; download may be truncated.'; rc=1; fi
  if [ -n "$WORK" ]; then rm -rf -- "$WORK" || rc=1; fi
  cleanup_bootstrap_stage || rc=1
  exit "$rc"
}
signal_exit() {
  local sig="$1" rc="$2"
  trap '' INT TERM HUP TSTP CONT
  if [ -n "$CHILD" ]; then
    if [ "$CHILD_GROUP" = true ];then kill -s "$sig" -- "-$CHILD" 2>/dev/null || true
    else
      # Async utilities can inherit ignored SIGINT; TERM cancels a decode stage.
      if [ "$sig" = INT ];then kill -s TERM "$CHILD" 2>/dev/null || true;else kill -s "$sig" "$CHILD" 2>/dev/null || true;fi
    fi
    # A stopped process must run to deliver a pending cancellation signal.
    if [ "$CHILD_GROUP" = true ];then kill -CONT -- "-$CHILD" 2>/dev/null || true
    else kill -CONT "$CHILD" 2>/dev/null || true;fi
    wait "$CHILD" || true
  fi
  exit "$rc"
}
wait_for_child() {
  local child_rc=0
  while :;do
    child_rc=0
    wait "$CHILD" || child_rc=$?
    # A trapped TSTP/CONT interrupts wait without completing the child.
    # Keep ownership and wait again instead of deleting its live work files.
    if [ "$child_rc" -gt 128 ] && kill -0 "$CHILD" 2>/dev/null;then continue;fi
    return "$child_rc"
  done
}
run_stage() {
  "$@" <&0 &
  CHILD=$!; CHILD_GROUP=false
  local stage_rc=0
  wait_for_child || stage_rc=$?
  CHILD=''
  return "$stage_rc"
}
suspend_stage() {
  log 'Installation suspended. Resume the SSH job with fg, or cancel after resuming.'
  if [ -n "$CHILD" ];then
    if [ "$CHILD_GROUP" = true ];then kill -STOP -- "-$CHILD" 2>/dev/null || true
    else kill -STOP "$CHILD" 2>/dev/null || true;fi
  fi
  trap - TSTP
  kill -TSTP "$$"
  trap suspend_stage TSTP
}
continue_stage() {
  if [ -n "$CHILD" ];then
    if [ "$CHILD_GROUP" = true ];then kill -CONT -- "-$CHILD" 2>/dev/null || true
    else kill -CONT "$CHILD" 2>/dev/null || true;fi
  fi
  log 'Installation resumed.'
}
trap suspend_stage TSTP
trap continue_stage CONT
trap cleanup EXIT
trap 'signal_exit INT 130' INT
trap 'signal_exit TERM 143' TERM
trap 'signal_exit HUP 129' HUP
# Parse the entire embedded package before dispatching any action.
sbox_main() {
ACTION="${1:?expected install, upgrade, recover or verify}"; VARIANT="${2:?expected generic}"
shift 2
case "$ACTION" in install|upgrade|verify|recover);; *) die 'Unknown action';; esac
[ "$VARIANT" = generic ] || die 'Expected generic Fresh profile'
EXTRA=("$@")
PACKAGE_ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
if [ "$ACTION" != install ]; then [ "${#EXTRA[@]}" -eq 0 ] || die 'Unexpected extra arguments.'; fi
for tool in bash gzip sha256sum mktemp rm chmod stat; do command -v "$tool" >/dev/null || die "Missing $tool; install normal OS prerequisites (apt-get install python3 openssl coreutils gzip)."; done
if [ "$ACTION" != verify ]; then
 [ "$(id -u)" -eq 0 ] || die 'Run from a root SSH shell: sudo -i, then run the one-line command.'
 [ "$(uname -m)" = x86_64 ] || die 'This accounting package supports amd64 (x86_64) only.'
 . /etc/os-release
 case "$VARIANT:$ID:$VERSION_ID" in generic:ubuntu:24.04|generic:ubuntu:26.04|generic:ubuntu:26.10|generic:debian:12);;*) die 'Supported: Fresh Ubuntu 24.04/26.04/26.10 or Debian 12, amd64.';;esac
 [ -d /run/systemd/system ] || die 'A running systemd VPS is required.'
 # Minimal supported images may omit Python/OpenSSL; bootstrap normal OS packages.
 # Verification remains read-only and never enters this branch.
 missing_bootstrap=false
 for dependency in python3 setsid flock; do command -v "$dependency" >/dev/null || missing_bootstrap=true; done
 if [ "$missing_bootstrap" = true ]; then
  command -v apt-get >/dev/null || die 'Missing OS prerequisites and apt-get is unavailable.'
  log 'Installing normal OS bootstrap packages for this supported image.'
  # Fresh VPS images often run unattended-upgrades at first boot: wait for the
  # dpkg lock instead of failing, and retry transient repository/network errors.
  bootstrap_apt() { DEBIAN_FRONTEND=noninteractive NEEDRESTART_MODE=l apt-get -o DPkg::Lock::Timeout=600 -o Acquire::Retries=3 -o Acquire::http::Timeout=30 -o Acquire::https::Timeout=30 "$@" </dev/null; }
  attempt=1
  until bootstrap_apt update; do
   [ "$attempt" -lt 3 ] || die 'Bootstrap apt-get update failed 3 times. Check DNS, any proxy and /etc/apt/sources.list*; no SBOX state was changed.'
   log "apt-get update failed; retry $attempt/2 in 10 seconds."; attempt=$((attempt+1)); sleep 10
  done
  attempt=1
  until bootstrap_apt install -y --no-install-recommends python3 openssl util-linux ca-certificates; do
   [ "$attempt" -lt 3 ] || die 'Bootstrap package installation failed 3 times (python3 openssl util-linux ca-certificates). Check the apt output above; no SBOX state was changed.'
   log "apt-get install failed; retry $attempt/2 in 10 seconds."; attempt=$((attempt+1)); sleep 10
  done
 fi
 command -v setsid >/dev/null || die 'Missing setsid (apt-get install util-linux).'
 # Serialize the entire operation, including bootstrap, without touching existing data.
 command -v flock >/dev/null || die 'Missing flock (apt-get install util-linux).'
 exec 8>/run/sbox-all-in-one.lock
 flock -n 8 || die 'Another all-in-one installer is running.'
 if [ -e /etc/sbox-manager/state.json ]; then
  { [ "$ACTION" = upgrade ] || [ "$ACTION" = recover ]; } || die 'Existing SBOX detected. Select the matching upgrade option.'
  installed_variant="$(python3 -c 'import json; s=json.load(open("/etc/sbox-manager/state.json")); print("zgocloud" if s.get("zgo_legacy_compat") is True else "generic")')" || die 'Cannot read existing state; recover it before upgrading.'
  [ "$installed_variant" = "$VARIANT" ] || die "Existing product is $installed_variant; refusing a product switch."
 else
  [ "$ACTION" = install ] || die 'No existing state. Use install on a fresh VPS.'
  if [ -e /usr/local/bin/sbox ] || [ -e /usr/local/lib/sbox-manager/manager ]; then die 'Incomplete existing SBOX detected; inspect/recover it before installing.';fi
 fi
fi
# Fixed trusted /tmp with sticky boundary; never accept attacker-controlled TMPDIR.
[ ! -L /tmp ] && [ "$(stat -c %u /tmp)" = 0 ] || die 'Unsafe /tmp owner or symlink.'
mode="$(stat -c %a /tmp)"; (( (8#$mode & 0022) == 0 || (8#$mode & 01000) != 0 )) || die 'Unsafe writable /tmp without sticky bit.'
WORK="$(mktemp -d /tmp/sbox-all-in-one.XXXXXXXXXX)";chmod 700 "$WORK"
log "Stage: verifying embedded $VARIANT manager..."
run_stage cp -- "$PACKAGE_ROOT/manager.gz" "$WORK/manager.gz"
EXPECTED='58309aaecc8972122da818190732c715d05f10f7f4f1f79f248593ad935d9358'
run_stage gzip -dc "$WORK/manager.gz" > "$WORK/manager" || die 'Manager archive damaged.'
printf '%s  %s\n' "$EXPECTED" "$WORK/manager" | sha256sum -c - >/dev/null || die 'Manager checksum failed.'
log 'Stage: checking embedded patched core integrity...'
run_stage cp -- "$PACKAGE_ROOT/core.gz" "$WORK/core.gz"
printf '%s  %s\n' '6b85ece446c20d844b6258bbde3cde0ca047c4176fd14f586793059d193251ce' "$WORK/core.gz" | sha256sum -c - >/dev/null || die 'Core archive checksum failed.'
run_stage gzip -dc "$WORK/core.gz" > "$WORK/sing-box-accounting" || die 'Core archive damaged.'
printf '%s  %s\n' '25a6e34ac9783adaa1d4576c3f76360291c274506fbb53b1fb8fb43452bb925d' "$WORK/sing-box-accounting" | sha256sum -c - >/dev/null || die 'Core checksum failed.'
bash -n "$WORK/manager" || die 'Manager syntax failed.'
chmod 700 "$WORK/manager" "$WORK/sing-box-accounting"
version="$(bash "$WORK/manager" version)" || die 'Manager version check failed.'
case "$VARIANT:$version" in generic:1.11.2);;*)die 'Manager version mismatch.';;esac
log "Verified $VARIANT $version; full application and shared core integrity passed."
if [ "$ACTION" = verify ];then
 log "Environment: $(uname -srm); uid=$(id -u). Verification changed no services."
 COMPLETE=true;exit 0
fi
export SBOX_ACCOUNTING_CORE_SOURCE="$WORK/sing-box-accounting" SBOX_MANAGER_SOURCE="$WORK/manager"
# Never allow the all-in-one fresh path to select an incompatible stock core.
if [ "$ACTION" = upgrade ];then args=(upgrade-features --confirm-core-restart)
elif [ "$ACTION" = recover ];then args=(recover)
else
 for arg in "${EXTRA[@]}"; do
   case "$arg" in --core|--core=*|--binary|--binary=*) die 'Fresh installs its verified accounting core.';; esac
 done
 args=(install "${EXTRA[@]}" --core "$WORK/sing-box-accounting")
fi
# A separate group cancels unattended manager trees. Interactive sudo use_pty
# must see its foreground reader: detaching it after piped input leaves prompts
# visible while sudo does not forward terminal answers to the detached reader.
log 'Stage: checking OS prerequisites, installing required packages and guided configuration.'
rc=0
if { : </dev/tty; } 2>/dev/null;then
 log 'Interactive terminal: Ctrl+C cancels; Ctrl+Z suspends the SSH job, then fg resumes it.'
 # Foreground children receive terminal signals with this wrapper. A caught
 # TSTP deferred by synchronous wait would leave a stopped child and a waiting
 # parent, so retain normal whole-job suspension in this branch.
 trap - TSTP CONT
 env --default-signal=INT,QUIT,TSTP,TTIN,TTOU bash "$WORK/manager" "${args[@]}" </dev/tty || rc=$?
else
 env --default-signal=INT,QUIT setsid bash "$WORK/manager" "${args[@]}" <&0 &
 CHILD=$!;CHILD_GROUP=true;wait_for_child || rc=$?;CHILD=''
fi
[ "$rc" -eq 0 ] || exit "$rc"
COMPLETE=true
}
sbox_main "$@"
