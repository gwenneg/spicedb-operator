package cluster

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/fatih/camelcase"
	"github.com/jzelinskie/stringz"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/errors"
	applyappsv1 "k8s.io/client-go/applyconfigurations/apps/v1"
	applybatchv1 "k8s.io/client-go/applyconfigurations/batch/v1"
	applycorev1 "k8s.io/client-go/applyconfigurations/core/v1"
	applymetav1 "k8s.io/client-go/applyconfigurations/meta/v1"
	applyrbacv1 "k8s.io/client-go/applyconfigurations/rbac/v1"

	"github.com/authzed/spicedb-operator/pkg/apis/authzed/v1alpha1"
)

type Config struct {
	MigrationConfig
	SpiceConfig
}

// MigrationConfig stores data that is relevant for running migrations
// or deciding if migrations need to be run
type MigrationConfig struct {
	LogLevel               string
	DatastoreEngine        string
	DatastoreURI           string
	SpannerCredsSecretRef  string
	TargetSpiceDBImage     string
	EnvPrefix              string
	SpiceDBCmd             string
	DatastoreTLSSecretName string
}

// SpiceConfig contains config relevant to running spicedb or determining
// if spicedb needs to be updated
type SpiceConfig struct {
	Name                         string
	Namespace                    string
	UID                          string
	Replicas                     int32
	PresharedKey                 string
	EnvPrefix                    string
	SpiceDBCmd                   string
	TLSSecretName                string
	DispatchUpstreamCASecretName string
	TelemetryTLSCASecretName     string
	SecretName                   string
	Passthrough                  map[string]string
}

