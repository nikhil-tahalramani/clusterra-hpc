# parabricks-rna-fq2bam — smoke test

GPU-accelerated RNA-seq alignment via `pbrun rna_fq2bam` (STAR-GPU). Validates
that the slurmd image + Parabricks 4.6.0-1 SIF + iGenomes STAR index path
work end-to-end on an A10G capacity pod.

## Inputs (small public RNA-seq pair)

Use `nf-core/test-datasets` rnaseq branch — paired ~10MB Illumina FASTQs:

```
https://raw.githubusercontent.com/nf-core/test-datasets/rnaseq/testdata/GSE110004/SRR6357070_1.fastq.gz
https://raw.githubusercontent.com/nf-core/test-datasets/rnaseq/testdata/GSE110004/SRR6357070_2.fastq.gz
```

Stage to EFS:

```bash
ssh ubuntu@<customer-cp>
sudo mkdir -p /mnt/efs/smoke/rna-fq2bam && sudo chmod 777 /mnt/efs/smoke/rna-fq2bam
cd /mnt/efs/smoke/rna-fq2bam
curl -L -O https://raw.githubusercontent.com/nf-core/test-datasets/rnaseq/testdata/GSE110004/SRR6357070_1.fastq.gz
curl -L -O https://raw.githubusercontent.com/nf-core/test-datasets/rnaseq/testdata/GSE110004/SRR6357070_2.fastq.gz
```

## STAR index (pre-built, expected on /mnt/s3-refs)

Default points at the iGenomes GRCh38 STAR index:

```
/mnt/s3-refs/igenomes/Homo_sapiens/GATK/GRCh38/Sequence/STARIndex/
```

Sentinel files the template checks for: `SAindex`. If missing, the job exits
non-zero before launching pbrun (no wasted GPU minutes).

If the iGenomes mount lacks STARIndex, build once offline (CPU instance, ~1h
on c6i.8xlarge) and stage to EFS at `/mnt/efs/refs/STARIndex-GRCh38/`, then
override `star_index_dir` in the form. Building inline is NOT recommended
for a smoke — adds 60+ min to a 5-min alignment.

## Submit

Console form (or via `clusterra` CLI / agent):

| field | value |
| --- | --- |
| input_fq1 | `/mnt/efs/smoke/rna-fq2bam/SRR6357070_1.fastq.gz` |
| input_fq2 | `/mnt/efs/smoke/rna-fq2bam/SRR6357070_2.fastq.gz` |
| reference_fasta | (default GRCh38) |
| star_index_dir | (default GRCh38 STARIndex) |
| output_bam | `/mnt/efs/smoke/rna-fq2bam/SRR6357070.rna.bam` |
| read_group_id | `RG1` |
| sample | `SRR6357070` |
| time_limit | `90` |

## Expected wall-clock + cost

- Cap-pod cold (Karpenter g5.12xlarge provision): ~3–4 min
- pbrun rna_fq2bam on the ~10MB test pair: ~2–4 min (input is tiny, GPU spin-up dominates)
- Total: **~6–8 min**, **~$0.30** at g5.12xlarge spot

For a representative 30M-read library: ~30–45 min on the same shape, ~$1.50.

## Validate

On the customer cp host:

```bash
ls -la /mnt/efs/smoke/rna-fq2bam/SRR6357070.rna.bam{,.bai}
apptainer exec docker://quay.io/biocontainers/samtools:1.20--h50ea8bc_0 \
  samtools view -c /mnt/efs/smoke/rna-fq2bam/SRR6357070.rna.bam
# expect: >0 reads, ideally close to 2x the input read count (paired)
apptainer exec docker://quay.io/biocontainers/samtools:1.20--h50ea8bc_0 \
  samtools view -H /mnt/efs/smoke/rna-fq2bam/SRR6357070.rna.bam | grep '^@RG'
# expect: @RG ID:RG1 LB:SRR6357070 PL:ILLUMINA SM:SRR6357070
```

Pass criteria:
- Job COMPLETED (not FAILED, not requeued >0 times after first try)
- `<output>.bam` and `<output>.bam.bai` both exist on EFS
- `samtools view -c` returns a non-zero count
- `@RG` header line carries the configured ID + SM

## Known concerns

- **STAR index availability** — iGenomes `Sequence/STARIndex/` is present on
  the GRCh38 layout but version-pinned to a specific STAR release; pbrun's
  bundled STAR is forward-compatible with iGenomes-built indices, but if a
  future Parabricks bumps STAR major version, indices must be rebuilt.
  Verify SAindex presence before declaring the smoke green.
- **Single-end FASTQs not supported** — pbrun rna_fq2bam is paired-end only;
  the template enforces both R1 and R2 as required.
- **Multi-lane samples** — concatenate per-read upstream or run one job per
  lane and `samtools merge` downstream. The template intentionally does NOT
  expose multi-lane --in-fq groups (keeps the form simple; advanced users
  can chain via dependency_after_ok).
