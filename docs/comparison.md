# Why Slurm is overkill (and K8s is a pain)

If you have a small lab with 4-10 nodes and a handful of researchers, you're in a weird spot. 

You're too big to rely on "SSH in and hope nobody's using GPU 0," but you're way too small to justify the headache of Slurm or the overhead of Kubernetes. 

We built Angarium to fix that.

### Slurm is built for a different scale
Slurm assumes you have InfiniBand, fair-share algorithms for thousands of users, and a dedicated sysadmin to babysit `slurm.conf`. If you just want to run a training script without stepping on your labmate's toes, Slurm is massive overkill. You shouldn't need a PhD in Systems Administration to manage a dozen GPUs.

### Kubernetes is built for web apps
Kubernetes is brilliant for scale, but it's miserable for research. 
Writing Dockerfiles for every single experiment, fighting `CrashLoopBackOff` errors, and messing with PVCs just to see your weights folder... it’s constant friction. Researchers want the CLI, the shared drive, and the bare metal.

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
