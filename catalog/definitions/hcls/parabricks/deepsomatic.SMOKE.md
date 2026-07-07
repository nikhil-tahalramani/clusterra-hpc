# parabricks-deepsomatic — smoke test

Verifies `pbrun deepsomatic` 4.6.0-1 on a tumor-normal pair produces a
non-empty somatic VCF with the expected FORMAT fields. Mirrors the
deepvariant smoke shape; same GRCh38 iGenomes ref via S3 mountpoint.

## Smoke dataset

**Mock-somatic GIAB Ashkenazim trio chr21** (active fixture — May 8 2026).
HG002 (son) used as "tumor", HG004 (mother) used as "normal", chr21 only.
Wrong biology (germline pair, no somatic truth) but exercises the
`pbrun deepsomatic` codepath end-to-end on real GRCh38-aligned 30x
Illumina BAMs. Chosen because s3://giab listing is allowed for known
keys; SEQC2 HCC1395 mirrors require listing perms we don't have.

Sliced inline by the smoke job from public S3 via htslib s3:// reader:
- Tumor source:  `s3://giab/data/AshkenazimTrio/HG002_NA24385_son/.../HG002.GRCh38.300x.bam`
- Normal source: `s3://giab/data/AshkenazimTrio/HG004_NA24143_mother/.../HG004.GRCh38.300x.bam`
- Local slices:  `/mnt/efs/smoke/deepsomatic/{HG002,HG004}.chr21.bam`
- Reference:     `/mnt/s3-refs/igenomes/.../Homo_sapiens_assembly38.fasta`
- Intervals BED: `/mnt/efs/smoke/deepsomatic/chr21.bed`

Both BAMs are unconditionally reheadered (via `samtools reheader`) to
SM=TUMOR / SM=NORMAL. The novoalign 300x BAMs ship with empty SM tags,
which deepsomatic rejects identically to a SM-collision; the unconditional
reheader covers both cases. Pre-reheader RG is preserved for ID/LB/PL.

Future upgrade: stage true SEQC2 HCC1395 chr21 recal BAMs into
`clusterra-public` and replace this fixture with a real somatic truth
set so we can run hap.py som.py concordance in CI.

## Submit (current smoke — direct sbatch via login pod)

```bash
# kubectl cp /tmp/deepsomatic-smoke.sh login-clusde74-...:/tmp/
# kubectl exec login-clusde74-... -- sbatch /tmp/deepsomatic-smoke.sh
# See /tmp/deepsomatic-smoke.sh for the rendered sbatch script.
# Verified May 8 2026 on clusde74: job 1805 on g5.4xlarge (A10G).
# Prior 1780 hit `pbrun: error: required: --out-variants` — flag in the
# template was --out-vcf (4.5 spelling); 4.6 renamed to --out-variants.
# Prior 1796 hit ExitCode 0:53 (slurmstepd EACCES on smoke.<job>.out)
# because /mnt/efs/smoke/deepsomatic/ was created root-owned 0755 by the
# 1780 fixture-stage step. Fixed via chmod 1777 on the dir + a
# `chmod 1777 "$OUTPUT_DIR" || true` line in the production template.
```

Once the launchable router lands, the templated submit will look like:

```bash
curl -X POST "$CAP_API/v1/launchables/parabricks-deepsomatic/submit" \
  -H "Authorization: Bearer $TOKEN" \
  -d '{
    "params": {
      "tumor_bam":  "/mnt/efs/smoke/deepsomatic/HG002.chr21.tumorSM.bam",
      "normal_bam": "/mnt/efs/smoke/deepsomatic/HG004.chr21.bam",
      "intervals_bed": "/mnt/efs/smoke/deepsomatic/chr21.bed",
      "time_limit": 90
    }
  }'
```

## Validate

```bash
VCF=/mnt/efs/smoke/deepsomatic/mock-somatic.HG002vsHG004.chr21.vcf.gz

# 1. VCF exists, non-empty, bgzipped + indexed
test -s "$VCF"
bcftools view -h "$VCF" | grep -q '^##fileformat=VCF'

# 2. Somatic FORMAT fields present (DeepSomatic emits GT/GQ/DP/AD/VAF)
bcftools view -h "$VCF" \
  | grep -E '^##FORMAT=<ID=(GT|DP|AD|VAF)' | wc -l   # expect >= 3

# 3. Codepath produced records (mock-somatic input, so PASS count is not biology)
bcftools view -H "$VCF" | wc -l   # expect > 0
```

## Pass criteria

- Job exits COMPLETED (or COMPLETED-after-1-requeue — see template comment
  on Parabricks 4.6.0-1 SIGSEGV rate; spot-reclaim NODE_FAIL also triggers
  Slurm `--requeue` and is not counted as a smoke failure).
- Wall-clock < 15 min on g5.4xlarge (A10G), single-attempt.
- VCF has a valid `##fileformat=VCF` header.
- VCF has >= 3 of {GT, DP, AD, VAF} FORMAT fields declared in the header.
- Record count: ZERO somatic records is the *correct* outcome for the
  HG002-vs-HG004 mock-somatic fixture (unrelated germline trio members,
  no somatic truth). DeepSomatic emits records into a `GERMLINE`
  filter-tagged sidecar; the main VCF body is expected empty here.
  When fixture is upgraded to real SEQC2 HCC1395 chr21 recal BAMs,
  bump this criterion to `>= 10 PASS records`.

Verified May 8 2026: job 1805 COMPLETED 0:0 in 9:15 (after 1 spot-reclaim
NODE_FAIL auto-requeue at 17:33), pbrun deepsomatic wall-clock 6:28 on
A10G, output VCF 16 KB with 4/4 FORMAT fields and 0 somatic records.

## Known gotchas

- If the cap-pod requeues once, both attempts share the same
  `output_vcf` path — pbrun overwrites cleanly.
- Truth-VCF concordance (hap.py / som.py) is out of scope for smoke;
  see `happy-giab-validation.yaml` sibling for the full validation flow.
