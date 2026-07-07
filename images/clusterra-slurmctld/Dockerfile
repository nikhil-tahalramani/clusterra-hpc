FROM ghcr.io/slinkyproject/slurmctld:25.11-ubuntu24.04

# slurmctld runs as UID 401 (non-root). The cluster-api user provisioner
# appends entries to /etc/passwd via kubectl exec so that getpwnam() resolves
# dynamically created users. Make /etc/passwd writable by the daemon user.
USER root
RUN chown 401:401 /etc/passwd /etc/group
USER 401
