# Cryo-ET tilt-series alignment + reconstruction (AreTomo2) — smoke + validation

Markerless tilt-series alignment + tomogram reconstruction on EMPIAR-10164
`TS_01`. Validates the AreTomo2 binary stage, running it inside the RELION
CUDA-12.6 runtime, and the reconstruction output.

## 1. Inputs

A raw tilt-series stack + tilt angles. On the staged EMPIAR-10164 workspace:

```bash
P=/mnt/efs/relion-empiar10164/Relion-5.0/AlignTiltSeries/job005/external/TS_01
ls $P/TS_01.mrc       # raw tilt stack, 4k x 4k x 41, 2.3 GB
ls $P/TS_01.rawtlt    # 41 tilt angles, one per line
```

## 2. Submit

Template `relion-tomo-align`:

| field        | value                                                                                      |
| ------------ | ------------------------------------------------------------------------------------------ |
| tilt_series  | `/mnt/efs/relion-empiar10164/Relion-5.0/AlignTiltSeries/job005/external/TS_01/TS_01.mrc`    |
| angle_file   | `/mnt/efs/relion-empiar10164/Relion-5.0/AlignTiltSeries/job005/external/TS_01/TS_01.rawtlt` |
| out_bin      | `6`                                                                                         |
| vol_z        | `2000`                                                                                      |
| pixel_size   | `1.35`                                                                                      |
| patches      | `0` (global align; purified sample)                                                         |
| n_gpus       | `1`                                                                                         |

Leave `tilt_series` empty for SMOKE_MODE → prints `AreTomo2 -Help` (no GPU work).

## 3. Validated benchmark + cost (clusde74, job 3061, 2026-06-04)

- **1x T4 spot**: AreTomo2 1.1.2 (Cuda121) markerless alignment of all 41 tilts +
  WBP reconstruction of a 638 x 618 x 332 tomogram → **192 s total**, exit 0.
- Cost: a few cents of spot GPU per tilt series at bin 6.
- Outputs: `tomogram_aretomo.mrc` (523 MB), `tomogram_aretomo.aln` (alignment),
  `tomogram_aretomo_projXY.mrc` (field-of-view projection), `*_Imod/` (IMOD files).
- The immature HIV-1 VLPs are clearly resolved in the reconstruction (see the
  cryo-ET case study figure).

## 4. Load-bearing details

- **AreTomo2 is not in the RELION SIF.** The template stages the prebuilt
  `AreTomo2_1.1.2_Cuda121` binary onto `/mnt/efs/bin/aretomo2` (flock-guarded,
  cached) and runs it via `apptainer exec --nv "$SIF" "$ARE_BIN"` so the CUDA-12
  libraries resolve. `python3 -m zipfile` unpacks the release (no `unzip` on
  cap-pods).
- **CUDA build match**: Cuda121 runs fine under the cap-pod's newer driver
  (forward-compatible). Cuda118 / Cuda120 builds are also in the release zip.
- **GPU gres**: prefer untyped `gpu:1` (typed `gpu:a10g` can hang when g5 spot is
  squeezed and on-demand GPU quota is 0).

## 5. Pass criteria

```bash
OUT=/mnt/efs/<email>/relion-tomo-align-<run_id>
ls $OUT/tomogram_aretomo.mrc $OUT/tomogram_aretomo.aln
```
Pass: AreTomo2 exit 0; the tomogram + `.aln` + projection are written; the log
ends with the per-slice "volume slices saved" + "Total time" lines.
