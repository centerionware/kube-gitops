# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

```bash
# Build
go build ./...

# Test
go test ./...

# Test a single package
go test ./controllers/...

# Vet
go vet ./...

# Build the binary
go build -o kube-gitops .

# Build the Docker image
docker build -t kube-gitops .

# Deploy to cluster (builds and runs via kube-deploy)
kubectl apply -f deploy.yaml

# Check operator status
kubectl get gr -n kube-deploy
kubectl get prd -n kube-deploy
```

## Architecture

kube-gitops is a Kubernetes operator that creates ephemeral preview environments for pull requests. It delegates all build/image/deployment work to [kube-deploy](https://github.com/centerionware/kube-deploy) and handles only the git-platform side.

### Two CRDs

**`GitRepo`** (`api/v1alpha1/types.go`) — one per watched repository. Configures:
- Which platform (github/gitlab/gitea/forgejo) and repo URL
- Trigger mode: `poll` (periodic API queries) or `webhook` (interrupt-driven)
- Trust policy: which PR authors can trigger deployments
- How to build and expose each PR preview (`prDeploy`)
- Notification templates for PR comments

**`PRDeployment`** — one per open PR. Created by `GitRepoReconciler`, owned by its parent `GitRepo`. Stores PR metadata and references the kube-deploy `App` CR it manages.

### Controller flow

```
GitRepo created
  └── GitRepoReconciler adds finalizer, registers webhook route or starts polling
        └── PR event arrives (webhook POST or poll cycle)
              └── policy.EvaluatePR() checks trust
                    └── PRDeployment CR created in GitRepo's namespace
                          └── PRDeploymentReconciler creates kube-deploy App CR
                                └── Mirrors App status → PRDeployment status
                                      └── Posts PR comment + commit status when ready
PR closed / GitRepo deleted
  └── PRDeployment deleted → finalizer deletes App CR → kube-deploy tears down everything
```

### Package responsibilities

| Package | Role |
|---------|------|
| `main.go` | Sets up controller-runtime manager, wires `GitRepoReconciler` ↔ `webhook.Server` |
| `controllers/gitrepo.go` | `GitRepoReconciler`: poll loops, webhook registration, PRDeployment lifecycle |
| `controllers/pr_deployment.go` | `PRDeploymentReconciler`: creates/deletes kube-deploy App CRs, mirrors status, posts notifications |
| `controllers/platform_*.go` | Platform-specific API calls for listing open PRs and fetching PR head info |
| `builder/app.go` | Constructs kube-deploy `App` CRs from `GitRepo` + `PRDeployment` specs; handles template rendering for names and ingress hostnames |
| `platform/` | Outbound git-platform communication: PR comments, commit statuses, webhook registration/deregistration |
| `webhook/` | HTTP server (`:8080`) + per-platform event parsing; validates HMAC-SHA256 signatures |
| `policy/trust.go` | Evaluates PR trust policy (author association, required labels, comment triggers) |
| `kubedeploy/types.go` | **Manually vendored** kube-deploy API types — kept in sync with `github.com/centerionware/kube-deploy` |
| `api/v1alpha1/` | CRD type definitions (no code generation — `DeepCopyObject` is hand-written) |

### Key design constraints

- **App CRs live in the GitRepo's namespace**, not `prDeploy.namespace`. The git secret must be co-located with the App CR for kube-deploy to find it. `prDeploy.namespace` controls where kube-deploy puts the resulting Deployment/Service/Ingress.
- **No cross-namespace owner references.** Kubernetes silently drops them. The `PRDeploymentReconciler` finalizer is the explicit cleanup path for App CRs.
- **Webhook supersedes poll.** If a `webhook`-mode GitRepo exists for the same platform+repo, any `poll`-mode GitRepo for the same repo enters `Superseded` state and suspends polling.
- **Empty-PR-list guard.** If the platform API returns zero open PRs but active PRDeployments exist, the poll reconciler skips the delete pass entirely (defensive against transient API failures).
- **`app-created` annotation** on PRDeployment prevents the reconciler from creating duplicate App CRs during the window between `Create()` returning and the cache catching up.

### Environment variables

| Var | Default | Purpose |
|-----|---------|---------|
| `ADDR` | `:8080` | HTTP server listen address |
| `EXTERNAL_URL` | — | Public base URL for auto-registering webhooks with platforms (e.g. `https://gitops.example.com`) |
| `LOG_DEV_MODE` | `true` | Set to `"false"` for production JSON logging |

### Secret format

Git credentials secret (referenced by `spec.gitSecret`): must have a `password` key containing the API token. HTTPS token auth is required for API calls; SSH keys alone are not sufficient.

Webhook secret (referenced by `spec.trigger.webhookSecret`): must have a `secret` key.
