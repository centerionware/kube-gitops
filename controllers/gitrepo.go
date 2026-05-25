package controllers

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	api "kube-gitops/api/v1alpha1"
	"kube-gitops/builder"
	"kube-gitops/kubedeploy"
	"kube-gitops/platform"
	"kube-gitops/policy"
	"kube-gitops/webhook"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	gitrepoFinalizer    = "kube-gitops.centerionware.app/gitrepo"
	defaultPollInterval = 2 * time.Minute

	// Annotation stored on the GitRepo to track the platform webhook ID
	// so we can deregister it when the GitRepo is deleted.
	annotationWebhookID = "kube-gitops.centerionware.app/webhook-id"
)

// GitRepoReconciler watches GitRepo CRDs and manages:
//   - webhook route registration + platform webhook auto-registration
//   - poll loops
//   - PRDeployment lifecycle in response to events
type GitRepoReconciler struct {
	client.Client
	Scheme        *runtime.Scheme
	WebhookServer *webhook.Server

	// ExternalBaseURL is the publicly reachable base URL of our webhook server,
	// e.g. "https://gitops.centerionware.com". Set from EXTERNAL_URL env var.
	ExternalBaseURL string
}

func SetupGitRepo(mgr ctrl.Manager, r *GitRepoReconciler) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&api.GitRepo{}).
		Owns(&api.PRDeployment{}).
		// Watch all GitRepo changes so when a webhook GitRepo is deleted,
		// any superseded poll GitRepos for the same repo are immediately requeued.
		Watches(&api.GitRepo{}, handler.EnqueueRequestsFromMapFunc(
			func(ctx context.Context, obj client.Object) []reconcile.Request {
				changed := obj.(*api.GitRepo)
				if changed.Spec.Trigger.Mode != "webhook" {
					return nil
				}
				// Find all poll GitRepos watching the same repo and requeue them
				var list api.GitRepoList
				if err := mgr.GetClient().List(ctx, &list); err != nil {
					return nil
				}
				var reqs []reconcile.Request
				for _, gr := range list.Items {
					if gr.Spec.Trigger.Mode == "poll" &&
						gr.Spec.Platform == changed.Spec.Platform &&
						gr.Spec.Repo == changed.Spec.Repo &&
						gr.Name != changed.Name {
						reqs = append(reqs, reconcile.Request{
							NamespacedName: types.NamespacedName{
								Name:      gr.Name,
								Namespace: gr.Namespace,
							},
						})
					}
				}
				return reqs
			},
		)).
		Complete(r)
}

func (r *GitRepoReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var gr api.GitRepo
	if err := r.Get(ctx, req.NamespacedName, &gr); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// ── Deletion ──────────────────────────────────────────────────
	if !gr.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, &gr)
	}

	// ── Finalizer ─────────────────────────────────────────────────
	if !containsString(gr.Finalizers, gitrepoFinalizer) {
		gr.Finalizers = append(gr.Finalizers, gitrepoFinalizer)
		if err := r.Update(ctx, &gr); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// ── Webhook mode ───────────────────────────────────────────────
	if gr.Spec.Trigger.Mode == "webhook" {
		return r.reconcileWebhook(ctx, &gr)
	}

	// ── Poll mode ──────────────────────────────────────────────────
	// Check if a webhook GitRepo exists for the same repo — webhook wins.
	if conflict, name := r.findWebhookConflict(ctx, &gr); conflict {
		msg := fmt.Sprintf("superseded by webhook GitRepo %q for the same repo — polling suspended", name)
		logger.Info("poll suppressed", "reason", msg)
		_ = r.setStatus(ctx, &gr, "Superseded", msg, "")
		return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
	}

	// No conflict — if we were previously superseded, clear it and resume
	if gr.Status.Phase == "Superseded" {
		logger.Info("webhook GitRepo gone, resuming poll", "gitrepo", gr.Name)
	}
	interval := defaultPollInterval
	if gr.Spec.Trigger.PollInterval != "" {
		if d, err := time.ParseDuration(gr.Spec.Trigger.PollInterval); err == nil {
			interval = d
		}
	}

	if err := r.poll(ctx, &gr); err != nil {
		logger.Error(err, "poll failed", "gitrepo", gr.Name)
		_ = r.setStatus(ctx, &gr, "Error", err.Error(), "")
		return ctrl.Result{RequeueAfter: interval}, nil
	}

	_ = r.setStatus(ctx, &gr, "Ready", "poll ok", time.Now().UTC().Format(time.RFC3339))
	return ctrl.Result{RequeueAfter: interval}, nil
}

