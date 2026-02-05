# Angarium

A lightweight GPU job queue for small clusters (5–100 GPUs) that replaces SSH chaos.

## Mission
Provide a minimal GPU-first scheduler without the overhead of Kubernetes, containers, or Slurm. Guarantee no double-booking, safe execution, and reliable cleanup.

## Key Features
- **GPU-First**: Aware of GPU health and memory via `nvidia-smi`.
- **Single-Node Placement**: Best-fit packing for optimal utilization.
- **Minimalist**: OS processes, not containers.
- **Visibility**: Clear "why queued" status for pending jobs.

## Job Execution Model
- **Shared Filesystem**: The scheduler does NOT move user code or files. Jobs must run from a working directory (`cwd`) that is already accessible on GPU nodes (e.g., via NFS, SMB, or Lustre).
- **Execution**: The Agent launches jobs with the specified `cwd`, environment variables, and `CUDA_VISIBLE_DEVICES` set by the scheduler.
- **No Uploads**: The CLI does not bundle or upload code. It only sends the execution context (command and `cwd`) to the Controller.

## Architecture
- **Controller**: REST API, SQLite state, scheduler loop.
- **Agent**: Heartbeat, GPU inventory, job execution/cleanup.
- **CLI**: Submission, status, and log tracking.

## API Summary
See [docs/api.md](docs/api.md) for details.

## License
Apache License 2.0. See [LICENSE](LICENSE) for details.
