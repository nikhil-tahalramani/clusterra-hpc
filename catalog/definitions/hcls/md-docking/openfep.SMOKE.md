# openfep — smoke test

End-to-end smoke for the `openfep` template using a single-edge OpenFE
transformation JSON drawn from the upstream test fixtures (RBFE, ~25k-atom
solvated complex). Expected wall-clock: ~10–30 min on a10g once the
micromamba env is materialized; first run on a fresh tenant adds ~5–10 min
to install `openfe=1.11.1`.

## Why we bypass the published OpenFE images

Verified May 8 2026 on clusde74:

- `ghcr.io/openfreeenergy/openfe:1.11.1-apptainer` is **not** a valid OCI
  image — `apptainer pull/exec` returns FATAL `"no extractable OCI/Docker
  tar layers found in this image"`. The `-apptainer` suffix advertises an
  apptainer-native artifact that the OCI puller can't materialize.
- `ghcr.io/openfreeenergy/openfe:latest` activates conda via Docker
  ENTRYPOINT. `apptainer exec` skips ENTRYPOINT (silent exit 127) — see
  `MEMORY/apptainer_skips_docker_entrypoint.md`.

Resolved by mirroring the openmmpbsa pattern: micromamba base image
(`mambaorg/micromamba:1.5.10`) + flock-guarded one-time `micromamba create`
into a per-version EFS prefix at `/mnt/efs/_openfe-env/v1.11.1`, then
`micromamba run -p $ENV` for every job.

## 1. Stage a tyk2 fixture onto EFS

The OpenFE repo layout has shifted; **don't** hardcode the legacy
`openfe/tests/data/openmm_rfe` path — that no longer ships a runnable
transformation (the only JSON in that tree is `bad_transformation.json`).
Instead, find a real `rbfe.json` under `tests/data` at runtime.

```bash
mkdir -p /mnt/efs/smoke/openfep && cd /mnt/efs/smoke/openfep
apptainer exec docker://alpine/git:latest \
  git clone --depth 1 https://github.com/OpenFreeEnergy/openfe.git openfe-src

# Find a real single-edge transformation JSON.
find openfe-src -type f -name 'rbfe.json'
# Fallback: any JSON with a `"protocol"` block, excluding the bad fixture.
find openfe-src -type f -name '*.json' \
  | xargs grep -l '"protocol"' \
  | grep -vi bad_transformation \
  | head
```

Pick any one path as `INPUT_JSON` for the launcher.

## 2. Submit via the launcher (form modality)

Open the `openfep` preset in the console and fill:

- input_json:    `/mnt/efs/smoke/openfep/openfe-src/<...>/rbfe.json`
- output_dir:    *(default — `/mnt/efs/<your-email>/openfep-{{$SLURM_JOB_ID}}`)*
- time_limit:    `120`

Submit. Job lands on the gpu partition; first run pays the env-build cost
inside the cap-pod (~5–10 min); subsequent runs reuse `/mnt/efs/_openfe-env/v1.11.1`.

## 3. Validate

Once Slurm reports COMPLETED:

```bash
JOB=<jobid>
DIR=/mnt/efs/<your-email>/openfep-${JOB}
ls "$DIR"
python -c "import json; d=json.load(open('$DIR/result.json')); print('estimate:', d.get('estimate'))"
```

PASS criteria:

- `result.json` exists in `output_dir`.
- It parses as JSON and contains an `estimate` field with a numeric value
  (OpenFE returns either a bare float in kcal/mol or a `pint`-style
  `{magnitude, units}` dict — the in-job validator accepts both).
- Magnitude is finite. Exact reproducibility is not the smoke check
  (replica-to-replica scatter is ~0.3 kcal/mol).

FAIL signals worth surfacing:

- `FATAL: no extractable OCI/Docker tar layers found in this image` →
  someone reverted to the `-apptainer` tag; restore the micromamba pattern.
- `exit 127` on container start → image with ENTRYPOINT-activated conda;
  same fix.
- `micromamba: command not found` → wrong base image; must be
  `mambaorg/micromamba:1.5.10` (not a generic miniforge or alpine base).
- `CUDA error: out of memory` → fixture is ~25k atoms which fits easily on
  a10g; OOM means a different (larger) transformation got picked.
- Job killed at time_limit → first run pays env install (~5–10 min); if a
  re-run also times out, suspect a multi-edge JSON got passed in.

## 4. Cost sanity

g5.xlarge on-demand is ~$1.00/hr us-east-1; a single-edge tyk2 quickrun is
typically 15–30 min ≈ $0.25–0.50. Karpenter consolidation reclaims the node
within ~1 min of the cap-pod scaling down. Production RBFE (3 replicates ×
11 lambdas × 5 ns per edge) is typically 8–24 h × N edges — budget
per-campaign, not per-job.
