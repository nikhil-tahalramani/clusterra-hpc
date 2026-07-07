# GPU4PySCF Smoke Test

## Container choice

Investigated four options, picked the slim+wheels approach:

- **(a) `docker://pyscf/gpu4pyscf:latest`** — no such image exists on Docker Hub
  under the official `pyscf` org as of May 2026. Rejected.
- **(b) NVIDIA CUDA 12.0 runtime base + `pip install gpu4pyscf-cuda12x`** —
  rejected after job 1659 hit `pip: command not found` (CUDA runtime has no
  python/pip; rootfs is RO under unprivileged apptainer).
- **(c) `nvcr.io/nvidia/pytorch:24.10-py3`** — rejected after job 1726
  failed venv create (`ensurepip` missing) AND on the broader 25 GB pull
  cost across the cap-pod fleet (see dl_ami_skip_pytorch_base.md).
- **(d) `docker://python:3.11-slim` + apptainer --nv + pip-installed nvidia
  CUDA wheels** — picked, verified May 8 2026 on clusde74 job 1781.
  ~50 MB SIF; the host DL AMI provides the driver layer via `--nv`; the
  CUDA userspace (libnvJitLink, cusolver, cusparse, cublas, cuda-runtime,
  curand, cufft, nvrtc) ships as pip wheels and is preloaded into the
  process via ctypes from `nvidia/<lib>/lib/*.so*` paths. `gpu4pyscf-cuda12x`
  is the official PyPI wheel
  (https://pypi.org/project/gpu4pyscf-cuda12x/) with CUDA kernels prebuilt
  against CUDA 12.x. Persisted at `/mnt/efs/_gpu4pyscf-venv`; subsequent
  jobs short-circuit on `import nvidia.nvjitlink`.

Cost: ~2-3 min cold (SIF pull + pip install) on first job; warm ~5s.
Follow-up: publish prebuilt SIF to `public.ecr.aws/n1h8z3e9/clusterra-gpu4pyscf`.

## Smoke run (zero inputs)

Submit the template with all defaults — `input_py` empty triggers the
inline water B3LYP/def2-svp script.

Expected:
- Wall-clock: ~2-3 min on A10G (SIF pull + pip install + ~10s SCF)
- `output_dir/output.log` contains `converged SCF energy =` (PySCF stdout)
  and `final energy: -76.358...` from the script's print
- Slurm exit 0; cap-pod drains via scaleDownNodes after IDLE

Verified on clusde74 May 8 2026:
- Job 1772: FAILED (`libnvJitLink.so` missing — apptainer `--nv` does not
  mount cuda userspace libs from the host; only driver libs).
- Job 1781: COMPLETED in 02:55, `converged SCF energy = -76.3581418309899`,
  `final energy: -76.35814183098985`. Fix landed in YAML.

## Cost (g5.xlarge, A10G, $1.006/hr on-demand)

- Smoke (water): ~2 min wall → ~$0.034
- Drug-like ~50 atoms, def2-tzvp B3LYP: ~10 min → ~$0.17
- Conformer batch (10 conformers, def2-tzvp): ~1.5h → ~$1.50

Karpenter g5.xlarge spot ~$0.30/hr cuts those by 70%.

## Validation

`output_dir/output.log` must contain the line `converged SCF energy =`
(emitted by PySCF's SCF driver on convergence). Non-converged runs print
`SCF not converged` and exit non-zero — Slurm marks FAILED.

## Known gaps / future

- No prebuilt SIF; pip-install-on-first-run is the cost.
- Multi-GPU not wired (GPU4PySCF supports it but most small-molecule
  workloads saturate one GPU's VRAM before they need scale-out). Add a
  `num_gpus` param + `tres_per_node: gres/gpu:a10g:{{.params.num_gpus}}`
  if/when needed.
- Geometry optimization, NEB, frequencies — same template works (user
  swaps `input_py`); no separate child preset yet.
