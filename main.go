package main

import (
	"context"
	"log"
	"os"
	"strings"

	api "kube-gitops/api/v1alpha1"
	"kube-gitops/controllers"
	"kube-gitops/kubedeploy"
	"kube-gitops/webhook"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(api.AddToScheme(scheme))
	utilruntime.Must(kubedeploy.AddToScheme(scheme))
	utilruntime.Must(corev1.AddToScheme(scheme))
	utilruntime.Must(networkingv1.AddToScheme(scheme))

	// Required: registers ListOptions conversion for our custom API groups.
	metav1.AddToGroupVersion(scheme, api.GroupVersion)
	metav1.AddToGroupVersion(scheme, kubedeploy.GroupVersion)

	if err := gatewayv1.Install(scheme); err != nil {
		log.Printf("warning: gateway API scheme not available: %v", err)
	}
}

func main() {
	zapOpts := zap.Options{
		Development: os.Getenv("LOG_DEV_MODE") != "false",
	}
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zapOpts)))

	// ADDR is the single port we listen on for all traffic.
	// Default: :8080
	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = ":8080"
	}

	// EXTERNAL_URL is the publicly reachable base URL of this operator,
	// e.g. "https://gitops.centerionware.com".
	// Required for automatic webhook registration with git platforms.
	// If unset, webhook registration is skipped (register manually).
	externalURL := strings.TrimRight(os.Getenv("EXTERNAL_URL"), "/")

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
	})
	if err != nil {
		log.Printf("manager init failed: %v", err)
		os.Exit(1)
	}

	gitRepoReconciler := &controllers.GitRepoReconciler{
		Client:          mgr.GetClient(),
		Scheme:          mgr.GetScheme(),
		ExternalBaseURL: externalURL,
	}

	srv := webhook.NewServer(
		mgr.GetClient(),
		addr,
		func(ctx context.Context, event webhook.PREvent) error {
			return gitRepoReconciler.HandleWebhookEvent(ctx, event)
		},
	)
	gitRepoReconciler.WebhookServer = srv

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

	if err := mgr.Add(srv); err != nil {
		log.Printf("failed to register HTTP server with manager: %v", err)
		os.Exit(1)
	}

	log.Println("starting kube-gitops controller manager...")

	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		log.Printf("manager exited: %v", err)
		os.Exit(1)
	}
}
