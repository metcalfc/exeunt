# Setup

This walks through the full setup from scratch. You'll end up with:

- Your server running Docker containers as GitHub Actions runners
- An exe.dev VM running the autoscaler
- Tailscale connecting everything
- GitHub OIDC so runners join your tailnet without stored secrets

Throughout this guide, `hedgehog` is our bare metal server and `porcupine` is our exe.dev autoscaler VM. Use whatever names you want.

## 1. Prepare your server

Your server needs Docker and Tailscale. That's it.

```bash
# Docker
curl -fsSL https://get.docker.com | sh
sudo usermod -aG docker $USER

# Tailscale
curl -fsSL https://tailscale.com/install.sh | sh
sudo tailscale up --ssh
```

Tag the machine as `tag:exework` in the [Tailscale admin console](https://login.tailscale.com/admin/machines) (click the machine, edit tags).

Pull the runner image so the first job doesn't wait for a download:

```bash
docker pull ghcr.io/metcalfc/exeunt-runner:latest
```

Set up an hourly cron to keep it current. New image builds happen daily — this way your server always has the latest without a cold pull on the first job after a rebuild.

```bash
mkdir -p ~/bin ~/.local/log
cp scripts/pull-runner-image.sh ~/bin/
chmod +x ~/bin/pull-runner-image.sh
(crontab -l 2>/dev/null; echo '0 * * * * ~/bin/pull-runner-image.sh >> ~/.local/log/pull-runner-image.log 2>&1') | crontab -
```

## 2. Create the autoscaler VM

The autoscaler needs to be always-on, so it runs on an [exe.dev](https://exe.dev) VM. The runner image has Tailscale pre-installed.

```bash
ssh exe.dev new --name=porcupine --image=ghcr.io/metcalfc/exeunt-runner:latest --no-email
```

Join it to your tailnet:

```bash
ssh porcupine.exe.xyz 'sudo systemctl enable --now tailscaled && sudo tailscale up --authkey=YOUR_AUTH_KEY --hostname=porcupine --ssh'
```

The auth key should be tagged `tag:exedev` so the VM gets that tag automatically. Create one at [Tailscale admin > Settings > Keys](https://login.tailscale.com/admin/settings/keys) — make it reusable and pre-authorized.

> **Key rotation:** Auth keys expire after 90 days. If you don't want to deal with that, use a [Tailscale OAuth client](https://login.tailscale.com/admin/settings/oauth) instead — those don't expire.

## 3. Tailscale ACLs

The autoscaler VM needs to SSH into your servers. Add these rules to your [Tailscale ACL policy](https://login.tailscale.com/admin/acls):

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

What this does:
- `tag:exedev` (your autoscaler) can reach `tag:exework` (your servers) on any port
- The first SSH rule lets the autoscaler SSH to your servers without interactive auth
- The second rule lets *you* SSH to your servers with the standard Tailscale check

## 4. Tailscale OIDC

This is optional but worth doing. With OIDC, runners join your tailnet using GitHub's identity — no Tailscale secrets stored in your repo.

1. Go to [Tailscale admin > Trust credentials](https://login.tailscale.com/admin/settings/trust-credentials)
2. Create an **OpenID Connect** credential
3. Issuer: **GitHub Actions**
4. Subject: `repo:your-org/*:*`
5. Tags: `tag:exedev` only (must match exactly what the workflow requests)
6. Scopes: **Auth Keys** (write) + **Devices Core** (write)
7. Save — you get a **Client ID** and **Audience**

Neither value is a secret. You can put them directly in workflow files.

**One credential per GitHub org.** If you have repos in `my-org` and `my-other-org`, create two credentials with different subject patterns.

**Tag matching is strict.** The tags on the credential must exactly match what the workflow's `tailscale/github-action` step requests. If the credential has `tag:exedev` and `tag:exework` but the workflow only requests `tag:exedev`, it fails. Keep it to one tag.

To use it in a workflow:

```yaml
permissions:
  id-token: write

steps:
  - uses: tailscale/github-action@v4
    with:
      oauth-client-id: YOUR_CLIENT_ID
      audience: YOUR_AUDIENCE
      tags: tag:exedev
```

## 5. Autoscaler config

Two files live on the autoscaler VM.

**Secrets** in `/etc/exeunt-autoscaler/env`:

```bash
AUTOSCALER_WEBHOOK_SECRET=<generate with: openssl rand -hex 32>
AUTOSCALER_GITHUB_TOKEN=<github classic PAT with repo scope>
```

The GitHub token needs `repo` scope on every repo you want the autoscaler to handle. Classic tokens with `repo` scope work across all repos and orgs your account can access.

**Config** gets deployed from `deploy/config.local.json` (gitignored):

```bash
cp deploy/config.json deploy/config.local.json
```

Edit it:

```json
{
  "repos": [
    "your-org/some-repo",
    "your-org/another-repo",
    "other-org/*"
  ],
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

`repos` controls which repositories the autoscaler responds to. Supports exact names and `path.Match` globs (`*` matches anything). Webhooks from repos not in the list are ignored.

`backends` defines where runners can be provisioned. Lower `priority` number means the autoscaler tries that backend first. If it fails (offline, at capacity, network issues), it moves to the next one.

Backend types:
- `docker` — SSHes to a host via Tailscale, runs `docker run` / `docker rm`
- `exedev` — creates/destroys exe.dev VMs via `ssh exe.dev`

## 6. Deploy

```bash
make deploy
```

This cross-compiles the autoscaler binary, copies it along with the config and systemd unit to the autoscaler VM, and starts the service. The default host is set in the Makefile — change `HOST` at the top or override it:

```bash
HOST=porcupine.exe.xyz make deploy
```

## 7. GitHub webhook

Add a webhook to each repo the autoscaler should handle. Go to `Settings > Webhooks > Add webhook`:

- **Payload URL:** `https://porcupine.exe.xyz/webhook`
- **Content type:** `application/json`
- **Secret:** the `AUTOSCALER_WEBHOOK_SECRET` value
- **Events:** click "Let me select individual events", check **Workflow jobs** only

The exe.dev VM's HTTPS endpoint needs to be public so GitHub can reach it:

```bash
ssh exe.dev share set-public porcupine
ssh exe.dev share port porcupine 8080
```

## 8. Persistent builder (optional)

For image builds, it helps to have a runner whose Docker cache sticks around between builds. Register a persistent runner on the autoscaler VM:

```bash
ssh porcupine.exe.xyz 'sudo -u exedev bash -c "
  cd ~/actions-runner
  ./config.sh --url https://github.com/YOUR/REPO --token YOUR_RUNNER_TOKEN \
    --name porcupine --labels self-hosted,exe-builder --unattended --replace
  nohup ./run.sh > runner.log 2>&1 &
"'
```

Name it something other than `exeunt-*` — the reaper destroys VMs matching that pattern. The autoscaler ignores the `exe-builder` label, so this runner only picks up jobs you explicitly send to it.

## What's next

Trigger a workflow with `runs-on: [self-hosted, exe]` and watch the autoscaler logs:

```bash
make logs
```

You should see the job get queued, a runner provisioned on your server, the job run, and the runner destroyed. The whole cycle takes about 5 seconds on bare metal.

For ongoing operations — deploying updates, monitoring, adding servers — see [operations.md](operations.md).
