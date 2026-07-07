# Uni-Dock smoke test

GPU virtual screening on the bundled `example/screening_test` fixture from the
Uni-Dock repo (10 ligands against a 1iep-like receptor). Designed to round-trip
in under 5 minutes wall-clock on a single A10G — the cheapest signal that
container, CUDA bind, scoring, and output writeback all work.

## Container availability

`dptechnology/unidock:latest` is published on Docker Hub
(https://hub.docker.com/r/dptechnology/unidock) by the Uni-Dock maintainers.
Apptainer pulls it via the standard `docker://` URI on first use and caches
the SIF in `/mnt/efs/_apptainer-cache` (NFS fcntl-locked across nodes).

If the Docker Hub image is missing or the org rotates names, the fallback is
to build from source inside Apptainer:

```bash
apptainer build /mnt/efs/_apptainer-cache/unidock.sif \
  docker://nvidia/cuda:12.2.0-devel-ubuntu22.04
# then inside: git clone https://github.com/dptech-corp/Uni-Dock && cmake --build
```

…and swap the template's `image` param to a SIF path. (Not done by default —
keep `dptechnology/unidock:latest` until it provably breaks.)

## Fixture prep (auto-staged; documented for reference)

The job script self-stages on first run. Manual equivalent:

```bash
curl -sSfL https://github.com/dptech-corp/Uni-Dock/archive/refs/heads/main.tar.gz \
  | tar xz -C /tmp 'Uni-Dock-main/unidock/example/screening_test/indata/*'
SRC=/tmp/Uni-Dock-main/unidock/example/screening_test/indata
mkdir -p /mnt/efs/fixtures/unidock-smoke/ligands
cp "$SRC/def.pdbqt" /mnt/efs/fixtures/unidock-smoke/receptor.pdbqt
# def.tar.bz2 holds 5,799 ligands; smoke uses the first 10
tar tjf "$SRC/def.tar.bz2" | grep '\.pdbqt$' | head -10 \
  | xargs tar xjf "$SRC/def.tar.bz2" -C /tmp
cp /tmp/def_unique_charged/*.pdbqt /mnt/efs/fixtures/unidock-smoke/ligands/
ls /mnt/efs/fixtures/unidock-smoke/
# expect: receptor.pdbqt  ligands/
```

Box geometry comes from `unidock/example/screening_test/config_def.json`:
center = (-36.0095, 25.6285, 67.4920), size = (17.201, 14.375, 12.240),
scoring = vina.

## Submit recipe

POST `/v1/launchables/unidock/render` (or via the console form) with:

```json
{
  "receptor_pdbqt": "/mnt/efs/fixtures/unidock-smoke/receptor.pdbqt",
  "ligand_dir":     "/mnt/efs/fixtures/unidock-smoke/ligands",
  "center_x": -36.0095, "center_y": 25.6285, "center_z": 67.4920,
  "size_x": 17.201, "size_y": 14.375, "size_z": 12.240,
  "search_mode": "balance",
  "scoring": "vina"
}
```

## Rendered sbatch (fully expanded)

```bash
#!/bin/bash
#SBATCH --job-name=unidock-balance
#SBATCH --cpus-per-task=8
#SBATCH --mem=32768
#SBATCH --time=60
#SBATCH --gres=gpu:a10g:1
#SBATCH --partition=gpu
#SBATCH --chdir=/mnt/efs

set -euo pipefail

RECEPTOR="/mnt/efs/fixtures/unidock-smoke/receptor.pdbqt"
LIGAND_DIR="/mnt/efs/fixtures/unidock-smoke/ligands"
OUTPUT_DIR="/mnt/efs/n52h53@gmail.com/unidock-${SLURM_JOB_ID}"

[ -r "$RECEPTOR" ] || { echo "receptor not readable: $RECEPTOR" >&2; exit 1; }
[ -d "$LIGAND_DIR" ] || { echo "ligand_dir not a directory: $LIGAND_DIR" >&2; exit 1; }
shopt -s nullglob
LIGANDS=( "$LIGAND_DIR"/*.pdbqt )
shopt -u nullglob
if [ "${#LIGANDS[@]}" -eq 0 ]; then
  echo "no .pdbqt ligands found under $LIGAND_DIR" >&2
  exit 1
fi
mkdir -p "$OUTPUT_DIR"
echo "uni-dock: ${#LIGANDS[@]} ligands -> $OUTPUT_DIR"

exec apptainer exec --nv --bind "$TMPDIR:$TMPDIR" \
  "docker://dptechnology/unidock:latest" \
  unidock \
    --receptor "$RECEPTOR" \
    --gpu_batch "${LIGANDS[@]}" \
    --center_x -36.0095 --center_y 25.6285 --center_z 67.4920 \
    --size_x 17.201 --size_y 14.375 --size_z 12.240 \
    --search_mode "balance" \
    --scoring "vina" \
    --dir "$OUTPUT_DIR"
```

## Expected wall-clock + cost

| Phase                          | Time         | Notes                         |
|-------------------------------|--------------|--------------------------------|
| Karpenter g5.2xlarge boot      | ~75-110 s    | first-of-kind shape on cluster |
| Apptainer SIF pull (first run) | ~60-120 s    | cached on EFS thereafter       |
| Uni-Dock kernel (10 ligands)   | 5-15 s       | balance mode, single A10G      |
| Total wall-clock (cold)        | **~3-4 min** |                                |
| Total wall-clock (warm SIF)    | **~90 s**    |                                |

Cost: g5.2xlarge on-demand = $1.212/hr → cold smoke ~$0.07, warm ~$0.03.
Cap-pod stays anchored for the strategy-v6 idle window so a follow-up real
screening run skips the boot tax.

## Success criteria

1. `sacct -j $JOB COMPLETED` exits 0:0.
2. `ls $OUTPUT_DIR | wc -l` == 10 (one `<ligand>_out.pdbqt` per input).
3. `grep -c 'REMARK VINA RESULT' $OUTPUT_DIR/*.pdbqt` == 10
   (each output has at least one scored pose).
4. Top-pose scores are in the chemically-sane range for the fixture
   (Vina kcal/mol typically -6 to -11 for the 1iep-like demo).
5. `nvidia-smi --query-gpu=utilization.gpu --format=csv` sampled mid-run
   shows >50% GPU utilization for at least one tick (sanity-checks `--nv`
   binding actually reached the kernel — a CPU fallback would silently
   complete in ~30 s with 0% GPU util).

## Negative test (recommended, not run by default)

Submit with `ligand_dir=/mnt/efs/empty`. Job should fail fast with
`no .pdbqt ligands found` and exit 1 inside the first second — confirms the
input-validation guard rather than burning a 60 min time-limit on an empty
glob.
