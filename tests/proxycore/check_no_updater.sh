#!/bin/bash
# PC-020 guard: no production path in the app source, build scripts or CI
# workflows may reference, download or launch the Throne updater / Throne
# release feed. Run from the repo root or any directory; exits non-zero if a
# forbidden pattern reappears.
set -u
cd "$(dirname "$0")/../.."

status=0

check_absent() {
    local pattern="$1" label="$2"
    if grep -rIn --exclude-dir=3rdparty -- "$pattern" src include >/dev/null 2>&1; then
        echo "FAIL: $label found in app source:"
        grep -rIn --exclude-dir=3rdparty -- "$pattern" src include
        status=1
    else
        echo "ok: $label absent from app source"
    fi
}

check_absent 'updater\.exe' "Throne updater binary launch"
check_absent 'RunUpdater' "updater exit path / ExitReason"
check_absent 'throneproj/Throne/releases' "Throne release feed request"
check_absent 'DownloadAsset([^,]*,[[:space:]]*"Throne\.zip"' "Throne release asset download"

# Build scripts and CI workflows must not mention the updater at all: they
# used to download its binary into the release package (PC-020).
for dir in script .github/workflows; do
    if grep -rIn -i 'updater' "$dir" >/dev/null 2>&1; then
        echo "FAIL: 'updater' found in $dir:"
        grep -rIn -i 'updater' "$dir"
        status=1
    else
        echo "ok: 'updater' absent from $dir"
    fi
done

# Fork isolation also applies to installers and packaging: a script that
# downloads the upstream Throne application is just as unsafe as bundling its
# updater binary. Keep this check independent from the word "updater".
for path in README.md CMakeLists.txt cmake script .github/workflows; do
    if grep -rIn -E 'throneproj/(Throne|updater)(/releases|/dev|\.git)?' "$path" >/dev/null 2>&1; then
        echo "FAIL: upstream Throne release/install source found in $path:"
        grep -rIn -E 'throneproj/(Throne|updater)(/releases|/dev|\.git)?' "$path"
        status=1
    else
        echo "ok: upstream Throne release/install source absent from $path"
    fi
done

exit "$status"
