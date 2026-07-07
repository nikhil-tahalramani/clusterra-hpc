#!/bin/bash
# Slurm Prolog (root, per-job-per-node) — invoked by slurmd before the job
# step starts. Creates the per-job scratch directory on whichever filesystem
# the node has available. TaskProlog reads back the path and exports TMPDIR
# and per-tool cache directories (NXF_HOME, HF_HOME, TORCH_HOME, etc.).
set -euo pipefail
if [ -d /mnt/scratch ] && [ -w /mnt/scratch ]; then
  ROOT="/mnt/scratch/job-${SLURM_JOB_ID}"
else
  ROOT="/mnt/efs/tmp/job-${SLURM_JOB_ID}"
fi

# Use numeric UID (SLURM_JOB_UID) rather than username (SLURM_JOB_USER).
# install -o calls getpwnam() to resolve a username string; nss_slurm only
# resolves users for jobs already active on the node, and the prolog runs
# before the job is registered — so getpwnam() always fails with "invalid
# user". Numeric UID bypasses the name lookup entirely.
OWNER="${SLURM_JOB_UID:-0}"

# Create root job directory and all cache subdirectories.
install -d -m 0700 -o "$OWNER" "$ROOT"

# apptainer temp/cache
install -d -m 0700 -o "$OWNER" "$ROOT/apptainer-tmp"
install -d -m 0700 -o "$OWNER" "$ROOT/apptainer-cache"

# Nextflow
install -d -m 0700 -o "$OWNER" "$ROOT/nxf-home"
install -d -m 0700 -o "$OWNER" "$ROOT/nxf-work"
install -d -m 0700 -o "$OWNER" "$ROOT/nxf-temp"

# ML/DL frameworks
install -d -m 0700 -o "$OWNER" "$ROOT/hf-home/transformers"
install -d -m 0700 -o "$OWNER" "$ROOT/torch-home"
install -d -m 0700 -o "$OWNER" "$ROOT/triton-cache"

# Python packaging
install -d -m 0700 -o "$OWNER" "$ROOT/pip-cache"
install -d -m 0700 -o "$OWNER" "$ROOT/uv-cache"

# Conda/mamba
install -d -m 0700 -o "$OWNER" "$ROOT/conda-pkgs"

# XDG
install -d -m 0700 -o "$OWNER" "$ROOT/xdg-cache"

# Private-repo Nextflow git credential staging.
#
# The cluster-api writes a per-job credential bundle at
#   /mnt/efs/clusterra-system/git-creds/<slot>.cred
# (mode 0600, owned by the cluster-api process UID) BEFORE submitting the
# job, where <slot> is an opaque UUID injected into the job environment as
# CLUSTERRA_GIT_CREDS_SLOT. The file contains exactly one line:
#   https://x-access-token:<token>@github.com
#
# The Prolog stages this into the per-job scratch dir as `.git-credentials`,
# chmods 0600 to the job's OWNER, writes a sibling .gitconfig pointing
# credential.helper at it, then DELETES the source file. Net effect: the
# token only lives on disk inside the per-job scratch root (cleaned by
# Epilog), never on Slurm's command line, never in stdout/stderr/sacct. The
# job environment carries only the slot id, which is an opaque identifier.
#
# The slot id reaches the Prolog via the job's submitted environment array,
# which Slurm exposes to the Prolog as SLURM_JOB_COMMENT / env vars on the
# step. Slurm's Prolog runs before the user environment is applied to the
# step, so we read the slot from /proc/<step>/environ via SPANK is not
# possible — instead, the slot is also written into the job's comment field
# (which slurmctld exposes as SLURM_JOB_COMMENT in the Prolog env).
#
# Implementation: the cluster-api stamps `creds_slot=<uuid>` into the slurm
# `--comment` field (the same structured `clusterra:` block we already use
# for run_id/user_email). The Prolog runs as root and can use `scontrol
# show job -o ${SLURM_JOB_ID}` to read the Comment field, then extract
# creds_slot from it.
CREDS_SLOT=""
if command -v scontrol >/dev/null 2>&1; then
  JOB_INFO="$(scontrol show job -o "${SLURM_JOB_ID}" 2>/dev/null || true)"
  # Comment=clusterra:run_id=...;creds_slot=<uuid>;...
  # `grep -oE` exits 1 when no match (e.g. public-repo jobs whose comment
  # carries no creds_slot). Under `set -euo pipefail` that aborts the whole
  # prolog with empty output. Suffix `|| true` on each match so a missing
  # creds_slot is a no-op, not a fatal prolog failure.
  COMMENT_BLOCK="$(printf '%s\n' "$JOB_INFO" | { grep -oE 'Comment=[^[:space:]]+' || true; } | head -n1 | cut -d= -f2-)"
  # Slot id is a UUID (hex + dashes only); anchor the regex so we never
  # match arbitrary user-supplied substrings in comment.
  CREDS_SLOT="$(printf '%s\n' "$COMMENT_BLOCK" | { grep -oE 'creds_slot=[0-9a-fA-F-]+' || true; } | head -n1 | cut -d= -f2-)"
