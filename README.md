# exeunt

<p align="center">
  <img src="exeunt-pufferfish.png" alt="Three pufferfish heading for the exit" width="600">
</p>

*"Exeunt all."* — stage direction for when everyone leaves.

Self-hosted GitHub Actions runners that provision themselves. A [Runner Scale Set](https://docs.github.com/en/actions/concepts/runners/runner-scale-sets) autoscaler spins up runners on your own hardware, runs the job, and tears them down. No Kubernetes required — just Linux boxes with Docker and [Tailscale](https://tailscale.com).

No setup actions, no provisioning jobs, no teardown steps. Just `runs-on`:

```yaml
jobs:
  build:
    runs-on: exe
    steps:
      - uses: actions/checkout@v5
      - run: make test
```

The runner image comes with Go, Python, Ruby, Rust, Node, and a pile of CLI tools pre-installed via [mise](https://mise.jdx.dev). Networking between your servers goes through [Tailscale](https://tailscale.com).

## How it works

```
            GitHub Actions
            Scale Set API
           (long-poll session)
                  │
                  ▼
          ┌───────────────┐
          │  Autoscaler   │  exeunt.exe.xyz
          │  (listener)   │  (exe.dev VM)
          └───────┬───────┘
                  │
        Tailscale │ SSH
           mesh   │
                  ▼
          ┌───────────────┐
          │  boxloader    │
          │  Docker       │  your hardware
          │  16 CPU / 62G │
          └───────────────┘
```

The autoscaler uses GitHub's [Runner Scale Set API](https://github.com/actions/scaleset) (`github.com/actions/scaleset`) — the same API that powers [Actions Runner Controller](https://github.com/actions/actions-runner-controller), but without Kubernetes.

1. **Registration**: Creates a scale set per repo with a name (e.g., `exe`) that workflows target via `runs-on: exe`
2. **Polling**: Long-poll message session with GitHub — receives job assignments, start/complete events
3. **Provisioning**: Spins up Docker containers on your hardware via Tailscale SSH
4. **Job binding**: GitHub assigns specific jobs to specific runners at JIT config time — no job stealing
5. **Cleanup**: Destroys the container when the job finishes

Multiple scale sets run concurrently in a single process, sharing backend capacity. Supports multiple repos across different GitHub orgs.

## Adding a server

Probe your hardware and generate config:

```bash
scripts/add-host.sh <hostname>
```

This SSHs to the host, checks CPU/RAM/disk/GPU, calculates runner capacity (75% of resources, 25% reserved for the host), and updates `deploy/config.local.json`.

## Configuration

```json
{
  "scale_sets": [
    {
      "registration_url": "https://github.com/your-org/your-repo",
      "name": "exe",
      "labels": ["exe", "exe-gpu"]
    }
  ],
  "runner_image": "ghcr.io/metcalfc/exeunt-runner:latest",
  "backends": [
    {
      "name": "boxloader",
      "type": "docker",
      "host": "boxloader",
      "user": "metcalfc",
      "max_runners": 6,
      "labels": ["exe", "exe-gpu"]
    }
  ]
}
```

Environment variables:
- `AUTOSCALER_GITHUB_TOKEN` — PAT with `repo` and `admin:org` scopes (required)
- `AUTOSCALER_CONFIG` — path to config file (default: `/etc/exeunt-autoscaler/config.json`)

## Deploying

```bash
make deploy              # build, upload, restart autoscaler
make deploy-monitor      # deploy the health monitor (5-min timer)
make deploy-alert-responder  # deploy email-triggered Claude investigation
make deploy-all          # all of the above
make status              # check autoscaler status
make logs                # tail autoscaler logs
```

## Monitoring

A systemd timer runs health checks every 5 minutes and emails alerts:

- Autoscaler process health and HTTP endpoint
- Error rate spikes (>10 errors in 10 min)
- Boxloader connectivity and disk space
- Orphaned containers (containers not tracked by the autoscaler)

Alerts go to email with `[exeunt]` subject prefix. Forward an alert to `alerts@exeunt.exe.xyz` to trigger an automated Claude investigation that diagnoses the issue and replies with findings.

## Claude Code commands

- `/debug-runner` — investigate a stuck or failed CI job
- `/health-check` — proactive infrastructure audit

## License

MIT
