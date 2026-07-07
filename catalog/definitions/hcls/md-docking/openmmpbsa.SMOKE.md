# openmmpbsa — smoke test

End-to-end smoke for the `openmmpbsa` template using the upstream gmx_MMPBSA
`Protein_ligand/ST` example (5-frame trajectory, ~3k atoms). Expected
wall-clock: **first run on a fresh tenant ~20–25 min** (12–15 min one-time
conda env build into `/mnt/efs/_gmx-mmpbsa-env/v1.6.4` — 619MB + ambertools
unpacks slowly under EFS contention — plus 5–10 min compute); **subsequent
runs ~5–10 min** once `.ready` is touched. Cost ~$0.30–$0.45 at g5.xlarge.

Verified May 8 2026 on clusde74. Two fixes from the May 7 attempt:
1. `mpi4py` is now installed via conda-forge (pre-built); `gmx_MMPBSA`
   is then `pip install --no-deps`'d, sidestepping mpi4py's setup.py
   trying to invoke `x86_64-conda-linux-gnu-cc` (which conda-forge no
   longer ships in the python=3.10 base).
2. `-cp` is no longer aliased to `-cs` — gmx_MMPBSA 1.6.4 hard-fails on
   duplicated args. The template now exposes a separate `top_file` input
   and only passes `-cp` when given a real .top (or when `topology`
   itself is .top).

## 1. Stage the bundled example onto EFS

From any node with `/mnt/efs` mounted (e.g. a notebook session, or shell into a
slurmd pod):

```bash
mkdir -p /mnt/efs/$USER/mmpbsa-smoke && cd /mnt/efs/$USER/mmpbsa-smoke
git clone --depth 1 https://github.com/Valdes-Tresanco-MS/gmx_MMPBSA.git
cp gmx_MMPBSA/examples/Protein_ligand/ST/com.tpr        ./topology.tpr
cp gmx_MMPBSA/examples/Protein_ligand/ST/com_traj.xtc   ./trajectory.xtc
cp gmx_MMPBSA/examples/Protein_ligand/ST/index.ndx      ./index.ndx
cp -r gmx_MMPBSA/examples/Protein_ligand/ST/topol.top \
      gmx_MMPBSA/examples/Protein_ligand/ST/toppar     ./
ls -lh topology.tpr trajectory.xtc index.ndx topol.top
```

(File names in the upstream example dir may shift across releases; if the
above paths 404, `find gmx_MMPBSA/examples/Protein_ligand -maxdepth 3` to
locate the .tpr/.xtc/.ndx triple.)

## 2. Submit via the launcher (form modality)

Open the `openmmpbsa` preset in the console and fill:

- topology:        `/mnt/efs/<you>/mmpbsa-smoke/topology.tpr`
- trajectory:      `/mnt/efs/<you>/mmpbsa-smoke/trajectory.xtc`
- index:           `/mnt/efs/<you>/mmpbsa-smoke/index.ndx`
- top_file:        `/mnt/efs/<you>/mmpbsa-smoke/topol.top`
- mmpbsa_in:       *(leave empty — uses the inline default)*
- receptor_group:  `receptor`   *(matches the bundled `index.ndx`; verified May 8 2026)*
- ligand_group:    `ligand`
- output_dir:      *(default — `/mnt/efs/<your-email>/mmpbsa-{{$SLURM_JOB_ID}}`)*
- time_limit:      `60`

Submit. Job should land on the gpu partition, get an a10g cap-pod, and start
inside ~3–5 min cold-start.

## 3. Validate

Once Slurm reports COMPLETED:

```bash
JOB=<jobid>
DIR=/mnt/efs/$(whoami)/mmpbsa-${JOB}        # adjust if user_email scoping differs
ls "$DIR"
grep -E 'ΔTOTAL|^TOTAL' "$DIR/FINAL_RESULTS_MMPBSA.dat" | tail -10
```

PASS criteria:

- `FINAL_RESULTS_MMPBSA.dat` exists in `output_dir`.
- File contains both a `GENERALIZED BORN:` section and a `POISSON BOLTZMANN:`
  section, each ending in a `Delta (Complex - Receptor - Ligand)` block with
  a `ΔTOTAL` line. (gmx_MMPBSA 1.6.4 prints the literal Δ glyph, not the
  word `DELTA`.)
- `ΔTOTAL` value is finite and negative (typically -10 to -40 kcal/mol for
  the bundled protein-ligand example). Verified May 8 2026 (job 1783):
  GB ΔTOTAL ≈ -19.4 kcal/mol, PB ΔTOTAL ≈ -15.75 kcal/mol — different
  AmberTools / forcefield versions shift the number a few kcal/mol; sign +
  order-of-magnitude is the smoke check, not exact reproducibility.

FAIL signals worth surfacing:

- `Error: Group "LIG" not found` → the bundled .ndx may use a different ligand
  group name; rerun with `ligand_group=Other` or inspect via `gmx make_ndx`.
- Container pull 403/404 → default image is now
  `docker://mambaorg/micromamba:1.5.10` and gmx_MMPBSA is runtime-installed
  via `micromamba create ... ambertools gromacs && pip install gmx_MMPBSA`
  into `/mnt/efs/_gmx-mmpbsa-env/v1.6.4`. Neither `valdesmsc/gmx_mmpbsa`
  (Docker Hub) nor `quay.io/biocontainers/gmx_mmpbsa` exist on the registry
  (verified 2026-05-07). If conda solve fails, `rm -rf` the env-prefix to
  force a clean rebuild.
- First job on a fresh tenant takes ~5–8 min longer than subsequent jobs —
  that's the one-time env build. Check `/mnt/efs/_gmx-mmpbsa-env/v1.6.4/.ready`
  to see whether the cache is warm.
- Job killed at time_limit → bundled smoke should never hit this; if it does,
  the trajectory or topology was wrong (check the input paths resolved).

## 4. Cost sanity

g5.xlarge on-demand is ~$1.00/hr us-east-1; 15 min ≈ $0.25. Karpenter
consolidation reclaims the node within ~1 min of the cap-pod scaling down.