fi
CRED_SRC=""
if [ -n "$CREDS_SLOT" ]; then
  CRED_SRC="/mnt/efs/clusterra-system/git-creds/${CREDS_SLOT}.cred"
fi
if [ -n "$CRED_SRC" ] && [ -r "$CRED_SRC" ]; then
  GIT_CRED_FILE="$ROOT/.git-credentials"
  GIT_CONFIG_FILE="$ROOT/.gitconfig"
  # Copy with 0600 to the job's UID; this lets `nextflow run` (running as
  # the job UID) read it but nothing else on the host can.
  install -m 0600 -o "$OWNER" "$CRED_SRC" "$GIT_CRED_FILE"
  cat > "$GIT_CONFIG_FILE" <<EOF
[credential]
  helper = store --file=$GIT_CRED_FILE
EOF
  chown "$OWNER" "$GIT_CONFIG_FILE"
  chmod 0600 "$GIT_CONFIG_FILE"
  # Best-effort source removal: the cluster-api should also clean up but
  # owning the lifecycle here defends against orphaned creds.
  rm -f "$CRED_SRC" 2>/dev/null || true
fi

# nss_slurm returns home=/nonexistent for the launch-credential user (Slinky
# default — see slurmd Dockerfile note above). Tools that cache to
# $HOME/.<tool>/ on first run (Triton autotune, HuggingFace, OpenFold3 weight
# DL prompt) silently fail or hang. Apptainer special-cases `--env HOME=...`
# so we can't override at exec time; TaskProlog stamps HOME instead and we
# pre-create the per-user dir here on EFS. 0700 per-user, never shared.
HOME_ROOT="/mnt/efs/.home/${SLURM_JOB_USER:-root}"
install -d -m 0700 -o "$OWNER" "$HOME_ROOT"

# dcv-desktop bootstrap. Runs as root before the job's user script (which
# can't mkdir /var/run/dbus or start dbus-daemon / dcvserver). Gated on:
#
#   1. SLURM_JOB_NAME == "dcv-desktop". Slurm Prolog does NOT export
#      SLURM_JOB_NAME by default (verified on slurm 25.11 — only a fixed
#      set of SLURM_JOB_* env vars are passed), so we read it via
#      scontrol show job.
#   2. /usr/bin/dcvserver present. Installed only on amd64 in the slurmd
#      image (DCV ships no ARM binary); doubles as the architecture gate.
#
# Three load-bearing details (each captured the hard way):
#
#   - `setsid /usr/bin/dcvserver -d --service & disown` — the dcv wrapper
#     is fork+wait, not exec. A synchronous call blocks the prolog in
#     do_wait and slurmctld marks the node Prolog-FailedNode after ~5 min.
#   - dbus system bus must be up first (the dcv CLI talks to dcvserver
#     over DBus; without a bus daemon `dcv list-sessions` returns
#     "Could not get the system bus").
#   - Session owner must be a /etc/passwd user. nss_slurm users aren't,
#     so we own as root. Auth is at the DCV layer (auth-token-verifier),
#     not the OS layer, so this is fine.
#
# Idempotent: pgrep gates so a second concurrent dcv-desktop on the same
# node is a no-op for the daemons (one dcvserver per host, many sessions).
JOB_NAME=""
if command -v scontrol >/dev/null 2>&1; then
    JOB_INFO_FOR_NAME="$(scontrol show job -o "${SLURM_JOB_ID}" 2>/dev/null || true)"
    JOB_NAME="$(printf '%s\n' "$JOB_INFO_FOR_NAME" | { grep -oE 'JobName=[^[:space:]]+' || true; } | head -n1 | cut -d= -f2-)"
