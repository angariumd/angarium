# Configuration Guide

Angarium configuration files are located in `/etc/angarium/` when installed via the official script.

## Controller Configuration

The controller configuration is located at `/etc/angarium/controller.yaml`.

```yaml
# /etc/angarium/controller.yaml
addr: ":8080" # Listen on all interfaces
db_path: "/var/lib/angarium/angarium.db" # Persistent state location
shared_token: "your-agent-secret" # Must match agent's shared_token

# User access tokens for the CLI
users:
  - id: "admin"
    name: "Administrator"
    token: "admin-secret-token"
```

## Agent Configuration

The agent configuration is located at `/etc/angarium/agent.yaml`.

```yaml
# /etc/angarium/agent.yaml
controller_url: "http://controller-ip:8080"
shared_token: "your-agent-secret" # Must match controller
node_id: "" # Optional: defaults to hostname
addr: "http://localhost:8081" # Internal agent API
```

## CLI Configuration

The CLI looks for configuration in `~/.config/angarium/config.yaml`.

```yaml
# ~/.config/angarium/config.yaml
controller_url: "http://controller-ip:8080"
token: "admin-secret-token" # Match one of the tokens in controller's users list
```

---

> [!TIP]
> **Environment Variables**: You can override config settings using environment variables:
> - `ANGARIUM_CONTROLLER_URL` (for Agent/CLI)
> - `ANGARIUM_TOKEN` (for CLI)