// NewConfig checks that the values in the config + the secret are sane
func NewConfig(nn types.NamespacedName, uid types.UID, image string, config map[string]string, secret *corev1.Secret) (*Config, error) {
	passthroughConfig := make(map[string]string, 0)
	errs := make([]error, 0)

	datastoreEngine, ok := config["datastoreEngine"]
	if !ok {
		errs = append(errs, fmt.Errorf("datastoreEngine is a required field"))
	}
	passthroughConfig["datastoreEngine"] = config["datastoreEngine"]
	delete(config, "datastoreEngine")

	// TODO: disable for memory engine
	passthroughConfig["dispatchClusterEnabled"] = "true"

	if secret == nil {
		errs = append(errs, fmt.Errorf("secret must be provided"))
	}
	var datastoreURI, psk []byte
	if secret != nil {
		var ok bool
		datastoreURI, ok = secret.Data["datastore_uri"]
		if !ok {
			errs = append(errs, fmt.Errorf("secret must contain a datastore_uri field"))
		}
		psk, ok = secret.Data["preshared_key"]
		if !ok {
			errs = append(errs, fmt.Errorf("secret must contain a preshared_key field"))
		}
	}

	replicas, err := strconv.ParseInt(stringz.DefaultEmpty(config["replicas"], "2"), 10, 32)
	if err != nil {
		errs = append(errs, fmt.Errorf("invalid value for replicas %q: %w", replicas, err))
	}
	delete(config, "replicas")

	envPrefix := stringz.DefaultEmpty(config["envPrefix"], "SPICEDB_")
	delete(config, "envPrefix")
	spicedbCmd := stringz.DefaultEmpty(config["cmd"], "spicedb")
	delete(config, "cmd")

	// TODO: strip secret names from passthrough config

	// generate secret refs for tls if specified
	if len(config["tlsSecretName"]) > 0 {
		const (
			TLSKey = "/tls/tls.key"
			TLSCrt = "/tls/tls.crt"
		)
		passthroughDefault := func(key string, fallback string) {
			passthroughConfig[key] = stringz.DefaultEmpty(config[key], fallback)
		}
		// set to the configured TLS secret unless explicitly set in config
		passthroughDefault("grpcTLSKeyPath", TLSKey)
		passthroughDefault("grpcTLSCertPath", TLSCrt)
		passthroughDefault("dispatchClusterTLSKeyPath", TLSKey)
		passthroughDefault("dispatchClusterTLSCertPath", TLSCrt)
		passthroughDefault("httpTLSKeyPath", TLSKey)
		passthroughDefault("httpTLSCertPath", TLSCrt)
		passthroughDefault("dashboardTLSKeyPath", TLSKey)
		passthroughDefault("dashboardTLSCertPath", TLSCrt)
		passthroughDefault("metricsTLSKeyPath", TLSKey)
		passthroughDefault("metricsTLSCertPath", TLSCrt)
	}

	if len(config["dispatchUpstreamCASecretName"]) > 0 {
		passthroughConfig["dispatchUpstreamCAPath"] = "/dispatch-tls/tls.crt"
	}

	if len(config["telemetryCASecretName"]) > 0 {
		passthroughConfig["telemetryCAOverridePath"] = "/telemetry-tls/tls.crt"
	}

	// the rest of the config is passed through to spicedb
	for k := range config {
		passthroughConfig[k] = config[k]
	}

	stripValues := []string{
		"datastoreConnUri",
		"grpcPresharedKey",
		"presharedKey",
		"preshared_key",
		"datastore_uri",
	}
	// strip sensitive values from passthrough config (if they have been
	// inadvertently set by a user)
	for k := range passthroughConfig {
		for _, s := range stripValues {
			if strings.EqualFold(k, s) {
				delete(passthroughConfig, k)
			}
		}
	}

	if len(errs) > 0 {
		return nil, errors.NewAggregate(errs)
	}

	// Note: The config objects hold values from the passed secret for
	// hashing purposes; these should not be used directly (instead the secret
	// should be mounted)
	return &Config{
		MigrationConfig: MigrationConfig{
			DatastoreEngine:        datastoreEngine,
			DatastoreURI:           string(datastoreURI),
			SpannerCredsSecretRef:  config["spannerCredentials"],
			TargetSpiceDBImage:     image,
			EnvPrefix:              envPrefix,
			SpiceDBCmd:             spicedbCmd,
			DatastoreTLSSecretName: config["datastoreTLSSecretName"],
		},
		SpiceConfig: SpiceConfig{
			Name:                         nn.Name,
			Namespace:                    nn.Namespace,
			UID:                          string(uid),
			PresharedKey:                 string(psk),
			Replicas:                     int32(replicas),
			EnvPrefix:                    envPrefix,
			SpiceDBCmd:                   spicedbCmd,
			TLSSecretName:                config["tlsSecretName"],
			DispatchUpstreamCASecretName: config["dispatchUpstreamCASecretName"],
			TelemetryTLSCASecretName:     config["telemetryCASecretName"],
			SecretName:                   secret.GetName(),
			Passthrough:                  passthroughConfig,
		},
	}, nil
}

// ToEnvVarApplyConfiguration returns a set of env variables to apply to a
// spicedb container
func (c *SpiceConfig) ToEnvVarApplyConfiguration() []*applycorev1.EnvVarApplyConfiguration {
	// Set non-passthrough config that is either generated directly by the
	// controller (dispatch address), has some direct effect on the cluster
	// (tls), or lives in an external secret (preshared key).
	envVars := []*applycorev1.EnvVarApplyConfiguration{
		applycorev1.EnvVar().WithName(c.EnvPrefix + "_DISPATCH_UPSTREAM_ADDR").
			WithValue(fmt.Sprintf("kubernetes:///%s.%s:dispatch", c.Name, c.Namespace)),
		applycorev1.EnvVar().WithName(c.EnvPrefix + "_DATASTORE_CONN_URI").
			WithValueFrom(applycorev1.EnvVarSource().WithSecretKeyRef(
				applycorev1.SecretKeySelector().WithName(c.SecretName).WithKey("datastore_uri"))),
		applycorev1.EnvVar().WithName(c.EnvPrefix + "_GRPC_PRESHARED_KEY").
			WithValueFrom(applycorev1.EnvVarSource().WithSecretKeyRef(
				applycorev1.SecretKeySelector().WithName(c.SecretName).WithKey("preshared_key"))),
	}

	// Passthrough config is user-provided and only affects spicedb runtime.
	keys := make([]string, 0, len(c.Passthrough))
	for k := range c.Passthrough {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		envVars = append(envVars, applycorev1.EnvVar().
			WithName(ToEnvVarName(c.EnvPrefix, k)).WithValue(c.Passthrough[k]))
	}

	return envVars
}