fi

if [ "$JOB_NAME" = "dcv-desktop" ] && [ -x /usr/bin/dcvserver ]; then
    # Rewrite the verifier URL placeholder baked into the image with the
    # central cluster-api URL the cap-pod was provisioned with. Slurm's
    # Prolog only inherits SLURM_* env vars from slurmd — CLUSTERRA_API is
    # set on the slurmd CONTAINER but not exported through to prolog
    # processes. Read it out of slurmd's /proc/1/environ (slurmd is PID 1
    # in the cap-pod's container PID ns). Idempotent — safe to run every
    # job; once the placeholder is gone, sed is a no-op.
    DCV_API_URL="${CLUSTERRA_API:-}"
    if [ -z "$DCV_API_URL" ] && [ -r /proc/1/environ ]; then
        DCV_API_URL="$(tr '\0' '\n' < /proc/1/environ 2>/dev/null \
                       | grep -oE '^CLUSTERRA_API=.+' \
                       | head -n1 | cut -d= -f2- || true)"
    fi
    if [ -n "$DCV_API_URL" ] && [ -f /etc/dcv/dcv.conf ]; then
        sed -i "s|__CLUSTERRA_API__|${DCV_API_URL}|g" /etc/dcv/dcv.conf
    fi

    mkdir -p /var/run/dbus
    dbus-uuidgen --ensure 2>/dev/null || true
    pgrep -x dbus-daemon >/dev/null 2>&1 \
        || { dbus-daemon --system --fork || true; sleep 1; }

    # If a dcvserver is already running, make sure it loaded the rewritten
    # config. The wrapper script's PID is in /var/run/dcv/server.pid; check
    # its /proc/<pid>/cmdline + the live config to detect placeholder
    # contamination, then kill & respawn.
    DCV_NEEDS_RESTART=0
    if pgrep -f '/usr/lib/x86_64-linux-gnu/dcv/dcvserver' >/dev/null 2>&1; then
        if grep -q '__CLUSTERRA_API__' /etc/dcv/dcv.conf 2>/dev/null; then
            DCV_NEEDS_RESTART=1
        fi
        # Even if the file is now clean, the running dcvserver may have
        # loaded the placeholder version. Check the most recent server log
        # for the in-memory verifier URL.
        if grep -q '__CLUSTERRA_API__' /var/log/dcv/server.log 2>/dev/null; then
            DCV_NEEDS_RESTART=1
        fi
    fi
    if [ "$DCV_NEEDS_RESTART" = 1 ]; then
        pkill -f /usr/lib/x86_64-linux-gnu/dcv/dcvserver 2>/dev/null || true
        sleep 2
    fi
    pgrep -f '/usr/lib/x86_64-linux-gnu/dcv/dcvserver' >/dev/null 2>&1 \
        || { setsid /usr/bin/dcvserver -d --service </dev/null >/dev/null 2>&1 & disown; sleep 2; }

    # Wait up to 30s for dcvserver to bind 8443. /dev/tcp probe in a
    # subshell so the `set -e` at top of file doesn't fire when the bind
    # hasn't happened yet.
    for _ in $(seq 1 30); do
        if (echo > /dev/tcp/127.0.0.1/8443) 2>/dev/null; then break; fi
        sleep 1
    done

    dcv create-session --type virtual --owner root --user root \
        "job-${SLURM_JOB_ID}" >/dev/null 2>&1 || true
fi

exit 0
