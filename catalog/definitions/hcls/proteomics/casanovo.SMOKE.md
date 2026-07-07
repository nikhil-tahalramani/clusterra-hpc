# Casanovo smoke test

End-to-end check that the `casanovo` template renders, dispatches to a GPU
cap-pod, pulls the SIF, runs inference, and writes a valid mzTab.

## 1. Stage the sample MGF on EFS

The Casanovo repo ships ~100 preprocessed spectra. From any pod with EFS:

```bash
mkdir -p /mnt/efs/$USER/casanovo-smoke
curl -sSL -o /mnt/efs/$USER/casanovo-smoke/sample.mgf \
  https://raw.githubusercontent.com/Noble-Lab/casanovo/main/sample_data/sample_preprocessed_spectra.mgf
wc -l /mnt/efs/$USER/casanovo-smoke/sample.mgf  # expect ~few thousand lines
```

## 2. Submit via the launchables API

```bash
curl -sS -X POST https://dev-api.clusterra.cloud/v1/launchables/casanovo/submit \
  -H "Authorization: Bearer $TOKEN" \
  -H 'content-type: application/json' \
  -d '{
    "params": {
      "input_mgf": "/mnt/efs/me@clusterra.io/casanovo-smoke/sample.mgf",
      "mode": "sequence",
      "model_path": "",
      "time_limit": 30
    }
  }'
```

Default `model_path=""` triggers auto-download of Casanovo v5 weights from the
GitHub release on first run (cached under `$TMPDIR/torch` per job — ephemeral).

## 3. Watch dispatch

```bash
JOB=<id>
sacct -j $JOB --format=JobID,State,Elapsed,NodeList,ExitCode
```

Expected timeline on a fresh GPU pool:

| Phase                       | Wall-clock |
| --------------------------- | ---------- |
| Karpenter g5.xlarge provision | ~90 s    |
| K3s join + slurmd register  | ~30 s      |
| SIF pull (ghcr, ~3 GiB)     | 60-120 s   |
| Model weight download       | ~10 s      |
| Inference (100 spectra)     | 30-60 s    |
| **Total cold-start**        | **3-5 min** |
| **Warm cap-pod re-run**     | **~60 s**  |

Cost: g5.xlarge spot ~$0.30/hr → ~$0.025 cold, ~$0.005 warm.

## 4. Validate output

```bash
OUT=/mnt/efs/me@clusterra.io/casanovo-<JOB>
ls -la $OUT
# expect: casanovo.mztab

# mzTab sanity: header + at least 1 PSM row
grep -c '^PSM\b' $OUT/casanovo.mztab    # expect > 0
head -20 $OUT/casanovo.mztab            # MTD lines + PSH header
```

A valid run has:
- `MTD` metadata lines at the top
- `PSH` (PSM header) line listing columns
- One or more `PSM` rows with `sequence`, `search_engine_score`, `charge`, etc.

## 5. Failure modes seen

- **GHCR pull 403**: kubelet credential provider only auths ECR. `ghcr.io` is
  public for casanovo so this should not fire; if it does, check the SIF
  resolves anonymously: `apptainer pull docker://ghcr.io/noble-lab/casanovo:5.0`.
- **Auto-download timeout**: GitHub releases occasionally slow; pin a
  checkpoint on EFS and pass `model_path` for production runs.
- **Empty mzTab on exit-0**: input MGF had no parseable spectra. Inspect the
  log for `Sequenced 0 spectra`.
