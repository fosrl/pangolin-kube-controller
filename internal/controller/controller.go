package controller

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	logrus "github.com/sirupsen/logrus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"

	"pangolin-kube-controller/internal/config"
	prometheus "pangolin-kube-controller/internal/observability/metrics_prometheus"
	traefikconfig "pangolin-kube-controller/internal/transform/config"
)

type Controller struct {
	lastSuccessfulReconcile atomic.Int64
	cfg                     *config.Config
	dyn                     dynamic.Interface
	kube                    kubernetes.Interface
	collector               *prometheus.Collector
	httpClient              *http.Client
	logger                  *logrus.Logger

	gvrMiddleware          schema.GroupVersionResource
	gvrIngressRoute        schema.GroupVersionResource
	gvrTraefikService      schema.GroupVersionResource
	gvrServersTransport    schema.GroupVersionResource
	gvrIngressRouteTCP     schema.GroupVersionResource
	gvrIngressRouteUDP     schema.GroupVersionResource
	gvrServersTransportTCP schema.GroupVersionResource
	gvrTLSOption           schema.GroupVersionResource

	gcSem chan struct{}

	graceDelQueue chan graceDeleteReq
	graceDelOnce  sync.Once

	exitRequested atomic.Bool
}

const (
	fieldManagerName      = "pangolin-kube-controller"
	headerIfNoneMatch     = "If-None-Match"
	headerETag            = "ETag"
	headerAuthorization   = "Authorization"
	PreviewLogFieldPrefix = "preview="
)

var resourceToKind = map[string]string{
	"ingressroutes":        "IngressRoute",
	"middlewares":          "Middleware",
	"traefikservices":      "TraefikService",
	"serverstransports":    "ServersTransport",
	"ingressroutetcps":     "IngressRouteTCP",
	"ingressrouteudps":     "IngressRouteUDP",
	"serverstransporttcps": "ServersTransportTCP",
	"tlsoptions":           "TLSOption",
	"services":             "Service",
	"routers":              "Router",
	"sites":                "Site",
	"targets":              "Target",
	"users":                "User",
	"clients":              "Client",
	"orgs":                 "Org",
	"resources":            "Resource",
}

func NewController(cfg *config.Config, dyn dynamic.Interface, kube kubernetes.Interface, collector *prometheus.Collector) *Controller {
	c := &Controller{
		cfg:       cfg,
		dyn:       dyn,
		kube:      kube,
		collector: collector,
		logger:    logrus.StandardLogger(),
		gvrMiddleware: schema.GroupVersionResource{
			Group:    traefikconfig.Group,
			Version:  traefikconfig.Version,
			Resource: "middlewares",
		},
		gvrIngressRoute: schema.GroupVersionResource{
			Group:    traefikconfig.Group,
			Version:  traefikconfig.Version,
			Resource: "ingressroutes",
		},
		gvrTraefikService: schema.GroupVersionResource{
			Group:    traefikconfig.Group,
			Version:  traefikconfig.Version,
			Resource: "traefikservices",
		},
		gvrServersTransport: schema.GroupVersionResource{
			Group:    traefikconfig.Group,
			Version:  traefikconfig.Version,
			Resource: "serverstransports",
		},
		gvrIngressRouteTCP: schema.GroupVersionResource{
			Group:    traefikconfig.Group,
			Version:  traefikconfig.Version,
			Resource: "ingressroutetcps",
		},
		gvrIngressRouteUDP: schema.GroupVersionResource{
			Group:    traefikconfig.Group,
			Version:  traefikconfig.Version,
			Resource: "ingressrouteudps",
		},
		gvrServersTransportTCP: schema.GroupVersionResource{
			Group:    traefikconfig.Group,
			Version:  traefikconfig.Version,
			Resource: "serverstransporttcps",
		},
		gvrTLSOption: schema.GroupVersionResource{
			Group:    traefikconfig.Group,
			Version:  traefikconfig.Version,
			Resource: "tlsoptions",
		},
	}

	workers := cfg.GCWorkers
	if workers <= 0 {
		workers = 1
	}
	c.gcSem = make(chan struct{}, workers)
	c.httpClient = c.buildHTTPClientFromConfig(cfg)
	if c.collector != nil {
		c.collector.Ready.Set(0)
	}

	return c
}

func (c *Controller) Run(ctx context.Context) {
	c.startGraceDeletionPool(ctx, c.cfg.GCWorkers)
	c.runLoop(ctx)
}

func (c *Controller) RunLeaderElection(ctx context.Context) {
	c.startGraceDeletionPool(ctx, c.cfg.GCWorkers)
	if !c.cfg.LeaderEnabled {
		c.runLoop(ctx)
		return
	}
	id := c.makeLeaderIdentity()
	lock := &resourcelock.LeaseLock{
		LeaseMeta: metav1.ObjectMeta{
			Name:      c.cfg.LeaseLockName,
			Namespace: c.cfg.LeaseLockNamespace,
		},
		Client: c.kube.CoordinationV1(),
		LockConfig: resourcelock.ResourceLockConfig{
			Identity: id,
		},
	}
	lctx, cancel := context.WithCancel(ctx)
	leaderelection.RunOrDie(lctx, leaderelection.LeaderElectionConfig{
		Lock:            lock,
		ReleaseOnCancel: true,
		LeaseDuration:   c.cfg.LeaseDuration,
		RenewDeadline:   c.cfg.RenewDeadline,
		RetryPeriod:     c.cfg.RetryPeriod,
		Callbacks:       c.leaderCallbacks(id, cancel),
	})
}

func (c *Controller) leaderCallbacks(id string, cancel context.CancelFunc) leaderelection.LeaderCallbacks {
	return leaderelection.LeaderCallbacks{
		OnStartedLeading: func(ctx context.Context) {
			c.OnStartedLeading(ctx, id)
		},
		OnStoppedLeading: func() {
			c.OnStoppedLeading(cancel)
		},
		OnNewLeader: func(current string) {
			onNewLeader(id, current)
		},
	}
}

func (c *Controller) handleLeadershipLoss(cancel context.CancelFunc) {
	behavior := c.cfg.OnLoseBehavior
	if strings.EqualFold(behavior, "exit") || behavior == "" {
		logrus.Warn("Lost leadership; marking exit requested (ON_LOSE=exit)")
		c.exitRequested.Store(true)
		cancel()
		return
	}
	if strings.EqualFold(behavior, "pause") {
		if c.collector != nil {
			c.collector.Ready.Set(0)
		}
		logrus.Warnf("Lost leadership; pausing reconciliation (ON_LOSE=%s)", behavior)
		return
	}
	logrus.Warnf("Lost leadership; unknown ON_LOSE=%q, treating as exit", behavior)
	c.exitRequested.Store(true)
	cancel()
}

func (c *Controller) ExitRequested() bool { return c.exitRequested.Load() }

func (c *Controller) makeLeaderIdentity() string {
	podName := os.Getenv("POD_NAME")
	if podName == "" {
		podName = c.hostnameFallback()
	}
	uid := os.Getenv("POD_UID")
	if uid == "" {
		uid = strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	return fmt.Sprintf("%s_%s", podName, uid)
}

func (*Controller) hostnameFallback() string {
	h, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return h
}

func ignoreFieldValidation(kind string) bool {
	return kind == "TraefikService"
}

func kindFor(resource string) string {
	if k, ok := resourceToKind[resource]; ok {
		return k
	}
	return titleCaseFirst(resource)
}

func titleCaseFirst(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

type graceDeleteReq struct {
	kind  string
	name  string
	delay time.Duration
}
