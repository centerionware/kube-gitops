package controllers

import (
	"context"
	"fmt"

	gitopsv1alpha1 "kube-gitops/api/v1alpha1"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"k8s.io/apimachinery/pkg/runtime"
)

// GitRepoReconciler watches GitRepo CRDs and manages:
//   - poll loops (when trigger.mode=poll)
//   - webhook registration (when trigger.mode=webhook)
//   - creation/deletion of PRDeployment objects in response to PR events
type GitRepoReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

func SetupGitRepo(mgr ctrl.Manager, r *GitRepoReconciler) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&gitopsv1alpha1.GitRepo{}).
		Owns(&gitopsv1alpha1.PRDeployment{}).
		Complete(r)
}

func (r *GitRepoReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var gitRepo gitopsv1alpha1.GitRepo
	if err := r.Get(ctx, req.NamespacedName, &gitRepo); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	logger.Info("reconciling GitRepo", "name", gitRepo.Name, "mode", gitRepo.Spec.Trigger.Mode)

	// TODO: implement trigger dispatch
	// - mode=webhook: ensure webhook ingress exists, register with platform if needed
	// - mode=poll:    schedule poll via requeue interval, list open PRs, diff against
	//                 existing PRDeployments, create/delete accordingly

	_ = fmt.Sprintf("placeholder — GitRepo %s/%s", gitRepo.Namespace, gitRepo.Name)

	return ctrl.Result{}, nil
}
