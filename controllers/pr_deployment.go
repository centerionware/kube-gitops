package controllers

import (
	"context"
	"fmt"

	gitopsv1alpha1 "kube-gitops/api/v1alpha1"
	kubedeploy "kube-gitops/internal/kubedeploy"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"k8s.io/apimachinery/pkg/runtime"
)

// PRDeploymentReconciler owns the lifecycle of a single PR's kube-deploy App CR.
// It creates the App when a PRDeployment is created, mirrors its status, and
// deletes it when the PRDeployment is removed (i.e. the PR was closed/merged).
type PRDeploymentReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

func SetupPRDeployment(mgr ctrl.Manager, r *PRDeploymentReconciler) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&gitopsv1alpha1.PRDeployment{}).
		Owns(&kubedeploy.App{}).
		Complete(r)
}

func (r *PRDeploymentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var prDeploy gitopsv1alpha1.PRDeployment
	if err := r.Get(ctx, req.NamespacedName, &prDeploy); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	logger.Info("reconciling PRDeployment",
		"name", prDeploy.Name,
		"pr", prDeploy.Spec.PRNumber,
		"branch", prDeploy.Spec.Branch,
		"state", prDeploy.Status.State,
	)

	// TODO: implement App CR lifecycle
	// 1. Fetch the parent GitRepo to get PRDeploy config
	// 2. If App does not exist: create it (buildAppCR)
	// 3. If App exists: mirror its status.phase → PRDeployment.status
	// 4. If PRDeployment is being deleted (DeletionTimestamp set): delete App CR

	_ = fmt.Sprintf("placeholder — PRDeployment %s/%s pr#%d",
		prDeploy.Namespace, prDeploy.Name, prDeploy.Spec.PRNumber)

	return ctrl.Result{}, nil
}
