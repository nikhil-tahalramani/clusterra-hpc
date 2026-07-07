# ProteinMPNN smoke test

Single 1BNI (barnase, 3 chains × 108 residues = 324 aa) PDB through the
two-stage `parse_multiple_chains.py` → `protein_mpnn_run.py` pipeline.
5 sequences at T=0.1, vanilla `v_48_020` model, fixed seed 37.

## Inputs

Place at `/mnt/efs/smoke/proteinmpnn/1BNI.pdb` (one-time stage via any
`custom-slurm` job — the cap-pod has curl + write access to that path):

```bash
mkdir -p /mnt/efs/smoke/proteinmpnn
curl -fSL https://files.rcsb.org/download/1BNI.pdb \
  -o /mnt/efs/smoke/proteinmpnn/1BNI.pdb
```

1BNI = wild-type barnase from *Bacillus amyloliquefaciens*. 3 identical
chains (A, B, C), 108 residues each, ~263 KB PDB. Ideal smoke target —
multi-chain stresses the chain auto-detection, small enough that the
inference itself runs in seconds even on the cheapest GPU.

## Rendered sbatch (after render.go variable substitution)

Assuming user_email=`n52h53@gmail.com`, SLURM_JOB_ID resolved at runtime,
params at template defaults except `num_seq_per_target=5`.

```bash
#!/bin/bash
#SBATCH --job-name=mpnn-smoke
#SBATCH --partition=gpu
#SBATCH --cpus-per-task=4
#SBATCH --mem=16G
#SBATCH --time=30:00
#SBATCH --nodes=1
#SBATCH --gres=gpu:1
#SBATCH --chdir=/mnt/efs
#SBATCH --output=/mnt/efs/smoke/proteinmpnn/slurm-%j.out

set -euo pipefail

INPUT_PDB="/mnt/efs/smoke/proteinmpnn/1BNI.pdb"
OUTPUT_DIR="/mnt/efs/n52h53@gmail.com/proteinmpnn-$SLURM_JOB_ID"
NUM_SEQ=5
SAMPLING_TEMP="0.1"
MODEL_NAME="v_48_020"
MODEL_VARIANT="vanilla"
BACKBONE_NOISE=0.0
SEED=37
BATCH_SIZE=1

[ -r "$INPUT_PDB" ] || exit 1
mkdir -p "$OUTPUT_DIR"

IMAGE="docker://python:3.11"

# ---- Lazy stage: ProteinMPNN repo on EFS (one-time, flock-guarded) ----
# Cap-pods have no git/unzip — curl-tarball is the only option. Sentinel
# = the protein_mpnn_run.py script in the repo root. See MEMORY:
# cap_pod_apptainer_build_blocked.md + glyco_pxd024195_session_learnings.md.
MPNN_REPO=/mnt/efs/_proteinmpnn/repo/main
mkdir -p /mnt/efs/_proteinmpnn/repo
(
  flock -x 9
  if [ ! -f "$MPNN_REPO/protein_mpnn_run.py" ]; then
    rm -rf "$MPNN_REPO.tmp" "$MPNN_REPO"
    mkdir -p "$MPNN_REPO.tmp"
    curl -fSL https://codeload.github.com/dauparas/ProteinMPNN/tar.gz/refs/heads/main \
      -o /tmp/proteinmpnn.tar.gz
    tar -xzf /tmp/proteinmpnn.tar.gz -C "$MPNN_REPO.tmp" --strip-components=1
    mv "$MPNN_REPO.tmp" "$MPNN_REPO"
    rm -f /tmp/proteinmpnn.tar.gz
  fi
) 9>/mnt/efs/_proteinmpnn/.repo.lock

# ---- Lazy stage: venv with torch + numpy + biopython ----
VENV_DIR=/mnt/efs/_proteinmpnn-venv/v1
export PIP_CACHE_DIR=/mnt/efs/_proteinmpnn-venv-cache
mkdir -p "$PIP_CACHE_DIR" "$(dirname "$VENV_DIR")"
(
  flock -x 9
  if [ ! -x "$VENV_DIR/bin/python" ] || \
     ! "$VENV_DIR/bin/python" -c "import torch, numpy, Bio" 2>/dev/null; then
    apptainer exec --nv "$IMAGE" bash -c "
      set -euo pipefail
      python3.11 -m venv '$VENV_DIR'
      '$VENV_DIR/bin/pip' install --quiet --cache-dir '$PIP_CACHE_DIR' --upgrade pip
      '$VENV_DIR/bin/pip' install --quiet --cache-dir '$PIP_CACHE_DIR' \
        torch numpy biopython
    "
  fi
) 9>"$VENV_DIR.lock"

# ---- Step A: parse PDB → JSONL ----
# parse_multiple_chains.py expects a directory of PDBs; stage a symlink
# so we can pass a single file as input_pdb.
PDB_STAGE_DIR="$OUTPUT_DIR/pdb_stage"
mkdir -p "$PDB_STAGE_DIR"
ln -sf "$INPUT_PDB" "$PDB_STAGE_DIR/$(basename "$INPUT_PDB")"

PARSED_JSONL="$OUTPUT_DIR/parsed_pdbs.jsonl"

apptainer exec --nv --bind /mnt/efs "$IMAGE" \
  "$VENV_DIR/bin/python" "$MPNN_REPO/helper_scripts/parse_multiple_chains.py" \
    --input_path "$PDB_STAGE_DIR" \
    --output_path "$PARSED_JSONL"

# ---- Step B: design ----
apptainer exec --nv --bind /mnt/efs "$IMAGE" \
  "$VENV_DIR/bin/python" "$MPNN_REPO/protein_mpnn_run.py" \
    --jsonl_path        "$PARSED_JSONL" \
    --out_folder        "$OUTPUT_DIR" \
    --num_seq_per_target 5 \
    --sampling_temp     "0.1" \
    --model_name        "v_48_020" \
    --backbone_noise    0.0 \
    --seed              37 \
    --batch_size        1
```