func (c *Config) ownerRef() *applymetav1.OwnerReferenceApplyConfiguration {
	return applymetav1.OwnerReference().
		WithName(c.Name).
		WithKind(v1alpha1.SpiceDBClusterKind).
		WithAPIVersion(v1alpha1.SchemeGroupVersion.String()).
		WithUID(types.UID(c.UID))
}

func (c *Config) serviceAccount() *applycorev1.ServiceAccountApplyConfiguration {
	return applycorev1.ServiceAccount(c.Name, c.Namespace).
		WithLabels(LabelsForComponent(c.Name, ComponentServiceAccountLabel)).
		WithOwnerReferences(c.ownerRef())
}

func (c *Config) role() *applyrbacv1.RoleApplyConfiguration {
	return applyrbacv1.Role(c.Name, c.Namespace).
		WithLabels(LabelsForComponent(c.Name, ComponentRoleLabel)).
		WithOwnerReferences(c.ownerRef()).
		WithRules(
			applyrbacv1.PolicyRule().
				WithAPIGroups("").
				WithResources("endpoints").
				WithVerbs("get", "list", "watch"),
		)
}

func (c *Config) roleBinding() *applyrbacv1.RoleBindingApplyConfiguration {
	return applyrbacv1.RoleBinding(c.Name, c.Namespace).
		WithLabels(LabelsForComponent(c.Name, ComponentRoleBindingLabel)).
		WithOwnerReferences(c.ownerRef()).
		WithRoleRef(applyrbacv1.RoleRef().
			WithKind("Role").
			WithName(c.Name),
		).WithSubjects(applyrbacv1.Subject().
		WithNamespace(c.Namespace).
		WithKind("ServiceAccount").WithName(c.Name),
	)
}

func (c *Config) service() *applycorev1.ServiceApplyConfiguration {
	return applycorev1.Service(c.Name, c.Namespace).
		WithLabels(LabelsForComponent(c.Name, ComponentServiceLabel)).
		WithOwnerReferences(c.ownerRef()).
		WithSpec(applycorev1.ServiceSpec().
			WithSelector(LabelsForComponent(c.Name, ComponentSpiceDBLabelValue)).
			WithPorts(
				applycorev1.ServicePort().WithName("grpc").WithPort(50051),
				applycorev1.ServicePort().WithName("dispatch").WithPort(50053),
				applycorev1.ServicePort().WithName("gateway").WithPort(8443),
				applycorev1.ServicePort().WithName("prometheus").WithPort(9090),
			),
		)
}

func (c *Config) jobVolumes() []*applycorev1.VolumeApplyConfiguration {
	volumes := make([]*applycorev1.VolumeApplyConfiguration, 0)
	if len(c.DatastoreTLSSecretName) > 0 {
		volumes = append(volumes, applycorev1.Volume().WithName("db-tls").WithSecret(applycorev1.SecretVolumeSource().WithDefaultMode(420).WithSecretName(c.DatastoreTLSSecretName)))
	}
	if len(c.SpannerCredsSecretRef) > 0 {
		volumes = append(volumes, applycorev1.Volume().WithName("spanner").WithSecret(applycorev1.SecretVolumeSource().WithDefaultMode(420).WithSecretName(c.SpannerCredsSecretRef).WithItems(
			applycorev1.KeyToPath().WithKey("credentials.json").WithPath("credentials.json"),
		)))
	}
	return volumes
}

