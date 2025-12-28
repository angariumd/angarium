# NON-GOALS (MVP)

The following features are explicitly out of scope for the initial MVP:

- **No containers/Kubernetes**: Jobs are standard OS processes.
- **No preemption**: Once a job is running, it stays until completion or manual cancel.
- **No multi-node training**: Each job must fit on a single node.
- **No advanced scheduling**: No fair-share, quotas, backfilling, or NVLink awareness.
- **No code upload**: No bundling, rsync, or git-clone logic. Users must ensure code is on a shared FS.
- **No container images**: Jobs run as OS processes directly. No Docker/Podman/Singularity.
- **No plugin system**: Built-in logic only.
- **No generic RBAC**: Simple token-based identity for ownership.
- **No high availability**: Single controller instance (state in SQLite).
