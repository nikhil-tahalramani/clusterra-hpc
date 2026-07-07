# GROMACS template — smoke test

## Default smoke (self-contained water box)

NGC's `nvcr.io/hpc/gromacs:2023.2` does NOT ship the
`water_GMX50_bare/water-cut1.0_GMX50_bare/0096` benchmark dataset that the
old water-bench docs reference (verified May 8 2026 — the `template/`
directory is absent from the NGC image's `share/gromacs/`). The smoke
generates a small box on the fly with `gmx solvate`:

1. Empty 3 nm cube → `gmx solvate -cs spc216.gro` ⇒ ~884 TIP3P waters.
2. Minimal `md.mdp` (PME, 2 fs, V-rescale @ 300 K, h-bond constraints).
3. `gmx grompp` → `md.tpr`, then `gmx mdrun` for 2000 steps (4 ps).

Verified end-to-end on clusde74, May 8 2026:

- **Job 1814** — g5.8xlarge spot / 1× A10G, COMPLETED in ~10 s of mdrun
  wall (after first SIF cache hit).
- **Performance: 1297 ns/day** (884 waters, 2000 steps, `-nb gpu -pme gpu
  -ntmpi 1 -ntomp 4 -pin off`).
- `gmx check -e md.edr` → "Found 5 frames with a timestep of 1 ps" (>0
  frames ⇒ pass).
- Outputs in `/mnt/efs/n52h53/gromacs-smoke-1814/`: `md.gro` (183 KB),
  `md.edr` (2.8 KB), `md.log` (24 KB), `md.tpr`, `md.cpt`, `md.xtc`.

Cost: ~$0.05 per smoke run on g5.8xlarge spot once SIF is cached. First
cold-cluster run pays Karpenter provisioning (~2–3 min) + SIF pull
(~700 MB; ~30 s on the EFS-cached path).

### Expected artefacts in `output_dir`

- `md.gro` — final coordinates (must exist and be non-empty)
- `md.edr` — energy file
- `md.log` — mdrun log; grep for `Performance:` to see ns/day (A10G hits
  ~1200–1500 ns/day on the 884-water smoke box; real protein systems
  drop to ~50–150 ns/day with full bonded + LINCS work)
- `gmx-check.log` — `gmx check -e md.edr` output. Validate it reports
  `Found N frames` for N > 0. Zero frames means mdrun aborted before
  the first integrator step (usually a topology / cutoff mismatch).

## Real-input smoke

Use lysozyme-in-water from the GROMACS tutorial:

1. Generate `md.mdp`, `conf.gro`, `topol.top` per the upstream tutorial
   (http://www.mdtutorials.com/gmx/lysozyme/).
2. Submit with `mdp_file`, `conf_gro`, `topol_top` set, `nsteps=50000`
   (100 ps @ 2 fs).
3. Expect ~3–6 min on A10G, ~$0.10 end-to-end. With a real protein you
   can re-enable `-bonded gpu` and `-update gpu` (both rejected on the
   pure-water smoke — see "Known gotchas" below).

## Container notes

- Primary: `nvcr.io/hpc/gromacs:2023.2` — NVIDIA NGC HPC build, public
  anonymous pull, GPU-resident PME compiled in. **This is the latest
  public NGC tag** (the `/v2/hpc/gromacs/tags/list` endpoint, queried
  May 8 2026 with the unauth proxy_auth bearer, returns 11 real tags
  topping out at 2023.2 — `2024.x` does NOT exist on NGC).
- Docker Hub `gromacs/gromacs:*` is also incomplete: latest published
  tag there is `2022.2` (verified via Docker Hub v2 API, May 8 2026).
  Don't fall back to it without testing.
- The image has NO Docker `ENTRYPOINT`, so `apptainer exec ... gmx`
  fails with `gmx: command not found` (apptainer skips Docker
  ENTRYPOINTs by design). Wrapper sources GMXRC explicitly:

      apptainer exec ... bash -c "source /usr/local/gromacs/avx2_256/bin/GMXRC && gmx ..."

  NGC ships 4 SIMD variants under `/usr/local/gromacs/{avx2_256,
  avx_512, avx_256, sse4.1}/bin/gmx`; we use `avx2_256` (works on
  every modern x86 EC2, including g5/g6).
- Cache: SIF lands in `/mnt/efs/_apptainer-cache` (default
  `APPTAINER_CACHEDIR`). Concurrent pulls across nodes are
  NFS-fcntl-locked by apptainer — safe.

## Known gotchas

- `-bonded gpu` is **rejected** by mdrun on a pure-water system
  ("None of the bonded types are implemented on the GPU"). Drop it
  for the smoke; re-enable for protein systems.
- `-update gpu` **segfaults** on the NGC 2023.2 build for the
  884-water smoke (job 1813 crashed at "starting mdrun"). Drop it
  for the smoke; can re-enable for larger / protein systems where
  the GPU update path is exercised more.
- `-ntmpi 1` is intentional — single-GPU runs do not need MPI ranks;
  adding them slows mdrun via duplicate GPU context creation.
- `-pin on` pins OpenMP threads; without it, GROMACS warns and you
  lose ~10–20 % perf on shared-CPU nodes. (The smoke runs `-pin off`
  to avoid `Overriding thread affinity` noise on the smoke node.)
- `GMX_ENABLE_DIRECT_GPU_COMM=1` is a no-op on a 1-GPU run but
  harmless; keep it so the same template works if we add multi-GPU
  later.
- The cap-pod is GPU-tainted (`clusterra.io/gpu=true:NoSchedule`) —
  only GPU-tolerating templates schedule here, so we won't fight CPU
  jobs for the slot.
