package config

import (
	"time"
)

// CertificateSecretRef identifies a Kubernetes TLS Secret to expose through
// the /api/v1/certificates endpoint.
type CertificateSecretRef struct {
	SecretName string `json:"secretName" yaml:"secretName"`
	Namespace  string `json:"namespace,omitempty" yaml:"namespace,omitempty"`
}

type Config struct {
	PollInterval time.Duration
	// ResyncInterval forces a full apply even when Pangolin's config is
	// unchanged, repairing objects that were edited or deleted by hand.
	// Zero disables it.
	ResyncInterval       time.Duration
	Endpoint             string
	Namespace            string
	MaxBackoff           time.Duration
	LeaderEnabled        bool
	LeaseLockName        string
	LeaseLockNamespace   string
	LeaseDuration        time.Duration
	RenewDeadline        time.Duration
	RetryPeriod          time.Duration
	ReadOnly             bool
	StandaloneHTTPOnly   bool
	LogTraefikConfig     bool
	LogConfigPreview     bool
	OnLoseBehavior       string
	MaxConfigLogBytes    int
	IngressClass         string
	IngressClassProvided bool
	ManagedAnnoKey       string
	ManagedAnnoValue     string
	ManagedLabelKey      string
	ManagedLabelValue    string
	SSAForce             bool

	TraefikInstanceLabelKey   string
	TraefikInstanceLabelValue string

	IngressClassLabelVerifyInterval time.Duration
	IngressClassLabelStrict         bool

	ConfigFile string

	FetchTimeout         time.Duration
	FetchLogInterval     time.Duration
	AuthHeader           string
	CAFile               string
	ClientCertFile       string
	ClientKeyFile        string
	TLSSkipVerify        bool
	MaxResponseBodyBytes int64

	HTTPMaxIdleConns        int
	HTTPMaxIdleConnsPerHost int
	HTTPIdleConnTimeout     time.Duration

	ClientQPS   float64
	ClientBurst int

	ReconcileParallel bool
	ReconcileMax      int

	GCWorkers        int
	GCGracePeriod    time.Duration
	GCGraceQueueSize int

	TraefikLBURL    string
	TraefikLBIP     string
	TraefikLBScheme string
	TraefikLBPort   string

	MetricsAddr              string
	DisableLivez             bool
	EnablePprof              bool
	MetricsTLSCertFile       string
	MetricsTLSKeyFile        string
	MetricsPlaintextOK       bool
	MetricsReadHeaderTimeout time.Duration
	AllowInsecureHTTP        bool

	CertificateSecrets []CertificateSecretRef
}

func LoadFromEnv() *Config {
	cfg := newDefaults()
	markIngressClassProvided(cfg)
	populateInstanceLabelFromEnv(cfg)
	mergeConfigFile(cfg)
	cfg.normalize()
	return cfg
}
