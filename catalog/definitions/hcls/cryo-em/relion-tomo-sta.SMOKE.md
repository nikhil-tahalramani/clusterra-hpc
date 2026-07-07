# RELION cryo-ET subtomogram averaging — smoke + validation

Gold-standard subtomogram averaging on EMPIAR-10164 (immature HIV-1 Gag
VLPs) — the dataset behind the cryo-ET case study. Validates the SIF cache,
`relion_tomo_subtomo` extraction, the tomo `--ios` refine path, the local
mpirun launch, and `relion_postprocess`.

## 1. Stage the dataset

The RELION-5 tomo tutorial workspace (Zenodo DOI 10.5281/zenodo.11068319,
`relion-5-sta-results.tar.gz`, 29.5 GB) staged once per cluster under
`/mnt/efs/relion-empiar10164/Relion-5.0/`. The archive ships tomograms,
masks, references, and metadata but **strips the heavy `.mrcs` subtomogram
stacks** (regenerable) — which is why `do_extract=yes` exists.

```bash
ls /mnt/efs/relion-empiar10164/Relion-5.0/Denoise/job008/tomograms.star      # tomograms
ls /mnt/efs/relion-empiar10164/Relion-5.0/Select/job018/particles.star       # 9442 picks
ls /mnt/efs/relion-empiar10164/Relion-5.0/Reconstruct/bin1reference/half1.mrc # box-192 reference
ls /mnt/efs/relion-empiar10164/Relion-5.0/mask_align.mrc                       # solvent mask
```

## 2. Submit (the case-study run)

Template `relion-tomo-sta`, regenerate-then-refine:

| field             | value                                                                         |
| ----------------- | ----------------------------------------------------------------------------- |
| do_extract        | `yes`                                                                          |
| tomograms_star    | `/mnt/efs/relion-empiar10164/Relion-5.0/Denoise/job008/tomograms.star`        |
| particles_star    | `/mnt/efs/relion-empiar10164/Relion-5.0/Select/job018/particles.star`         |
| reference_map     | `/mnt/efs/relion-empiar10164/Relion-5.0/Reconstruct/bin1reference/half1.mrc`  |
| solvent_mask      | `/mnt/efs/relion-empiar10164/Relion-5.0/mask_align.mrc`                        |
| box / crop / bin  | `512` / `192` / `1`                                                            |
| sym               | `C6`                                                                          |
| particle_diameter | `230`                                                                         |
| ini_high          | `5.5`                                                                         |
| n_gpus            | `4`                                                                           |

For a fast plumbing smoke leave `optimisation_set` + `particles_star` empty
→ SMOKE_MODE prints `relion_refine --version` (no GPU, ~$0).

## 3. Validated benchmark + cost (clusde74, 2026-06-03/04)

- **Extract** (job 3050, CPU 16-core): `relion_tomo_subtomo_mpi` b512/crop192/bin1,
  9442 particles → 11 GB of stacks, **~7 min**.
- **Refine** (job 3053, 4x T4 g4dn.12xlarge spot): gold-standard auto-refine, C6,
  **17 iterations → 4.32 A** (refine) / **3.99 A** (postprocess, B-factor -101.6),
  **~1.6 h, ~$2.30 spot** ($1.35/hr).
- **Single-GPU contrast** (job 3051, 1x T4): ~26 min/iter, timed out at iter 11 in
  the 5 h cap → use multi-GPU. 4x T4 = ~4.6 min/iter (**5.6x**).

## 4. Load-bearing details

- **Gold-standard refine needs `-np >= 3`** (1 leader + 2 half-set workers). The
  template forces `NRANK >= 3` even for 1 GPU. `-np 2` fails with
  `at least 3 MPI processes are required when splitting data into random halves`.
- **Tomo refine uses `--ios <optimisation_set.star>`**, not `--i <particles>`.
- **`--bind "$TMPDIR:$TMPDIR"`** is required or OpenMPI's ORTE session dir fails.
- **GPU gres**: prefer untyped `gpu:N` — typed `gpu:a10g` can hang PENDING when
  g5 spot is squeezed and the account's on-demand GPU quota is 0 (no fallback).

## 5. Pass criteria

```bash
OUT=/mnt/efs/<email>/relion-tomo-sta-<run_id>
ls $OUT/Refine3D/run_class001.mrc            # final average
ls $OUT/PostProcess/postprocess.star         # FSC + resolution
grep FINAL $OUT/PostProcess/postprocess.star # FINAL RESOLUTION line
```
Pass: exit 0; `run_class001.mrc` present; `postprocess.star` reports a real
resolution; `run.log` reaches convergence (no `orte_session_dir failed`, no MPI abort).
