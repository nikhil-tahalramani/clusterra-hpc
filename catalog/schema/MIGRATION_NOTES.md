# Template v2 — Schema Notes

Current state: 29 templates authored against v2. The single chain template
(`hcls/chains/bench-giab-gpu-e2e.yaml`) is intentionally pinned to v1 until
the workflow layer ships — see "Chain template" below.

The schema at `template.v2.schema.json` is the source of truth. Both the
sync command and the in-process registry validate against it on every
load.

## Schema additions beyond the original spec

- **`runtime.venv.base_image`** — optional base container for `venv`-kind
  runtimes. `boltz`, `casanovo`, `gpu4pyscf`, and `openmmpbsa` build their
  Python env on top of a published image; without `base_image` the script
  would have to hardcode it outside the runtime contract.
- **`parameters[].label`** — optional human label distinct from
  `description`. Most templates carry both.
- **`outputs.artifacts[].description`** — optional, one-line gloss.
- **`PolicyBlock`** — fully optional; both `prefer_on_demand` and
  `prefer_arm64` default to false.
- **`config:`** — only allowed when `runtime.kind == nextflow`, enforced
  via the schema's `allOf` / `if-then-else`.
- **`routing:`** — only allowed when `kind == interactive`, same pattern.

## Per-template oddities

- **autodock-gpu** — `host_binary` runtime; the renderer aliases
  `.runtime.binary` → `.runtime.binary_path` so authored scripts can use
  the shorter form. Cache convention: `<binary_path>` is the EFS location
  of the compiled binary, sibling SIF cache lives in `.._/cuda-devel-*.sif`.
- **openvscreen** — uses two container images (Uni-Dock + GNINA). The
  schema only models one `runtime.image`; GNINA is pinned as a literal
  in the script body and documented in `runtime.notes`. Cleaner once the
  workflow layer lets `runtime` be a list.
- **openmmpbsa** — uses `venv` with a `mambaorg/micromamba` base and
  conda as the package manager. Fits the existing `venv` contract; a
  dedicated `runtime.kind: conda` would be cleaner if more templates need
  this pattern.
- **bench-na12878-post-sarek** — historically used the v1
  `parent_id`/`static_values` overlay against `parabricks-deepvariant`.
  The parent's fields are inlined; the inheritance signal is preserved
  as a `runtime.notes` pointer.
- **AlphaFold3, Boltz, Cryosparc, RELION, AMBER, autodock-vina, GROMACS,
  openfep** — placeholder scripts that `exit 1` with a "TBD" message;
  resources + runtime metadata are real so the form renders, but
  `ready: false` keeps them out of launchers until the integration work
  lands.
- **Custom Slurm** — `runtime.kind: host_system` with `binary: bash`.
  The user supplies the entire script body via the `sbatch_script`
  parameter; `outputs.artifacts` is empty.
- **Resource ranges** — authored as informed guesses
  (`cpus.max ≈ 4 × default`, `walltime.max ≈ 8 × default` with floors).
  Cluster admins should review per template.
- **Samplesheets** — moved from `parameters` (v1 textarea) to `inputs`
  with `role: samplesheet`. The renderer accepts either a path or inline
  content based on whether the value starts with `/`.

## Chain template

`hcls/chains/bench-giab-gpu-e2e.yaml` stays v1 until `kind: workflow`
is implemented. The registry's `parseRow` rejects any row missing
`kind` or `runtime.kind` (see registry.go) so the chain is excluded
from the sync gate and never lands in DDB. When the workflow layer
ships, this template becomes `kind: workflow` with three child sbatch
scripts as steps and the v1 file is deleted.

## Renderer contract (rewrite map)

All v1 → v2 substitutions are applied at curation time. Notable bits:

- `.params.<data-path>` → `.inputs.<same-key>` (BAM/FASTQ/etc.)
- `.params.image` → `.runtime.image`
- `.params.host_binary` → `.runtime.binary` (autodock-gpu only)
- `.params.time_limit` → `.resources.walltime_min`
- `.params.cpus` / `.cpus_per_task` → `.resources.cpus.default`
- `.params.memory_gb` → `.resources.memory_gb.default` (units shifted
  from MB → GB at the contract layer)
- `.params.tres_per_node` derived from `.resources.gpu`
- `.params.output_dir` → `.outputs.dir`
- `.params.dependency_after_ok` → `.dependencies.after_ok`
- `.params.prefer_on_demand` / `.prefer_arm64` → `.policy.*`
- `.ctx.user_email` / `.cluster_id` / `.run_id` unchanged

`.resources.gpu.type` is exposed as a string when present;
`outputs.artifacts` is artifact-keyed so scripts can address
`{{.outputs.dir}}/foo` directly. `$SLURM_JOB_ID` is only used inside
the script body where the env var is naturally available.
