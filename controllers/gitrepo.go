package controllers

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"kube-gitops/api"
	"kube-gitops/builder"
	"kube-gitops/policy"
	"kube-gitops/webhook"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	gitrepoFinalizer    = "kube-gitops.centerionware.app/gitrepo"
	defaultPollInterval = 2 * time.Minute
)

// GitRepoReconciler watches GitRepo CRDs and manages:
//   - poll loops (trigger.mode=poll)
//   - webhook route registration (trigger.mode=webhook)
//   - creation/deletion of PRDeployment objects in response to PR events
type GitRepoReconciler struct {
	client.Client
	Scheme        *runtime.Scheme
	WebhookServer *webhook.Server
}

func SetupGitRepo(mgr ctrl.Manager, r *GitRepoReconciler) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&api.GitRepo{}).
		Owns(&api.PRDeployment{}).
		Complete(r)
}

func (r *GitRepoReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var gr api.GitRepo
	if err := r.Get(ctx, req.NamespacedName, &gr); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Deletion
	if !gr.DeletionTimestamp.IsZero() {
		if r.WebhookServer != nil {
			r.WebhookServer.Deregister(gr)
		}
		if containsString(gr.Finalizers, gitrepoFinalizer) {
			gr.Finalizers = removeString(gr.Finalizers, gitrepoFinalizer)
			if err := r.Update(ctx, &gr); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	// Finalizer
	if !containsString(gr.Finalizers, gitrepoFinalizer) {
		gr.Finalizers = append(gr.Finalizers, gitrepoFinalizer)
		if err := r.Update(ctx, &gr); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// Webhook mode — register route, event-driven from here
	if gr.Spec.Trigger.Mode == "webhook" {
		if r.WebhookServer != nil {
			r.WebhookServer.Register(gr)
		}
		if err := r.setStatus(ctx, &gr, "Ready", "webhook registered", ""); err != nil {
			logger.Error(err, "failed to update status")
		}
		return ctrl.Result{}, nil
	}

	// Poll mode
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
			PRNumber:          pr.Number,
			Branch:            pr.Branch,
			HeadSHA:           pr.HeadSHA,
			Author:            pr.Author,
			AuthorAssociation: pr.AuthorAssociation,
			Title:             pr.Title,
			Labels:            pr.Labels,
		}

		if existing, alreadyExists := existingByPR[pr.Number]; alreadyExists {
			// Update SHA if the PR head changed since last poll
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
			logger.Info("poll: PR failed trust policy, skipping",
				"pr", pr.Number, "author", pr.Author)
			delete(existingByPR, pr.Number)
			continue
		}

		if err := r.createPRDeployment(ctx, gr, event); err != nil {
			logger.Error(err, "poll: failed to create PRDeployment", "pr", pr.Number)
		}
		delete(existingByPR, pr.Number)
	}

	// Delete PRDeployments for PRs no longer open
	for _, prd := range existingByPR {
		prdCopy := prd
		logger.Info("poll: PR closed, deleting PRDeployment", "pr", prd.Spec.PRNumber)
		if err := r.Delete(ctx, &prdCopy); err != nil && !errors.IsNotFound(err) {
			logger.Error(err, "poll: failed to delete PRDeployment", "pr", prd.Spec.PRNumber)
		}
	}

	return nil
}

// HandleEvent is called by the webhook server when a validated PR event arrives.
func (r *GitRepoReconciler) HandleEvent(ctx context.Context, gr api.GitRepo, event webhook.PREvent) error {
	logger := log.FromContext(ctx)
	logger.Info("webhook event", "gitrepo", gr.Name, "action", event.Action, "pr", event.PRNumber)

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
			// Label that was required got removed — tear down
			return r.deletePRDeployment(ctx, &gr, event.PRNumber)
		}
		return r.createOrUpdatePRDeployment(ctx, &gr, event)

	case "comment":
		if !policy.EvaluatePR(gr.Spec.PRPolicy, event) {
			return nil
		}
		// Comment events don't carry head SHA — fetch it
		if event.HeadSHA == "" {
			token, err := r.loadGitToken(ctx, &gr)
			if err != nil {
				return fmt.Errorf("load git secret for comment trigger: %w", err)
			}
			sha, branch, err := fetchPRHead(ctx, gr.Spec.Platform, gr.Spec.Repo, event.PRNumber, token)
			if err != nil {
				return fmt.Errorf("fetch PR head for comment trigger: %w", err)
			}
			event.HeadSHA = sha
			event.Branch = branch
		}
		return r.createOrUpdatePRDeployment(ctx, &gr, event)
	}

	return nil
}

// HandleWebhookEvent is called by the webhook server with a validated, parsed event.
// It looks up the GitRepo that owns this repo URL and dispatches to HandleEvent.
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

	// Already exists — update head SHA on synchronize
	if event.HeadSHA != "" && existing.Spec.HeadSHA != event.HeadSHA {
		existing.Spec.HeadSHA = event.HeadSHA
		return r.Update(ctx, &existing)
	}

	return nil
}

func (r *GitRepoReconciler) createPRDeployment(ctx context.Context, gr *api.GitRepo, event webhook.PREvent) error {
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
			PRNumber:          event.PRNumber,
			Branch:            event.Branch,
			HeadSHA:           event.HeadSHA,
			Author:            event.Author,
			AuthorAssociation: event.AuthorAssociation,
			Title:             event.Title,
			AppRef:            appName,
			AppNamespace:      builder.AppNamespace(*gr),
		},
	}

	return r.Create(ctx, prd)
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

func (r *GitRepoReconciler) setStatus(ctx context.Context, gr *api.GitRepo, phase, message, pollTime string) error {
	gr.Status.Phase = phase
	gr.Status.Message = message
	gr.Status.LastUpdated = time.Now().UTC().Format(time.RFC3339)
	if pollTime != "" {
		gr.Status.LastPollTime = pollTime
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
	return "", fmt.Errorf("secret %s has no 'password' key (API polling requires HTTPS token auth)", gr.Spec.GitSecret)
}

// prDeploymentName returns a stable, unique name for a PRDeployment object.
// Uses AppName (which slugifies the repo+PR number) as the base.
func prDeploymentName(gr api.GitRepo, prNumber int, branch string) string {
	name, err := builder.AppName(gr, prNumber, branch)
	if err != nil {
		// Fallback — should not happen in practice
		return fmt.Sprintf("pr-%d", prNumber)
	}
	return name
}

// ── Helpers ───────────────────────────────────────────────────────────────────

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

func fetchPRHead(ctx context.Context, platform, repoURL string, prNumber int, token string) (sha, branch string, err error) {
	switch platform {
	case "github":
		return fetchGitHubPRHead(ctx, repoURL, prNumber, token)
	case "gitlab":
		return fetchGitLabMRHead(ctx, repoURL, prNumber, token)
	case "gitea", "forgejo":
		return fetchGiteaPRHead(ctx, repoURL, prNumber, token)
	default:
		return "", "", fmt.Errorf("unsupported platform: %s", platform)
	}
}

// orgRepo extracts "org/repo" from a full repo URL.
func orgRepo(repoURL string) string {
	u := strings.TrimSuffix(repoURL, ".git")
	parts := strings.Split(u, "/")
	if len(parts) < 2 {
		return u
	}
	return strings.Join(parts[len(parts)-2:], "/")
}
