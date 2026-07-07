# rapids-singlecell — smoke test

GPU-accelerated scanpy drop-in, served as a JupyterLab interactive session.
Same auth/proxy/ready-callback pipe as `jupyter.yaml` (path_prefix `/jupyter`).

## Rendered sbatch (gpu-a10 default)

```bash
#!/bin/bash
#SBATCH --job-name=rapids-singlecell
#SBATCH --chdir=/mnt/efs/user@example.com
#SBATCH --time=480
#SBATCH --cpus-per-task=4
#SBATCH --mem=24576
#SBATCH --gres=gpu:a10g:1
#SBATCH --partition=gpu

# (script body — see rapids-singlecell.yaml; ready callback + apptainer pull
# + jupyter lab on /jupyter/{cluster_id}/{job_id}/)
```

## Smoke = ready-callback fires + curl 200 from inside the cluster

This is an interactive template, so there is no batch output to grep for
"Done". The validation contract is:

1. `${CLUSTERRA_API}/v1/internal/sessions/${SLURM_JOB_ID}/ready` is POSTed
   with `{node_ip, port, cluster_id, user_email, template_id}` once Slurm
   has assigned the node and PORT.
2. JupyterLab returns HTTP 200 on
   `/jupyter/{cluster_id}/{job_id}/api/status` (the cluster-api reverse
   proxy will 502 until step 1 lands; 200 after).
3. Open the starter notebook `rapids-singlecell-pbmc3k.ipynb` (auto-
   dropped into `/mnt/efs/{user_email}` on first launch) and run all cells.
   The pbmc3k tutorial finishes end-to-end (filter → norm → HVG → PCA →
   UMAP → Leiden) in ~10s on a10g.

### From inside the central K3s (curl smoke)

```
curl -sS -o /dev/null -w '%{http_code}\n' \
  https://dev-api.clusterra.cloud/jupyter/${CLUSTER_ID}/${JOB_ID}/api/status
# expect: 200
```

## Wall-clock to ready

| Phase                                       | Time      |
|---------------------------------------------|-----------|
| Slurm queue → node assigned (cap-pod warm)  | <30s      |
| Karpenter cold-start (g5.xlarge)            | 90-120s   |
| `apptainer pull` first time (~6 GiB SIF)    | 180-300s  |
| `apptainer pull` cached (subsequent jobs)   | <2s       |
| JupyterLab listen                           | ~5s       |
| **Total cold (cap-pod warm)**               | **3-5 min** |
| **Total cold (Karpenter cold + pull)**      | **5-8 min** |
| **Total warm (SIF cached on node)**         | **<60s**  |

## Cost

g5.xlarge on-demand us-east-1 = **$1.006/hr** (4 vCPU, 16 GiB host, 1×A10G
24 GiB VRAM). Sized profile uses 4 vCPU + 24 GiB — note: 24 GiB request on
a 16 GiB instance forces Karpenter onto **g5.2xlarge ($1.212/hr)** or
g5.4xlarge ($1.624/hr). The console profile mapping pins g5.2xlarge as the
canonical shape; budget **$1.21/hr while running**, idle-cull stops the job.

## Image-pull notes

- Default `nvcr.io/nvidia/rapidsai/notebooks:25.10-cuda12.9-py3.11` is a
  **public** NGC image — pulls anonymously (verified 2026-05 via the
  `nvcr.io/proxy_auth` anonymous-bearer path). NOTE: the legacy
  `nvcr.io/nvidia/rapidsai/rapidsai:*` repo stopped publishing after 22.x
  and 404s for any 23+ tag pull — always use `rapidsai/notebooks:*` for
  the Jupyter-shipped flavor (`rapidsai/base:*` is libs-only). NGC ships
  weekly tags (e.g. 25.10, 25.12); pin to a known-good weekly to avoid
  drift.
- **If a future tag becomes auth-gated:** NGC requires an API key as the
  password with username `$oauthtoken`. Wire it through the existing
  `/etc/clusterra/ghcr/config.json` mount (a second `auths` entry for
  `nvcr.io`), and the parent jupyter.yaml docker-auth block (lines 96-107)
  will pick it up unchanged. Or fall back to `docker://rapidsai/rapidsai`
  on Docker Hub (mirror, no auth, lags NGC by ~1 release).
- **Custom-built fallback:** if RAPIDS ever ships a breaking change that
  conflicts with our slurmd entrypoint binds, build a thin overlay image
  pinned to a known-good RAPIDS base + our /mnt/efs and /mnt/s3-refs
  bind-points pre-created. Park under `images/rapids-singlecell/`.
