# API Specification

## Controller APIs

### Agent Heartbeat
- **POST** `/api/v1/heartbeat`
- **Payload**:
  ```json
  {
    "node_id": "uuid",
    "hostname": "string",
    "address": "http://node-ip:8081",
    "gpus": [
      {
        "uuid": "GPU-...",
        "index": 0,
        "name": "A100",
        "memory_mb": 40960,
        "health": "OK"
      }
    ]
  }
  ```

### Job Submission (CLI)
- **POST** `/api/v1/jobs`
- **Payload**:
  ```json
  {
    "gpu_count": 2,
    "priority": 10,
    "command": "python train.go",
    "env": {"FOO": "BAR"},
    "cwd": "/home/user/project" 
  }
  ```
  > [!IMPORTANT]
  > `cwd` must be a path accessible on all GPU nodes (e.g., on a shared filesystem). The system will NOT upload any code to this directory.

### Job Status
- **GET** `/api/v1/jobs/:id`

## Agent APIs (Internal to Controller)

### Job Launch
- **POST** `/api/v1/launch`
- **Payload**:
  ```json
  {
    "job_id": "uuid",
    "command": "string",
    "gpu_indices": [0, 1],
    "env": {},
    "cwd": "string"
  }
  ```

### Job Terminate
- **POST** `/api/v1/terminate/:id`
