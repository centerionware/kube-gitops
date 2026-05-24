package controllers

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"text/template"
	"time"

	api "kube-gitops/api/v1alpha1"
	"kube-gitops/builder"
	"kube-gitops/kubedeploy"
	"kube-gitops/platform"

	"k8s.io/apimachinery/pkg/api/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const prDeploymentFinalizer = "kube-gitops.centerionware.app/prdeployment"

// PRDeploymentReconciler owns the lifecycle of a single PR's kube-deploy App CR.
//
//   - Create:  builds and creates the kube-deploy App CR
//   - Running: mirrors App status → PRDeployment status, posts PR comment + commit status
//   - Delete:  removes App CR (kube-deploy cleans up all owned resources)
type PRDeploymentReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

func SetupPRDeployment(mgr ctrl.Manager, r *PRDeploymentReconciler) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&api.PRDeployment{}).
		Complete(r)
}

func (r *PRDeploymentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var prd api.PRDeployment
	if err := r.Get(ctx, req.NamespacedName, &prd); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !prd.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, &prd)
	}

	if !containsString(prd.Finalizers, prDeploymentFinalizer) {
		prd.Finalizers = append(prd.Finalizers, prDeploymentFinalizer)
		if err := r.Update(ctx, &prd); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// Fetch parent GitRepo for config
	var gr api.GitRepo
	if err := r.Get(ctx, types.NamespacedName{
		Namespace: prd.Namespace,
		Name:      prd.Spec.GitRepoRef,
	}, &gr); err != nil {
		if errors.IsNotFound(err) {
			logger.Info("parent GitRepo gone, deleting PRDeployment")
			return ctrl.Result{}, r.Delete(ctx, &prd)
		}
		return ctrl.Result{}, err
	}

	// Ensure App CR exists.
	// Check annotation first — if we've already created it, go straight to
	// syncStatus rather than hitting NotFound and creating again. This handles
	// the window between Create returning and the cache catching up.
	appKey := types.NamespacedName{Name: prd.Spec.AppRef, Namespace: prd.Spec.AppNamespace}
	var existingApp kubedeploy.App
	err := r.Get(ctx, appKey, &existingApp)

	if errors.IsNotFound(err) {
		// Only create if we haven't done so already
		if prd.Annotations["kube-gitops.centerionware.app/app-created"] == "true" {
			// Cache hasn't caught up yet — wait and retry
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}
		return r.createApp(ctx, gr, &prd)
	}
	if err != nil {
		return ctrl.Result{}, err
	}

	return r.syncStatus(ctx, gr, &prd, &existingApp)
}

func (r *PRDeploymentReconciler) createApp(ctx context.Context, gr api.GitRepo, prd *api.PRDeployment) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	app, err := builder.BuildApp(gr, *prd)
	if err != nil {
		logger.Error(err, "failed to build App CR")
		_ = r.setPRDStatus(ctx, prd, "error", fmt.Sprintf("build failed: %v", err), "", "", "")
		_ = r.postCommitStatus(ctx, gr, prd, "error", fmt.Sprintf("build failed: %v", err), "")
		return ctrl.Result{}, nil
	}

	// Do NOT set a cross-namespace owner reference — Kubernetes silently drops
	// them and the object still gets created, causing the loop we saw.
	// Cleanup is handled by the PRDeployment finalizer in reconcileDelete instead.
	app.OwnerReferences = nil

	if err := r.Create(ctx, app); err != nil {
		if errors.IsAlreadyExists(err) {
			// Race — already exists, fall through to sync on next reconcile
			logger.Info("App CR already exists, syncing", "app", app.Name)
			return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
		}
		logger.Error(err, "failed to create App CR")
		_ = r.setPRDStatus(ctx, prd, "error", fmt.Sprintf("create App failed: %v", err), "", "", "")
		return ctrl.Result{}, nil
	}

	logger.Info("created App CR", "app", app.Name, "ns", app.Namespace, "pr", prd.Spec.PRNumber)

	// Mark that we've created the App so the next reconcile goes to syncStatus
	// rather than hitting NotFound and creating again.
	if prd.Annotations == nil {
		prd.Annotations = make(map[string]string)
	}
	prd.Annotations["kube-gitops.centerionware.app/app-created"] = "true"
	if err := r.Update(ctx, prd); err != nil {
		return ctrl.Result{}, err
	}

	_ = r.postCommitStatus(ctx, gr, prd, "pending", "Preview deploying…", "")

	return ctrl.Result{RequeueAfter: 15 * time.Second},
		r.setPRDStatus(ctx, prd, "deploying", "App CR created", "", "", "")
}

