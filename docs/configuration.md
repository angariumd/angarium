# Configuration Guide

Angarium is designed to be "zero-config" for local testing, but here is how you set up a real cluster.

## Controller Configuration

The `controller.yaml`. This is where the state lives.

```yaml
# config/controller.yaml
addr: "localhost:8080" # Where the controller listens for API requests
db_path: "/var/lib/angarium/angarium.db" # Path to the SQLite database
# Secret token for agents to join
shared_token: "your-very-secure-agent-secret" # Must match agent's shared_token
# Secret token for users (CLI)
users:
  - id: "user-1" # Unique identifier for this user
    name: "User 1" # Display name for this user
    token: "user-access-token" # Used by CLI to authenticate
```

## Agent Configuration

Each GPU node runs an agent.

```yaml
# config/agent.yaml
controller_url: "http://controller-ip:8080" # URL of the controller
shared_token: "your-very-secure-agent-secret" # Must match controller's shared_token
node_id: "node-1" # Unique identifier for this node
addr: "localhost:8081" # Agent listens on this port for controller
```

## CLI Configuration

The CLI looks for a config in `~/.config/angarium/config.yaml`.

```yaml
controller_url: "http://controller-ip:8080" # URL of the controller
token: "user-access-token" # Must match controller's users token
```

> [!TIP]
> You can also use environment variables:
> `export ANGARIUM_CONTROLLER=http://...`
> `export ANGARIUM_TOKEN=...`