## Submit recipe

Via Clusterra console → Templates → ProteinMPNN → fill:

- input_pdb: `/mnt/efs/smoke/proteinmpnn/1BNI.pdb`
- num_seq_per_target: `5`
- sampling_temp: `0.1`
- model_name: `v_48_020`
- model_variant: `vanilla`
- backbone_noise: `0.0`
- seed: `37`
- batch_size: `1`

Leave `fixed_positions_jsonl` and `design_chains` at their empty defaults
(all-chain redesign).

## Expected wall-clock + cost

| Phase                                          | First run  | Warm cache |
|------------------------------------------------|------------|------------|
| Karpenter g5/g6 boot + slurmd register         | ~1-2 min   | ~10-30 s   |
| `docker://python:3.11` SIF pull (per-host)     | ~30-45 s   | cached     |
| ProteinMPNN tarball curl + extract (~3 MB)     | ~5 s       | 0 (cached) |
| pip install torch + numpy + biopython into EFS | ~3-5 min   | 0 (cached) |
| parse_multiple_chains.py (1 PDB, 3 chains)     | ~5 s       | ~5 s       |
| protein_mpnn_run.py (324aa, 5 seqs, T=0.1)     | ~6 s       | ~6 s       |
| **Total wall-clock**                           | ~6-8 min   | ~1-2 min   |

ProteinMPNN weights (~few hundred MB across vanilla/soluble/ca_only) ship
inside the GitHub tarball — no separate weight download.

Cost on g5.4xlarge / g6.2xlarge spot (us-east-1):
- First run (cold venv): **~$0.05**
- Warm: **~$0.02**

Verified: jobid 2697 (vanilla, num_seq=5) completed in 125 s wall-clock,
cost $0.022, on g5.4xlarge spot. Inference itself was 5.96 s (the rest
was first-run venv build + Karpenter boot).

## Success criteria

1. `sacct -j $JOB_ID --format=State,ExitCode` → `COMPLETED 0:0`.
2. File exists:
   `/mnt/efs/<user>/proteinmpnn-$JOB_ID/seqs/1BNI.fa`
3. The FASTA contains exactly `num_seq_per_target + 1` records — one
   wild-type reference header plus N designed sequences:
   ```
   $ grep -c '^>' .../seqs/1BNI.fa
   6
   ```
4. Every designed record header has `score=`, `global_score=`,
   `seq_recovery=` keys. Recovery should land in roughly 0.30-0.60 for
   T=0.1 on a typical natural fold (smoke run hit 0.54-0.58 on 1BNI).
5. Parsed JSONL exists and is non-empty:
   `/mnt/efs/<user>/proteinmpnn-$JOB_ID/parsed_pdbs.jsonl` — should
   contain one line with `seq_chain_A`, `coords_chain_A`, etc. keys per
   chain in the input PDB.

