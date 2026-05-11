package main

import (
	"context"
	"log"
	"os"

	"kube-gitops/api"
	"kube-gitops/controllers"
	"kube-gitops/kubedeploy"
	"kube-gitops/webhook"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(api.AddToScheme(scheme))
	utilruntime.Must(kubedeploy.AddToScheme(scheme))
	utilruntime.Must(corev1.AddToScheme(scheme))
	utilruntime.Must(networkingv1.AddToScheme(scheme))

	if err := gatewayv1.Install(scheme); err != nil {
		log.Printf("warning: gateway API scheme registration failed (CRDs may not be installed): %v", err)
	}
}

func main() {
	zapOpts := zap.Options{
		Development: os.Getenv("LOG_DEV_MODE") != "false",
	}
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zapOpts)))

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
	})
	if err != nil {
		log.Printf("manager init failed: %v", err)
		os.Exit(1)
	}

	webhookAddr := os.Getenv("WEBHOOK_ADDR")
	if webhookAddr == "" {
		webhookAddr = ":8080"
	}

	// The webhook server needs a k8s client to load secrets for HMAC validation.
	// We use the manager's client so it benefits from the informer cache.
	gitRepoReconciler := &controllers.GitRepoReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}

	webhookServer := webhook.NewServer(
		mgr.GetClient(),
		webhookAddr,
		func(ctx context.Context, event webhook.PREvent) error {
			// Route the validated event to the correct GitRepo reconciler
			return gitRepoReconciler.HandleWebhookEvent(ctx, event)
		},
	)
	gitRepoReconciler.WebhookServer = webhookServer

	if err := controllers.SetupGitRepo(mgr, gitRepoReconciler); err != nil {
		log.Printf("GitRepo controller setup failed: %v", err)
		os.Exit(1)
	}

	if err := controllers.SetupPRDeployment(mgr, &controllers.PRDeploymentReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}); err != nil {
		log.Printf("PRDeployment controller setup failed: %v", err)
		os.Exit(1)
	}

	// Run the webhook server in the background under the manager's context
	if err := mgr.Add(webhookServer); err != nil {
		log.Printf("failed to add webhook server to manager: %v", err)
		os.Exit(1)
	}

	log.Println("starting kube-gitops controller manager...")

	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		log.Printf("manager exited: %v", err)
		os.Exit(1)
	}
}