func (c *Config) jobVolumeMounts() []*applycorev1.VolumeMountApplyConfiguration {
	volumeMounts := make([]*applycorev1.VolumeMountApplyConfiguration, 0)
	if len(c.DatastoreTLSSecretName) > 0 {
		volumeMounts = append(volumeMounts, applycorev1.VolumeMount().WithName("db-tls").WithMountPath("/db-tls").WithReadOnly(true))
	}
	if len(c.SpannerCredsSecretRef) > 0 {
		volumeMounts = append(volumeMounts, applycorev1.VolumeMount().WithName("spanner").WithMountPath("/spanner-credentials").WithReadOnly(true))
	}
	return volumeMounts
}

func (c *Config) migrationJob(migrationHash string) *applybatchv1.JobApplyConfiguration {
	name := fmt.Sprintf("%s-migrate-%s", c.Name, migrationHash[:15])
	envPrefix := c.SpiceConfig.EnvPrefix
	return applybatchv1.Job(name, c.Namespace).
		WithOwnerReferences(c.ownerRef()).
		WithLabels(LabelsForComponent(c.Name, ComponentMigrationJobLabelValue)).
		WithAnnotations(map[string]string{
			SpiceDBMigrationRequirementsKey: migrationHash,
		}).
		WithSpec(applybatchv1.JobSpec().WithTemplate(
			applycorev1.PodTemplateSpec().WithLabels(
				LabelsForComponent(c.Name, ComponentMigrationJobLabelValue),
			).WithSpec(applycorev1.PodSpec().WithContainers(
				applycorev1.Container().WithName(name).WithImage(c.TargetSpiceDBImage).WithCommand(c.MigrationConfig.SpiceDBCmd, "migrate", "head").WithEnv(
					applycorev1.EnvVar().WithName(envPrefix+"_LOG_LEVEL").WithValue(c.LogLevel),
					applycorev1.EnvVar().WithName(envPrefix+"_DATASTORE_ENGINE").WithValue(c.DatastoreEngine),
					applycorev1.EnvVar().WithName(envPrefix+"_DATASTORE_CONN_URI").WithValueFrom(applycorev1.EnvVarSource().WithSecretKeyRef(applycorev1.SecretKeySelector().WithName(c.SecretName).WithKey("datastore_uri"))),
					applycorev1.EnvVar().WithName(envPrefix+"_SECRETS").WithValueFrom(applycorev1.EnvVarSource().WithSecretKeyRef(applycorev1.SecretKeySelector().WithName(c.SecretName).WithKey("migration_secrets"))),
				).WithVolumeMounts(c.jobVolumeMounts()...).WithPorts(
					applycorev1.ContainerPort().WithName("grpc").WithContainerPort(50051),
					applycorev1.ContainerPort().WithName("dispatch").WithContainerPort(50053),
					applycorev1.ContainerPort().WithName("gateway").WithContainerPort(8443),
					applycorev1.ContainerPort().WithName("prometheus").WithContainerPort(9090),
				).WithTerminationMessagePolicy(corev1.TerminationMessageFallbackToLogsOnError),
			).WithVolumes(c.jobVolumes()...).WithRestartPolicy(corev1.RestartPolicyNever))))
}

func (c *Config) deploymentVolumes() []*applycorev1.VolumeApplyConfiguration {
	volumes := c.jobVolumes()
	// TODO: validate that the secrets exist before we start applying the deployment
	if len(c.TLSSecretName) > 0 {
		volumes = append(volumes, applycorev1.Volume().WithName("tls").WithSecret(applycorev1.SecretVolumeSource().WithDefaultMode(420).WithSecretName(c.TLSSecretName)))
	}
	if len(c.DispatchUpstreamCASecretName) > 0 {
		volumes = append(volumes, applycorev1.Volume().WithName("dispatch-tls").WithSecret(applycorev1.SecretVolumeSource().WithDefaultMode(420).WithSecretName(c.DispatchUpstreamCASecretName)))
	}
	if len(c.TelemetryTLSCASecretName) > 0 {
		volumes = append(volumes, applycorev1.Volume().WithName("telemetry-tls").WithSecret(applycorev1.SecretVolumeSource().WithDefaultMode(420).WithSecretName(c.TelemetryTLSCASecretName)))
	}
	return volumes
}