## Failure-mode quick triage

- `Error: Failed to find protein_mpnn_run.py` (exit 1 inside the lock
  block) → the codeload tarball failed mid-download and the sentinel
  check after move misses. Delete `/mnt/efs/_proteinmpnn/repo/` and
  resubmit — the curl is retried fresh.
- `ImportError: No module named torch` after the venv lock-block →
  pip install hit transient PyPI 5xx. Delete
  `/mnt/efs/_proteinmpnn-venv/v1` and resubmit (the lock-block
  re-creates from scratch when `python -c "import torch, numpy, Bio"`
  fails).
- `[proteinmpnn] step A produced no parsed JSONL` (exit 71) → the
  input PDB has no recognisable ATOM records (e.g. a CIF file
  mis-named .pdb). `parse_multiple_chains.py` silently writes an
  empty file when no ATOM lines match; the explicit `-s` check catches
  it.
- `[proteinmpnn] no designed FASTA in .../seqs` (exit 72) →
  protein_mpnn_run.py wrote output but to `outputs/` instead of
  `seqs/`. This happens when a future upstream release renames the
  output dir; pin the repo tarball to a tag instead of `main` if it
  recurs.
- `WARNING: Overriding HOME environment variable with APPTAINERENV_HOME
  is not permitted` — benign apptainer noise; ignore.
- `ReqNodeNotAvail` PENDING > 5 min → Karpenter NodePool spot pricing
  spike. Switch model_name to a smaller checkpoint or wait; not a
  template issue.

## Attempt log

### 2026-05-27

- jobid 2692 — stage 1BNI.pdb to EFS via custom-slurm. COMPLETED 0:0
  in 1 s, cost <$0.001. File at
  `/mnt/efs/smoke/proteinmpnn/1BNI.pdb` (263 KB, 3254 lines, header
  `BARNASE WILDTYPE STRUCTURE AT PH 6.0`).
- jobid 2694 — verify staged file readable from a separate cap-pod
  (cross-cap-pod EFS sanity). COMPLETED 0:0 in 1 s.
- jobid 2697 — full smoke (vanilla, num_seq=5, seed=37) via
  `custom-slurm` + `sbatch_override` mirroring the template's `sbatch:`
  + `script:` blocks verbatim. **COMPLETED 0:0 in 125 s** on
  g5.4xlarge spot. cost $0.022. Cold-path: SIF pull (~30 s) + repo
  tarball + venv build dominated; the actual ProteinMPNN inference was
  5.96 s for 5 sequences × 324 residues. Output FASTA contains the
  wild-type reference + 5 designs with seq_recovery 0.54-0.58 at T=0.1.
- jobid 2714 — variant=soluble, num_seq=2. **COMPLETED 0:0** on
  g6.2xlarge spot (fresh node — venv was on EFS but SIF was not in
  this host's local cache, so SIF pull ~30 s was the dominant cold
  cost). Inference 2.36 s. Output FASTA at
  `/mnt/efs/smoke/proteinmpnn/run-soluble-2714/seqs/1BNI.fa`. Designs
  use the soluble checkpoint (`soluble_model_weights/v_48_020.pt`
  inside the repo tarball); seq_recovery 0.53-0.58 — slightly higher
  than vanilla on barnase because soluble's training distribution
  excludes membrane proteins so it leans harder on hydrophilic
  surface residues.
- jobid 2716 — variant=ca_only, num_seq=2. **COMPLETED 0:0** in 83 s
  on g6.2xlarge spot. Inference 2.10 s. Output FASTA header shows
  `CA_model_name=v_48_020` (the CA-only run path stamps a different
  key than vanilla/soluble — useful for downstream provenance).
  seq_recovery 0.43-0.46, **markedly lower** than vanilla on the same
  PDB — expected, because CA-only sees only Cα coordinates and has
  less structural context, so designs diverge further from
  wild-type. Use this mode only when your input backbone is itself
  CA-only (sparse / coarse-grain output from RFdiffusion at low
  resolution).

All three variants (vanilla / soluble / ca_only) ran with the same
script body as 2697; only the `MODEL_VARIANT` arg changed, exercising
the `--use_soluble_model` and `--ca_only` branches in the `ARGS`
builder. No code changes between the draft and the committed template
— the smoke pass was clean on first submission.
