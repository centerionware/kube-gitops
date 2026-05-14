# Quickstart: Webhook Mode

Webhook mode is the preferred trigger. Your git platform calls kube-gitops
the moment a PR event occurs — no polling delay, no wasted API calls.

The operator will automatically register the webhook with your git platform
when `EXTERNAL_URL` is configured and a `webhookSecret` is provided. You do
not need to manually configure anything on the platform side after that.

---

## Prerequisites

- kube-gitops running with a publicly reachable ingress (`EXTERNAL_URL` set)
- A personal access token with permission to manage webhooks on the repository
- A randomly generated webhook secret

---

## Step 1 — Create secrets

**Git credentials** (token needs webhook management permission in addition to
repo read):

```bash
kubectl create secret generic myapp-git-secret \
  -n kube-deploy \
  --from-literal=username=myusername \
  --from-literal=password=<your-token>
```

**GitHub:** Token scope: `repo` + `admin:repo_hook`

**GitLab:** Token scope: `api`

**Gitea / Forgejo:** Token permission: Repository — Read, Webhooks — Read/Write

**Webhook HMAC secret** (any random string — the operator and the platform
both use this to verify payloads):

```bash
kubectl create secret generic myapp-webhook-secret \
  -n kube-deploy \
  --from-literal=secret=$(openssl rand -hex 32)
```

---

## Step 2 — Create the GitRepo

```yaml
apiVersion: kube-gitops.centerionware.app/v1alpha1
kind: GitRepo
metadata:
  name: myapp
  namespace: kube-deploy
spec:
  platform: github          # github | gitlab | gitea | forgejo
  repo: https://github.com/myorg/myapp
  gitSecret: myapp-git-secret

  trigger:
    mode: webhook
    webhookSecret: myapp-webhook-secret

  prDeploy:
    namespace: pr-previews
    baseDomain: previews.example.com

    run:
      port: 3000

    ingress:
      enabled: true
      className: nginx
```

Apply it:

```bash
kubectl apply -f gitrepo.yaml
```

---

## Step 3 — Verify webhook registration

```bash
kubectl get gr myapp -n kube-deploy
```

The `WEBHOOKURL` column will show the registered endpoint, e.g.:
`https://gitops.example.com/webhook/kube-deploy/myapp`

The operator registers this URL with your platform automatically. You can
confirm it appeared on the platform:

**GitHub:** Repository → Settings → Webhooks

**GitLab:** Repository → Settings → Webhooks

**Gitea / Forgejo:** Repository → Settings → Webhooks

The webhook should show as active with a green tick after the first delivery.

---

## Step 4 — Open a PR

Open a pull request from an account that satisfies the default trust policy
(owner, member, or collaborator). Within seconds:

```bash
kubectl get prd -n kube-deploy
```

A `PRDeployment` will appear with State `deploying`. Once the build completes
the State moves to `running` and the `URL` column shows the preview link.

The operator will also post a comment on the PR and set a commit status.

---

## Platform-specific notes

### GitHub

Events subscribed: `pull_request`, `issue_comment`

The `pull_request` event covers: opened, synchronize (new push), closed,
labeled, unlabeled. The `issue_comment` event covers `/deploy` comment
triggers if `prPolicy.allowCommentTrigger` is enabled.

GitHub delivers to HTTPS only in production. Your ingress must have a valid
TLS certificate or GitHub will refuse to deliver.

### GitLab

Events subscribed: Merge Request Hook, Note Hook

GitLab uses a plain token header (`X-Gitlab-Token`) rather than HMAC. The
`webhookSecret` value is sent as-is and compared directly. This is still
secure as long as the secret is sufficiently random.

GitLab self-hosted instances may have network policies that prevent outbound
webhook delivery to private IP ranges. Check Admin → Network → Outbound
requests if webhooks aren't arriving.

### Gitea / Forgejo

Events subscribed: `pull_request`, `issue_comment`

Gitea uses the same HMAC-SHA256 signature scheme as GitHub
(`X-Gitea-Signature` / `X-Hub-Signature-256`). Forgejo is fully compatible
and uses the same header names.

Self-hosted Gitea instances need to be able to reach your cluster's ingress
from the network where Gitea runs. If both are in the same cluster, use the
internal service URL rather than the external ingress.

---

## If auto-registration is unavailable

If `EXTERNAL_URL` is not set, or your token lacks webhook management
permission, auto-registration is skipped. Register manually:

1. Get the webhook path: `kubectl get gr myapp -n kube-deploy -o jsonpath='{.status.webhookUrl}'`
2. Go to your repository's webhook settings on the platform
3. Add a webhook pointing at that URL
4. Set the content type to `application/json`
5. Enter the same value that's in your `webhookSecret` secret
6. Select the events listed above for your platform
