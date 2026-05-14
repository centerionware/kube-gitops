# Example: Full Configuration

This example shows a fully configured `GitRepo` with every field set. It is
intended as a reference, not a starting point — most fields have sensible
defaults and can be omitted.

See the quickstart docs for minimal working examples.

---

## Secrets

```bash
# Git credentials — used for API calls (poll/comment trigger) and clone (build)
kubectl create secret generic myapp-git-secret \
  -n kube-deploy \
  --from-literal=username=myusername \
  --from-literal=password=ghp_yourtoken

# Webhook HMAC secret — used to validate incoming webhook payloads
kubectl create secret generic myapp-webhook-secret \
  -n kube-deploy \
  --from-literal=secret=$(openssl rand -hex 32)
```

---

## GitRepo

```yaml
apiVersion: kube-gitops.centerionware.app/v1alpha1
kind: GitRepo
metadata:
  name: myapp
  namespace: kube-deploy
spec:

  # ── Identity ────────────────────────────────────────────────────
  platform: github
  repo: https://github.com/myorg/myapp
  gitSecret: myapp-git-secret

  # ── Trigger ─────────────────────────────────────────────────────
  trigger:
    mode: webhook
    webhookSecret: myapp-webhook-secret
    # webhookPath: /webhook/custom-path   # override default /webhook/<ns>/<name>
    # pollInterval: 2m                    # only used when mode: poll

  # ── Trust policy ────────────────────────────────────────────────
  # Controls who can trigger a PR deployment.
  # Untrusted PRs are silently ignored.
  prPolicy:
    # Platform-reported author roles that auto-trigger on PR open/push.
    # GitHub: OWNER, MEMBER, COLLABORATOR, CONTRIBUTOR, FIRST_TIME_CONTRIBUTOR, NONE
    # GitLab/Gitea: mapped to MEMBER or NONE
    allowedAuthorAssociations:
      - OWNER
      - MEMBER
      - COLLABORATOR

    # PR must carry this label to be eligible for deployment.
    # Useful as a manual gate for external contributors.
    # Remove this field to deploy all trusted PRs automatically.
    requireLabel: "deploy-preview"

    # Allow /deploy comments to trigger or re-trigger a deployment.
    allowCommentTrigger: true
    commentTriggerPhrase: "/deploy"

    # Explicit list of usernames allowed to use comment triggers.
    # Takes precedence over allowedAuthorAssociations for comment events.
    # Use ["*"] to allow any repository member.
    allowedCommenters:
      - lead-dev
      - ci-bot

  # ── PR notifications ─────────────────────────────────────────────
  # Comments posted to the PR thread and commit statuses on the head SHA.
  # All templates support Go template syntax.
  # Available vars: .URL .Error .PRNumber .Branch .SHA .Author .RepoName .Title
  notify:
    onDeploy: true
    onError: true
    onClose: false   # set true to post a comment when the preview is torn down

    deployTemplate: |
      🚀 **Preview deployed** for `{{.Branch}}`

      **URL:** {{.URL}}
      **Commit:** `{{.SHA}}`

      _Deployed by kube-gitops_

    errorTemplate: |
      ❌ **Preview deployment failed** for `{{.Branch}}`

      **Error:** {{.Error}}
      **Commit:** `{{.SHA}}`

      Check the operator logs or run:
      ```
      kubectl get prd -n kube-deploy -l kube-gitops.centerionware.app/pr-number={{.PRNumber}}
      ```

    closeTemplate: "🧹 Preview environment for `{{.Branch}}` removed."

  # ── Deployment template ──────────────────────────────────────────
  # Defines how each PR preview is built and exposed.
  # Every field here becomes part of the generated kube-deploy App CR.
  prDeploy:

    # Namespace to create App CRs in. Created automatically if it doesn't exist.
    namespace: pr-previews

    # Go template for the App CR name.
    # Vars: .RepoName .PRNumber .BranchSlug
    nameTemplate: "{{.RepoName}}-pr-{{.PRNumber}}"

    # Go template for the preview ingress hostname.
    # Vars: .RepoName .PRNumber .BranchSlug .BaseDomain
    ingressHostTemplate: "pr-{{.PRNumber}}.{{.RepoName}}.{{.BaseDomain}}"

    # Substituted as .BaseDomain in ingressHostTemplate.
    baseDomain: previews.example.com

    # Inject PR metadata as environment variables into the App.
    # Variables: GITOPS_PR_NUMBER, GITOPS_PR_BRANCH, GITOPS_PR_SHA,
    #            GITOPS_PR_AUTHOR, GITOPS_PR_TITLE, GITOPS_REPO_URL,
    #            GITOPS_PREVIEW_URL
    injectPREnv: true

    # Build overrides — forwarded to kube-deploy App spec.build.
    # gitSecret is always set automatically from spec.gitSecret above.
    build:
      baseImage: node:20-alpine
      installCmd: pnpm install --frozen-lockfile
      buildCmd: pnpm build
      output: dist
      registry: registry.registry.svc.cluster.local:5000
      # dockerfileMode: auto   # auto | generate | inline
      # dockerfile: |          # used when dockerfileMode: inline
      #   FROM node:20-alpine
      #   ...

    # Run overrides — forwarded to kube-deploy App spec.run.
    run:
      port: 3000
      replicas: 1
      command: ["node", "server.js"]

    # Ingress for the preview. Mutually exclusive with gateway.
    ingress:
      enabled: true
      className: nginx
      tlsSecret: wildcard-previews-tls
      annotations:
        nginx.ingress.kubernetes.io/proxy-body-size: "50m"

    # Static environment variables merged into every App.
    # These win over injectPREnv values if keys collide.
    env:
      NODE_ENV: preview
      API_URL: https://api.example.com
```

---

## What this produces

When a PR is opened by a trusted author with the `deploy-preview` label:

1. A `PRDeployment` CR is created in the `kube-deploy` namespace
2. A kube-deploy `App` CR is created in `pr-previews` with:
   - `spec.repo` pointing at the source repository
   - `spec.build.branch` set to the PR head branch
   - `spec.build.gitSecret` forwarded from `myapp-git-secret`
   - `spec.ingress.host` set to e.g. `pr-42.myapp.previews.example.com`
   - All env vars injected including `GITOPS_PR_NUMBER=42` etc.
3. kube-deploy builds the branch, pushes the image, deploys it
4. The operator mirrors the App phase into the PRDeployment status
5. When the App reaches `Running`, the operator:
   - Posts the `deployTemplate` comment on the PR
   - Sets a `success` commit status on the head SHA
6. When the PR is closed, the App CR is deleted and kube-deploy tears down
   everything it owns

---

## Inspecting deployments

```bash
# All monitored repos
kubectl get gr -n kube-deploy

# All active PR previews
kubectl get prd -n kube-deploy

# Detail on a specific preview
kubectl describe prd myapp-pr-42 -n kube-deploy

# The underlying kube-deploy App
kubectl get app myapp-pr-42 -n pr-previews
```
