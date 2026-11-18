package config

import (
	"time"
)

func newDefaults() *Config {
	return &Config{
		PollInterval:       envDuration("POLL_INTERVAL", 15*time.Second),
		ResyncInterval:     envDuration("RESYNC_INTERVAL", 10*time.Minute),
		Endpoint:           envString("CONFIG_ENDPOINT", "https://pangolin:3001/api/v1/traefik-config"),
		Namespace:          envString("TARGET_NAMESPACE", "pangolin"),
		MaxBackoff:         envDuration("MAX_BACKOFF", 2*time.Minute),
		LeaderEnabled:      envBool("ENABLE_LEADER_ELECTION", false),
		LeaseLockName:      envString("LEASE_LOCK_NAME", "pangolin-kube-controller-leader"),
		LeaseLockNamespace: envString("LEASE_LOCK_NAMESPACE", envString("TARGET_NAMESPACE", "pangolin")),
		LeaseDuration:      envDuration("LEASE_DURATION", 30*time.Second),
		RenewDeadline:      envDuration("RENEW_DEADLINE", 20*time.Second),
		RetryPeriod:        envDuration("RETRY_PERIOD", 5*time.Second),
		ReadOnly:           envBool("READ_ONLY", false),
		StandaloneHTTPOnly: envBool("STANDALONE_HTTP_ONLY", false),
		LogTraefikConfig:   envBool("LOG_TRAEFIK_CONFIG", false),
		LogConfigPreview:   envBool("CONFIG_LOG_PREVIEW", false),
		OnLoseBehavior:     envString("ON_LOSE", "exit"),
		MaxConfigLogBytes:  envInt("MAX_CONFIG_LOG_BYTES", 0),
		IngressClass:       envString("INGRESS_CLASS", "traefik"),
		ManagedAnnoKey:     envString("MANAGED_ANNOTATION_KEY", "pangolin.io/managed-by"),
		ManagedAnnoValue:   envString("MANAGED_ANNOTATION_VALUE", "pangolin-kube-controller"),
		ManagedLabelKey:    envString("MANAGED_LABEL_KEY", "app.kubernetes.io/managed-by"),
		ManagedLabelValue:  envString("MANAGED_LABEL_VALUE", "pangolin-kube-controller"),
		SSAForce:           envBool("SSA_FORCE", false),

		IngressClassLabelVerifyInterval: envDuration("INGRESS_CLASS_LABEL_VERIFY_INTERVAL", 3*time.Hour),
		IngressClassLabelStrict:         envBool("INGRESS_CLASS_LABEL_STRICT", false),
		ConfigFile:                      envString("CONFIG_FILE", ""),

		FetchTimeout:         envDuration("FETCH_TIMEOUT", 30*time.Second),
		FetchLogInterval:     envDuration("FETCH_LOG_INTERVAL", 5*time.Minute),
		AuthHeader:           envString("CONFIG_AUTH_HEADER", ""),
		CAFile:               envString("CONFIG_CA_FILE", ""),
		ClientCertFile:       envString("CONFIG_CLIENT_CERT_FILE", ""),
		ClientKeyFile:        envString("CONFIG_CLIENT_KEY_FILE", ""),
		TLSSkipVerify:        envBool("CONFIG_TLS_SKIP_VERIFY", false),
		MaxResponseBodyBytes: envInt64("MAX_RESPONSE_BODY_BYTES", 50*1024*1024),

		HTTPMaxIdleConns:        envInt("HTTP_MAX_IDLE_CONNS", 100),
		HTTPMaxIdleConnsPerHost: envInt("HTTP_MAX_IDLE_CONNS_PER_HOST", 100),
		HTTPIdleConnTimeout:     envDuration("HTTP_IDLE_CONN_TIMEOUT", 90*time.Second),

		ClientQPS:   envFloat("CLIENT_QPS", 0),
		ClientBurst: envInt("CLIENT_BURST", 0),

		ReconcileParallel: envBool("RECONCILE_PARALLEL", false),
		ReconcileMax:      envInt("RECONCILE_MAX", 3),

		GCWorkers:        envInt("GC_WORKERS", 2),
		GCGracePeriod:    envDuration("GC_GRACE_PERIOD", 0),
		GCGraceQueueSize: envInt("GC_GRACE_QUEUE_SIZE", 256),

		TraefikLBURL:    envString("TRAEFIK_LB_URL", ""),
		TraefikLBIP:     envString("TRAEFIK_LB_IP", ""),
		TraefikLBScheme: envString("TRAEFIK_LB_SCHEME", "http"),
		TraefikLBPort:   envString("TRAEFIK_LB_PORT", ""),

		MetricsAddr:              envString("METRICS_ADDR", ":9090"),
		DisableLivez:             envBool("DISABLE_LIVEZ", false),
		EnablePprof:              envBool("ENABLE_PPROF", false),
		MetricsTLSCertFile:       envString("METRICS_TLS_CERT_FILE", ""),
		MetricsTLSKeyFile:        envString("METRICS_TLS_KEY_FILE", ""),
		MetricsPlaintextOK:       envBool("METRICS_PLAINTEXT_OK", false),
		MetricsReadHeaderTimeout: envDuration("METRICS_READ_HEADER_TIMEOUT", 3*time.Second),
		AllowInsecureHTTP:        envBool("CONFIG_ALLOW_INSECURE_HTTP", false),

		CertificateSecrets: parseCertificateSecretsEnv(envString("CERTIFICATE_SECRETS", "")),
	}
}
