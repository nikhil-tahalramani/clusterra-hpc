# Smoke test — parabricks-pacbio-germline

## What this validates

`pbrun pacbio_germline` end-to-end on Clusterra: PacBio HiFi uBAM → aligned BAM
+ DeepVariant VCF, on a single A10G (g5.12xlarge cap-pod), with
`/mnt/efs` + `/mnt/s3-refs` auto-bound and TMPDIR pinned to NVMe via
TaskProlog. Confirms partition routing (`gpu:a10g:1`), Apptainer pull of
`nvcr.io/nvidia/clara/clara-parabricks:4.6.0-1` (~8 GiB SIF, cached on
EFS after first run), and `--in-bam` plumbing into the pacbio_germline
entry-point. (Parabricks 4.6 renamed `--in-hifi-bam` → `--in-bam`; a flag
mismatch was the root cause of job 1693's 255:0 in May 2026.)

## Smoke data

**Recommended:** GIAB HG002 PacBio HiFi 5× downsample, chr20 region-
restricted (~500 MB BAM, ~30 min wall-clock on A10G).

- Source: GIAB HG002 PacBio HiFi aligned to GRCh38 (pbmm2, haplotagged), at
  `https://s3.amazonaws.com/giab/data/AshkenazimTrio/HG002_NA24385_son/PacBio_CCS_15kb_20kb_chemistry2/GRCh38/HG002.SequelII.merged_15kb_20kb.pbmm2.GRCh38.haplotag.10x.bam`
  (full ~120 GiB, anonymous-readable; sibling `.bai` ~24 MB).
- Stage chr20-only via samtools using HTTPS+local-BAI (`URL##idx##LOCAL_BAI`)
  — this issues range requests for the chr20 byte ranges and emits a
  properly framed BGZF BAM (~500 MB). Place at
  `/mnt/efs/smoke/hg002.chr20.hifi.bam`.
- Truth set for spot-check: GIAB v4.2.1 HG002 benchmark VCF
  (`s3://giab/release/AshkenazimTrio/HG002_NA24385_son/NISTv4.2.1/`).

**Why not `samtools view -b s3://...`:** the htslib S3 plugin streamed
output silently corrupted the BAM (zlib `inflate -3` / "Exec format error")
on jobs 1693 + 1695 (May 7-8 2026). Always use HTTPS + `##idx##` with the
BAI pre-staged — confirmed working on job 1775.

**Fallback (smaller, slower):** the PacBio CCS dataset directory at
`https://downloads.pacbcloud.com/public/dataset/HG002-CpG-methylation-202202/`
hosts `HG002.GRCh38.haplotagged.bam` + `.bai` — same approach (HTTPS + BAI),
similar chr20 footprint.

## Submission

```
input_bam:        /mnt/efs/smoke/hg002.chr20.hifi.bam
reference_fasta:  /mnt/s3-refs/igenomes/Homo_sapiens/GATK/GRCh38/Sequence/WholeGenomeFasta/Homo_sapiens_assembly38.fasta
output_bam:       /mnt/efs/smoke/hg002.chr20.hifi.aligned.bam
output_vcf:       /mnt/efs/smoke/hg002.chr20.pacbio.dv.vcf.gz
intervals_bed:    /mnt/efs/smoke/chr20.bed   # optional but recommended for the smoke
time_limit:       120
```

## Validation

```
# 1. Output VCF exists and is non-empty.
test -s /mnt/efs/smoke/hg002.chr20.pacbio.dv.vcf.gz

# 2. >100 variant records (chr20 alone yields ~150–200K SNVs+indels on
#    HG002 5× HiFi).
apptainer exec docker://quay.io/biocontainers/bcftools:1.21--h8b25389_0 \
  bcftools view -H /mnt/efs/smoke/hg002.chr20.pacbio.dv.vcf.gz | wc -l
# expected: >> 100

# 3. Aligned BAM has HiFi-aware MAPQ distribution + chr20 reads only.
apptainer exec docker://quay.io/biocontainers/samtools:1.21--h50ea8bc_0 \
  samtools view -c /mnt/efs/smoke/hg002.chr20.hifi.aligned.bam chr20
# expected: matches input read count within 1%

# 4. (Optional) hap.py concordance vs GIAB HG002 v4.2.1 truth on chr20:
#    F1 > 0.99 SNP, > 0.95 indel is the documented Parabricks pacbio
#    accuracy floor (matches CPU DV-PACBIO).
```

## Wall-clock + cost expectations

| Tier            | Hardware            | Wall   | Spot $/run |
| --------------- | ------------------- | ------ | ---------- |
| Smoke (chr20)   | g5.12xlarge (A10G)  | ~45 m  | ~$0.75     |
| Full 30× HG002  | g5.12xlarge (A10G)  | ~2 h   | ~$2.00     |
| Full 30× HG002  | g6e.12xlarge (L40S) | ~75 m  | ~$2.50     |
| CPU baseline    | c6i.32xlarge        | ~24 h  | ~$40       |

Cost basis: us-east-1 spot, May 2026.

## Known concerns

- **A10G vs A100:** NVIDIA docs recommend A100 for full 30× HiFi WGS;
  A10G's 24 GiB VRAM is at the edge for some HiFi tile sizes. Smoke
  passes cleanly; production users with > 30× HiFi or telomere-to-telomere
  references should override `tres_per_node` to `gres/gpu:a100:1`.
- **First run slow:** Parabricks SIF (~8 GiB) pulls on first-job-on-node;
  EFS apptainer cache amortizes across the cluster after that. Time-limit
  default (180 min) accounts for this.
- **uBAM vs aligned BAM:** `pbrun pacbio_germline` accepts both via
  `--in-hifi-bam`. Pre-aligned input still re-aligns (the tool
  re-canonicalizes). Users with already-aligned HiFi BAM who only want
  variant calling should use the sibling `parabricks-deepvariant` template
  with `model_type=pacbio` instead — it skips the alignment stage.