func (r *PRDeploymentReconciler) syncStatus(ctx context.Context, gr api.GitRepo, prd *api.PRDeployment, app *kubedeploy.App) (ctrl.Result, error) {
	appPhase := app.Status.Phase
	appImage := app.Status.Image
	appCommit := app.Status.Commit
	state := prStateFromAppPhase(appPhase)

	previewURL := prd.Status.URL
	if app.Spec.Ingress != nil && app.Spec.Ingress.Host != "" {
		scheme := "https"
		if app.Spec.Ingress.TLSSecret == "" {
			scheme = "http"
		}
		previewURL = scheme + "://" + app.Spec.Ingress.Host
	} else if app.Spec.Gateway != nil && len(app.Spec.Gateway.Hostnames) > 0 {
		previewURL = "https://" + app.Spec.Gateway.Hostnames[0]
	}

	statusChanged := prd.Status.AppPhase != appPhase ||
		prd.Status.State != state ||
		prd.Status.URL != previewURL ||
		prd.Status.Image != appImage ||
		prd.Status.Commit != appCommit

	if statusChanged {
		if err := r.setPRDStatus(ctx, prd, state, appPhase, previewURL, appImage, appCommit); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Post notifications when we transition into terminal states
	if state == "running" && !prd.Status.NotifiedDeploy {
		_ = r.postCommitStatus(ctx, gr, prd, "success", "Preview ready", previewURL)
		_ = r.postDeployComment(ctx, gr, prd, previewURL)
		prd.Status.NotifiedDeploy = true
		_ = r.Status().Update(ctx, prd)
	}

	if state == "error" && !prd.Status.NotifiedError {
		_ = r.postCommitStatus(ctx, gr, prd, "failure", appPhase, "")
		_ = r.postErrorComment(ctx, gr, prd, appPhase)
		prd.Status.NotifiedError = true
		_ = r.Status().Update(ctx, prd)
	}

	if state == "deploying" || state == "pending" {
		return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
	}
	return ctrl.Result{RequeueAfter: 60 * time.Second}, nil
}

func (r *PRDeploymentReconciler) reconcileDelete(ctx context.Context, prd *api.PRDeployment) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Optionally post close comment
	var gr api.GitRepo
	if err := r.Get(ctx, types.NamespacedName{
		Namespace: prd.Namespace,
		Name:      prd.Spec.GitRepoRef,
	}, &gr); err == nil {
		notify := gr.Spec.Notify
		onClose := notify.OnClose != nil && *notify.OnClose
		if onClose {
			_ = r.postCloseComment(ctx, gr, prd)
		}
	}

	var app kubedeploy.App
	err := r.Get(ctx, types.NamespacedName{
		Name:      prd.Spec.AppRef,
		Namespace: prd.Spec.AppNamespace,
	}, &app)
	if err != nil && !errors.IsNotFound(err) {
		return ctrl.Result{}, err
	}
	if err == nil {
		if delErr := r.Delete(ctx, &app); delErr != nil && !errors.IsNotFound(delErr) {
			logger.Error(delErr, "failed to delete App CR", "app", app.Name)
			return ctrl.Result{}, delErr
		}
		logger.Info("deleted App CR", "app", app.Name, "pr", prd.Spec.PRNumber)
	}

	prd.Finalizers = removeString(prd.Finalizers, prDeploymentFinalizer)
	return ctrl.Result{}, r.Update(ctx, prd)
}

func (r *PRDeploymentReconciler) setPRDStatus(ctx context.Context, prd *api.PRDeployment, state, message, url, image, commit string) error {
	prd.Status.State = state
	prd.Status.Message = message
	prd.Status.LastUpdated = time.Now().UTC().Format(time.RFC3339)
	if url != "" {
		prd.Status.URL = url
	}
	if image != "" {
		prd.Status.Image = image
	}
	if commit != "" {
		prd.Status.Commit = commit
	}
	prd.Status.AppPhase = message
	return r.Status().Update(ctx, prd)
}

// ── Notifications ─────────────────────────────────────────────────────────────

