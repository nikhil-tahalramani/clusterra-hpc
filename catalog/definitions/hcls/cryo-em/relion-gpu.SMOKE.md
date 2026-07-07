# RELION GPU smoke test

GPU 3D classification on EMPIAR-10028 (Plasmodium falciparum 80S ribosome,
Wong et al. 2014) — the dataset behind the published case study. Validates
EFS I/O, the ~11.7 GB `jidaniel/relion:5.0-cuda12.6` SIF pull, the
local-mpirun launch (`relion_refine_mpi` under `mpirun --mca plm ^slurm`),
the `--gpu` codepath, NVMe `--scratch_dir`, and `--continue` spot-resume.

Unlike the CPU `relion` template, this one IS GPU. The DL-AMI on cap-pods
ships CUDA + drivers; `apptainer exec --nv` mounts the host CUDA userspace,
so no in-image driver is needed.

## 1. Stage the dataset

EMPIAR-10028 extracted particles + a starting 3D reference, staged once per
cluster under `/mnt/efs/relion-empiar10028/`. Expected project layout
(auto-detected from `input_star`):

```bash
ls /mnt/efs/relion-empiar10028/Extract/job007/particles.star   # extracted particles
ls /mnt/efs/relion-empiar10028/ref/emd_2660_lp40.mrc           # starting map (low-pass 40A)
```

The template `cd`s into the auto-detected project root so the relative
`.mrcs` references in the STAR file resolve.

## 2. Submit via Clusterra

Template `relion-gpu`. 3D classification params (the case-study run):

| field              | value                                                                      |
| ------------------ | -------------------------------------------------------------------------- |
| job_type           | `class3d`                                                                  |
| input_star         | `/mnt/efs/relion-empiar10028/Extract/job007/particles.star`               |
| reference_map      | `/mnt/efs/relion-empiar10028/ref/emd_2660_lp40.mrc`                        |
| n_gpus             | `1`                                                                        |
| gpu_model          | `a10g`                                                                     |
| cpus_per_task      | `3`                                                                        |
| mem_gb             | `14`                                                                       |
| threads            | `3`                                                                        |
| n_classes_k        | `6`                                                                        |
| pool               | `30`                                                                       |
| iter               | `25`                                                                       |
| particle_diameter  | `360`                                                                      |
| output_prefix      | `` (empty -> `/mnt/efs/<email>/relion-gpu-<run_id>/Class3D/run`)           |

For a fast plumbing smoke use `job_type=class2d`, `iter=5`, `n_classes_k=2`,
no `reference_map` (~1–2 min once the SIF is cached).

## 3. Validated benchmark + cost

Verified Jun 2 2026 on clusde74:

- **Job 2983** (as-run, g5.4xlarge spot, 16 vCPU / 64 GB, 1× A10G):
  `class3d` K=6, 25 iter, EMPIAR-10028 → **~3 h 18 m** `relion_refine_mpi`
  wall-clock → **~$4** of spot compute. Oversized node (cpus/mem far above
  what a GPU-bound single-GPU run needs).
- **Job 2986** (right-sized, g5.xlarge spot, 4 vCPU / 16 GB, 1× A10G):
  same recipe with `cpus_per_task=3`, `mem_gb=14` so the allocation fits
  g5.xlarge allocatable (~3.45 vCPU). Same ~3 h class3d wall-clock at
  **~$1.65** spot — the template default.
- First-job SIF pull: ~11.7 GB OCI → SIF convert, flock-guarded, promoted
  to `/mnt/efs/sifs/relion_5.0-cuda12.6.sif` so later jobs skip it.

`cpus_per_task=4` does NOT fit g5.xlarge — it bumps Karpenter to the next
shape and erases the right-sizing. Keep cpus <= instance_vCPU - ~1.

## 4. The local-mpirun launch (why it's needed)

`relion_refine_mpi` needs >=2 ranks (1 master + N GPU workers). For 1 GPU we
still run `mpirun -np 2`. The flags:

```
mpirun --mca plm ^slurm --mca ras ^slurm --oversubscribe --bind-to none -np $NRANK \
  relion_refine_mpi --gpu --j $THREADS --scratch_dir $TMPDIR/relion_scratch ...
```

- `--mca plm ^slurm --mca ras ^slurm` — stops OpenMPI using Slurm's
  (unconfigured) PMI launcher; ranks fork locally inside the pod.
- `--bind-to none` — lets RELION's `--j` threads spread across the cores.
- `apptainer exec --nv --bind "$TMPDIR:$TMPDIR"` — `--bind` is REQUIRED or
  OpenMPI's ORTE session dir under `/mnt/scratch` fails with
  `orte_session_dir failed` (it isn't in the image's default bind list).
- `--scratch_dir "$TMPDIR/relion_scratch"` — load-bearing; RELION copies
  particles off EFS to NVMe once.

Multi-GPU: set `n_gpus=4` (ranks = 5), `gpu_model=a10g`, and raise
`cpus_per_task` / `mem_gb` to a g5.12xlarge-class shape. RELION GPU scaling
is roughly flat past 4 GPUs for small-lab particle counts.

## 5. `--continue` spot-resume

`--requeue` is set in the sbatch block. On a spot reclaim Slurm requeues the
job; at start the script scans the output dir for the highest-numbered
`run_it*_optimiser.star`:

- **FRESH path** (no checkpoint): starts from iteration 0 with full args.
- **RESUME path** (checkpoint found): runs
  `relion_refine_mpi --continue <that optimiser.star> --o <same prefix> --iter N --gpu --j T --scratch_dir ...`
  — RELION reads the rest of the run parameters from the optimiser, so the
  job resumes from the last checkpointed iteration instead of restarting.

The chosen path is logged (`[relion-gpu] RESUME path:` / `FRESH path:`).

## 6. Validation / pass criteria

```bash
OUT=/mnt/efs/<email>/relion-gpu-<run_id>/Class3D
ls $OUT/run_it025_data.star       # per-particle assignments
ls $OUT/run_it025_model.star      # K refined refs + per-class FSC
ls $OUT/run_it025_class00?.mrc    # K 3D class maps (class3d) — refine3d emits run_class001.mrc
tail -n 3 $OUT/run.log
```

Pass:
- slurmstepd exit code `0`
- final-iteration `run_it{NNN}_data.star` + `_model.star` present, non-zero
- K class maps present (`class3d`) / `run_class001.mrc` (`refine3d`)
- `run.log` reaches the final iteration (no MPI abort, no
  `orte_session_dir failed`)
- a `RESUME path` / `FRESH path` line is logged at start
