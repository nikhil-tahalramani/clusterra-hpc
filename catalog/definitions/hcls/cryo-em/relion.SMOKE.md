# RELION smoke test

CPU 2D classification on the upstream RELION 3 tutorial dataset
(β-galactosidase, EMPIAR-10204 raw, precalculated_results tarball from
the MRC LMB pub mirror). Validates EFS I/O, biocontainers apptainer
SIF pull, and the `relion_refine` codepath end-to-end.

The shipped template is **CPU-only** by design — the verified
biocontainers image `quay.io/biocontainers/relion:5.1.0--h4fe8dad_0`
ships no CUDA. For GPU 2D/3D classification, build a SIF from
upstream `3dem/relion` against CUDA and override `runtime.image`.

## 1. Stage tutorial data

Tutorial is `~4.5 GB` compressed, `~5 GB` extracted; one-time per cluster.
The MRC LMB host is FTP-only and `wget` is NOT installed on cap-pods, so
fetch with `curl`:

```bash
mkdir -p /mnt/efs/relion-tutorial && cd /mnt/efs/relion-tutorial
curl -fSL ftp://ftp.mrc-lmb.cam.ac.uk/pub/scheres/relion30_tutorial_precalculated_results.tar.gz \
  -o precalc.tar.gz
tar -xzf precalc.tar.gz
rm precalc.tar.gz
# expects:
ls relion30_tutorial_precalculated_results/Extract/job007/particles.star
ls relion30_tutorial_precalculated_results/Select/job009/particles.star   # post-Class2D-Select set, ~1100 particles
```

Submit via `custom-slurm` with `partition=cpu` to dodge the templates-sync
cron when iterating on the script body itself.

## 2. Submit via Clusterra

Template `relion`, params (mirrors `relion.yaml` defaults):

| field              | value                                                                                                                |
| ------------------ | -------------------------------------------------------------------------------------------------------------------- |
| subcommand         | `relion_refine`                                                                                                      |
| input_star         | `/mnt/efs/relion-tutorial/relion30_tutorial_precalculated_results/Extract/job007/particles.star`                     |
| n_classes_k        | `4`                                                                                                                  |
| pool               | `30`                                                                                                                 |
| iter               | `25`                                                                                                                 |
| particle_diameter  | `200`                                                                                                                |
| time_limit         | `0`                                                                                                                  |
| output_prefix      | `` (empty -> `/mnt/efs/<email>/relion-<run_id>/Class2D/run`)                                                          |

The template `cd`s into the project root (auto-detected from `input_star`)
so the relative `Extract/job007/Movies/*.mrcs` paths in the STAR file
resolve correctly.

## 3. Expected wall-clock + cost

Verified May 27 2026 on clusde74 (job 2733, m4.10xlarge spot, 16 CPU,
32 GB):

- First-job apptainer pull: 333 MB OCI → SIF convert is `~30-60 s` on a
  fresh node (SIF promoted to `/mnt/efs/sifs/relion_5.1.0--h4fe8dad_0.sif`
  so subsequent jobs skip the pull entirely).
- 25-iter 4-class 2D classification on the `~1500` particle
  Extract/job007 set, 16 CPU: **169 s relion_refine wall-clock** on
  spot m4.10xlarge → **~$0.03** of compute per smoke run (plus the
  one-time 30-60 s OCI→SIF amortized across the cluster lifetime).
- For a faster iteration loop, use `iter=5 K=2` (mini smoke, ~60 s).

## 4. Validation

RELION emits ONE stack containing K class slices per iteration, not
K separate files (this differs from older guides that show `class*.mrcs`):

```bash
OUT=/mnt/efs/<email>/relion-<run_id>/Class2D
ls $OUT/run_it025_classes.mrcs   # 1 file, K slices stacked
ls $OUT/run_it025_data.star      # per-particle class assignments
ls $OUT/run_it025_model.star     # K refined references
ls $OUT/run_it025_optimiser.star # checkpoint
ls $OUT/run_it025_sampling.star  # sampling state
tail -n 3 $OUT/run.log           # final "yum!" marker = max-iter loop done
```

Pass criteria:
- slurmstepd exit code `0`
- all five `run_it025_*` files present and non-zero size
- the `*_classes.mrcs` file size scales with K (one slice per class, box-size dependent)
- `run.log` ends with a `yum!` line (RELION 5.x 2D Class-marker — there is
  no `Done!` marker in pure 2D classification; that string only appears in
  `--auto_refine` runs)

## 5. Container caveat

The default `quay.io/biocontainers/relion:5.1.0--h4fe8dad_0` is the
bioconda CPU build. Verified May 27 2026 against the EMPIAR β-gal
tutorial (PROJECT_DIR=`relion30_tutorial_precalculated_results`).

For GPU acceleration on Clusterra cap-pods (DL-AMI ships CUDA +
drivers; `apptainer exec --nv` mounts the host CUDA userspace), build
a SIF from `3dem/relion` against CUDA 11.8+, push to
`/mnt/efs/sifs/relion-5.1.0-cuda.sif`, and override `runtime.image`
to the SIF path. Add `--gres=gpu:1` to `sbatch_override`, add `--gpu`
to the `relion_refine` ARGS, and add `--nv` to the `apptainer exec`
invocation. Cap-pods cannot `apptainer build --fakeroot`
(MEMORY: cap_pod_apptainer_build_blocked.md) — build the SIF on a
workstation with `apptainer build` from a `--fakeroot`-capable host
and `scp` the SIF onto EFS.
