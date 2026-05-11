module kube-gitops

go 1.22

require (
	github.com/go-git/go-git/v6 v6.0.0-alpha.1
	golang.org/x/crypto v0.22.0
	k8s.io/api v0.29.0
	k8s.io/apimachinery v0.29.0
	k8s.io/client-go v0.29.0
	sigs.k8s.io/controller-runtime v0.17.0
	sigs.k8s.io/gateway-api v1.0.0
)