// reconcileWebhook registers the webhook route locally and on the platform.
func (r *GitRepoReconciler) reconcileWebhook(ctx context.Context, gr *api.GitRepo) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Register with our local HTTP server so incoming requests are routed correctly
	if r.WebhookServer != nil {
		r.WebhookServer.Register(*gr)
	}

	// Build the full public URL for this webhook
	hookURL := r.webhookURL(gr)

	// Update status.webhookUrl so operators can see it in kubectl get gr
	if gr.Status.WebhookURL != hookURL {
		gr.Status.WebhookURL = hookURL
	}

	// Auto-register with the platform if we have a secret and haven't done it yet
	existingID := gr.Annotations[annotationWebhookID]
	if existingID == "" && gr.Spec.Trigger.WebhookSecret != "" && r.ExternalBaseURL != "" {
		token, err := r.loadGitToken(ctx, gr)
		if err != nil {
			logger.Error(err, "cannot load git token for webhook registration")
			_ = r.setStatus(ctx, gr, "Error", fmt.Sprintf("cannot load git token: %v", err), "")
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}

		secret, err := r.loadWebhookSecret(ctx, gr)
		if err != nil {
			logger.Error(err, "cannot load webhook secret")
			_ = r.setStatus(ctx, gr, "Error", fmt.Sprintf("cannot load webhook secret: %v", err), "")
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}

		n, err := platform.New(gr.Spec.Platform, gr.Spec.Repo, token)
		if err != nil {
			logger.Error(err, "cannot create platform notifier")
			_ = r.setStatus(ctx, gr, "Error", err.Error(), "")
			return ctrl.Result{}, nil
		}

		hookID, err := n.RegisterWebhook(ctx, gr.Spec.Repo, hookURL, string(secret))
		if err != nil {
			logger.Error(err, "failed to register webhook with platform")
			gr.Status.WebhookStatus = "failed"
			_ = r.setStatus(ctx, gr, "Error", fmt.Sprintf("webhook registration failed: %v", err), "")
			return ctrl.Result{RequeueAfter: 60 * time.Second}, nil
		}

		logger.Info("registered webhook with platform", "hookID", hookID, "url", hookURL)

		if gr.Annotations == nil {
			gr.Annotations = make(map[string]string)
		}
		gr.Annotations[annotationWebhookID] = hookID
		gr.Status.WebhookID = hookID
		gr.Status.WebhookStatus = "registered"
		if err := r.Update(ctx, gr); err != nil {
			return ctrl.Result{}, err
		}
	} else if existingID != "" {
		// Already registered — mirror the ID into status in case it was lost
		gr.Status.WebhookID = existingID
		gr.Status.WebhookStatus = "registered"
	} else if r.ExternalBaseURL == "" || gr.Spec.Trigger.WebhookSecret == "" {
		// No auto-registration possible — user must register manually
		gr.Status.WebhookStatus = "manual"
	}

	_ = r.setStatus(ctx, gr, "Ready", "webhook ready: "+hookURL, "")
	return ctrl.Result{}, nil
}

