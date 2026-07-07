# RFdiffusion smoke test

Smallest reproducible unconditional monomer design: 50 residues,
`num_designs=1`, `diffuser_t=15`. Cold path exercises the SIF pull
(~7.4 GB) and weight download (~3.8 GB across 8 `.pt` files); warm
path is essentially "apptainer exec on cached SIF".

## Inputs

None. Unconditional monomer design — no PDB required.

## Submit recipe

Via Clusterra console → Templates → RFdiffusion → fill:

- `contigs`: `[50-50]`
- `num_designs`: `1`
- `diffuser_t`: `15` (smoke; production default = 50)
- `symmetry`: `none`
- `extra_args`: (empty)
- `input_pdb`: (empty)
- `ppi_scaffolds_dir`: (empty)

Or via API:

```bash
JWT=$(cat /tmp/rfdiff-jwt.txt)  # mint per MEMORY api_jwt_mint_workflow
curl -b "clusterra_session=$JWT" -X POST \
  https://dev-api.clusterra.cloud/v1/clusters/clusde74/jobs/submit \
  -H 'Content-Type: application/json' -d '{
    "run_name":"rfdiff-smoke",
    "steps":[{
      "step_id":"main",
      "template_id":"rfdiffusion",
      "inputs":{"input_pdb":""},
      "params":{
        "contigs":"[50-50]",
        "num_designs":1,
        "diffuser_t":15,
        "symmetry":"none",
        "extra_args":""
      }
    }]
  }'
```

## Rendered sbatch (after render.go variable substitution)

```bash
#!/bin/bash
#SBATCH --job-name=rfdiffusion
#SBATCH --partition=gpu
#SBATCH --gres=gpu:1
#SBATCH --cpus-per-task=8
#SBATCH --mem=32G
#SBATCH --time=2:00:00
#SBATCH --nodes=1
#SBATCH --chdir=/mnt/efs

set -euo pipefail

OUTPUT_DIR="/mnt/efs/n52h53@gmail.com/rfdiff-$RUN_ID"
mkdir -p "$OUTPUT_DIR"

CACHE_ROOT=/mnt/efs/_rfdiffusion-cache
MODELS_DIR="$CACHE_ROOT/models"
SIF_PATH="$CACHE_ROOT/rfdiffusion.sif"
SCHEDULES_DIR="$CACHE_ROOT/schedules"
mkdir -p "$MODELS_DIR" "$SCHEDULES_DIR" "$(dirname "$SIF_PATH")"

IMAGE_URI="docker://rosettacommons/rfdiffusion:latest"

# Stage 1: SIF pull (flock-guarded, one-time ~7.4 GB)
( flock -x 9
  if [ ! -s "$SIF_PATH" ]; then
    apptainer pull --force "$SIF_PATH" "$IMAGE_URI"
  fi
) 9>"$SIF_PATH.lock"

# Stage 2: weights (flock-guarded, one-time ~3.8 GB)
( flock -x 9
  if [ ! -f "$MODELS_DIR/Base_ckpt.pt" ] || \
     [ ! -f "$MODELS_DIR/Complex_base_ckpt.pt" ] || \
     [ ! -f "$MODELS_DIR/Base_epoch8_ckpt.pt" ]; then
    apptainer exec --bind "$MODELS_DIR:/models" "$SIF_PATH" \
      bash /app/RFdiffusion/scripts/download_models.sh /models
  fi
) 9>"$MODELS_DIR/.lock"

# Stage 3: assemble Hydra overrides
ARGS=(
  "inference.output_prefix=$OUTPUT_DIR/design"
  "inference.model_directory_path=/models"
  "inference.schedule_directory_path=/schedules"
  "inference.num_designs=1"
  "diffuser.T=15"
  "contigmap.contigs=[50-50]"
  "hydra.run.dir=$OUTPUT_DIR/hydra"
  "hydra.output_subdir=null"
)
BIND_ARGS=( --nv
            --bind "$MODELS_DIR:/models"
            --bind "$SCHEDULES_DIR:/schedules"
            --bind "$OUTPUT_DIR:$OUTPUT_DIR" )

# Stage 4: run (apptainer skips Docker ENTRYPOINT — call .venv python directly)
cd "$OUTPUT_DIR"
exec apptainer exec "${BIND_ARGS[@]}" "$SIF_PATH" \
  bash -lc "cd '$OUTPUT_DIR' && /app/RFdiffusion/.venv/bin/python3.9 /app/RFdiffusion/scripts/run_inference.py ${ARGS[*]@Q}"
```

