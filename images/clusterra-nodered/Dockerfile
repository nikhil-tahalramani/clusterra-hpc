# Node-RED image for kind:interactive sessions. Mirrors clusterra-notebook:
# slurmd owns the lifecycle via `apptainer exec docker://<this>`; we don't run
# Node-RED as a long-lived service. Cookie auth lives at the reverse-proxy
# (cluster-api) — Node-RED's adminAuth is left off so there's only one auth
# system on the pipe.

# Full base (NOT `-minimal`) so the stock storage / exec / tcp / udp nodes
# are present — automation flows want `file in`, `exec`, etc. out of the
# box. Latest stable as of 2026-05-01. Bump explicitly; CI digest-pins on
# first build.
FROM nodered/node-red:4.1.8

USER root

# `jq` for the entrypoint to read /run/clusterra/credentials.json without a
# Node dep. `curl` for healthcheck/manual debugging.
RUN apk add --no-cache jq curl bash python3

# Bundled example flow — importable from Examples → Clusterra → "Hello job".
COPY examples/ /data/examples/

# settings.js drives httpAdminRoot from $BASE_URL and points userDir at EFS.
COPY settings.js /data/settings.js

# Entrypoint reads /run/clusterra/credentials.json (mounted tmpfs from the
# sbatch script) and seeds two global config nodes — `clusterra-api` (URL)
# and `clusterra-auth` (bearer token) — so customer flows reference them by
# name without copy-pasting tokens.
COPY entrypoint.sh /usr/local/bin/clusterra-nodered-entrypoint
RUN chmod +x /usr/local/bin/clusterra-nodered-entrypoint

USER node-red
