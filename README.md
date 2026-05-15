# kube-gitops

Automatic PR preview deployments for Kubernetes.

When a pull request opens, kube-gitops creates a live preview environment for
it. When the PR closes, the environment is torn down. No manual steps. No
leftover resources.

It sits on top of [kube-deploy](https://github.com/centerionware/kube-deploy)
and delegates all build, image, and deployment work to it. kube-gitops only
handles the git platform side — watching for PRs, enforcing trust policy,
generating the right kube-deploy `App` CR, and posting status back to the PR.

---

## Project Status

Initial development phase should be done, new features and schema changes should be minimal. Everything needs to be thoroughly tested to ensure what's there now works.

---

## Dependencies

- **kube-deploy** must be installed and running in the cluster. kube-gitops
  generates `App` CRs for kube-deploy to act on. Without it nothing deploys.

---

## Quickstart

**1. Edit `deploy.yaml`** — set your ingress host and external URL:

```yaml
  ingress:
    enabled: true
    host: gitops.your-domain.com    # where kube-gitops is reachable
    className: nginx

  env:
    LOG_DEV_MODE: "false"
    EXTERNAL_URL: "https://gitops.your-domain.com"   # same as ingress host
```

**2. Apply it:**

```bash
kubectl apply -f deploy.yaml
```

kube-deploy builds and runs the operator. CRDs, RBAC, and the ServiceAccount
are all created automatically as part of the same manifest.

**3. Create a `GitRepo` to start watching a repository:**

```bash
# Create credentials
kubectl create secret generic myapp-git-secret \
  -n kube-deploy \
  --from-literal=username=myuser \
  --from-literal=password=<your-token>

# Apply a GitRepo
kubectl apply -f - <<YAML
apiVersion: kube-gitops.centerionware.app/v1alpha1
kind: GitRepo
metadata:
  name: myapp
  namespace: kube-deploy
spec:
  platform: github
  repo: https://github.com/myorg/myapp
  gitSecret: myapp-git-secret
  trigger:
    mode: poll
    pollInterval: 2m
  prDeploy:
    namespace: pr-previews
    baseDomain: previews.your-domain.com
    run:
      port: 3000
    ingress:
      enabled: true
      className: nginx
YAML
```

**4. Check status:**

```bash
kubectl get gr -n kube-deploy
kubectl get prd -n kube-deploy
```

Open a PR. A preview environment will appear within one poll cycle.

---

## How it works

```
PR opened
  └── kube-gitops detects it (webhook or poll)
        └── trust policy passes
              └── kube-deploy App CR created (branch pinned to PR head)
                    └── kube-deploy builds, deploys, exposes the preview
                          └── PR receives comment with preview URL
PR closed
  └── App CR deleted
        └── kube-deploy tears down everything it owns
```

---

## Further reading

- [Quickstart: Poll mode](docs/quickstart-poll.md)
- [Quickstart: Webhook mode](docs/quickstart-webhook.md)
- [Full configuration example](docs/example-complex.md)

## AI Disclaimer
This project and all documentation at public release was entirely written by Claude at the direction of one poor and silly engineer.