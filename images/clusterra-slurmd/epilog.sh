#!/bin/bash
# Slurm Epilog — runs after the job step ends, on each allocated node.
# Best-effort scratch cleanup; idempotent and tolerant of pre-removed dirs
# so a partial-completion job still tears its scratch down.
rm -rf "/mnt/scratch/job-${SLURM_JOB_ID}" "/mnt/efs/tmp/job-${SLURM_JOB_ID}" 2>/dev/null || true

# Private-repo credential cleanup: in the rare case the Prolog crashed
# before staging the credential bundle into per-job scratch, the source
# file at /mnt/efs/clusterra-system/git-creds/<slot>.cred may still exist.
# Delete it unconditionally so a bricked Prolog never leaves a long-lived
# token on EFS. The staged copy inside scratch is removed by the scratch
# rm -rf above.
if command -v scontrol >/dev/null 2>&1; then
  JOB_INFO="$(scontrol show job -o "${SLURM_JOB_ID}" 2>/dev/null || true)"
  COMMENT_BLOCK="$(printf '%s\n' "$JOB_INFO" | grep -oE 'Comment=[^[:space:]]+' | head -n1 | cut -d= -f2-)"
  CREDS_SLOT="$(printf '%s\n' "$COMMENT_BLOCK" | grep -oE 'creds_slot=[0-9a-fA-F-]+' | head -n1 | cut -d= -f2-)"
  if [ -n "$CREDS_SLOT" ]; then
    rm -f "/mnt/efs/clusterra-system/git-creds/${CREDS_SLOT}.cred" 2>/dev/null || true
  fi
fi
# Legacy path cleanup (pre-Phase-1 layout). Safe to remove once a release
# cycle has shipped with the new path.
rm -f "/mnt/efs/.git-creds/job-${SLURM_JOB_ID}.cred" 2>/dev/null || true

# NB: no explicit DCV close-session here. Cap-pods are per-job ephemeral
# (Karpenter consolidates the node ~3 min after the last job ends), so
# dcvserver — and every session under it — dies with the cap-pod. Adding
# an explicit close would require wiring Epilog= into slurm.conf and
# guarding against non-DCV jobs; not worth the complexity for sub-minute
# leak windows on a node that's about to terminate anyway.

exit 0
