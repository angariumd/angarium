# API Specification

All endpoints are prefixed with `/v1`. Authentication is required for all non-internal routes via a Bearer token.

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
        "health": "OK"
      }
    ]
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
    "env": {"CUDA_LAUNCH_BLOCKING": "1"}
  }
  ```
- **Response**: `{"id": "uuid"}`

### Job Management
- **GET** `/v1/jobs`: List all jobs.
- **GET** `/v1/jobs/{id}/logs`: Stream logs from the assigned Agent. Supports `?follow=true`.
- **POST** `/v1/jobs/{id}/cancel`: Signal cancellation to the assigned Agent.

### Cluster Status
- **GET** `/v1/nodes`: List nodes and GPU availability.

## Agent APIs (Internal)

These are called by the Controller.

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
- **GET** `/v1/agent/running`: Returns list of active PIDs for reconciliation.
