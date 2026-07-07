# AMBER smoke test — alanine dipeptide sander CPU minimization

Goal: end-to-end exercise of the `amber` template on the **AmberTools (free)**
path. Validates apptainer pull, /mnt/efs binding, sander binary, and the
`Final Performance Info:` validation hook — without needing a paid Amber
license or a GPU.

System: alanine dipeptide (ACE-ALA-NME, ~22 atoms). Standard AMBER tutorial
system; runs in <1 min on a single core.

## Inputs

Place under `/mnt/efs/<user>/amber-smoke/`:

- `min.in` — sander control file:
  ```
  Alanine dipeptide minimization
   &cntrl
    imin=1, maxcyc=200, ncyc=100,
    cut=999.0, ntb=0, igb=1,
    ntpr=10,
   /
  ```
  Note: `cut=999.0` (effectively no cutoff) is required for implicit-solvent
  runs (`ntb=0`, `igb>0`) — sander 21 rejects `cut=8.0` here with
  "unreasonably small cut for non-periodic run". For explicit solvent (PME)
  the standard 8–10 Å cutoff applies.
- `complex.prmtop` — generated via `tleap` from ff14SB:
  ```
  source leaprc.protein.ff14SB
  ala = sequence { ACE ALA NME }
  saveamberparm ala complex.prmtop complex.inpcrd
  quit
  ```
- `complex.inpcrd` — produced by the same tleap run.

Run tleap once locally (or via this template with `control_in: tleap.in` and a
custom wrapper) to materialize the prmtop/inpcrd.

## Launch

Form params:
- `mode`: `ambertools`
- `input_dir`: `/mnt/efs/<user>/amber-smoke`
- `control_in`: `min.in`
- `prmtop`: `complex.prmtop`
- `inpcrd`: `complex.inpcrd`
- `time_limit`: `15`

## Expected

- Wall-clock: 30–90s sander runtime + 1–3 min apptainer pull (first run).
- Output: `<output_dir>/min.out` containing `Total time` (wall-clock block) and
  `Maximum number of minimization cycles reached.` (or convergence).
- Cost: <$0.05 (one g5.2xlarge minute, mostly idle).
- Validation hook: `file_contains` on `min.out` for `Total time`
  fires green. (The historical needle `Final Performance Info:` is
  pmemd-only — sander emits `FINAL RESULTS` + the `Total time` timing block.)

## Failure modes seen

- `sander: command not found` → wrong image; ensure `image_ambertools` resolves
  (default `docker://ambermd/ambertools:24`).
- Validation green but `min.out` truncated → NaN explosion; check `min.in`
  cutoffs / igb settings.
- `mode=pmemd-cuda` selected without `image_paid` → exits 2 with a clear
  licensing message. Expected.
