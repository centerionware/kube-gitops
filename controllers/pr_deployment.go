package controllers

import (
	"context"
	"fmt"
	"time"

	api "kube-gitops/api/v1alpha1"
	"kube-gitops/builder"
	"kube-gitops/kubedeploy"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const prDeploymentFinalizer = "kube-gitops.centerionware.app/prdeployment"

// PRDeploymentReconciler owns the lifecycle of a single PR's kube-deploy App CR.
//
//   - On create: builds and applies the kube-deploy App CR
//   - On update: mirrors App status.phase back into PRDeployment status
//   - On delete: removes the App CR; kube-deploy cleans up everything it owns
type PRDeploymentReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

func SetupPRDeployment(mgr ctrl.Manager, r *PRDeploymentReconciler) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&api.PRDeployment{}).
		Owns(&kubedeploy.App{}).
		Complete(r)
}

func (r *PRDeploymentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var prd api.PRDeployment
	if err := r.Get(ctx, req.NamespacedName, &prd); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Deletion path
	if !prd.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, &prd)
	}

	// Ensure finalizer
	if !containsString(prd.Finalizers, prDeploymentFinalizer) {
		prd.Finalizers = append(prd.Finalizers, prDeploymentFinalizer)
		if err := r.Update(ctx, &prd); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// Fetch parent GitRepo for build config
	var gr api.GitRepo
	if err := r.Get(ctx, types.NamespacedName{
		Namespace: prd.Namespace,
		Name:      prd.Spec.GitRepoRef,
	}, &gr); err != nil {
		if errors.IsNotFound(err) {
			logger.Info("parent GitRepo gone, deleting PRDeployment", "gitrepo", prd.Spec.GitRepoRef)
			return ctrl.Result{}, r.Delete(ctx, &prd)
		}
		return ctrl.Result{}, err
	}

	// Check whether the App CR already exists
	appKey := types.NamespacedName{
		Name:      prd.Spec.AppRef,
		Namespace: prd.Spec.AppNamespace,
	}
	var existingApp kubedeploy.App
	err := r.Get(ctx, appKey, &existingApp)

	if errors.IsNotFound(err) {
		return r.createApp(ctx, gr, &prd)
	}
	if err != nil {
		return ctrl.Result{}, err
	}

	// Mirror App status into PRDeployment status
	return r.syncStatus(ctx, &prd, &existingApp)
}

func (r *PRDeploymentReconciler) createApp(ctx context.Context, gr api.GitRepo, prd *api.PRDeployment) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	app, err := builder.BuildApp(gr, *prd)
	if err != nil {
		logger.Error(err, "failed to build App CR")
		return ctrl.Result{}, r.setPRDStatus(ctx, prd, "error",
			fmt.Sprintf("failed to build App CR: %v", err), "")
	}

	// Owner reference — App CR is deleted when PRDeployment is deleted
	app.OwnerReferences = []metav1.OwnerReference{
		{
			APIVersion:         "kube-gitops.centerionware.app/v1alpha1",
			Kind:               "PRDeployment",
			Name:               prd.Name,
			UID:                prd.UID,
			BlockOwnerDeletion: boolPtr(true),
		},
	}

	if err := r.Create(ctx, app); err != nil {
		logger.Error(err, "failed to create App CR", "app", app.Name)
		return ctrl.Result{}, r.setPRDStatus(ctx, prd, "error",
			fmt.Sprintf("failed to create App CR: %v", err), "")
	}

	logger.Info("created App CR",
		"app", app.Name, "namespace", app.Namespace,
		"pr", prd.Spec.PRNumber, "branch", prd.Spec.Branch)

	return ctrl.Result{RequeueAfter: 15 * time.Second},
		r.setPRDStatus(ctx, prd, "deploying", "App CR created", "")
}

func (r *PRDeploymentReconciler) syncStatus(ctx context.Context, prd *api.PRDeployment, app *kubedeploy.App) (ctrl.Result, error) {
	appPhase := app.Status.Phase
	state := prStateFromAppPhase(appPhase)

	previewURL := prd.Status.URL
	if app.Spec.Ingress != nil && app.Spec.Ingress.Host != "" {
		previewURL = "https://" + app.Spec.Ingress.Host
	} else if app.Spec.Gateway != nil && len(app.Spec.Gateway.Hostnames) > 0 {
		previewURL = "https://" + app.Spec.Gateway.Hostnames[0]
	}

	if prd.Status.AppPhase != appPhase || prd.Status.State != state || prd.Status.URL != previewURL {
		prd.Status.AppPhase = appPhase
		if err := r.setPRDStatus(ctx, prd, state, appPhase, previewURL); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Poll more frequently while building
	if state == "deploying" || state == "pending" {
		return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
	}
	return ctrl.Result{RequeueAfter: 60 * time.Second}, nil
}

func (r *PRDeploymentReconciler) reconcileDelete(ctx context.Context, prd *api.PRDeployment) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

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

func (r *PRDeploymentReconciler) setPRDStatus(ctx context.Context, prd *api.PRDeployment, state, message, url string) error {
	prd.Status.State = state
	prd.Status.Message = message
	prd.Status.LastUpdated = time.Now().UTC().Format(time.RFC3339)
	if url != "" {
		prd.Status.URL = url
	}
	return r.Status().Update(ctx, prd)
}

// prStateFromAppPhase maps kube-deploy App phases to PRDeployment states.
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