## Expected wall-clock + cost

| Phase                                          | Cold path  | Warm cache |
|------------------------------------------------|------------|------------|
| Karpenter gpu cap-pod boot + slurmd register   | ~2-4 min   | ~30 s      |
| SIF pull `rosettacommons/rfdiffusion:latest`   | ~3-5 min   | 0 (cached) |
| Weight download (8 `.pt` files, 3.8 GB)        | ~3-5 min   | 0 (cached) |
| IGSO3 schedule compute (first run only)        | ~5-8 s     | 0 (cached) |
| Inference (50aa, num_designs=1, diffuser_t=15) | ~25-40 s   | ~25-40 s   |
| **Total wall-clock**                           | ~10-15 min | ~1-2 min   |

After the first job, `/mnt/efs/_rfdiffusion-cache/{rfdiffusion.sif,models,schedules}`
is reused across every subsequent run on any cap-pod.

Cost on a g5.xlarge / g4dn.xlarge spot (us-east-1, A10G / T4):

- Cold: **~$0.08-0.12** (driven by EFS write tax on 3.8 GB weight download)
- Warm: **~$0.02-0.03**

## Validated smoke run

**Job 2833** on cluster `clusde74` (dev-demo), 2026-05-28 00:00 UTC:

- Cap-pod: `ip-10-222-29-252` (g4dn.xlarge spot, Tesla T4 — Karpenter
  picked T4 because the smoke ran with untyped `--gres=gpu:1` per the
  fix below).
- Wall-clock: **75 s** end-to-end (cache pre-warmed from prior Wave 1/2
  attempts).
- Inference: 0.37 min reported by RFdiffusion (`Finished design in 0.37
  minutes`), 15 timesteps at ~0.54 s/step on T4.
- Exit: `COMPLETED 0:0`.
- Output dir: `/mnt/efs/n52h53@gmail.com/rfdiff-5a6c19e0-6a3b-47c5-b829-0e2482958696`.

### Success criteria — all met

1. `sacct -j 2833 --format=State,ExitCode` → `COMPLETED 0:0` ✓
2. `design_0.pdb` exists (13,400 bytes) ✓
3. `design_0.pdb` has 200 ATOM records (50 residues × 4 backbone atoms
   N/CA/C/O = 200; matches a 50aa Gly polymer backbone) ✓
4. `design_0.trb` sibling exists with Hydra config dump (6,100 bytes) ✓
5. `traj/design_0_pX0_traj.pdb` (~200 KB) and
   `traj/design_0_Xt-1_traj.pdb` (~150 KB) present — full denoising
   trajectories ✓
6. `hydra/` directory with config snapshot ✓

```
$ ls /mnt/efs/n52h53@gmail.com/rfdiff-5a6c19e0-6a3b-47c5-b829-0e2482958696
design_0.pdb   design_0.trb   hydra/   traj/

$ head -5 design_0.pdb
ATOM      1  N   GLY A   1     -24.940   9.662  -0.137  1.00  0.00
ATOM      2  CA  GLY A   1     -24.596   8.247  -0.211  1.00  0.00
ATOM      3  C   GLY A   1     -23.431   8.012  -1.164  1.00  0.00
ATOM      4  O   GLY A   1     -22.543   7.206  -0.887  1.00  0.00
ATOM      5  N   GLY A   2     -23.496   8.597  -2.198  1.00  0.00
```

(Unconditional RFdiffusion output is sequence-agnostic — the residue
names are all Gly by convention; a downstream ProteinMPNN pass assigns
real side-chains.)

## Failure-mode quick triage