// reconcileDelete cleans up: deregisters webhook from platform, removes local route.
func (r *GitRepoReconciler) reconcileDelete(ctx context.Context, gr *api.GitRepo) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	if r.WebhookServer != nil {
		r.WebhookServer.Deregister(*gr)
	}

	// Deregister from platform if we have a stored webhook ID
	if hookID := gr.Annotations[annotationWebhookID]; hookID != "" {
		token, err := r.loadGitToken(ctx, gr)
		if err == nil {
			n, err := platform.New(gr.Spec.Platform, gr.Spec.Repo, token)
			if err == nil {
				if err := n.DeregisterWebhook(ctx, gr.Spec.Repo, hookID); err != nil {
					logger.Error(err, "failed to deregister webhook from platform", "hookID", hookID)
				} else {
					logger.Info("deregistered webhook from platform", "hookID", hookID)
				}
			}
		}
		// Clear status regardless of deregister outcome — the CR is being deleted
		gr.Status.WebhookStatus = "unregistered"
		gr.Status.WebhookID = ""
	}

	if containsString(gr.Finalizers, gitrepoFinalizer) {
		gr.Finalizers = removeString(gr.Finalizers, gitrepoFinalizer)
		if err := r.Update(ctx, gr); err != nil {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{}, nil
}

// poll queries the platform API for open PRs and reconciles PRDeployment objects.
func (r *GitRepoReconciler) poll(ctx context.Context, gr *api.GitRepo) error {
	logger := log.FromContext(ctx)

	token, err := r.loadGitToken(ctx, gr)
	if err != nil {
		return fmt.Errorf("load git secret: %w", err)
	}

	openPRs, err := fetchOpenPRs(ctx, gr.Spec.Platform, gr.Spec.Repo, token)
	if err != nil {
		return fmt.Errorf("fetch open PRs: %w", err)
	}

	var existing api.PRDeploymentList
	if err := r.List(ctx, &existing,
		client.InNamespace(gr.Namespace),
		client.MatchingLabels{"kube-gitops.centerionware.app/gitrepo": gr.Name},
	); err != nil {
		return fmt.Errorf("list PRDeployments: %w", err)
	}

	existingByPR := make(map[int]api.PRDeployment)
	for _, prd := range existing.Items {
		existingByPR[prd.Spec.PRNumber] = prd
	}

	for _, pr := range openPRs {
		event := webhook.PREvent{
			Action:            "opened",
			Platform:          gr.Spec.Platform,
			RepoURL:           gr.Spec.Repo,
			CloneURL:          pr.CloneURL,
			PRNumber:          pr.Number,
			Branch:            pr.Branch,
			HeadSHA:           pr.HeadSHA,
			Author:            pr.Author,
			AuthorAssociation: pr.AuthorAssociation,
			Title:             pr.Title,
			Labels:            pr.Labels,
		}

		if existing, alreadyExists := existingByPR[pr.Number]; alreadyExists {
			if existing.Spec.HeadSHA != pr.HeadSHA {
				existing.Spec.HeadSHA = pr.HeadSHA
				if err := r.Update(ctx, &existing); err != nil {
					logger.Error(err, "poll: failed to update PRDeployment SHA", "pr", pr.Number)
				}
			}
			delete(existingByPR, pr.Number)
			continue
		}

		if !policy.EvaluatePR(gr.Spec.PRPolicy, event) {
			logger.Info("poll: PR failed trust policy", "pr", pr.Number, "author", pr.Author)
			delete(existingByPR, pr.Number)
			continue
		}

		if err := r.createPRDeployment(ctx, gr, event); err != nil {
			logger.Error(err, "poll: failed to create PRDeployment", "pr", pr.Number)
		}
		delete(existingByPR, pr.Number)
	}

	// Only delete PRDeployments for PRs no longer open.
	// Guard: if openPRs is empty and we have existing PRDeployments, something
	// is wrong with the API response — an empty list would wipe all previews.
	// Skip the delete pass entirely and log a warning instead.
	if len(openPRs) == 0 && len(existingByPR) > 0 {
		logger.Info("poll: API returned zero open PRs but we have active PRDeployments — skipping delete pass to avoid false cleanup",
			"activePRDeployments", len(existingByPR))
		return nil
	}

	for _, prd := range existingByPR {
		prdCopy := prd
		logger.Info("poll: PR closed, deleting PRDeployment", "pr", prd.Spec.PRNumber)
		if err := r.Delete(ctx, &prdCopy); err != nil && !errors.IsNotFound(err) {
			logger.Error(err, "poll: failed to delete PRDeployment", "pr", prd.Spec.PRNumber)
		}
	}

	return nil
}

// HandleEvent is called when a validated PR event arrives (webhook or poll).
func (r *GitRepoReconciler) HandleEvent(ctx context.Context, gr api.GitRepo, event webhook.PREvent) error {
	logger := log.FromContext(ctx)
	logger.Info("PR event", "gitrepo", gr.Name, "action", event.Action, "pr", event.PRNumber)

	switch event.Action {
	case "opened", "synchronize":
		if !policy.EvaluatePR(gr.Spec.PRPolicy, event) {
			logger.Info("event failed trust policy", "pr", event.PRNumber, "author", event.Author)
			return nil
		}
		return r.createOrUpdatePRDeployment(ctx, &gr, event)

	case "closed":
		return r.deletePRDeployment(ctx, &gr, event.PRNumber)

	case "labeled", "unlabeled":
		if !policy.EvaluatePR(gr.Spec.PRPolicy, event) {
			return r.deletePRDeployment(ctx, &gr, event.PRNumber)
		}
		return r.createOrUpdatePRDeployment(ctx, &gr, event)

	case "comment":
		if !policy.EvaluatePR(gr.Spec.PRPolicy, event) {
			logger.Info("comment failed trust policy",
				"pr", event.PRNumber,
				"author", event.CommentAuthor,
				"association", event.CommentAuthorAssociation,
			)
			return nil
		}
		if event.HeadSHA == "" {
			token, err := r.loadGitToken(ctx, &gr)
			if err != nil {
				return fmt.Errorf("load git token for comment trigger: %w", err)
			}
			sha, branch, cloneURL, err := fetchPRHead(ctx, gr.Spec.Platform, gr.Spec.Repo, event.PRNumber, token)
			if err != nil {
				logger.Error(err, "failed to fetch PR head for comment trigger", "pr", event.PRNumber)
				return fmt.Errorf("fetch PR head for comment trigger: %w", err)
			}
			logger.Info("fetched PR head for comment trigger", "pr", event.PRNumber, "branch", branch, "sha", sha, "cloneURL", cloneURL)
			event.HeadSHA = sha
			event.Branch = branch
			event.CloneURL = cloneURL
		}
		return r.createOrUpdatePRDeployment(ctx, &gr, event)
	}

	return nil
}

// HandleWebhookEvent looks up the GitRepo for the incoming event and dispatches.
func (r *GitRepoReconciler) HandleWebhookEvent(ctx context.Context, event webhook.PREvent) error {
	var list api.GitRepoList
	if err := r.List(ctx, &list); err != nil {
		return fmt.Errorf("list GitRepos: %w", err)
	}
	for _, gr := range list.Items {
		if gr.Spec.Platform == event.Platform && gr.Spec.Repo == event.RepoURL {
			return r.HandleEvent(ctx, gr, event)
		}
	}
	return fmt.Errorf("no GitRepo found for platform=%s repo=%s", event.Platform, event.RepoURL)
}

func (r *GitRepoReconciler) createOrUpdatePRDeployment(ctx context.Context, gr *api.GitRepo, event webhook.PREvent) error {
	prdName := prDeploymentName(*gr, event.PRNumber, event.Branch)
	var existing api.PRDeployment
	err := r.Get(ctx, types.NamespacedName{Name: prdName, Namespace: gr.Namespace}, &existing)
	if err != nil && !errors.IsNotFound(err) {
		return err
	}
	if errors.IsNotFound(err) {
		return r.createPRDeployment(ctx, gr, event)
	}

	// Update mutable fields that may have changed since creation
	changed := false
	if event.HeadSHA != "" && existing.Spec.HeadSHA != event.HeadSHA {
		existing.Spec.HeadSHA = event.HeadSHA
		changed = true
	}
	if event.CloneURL != "" && existing.Spec.CloneURL != event.CloneURL {
		existing.Spec.CloneURL = event.CloneURL
		changed = true
		// CloneURL changed — delete the App CR so it rebuilds from the correct repo
		if delErr := r.deleteAppCR(ctx, &existing); delErr != nil {
			return delErr
		}
		// Clear the app-created annotation so pr_deployment reconciler recreates the App
		delete(existing.Annotations, "kube-gitops.centerionware.app/app-created")
	}
	if changed {
		return r.Update(ctx, &existing)
	}
	return nil
}

func (r *GitRepoReconciler) createPRDeployment(ctx context.Context, gr *api.GitRepo, event webhook.PREvent) error {
	// Ensure target namespace exists
	targetNS := builder.AppNamespace(*gr)
	if err := r.ensureNamespace(ctx, targetNS); err != nil {
		return fmt.Errorf("ensure namespace %s: %w", targetNS, err)
	}

	appName, err := builder.AppName(*gr, event.PRNumber, event.Branch)
	if err != nil {
		return err
	}

	prd := &api.PRDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      prDeploymentName(*gr, event.PRNumber, event.Branch),
			Namespace: gr.Namespace,
			Labels: map[string]string{
				"kube-gitops.centerionware.app/gitrepo":   gr.Name,
				"kube-gitops.centerionware.app/pr-number": strconv.Itoa(event.PRNumber),
				"kube-gitops.centerionware.app/platform":  gr.Spec.Platform,
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion:         "kube-gitops.centerionware.app/v1alpha1",
					Kind:               "GitRepo",
					Name:               gr.Name,
					UID:                gr.UID,
					BlockOwnerDeletion: boolPtr(true),
				},
			},
		},
		Spec: api.PRDeploymentSpec{
			GitRepoRef:        gr.Name,
			Platform:          event.Platform,
			RepoURL:           event.RepoURL,
			CloneURL:          event.CloneURL,
			PRNumber:          event.PRNumber,
			Branch:            event.Branch,
			HeadSHA:           event.HeadSHA,
			Author:            event.Author,
			AuthorAssociation: event.AuthorAssociation,
			Title:             event.Title,
			AppRef:            appName,
			AppNamespace:      targetNS,
		},
	}

	return r.Create(ctx, prd)
}

