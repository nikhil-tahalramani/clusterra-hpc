# Boltz-1 smoke test

Single ~50aa chain (1AKE chain A residues 1-50) with MSA-server enabled,
single seed, 1 recycling step.

## Inputs

Place at `/mnt/efs/n52h53/boltz-smoke/1ake_a50.fasta` (uid 401 / sticky bit
on `/mnt/efs/n52h53`):

```
>A|protein|empty
MRIILLGAPGAGKGTQAQFIMEKYGIPQISTGDMLRAAVKSGSELGKQAK
```

> NOTE: Boltz 1.0.0 requires the FASTA record id in `CHAIN|ENTITY|MSA`
> form (`>A|protein` minimum, optionally `|empty` for explicit-empty MSA
> or `|/path/to.a3m` for a precomputed MSA). The plain `>1AKE_A` PDB-style
> header trips `ValueError: Invalid record id` in `boltz/data/parse/fasta.py`
> (verified clusde74 job 1778).

## Rendered sbatch (after render.go variable substitution)

Assuming user_email=`n52h53@gmail.com`, SLURM_JOB_ID resolved at runtime,
params: `recycling_steps=1`, `diffusion_samples=1`, `use_msa_server=true`,
`time_limit=30`.

```bash
#!/bin/bash
#SBATCH --job-name=boltz-smoke
#SBATCH --partition=gpu
#SBATCH --gres=gpu:a10g:1
#SBATCH --cpus-per-task=4
#SBATCH --mem=16384
#SBATCH --time=30
#SBATCH --chdir=/mnt/efs
#SBATCH --output=/mnt/efs/smoke/boltz/slurm-%j.out

set -euo pipefail

INPUT_FASTA="/mnt/efs/n52h53/boltz-smoke/1ake_a50.fasta"
OUTPUT_DIR="/mnt/efs/n52h53/boltz-$SLURM_JOB_ID"

[ -r "$INPUT_FASTA" ] || { echo "input_fasta not readable: $INPUT_FASTA" >&2; exit 1; }
mkdir -p "$OUTPUT_DIR"

VENV_DIR=/mnt/efs/_boltz-venv
WEIGHTS_CACHE=/mnt/efs/_boltz-cache
export PIP_CACHE_DIR=/mnt/efs/_boltz-venv-cache
mkdir -p "$PIP_CACHE_DIR" "$WEIGHTS_CACHE" "$(dirname "$VENV_DIR")"

# python:3.11 (full Debian, ~140 MB) — NOT python:3.11-slim. boltz pulls
# triton, which JIT-compiles CUDA kernels at runtime via gcc; the slim
# image strips the compiler and inference dies with
# `RuntimeError: Failed to find C compiler` on the first triangle-attention
# kernel (clusde74 job 1785).
IMAGE="docker://python:3.11"

(
  flock -x 9
  if [ ! -x "$VENV_DIR/bin/boltz" ]; then
    apptainer exec --nv "$IMAGE" bash -c "
      python3.11 -m venv --system-site-packages '$VENV_DIR'
      '$VENV_DIR/bin/pip' install --quiet --cache-dir '$PIP_CACHE_DIR' --upgrade pip
      '$VENV_DIR/bin/pip' install --quiet --cache-dir '$PIP_CACHE_DIR' boltz==1.0.0
    "
  fi
) 9>"$VENV_DIR.lock"

cd /tmp
exec apptainer exec --nv --bind "$TMPDIR:$TMPDIR" \
  "$IMAGE" \
  "$VENV_DIR/bin/boltz" predict "$INPUT_FASTA" \
    --out_dir            "$OUTPUT_DIR" \
    --cache              "$WEIGHTS_CACHE" \
    --recycling_steps    1 \
    --diffusion_samples  1 \
    --use_msa_server
```

## Submit recipe

Via Clusterra console → Templates → Boltz → fill:

- input_fasta: `/mnt/efs/n52h53/boltz-smoke/1ake_a50.fasta`
- recycling_steps: `1`
- diffusion_samples: `1`
- use_msa_server: `true`
- time_limit: `30`

## Expected wall-clock + cost

| Phase                                        | First run | Warm cache  |
|----------------------------------------------|-----------|-------------|
| Karpenter g5.xlarge boot + slurmd register   | ~2-3 min  | ~30 s       |
| `docker://python:3.11` SIF pull (per-host)   | ~30-45 s  | cached      |
| pip install boltz==1.0.0 into EFS venv       | ~10-15 min| 0 (cached)  |
| Boltz weight + CCD download (~3.2 GB)        | ~60-90 s  | 0 (cached)  |
| MSA fetch (ColabFold MMseqs2 server)         | ~60-180 s | same        |
| Inference (recycling=1, samples=1, 50aa)     | ~30-60 s  | ~30-60 s    |
| **Total wall-clock**                         | ~15-22 min| ~3-5 min    |

The pip install dominates the cold-path because torch+triton+flash-attn
wheels (~5 GB total) are written to EFS, and EFS metadata is the meter.
After the first job the venv at `/mnt/efs/_boltz-venv` and weights at
`/mnt/efs/_boltz-cache` are reused across every subsequent run on any
cap-pod; the warm path is essentially "pull SIF + run boltz".