type notifyVars struct {
	URL        string
	Error      string
	PRNumber   int
	Branch     string
	SHA        string
	Author     string
	RepoName   string
	Title      string
}

func prNotifyVars(prd *api.PRDeployment, url, errMsg string) notifyVars {
	parts := strings.Split(strings.TrimSuffix(prd.Spec.RepoURL, ".git"), "/")
	repoName := ""
	if len(parts) > 0 {
		repoName = parts[len(parts)-1]
	}
	return notifyVars{
		URL:      url,
		Error:    errMsg,
		PRNumber: prd.Spec.PRNumber,
		Branch:   prd.Spec.Branch,
		SHA:      prd.Spec.HeadSHA,
		Author:   prd.Spec.Author,
		RepoName: repoName,
		Title:    prd.Spec.Title,
	}
}

func renderNotifyTemplate(name, tpl string, vars notifyVars) string {
	t, err := template.New(name).Parse(tpl)
	if err != nil {
		return tpl
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, vars); err != nil {
		return tpl
	}
	return buf.String()
}

func (r *PRDeploymentReconciler) notifier(ctx context.Context, gr api.GitRepo, prd *api.PRDeployment) (platform.Notifier, error) {
	// Load token from the GitRepo's gitSecret
	var secret corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{
		Namespace: gr.Namespace,
		Name:      gr.Spec.GitSecret,
	}, &secret); err != nil {
		return nil, fmt.Errorf("load git secret: %w", err)
	}
	token := strings.TrimSpace(string(secret.Data["password"]))
	return platform.New(gr.Spec.Platform, gr.Spec.Repo, token)
}

func (r *PRDeploymentReconciler) postDeployComment(ctx context.Context, gr api.GitRepo, prd *api.PRDeployment, url string) error {
	notify := gr.Spec.Notify
	onDeploy := notify.OnDeploy == nil || *notify.OnDeploy // default true
	if !onDeploy {
		return nil
	}

	tpl := notify.DeployTemplate
	if tpl == "" {
		tpl = "🚀 Preview deployed: {{.URL}}"
	}

	n, err := r.notifier(ctx, gr, prd)
	if err != nil {
		return err
	}
	body := renderNotifyTemplate("deploy", tpl, prNotifyVars(prd, url, ""))
	return n.PostComment(ctx, prd.Spec.RepoURL, prd.Spec.PRNumber, body)
}

func (r *PRDeploymentReconciler) postErrorComment(ctx context.Context, gr api.GitRepo, prd *api.PRDeployment, errMsg string) error {
	notify := gr.Spec.Notify
	onError := notify.OnError == nil || *notify.OnError // default true
	if !onError {
		return nil
	}

	tpl := notify.ErrorTemplate
	if tpl == "" {
		tpl = "❌ Preview deployment failed: {{.Error}}"
	}

	n, err := r.notifier(ctx, gr, prd)
	if err != nil {
		return err
	}
	body := renderNotifyTemplate("error", tpl, prNotifyVars(prd, "", errMsg))
	return n.PostComment(ctx, prd.Spec.RepoURL, prd.Spec.PRNumber, body)
}

func (r *PRDeploymentReconciler) postCloseComment(ctx context.Context, gr api.GitRepo, prd *api.PRDeployment) error {
	tpl := gr.Spec.Notify.CloseTemplate
	if tpl == "" {
		tpl = "🧹 Preview environment removed."
	}
	n, err := r.notifier(ctx, gr, prd)
	if err != nil {
		return err
	}
	body := renderNotifyTemplate("close", tpl, prNotifyVars(prd, prd.Status.URL, ""))
	return n.PostComment(ctx, prd.Spec.RepoURL, prd.Spec.PRNumber, body)
}

func (r *PRDeploymentReconciler) postCommitStatus(ctx context.Context, gr api.GitRepo, prd *api.PRDeployment, state, description, targetURL string) error {
	if prd.Spec.HeadSHA == "" {
		return nil
	}
	n, err := r.notifier(ctx, gr, prd)
	if err != nil {
		return err
	}
	return n.SetCommitStatus(ctx, prd.Spec.RepoURL, prd.Spec.HeadSHA, state, description, targetURL)
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func prStateFromAppPhase(phase string) string {
	switch phase {
	case "Running":
		return "running"
	case "Error", "Failed":
		return "error"
	case "":
		return "pending"
	default:
		return "deploying"
	}
}
