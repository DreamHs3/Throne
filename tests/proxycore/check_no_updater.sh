#!/bin/bash
# PC-020 guard: no production path in the app source may reference, download
# or launch the Throne updater / Throne release feed. Run from the repo root
# or any directory; exits non-zero if a forbidden pattern reappears.
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

exit "$status"
