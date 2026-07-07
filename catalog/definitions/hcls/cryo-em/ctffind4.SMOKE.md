# CTFFIND4 smoke test

Single-micrograph CTF estimation on the RELION 3 tutorial dataset that is
already staged on `dev-demo` (clusde74) at `/mnt/efs/relion-tutorial/`.
Validates the `host_system` binary stage path + the scripted-stdin invocation
order. CPU only; no apptainer; no GPU.

## 1. SMOKE_MODE (binary stage only)

No inputs — the script stages the upstream tarball + version-prints. Use this
to validate the EFS download path on a fresh cluster.

| field            | value           |
| ---------------- | --------------- |
| template_id      | `ctffind4`      |
| inputs           | `{}`            |
| params           | `{}`            |

Submit:

```bash
curl -b "clusterra_session=$JWT" -X POST \
  https://dev-api.clusterra.cloud/v1/clusters/clusde74/jobs/submit \
  -H 'Content-Type: application/json' -d '{
    "run_name":"ctffind4-smoke",
    "steps":[{"step_id":"main","template_id":"ctffind4","inputs":{},"params":{}}]
  }'
```

Expected stdout:

```
[ctffind4] staging v4.1.14 from grigoriefflab.umassmed.edu...
[ctffind4] binary staged at /mnt/efs/bin/ctffind-v4.1.14
Usage: ctffind-v4.1 [--old-school-input] [--old-school-input-ctffind4] ...
[ctffind4] SMOKE_MODE: binary staged + version-printed.
```

Wall-clock: ~5 s on a warm cap-pod (verified May 27 2026, job 2719 — submit→end
1779901559→1779901564).

## 2. Real CTF estimation (RELION 3 tutorial micrograph)

The RELION 3 tutorial `MotionCorr/job002/Movies/` directory contains 24
aligned-sum micrographs (β-galactosidase, 200 kV, Cs=1.4 mm, native pixel
size ~0.885 Å, dose-weighted by MotionCor2). Pick one for the smoke.

| field          | value                                                                                                                  |
| -------------- | ---------------------------------------------------------------------------------------------------------------------- |
| template_id    | `ctffind4`                                                                                                             |
| micrograph     | `/mnt/efs/relion-tutorial/relion30_tutorial_precalculated_results/MotionCorr/job002/Movies/20170629_00021_frameImage.mrc` |
| pixel_size_a   | `0.885`                                                                                                                |
| voltage_kv     | `200`                                                                                                                  |
| cs_mm          | `1.4`                                                                                                                  |
| amp_contrast   | `0.1`                                                                                                                  |
| box_size       | `512`                                                                                                                  |
| min_res_a      | `30`                                                                                                                   |
| max_res_a      | `5`                                                                                                                    |
| min_defocus_a  | `5000`                                                                                                                 |
| max_defocus_a  | `50000`                                                                                                                |
| defocus_step_a | `500`                                                                                                                  |

```bash
curl -b "clusterra_session=$JWT" -X POST \
  https://dev-api.clusterra.cloud/v1/clusters/clusde74/jobs/submit \
  -H 'Content-Type: application/json' -d '{
    "run_name":"ctffind4-real",
    "steps":[{"step_id":"main","template_id":"ctffind4",
      "inputs":{"micrograph":"/mnt/efs/relion-tutorial/relion30_tutorial_precalculated_results/MotionCorr/job002/Movies/20170629_00021_frameImage.mrc"},
      "params":{"pixel_size_a":0.885,"voltage_kv":200,"cs_mm":1.4,"amp_contrast":0.1,"box_size":512,"min_res_a":30,"max_res_a":5,"min_defocus_a":5000,"max_defocus_a":50000,"defocus_step_a":500}
    }]
  }'
```

## 3. Expected wall-clock + cost

- First call on a cluster: ~5 s (binary stage) + ~3 s (one micrograph fit).
- Steady state: ~3 s per 3710×3838-px micrograph on 4 vCPU (verified job 2730).
- Cost: 4-vCPU cpu cap-pod (`c6i.xlarge` class, ~$0.04/hr us-east-1
  on-demand) → fractions of a cent per micrograph. Whole-tutorial fan-out
  (24 micrographs) ≈ 90 s wall, ≈ $0.001.

## 4. Validation

After completion, `outputs.dir/results.txt` should contain exactly one
tab-separated row per micrograph processed:

```
20170629_00021_frameImage  1.000000  10804.380859  10563.579102  84.650193  0.000000  0.158506  5.120357
```

Columns: `micrograph_name`, `idx`, `defocus_1 [Å]`, `defocus_2 [Å]`,
`astigmatism_angle [°]`, `phase_shift [rad]`, `cross_correlation`,
`max_resolution_fit [Å]`.

Sanity checks:

- Defocus 1 & 2 fall between `min_defocus_a` and `max_defocus_a` (here 5000-50000).
- Defocus 1 ≥ Defocus 2 (CTFFIND4 convention: major axis first).
- Cross-correlation > 0.05 (typical good fit; 0.158 here).
- Max-resolution-fit ≤ Nyquist for the input pixel size (here 5.12 Å vs
  Nyquist=1.77 Å, well within range).

The diagnostic `<basename>_diag.mrc` is the experimental + fitted power
spectrum side-by-side — open in IMOD `3dmod` or RELION to visually verify
Thon rings; the 1D-averaged fit profile is `<basename>_diag_avrot.txt`.

## 5. Verified runs (clusde74, May 27 2026)

| job  | run_name        | state     | wall-clock | notes                                              |
| ---- | --------------- | --------- | ---------- | -------------------------------------------------- |
| 2719 | ctffind4-smoke  | COMPLETED | 5 s        | SMOKE_MODE — first stage of binary to /mnt/efs/bin |
| 2722 | ctffind4-real   | FAILED    | 1 s        | `--old-school-input` is CTFFIND3-compat (5 args on line 3) — wrong mode |
| 2726 | ctffind4-real-v2| FAILED    | 0 s        | scripted mode does NOT prompt for "movie?"         |
| 2730 | ctffind4-real-v3| COMPLETED | 3 s        | Scripted-stdin landed; defocus 10804/10564 Å       |

## 6. Footguns

- **Don't use `--old-school-input`.** That's CTFFIND3 compatibility mode and
  expects `Cs[mm], HT[kV], AmpCnst, XMAG, DStep[um]` packed on one line.
  CTFFIND4's default mode (no flag) is scripted-stdin, one answer per line.
- **Scripted mode skips the "Input is a movie?" prompt.** It only appears
  in interactive TTY mode. Don't add a `no` after the input image.
- **Pixel size in the log may not equal what you passed.** CTFFIND4 4.1.14
  auto-resamples when the input pixel is too small (defaults on). The
  reported "Pixel size for fitting" reflects the resampled grid; the
  defocus + astigmatism values are still in the original-pixel frame.
- **Diagnostic outputs land next to results.** Each micrograph emits a
  `<basename>_diag.mrc`, `<basename>_diag.txt`, and `<basename>_diag_avrot.txt`
  alongside the parsed `results.txt`. Plan storage accordingly when fanning
  out over thousands of micrographs.
- **CTFFIND5 (GPU) is not shipped here.** Janelia GitLab ships CTFFIND5 as
  source-build only (no static binary). Add a separate `ctffind5` template
  once we have a packaged binary or a working apptainer build path.