func (r *GitRepoReconciler) deleteAppCR(ctx context.Context, prd *api.PRDeployment) error {
	var app kubedeploy.App
	err := r.Get(ctx, types.NamespacedName{
		Name:      prd.Spec.AppRef,
		Namespace: prd.Spec.AppNamespace,
	}, &app)
	if err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		return err
	}
	return r.Delete(ctx, &app)
}

func (r *GitRepoReconciler) deletePRDeployment(ctx context.Context, gr *api.GitRepo, prNumber int) error {
	var list api.PRDeploymentList
	if err := r.List(ctx, &list,
		client.InNamespace(gr.Namespace),
		client.MatchingLabels{
			"kube-gitops.centerionware.app/gitrepo":   gr.Name,
			"kube-gitops.centerionware.app/pr-number": strconv.Itoa(prNumber),
		},
	); err != nil {
		return err
	}
	for _, prd := range list.Items {
		prdCopy := prd
		if err := r.Delete(ctx, &prdCopy); err != nil && !errors.IsNotFound(err) {
			return err
		}
	}
	return nil
}

func (r *GitRepoReconciler) ensureNamespace(ctx context.Context, name string) error {
	var ns corev1.Namespace
	err := r.Get(ctx, types.NamespacedName{Name: name}, &ns)
	if err == nil {
		return nil
	}
	if !errors.IsNotFound(err) {
		return err
	}
	return r.Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				"kube-gitops.centerionware.app/managed": "true",
			},
		},
	})
}

