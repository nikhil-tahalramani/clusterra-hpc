#!/bin/bash
# Clusterra Node-RED entrypoint. The host-side sbatch script writes the
# per-session credentials file directly into the userDir (on EFS) once the
# HTTP listener is up — flows read $NODE_RED_USER_DIR/clusterra-creds.json
# at message time.
#
# On first launch we seed three things into the userDir:
#   1. settings.js symlink (existing behaviour)
#   2. lib/flows/ — bundled importable subflows (Examples → Clusterra)
#   3. flows.json — starter tab that instantiates clusterra-init so its
#      on-startup inject fires once and renders the per-template /
#      per-endpoint palette into lib/flows/.
#
# Subsequent launches preserve the user's flows.json. lib/flows/ is
# refreshed on every launch so newly-published image-level subflows reach
# existing userDirs without manual intervention.
set -euo pipefail

USER_DIR="${NODE_RED_USER_DIR:-/data}"
mkdir -p "$USER_DIR"

if [ ! -e "$USER_DIR/settings.js" ]; then
  ln -sf /data/settings.js "$USER_DIR/settings.js"
fi

# Refresh the importable subflow library every launch — the three primitives
# are part of the image, not user state, and shipping new versions to all
# sessions matters.
mkdir -p "$USER_DIR/lib/flows"
cp -f /data/examples/subflows/*.json "$USER_DIR/lib/flows/"

# Reconcile flows.json on every launch — preserves user-authored tabs/nodes,
# but always (re-)installs the three Clusterra primitive subflow definitions
# plus a bootstrap tab whose clusterra-init instance fires `once: true` on
# Node-RED start and renders the rest of the palette into lib/flows/.
#
# Safety: we strip only nodes we own (managed ids + anything whose `z`
# points at a managed subflow) and rewrite them from the bundled image
# files. Anything else the user created is left untouched.
python3 - "$USER_DIR/flows.json" <<'PY'
import json, os, sys
out = sys.argv[1]
existing = []
if os.path.exists(out):
    try:
        with open(out) as f:
            existing = json.load(f)
        if not isinstance(existing, list):
            existing = []
    except Exception:
        existing = []

managed_subflows = ("clusterra-api", "clusterra-watch-job", "clusterra-init")
managed_extra = {"clusterra-bootstrap-tab", "boot-init-instance", "boot-debug"}

def is_managed(node):
    nid = node.get("id", "")
    if nid in managed_subflows or nid in managed_extra:
        return True
    if node.get("z") in managed_subflows:
        return True
    return False

preserved = [n for n in existing if not is_managed(n)]

managed = []
for sub in managed_subflows:
    with open("/data/examples/subflows/" + sub + ".json") as f:
        managed.extend(json.load(f))

managed.append({
    "id": "clusterra-bootstrap-tab",
    "type": "tab",
    "label": "Clusterra bootstrap",
    "disabled": False,
    "info": "Auto-managed by the entrypoint. The clusterra-init node renders the per-template / per-endpoint palette into lib/flows/ on Node-RED start — drag those into your own tabs from the Examples menu.\n\nThis tab is recreated on every launch; edits to it will be overwritten. Re-trigger init via the inject button to refresh the palette mid-session."
})
managed.append({
    "id": "boot-init-instance",
    "type": "subflow:clusterra-init",
    "z": "clusterra-bootstrap-tab",
    "name": "render palette",
    "x": 220, "y": 80, "wires": [["boot-debug"]]
})
managed.append({
    "id": "boot-debug",
    "type": "debug",
    "z": "clusterra-bootstrap-tab",
    "name": "init result",
    "active": True, "complete": "payload",
    "x": 420, "y": 80, "wires": []
})

with open(out, "w") as f:
    json.dump(preserved + managed, f, indent=2)
PY

exec node-red --userDir "$USER_DIR" --port "${PORT:-1880}" --settings "$USER_DIR/settings.js"
