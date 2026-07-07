# Clusterra Template v2 Contract

Source-of-truth reference. Frozen — backend renderer, console launcher, and the
agent all read from this. Schema lives at `template.v2.schema.json` (Draft
2020-12, top-level `additionalProperties: false`).

## Top-level shape

```yaml
id: <kebab-case>                # required
name: <string>                  # required
description: <string>           # required
vertical: <string>              # required (e.g. hcls, general)
category: <string>              # required (e.g. variant-calling)
workload_group: <string>        # required (e.g. genomics)
modality: <string>              # required (form, cli, vendor-gui, notebook)
tools: [<string>, ...]          # required
ready: <bool>                   # required — false = preview-only
kind: batch | interactive | workflow   # required
runtime: { kind: ..., ... }     # required (tagged union — see below)
resources: { cpus, memory_gb, walltime_min, partition_hint, ... }
inputs:    [ InputDecl, ... ]   # required (may be [])
parameters:[ ParamDecl, ... ]   # required (may be [])
outputs:   { dir, artifacts }   # required when kind in {batch, workflow}
script: |                       # required when kind in {batch, workflow}
  ...
routing:   { path_prefix, port_param, ... }   # required when kind=interactive
policy:    { prefer_on_demand, prefer_arm64 } # optional
dependencies: { after_ok }      # optional
cost_anchor: <free-form>        # optional
validation: { ... }             # optional
config: |                       # optional, only when runtime.kind=nextflow
  ...nextflow.config body...
```

Unknown top-level keys are rejected. v1 fields (`job:`, top-level
`parameters` with `type:`/`label:`/`hidden:`, `parent_id:`,
`static_values:`, `schema_values:`) are not part of v2.

> Note: `parent_id` / `static_values` template inheritance was a v1 mechanism.
> v2 templates are flat, single-file. The console-side template registry
> may layer presets at runtime, but no v2 template file uses inheritance.

## `kind`

- **batch** — one Slurm sbatch produced from `script`. Default for nearly
  every HCLS template.
- **interactive** — long-lived session (Jupyter, Node-RED, vendor GUI).
  Requires `routing`. `outputs` and `script` are optional but typical.
- **workflow** — multi-step DAG. Currently parked for the workflow layer;
  the GIAB chain template stays v1 until that lands.

## `runtime` — tagged union on `runtime.kind`

### `docker_image`
The default for any template that runs `apptainer exec docker://<image>`.

```yaml
runtime:
  kind: docker_image
  image: nvcr.io/nvidia/clara/clara-parabricks:4.6.0-1
  registry_auth: ghcr   # optional
  notes: |              # optional, markdown — author warnings, paid alts
    Pinned to 4.6.0-1; 4.5.1 only for ONT (see deepvariant-ont).
```

### `host_binary`
For tools that have no usable upstream image — we pull a build SIF, compile a
host binary onto EFS once, then exec it through the SIF on every run.

```yaml
runtime:
  kind: host_binary
  build_image: docker://nvidia/cuda:12.2.2-devel-ubuntu22.04
  binary_path: /mnt/efs/refs/autodock-gpu/bin/autodock_gpu_128wi
  build_command: |
    cd <src>
    export GPU_INCLUDE_PATH=/usr/local/cuda/include
    export GPU_LIBRARY_PATH=/usr/local/cuda/lib64
    make DEVICE=CUDA NUMWI=128
    install -m 0755 bin/autodock_gpu_128wi <binary_path>
  notes: |
    AutoDock-GPU has no maintained upstream container; %post is blocked
    in the cap-pod (no CAP_SETGID).
```

### `venv`
For pip-installable tools where we materialize a Python env on EFS once.

```yaml
runtime:
  kind: venv
  python_version: "3.10"
  base_image: nvcr.io/nvidia/pytorch:24.01-py3   # optional, where the venv is rebuilt
  requirements: ["casanovo==5.0.0"]
  cache_dir: /mnt/efs/_casanovo-venv/5.0.0
  entrypoint: python -m casanovo.casanovo        # optional
  notes: |
    No upstream Casanovo container exists.
```

### `host_system`
For "use whatever the host already has" — `custom-slurm` only.

```yaml
runtime:
  kind: host_system
  binary: bash
  notes: User-supplied script body runs verbatim.
```

### `nextflow`
Pipeline registry pin lives here, not in `parameters`.

```yaml
runtime:
  kind: nextflow
  pipeline_repo: nf-core/sarek
  revision: "3.6.1"
  nxf_version: "25.04.8"
  schema_source: auto    # auto | none | url   (default: auto)
  schema_url: ""         # only when schema_source=url
  notes: |
    The samplesheet is auto-derived from nextflow_schema.json at form-render time.
```

### `interactive_image`
For `kind: interactive` templates. Same fields as `docker_image`.

## `resources`

