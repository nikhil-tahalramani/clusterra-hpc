# GROMACS with MPI (gmx_mpi) + CUDA — for single-node multi-GPU `mdrun -multidir`
# replica ensembles (REMD / non-equilibrium FEP) on A10G (sm_86) cap-pods.
#
# Why a custom image: no upstream image ships gmx_mpi + CUDA. The NGC
# (nvcr.io/hpc/gromacs) and Docker Hub (gromacs/gromacs) GPU images are
# thread-MPI ONLY — they have no gmx_mpi binary, so `mdrun -multidir`
# (multi-simulation / replica exchange) cannot run on them. Same trap as the
# stock RELION container. We therefore build gmx_mpi from source here.
#
# MPI model: single-node `-multidir` uses the container's OWN OpenMPI launcher
# (mpirun) entirely within one pod — no Slurm cross-node PMIx, no host-MPI ABI
# coupling. (True multi-node MPI on this fleet is separately gated on an
# intra-cluster security-group rule + EFA; per AWS data, GROMACS multi-node
# scaling collapses without EFA anyway, so single-node multi-GPU is the right
# and cost-optimal pattern for REMD/FEP.)
#
# amd64 only: GPU jobs land on x86 g5 (A10G) cap-pods.
FROM nvidia/cuda:12.6.3-devel-ubuntu22.04

ARG GMX_VERSION=2024.4
# A10G = compute capability 8.6. Pinning one target keeps the CUDA compile fast.
ARG GMX_CUDA_SM=86
ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update && apt-get install -y --no-install-recommends \
        build-essential cmake \
        libopenmpi-dev openmpi-bin \
        wget ca-certificates \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /tmp/gmx
RUN wget -q "https://ftp.gromacs.org/gromacs/gromacs-${GMX_VERSION}.tar.gz" \
    && tar xf "gromacs-${GMX_VERSION}.tar.gz" \
    && cd "gromacs-${GMX_VERSION}" \
    && mkdir build && cd build \
    && cmake .. \
        -DGMX_MPI=ON \
        -DGMX_GPU=CUDA \
        -DGMX_OPENMP=ON \
        -DGMX_BUILD_OWN_FFTW=ON \
        -DGMX_CUDA_TARGET_SM="${GMX_CUDA_SM}" \
        -DGMX_SIMD=AVX2_256 \
        -DCMAKE_C_COMPILER=mpicc \
        -DCMAKE_CXX_COMPILER=mpicxx \
        -DCMAKE_INSTALL_PREFIX=/usr/local/gromacs \
        -DCMAKE_BUILD_TYPE=Release \
    && make -j"$(nproc)" \
    && make install \
    && cd / && rm -rf /tmp/gmx

ENV PATH=/usr/local/gromacs/bin:$PATH
ENV LD_LIBRARY_PATH=/usr/local/gromacs/lib:/usr/local/gromacs/lib64:$LD_LIBRARY_PATH

# Sanity: gmx_mpi must report "MPI library: MPI" and "GPU support: CUDA".
RUN gmx_mpi --version | grep -Ei "version|MPI|GPU support|SIMD" || true
