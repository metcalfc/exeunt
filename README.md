# exeunt

<p align="center">
  <img src="exeunt-pufferfish.png" alt="Three pufferfish heading for the exit" width="600">
</p>

*"Exeunt all."* — stage direction for when everyone leaves.

Ephemeral self-hosted GitHub Actions runners. VMs spin up, do the work, and exit. Hence the name.

Runs on your own hardware (bare metal, home lab) with [exe.dev](https://exe.dev) cloud VMs as automatic fallback. Connected via [Tailscale](https://tailscale.com). Authenticated via GitHub OIDC — zero stored secrets.

(Not to be confused with [exeuntu](https://github.com/boldsoftware/exeuntu), the exe.dev base image. Yes, we noticed.)

## Architecture

Pick names for your machines. We like pufferfish — `porcupine`, `hedgehog`, `balloonfish` — but anything works. You'll need:

- **An exe.dev VM** for the autoscaler (we'll call ours `porcupine`)
- **One or more bare metal servers** with Docker (we'll call ours `hedgehog`)

```
GitHub webhook (workflow_job)
        │
        ▼
┌─────────────────┐
│   Autoscaler    │  porcupine.exe.xyz
│   :8080/webhook │  (persistent exe.dev VM)
└────────┬────────┘
         │ routes by label + priority
         ├──────────────────────────┐
         ▼                          ▼
┌─────────────────┐      ┌──────────────────┐
│  Docker Backend │      │  exe.dev Backend │
│  (bare metal)   │      │  (cloud VMs)     │
│  priority: 1    │      │  priority: 10    │
│  tailscale ssh  │      │  ssh exe.dev     │
└─────────────────┘      └──────────────────┘
   hedgehog                exe.dev VMs
   your hardware           shared cloud
   FREE                    ~$20/mo
```

Jobs land on your hardware first (free, fast, more resources). If it's full or unreachable, they fall back to exe.dev automatically.

## Quick start

With the autoscaler running, any workflow just uses `runs-on`:

```yaml
jobs:
  build:
    runs-on: [self-hosted, exe]
    steps:
      - uses: actions/checkout@v5
      - run: echo "Running on bare metal or cloud VM"
```

No provision/teardown jobs. The autoscaler handles the full lifecycle via webhooks.

## Labels

| Label | Routes to | Use case |
|-------|-----------|----------|
| `exe` | Best available (bare metal first, then exe.dev) | Default for all jobs |
| `exe-large` | Bare metal only | CPU/memory-intensive builds |
| `exe-gpu` | Bare metal with GPU | GPU workloads |
| `exe-builder` | Persistent builder VM | Image builds (Docker cache persists) |

## Setup

### What you need

- A server with Docker (the bigger the better — `hedgehog` in our examples)
- An [exe.dev](https://exe.dev) account with SSH key
- A [Tailscale](https://tailscale.com) tailnet
- A GitHub repo

### 1. Prepare your server (`hedgehog`)

Your bare metal server needs Docker and Tailscale.

```bash
# Install Docker (if not already)
curl -fsSL https://get.docker.com | sh
sudo usermod -aG docker $USER

# Install Tailscale and join your tailnet
curl -fsSL https://tailscale.com/install.sh | sh
sudo tailscale up --ssh
```

Tag `hedgehog` as `tag:exework` in the [Tailscale admin](https://login.tailscale.com/admin/machines).

Pull the runner image:

```bash
docker pull ghcr.io/metcalfc/exeunt-runner:latest
```

Set up hourly image pulls so new builds are ready instantly:

```bash
cp scripts/pull-runner-image.sh ~/bin/
chmod +x ~/bin/pull-runner-image.sh
(crontab -l 2>/dev/null; echo '0 * * * * ~/bin/pull-runner-image.sh >> ~/.local/log/pull-runner-image.log 2>&1') | crontab -
```

### 2. Create the autoscaler VM (`porcupine`)

The autoscaler runs on a persistent exe.dev VM.

```bash
# Create the VM
ssh exe.dev new --name=porcupine --image=ghcr.io/metcalfc/exeunt-runner:latest --no-email

# Join the tailnet (Tailscale is pre-installed in the image)
ssh porcupine.exe.xyz 'sudo systemctl enable --now tailscaled && sudo tailscale up --authkey=YOUR_AUTH_KEY --hostname=porcupine --ssh'
```

Tag `porcupine` as `tag:exedev` (this happens automatically if your auth key is tagged).

### 3. Set up Tailscale OIDC (zero secrets)

Tailscale supports [GitHub Actions OIDC](https://tailscale.com/docs/features/workload-identity-federation) — no stored secrets needed for runner auth.

1. Go to [Tailscale admin > Trust credentials](https://login.tailscale.com/admin/settings/trust-credentials)
2. Create an **OpenID Connect** credential
3. Issuer: **GitHub Actions**
4. Subject: `repo:your-org/*:*` (covers all repos in your org)
5. Scopes: **Auth Keys** (write) + **Devices Core** (write)
6. Save — you get a **Client ID** and **Audience** (neither is a secret)

### 4. Tailscale ACLs

Add these to your [Tailscale ACL policy](https://login.tailscale.com/admin/acls) so the autoscaler VM can reach your servers:

```json
"tagOwners": {
  "tag:exedev":  ["autogroup:admin"],
  "tag:exework": ["autogroup:admin"]
},
"ACLs": [
  {"Action": "accept", "Users": ["tag:exedev"], "Ports": ["tag:exework:*"]}
],
"ssh": [
  {"action": "accept", "src": ["tag:exedev"], "dst": ["tag:exework"], "users": ["autogroup:nonroot", "root"]},
  {"action": "check",  "src": ["autogroup:member"], "dst": ["tag:exework"], "users": ["autogroup:nonroot", "root"]}
]
```

- `tag:exedev` → autoscaler VM (`porcupine`)
- `tag:exework` → bare metal servers (`hedgehog`, etc.)
- The second SSH rule lets you personally SSH to your servers too

### 5. Autoscaler secrets

Create `/etc/exeunt-autoscaler/env` on `porcupine`:

```bash
AUTOSCALER_WEBHOOK_SECRET=<generate with: openssl rand -hex 32>
AUTOSCALER_GITHUB_TOKEN=<github PAT with repo scope>
```

These are the only two secrets in the system. Everything else uses OIDC.

### 6. Config

```bash
cp deploy/config.json deploy/config.local.json
```

Edit `deploy/config.local.json` with your hosts (this file is gitignored):

```json
{
  "repo": "your-org/your-repo",
  "port": 8080,
  "runner_image": "ghcr.io/metcalfc/exeunt-runner:latest",
  "backends": [
    {
      "name": "hedgehog",
      "type": "docker",
      "host": "hedgehog",
      "user": "youruser",
      "max_runners": 5,
      "labels": ["exe", "exe-large", "exe-gpu"],
      "priority": 1
    },
    {
      "name": "exe.dev",
      "type": "exedev",
      "max_runners": 5,
      "labels": ["exe"],
      "priority": 10
    }
  ]
}
```

Lower priority = preferred. If `hedgehog` fails or is full, jobs fall back to exe.dev.

### 7. Deploy

```bash
make deploy                           # deploys to porcupine (default HOST)
```

### 8. GitHub webhook

Add a webhook in your repo settings (`Settings > Webhooks`):

- **URL:** `https://porcupine.exe.xyz/webhook`
- **Content type:** `application/json`
- **Secret:** same as `AUTOSCALER_WEBHOOK_SECRET`
- **Events:** select **Workflow jobs** only

Make the endpoint public:

```bash
ssh exe.dev share set-public porcupine
ssh exe.dev share port porcupine 8080
```

### 9. Register the persistent builder (optional)

If you want a persistent runner for image builds (Docker cache persists between builds):

```bash
ssh porcupine.exe.xyz 'sudo -u exedev bash -c "
  cd ~/actions-runner
  ./config.sh --url https://github.com/YOUR/REPO --token YOUR_RUNNER_TOKEN \
    --name porcupine --labels self-hosted,exe-builder --unattended --replace
  nohup ./run.sh > runner.log 2>&1 &
"'
```

Name it outside the `exeunt-*` pattern so the reaper won't touch it.

## Operations

### Deploy and manage

```bash
make deploy                  # build, push binary + config + systemd unit, restart
make restart                 # rebuild and hot-swap the binary
make start / stop            # start or stop the service
make status                  # systemd status
make logs                    # last 50 journal lines
make test                    # run unit tests
HOST=porcupine.exe.xyz make status  # explicit host
```

### Monitoring

```bash
# Health check
curl https://porcupine.exe.xyz/healthz
# {"status":"ok","uptime":"2h15m","active_vms":1,"backends":2}

# Detailed status
curl https://porcupine.exe.xyz/status
# {"active_vms":[...],"count":1,"backends":2,"uptime":"2h15m"}

# Follow logs
make logs
```

### Upgrading

```bash
git pull && make deploy       # full deploy with config
git pull && make restart      # binary only, keep config
```

### Adding a server

1. Install Docker and Tailscale on the new server
2. `tailscale up --ssh`, tag as `tag:exework` in Tailscale admin
3. Install the hourly image pull cron (`scripts/pull-runner-image.sh`)
4. Add an entry to `deploy/config.local.json`
5. `make deploy`

### Runner image

Built from a [fork of exeuntu](https://github.com/metcalfc/exeuntu) with:

- GitHub Actions runner binary
- [mise](https://mise.jdx.dev) toolchains: Go, Python, Ruby, Rust, Node + CLI tools
- Tailscale for private networking
- Claude Code and Codex
- Docker, git, and standard dev tooling

Toolchains are split into tiered Docker layers:
- **Tier 1:** Heavy runtimes (Rust, Ruby, Go, Python, Node) — own layers, rarely rebuild
- **Tier 2:** CLI tools (buf, ruff, golangci-lint, etc.) — single layer, cheap to update

### Automated maintenance

| Workflow | Schedule | What it does |
|----------|----------|--------------|
| `sync-exeuntu-fork` | Daily 05:00 UTC | Rebases fork onto upstream, triggers build on changes |
| `build-runner-image` | Daily 06:00 UTC | Builds image with latest runner version, pushes to GHCR |
| `reaper` | Every 15 min | Destroys orphaned `exeunt-*` VMs (safety net) |

### Reaper

Runs every 15 minutes. Cross-references exe.dev VMs against registered GitHub runners. Any `exeunt-*` VM without an active runner gets destroyed. Catches failed teardowns, cancelled workflows, and autoscaler crashes.

Your persistent autoscaler VM is named outside the `exeunt-*` pattern (e.g., `porcupine`), so the reaper ignores it.

## Composite actions (alternative to autoscaler)

If you don't want the autoscaler, you can use the composite actions directly. This requires three jobs per workflow (provision → work → teardown).

### `setup-exe-runner`

| Input | Required | Default | Description |
|-------|----------|---------|-------------|
| `exe-ssh-key` | yes | | SSH private key (ed25519) for exe.dev |
| `github-token` | yes | | PAT with `repo` scope for JIT runner config |
| `labels` | no | `exe` | Comma-separated runner labels |
| `image` | no | `ghcr.io/metcalfc/exeunt-runner:latest` | Container image for the VM |

### `teardown-exe-runner`

| Input | Required | Description |
|-------|----------|-------------|
| `exe-ssh-key` | yes | SSH private key for exe.dev |
| `vm-name` | yes | VM name from setup output |

## License

MIT