func (r *GitRepoReconciler) setStatus(ctx context.Context, gr *api.GitRepo, phase, message, pollTime string) error {
	gr.Status.Phase = phase
	gr.Status.Message = message
	gr.Status.LastUpdated = time.Now().UTC().Format(time.RFC3339)
	if pollTime != "" {
		gr.Status.LastPollTime = pollTime
	}
	// Recount active PRDeployments
	var list api.PRDeploymentList
	if err := r.List(ctx, &list,
		client.InNamespace(gr.Namespace),
		client.MatchingLabels{"kube-gitops.centerionware.app/gitrepo": gr.Name},
	); err == nil {
		gr.Status.ActivePRDeployments = len(list.Items)
	}
	return r.Status().Update(ctx, gr)
}

func (r *GitRepoReconciler) loadGitToken(ctx context.Context, gr *api.GitRepo) (string, error) {
	var secret corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{
		Namespace: gr.Namespace,
		Name:      gr.Spec.GitSecret,
	}, &secret); err != nil {
		return "", fmt.Errorf("get secret %s: %w", gr.Spec.GitSecret, err)
	}
	if token, ok := secret.Data["password"]; ok {
		return strings.TrimSpace(string(token)), nil
	}
	return "", fmt.Errorf("secret %s has no 'password' key (requires HTTPS token auth)", gr.Spec.GitSecret)
}

