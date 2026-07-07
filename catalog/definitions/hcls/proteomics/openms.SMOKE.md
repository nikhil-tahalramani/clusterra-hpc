# OpenMS templates — smoke recipes

Covers both `openms-dda` (CometAdapter + PercolatorAdapter + IDFilter) and
`openswath-dia` (OpenSwathWorkflow + pyprophet). Validated on `clusde74`
(dev-demo) on 2026-05-28.

## Positioning recap

OpenMS is the **fully BSD/permissive** DDA + DIA fallback for customers who
reject the MaxQuant freeware EULA or want a redistributable toolchain. OpenMS
itself is BSD-3; Comet and Percolator are Apache-2.0; pyprophet is BSD-3.

Pick OpenMS when:

- Customer legal flags the MaxQuant EULA.
- Customer wants Percolator rescoring + the broader TOPP toolchain (Epifany
  protein inference, FeatureFinderIdentification quant) post-search.
- Customer is open-source-only (sample-prep, CRO redistribution).

For pure DDA, Sage (also BSD) is the faster choice. OpenMS-DDA's edge is the
Percolator + downstream TOPP ecosystem.

## CRITICAL apptainer flags — load-bearing

Every `apptainer exec` in both templates uses
`--no-home --writable-tmpfs`. Without these, OpenMS adapters
(CometAdapter, PercolatorAdapter, MSGFPlusAdapter, OpenSwathWorkflow)
crash with the misleading error `"Process failed to start"` /
`"Could not remove directory"`. Documented in
`MEMORY:openms_cometadapter_apptainer_fix`. Verified on this smoke pass —
both templates ran clean.

## §1 — Converting MGF → mzML (one-liner)

OpenMS adapters prefer mzML. If you only have MGF or vendor RAW:

```bash
apptainer exec --no-home --writable-tmpfs --bind /mnt/efs \
  docker://quay.io/biocontainers/openms:3.5.0--h78fb946_0 \
  FileConverter -in input.mgf -out input.mzML
```

ThermoRawFileParser handles `.raw` → `.mzML` (separate biocontainer, not
exercised in this smoke).

## §2 — DDA smoke (openms-dda)

### Fixture

- `input_mzml`: `/mnt/efs/n52h53@gmail.com/openms-smoke-cometadapter/input.mzML` — 416 KiB, 127 spectra (subset of the Casanovo/Comet MAPPs benchmark, already pre-converted from MGF).
- `fasta_db`: `/mnt/efs/nikhil@clusterra.io/comet-dbsearch/human_sprot_td.fasta` — UniProt Swiss-Prot human + DECOY_-prefixed reversed (~85k entries).
- `comet_executable_path`: `/mnt/efs/nikhil@clusterra.io/comet-dbsearch/comet.linux.exe` — pre-staged Apache-2.0 Comet binary.

### Submit (via custom-slurm passthrough; templates not yet DDB-synced)

JWT mint per `MEMORY:api_jwt_mint_workflow`. Then submit a custom-slurm job
whose `script_body` is the literal contents of
`core/templates/definitions/hcls/proteomics/openms-dda.yaml`'s `script:`
block, after substituting:

| Placeholder | Value |
|---|---|
| `{{.inputs.input_mzml}}` | `/mnt/efs/n52h53@gmail.com/openms-smoke-cometadapter/input.mzML` |
| `{{.inputs.fasta_db}}` | `/mnt/efs/nikhil@clusterra.io/comet-dbsearch/human_sprot_td.fasta` |
| `{{.outputs.dir}}` | `/mnt/efs/n52h53@gmail.com/openms-dda-smoke-${SLURM_JOB_ID}` |
| `{{.params.comet_executable_path}}` | `/mnt/efs/nikhil@clusterra.io/comet-dbsearch/comet.linux.exe` |
| `{{.params.precursor_mass_tolerance_ppm}}` | `20` |
| `{{.params.fragment_mass_tolerance}}` | `0.02` |
| `{{.params.enzyme}}` | `Trypsin` |
| `{{.params.missed_cleavages}}` | `1` |
| `{{.params.rescore_with_percolator}}` | `true` |
| `{{.params.fdr_threshold}}` | `0.05` (use 0.01 on real datasets; smoke fixture has only 127 spectra so the minimum discrete q-value Percolator can compute is ~0.019 — 0.01 yields zero hits) |

Once the templates are DDB-synced, this becomes a single
`template_id: openms-dda` submit.

### Observed (job 2826, COMPLETED, 149 s wall, $0.039, c5.18xlarge spot)

```
===== STEP 1: CometAdapter search =====
... (Comet 2024.02.0 rev. 0, 127 spectra processed)
===== STEP 2: PercolatorAdapter rescore =====
127 suitable PeptideHits of 127 PSMs were reannotated.
===== STEP 3: IDFilter at q <= 0.05 =====
Before filtering: 1 identification runs with 299 proteins, 127 spectra identified with 127 spectrum matches.
After filtering:  1 identification runs with 179 proteins,  60 spectra identified with  60 spectrum matches.
===== Summary =====
Comet PSMs (pre-rescore):      127
Percolator-rescored PSMs:      127
Filtered at q<=0.05:            60
```

Artifacts at `/mnt/efs/n52h53@gmail.com/openms-dda-smoke-2826/`:

