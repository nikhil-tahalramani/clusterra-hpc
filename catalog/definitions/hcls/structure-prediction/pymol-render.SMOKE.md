# PyMOL render smoke test

Smallest reproducible smoke: render a single PDB to a single PNG. Cold
path exercises the SIF pull (~190 MB), warm path is "apptainer exec on
cached SIF + 1 ray-trace" (< 60 s).

## Inputs

A small PDB on EFS. The smoke uses crambin (~46 residues) from RCSB
mirrored to `/mnt/efs/refs/test/1crn.pdb` — or any small structure you
have at hand.

## Submit recipe

Via API (per MEMORY `api_jwt_mint_workflow`):

```bash
JWT=$(cat /tmp/clusterra_jwt)
python3 <<'PY' > /tmp/pymol-smoke.json
import json
print(json.dumps({
  "run_name": "pymol-smoke",
  "steps": [{
    "step_id": "main",
    "template_id": "pymol-render",
    "inputs": {"working_directory": "/mnt/efs"},
    "params": {
      "script_body": (
        "import os\n"
        "from pymol import cmd\n"
        "cmd.fetch('1crn', 'crambin', type='pdb', path=os.environ['OUTPUT_DIR'])\n"
        "cmd.hide('everything')\n"
        "cmd.show('cartoon')\n"
        "cmd.color('spectrum')\n"
        "cmd.bg_color('white')\n"
        "cmd.orient()\n"
        "cmd.ray(800, 600)\n"
        "cmd.png(os.path.join(os.environ['OUTPUT_DIR'], 'smoke.png'), dpi=120)\n"
      )
    }
  }]
}))
PY
curl -b "clusterra_session=$JWT" -H 'Content-Type: application/json' \
  -X POST -d @/tmp/pymol-smoke.json \
  https://dev-api.clusterra.cloud/v1/clusters/clusde74/jobs/submit
```

## Expected output

- `OUTPUT_DIR/smoke.png` — an 800×600 rainbow cartoon of crambin.
- `OUTPUT_DIR/script.py` — the materialised PyMOL script (useful for
  reproducing locally with `pymol -cq script.py`).
- `OUTPUT_DIR/stdout.log` — full PyMOL stdout, including the fetch
  status line and the ray-trace timing line.

## Reference: real-world hero render

The production usage that motivated this template — a TCR–pMHC crystal
vs OpenFold3 prediction overlay (7QPJ, DockQ 0.91, Cα RMSD 0.70 Å) — is
embedded in the template's default `script_body`. That render produced
the two hero PNGs in
`marketing/blogs/images/renders/tcr-pmhc/7qpj-hero-*.png` (job 2882,
~$0.03 warm).

## Wall-clock + cost

| Path | Wall-clock | Cost (CPU spot t-class) |
|---|---|---|
| Cold (first ever on cluster) | ~3 min (SIF pull ~2 min + render ~30 s) | ~$0.02 |
| Warm (SIF cached on EFS) | ~30 s (render only) | < $0.01 |

## Known good combinations

| Job ID | Date | Image | Outcome |
|---|---|---|---|
| 2882 | 2026-05-28 | pegi3s/pymol:latest | hero 7QPJ overlay — 1600×1200 + 1400×1000 zoom in ~1m 46s after warmup |

## Failure modes seen and ruled out

- `mamba install pymol-open-source`: solver hangs > 13 min on t-class
  cap-pods (jobs 2877/2879/2880). Use the prebuilt image.
- `docker://quay.io/biocontainers/pymol-open-source`: repo has zero
  tags via Quay v2 API despite the bioconda listing. Don't reach for it.
- `/mnt/scratch/job-$ID` missing on instance-store-less cap-pods: the
  `--no-home --writable-tmpfs` flags in the template's `apptainer exec`
  defend against the prolog gate bug; once that's fixed, the flags
  become harmless no-ops. See `core/images/clusterra-slurmd/prolog.sh:7`
  (gate uses `[ -d ]` instead of `mountpoint -q`).
