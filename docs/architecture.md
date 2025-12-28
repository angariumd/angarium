# Architecture Overview

## Components

### Controller
The brain of the cluster.
- **REST API**: Handles CLI requests and Agent heartbeats.
- **Scheduler**: Periodically checks QUEUED jobs and assigns them to available nodes.
- **Database (SQLite)**: Persists state for nodes, GPUs, jobs, and allocations.
- **Lease Manager**: Ensures GPUs are reserved during the `ALLOCATED` -> `STARTING` transition.

### Agent
The executor on each compute node.
- **Inventory**: Detects GPUs via `nvidia-smi` and reports health/memory.
- **Heartbeat**: Periodically reports status to the Controller.
- **Executor**: Launches jobs in their own process groups, setting `CUDA_VISIBLE_DEVICES`.
- **Monitor**: Tracks PID and reports exit codes/failures.

## Job Submission Contract

- **Working Directory (`cwd`)**: Job submission MUST include a `cwd` that exists on all GPU nodes.
- **Shared Filesystem**: Users are responsible for ensuring code and data are available at the specified `cwd` (e.g., via NFS).
- **Execution**: The Agent executes the command by:
  1. Changing directory to `cwd`.
  2. Injecting provided environment variables.
  3. Setting `CUDA_VISIBLE_DEVICES` based on assigned GPU indices.

### CLI
The user interface.
- **Remote Access**: Connects to the Controller via HTTPS using a token (read from env or config).
- **No SSH Required**: Users submit jobs from their local machine; they do not need to SSH into the cluster.
- **Contract**: Only sends the command and `cwd`. Does not upload files.

## Job State Machine

```
stateDiagram-v2
    [*] --> QUEUED
    QUEUED --> ALLOCATED: Scheduler finds node
    ALLOCATED --> STARTING: Agent receives /launch
    STARTING --> RUNNING: Process started
    RUNNING --> SUCCEEDED: Exit 0
    RUNNING --> FAILED: Exit non-zero
    RUNNING --> CANCELED: Manual kill
    RUNNING --> LOST: Node heartbeat stale
```

## Security
- **Bearer Tokens**: Shared secret or generated tokens for CLI and Agent.
- **HTTPS**: Traffic between all components is encrypted.