- `comet.idXML` (289 KiB) — raw Comet PSMs
- `comet.perc.idXML` (325 KiB) — Percolator-rescored, q-value-annotated PSMs
- `comet.perc.fdr.idXML` (≈9.9 KiB) — IDFilter-passed PSMs at q ≤ FDR
- `openms-dda.log` — full TOPP stdout/stderr tee

### Bug found + fixed during smoke

Original draft had:

```bash
$APPT "$IMG" IDFilter -in ... -score:pep "$FDR" || \
$APPT "$IMG" IDFilter -in ... -score:psm "$FDR"
```

`-score:pep` is NOT a valid IDFilter option (the valid ones are
`-score:psm` / `-score:peptide` / `-score:protein` / `-score:proteingroup`).
The first call aborted, the OR-fallback caught it. Replaced with a single
unambiguous `-score:psm "$FDR"` call. Committed in the YAML.

## §3 — DIA smoke (openswath-dia)

A real DIA analysis needs a spectral library (`.pqp` or `.tsv`) that matches
the SWATH window scheme of your mzML. Library construction is a customer-
config exercise (typically `OpenSwathAssayGenerator` on a matched DDA
search of pooled samples). For the smoke we exercise the
**container-existence** path:

- Image extracts and `OpenSwathWorkflow --help` runs.
- `pyprophet` biocontainer extracts and prints version.
- Apptainer flags `--no-home --writable-tmpfs` resolve cleanly (no
  "Could not remove directory" error).
- mzML + FASTA inputs readable.

Set `OSW_SMOKE=0` and provide `spectral_library` (and optionally
`irt_peptides`) to run a real DIA analysis.

### Fixture

- `input_mzml`: `/mnt/efs/n52h53@gmail.com/step-prep/mzml/20201115_GD_NT1gly_1115_C18_45C_run1.mzML` (419 MB; NOT a true SWATH file — used only to verify path readability and stat).
- `spectral_library`: empty (smoke trigger).
- `fasta_db`: same as DDA smoke.

### Observed (job 2831, COMPLETED, 335 s wall, $0.061, f2.6xlarge spot)

```
[openswath-dia] container-existence smoke
OpenSwathWorkflow -- Complete workflow to run OpenSWATH
input_mzml readable: /mnt/efs/.../run1.mzML (419846899 bytes)
fasta readable: /mnt/efs/.../human_sprot_td.fasta (54769581 bytes)
[openswath-dia] image extracts; entrypoint present.
pyprophet, version 3.0.15
done
```

Most of the 335 s is OCI → SIF conversion for both biocontainers
(openms:3.5.0 ~860 MB SIF, pyprophet:3.0.15 ~380 MB SIF). Subsequent
runs on the same node hit the SIF cache and re-execute in ~10-15 s.

### Bug found + fixed during smoke

First attempt FAILED with `(eval):1: == not found`. Root cause: original
draft piped `OpenSwathWorkflow --help 2>&1 | head -10`. With `set -o
pipefail`, the SIGPIPE from `head` closing early after 10 lines tripped a
non-zero exit on the upstream `OpenSwathWorkflow` process, which aborted
the script under `set -e`. Fix: capture full help to a tempfile then
`head` it, suffixed with `|| true`. Committed.

Also added an explicit pyprophet image pre-pull in the smoke path so the
first real DIA run doesn't pay the pyprophet OCI conversion cost.

## §4 — Spectral library staging (for real DIA runs)

When customer is ready for a real DIA analysis:

1. Run a DDA search of a pool sample (sage, openms-dda, or MaxQuant) to
   produce an `idXML` (or convert).
2. Use `OpenSwathAssayGenerator` to convert DDA peptide IDs + matched
   featureXML into a `.pqp` assay library.
3. Use `OpenSwathDecoyGenerator` to add shuffled decoys to the library.
4. Use `EasyPQP convert + library` (separate package) for iRT alignment
   peptides → iRT `.pqp`.

This is multi-step and customer-data-specific; not in this template's
scope. We may add an `openswath-library-build` template as a follow-up.

## ready: flags

- `openms-dda`: `ready: true` — end-to-end smoke passes on real spectra,
  produces real PSM counts.
- `openswath-dia`: `ready: true` — container-existence smoke passes; real
  DIA run is gated on customer-supplied spectral library (documented in
  template description and parameter).

## Jobs cited

| Job ID | Template | State | Wall | Cost | Notes |
|---|---|---|---|---|---|
| 2813 | discovery | COMPLETED | 27 s | $0.007 | EFS fixture inventory |
| 2816 | pre-check | COMPLETED | 1 s | — | verify mzML readable |
| 2819 | openms-dda (v1) | COMPLETED | 159 s | $0.042 | exposed `-score:pep` bug |
| 2824 | IDFilter direct | COMPLETED | — | — | confirmed `-score:psm` works (60 PSMs @ 0.05, 52 @ 0.02, 0 @ 0.01) |
| 2826 | openms-dda (v2) | COMPLETED | 149 s | $0.039 | clean end-to-end |
| 2827 | openswath-dia (v1) | FAILED | 122 s | $0.032 | SIGPIPE on `head` |
| 2831 | openswath-dia (v2) | COMPLETED | 335 s | $0.061 | clean container-existence |
