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

kube-gitops is designed to run inside the same cluster as kube-deploy. The entire
installation — CRDs, namespace, RBAC, and the operator itself — is expressed as a
single kube-deploy `App` CR. kube-deploy's `resources` field applies any raw
Kubernetes objects alongside the deployment, so nothing needs to be pre-applied manually.

**Prerequisites:** kube-deploy running in the cluster (BuildKit + registry).
That's it.

Edit the `ingress.host` (or switch to `gateway`), then apply:

```bash
kubectl apply -f https://raw.githubusercontent.com/centerionware/kube-gitops/main/deploy.yaml
```

`deploy.yaml`:

```yaml
apiVersion: kube-deploy.centerionware.app/v1alpha1
kind: App
metadata:
  name: kube-gitops
  namespace: kube-deploy   # deploy into kube-deploy's own namespace — no extra ns needed
spec:
  repo: https://github.com/centerionware/kube-gitops
  updateInterval: 10m

  build:
    baseImage: golang:1.22-alpine
    installCmd: go mod download
    buildCmd: go build -trimpath -ldflags="-s -w" -o /app/kube-gitops ./main.go
    # gitSecret: kube-gitops-git-secret   # uncomment if repo is private

  run:
    command: ["/app/kube-gitops"]
    port: 8080
    replicas: 1
    serviceAccountName: kube-gitops
    resources:
      cpuRequest: 50m
      memoryRequest: 64Mi
      cpuLimit: 200m
      memoryLimit: 128Mi

  # Expose the webhook endpoint so git platforms can reach it.
  # Use ingress OR gateway — not both.
  ingress:
    enabled: true
    host: gitops.example.com   # <-- change this
    className: nginx
    # tlsSecret: gitops-tls

  # gateway:
  #   enabled: true
  #   gatewayRef:
  #     name: main-gateway
  #   hostnames:
  #     - gitops.example.com

  env:
    LOG_DEV_MODE: "false"

  # Everything below is applied to the cluster before the operator starts.
  # CRDs, RBAC, and the ServiceAccount are fully self-contained here —
  # no kubectl pre-steps required.
  resources:

    # ── ServiceAccount ─────────────────────────────────────────────
    - apiVersion: v1
      kind: ServiceAccount
      metadata:
        name: kube-gitops
        namespace: kube-deploy

    # ── ClusterRole ────────────────────────────────────────────────
    - apiVersion: rbac.authorization.k8s.io/v1
      kind: ClusterRole
      metadata:
        name: kube-gitops
      rules:
        - apiGroups: ["kube-gitops.centerionware.app"]
          resources: ["gitrepos", "gitrepos/status", "prdeployments", "prdeployments/status"]
          verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
        - apiGroups: ["kube-deploy.centerionware.app"]
          resources: ["apps", "apps/status", "containerapps", "containerapps/status"]
          verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
        - apiGroups: [""]
          resources: ["secrets"]
          verbs: ["get", "list", "watch"]
        - apiGroups: [""]
          resources: ["events"]
          verbs: ["create", "patch"]
        - apiGroups: ["networking.k8s.io"]
          resources: ["ingresses"]
          verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
        - apiGroups: ["gateway.networking.k8s.io"]
          resources: ["httproutes"]
          verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]

    # ── ClusterRoleBinding ─────────────────────────────────────────
    - apiVersion: rbac.authorization.k8s.io/v1
      kind: ClusterRoleBinding
      metadata:
        name: kube-gitops
      roleRef:
        apiGroup: rbac.authorization.k8s.io
        kind: ClusterRole
        name: kube-gitops
      subjects:
        - kind: ServiceAccount
          name: kube-gitops
          namespace: kube-deploy

    # ── GitRepo CRD ────────────────────────────────────────────────
    - apiVersion: apiextensions.k8s.io/v1
      kind: CustomResourceDefinition
      metadata:
        name: gitrepos.kube-gitops.centerionware.app
      spec:
        group: kube-gitops.centerionware.app
        scope: Namespaced
        names:
          plural: gitrepos
          singular: gitrepo
          kind: GitRepo
          shortNames: ["gr"]
        versions:
          - name: v1alpha1
            served: true
            storage: true
            subresources:
              status: {}
            additionalPrinterColumns:
              - name: Platform
                type: string
                jsonPath: .spec.platform
              - name: Mode
                type: string
                jsonPath: .spec.trigger.mode
              - name: Phase
                type: string
                jsonPath: .status.phase
              - name: ActivePRs
                type: integer
                jsonPath: .status.activePrDeployments
              - name: Age
                type: date
                jsonPath: .metadata.creationTimestamp
            schema:
              openAPIV3Schema:
                type: object
                properties:
                  spec:
                    type: object
                    required: ["platform", "repo", "gitSecret", "trigger"]
                    properties:
                      platform:
                        type: string
                        enum: ["github", "gitlab", "gitea", "forgejo"]
                      repo:
                        type: string
                      gitSecret:
                        type: string
                      trigger:
                        type: object
                        required: ["mode"]
                        properties:
                          mode:
                            type: string
                            enum: ["webhook", "poll"]
                          pollInterval:
                            type: string
                            default: "2m"
                          webhookSecret:
                            type: string
                          webhookPath:
                            type: string
                      prPolicy:
                        type: object
                        properties:
                          allowedAuthorAssociations:
                            type: array
                            items:
                              type: string
                            default: ["OWNER", "MEMBER", "COLLABORATOR"]
                          requireLabel:
                            type: string
                          allowCommentTrigger:
                            type: boolean
                            default: false
                          commentTriggerPhrase:
                            type: string
                            default: "/deploy"
                          allowedCommenters:
                            type: array
                            items:
                              type: string
                      prDeploy:
                        type: object
                        properties:
                          namespace:
                            type: string
                          nameTemplate:
                            type: string
                            default: "{{.RepoName}}-pr-{{.PRNumber}}"
                          ingressHostTemplate:
                            type: string
                            default: "pr-{{.PRNumber}}.{{.RepoName}}.{{.BaseDomain}}"
                          baseDomain:
                            type: string
                          injectPREnv:
                            type: boolean
                            default: true
                          build:
                            type: object
                            properties:
                              baseImage:
                                type: string
                              installCmd:
                                type: string
                              buildCmd:
                                type: string
                              dockerfileMode:
                                type: string
                                enum: ["auto", "generate", "inline"]
                              dockerfile:
                                type: string
                              output:
                                type: string
                              registry:
                                type: string
                          run:
                            type: object
                            properties:
                              command:
                                type: array
                                items:
                                  type: string
                              port:
                                type: integer
                              replicas:
                                type: integer
                                default: 1
                              registry:
                                type: string
                          ingress:
                            type: object
                            properties:
                              enabled:
                                type: boolean
                                default: true
                              className:
                                type: string
                              tlsSecret:
                                type: string
                              annotations:
                                type: object
                                additionalProperties:
                                  type: string
                          gateway:
                            type: object
                            properties:
                              enabled:
                                type: boolean
                                default: false
                              gatewayRef:
                                type: object
                                required: ["name"]
                                properties:
                                  name:
                                    type: string
                                  namespace:
                                    type: string
                                  sectionName:
                                    type: string
                              tlsSecret:
                                type: string
                              annotations:
                                type: object
                                additionalProperties:
                                  type: string
                          env:
                            type: object
                            additionalProperties:
                              type: string
                  status:
                    type: object
                    x-kubernetes-preserve-unknown-fields: true

    # ── PRDeployment CRD ───────────────────────────────────────────
    - apiVersion: apiextensions.k8s.io/v1
      kind: CustomResourceDefinition
      metadata:
        name: prdeployments.kube-gitops.centerionware.app
      spec:
        group: kube-gitops.centerionware.app
        scope: Namespaced
        names:
          plural: prdeployments
          singular: prdeployment
          kind: PRDeployment
          shortNames: ["prd"]
        versions:
          - name: v1alpha1
            served: true
            storage: true
            subresources:
              status: {}
            additionalPrinterColumns:
              - name: Platform
                type: string
                jsonPath: .spec.platform
              - name: PR
                type: integer
                jsonPath: .spec.prNumber
              - name: Branch
                type: string
                jsonPath: .spec.branch
              - name: Author
                type: string
                jsonPath: .spec.author
              - name: State
                type: string
                jsonPath: .status.state
              - name: AppPhase
                type: string
                jsonPath: .status.appPhase
              - name: URL
                type: string
                jsonPath: .status.url
              - name: Age
                type: date
                jsonPath: .metadata.creationTimestamp
            schema:
              openAPIV3Schema:
                type: object
                properties:
                  spec:
                    type: object
                    required: ["gitRepoRef", "platform", "repoURL", "prNumber", "branch", "headSHA", "author", "appRef", "appNamespace"]
                    properties:
                      gitRepoRef:
                        type: string
                      platform:
                        type: string
                      repoURL:
                        type: string
                      prNumber:
                        type: integer
                      branch:
                        type: string
                      headSHA:
                        type: string
                      author:
                        type: string
                      authorAssociation:
                        type: string
                      title:
                        type: string
                      appRef:
                        type: string
                      appNamespace:
                        type: string
                  status:
                    type: object
                    x-kubernetes-preserve-unknown-fields: true
```

kube-deploy applies the `resources` entries before starting the operator, so by the
time the binary is running, its CRDs and RBAC are already in place. The webhook
endpoint will be available at `https://gitops.example.com/webhook/<namespace>/<gitrepo-name>`.

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
