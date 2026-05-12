// Package kubedeploy vendors the kube-deploy API types needed to create and
// manage App CRs on behalf of kube-gitops. Kept in sync with
// github.com/centerionware/kube-deploy api/v1alpha1/types.go (types.go).
package kubedeploy

import (
	"encoding/json"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var GroupVersion = schema.GroupVersion{
	Group:   "kube-deploy.centerionware.app",
	Version: "v1alpha1",
}

var (
	SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)
	AddToScheme   = SchemeBuilder.AddToScheme
)

func addKnownTypes(scheme *runtime.Scheme) error {
	scheme.AddKnownTypes(GroupVersion,
		&App{},
		&AppList{},
		&ContainerApp{},
		&ContainerAppList{},
	)
	return nil
}

// ----------------------------------------------------------------
// App — build from source and deploy
// ----------------------------------------------------------------

type App struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AppSpec   `json:"spec,omitempty"`
	Status AppStatus `json:"status,omitempty"`
}

type AppList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []App `json:"items"`
}

func (in *App) DeepCopyObject() runtime.Object {
	out := new(App)
	*out = *in
	return out
}

func (in *AppList) DeepCopyObject() runtime.Object {
	out := new(AppList)
	*out = *in
	return out
}

type AppSpec struct {
	Repo           string            `json:"repo"`
	UpdateInterval string            `json:"updateInterval,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
	Build          BuildSpec         `json:"build,omitempty"`
	Run            RunSpec           `json:"run,omitempty"`
	Service        ServiceSpec       `json:"service,omitempty"`
	Ingress        *IngressSpec      `json:"ingress,omitempty"`
	Gateway        *GatewaySpec      `json:"gateway,omitempty"`
	RBAC           *RBACSpec         `json:"rbac,omitempty"`
	Resources      []json.RawMessage `json:"resources,omitempty"`
}

type AppStatus struct {
	Phase         string `json:"phase,omitempty"`
	Image         string `json:"image,omitempty"`
	Commit        string `json:"commit,omitempty"`
	LastUpdate    string `json:"lastUpdate,omitempty"`
	PendingCommit string `json:"pendingCommit,omitempty"`
}

// ----------------------------------------------------------------
// ContainerApp — deploy a pre-built image
// ----------------------------------------------------------------

type ContainerApp struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ContainerAppSpec   `json:"spec,omitempty"`
	Status ContainerAppStatus `json:"status,omitempty"`
}

type ContainerAppList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ContainerApp `json:"items"`
}

func (in *ContainerApp) DeepCopyObject() runtime.Object {
	out := new(ContainerApp)
	*out = *in
	return out
}

func (in *ContainerAppList) DeepCopyObject() runtime.Object {
	out := new(ContainerAppList)
	*out = *in
	return out
}

type ContainerAppSpec struct {
	Image     string            `json:"image"`
	Env       map[string]string `json:"env,omitempty"`
	Run       RunSpec           `json:"run,omitempty"`
	Service   ServiceSpec       `json:"service,omitempty"`
	Ingress   *IngressSpec      `json:"ingress,omitempty"`
	Gateway   *GatewaySpec      `json:"gateway,omitempty"`
	RBAC      *RBACSpec         `json:"rbac,omitempty"`
	Resources []json.RawMessage `json:"resources,omitempty"`
}

type ContainerAppStatus struct {
	Phase      string `json:"phase,omitempty"`
	Message    string `json:"message,omitempty"`
	LastUpdate string `json:"lastUpdate,omitempty"`
}

// ----------------------------------------------------------------
// BUILD
// ----------------------------------------------------------------

type BuildSpec struct {
	BaseImage      string            `json:"baseImage,omitempty"`
	InstallCmd     string            `json:"installCmd,omitempty"`
	BuildCmd       string            `json:"buildCmd,omitempty"`
	Branch         string            `json:"branch,omitempty"`
	DockerfileMode string            `json:"dockerfileMode,omitempty"`
	Dockerfile     string            `json:"dockerfile,omitempty"`
	Output         string            `json:"output,omitempty"`
	Args           []string          `json:"args,omitempty"`
	Registry       string            `json:"registry,omitempty"`
	GitSecret      string            `json:"gitSecret,omitempty"`
	RegistrySecret string            `json:"registrySecret,omitempty"`
	Resources      BuildResourceSpec `json:"resources,omitempty"`
}

type BuildResourceSpec struct {
	CPURequest         string `json:"cpuRequest,omitempty"`
	MemoryRequest      string `json:"memoryRequest,omitempty"`
	CPULimit           string `json:"cpuLimit,omitempty"`
	MemoryLimit        string `json:"memoryLimit,omitempty"`
	CloneCPURequest    string `json:"cloneCpuRequest,omitempty"`
	CloneMemoryRequest string `json:"cloneMemoryRequest,omitempty"`
	CloneCPULimit      string `json:"cloneCpuLimit,omitempty"`
	CloneMemoryLimit   string `json:"cloneMemoryLimit,omitempty"`
}

// ----------------------------------------------------------------
// RUN
// ----------------------------------------------------------------

type RunSpec struct {
	Command            []string         `json:"command,omitempty"`
	Args               []string         `json:"args,omitempty"`
	Port               int              `json:"port,omitempty"`
	Replicas           int              `json:"replicas,omitempty"`
	Registry           string           `json:"registry,omitempty"`
	ImagePullSecret    string           `json:"imagePullSecret,omitempty"`
	ImagePullSecrets   []string         `json:"imagePullSecrets,omitempty"`
	HostNetwork        bool             `json:"hostNetwork,omitempty"`
	EnableServiceLinks *bool            `json:"enableServiceLinks,omitempty"`
	ServiceAccountName string           `json:"serviceAccountName,omitempty"`
	Resources          ResourceSpec     `json:"resources,omitempty"`
	HealthCheck        HealthCheckSpec  `json:"healthCheck,omitempty"`
	Volumes            []VolumeSpec     `json:"volumes,omitempty"`
	Autoscaling        *AutoscalingSpec `json:"autoscaling,omitempty"`
}

type ResourceSpec struct {
	CPURequest    string `json:"cpuRequest,omitempty"`
	MemoryRequest string `json:"memoryRequest,omitempty"`
	CPULimit      string `json:"cpuLimit,omitempty"`
	MemoryLimit   string `json:"memoryLimit,omitempty"`
}

type HealthCheckSpec struct {
	Path string `json:"path,omitempty"`
}

type VolumeSpec struct {
	Name      string                 `json:"name"`
	MountPath string                 `json:"mountPath"`
	PVC       *PVCVolumeSource       `json:"pvc,omitempty"`
	ConfigMap *ConfigMapVolumeSource `json:"configMap,omitempty"`
	Secret    *SecretVolumeSource    `json:"secret,omitempty"`
	EmptyDir  *EmptyDirVolumeSource  `json:"emptyDir,omitempty"`
	HostPath  *HostPathVolumeSource  `json:"hostPath,omitempty"`
}

type PVCVolumeSource struct {
	ClaimName    string `json:"claimName,omitempty"`
	Size         string `json:"size,omitempty"`
	StorageClass string `json:"storageClass,omitempty"`
	ReadOnly     bool   `json:"readOnly,omitempty"`
}

type ConfigMapVolumeSource struct {
	Name  string      `json:"name"`
	Items []KeyToPath `json:"items,omitempty"`
}

type SecretVolumeSource struct {
	SecretName string      `json:"secretName"`
	Items      []KeyToPath `json:"items,omitempty"`
}

type EmptyDirVolumeSource struct {
	Medium string `json:"medium,omitempty"`
}

type HostPathVolumeSource struct {
	Path string `json:"path"`
	Type string `json:"type,omitempty"`
}

type KeyToPath struct {
	Key  string `json:"key"`
	Path string `json:"path"`
}

type AutoscalingSpec struct {
	Enabled     bool `json:"enabled"`
	MinReplicas int  `json:"minReplicas,omitempty"`
	MaxReplicas int  `json:"maxReplicas,omitempty"`
	CPUTarget   int  `json:"cpuTarget,omitempty"`
}

// ----------------------------------------------------------------
// RBAC
// ----------------------------------------------------------------

type RBACSpec struct {
	ServiceAccountName  string           `json:"serviceAccountName,omitempty"`
	Roles               []RoleDefinition `json:"roles,omitempty"`
	ClusterRoles        []RoleDefinition `json:"clusterRoles,omitempty"`
	RoleBindings        []string         `json:"roleBindings,omitempty"`
	ClusterRoleBindings []string         `json:"clusterRoleBindings,omitempty"`
}

type RoleDefinition struct {
	Name  string     `json:"name"`
	Rules []RBACRule `json:"rules"`
}

type RBACRule struct {
	APIGroups     []string `json:"apiGroups"`
	Resources     []string `json:"resources"`
	Verbs         []string `json:"verbs"`
	ResourceNames []string `json:"resourceNames,omitempty"`
}

// ----------------------------------------------------------------
// SERVICE
// ----------------------------------------------------------------

type ServiceSpec struct {
	Type                     string            `json:"type,omitempty"`
	Annotations              map[string]string `json:"annotations,omitempty"`
	Labels                   map[string]string `json:"labels,omitempty"`
	ClusterIP                string            `json:"clusterIP,omitempty"`
	ExternalIPs              []string          `json:"externalIPs,omitempty"`
	LoadBalancerIP           string            `json:"loadBalancerIP,omitempty"`
	LoadBalancerSourceRanges []string          `json:"loadBalancerSourceRanges,omitempty"`
	ExternalTrafficPolicy    string            `json:"externalTrafficPolicy,omitempty"`
	SessionAffinity          string            `json:"sessionAffinity,omitempty"`
	PublishNotReadyAddresses bool              `json:"publishNotReadyAddresses,omitempty"`
	Ports                    []ServicePortSpec `json:"ports,omitempty"`
	PortRanges               []PortRangeSpec   `json:"portRanges,omitempty"`
}

type ServicePortSpec struct {
	Name       string `json:"name,omitempty"`
	Port       int32  `json:"port"`
	TargetPort int32  `json:"targetPort,omitempty"`
	NodePort   int32  `json:"nodePort,omitempty"`
	Protocol   string `json:"protocol,omitempty"`
}

type PortRangeSpec struct {
	Start            int32  `json:"start"`
	End              int32  `json:"end"`
	Protocol         string `json:"protocol,omitempty"`
	TargetPortOffset int32  `json:"targetPortOffset,omitempty"`
}

// ----------------------------------------------------------------
// INGRESS
// ----------------------------------------------------------------

type IngressSpec struct {
	Enabled     bool              `json:"enabled"`
	Host        string            `json:"host,omitempty"`
	ClassName   *string           `json:"className,omitempty"`
	TLSSecret   string            `json:"tlsSecret,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
	Paths       []IngressPathSpec `json:"paths,omitempty"`
}

type IngressPathSpec struct {
	Path     string `json:"path"`
	PathType string `json:"pathType,omitempty"`
}

// ----------------------------------------------------------------
// GATEWAY API
// ----------------------------------------------------------------

type GatewaySpec struct {
	Enabled     bool              `json:"enabled"`
	GatewayRef  GatewayRefSpec    `json:"gatewayRef"`
	Hostnames   []string          `json:"hostnames,omitempty"`
	TLSSecret   string            `json:"tlsSecret,omitempty"`
	Paths       []GatewayPathSpec `json:"paths,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

type GatewayRefSpec struct {
	Name        string `json:"name"`
	Namespace   string `json:"namespace,omitempty"`
	SectionName string `json:"sectionName,omitempty"`
}

type GatewayPathSpec struct {
	Path      string `json:"path"`
	MatchType string `json:"matchType,omitempty"`
}