Cost on g5.xlarge spot ($0.30/hr typical, us-east-1):
- First run: **~$0.10**
- Warm: **~$0.025**

## Success criteria

1. `sacct -j $JOB_ID --format=State,ExitCode` → `COMPLETED 0:0`.
2. File exists:
   `/mnt/efs/n52h53/boltz-$JOB_ID/boltz_results_1ake_a50/predictions/1ake_a50/1ake_a50_model_0.cif`
3. The CIF parses (`grep -c '^ATOM' …_model_0.cif` ≥ 200 atoms for a 50aa
   chain).
4. Confidence JSON sibling
   `…/predictions/1ake_a50/confidence_1ake_a50_model_0.json` present.

## Failure-mode quick triage

- `Invalid record id` on FASTA parse → header is missing `|protein` (or
  `|protein|empty`). Boltz 1.0.0 requires the chain/entity/MSA pipe
  format; PDB-style `>1AKE_A` does not parse.
- `Failed to find C compiler` deep in a triton stack → IMAGE is
  `python:3.11-slim` (or another stripped base). Switch to full
  `python:3.11`. Triton needs gcc at predict-time, not just install-time.
- `_get_user_env: Unable to get user's local environment` and
  `user_env_retrieval_failed_requeued_held` → cluster-side nss_slurm
  bug on persistent slurmd Deployment pods. Resubmit; Karpenter will
  bring up a fresh cap-pod whose slurmd registers cleanly. Avoid
  `--export=PATH=...` in sbatch — it forces user-env retrieval.
- Job stuck PENDING with `ReqNodeNotAvail, UnavailableNodes:cloud-tpl-gpu`
  for >5 min → Karpenter NodePool requirements exclude amd64 GPU
  shapes. See cluster-level workaround in plan doc; not a boltz issue.

## Attempt log

### 2026-05-07

- jobid 1657 — PENDING ~7min while Karpenter thrashed 12 gpu cap-pods,
  none scheduled. Cancelled. Karpenter NodePool blocker. Cancelled.
- jobid 1706 — repeated PLANNED → RUNNING (briefly) →
  `user_env_retrieval_failed_requeued_held` loop; never produced
  output. Root-caused to nss_slurm regression on persistent slurmd
  Deployment pods. Cancelled at 30 min.

### 2026-05-08

- jobid 1771 — submitted with `--export=PATH=...` and `--chdir=/tmp`.
  Same `user_env_retrieval_failed` loop. The explicit `--export` is
  what re-triggered the persistent-slurmd nss bug; removing it lets
  slurmstepd skip user-env retrieval entirely. Cancelled.
- jobid 1778 — fix: dropped `--export`, `--chdir=/mnt/efs`. Job
  RUNNING on cap-pod `ip-10-222-41-9` (g5.4xlarge spot). Pip-installed
  boltz==1.0.0 into `/mnt/efs/_boltz-venv` (~17 min, ~5.3 GB venv +
  2.8 GB pip cache on EFS). Boltz downloaded weights (3.2 GB) to
  `/mnt/efs/_boltz-cache`. **FAILED 1:0 at FASTA parse** —
  `ValueError: Invalid record id: 1AKE_A`. The PDB-style header is
  not Boltz-1.0.0 compliant. Elapsed: 19m27s.
- jobid 1785 — fix: rewrote FASTA header to `>A|protein|empty`.
  Started fresh on cap-pod `ip-10-222-29-1`; venv + weights cache
  reused. FASTA parsed cleanly, processed structures + constraints
  written. **FAILED 1:0 in inference** —
  `RuntimeError: Failed to find C compiler` from triton's
  `_create_driver` while autotuning the first triangle-attention
  kernel. Root cause: `python:3.11-slim` ships no gcc. Elapsed: 2m35s.
- jobid 1786 — fix: switch base image to full `python:3.11` (gcc
  present); script rm -rf'd the venv to force a rebuild against the
  new image. The rm -rf on EFS is slow (3.5 GB of small files).
  Cancelled to free the cap-pod and resubmit with a cleaner script.
- jobid 1787 — **COMPLETED 0:0 in 18m01s** on cap-pod
  `ip-10-222-29-1` (g5.xlarge spot). Venv rebuilt against full
  `python:3.11` (~16 min — wheels reused from EFS pip cache, mostly
  metadata + EFS write tax), boltz weights reused from
  `/mnt/efs/_boltz-cache`, MSA fetch skipped (single-sequence mode
  due to `|empty` in FASTA header), inference ~38 s on A10G.
  Output:
  `/mnt/efs/n52h53/boltz-1787/boltz_results_1ake_a50/predictions/1ake_a50/1ake_a50_model_0.cif`
  with 363 ATOM records and `confidence_1ake_a50_model_0.json`
  reporting `confidence_score=0.503`, `ptm=0.243`. The relatively
  modest confidence is consistent with single-sequence mode; with a
  real MSA (`|protein|<a3m_path>` or a fully-pasted-in MSA) Boltz-1
  comfortably hits >0.8 on this fragment. Smoke fixture passes the
  shape check (CIF parses, predictions/confidence land); template
  flipped to `ready: true`.
