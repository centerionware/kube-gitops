# kube-gitops

A Kubernetes operator that monitors git platform pull requests and automatically
generates [kube-deploy](https://github.com/centerionware/kube-deploy) `App` CRs
to produce live PR preview deployments — fully automated, interrupt-driven, secure.

Think of it as the event bridge between your git platform and kube-deploy. It owns
nothing except the `App` objects it creates; everything else (build, image, deployment,
ingress) is kube-deploy's responsibility.

---

## How it works

```
PR opened on GitHub/GitLab/Gitea
  └── Webhook fires (or poll detects it)
        └── Trust policy evaluated (author association, required label, etc.)
              └── PRDeployment CR created
                    └── kube-deploy App CR created (branch pinned to PR head)
                          └── kube-deploy builds + deploys the preview
                                └── PRDeployment status mirrors App phase + URL
PR closed / merged
  └── PRDeployment deleted
        └── kube-deploy App CR deleted
              └── All cluster resources cleaned up by kube-deploy
```

---

## CRDs

### `GitRepo` — defines a monitored repository

```yaml
apiVersion: kube-gitops.centerionware.app/v1alpha1
kind: GitRepo
metadata:
  name: myapp
  namespace: gitops
spec:
  platform: github          # github | gitlab | gitea | forgejo
  repo: https://github.com/org/myapp
  gitSecret: myapp-git-secret   # Secret with username + password (token)

  trigger:
    mode: webhook             # webhook (preferred) | poll
    webhookSecret: myapp-webhook-secret   # Secret with a "secret" key
    # mode: poll
    # pollInterval: 2m

  prPolicy:
    allowedAuthorAssociations: ["OWNER", "MEMBER", "COLLABORATOR"]
    requireLabel: "deploy-preview"        # optional gate
    allowCommentTrigger: true
    commentTriggerPhrase: "/deploy"

  prDeploy:
    namespace: pr-previews
    baseDomain: previews.example.com
    # ingressHostTemplate: "pr-{{.PRNumber}}.{{.RepoName}}.{{.BaseDomain}}"
    # nameTemplate: "{{.RepoName}}-pr-{{.PRNumber}}"

    build:
      baseImage: node:20-alpine
      installCmd: pnpm install
      buildCmd: pnpm build

    run:
      port: 3000
      replicas: 1

    ingress:
      enabled: true
      className: nginx
      # tlsSecret: wildcard-tls

    env:
      NODE_ENV: preview
```

### `PRDeployment` — auto-managed, one per open PR

Created and deleted automatically. Inspect with:

```bash
kubectl get prd -A
kubectl describe prd myapp-pr-42 -n pr-previews
```

---

## Secrets

**Git credentials (HTTPS/token):**

```bash
kubectl create secret generic myapp-git-secret -n gitops \
  --from-literal=username=myuser \
  --from-literal=password=ghp_yourtoken
```

**Git credentials (SSH):**

```bash
kubectl create secret generic myapp-git-secret -n gitops \
  --from-file=ssh-privatekey=~/.ssh/id_ed25519
```

**Webhook HMAC secret:**

```bash
kubectl create secret generic myapp-webhook-secret -n gitops \
  --from-literal=secret=your-random-webhook-secret
```

Set the same value in your git platform's webhook configuration.

---

## Trigger modes

### Webhook (preferred — interrupt-driven)

kube-gitops exposes an HTTP endpoint per GitRepo. When a PR event arrives,
it validates the HMAC-SHA256 signature using the webhook secret, then
immediately creates or deletes a `PRDeployment`.

Register the webhook URL shown in `GitRepo.status.webhookUrl` with your
git platform. Events handled: `pull_request` (opened, synchronize, closed,
labeled, unlabeled) and `issue_comment` (for `/deploy` triggers).

### Poll

The operator polls the platform API on `trigger.pollInterval` (default: `2m`),
diffs open PRs against existing `PRDeployment` objects, and reconciles.
No webhook registration needed; auth comes from `gitSecret`.

---

## Trust policy

kube-gitops does not deploy every PR. The `prPolicy` block controls who can
trigger a deployment:

| Field | Default | Behaviour |
|---|---|---|
| `allowedAuthorAssociations` | `[OWNER, MEMBER, COLLABORATOR]` | Platform-reported roles that auto-trigger |
| `requireLabel` | *(none)* | PR must carry this label |
| `allowCommentTrigger` | `false` | `/deploy` comment triggers a deployment |
| `commentTriggerPhrase` | `/deploy` | The exact comment string |
| `allowedCommenters` | *(from associations)* | Explicit override list; `["*"]` for any member |

PRs that fail the policy are silently ignored (no error, no noise).

---

## Injected environment variables

When `prDeploy.injectPREnv: true` (default), every generated App receives:

| Variable | Value |
|---|---|
| `GITOPS_PR_NUMBER` | PR number |
| `GITOPS_PR_BRANCH` | Head branch name |
| `GITOPS_PR_SHA` | Head commit SHA |
| `GITOPS_PR_AUTHOR` | Author username |
| `GITOPS_REPO_URL` | Repository clone URL |

---

## Deploying kube-gitops with kube-deploy

kube-gitops is designed to run inside the same cluster as kube-deploy,
and the cleanest way to install it is to point kube-deploy at this repo.

### Prerequisites

- kube-deploy running in the cluster (with BuildKit + registry)
- The kube-gitops CRDs applied (see below)
- A namespace for the operator itself (e.g. `gitops`)

### Step 1 — Apply the CRDs

```bash
kubectl apply -f https://raw.githubusercontent.com/centerionware/kube-gitops/main/chart/templates/crd.yaml
```

### Step 2 — Apply RBAC

```bash
kubectl create namespace gitops
kubectl apply -f https://raw.githubusercontent.com/centerionware/kube-gitops/main/chart/templates/rbac.yaml
```

### Step 3 — Launch via kube-deploy

```yaml
apiVersion: kube-deploy.centerionware.app/v1alpha1
kind: App
metadata:
  name: kube-gitops
  namespace: gitops
spec:
  repo: https://github.com/centerionware/kube-gitops

  # kube-gitops is a controller, not a web server — it needs no ingress.
  # updateInterval keeps it tracking main branch for self-updates.
  updateInterval: 10m

  build:
    baseImage: golang:1.22-alpine
    installCmd: go mod download
    buildCmd: go build -trimpath -ldflags="-s -w" -o /app/kube-gitops ./main.go
    # If the repo is private, reference credentials here:
    # gitSecret: kube-gitops-git-secret

  run:
    command: ["/app/kube-gitops"]
    port: 8080          # webhook listener port
    replicas: 1
    serviceAccountName: kube-gitops

    resources:
      cpuRequest: 50m
      memoryRequest: 64Mi
      cpuLimit: 200m
      memoryLimit: 128Mi

  # Expose the webhook endpoint so git platforms can reach it.
  # Use either ingress or gateway depending on your cluster setup.
  ingress:
    enabled: true
    host: gitops.example.com
    className: nginx
    # tlsSecret: gitops-tls

  # --- OR Gateway API ---
  # gateway:
  #   enabled: true
  #   gatewayRef:
  #     name: main-gateway
  #   hostnames:
  #     - gitops.example.com

  env:
    LOG_DEV_MODE: "false"
```

Apply with:

```bash
kubectl apply -f the-above.yaml
```

kube-deploy will clone this repo, build the binary, and run the operator.
The webhook endpoint will be available at `https://gitops.example.com/webhook/<namespace>/<gitrepo-name>`.

---

## Project layout

```
kube-gitops/
├── main.go                          # Operator entrypoint, scheme registration
├── Dockerfile                       # Multi-stage: golang builder → scratch runtime
├── go.mod
│
├── api/v1alpha1/
│   ├── types.go                     # GitRepo + PRDeployment CRD types
│   └── register.go                  # Scheme registration
│
├── controllers/
│   ├── gitrepo_controller.go        # Watches GitRepo, manages poll/webhook
│   └── prdeployment_controller.go   # Owns kube-deploy App CR lifecycle
│
├── internal/
│   ├── kubedeploy/
│   │   ├── types.go                 # Vendored kube-deploy types (App, ContainerApp)
│   │   └── register.go              # Scheme registration for kube-deploy types
│   │
│   ├── platform/
│   │   ├── adapter.go               # Platform adapter interface
│   │   ├── github/                  # GitHub implementation
│   │   ├── gitlab/                  # GitLab implementation
│   │   └── gitea/                   # Gitea/Forgejo implementation
│   │
│   ├── webhook/
│   │   └── server.go                # HTTP server, HMAC validation, event routing
│   │
│   └── appbuilder/
│       └── builder.go               # Constructs kube-deploy App CR from PRDeployment
│
└── chart/
    └── templates/
        ├── crd.yaml                 # GitRepo + PRDeployment CRDs
        └── rbac.yaml                # ServiceAccount, ClusterRole, ClusterRoleBinding
```

---

## Status

| Feature | Status |
|---|---|
| GitRepo CRD | ✅ Defined |
| PRDeployment CRD | ✅ Defined |
| kube-deploy App CR generation | 🔲 In progress |
| GitHub webhook adapter | 🔲 In progress |
| GitLab webhook adapter | 🔲 Planned |
| Gitea/Forgejo webhook adapter | 🔲 Planned |
| Poll mode | 🔲 Planned |
| Trust policy enforcement | 🔲 In progress |
| Comment trigger (`/deploy`) | 🔲 Planned |
| PRDeployment status mirroring | 🔲 Planned |
| Cleanup on PR close | 🔲 Planned |

---

## Developers

centerionware

## License

MIT
