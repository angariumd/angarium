# Why Slurm is overkill (and K8s is a pain)

Angarium was built because most AI researchers and labs are in a "weird middle ground": 
You're too large for simple SSH scripts, but too small for the massive complexity of Slurm or Kubernetes.

This document dives deeper into why we chose a different path.

## The Angarium Way

Angarium gives you the safety of a queue without the "DevOps" baggage.

| Feature | The "Mental" Sheet | Angarium | Slurm / K8s |
| :--- | :--- | :--- | :--- |
| **Setup** | 0 mins | **< 5 mins** | Days / Weeks |
| **Checking GPUs** | Manual / Gut feeling | **Automatic** | Automatic |
| **Isolation** | Social contract | **Process-level** | Container (Slow) |
| **Ops Overhead** | High (Human error) | **Near-Zero** | "Need an Ops Team" |
| **Vibe** | Periodic Anxiety | **"It just works"** | "Open a ticket" |

### How it actually works
* **No Containers**. We launch standard Linux processes. No Docker tax, no image pulls, zero overhead. If it runs in your shell, it runs in Angarium. Easy to debug with `top` or `ps`.
* **Shared Drive First**. Most labs already have NFS or a shared drive. We don't move your files; we just move where the command runs.
* **GPU-First**. If a GPU is dead or the memory is taken, we don't schedule there. No "zombie" processes eating your VRAM.

Angarium is for the 90% of labs that just need a reliable queue, not a cloud provider.
