#!/bin/bash
# Slurm TaskProlog — runs as the user once per task. Anything it prints to
# stdout in the form `export VAR=value` is spliced into the task env.
#
# Per-job scratch and caching: We stamp TMPDIR onto whichever scratch path
# Prolog created, and pin APPTAINER_TMPDIR/APPTAINER_CACHEDIR to the same root
# so `apptainer pull` (OCI layer unpack, hardlink-heavy) lands on NVMe when
# present and never on EFS — EFS NFSv4.1 caps hardlinks per inode and trips on
# common base layers (e.g. /usr/bin/crontab) the moment Nextflow fans out
# parallel image pulls. See storage_strategy.md.
#
# Universal cache vars: Framework and tool-specific cache directories are
# exported pointing to per-job subdirectories under the same scratch root.
# This prevents multi-job cache collisions (e.g. Nextflow hardlink storms on
# EFS when jobs pull the same SIF concurrently) and cache bloat on EFS.
# See template-platform-plan.md §2.1.
if [ -d "/mnt/scratch/job-${SLURM_JOB_ID}" ]; then
  ROOT="/mnt/scratch/job-${SLURM_JOB_ID}"
else
  ROOT="/mnt/efs/tmp/job-${SLURM_JOB_ID}"
fi

# Scratch + container temp/cache
echo "export TMPDIR=${ROOT}"
echo "export APPTAINER_TMPDIR=${ROOT}/apptainer-tmp"
echo "export APPTAINER_CACHEDIR=${ROOT}/apptainer-cache"

# Nextflow caches
echo "export NXF_HOME=${ROOT}/nxf-home"
echo "export NXF_WORK=${ROOT}/nxf-work"
echo "export NXF_TEMP=${ROOT}/nxf-temp"

# ML/DL framework caches
echo "export HF_HOME=${ROOT}/hf-home"
echo "export TRANSFORMERS_CACHE=${ROOT}/hf-home/transformers"
echo "export TORCH_HOME=${ROOT}/torch-home"
echo "export TRITON_CACHE_DIR=${ROOT}/triton-cache"

# Python packaging caches
echo "export PIP_CACHE_DIR=${ROOT}/pip-cache"
echo "export UV_CACHE_DIR=${ROOT}/uv-cache"
echo "export PYTHONUSERBASE=${HOME}/.local"

# Conda/mamba bootstrap (absorbs per-template mamba-create boilerplate)
echo "export MAMBA_ROOT_PREFIX=${HOME}/.mamba"
echo "export CONDA_PKGS_DIRS=${ROOT}/conda-pkgs"

# Generic XDG cache
echo "export XDG_CACHE_HOME=${ROOT}/xdg-cache"

# Override nss_slurm's home=/nonexistent. Pre-created 0700 by Prolog on EFS.
# Apptainer respects HOME from the task env (the `--env HOME` block only
# applies when set on the apptainer CLI), so this propagates into the
# container too. APPTAINERENV_HOME is the apptainer-supported channel.
echo "export HOME=/mnt/efs/.home/${USER:-$(id -un)}"
echo "export APPTAINERENV_HOME=/mnt/efs/.home/${USER:-$(id -un)}"

# Job array row export: when CLUSTERRA_ARRAY_MANIFEST is set and this is an
# array task, read row $SLURM_ARRAY_TASK_ID (1-based) from the manifest and
# export each declared column as ARRAY_ROW_<COL> into the task env.
# Supports csv (default) and tsv; header row is line 1, data starts line 2.
if [ -n "${CLUSTERRA_ARRAY_MANIFEST}" ] && [ -n "${SLURM_ARRAY_TASK_ID}" ]; then
  _sep=","
  [ "${CLUSTERRA_ARRAY_MANIFEST_FORMAT}" = "tsv" ] && _sep=$'\t'
  # Line 1 = header; line (TASK_ID+1) = data row (tasks are 1-based).
  _header=$(sed -n '1p' "${CLUSTERRA_ARRAY_MANIFEST}")
  _row=$(sed -n "$((SLURM_ARRAY_TASK_ID + 1))p" "${CLUSTERRA_ARRAY_MANIFEST}")
  if [ -n "${_row}" ] && [ -n "${CLUSTERRA_ARRAY_ROW_VARS}" ]; then
    _col_idx=1
    IFS="${_sep}" read -ra _cols <<< "${_header}"
    IFS="${_sep}" read -ra _vals <<< "${_row}"
    for _col in "${_cols[@]}"; do
      _col_upper=$(echo "${_col}" | tr '[:lower:]' '[:upper:]')
      # Only export columns declared in CLUSTERRA_ARRAY_ROW_VARS.
      if echo ",${CLUSTERRA_ARRAY_ROW_VARS}," | grep -qi ",${_col},"; then
        echo "export ARRAY_ROW_${_col_upper}=${_vals[$((_col_idx - 1))]}"
      fi
      _col_idx=$((_col_idx + 1))
    done
  fi
fi
