# singbox Fresh

Install and maintain the SBOX 1.11.2 Fresh server on Linux amd64 with systemd. The installation includes VLESS REALITY+Vision, VMess WS+TLS, Hysteria2, TUIC v5, AnyTLS, a read-only Personal Cloud HTTPS fallback, the administration WebUI, subscriptions, DNS policies, user accounting and native quotas.

## Interactive installation

Run from a terminal on Ubuntu 24.04, 26.04, 26.10 or Debian 12.

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/sam-ci-cloud/singbox/main/sb.sh)
```

The menu appears before a download or privilege request. Choose **1** for a guided installation, **8** to verify the release without root, or **0** to exit. Selecting a change requests administrator access. The shorter `curl -Ls` form works; `-fsSL` additionally reports HTTP failures.

These commands trust the current copy on `main`. Process substitution passes a stream to Bash. If the outer curl fails before sending any script, Bash can exit successfully on an empty stream while curl prints the error. Use the file-download sequence below when deployment automation must check the downloader’s exit status. The bootstrap checks the runtime SHA-256 and a detached Ed25519 signature before reading the archive, then accepts ten named regular files. Installation uses a guided dialog. An existing protected installation requires its separate owner-only offline installer.

## Pin the bootstrap

The signed release notes provide an exact commit-specific command. This alternative uses the tag to discover the commit and pins the bootstrap content independently of the tag. It needs Git only to resolve the tag. Save the printed commit for subsequent deployments.

```bash
commit="$(git ls-remote https://github.com/sam-ci-cloud/singbox.git refs/tags/v1.11.2-public.1 | cut -f1)"
test "${#commit}" -eq 40 &&
curl -fsSL --proto '=https' --proto-redir '=https' "https://raw.githubusercontent.com/sam-ci-cloud/singbox/$commit/sb.sh" -o sb.sh &&
printf '%s  sb.sh\n' '5f383c947f065c82d6b1b8c36b78ef9ffb36bba89b369e1b3f4bd6e15513ecbd' | sha256sum -c - &&
bash sb.sh
```

## Commands and offline use

| Command | Operation |
|---|---|
| `bash sb.sh verify` | Authenticate the release, Fresh manager and patched core |
| `bash sb.sh preflight [options]` | Check host readiness |
| `bash sb.sh install [options]` | Install interactively or pass the manager’s unattended options |
| `bash sb.sh doctor` | Check the installed Fresh services and output JSON |
| `bash sb.sh backup` | Create a local backup |
| `bash sb.sh upgrade` | Verify the package, check host readiness and back up before upgrading |
| `bash sb.sh recover` | Recover an interrupted transaction |
| `bash sb.sh rollback [archive] [--force]` | Restore the pre-upgrade backup or a named backup |
| `bash sb.sh uninstall [--force]` | Remove the runtime; keep backups and certificate lineages |

Download `singbox-fresh-1.11.2.tar.gz` from the [versioned release](https://github.com/sam-ci-cloud/singbox/releases/tag/v1.11.2-public.1). Select the local file with `SBOX_INSTALL_ARCHIVE`; the same digest, signature and archive checks apply.

```bash
SBOX_INSTALL_ARCHIVE="$PWD/singbox-fresh-1.11.2.tar.gz" bash sb.sh verify
SBOX_INSTALL_ARCHIVE="$PWD/singbox-fresh-1.11.2.tar.gz" bash sb.sh install
```

Verification requires Bash, Python 3.9 or newer, OpenSSL 3 and standard system utilities. Online use also needs curl. For a fully disconnected install, provision the operating-system packages and use an existing matching certificate and key. Public ACME issuance needs network access. Use your DNS name and certificate policy when configuring a public service.

## Rebuild and verify

Use Ubuntu 24.04 with stock zlib and Python 3.11+ for the release build. Both Git checkouts and extracted source archives include the Fresh manager inputs, pinned compressed accounting core, complete matching core source, frontend sources, asset generation, licences and a source inventory.

```bash
python3 -B tools/build_public.py
python3 -B tools/build_source.py
(cd dist && sha256sum --strict -c ../release/RUNTIME-SHA256SUMS)
openssl pkeyutl -verify -rawin -pubin -inkey release/RELEASE-PUBLIC.pem -in dist/singbox-fresh-1.11.2.tar.gz -sigfile release/RUNTIME.sig
bash tests/bootstrap.sh "$PWD/dist/singbox-fresh-1.11.2.tar.gz"
```

To verify **all** published files, download every asset to one empty directory and run `python3 tools/verify_public.py /path/to/assets --key release/RELEASE-PUBLIC.pem`. The public-key PEM SHA-256 is `9f09e286ddd68e762b883bd4e098450208a1cc5e878c106cc9dd29f23040ffe8`. Verify that fingerprint through a channel you trust.

For a separate build of the core itself, run `bash core-source/build.sh` on Linux amd64. That build uses the pinned upstream Go toolchain and go.sum-verified public modules. The manager and runtime archive build above uses the included core bytes and does not fetch dependencies. See the full Go inventory in `core-source/SBOM.json` and component notices in `THIRD_PARTY_LICENSES/` and `shared_webui/licenses/`.
