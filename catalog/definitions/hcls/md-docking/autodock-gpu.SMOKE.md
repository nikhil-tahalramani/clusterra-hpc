# AutoDock-GPU smoke test

## Recipe (single-ligand 1stp benchmark)

The Scripps repo ships a canonical `input/1stp/` example (streptavidin +
biotin) with the receptor maps pre-baked. Stage it on EFS once:

```bash
mkdir -p /mnt/efs/refs/autodock-gpu/1stp
cd /mnt/efs/refs/autodock-gpu/1stp
curl -sSL -o data.tar.gz \
  https://github.com/ccsb-scripps/AutoDock-GPU/archive/refs/heads/develop.tar.gz
tar -xzf data.tar.gz --strip-components=2 \
  AutoDock-GPU-develop/input/1stp
```

Then submit through the template with:

- `maps_fld` = `/mnt/efs/refs/autodock-gpu/1stp/1stp/derived/1stp_protein.maps.fld`
- `ligand_pdbqt` = `/mnt/efs/refs/autodock-gpu/1stp/1stp/derived/1stp_ligand.pdbqt`
- `ligand_filelist` = (empty)
- `nrun` = `100`

## Rendered sbatch (abridged)

```bash
#SBATCH --job-name=autodock-gpu-n100
#SBATCH --cpus-per-task=4
#SBATCH --mem=16384
#SBATCH --time=30
#SBATCH --gres=gpu:a10g:1
#SBATCH --partition=gpu
#SBATCH --chdir=/mnt/efs

set -euo pipefail
MAPS_FLD='/mnt/efs/refs/autodock-gpu/1stp/.../1stp_protein.maps.fld'
LIGAND_PDBQT='/mnt/efs/refs/autodock-gpu/1stp/.../1stp_ligand.pdbqt'
OUTPUT_DIR=/mnt/efs/n52h53@gmail.com/autodock-gpu-${SLURM_JOB_ID}
mkdir -p "$OUTPUT_DIR"; cd "$OUTPUT_DIR"
SIF=/mnt/efs/refs/autodock-gpu/cuda-devel-12.2.2.sif
BIN=/mnt/efs/refs/autodock-gpu/bin/autodock_gpu_128wi
# First-run only (split because cap-pod can't run %post):
#   apptainer pull $SIF docker://nvidia/cuda:12.2.2-devel-ubuntu22.04
#   curl tarball + apptainer exec --bind /mnt/efs $SIF make DEVICE=CUDA NUMWI=128
#   install bin/autodock_gpu_128wi $BIN
exec apptainer exec --nv --bind /mnt/efs "$SIF" \
  "$BIN" --ffile "$MAPS_FLD" --nrun 100 --gbest 1 --lfile "$LIGAND_PDBQT"
```

## Wall-clock + cost (estimated)

| Config            | nrun | Wall-clock        | Node          | $/hr (on-demand) | Per-job |
| ----------------- | ---- | ----------------- | ------------- | ---------------- | ------- |
| 1stp single       | 100  | ~10–15 s GPU      | g5.xlarge A10G | $1.006          | <$0.01  |
| 1stp single       | 100  | ~3 min wall (incl. Karpenter cold-start + apptainer SIF pull on first run) | g5.xlarge | $1.006 | ~$0.05 |
| Batch 100 ligands | 100  | ~15–25 min        | g5.xlarge A10G | $1.006          | ~$0.30  |

Cap-pod is sized at the nominal A10G g5 family; subsequent jobs that hit
a warm node skip the SIF pull and finish in single-digit seconds.

## Success criteria

- `$OUTPUT_DIR/1stp_ligand.dlg` exists and contains a `Run:` block per
  LGA run (grep `^Run:` should print `nrun` lines).
- `$OUTPUT_DIR/1stp_ligand_best.pdbqt` exists and parses as PDBQT
  (first line `MODEL` or `REMARK`).
- `dlg` reports a final docked energy in the expected –10 to –12 kcal/mol
  range for streptavidin+biotin (the canonical reference is ~–11.4).
- Slurm exit code 0; admin_comment cost stamp present.

## Container availability note

Scripps does **not** publish an official `ccsbscripps/autodock-gpu`
image and **no community image we vetted ships a usable
`autodock_gpu_128wi` binary** as of 2026-05-07:

- `stjude/autodock-gpu` — does not exist on Docker Hub (404 from
  v2 tags API). Job 1688's `apptainer pull` "succeeded" against an
  image that resolves through Docker Hub's library fallback and lands
  with no AutoDock binaries — `which autodock_gpu*` returns nothing.
- `gabinsc/autodock-gpu` — only `1.5.3-CPU` / `1.5.3-intelcpu-opencl`,
  no CUDA build.
- Upstream `ccsb-scripps/AutoDock-GPU` — repo ships only `Makefile`
  + `Makefile.Cuda`; **no Dockerfile, no Apptainer recipe**.

**Resolved approach** (2026-05-07): the obvious "`apptainer build`
inline" path does not work in this cluster. The slurmd cap-pod runs
apptainer 1.4.5 as `slurm` (uid 401) with no `/etc/subuid` mapping
and a user namespace that can't write `/proc/self/setgroups`, so any
`%post` step (even with `--fakeroot --ignore-fakeroot-command`) fails:

```
ERROR  : Could not write info to setgroups: Permission denied
ERROR  : Error while waiting event for user namespace mappings: no event received
FATAL  : While performing build: while running engine:
         while running %post section: exit status 1
```

Verified failure on jobs 1714 (--fakeroot) and 1722
(--fakeroot --ignore-fakeroot-command). The host (slurmd cap-pod
host on the customer worker) has no apptainer/docker either, so we
can't sidestep into a host-level build.

`apptainer pull`, however, doesn't run `%post` and works fine. The
working pattern (verified on job 1730, COMPLETED in ~10 min wall):

1. `apptainer pull /mnt/efs/refs/autodock-gpu/cuda-devel-12.2.2.sif
   docker://nvidia/cuda:12.2.2-devel-ubuntu22.04` (~3 min, 3.7 GiB SIF).
2. `curl` the AutoDock-GPU `develop` tarball (no `git` on cap-pod
   host).
3. `apptainer exec --bind /mnt/efs $SIF bash -c 'cd src && make
   DEVICE=CUDA NUMWI=128 && install bin/autodock_gpu_128wi $BIN'` —
   the CUDA toolkit lives in the SIF, the resulting host binary
   (1.7 MiB ELF) lives on EFS and is invoked at runtime via
   `apptainer exec --nv --bind /mnt/efs $SIF $BIN ...`.

Cached at `/mnt/efs/refs/autodock-gpu/{cuda-devel-12.2.2.sif,
bin/autodock_gpu_128wi}` (~3.9 GiB total, ~5 s startup on warm
nodes). `ready: false` until the 1stp smoke run produces the
expected ~–11.4 kcal/mol.
