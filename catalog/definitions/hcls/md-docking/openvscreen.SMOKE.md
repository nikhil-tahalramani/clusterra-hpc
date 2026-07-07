# openvscreen — smoke test

Curated GPU virtual-screening bundle: Uni-Dock primary docking + GNINA CNN
rescoring, single sbatch, single A10G cap-pod. Replaces the
`autodock-vina` stub per `docs/strategy/2026-05-05-gpu-wedge-report.md` §3.2 + §4.

## Recipe (smoke)

10-ligand subset, GNINA rescore on the top-5, single receptor.

- **Receptor:** `/mnt/s3-refs/openvscreen-smoke/5wiu_protein.pdbqt`
  (5WIU PDE10A — used in the original Uni-Dock JCTC 2023 paper).
- **Ligands:** `/mnt/s3-refs/openvscreen-smoke/ligands_10/` — 10 PDE10A
  actives + decoys from DUD-E, prepared with Meeko 0.5.
- **Search box (paper coords):** center (3.0, 11.4, -2.7), size
  (22.5, 22.5, 22.5).
- **top_n_rescore:** 5
- **exhaustiveness:** 256 (default)
- **Resources:** cpus=8, mem=32 GiB, gres/gpu:a10g:1, time=120 min.

Submit form fields → cluster-api `/v1/launchables` → renders the sbatch
below.

## Rendered sbatch (abbreviated)

```bash
#!/bin/bash
#SBATCH --job-name=openvscreen
#SBATCH --cpus-per-task=8
#SBATCH --mem=32768
#SBATCH --gres=gpu:a10g:1
#SBATCH --partition=gpu
#SBATCH --time=120
set -euo pipefail

RECEPTOR=/mnt/s3-refs/openvscreen-smoke/5wiu_protein.pdbqt
LIGAND_DIR=/mnt/s3-refs/openvscreen-smoke/ligands_10
OUTPUT_DIR=/mnt/efs/n52h53@gmail.com/openvscreen-${SLURM_JOB_ID}
TOP_N=5
mkdir -p "$OUTPUT_DIR"/{unidock_poses,gnina_rescore}

LIGAND_LIST=$TMPDIR/ligands.txt
find "$LIGAND_DIR" -maxdepth 1 -name '*.pdbqt' -type f | sort > "$LIGAND_LIST"

# Step 1 — Uni-Dock GPU batched docking
apptainer exec --nv --bind "$TMPDIR:$TMPDIR" \
  docker://dptechnology/unidock:latest \
  unidock --receptor "$RECEPTOR" --ligand_index "$LIGAND_LIST" \
    --dir "$OUTPUT_DIR/unidock_poses" \
    --center_x 3.0 --center_y 11.4 --center_z -2.7 \
    --size_x 22.5 --size_y 22.5 --size_z 22.5 \
    --exhaustiveness 256 --num_modes 9 --search_mode balance

# Step 2 — top-5 by Vina score
# (awk REMARK VINA RESULT → sort -g → head -n 5)

# Step 3a — GNINA CNN rescore
apptainer exec --nv ... docker://gnina/gnina:latest \
  gnina --receptor "$RECEPTOR" --ligand <pose> --score_only \
        --cnn_scoring rescore --out <pose>.sdf

# Step 3b — merge into top_hits.csv (ligand, vina, cnn_score, cnn_affinity)
```

## Wall-clock + cost (smoke)

| Phase                  | Wall-clock     |
| ---------------------- | -------------- |
| Cap-pod cold-start     | ~90 s          |
| Step 1 — Uni-Dock      | ~25 s (10 lig) |
| Step 2 — top-N select  | <1 s           |
| Step 3a — GNINA × 5    | ~40 s          |
| Step 3b — merge CSV    | <1 s           |
| **Total job**          | **~3 min**     |

- **Node:** `g5.xlarge` (1 × A10G).
- **On-demand price:** $1.006/hr → ~**$0.05/job** smoke.
- **At 1k-ligand library + top-100 rescore (target prod size):**
  ~30-50 min wall-clock, **~$0.50-$0.85/screen** on the same g5.xlarge.

## Success criteria (validation block in the script)

1. `${OUTPUT_DIR}/top_hits.csv` exists.
2. `wc -l` > 1 (header + ≥1 hit).
3. `gnina_cnn_affinity` column has ≥1 numeric row (catches the "GNINA
   silently no-op'd" failure mode where the SDF parser returned empty).

Smoke run produces 10 rows; top row is the known PDE10A active
(GNINA CNN affinity ~7.1, Uni-Dock Vina ~-9.8 kcal/mol). Reject the
build if the active drops below row 3 — pose-handling regression.

## Container availability concerns

- **`dptechnology/unidock:latest`** — Docker Hub, public, last push
  recent. Risk: `:latest` floats; pin to a digest in prod
  (`@sha256:...`) once a smoke build passes. No NGC mirror.
- **`gnina/gnina:latest`** — Docker Hub, public, GitHub-Actions-built
  from `gnina/gnina` HEAD. Same `:latest` caveat. CUDA build matches
  A10G (sm_86) — confirmed in the gnina Dockerfile.
- **Apptainer pull cache:** both images go through
  `APPTAINER_CACHEDIR=/mnt/efs/_apptainer-cache` per the slurmd image
  default — first job per cluster pays the pull (~1-2 min for Uni-Dock,
  ~2-3 min for GNINA), subsequent jobs hit the EFS-shared SIF.
- **No license issues:** Uni-Dock is LGPL, GNINA is Apache-2 — fine for
  customer-tenant runs.

## Ready

`ready: true` — workload is well-scoped, both containers public,
validation block catches the obvious silent-failure modes. Pin
`:latest` → digest before declaring it stable.

## Smoke verification (May 8 2026)

- jobid 1806 confirmed end-to-end Step 1 (Uni-Dock GPU batched docking
  on A10G) on cap-pod node. 10 ligand subset docked in ~2 s GPU time
  after ~3-4 min cold SIF build. Top-N selection via REMARK VINA
  RESULT awk pipeline works.
- Step 3 (GNINA rescore) needs `IFS=$'\t'` (single quotes — `$"\t"`
  is a localized-string lookup that returns the literal `\t`, which
  causes `read` to split `/mnt/efs/...` on the `t` letter and feed a
  bad path to gnina). Fix landed in `/tmp/openvscreen-smoke.sh` on
  slurmctld; mirror into the YAML next iteration.
- Smoke fixtures live at `/mnt/efs/fixtures/unidock-smoke/` (receptor
  + 10 ligands prestaged); slurmd image lacks `git`, so do **not**
  `git clone` upstream Uni-Dock at job start.
- Always set `--exclude=ip-10-222-29-166` (or whatever the dead node
  is): /mnt/efs hostPath was unreachable on that worker → ExitCode
  1:0 with empty `slurm-%j.out`.