func (c *Config) deploymentVolumeMounts() []*applycorev1.VolumeMountApplyConfiguration {
	volumeMounts := c.jobVolumeMounts()
	// TODO: validate that the secrets exist before we start applying the deployment
	if len(c.TLSSecretName) > 0 {
		volumeMounts = append(volumeMounts, applycorev1.VolumeMount().WithName("tls").WithMountPath("/tls").WithReadOnly(true))
	}
	if len(c.DispatchUpstreamCASecretName) > 0 {
		volumeMounts = append(volumeMounts, applycorev1.VolumeMount().WithName("dispatch-tls").WithMountPath("/dispatch-tls").WithReadOnly(true))
	}
	if len(c.TelemetryTLSCASecretName) > 0 {
		volumeMounts = append(volumeMounts, applycorev1.VolumeMount().WithName("telemetry-tls").WithMountPath("/telemetry-tls").WithReadOnly(true))
	}
	return volumeMounts
}

func (c *Config) probeCmd() []string {
	probeCmd := []string{"grpc_health_probe", "-v", "-addr=localhost:50051"}

	if len(c.TLSSecretName) > 0 {
		probeCmd = append(probeCmd, "-tls", "-tls-ca-cert=/tls/tls.crt")
	}
	return probeCmd
}

func (c *Config) deployment(migrationHash string) *applyappsv1.DeploymentApplyConfiguration {
	name := fmt.Sprintf("%s-spicedb", c.Name)
	return applyappsv1.Deployment(name, c.Namespace).WithOwnerReferences(c.ownerRef()).
		WithLabels(LabelsForComponent(c.Name, ComponentSpiceDBLabelValue)).
		WithAnnotations(map[string]string{
			SpiceDBMigrationRequirementsKey: migrationHash,
		}).
		WithSpec(applyappsv1.DeploymentSpec().
			WithReplicas(c.Replicas).
			WithSelector(applymetav1.LabelSelector().WithMatchLabels(map[string]string{"app.kubernetes.io/instance": name})).
			WithTemplate(applycorev1.PodTemplateSpec().
				WithLabels(map[string]string{"app.kubernetes.io/instance": name}).
				WithLabels(LabelsForComponent(c.Name, ComponentSpiceDBLabelValue)).
				WithSpec(applycorev1.PodSpec().WithServiceAccountName(c.Name).WithContainers(
					applycorev1.Container().WithName(name).WithImage(c.TargetSpiceDBImage).
						WithCommand(c.SpiceConfig.SpiceDBCmd, "serve").
						WithEnv(c.ToEnvVarApplyConfiguration()...).
						WithPorts(
							applycorev1.ContainerPort().WithContainerPort(50051).WithName("grpc"),
							applycorev1.ContainerPort().WithContainerPort(50053).WithName("dispatch"),
							applycorev1.ContainerPort().WithContainerPort(8443).WithName("gateway"),
							applycorev1.ContainerPort().WithContainerPort(9090).WithName("metrics"),
						).WithLivenessProbe(
						applycorev1.Probe().WithExec(applycorev1.ExecAction().WithCommand(c.probeCmd()...)).
							WithInitialDelaySeconds(60).WithFailureThreshold(5).WithPeriodSeconds(10).WithTimeoutSeconds(5),
					).WithReadinessProbe(
						applycorev1.Probe().WithExec(applycorev1.ExecAction().WithCommand(c.probeCmd()...)).
							WithFailureThreshold(5).WithPeriodSeconds(10).WithTimeoutSeconds(5),
					).WithVolumeMounts(c.deploymentVolumeMounts()...),
				).WithVolumes(c.deploymentVolumes()...))))
}

// ToEnvVarName converts a key from the api object into an env var name.
// the key isCamelCased will be converted to PREFIX_IS_CAMEL_CASED
func ToEnvVarName(prefix string, key string) string {
	envVarParts := []string{strings.ToUpper(prefix)}
	for _, p := range camelcase.Split(key) {
		envVarParts = append(envVarParts, strings.ToUpper(p))
	}
	return strings.Join(envVarParts, "_")
}
