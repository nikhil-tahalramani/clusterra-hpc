# MotionCor3 smoke test

Single-GPU motion correction on a synthetic multi-frame MRC stack. Validates
the host-binary runtime (CUDA devel SIF + libtiff vendoring + on-EFS compiled
binary), the apptainer `--nv` GPU bind, and the full alignment + dose-weight +
CTF estimate pipeline. ~5 s compute on a T4 once the binary is cached.

## 1. Stage tutorial data

MotionCor3 hard-requires a real multi-frame movie — empty input dirs SIGFPE
the ingestor, and no-arg invocations exit 2 via the `mCheckSame` guard. The
template draws an exit 1 with a clear error if movie_path/movie_dir are both
empty rather than firing MotionCor3 against junk. To smoke-test, stage a
small synthetic stack:

```bash
# Submit via custom-slurm with job_name=stage-mc3-mode1, script_body:
set -euxo pipefail
SIF=/mnt/efs/_sif/python311.sif
mkdir -p /mnt/efs/_sif /mnt/efs/_motioncor3-testdata
if [ ! -s "$SIF" ]; then apptainer pull --force "$SIF" docker://python:3.11-slim; fi
apptainer exec --bind /mnt/efs "$SIF" bash -c 'pip install --target=/tmp/pyx mrcfile numpy >/dev/null 2>&1; PYTHONPATH=/tmp/pyx python3 -c "
import mrcfile, numpy as np
rng = np.random.default_rng(42)
base = (rng.poisson(50, (512,512))).astype(np.int16)
frames = []
for i in range(8):
  s = np.roll(np.roll(base, i, axis=0), i, axis=1) + rng.poisson(2,(512,512)).astype(np.int16)
  frames.append(s.astype(np.int16))
stack = np.stack(frames)
with mrcfile.new(\"/mnt/efs/_motioncor3-testdata/test_movie_mode1.mrc\", overwrite=True) as m:
  m.set_data(stack)
  m.voxel_size = 1.0
"'
```

This writes a 4 MB **int16 (MRC mode 1)** stack — 8 frames × 512 × 512 with
small per-frame drift. Mode 1 is mandatory: float32 (mode 2) SIGFPEs the
MotionCor3 ingestor (verified job 2850 / clusde74).

## 2. Submit via Clusterra

Template `motioncor3`, inputs + params:

| field           | value                                                    |
| --------------- | -------------------------------------------------------- |
| movie_path      | `/mnt/efs/_motioncor3-testdata/test_movie_mode1.mrc`     |
| movie_dir       | _(empty)_                                                |
| gain_reference  | _(empty)_                                                |
| movie_suffix    | `mrc`                                                    |
| pixel_size      | `1.0`                                                    |
| dose_per_frame  | `1.0`                                                    |
| patch_x         | `5`                                                      |
| patch_y         | `5`                                                      |
| bft             | `500`                                                    |
| fmref           | `0`                                                      |

## 3. Expected wall-clock + cost

- First-build (one-time per cluster):
  - Pull CUDA devel SIF (~3.6 GB) → ~3 min.
  - Vendor libtiff 4.6.0 → ~2 min.
  - Compile MotionCor3 v1.0.1 → ~1 min.
  - Cached at `/mnt/efs/_motioncor3-cache/{cuda-devel-12.2.2.sif,v1.0.1/MotionCor3,deps/lib/libtiff.so}`.
- Steady-state (cache hit):
  - MotionCor3 alignment + dose-weight + CTF on 8×512×512 → **~5 s** on T4.
  - Karpenter cold-start to first running job: ~3–5 min (job sits PENDING).
  - Cap-pod-warm steady state: ~30 s submit → done.
- Cost: t4 g4dn.xlarge spot ~$0.16/hr → < $0.01 per smoke run. Cost stamp
  lags 20 s so very-short jobs (< 20 s elapsed) won't have admin_comment.
- Verified end-to-end on clusde74 / 2026-05-28 / job 2863: 6 s elapsed,
  state COMPLETED.

## 4. Validation

```bash
OUT=/mnt/efs/n52h53@gmail.com/motioncor3-<run_id>
ls "$OUT" | sort
# Expect (5 files):
# aligned_.mrc      <- motion-corrected sum
# aligned__DW.mrc   <- dose-weighted
# aligned__DWS.mrc  <- dose-weighted sharpened
# aligned__Ctf.mrc  <- CTF estimate image
# aligned__Ctf.txt  <- CTF parameters
cat "$OUT/aligned__Ctf.txt"
# Expect 1 line per frame with df_max/df_min/azimuth/phase/score.
```

Exit code `0` from slurmstepd + all 5 outputs above = pass.

## 5. Gotchas / why this template took 12 iterations

Captured live on clusde74:

1. **Empty input dir SIGFPEs** (`mc3-emptyin-test`, job 2838). MotionCor3's
   ingestor doesn't tolerate `-InMrc emptydir/`. The Wave 2 author's
   "SMOKE_MODE on empty dir" comment is *wrong* — it crashes with exit 136.
2. **No-arg invocation exits 2**, not 0 (job 2845). Triggers the `mCheckSame`
   guard ("input and output files are the same"). Useful for printing the
   arg-default table, but not for an exit-0 smoke.
3. **Trailing-slash `-OutMrc`** treats the path as a literal filename and
   fails `mCheckSave` (job 2840). Always pass a prefix like `dir/aligned_`,
   never `dir/`.
4. **`-InMrc /singletondir/ -InSuffix .mrc` SIGFPEs** even when the dir
   contains exactly one good file (job 2862). The dir-walker assumes >1
   file. Always use `-InMrc <file>` (no `-InSuffix`) for single-movie mode;
   reserve `-InMrc <dir>/ -InSuffix .mrc` for batch.
5. **Float32 MRC (mode 2) SIGFPEs the ingestor** (job 2850). MotionCor3
   wants int16 (mode 1) which is what K2/K3 cameras produce. Document this
   in any test-data generator.
6. **TIFF input requires EM-camera tags**, not generic tifffile output
   (job 2848, "Cannot read TIFF header"). Use raw `.mrc` (int16) for
   smoke; .eer / .tif paths need Falcon4 / Gatan-style headers.
7. **`gpu:a10g:1` typed-GRES pins g5 family**; us-east-1 dev-demo cluster
   has only T4 (g4dn). Use untyped `--gres=gpu:1` so the scheduler accepts
   any GPU. MotionCor3 is built for SM_70..SM_90 so T4 (SM_75) through
   H100 all work.
