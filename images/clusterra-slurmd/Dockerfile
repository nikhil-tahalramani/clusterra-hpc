# Extends Slinky's stock slurmd with apptainer + nextflow launcher + JRE.
# - apptainer: Jupyter interactive + Nextflow task containers (`apptainer exec docker://...`)
# - curl: ready-callback in interactive template + nextflow launcher download
# - JRE 21 + nextflow launcher: nf-core pipelines invoke `nextflow run` directly;
#   the engine jar is self-downloaded at runtime per NXF_VER pinned in the
#   sbatch template, so an engine version bump stays a templates-rev (no rebuild).
# Multi-arch (amd64 + arm64) — apt resolves the arch-specific JRE automatically;
# the nextflow launcher is an arch-agnostic bash+capsule shim.

FROM ghcr.io/slinkyproject/slurmd:25.11-ubuntu24.04

USER root

# PPA signing key: F6B0F5193D4F3301EF491FF0AFE36534FC6218AE (apptainer/ppa)
RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends \
        ca-certificates \
        curl \
        gnupg \
        openjdk-21-jre-headless; \
    install -d -m 0755 /etc/apt/keyrings; \
    curl -fsSL "https://keyserver.ubuntu.com/pks/lookup?op=get&search=0xF6B0F5193D4F3301EF491FF0AFE36534FC6218AE" \
        | gpg --dearmor -o /etc/apt/keyrings/apptainer.gpg; \
    echo "deb [signed-by=/etc/apt/keyrings/apptainer.gpg] https://ppa.launchpadcontent.net/apptainer/ppa/ubuntu noble main" \
        > /etc/apt/sources.list.d/apptainer.list; \
    apt-get update; \
    apt-get install -y --no-install-recommends \
        apptainer \
        apptainer-suid; \
    curl -fsSL https://get.nextflow.io -o /usr/local/bin/nextflow; \
    chmod +x /usr/local/bin/nextflow; \
    apt-get purge -y --auto-remove gnupg; \
    rm -rf /var/lib/apt/lists/*; \
    apptainer --version; \
    java -version; \
    /usr/local/bin/nextflow -v

# Splice `slurm` into nsswitch so libnss_slurm.so.2 (shipped by the upstream
# Slinky base image at /usr/lib/<arch>-linux-gnu/) resolves users that
# slurmctld bundled into the launch credential. Without this, apptainer-suid's
# host-side user.Lookup() FATALs with "unknown userid N" the moment a job
# tries to start. Originally added in 89dc394, deleted in 8ae2077 in favor
# of a central kubectl-exec hack — verified May 2 2026 (clusde74 job 1101)
# that this splice + `LaunchParameters=enable_nss_slurm` on slurmctld makes
# the central exec entirely unnecessary, and nss_slurm returns a strictly
# richer record (real home, shell, GID).
RUN set -eux; \
    sed -ri 's/^(passwd:\s+)(.*)$/\1\2 slurm/; s/^(group:\s+)(.*)$/\1\2 slurm/' /etc/nsswitch.conf; \
    grep -E '^(passwd|group):' /etc/nsswitch.conf

# NB: APPTAINER_CACHEDIR / APPTAINER_TMPDIR are NOT set as image-level ENV
# any more — they're stamped per-job by /etc/clusterra/task_prolog.sh onto
# the per-job scratch root (NVMe `/mnt/scratch/job-$ID/` when available,
# EFS `/mnt/efs/tmp/job-$ID/` fallback). The OCI-layer unpack inside
# `apptainer pull` reproduces hardlinks (e.g. /usr/bin/crontab) and trips
# EFS's NFSv4.1 hardlink-per-inode ceiling when ~15 sarek SIFs pull in
# parallel — so the cache must live on a real POSIX FS. Cross-node SIF
# reuse is provided by NXF_SINGULARITY_CACHEDIR=/mnt/efs/nxf-cache/_lib
# (final SIFs only, no hardlink-heavy unpack happens there).

# Default bind paths so templates don't need `--bind /mnt/efs:/mnt/efs` on
# every apptainer exec line. /mnt/s3-refs is the read-only mountpoint where
# central reference genomes live (rolled out per-cluster); apptainer warns
# but doesn't fail when a host bind path is missing.
#
# NOTE: $TMPDIR is intentionally NOT in the config-level bind path list.
# apptainer evaluates `bind path = $TMPDIR:$TMPDIR` using the slurmd
# container's own env, where TMPDIR is unset — yielding "source must be an
# absolute path" on every exec. The per-job TMPDIR bind is handled by each
# template script with `--bind "$TMPDIR:$TMPDIR"`, where bash expands the
# variable from the TaskProlog-exported task env before apptainer sees it.
#
# env path directives propagate TaskProlog-exported variables into containers
# without needing per-task --env flags.
RUN set -eux; \
    test -f /etc/apptainer/apptainer.conf || (echo "missing /etc/apptainer/apptainer.conf" && exit 1); \
    printf '\n# clusterra defaults — see core/images/clusterra-slurmd/Dockerfile\nbind path = /mnt/efs:/mnt/efs\nbind path = /mnt/s3-refs:/mnt/s3-refs\nbind path = /dev/shm:/dev/shm\n\nenv path = NXF_HOME, NXF_WORK, NXF_TEMP, NXF_CONFIG_FILE\nenv path = HF_HOME, TRANSFORMERS_CACHE, TORCH_HOME, TRITON_CACHE_DIR\nenv path = PIP_CACHE_DIR, UV_CACHE_DIR, PYTHONUSERBASE\nenv path = MAMBA_ROOT_PREFIX, CONDA_PKGS_DIRS, XDG_CACHE_HOME\nenv path = APPTAINER_CACHEDIR, APPTAINER_TMPDIR\n' \
        >> /etc/apptainer/apptainer.conf; \
    grep -E '^(bind path|env path)' /etc/apptainer/apptainer.conf

# NVMe-conditional TMPDIR scripts. Slurm runs Prolog as root once per job
# per node before the job step starts; TaskProlog runs as the user, per
# task, and any `export VAR=...` line it prints is added to the task env.
#
# Behavior: /mnt/scratch exists only on Karpenter NVMe-bearing instance
# families (g5.*, m6id.*, etc.) where userData mkfs.xfs's the local NVMe
# raid. Non-NVMe nodes fall back to a per-job dir on EFS so TMPDIR is
# always defined — the previous "--tmp=N pends until *d family" behavior
# stays intact (Slurm honors the request) but jobs without --tmp=N still
# get a real TMPDIR on any node.
COPY prolog.sh task_prolog.sh epilog.sh /etc/clusterra/
RUN chmod +x /etc/clusterra/prolog.sh /etc/clusterra/task_prolog.sh /etc/clusterra/epilog.sh \
    && ls -la /etc/clusterra/

# NICE DCV + xfce desktop for the dcv-desktop interactive template (drug
# discovery 3D viewers — PyMOL/ChimeraX/VMD launched via apptainer inside
# the session). AMD64-only — every NVIDIA GPU instance family (g4dn, g5,
# g6, g6e) is x86_64 and DCV only ships AMD64 server binaries. On ARM
# nodes this whole stanza is a no-op so DCV isn't carried in the ARM
# manifest.
#
# dcv.conf wires the auth-token external-authentication verifier at
# cluster-api: dcvserver POSTs (sessionId, authenticationToken) on every
# client connection; cluster-api hashes the token, compares against the
# per-session dcv_token_hash stored in DDB, and returns root if it
# matches. The `__CLUSTERRA_API__` token is rewritten to the real central
# API URL by the Slurm Prolog at job start (see prolog.sh) — has to be
# runtime because CLUSTERRA_API varies per env (dev/prod) and per
# cluster's central reachability path.
ARG TARGETARCH
RUN set -eux; \
    if [ "${TARGETARCH:-}" = "amd64" ]; then \
        export DEBIAN_FRONTEND=noninteractive; \
        apt-get update; \
        apt-get install -y --no-install-recommends \
            xfce4 xfce4-terminal dbus-x11 dbus mesa-utils xz-utils unzip; \
        cd /tmp; \
        DCV_TGZ=nice-dcv-2024.0-19030-ubuntu2404-x86_64.tgz; \
        curl -fsSL -o "${DCV_TGZ}" "https://d1uj6qtbmh3dt5.cloudfront.net/2024.0/Servers/${DCV_TGZ}"; \
        tar -xzf "${DCV_TGZ}"; \
        cd nice-dcv-2024.0-19030-ubuntu2404-x86_64; \
        apt-get install -y --no-install-recommends \
            ./nice-dcv-server_*.deb \
            ./nice-xdcv_*.deb \
            ./nice-dcv-web-viewer_*.deb \
            ./nice-dcv-gl_*.deb; \
        rm -rf /tmp/nice-dcv-* /var/lib/apt/lists/*; \
        printf '%s\n' \
            '[license]' \
            '[log]' \
            '[display]' \
            'target-fps = 30' \
            '[connectivity]' \
            'web-port = 8443' \
            'quic-port = 8443' \
            'enable-quic-frontend = true' \
            'idle-timeout = 60' \
            '[security]' \
            'authentication = "auth-token"' \
            'auth-token-verifier = "__CLUSTERRA_API__/v1/internal/dcv/auth"' \
            'no-tls-strict = true' \
            '[session-management]' \
            'create-session = false' \
            > /etc/dcv/dcv.conf; \
        cat /etc/dcv/dcv.conf; \
    else \
        echo "Skipping DCV install on ${TARGETARCH:-unknown} — AMD64-only"; \
    fi