- Job stuck PENDING with `Nodes required for job are DOWN, DRAINED or
  reserved for jobs in higher priority partitions` or with reason
  `ReqNodeNotAvail, UnavailableNodes:cloud-tpl-gpu` → typed-GRES spot
  squeeze. Original draft used `--gres=gpu:a10g:1` (smoke 2812) which
  pinned the job to g5* shapes; us-east-1 a10g spot was unavailable so
  Karpenter provisioned a T4 instead and the typed request never
  matched. **Fixed in this template by using untyped `--gres=gpu:1`.**
  See MEMORY gpu_typed_gres_spot_squeeze.md. If you need an explicit
  VRAM floor (>24 GB activations on >300aa multi-state designs), pin
  via `--gres=gpu:l40s:1` through sbatch_override — but never via a10g.
- `ImportError: libcudart.so.11.0` or
  `RuntimeError: CUDA error: no kernel image is available for execution`
  → the host driver is too old for the SIF's CUDA 11.6 client libs.
  Should not occur on Clusterra DL AMIs (driver 535+); see MEMORY
  dl_ami_skip_pytorch_base.md.
- `[hydra]: Cannot find primary config 'inference/symmetry'` → user
  passed `symmetry=c3` etc. but the SIF's hydra config search-path
  doesn't include the symmetry config root. The template prepends
  `--config-name=symmetry` to ARGS when symmetry != none; if upstream
  ever moves the config, update the prepend.
- `OSError: [Errno 13] Permission denied: 'schedules/...'` → the
  `inference.schedule_directory_path` override didn't land. Confirm
  the runtime ARGS include `inference.schedule_directory_path=/schedules`
  and `BIND_ARGS` binds `$SCHEDULES_DIR:/schedules`. The Docker image
  ships a read-only `/app/RFdiffusion/schedules`, so nested binds under
  `/app` will fail under apptainer's read-only overlay.
- `WARNING: Overriding HOME environment variable with APPTAINERENV_HOME
  is not permitted` in stdout → harmless apptainer cosmetic, ignore.

## Attempt log

### 2026-05-27 (Wave 1 + Wave 2)

Both prior agents hit session quota before smoke-testing. Wave 2 left
the SIF (`/mnt/efs/_rfdiffusion-cache/rfdiffusion.sif`, 7.4 GB) and the
8 weight files (`Base_ckpt.pt`, `Complex_base_ckpt.pt`, etc., ~3.8 GB)
on EFS, so this run inherited a fully-warm cache. Cold-path numbers
above are estimated from the file sizes and EFS write-throughput
characteristics — not yet measured end-to-end.

### 2026-05-28 (Wave 3)

- **Job 2812** — submitted with the Wave 2 draft as-is (typed
  `--gres=gpu:a10g:1`). Sat PENDING ~7 minutes with reason `Nodes
  required for job are DOWN, DRAINED or reserved for jobs in higher
  priority partitions`. Root cause: Karpenter provisioned a T4 GPU
  cap-pod (ip-10-222-6-104) because a10g spot was squeezed in
  us-east-1; the typed-GRES request never matched. Job auto-cancelled
  by Slurm. **Fix: switch sbatch to untyped `--gres=gpu:1`.** See
  MEMORY gpu_typed_gres_spot_squeeze.md.
- **Job 2833** — resubmitted with untyped GRES. PENDING ~4 min while
  Karpenter brought up a second T4 cap-pod (ip-10-222-29-252,
  g4dn.xlarge spot). Inference ran in 0.37 min on T4. **COMPLETED 0:0
  in 75 s wall-clock total** (cache fully warm).
  Output `design_0.pdb` validated: 200 ATOM records for a 50aa
  backbone, well-formed PDB. Template flipped to `ready: true`.
- **Job 2835** — verification pass via `custom-slurm` to `ls` the
  output dir and confirm cache state. Confirmed all expected
  artifacts plus full warm cache.
- **Job 2837** — verification pass to grep `^ATOM` count from the
  produced PDB. Returned 200, exactly matching expectation for a 50aa
  backbone-only output.