func (r *GitRepoReconciler) loadWebhookSecret(ctx context.Context, gr *api.GitRepo) ([]byte, error) {
	var secret corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{
		Namespace: gr.Namespace,
		Name:      gr.Spec.Trigger.WebhookSecret,
	}, &secret); err != nil {
		return nil, fmt.Errorf("get webhook secret %s: %w", gr.Spec.Trigger.WebhookSecret, err)
	}
	val, ok := secret.Data["secret"]
	if !ok {
		return nil, fmt.Errorf("webhook secret %s has no 'secret' key", gr.Spec.Trigger.WebhookSecret)
	}
	return val, nil
}

func (r *GitRepoReconciler) webhookURL(gr *api.GitRepo) string {
	path := webhook.PublicPath(*gr)
	base := strings.TrimRight(r.ExternalBaseURL, "/")
	return base + path
}

// findWebhookConflict checks if any other GitRepo in the cluster is configured
// in webhook mode for the same platform + repo URL. Returns true and the
// conflicting GitRepo's name if found.
func (r *GitRepoReconciler) findWebhookConflict(ctx context.Context, gr *api.GitRepo) (bool, string) {
	var list api.GitRepoList
	if err := r.List(ctx, &list); err != nil {
		return false, ""
	}
	for _, other := range list.Items {
		if other.Name == gr.Name && other.Namespace == gr.Namespace {
			continue // skip self
		}
		if other.Spec.Platform == gr.Spec.Platform &&
			other.Spec.Repo == gr.Spec.Repo &&
			other.Spec.Trigger.Mode == "webhook" {
			return true, fmt.Sprintf("%s/%s", other.Namespace, other.Name)
		}
	}
	return false, ""
}

func prDeploymentName(gr api.GitRepo, prNumber int, branch string) string {
	name, err := builder.AppName(gr, prNumber, branch)
	if err != nil {
		return fmt.Sprintf("pr-%d", prNumber)
	}
	return name
}

func containsString(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}

func removeString(slice []string, s string) []string {
	out := make([]string, 0, len(slice))
	for _, v := range slice {
		if v != s {
			out = append(out, v)
		}
	}
	return out
}

func boolPtr(b bool) *bool { return &b }

// ── Platform API dispatch ─────────────────────────────────────────────────────

type openPR struct {
	Number            int
	Title             string
	Branch            string
	HeadSHA           string
	CloneURL          string
	Author            string
	AuthorAssociation string
	Labels            []string
}

func fetchOpenPRs(ctx context.Context, platform, repoURL, token string) ([]openPR, error) {
	switch platform {
	case "github":
		return fetchGitHubOpenPRs(ctx, repoURL, token)
	case "gitlab":
		return fetchGitLabOpenMRs(ctx, repoURL, token)
	case "gitea", "forgejo":
		return fetchGiteaOpenPRs(ctx, repoURL, token)
	default:
		return nil, fmt.Errorf("unsupported platform: %s", platform)
	}
}

func fetchPRHead(ctx context.Context, pl, repoURL string, prNumber int, token string) (sha, branch, cloneURL string, err error) {
	switch pl {
	case "github":
		return fetchGitHubPRHead(ctx, repoURL, prNumber, token)
	case "gitlab":
		sha, branch, err = fetchGitLabMRHead(ctx, repoURL, prNumber, token)
		return sha, branch, repoURL, err // GitLab MRs are always same-repo
	case "gitea", "forgejo":
		sha, branch, err = fetchGiteaPRHead(ctx, repoURL, prNumber, token)
		return sha, branch, repoURL, err // Gitea PRs are always same-repo for now
	default:
		return "", "", "", fmt.Errorf("unsupported platform: %s", pl)
	}
}

func orgRepo(repoURL string) string {
	u := strings.TrimSuffix(repoURL, ".git")
	parts := strings.Split(u, "/")
	if len(parts) < 2 {
		return u
	}
	return strings.Join(parts[len(parts)-2:], "/")
}
