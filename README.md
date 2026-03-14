# exeunt

<p align="center">
  <img src="exeunt-pufferfish.png" alt="Three pufferfish heading for the exit" width="600">
</p>

*"Exeunt all."* — stage direction for when everyone leaves.

Self-hosted GitHub Actions runners that provision themselves. A webhook-based autoscaler spins up runners on your own hardware, runs the job, and tears them down. Your servers go first (they're free and fast). If they're busy or offline, [exe.dev](https://exe.dev) cloud VMs pick up the slack.

No setup actions, no provisioning jobs, no teardown steps. Just `runs-on`:

```yaml
jobs:
  build:
    runs-on: [self-hosted, exe]
    steps:
      - uses: actions/checkout@v5
      - run: make test
```

The runner image comes with Go, Python, Ruby, Rust, Node, and a pile of CLI tools pre-installed via [mise](https://mise.jdx.dev). Networking between your servers and exe.dev VMs goes through [Tailscale](https://tailscale.com), authenticated with GitHub OIDC so there are no long-lived secrets to rotate.

## How it works

Pick names for your machines. We like pufferfish (the logo, you see) — `porcupine`, `hedgehog`, `balloonfish` — but anything works.

```
                    GitHub
                  workflow_job
                   webhook
                      │
                      ▼
              ┌───────────────┐
              │  Autoscaler   │  porcupine.exe.xyz
              │  /webhook     │  (exe.dev VM, always on)
              └───────┬───────┘
                      │
            Tailscale │ routes by label + priority
              mesh    │
         ┌────────────┼────────────┐
         ▼            │            ▼
   ┌───────────┐      │     ┌───────────┐
   │ hedgehog  │      │     │  exe.dev  │
   │ Docker    │◄─────┘     │  VMs      │
   │ pri: 1    │            │  pri: 10  │
   │ FREE      │            │  fallback │
   └───────────┘            └───────────┘
    your hardware             cloud VMs
```

The autoscaler receives `workflow_job` webhooks from GitHub. When a job with the `exe` label is queued, it provisions a runner on the best available backend, registers it as an ephemeral JIT runner, and destroys it when the job finishes. If a backend fails mid-provisioning, the next one picks it up automatically.

## Labels

| Label | Where it runs | When to use it |
|-------|---------------|----------------|
| `exe` | Best available — your servers first, exe.dev if needed | Default. Works for everything. |
| `exe-large` | Bare metal only | Heavy builds that need real CPU/RAM |
| `exe-gpu` | Bare metal with GPU | GPU workloads |
| `exe-builder` | Persistent VM (not autoscaled) | Image builds where Docker cache matters |

## Getting started

You need a server with Docker, an [exe.dev](https://exe.dev) account, and a [Tailscale](https://tailscale.com) tailnet.

The full setup is in [docs/setup.md](docs/setup.md) — it walks through preparing your server, creating the autoscaler VM, configuring Tailscale OIDC, and wiring up the webhook.

Once running, see [docs/operations.md](docs/operations.md) for deploying, monitoring, upgrading, and adding more servers.

## Composite actions (no autoscaler)

If you'd rather not run the autoscaler, the repo includes composite actions for manual provisioning. This means three jobs per workflow (provision, work, teardown) instead of one, and only works with exe.dev (not bare metal).

```yaml
jobs:
  provision:
    runs-on: ubuntu-latest
    outputs:
      vm_name: ${{ steps.setup.outputs.vm_name }}
    steps:
      - uses: actions/checkout@v5
      - uses: your-org/exeunt/setup-exe-runner@main
        id: setup
        with:
          exe-ssh-key: ${{ secrets.EXE_SSH_KEY }}
          github-token: ${{ secrets.GH_RUNNER_TOKEN }}

  work:
    needs: provision
    runs-on: [self-hosted, exe]
    steps:
      - uses: actions/checkout@v5
      - run: make test

  teardown:
    runs-on: ubuntu-latest
    needs: [provision, work]
    if: always()
    steps:
      - uses: actions/checkout@v5
      - uses: your-org/exeunt/teardown-exe-runner@main
        with:
          exe-ssh-key: ${{ secrets.EXE_SSH_KEY }}
          vm-name: ${{ needs.provision.outputs.vm_name }}
```

| Action | Inputs |
|--------|--------|
| `setup-exe-runner` | `exe-ssh-key` (required), `github-token` (required), `labels` (default: `exe`), `image` (default: runner image) |
| `teardown-exe-runner` | `exe-ssh-key` (required), `vm-name` (required) |

## License

MIT
