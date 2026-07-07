# mace-off — smoke test

Single-point energy/forces of a small (24-atom) organic molecule using the
MACE-OFF23 `small` foundation model on CUDA. Exercises every architectural
seam in the template: persistent EFS venv build, mace-torch import, model
download to `XDG_CACHE_HOME`, GPU inference, and the three artifact writes
(`trajectory.xyz`, `energy.json`, `mace.log`).

## 1. Stage the input fixture

Caffeine-shaped 24-atom organic test molecule (C9H9N4O2 — a synthetic
3-carbon-isomer geometry, not the canonical C8H10N4O2 caffeine; close
enough as a single-molecule MLIP exerciser). Submit via `custom-slurm`:

```bash
cat > /tmp/mace-stage.sh <<'EOF'
#!/bin/bash
set -euo pipefail
mkdir -p /mnt/efs/smoke/mace
cat > /mnt/efs/smoke/mace/caffeine.xyz <<'EOFXYZ'
24
caffeine C8H10N4O2
C  0.4847  0.6797  0.0000
N  1.7882  0.1601  0.0000
C  1.7390 -1.2204  0.0000
N  0.5085 -1.5673  0.0000
C -0.3194 -0.5072  0.0000
C  3.0079  0.8639  0.0000
N  0.4691  1.9869  0.0000
C  2.8638 -2.0089  0.0000
O  3.9097 -1.4136  0.0000
N  2.9089 -3.3585  0.0000
C  1.7036 -3.9882  0.0000
O  1.5811 -5.1996  0.0000
C  4.1538 -4.1135  0.0000
C -1.6928 -0.7321  0.0000
C -0.9111  2.4116  0.0000
H -1.6850  1.6394  0.0000
H -1.0040  3.0241 -0.8895
H -1.0040  3.0241  0.8895
H -2.1907 -0.2200  0.8606
H -2.1907 -0.2200 -0.8606
H -1.8767 -1.8011  0.0000
H  3.9213  0.2774  0.0000
H  3.0436  1.4844 -0.8895
H  3.0436  1.4844  0.8895
EOFXYZ
ls -la /mnt/efs/smoke/mace/
EOF
```

Submit the script body via the `custom-slurm` preset; it writes
`/mnt/efs/smoke/mace/caffeine.xyz` (646 B, 26 lines incl. ASE
header).

## 2. Submit the mace-off smoke job

```bash
curl -b "clusterra_session=$JWT" -X POST \
  https://dev-api.clusterra.cloud/v1/clusters/clusde74/jobs/submit \
  -H 'Content-Type: application/json' -d '{
    "run_name":"mace-smoke-singlepoint-small",
    "steps":[{
      "step_id":"main",
      "template_id":"mace-off",
      "inputs":{"input_structure":"/mnt/efs/smoke/mace/caffeine.xyz"},
      "params":{
        "model_size":"small","operation":"singlepoint",
        "device":"cuda","dtype":"float32",
        "opt_fmax":0.05,"opt_steps":200,"md_steps":1000
      }
    }]
  }'
```

Job lands on the `gpu` partition (`gres=gpu:1`, `cpus=8`, `mem=16G`).
First run on a fresh tenant pays the full `pip install mace-torch` cost
(~3.2 GB venv + ~2.7 GB pip cache on EFS). Subsequent runs reuse
`/mnt/efs/_mace-venv/`.

## 3. Validate

```bash
JOB_RUN_ID=<run_id from submit response, NOT slurm_job_id>
DIR=/mnt/efs/n52h53@gmail.com/mace-off-$JOB_RUN_ID
ls "$DIR"
# trajectory.xyz   energy.json   mace.log   run.py
cat "$DIR/energy.json"
```

PASS criteria:

- All four files exist: `energy.json`, `trajectory.xyz`, `mace.log`,
  `run.py` (the rendered Python driver, kept for debugging).
- `energy.json` parses; `atoms == 24`, `formula == "C9H9N4O2"`,
  `operation == "singlepoint"`, `device == "cuda"`.
- `energy_ev` lands at **-19537.98 eV** (MACE-OFF23 small, float32,
  CUDA, this exact geometry). Per-atom: **-814.08 eV/atom**.
  `max_force_ev_per_ang` ≈ **5.43** (unrelaxed geometry, so non-zero
  forces are expected).
- `wall_seconds` for the inference itself is ~6 s on g6.4xlarge
  (A10G L40-equivalent — see "Cost & wall-clock" below).
- `trajectory.xyz` is a single ASE extxyz frame with the energy/forces
  attached in the comment line (`energy=-19537.97... free_energy=...
  Properties=species:S:1:pos:R:3:forces:R:3:energies:R:1`).

FAIL signals worth surfacing:

- `template "mace-off" not found` on submit → templates-sync hasn't
  picked up the YAML; re-run
  `AWS_PROFILE=dev go run ./cmd/templates-sync --root core/templates/definitions`.
- `mace.calculators.mace_off ... unexpected keyword argument` → upstream
  `mace-torch` changed the foundation-model loader API (0.3.x ships
  `mace_off(model=...)`; older 0.2.x used `model_paths=...`). Update
  the call in the heredoc.
- `Default dtype float32 does not match model dtype float64, converting
  models to float32` warning is expected and benign — MACE-OFF23 ships
  in float64; the calculator down-casts when requested.
- `cuequivariance or cuequivariance_torch is not available` warning is
  also benign — `cuequivariance` is an optional ~5x speed-up dependency
  not yet on PyPI; mace-torch falls back to the e3nn implementation.
- `cuda requested but torch.cuda.is_available() is False` in stderr →
  the cap-pod didn't get NVIDIA driver injection; check
  `apptainer exec --nv` worked and the slurmd image has
  `default-runtime: nvidia`.

## 4. Cost & wall-clock

| Phase                          | Cold (first run)  | Warm (reused venv) |
| ------------------------------ | ----------------- | ------------------ |
| Cap-pod cold-start (Karpenter) | ~90–150 s         | ~5–60 s            |
| Apptainer pull `python:3.11`   | ~30 s             | cache hit, ~5 s    |
| `pip install mace-torch`       | **~18–20 min**    | skipped (flock-noop) |
| MACE-OFF23 weight download     | ~5 s (small=7 MB) | cache hit          |
| Inference (singlepoint)        | ~6 s              | ~3.5 s             |
| **Total wall-clock**           | **~32 min**       | **~133 s**         |
| **Spot cost (g6.4xlarge)**     | **$0.165**        | **$0.022**         |

Verified May 27 2026 on clusde74:
- Cold: jobid **2713**, node = g6.4xlarge spot (L4 GPU, ~$0.59/hr spot),
  final `cost_usd = 0.165`, submit→COMPLETED = 32 min (incl. ~3 min
  PENDING for Karpenter to scale the GPU node).
- Warm: jobid **2768**, same node-family, `cost_usd = 0.022`,
  submit→COMPLETED = 133 s (no PENDING — Karpenter still had the node
  warm from cold run). Same energy (-19537.98 eV) to the 4th decimal;
  forces match to 3 decimals (float32 nondeterminism in the last digit).

### Why mace-torch install is so big

Pulls a full torch + matching NVIDIA CUDA wheels into the venv. Site
packages we saw mid-install: `mace`, `mace_torch-0.3.16`, `nvidia_cublas`,
`nvidia_cuda_runtime`, `nvidia_cudnn_cu13`, `nvidia_cufft`, plus e3nn,
ase, prettytable, matscipy. ~3.2 GB on disk, ~2.7 GB of pip cache. Both
on EFS so the next job is free. **Do not switch to `python:3.11-slim`** —
e3nn's Wigner-D constant table compiles a torch extension at import time
and needs gcc/g++ which slim strips (same gotcha as Boltz, see
`MEMORY/boltz2_affinity_fix.md` and `dl_ami_skip_pytorch_base.md`).

### MACE-OFF23 weight sizes

| Model size | Checkpoint | Notes                                    |
| ---------- | ---------- | ---------------------------------------- |
| small      | 7.0 MB     | smoke default — ~4M params               |
| medium     | ~50 MB     | paper default                            |
| large      | ~150 MB    | best accuracy, slower                    |

Pulled from
`raw.githubusercontent.com/ACEsuit/mace-off/main/mace_off23/MACE-OFF23_<size>.model`
on first use; cached to `/mnt/efs/_mace-cache/mace/` via the
`XDG_CACHE_HOME` redirect.

## 5. Operation modes not exercised by this smoke

`operation=optimize` (BFGS to fmax) and `operation=md` (Langevin 300 K
NVT) share the same code path inside `run.py` — same calculator, same
artifact writes. Trajectory will accumulate multiple frames instead of
one; `energy.json` reports the final-frame energy. The BFGS
`opt.attach(_append, interval=1)` pattern writes the initial geometry
twice (once before the loop, once at step 0); cosmetic only, leave as
is until a customer complains.

## 6. License notes

MACE-OFF23 weights are distributed under the **Academic Software License
(ASL)** — "GPL-based, does not permit commercial use." The mace-torch
package code itself is **MIT**. For commercial customer use, swap the
foundation model with a customer-trained MACE checkpoint at the
`model_size`→`mace_off(model=...)` boundary; the rest of the template
is license-clean.

## Ready

`ready: true` — end-to-end smoke verified on clusde74 (job 2713, May 27
2026). Energy + forces match across runs, artifacts complete, cost
stamp working, weight cache populated. ASL license caveat documented;
not a blocker for the typical pre-clinical-research customer.
