# API Specification

All endpoints are prefixed with `/v1`. Authentication is required for all non-internal routes via a Bearer token. Internal agent routes require `X-Agent-Token`.

## Controller APIs

### Agent Heartbeat
- **POST** `/v1/agent/heartbeat`
- **Internal Only**: Requires `X-Agent-Token`.
- **Payload**:
  ```json
  {
    "node_id": "string",
    "agent_version": "string",
    "addr": "http://node-ip:8091",
    "gpus": [
      {
        "uuid": "GPU-...",
        "idx": 0,
        "name": "NVIDIA A100",
        "memory_mb": 40960,
        "health": "OK",
        "utilization": 0,
        "memory_used_mb": 0
      }
    ],
    "active_job_ids": ["uuid-1", "uuid-2"],
    "memory_total_mb": 128000,
    "memory_used_mb": 4096
  }
  ```

### Job State Update
- **POST** `/v1/agent/jobs/{id}/state`
- **Internal Only**: Requires `X-Agent-Token`.
- **Payload**:
  ```json
  {
    "state": "RUNNING",
    "exit_code": 0
  }
  ```

### Job Submission
- **POST** `/v1/jobs`
- **Payload**:
  ```json
  {
    "gpu_count": 1,
    "priority": 10,
    "command": "python train.py",
    "cwd": "/shared/project",
    "env": {"CUDA_LAUNCH_BLOCKING": "1"},
    "max_runtime_minutes": 60
  }
  ```
- **Response**: `{"id": "uuid"}`

### Job Management
- **GET** `/v1/jobs`: List all jobs. Returns an array of Job objects.
- **GET** `/v1/jobs/{id}/logs`: Stream logs from the assigned Agent. Supports `?follow=true`.
- **GET** `/v1/jobs/{id}/events`: List events for a specific job (submission, allocation, state changes).
- **POST** `/v1/jobs/{id}/cancel`: Signal cancellation to the assigned Agent.

### Cluster Status
- **GET** `/v1/nodes`: List nodes and GPU availability.
- **Response**: Array of Node objects containing GPU counts, health, and memory metrics.

### Identity
- **GET** `/v1/whoami`: Returns the current authenticated user's identity.

## Agent APIs (Internal)

These are called by the Controller or proxied through it. All require `X-Agent-Token`.

### Job Launch
- **POST** `/v1/agent/launch`
- **Payload**:
  ```json
  {
    "job": { ...job_object... },
    "gpu_uuids": ["GPU-uuid-1"]
  }
  ```

### Job Terminate
- **POST** `/v1/agent/terminate`
- **Payload**: `{"job_id": "uuid"}`

### Running Jobs
- **GET** `/v1/agent/running`
- **Response**: `[{"job_id": "uuid", "pid": 1234}]` (Used for reconciliation).

### Agent Logs
- **GET** `/v1/agent/jobs/{id}/logs`
- **Query**: `?follow=true` for streaming.
- **Response**: Plain text log stream.
