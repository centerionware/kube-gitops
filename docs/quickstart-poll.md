# Quickstart: Poll Mode

Poll mode is the simplest way to get started. The operator periodically queries
your git platform's API for open pull requests. No public ingress required, no
webhook configuration on the platform side.

The tradeoff is latency — a new PR won't be picked up until the next poll cycle
(default 2 minutes). For most teams this is fine.

---

## Prerequisites

- kube-gitops running in your cluster
- A personal access token for your git platform with read access to pull requests
- Your app repository accessible from inside the cluster

---

## Step 1 — Create the git secret

The secret must have a `username` and `password` key. The `password` value is
your personal access token, not your account password.

```bash
kubectl create secret generic myapp-git-secret \
  -n kube-deploy \
  --from-literal=username=myusername \
  --from-literal=password=<your-token>
```

**GitHub:** Generate at Settings → Developer settings → Personal access tokens.
Minimum scope: `repo` (for private repos) or `public_repo` (for public repos).

**GitLab:** Generate at User Settings → Access Tokens.
Minimum scope: `read_api`.

**Gitea / Forgejo:** Generate at Settings → Applications → Manage Access Tokens.
Minimum permission: Repository — Read.

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
    mode: poll
    pollInterval: 2m

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

## Step 3 — Verify

```bash
kubectl get gr -n kube-deploy
```

You should see Phase `Ready` and LastPollTime updating every 2 minutes.

When a PR is opened by a trusted author, a `PRDeployment` will appear:

```bash
kubectl get prd -n kube-deploy
```

The preview URL will be in the `URL` column once the build completes.

---

## Notes

- Poll mode requires the `password` key in the git secret. SSH keys cannot be
  used for API calls.
- The operator only polls when the pod is running. If it restarts, it will
  catch up on the next reconcile.
- PRs that fail the trust policy are silently ignored — no error, no noise.
