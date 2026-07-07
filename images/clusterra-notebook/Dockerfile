# First vertical-template image for kind:interactive. Launched by the Jupyter
# template's sbatch script via `apptainer exec docker://<this>` on a Karpenter
# worker; the container does NOT run as a long-lived service — slurmd owns the
# lifecycle. No entrypoint override: the sbatch script invokes `jupyter lab`
# directly with runtime-generated port and token.
#
# Pre-pulled into the Apptainer cache by slurmd node userData to drop cold-start
# from ~120s to ~15s on warm nodes. Size budget: ~2GB (scipy base ~1.5GB +
# bioconda stack ~400MB).

FROM quay.io/jupyter/scipy-notebook:2026-04-20

USER root

# Slurm client for !sbatch / !squeue / !sacct inside the notebook. Clusterra
# uses AuthType=auth/slurm (shared slurm.key), NOT auth/munge, so no munge
# daemon or libmunge is needed — slurm-client reads /etc/slurm/slurm.key
# bind-mounted from the slurmd host by the sbatch Apptainer invocation when
# we wire cross-job submission.
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      slurm-client \
      curl \
      ca-certificates \
 && rm -rf /var/lib/apt/lists/*

USER $NB_UID

# Biotech stack — the differentiator vs. a vanilla JupyterHub. Kept in one
# mamba run so the solver resolves once. jupyter-collaboration enables the
# real-time cursor extension (invite UX is v2).
RUN mamba install -n base -y \
      -c conda-forge -c bioconda \
      rdkit \
      mdanalysis \
      nglview \
      biopython \
      pymol-open-source \
 && mamba clean -a -y

RUN pip install --no-cache-dir \
      jupyter-collaboration \
      jupyter-ai \
      dask-jobqueue

# Bake in the auto-generated Python SDK so notebooks can `from clusterra
# import Client` without a one-time pip install. The CI regen-clients
# workflow runs `openapi-python-client generate ... --output-path
# core/sdks/python/clusterra/_gen` immediately before the docker build, so
# `_gen/` is populated in the build context even though it's gitignored.
# Local builds without _gen still succeed — pip skips the install when the
# package layout is incomplete (the facade `__init__.py` survives a missing
# `_gen` import via its try/except guard).
COPY --chown=$NB_UID:$NB_GID sdks/python/clusterra /opt/clusterra-sdk/clusterra
COPY --chown=$NB_UID:$NB_GID sdks/python/pyproject.toml /opt/clusterra-sdk/pyproject.toml
RUN pip install --no-cache-dir /opt/clusterra-sdk || \
    echo "clusterra SDK install skipped (no _gen yet); facade import will raise at runtime"

# Per-user pip target so `!pip install` from the notebook doesn't race on a
# shared EFS path (users may be sharing /mnt/efs). Users can override.
ENV PIP_TARGET=/mnt/efs/${NB_USER}/.local/lib/python3.12/site-packages \
    PYTHONPATH=/mnt/efs/${NB_USER}/.local/lib/python3.12/site-packages:$PYTHONPATH