```yaml
resources:
  cpus:         { default: 8,  min: 2,  max: 32 }
  memory_gb:    { default: 16, min: 8,  max: 256 }
  walltime_min: { default: 90, min: 10, max: 1440 }
  gpu:                           # absent = CPU-only job
    type: a10g
    count: 1
    allowed: [a10g, a100, l40s]
  partition_hint: gpu            # cpu | gpu | interactive
  requeue: { enabled: true, max: 1 }   # optional
```

The renderer derives `tres_per_node = gres/gpu:<type>:<count>` when `gpu` is
present. `partition_hint` is a hint for the scaler/feasibility logic.

## `inputs[]` — data paths

`role` constrains the renderer + console widget. Snake_case keys.

| role               | shape                                    |
|--------------------|------------------------------------------|
| `fastq_r1`/`r2`    | gzipped FASTQ                            |
| `bam`/`cram`       | aligned reads + index sibling            |
| `fasta`/`reference_fasta` | nucleotide FASTA + .fai sibling   |
| `samplesheet`      | CSV (use `schema.columns` for required) |
| `config_xml` / `config_json` | tool config (e.g. mqpar.xml)   |
| `mdp` / `prmtop` / `inpcrd`  | Amber/GROMACS topology         |
| `pdb` / `pdbqt` / `sdf` / `mol2` | structure files            |
| `mgf` / `mzml`     | mass-spec spectra                        |
| `hmm_db` / `fasta_db` | sequence search databases             |
| `generic_path`     | catch-all for any path-shaped input      |
| `raw_text`         | inline body (e.g. paste-in samplesheet)  |
| `sample_id`        | string identifier                        |
| `license_id`       | BYOL token (e.g. cryoSPARC)              |
| `env_map`          | name=value list                          |

```yaml
inputs:
  - key: input_bam
    role: bam
    required: true
    description: Recalibrated BAM/CRAM from sarek preprocessing.
  - key: samplesheet_csv
    role: samplesheet
    required: false
    format: nf-core-sarek
    schema:
      columns: [patient, sample, lane, fastq_1, fastq_2]
```

## `parameters[]` — algorithm knobs only

NEVER images, paths, or resources. Those live in `runtime`/`inputs`/`resources`.

| widget             | for                                              |
|--------------------|--------------------------------------------------|
| `text`             | one-line strings                                 |
| `textarea`         | multi-line strings (e.g. inline scripts)         |
| `number`           | int/float; `min`/`max` optional                  |
| `enum`             | drop-down; `options:` required                   |
| `bool`             | checkbox                                         |
| `env_map`          | `KEY=value` list                                 |
| `nextflow_schema`  | dynamically rendered from pipeline schema JSON   |
| `json_textarea`    | free-form JSON blob                              |

```yaml
parameters:
  - key: model_type
    widget: enum
    options: [shortread, ont, pacbio]
    default: shortread
    description: pbrun mode flag.
  - key: nrun
    widget: number
    default: 100
    min: 1
    max: 1024
    advanced: true
```

## `outputs`

```yaml
outputs:
  dir: "/mnt/efs/{{.ctx.user_email}}/job-{{.ctx.run_id}}"
  artifacts:
    - key: vcf
      path: "{{.outputs.dir}}/sample.deepvariant.vcf.gz"
      role: primary_output
    - key: log
      path: "{{.outputs.dir}}/run.log"
      role: log
```

`role` is one of `primary_output | metrics_file | log | generic`. The console
+ agent surface `primary_output` and `metrics_file` as first-class artifacts.

## `routing` (interactive only)

```yaml
routing:
  path_prefix: /jupyter
  port_param: port
  ready_callback: true
  idle_cull_min: 60
```

`port_param` names a parameter (or hidden parameter) that holds the port the
session is listening on. The script writes the port via the ready-callback.

## `policy` and `dependencies`

```yaml
policy:
  prefer_on_demand: false
  prefer_arm64: false

dependencies:
  after_ok: "{{.params.dep_jobid}}"   # rare; only for chain templates
```

These absorb the v1 `dependency_after_ok`, `prefer_on_demand`, `prefer_arm64`
parameters which are no longer authored in `parameters[]`.

## Go-template path reference

What templates can address inside `script:`/`config:`/`outputs.dir`/etc.:

```
.runtime.image / .runtime.binary / .runtime.cache_dir / .runtime.* (per-kind)
.resources.cpus.default
.resources.memory_gb.default
.resources.walltime_min
.resources.gpu.type
.resources.gpu.count
.inputs.<key>
.params.<key>
.outputs.dir
.outputs.<key>            # artifact path by key
.policy.prefer_on_demand
.policy.prefer_arm64
.dependencies.after_ok
.ctx.user_email
.ctx.cluster_id
.ctx.run_id
```

`walltime_min` is in MINUTES. `memory_gb` is in GIBIBYTES.
