package controller

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"fmt"
	"sort"
	"time"

	logrus "github.com/sirupsen/logrus"

	"golang.org/x/sync/errgroup"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"

	"pangolin-kube-controller/internal/apply"
	"pangolin-kube-controller/internal/kube/resources"
	"pangolin-kube-controller/internal/observability/metrics_otel"
	traefikconfig "pangolin-kube-controller/internal/transform/config"
	"pangolin-kube-controller/internal/transform/protocol"
	"pangolin-kube-controller/internal/transform/routing"
	"pangolin-kube-controller/internal/transform/sanitize"

	"go.opentelemetry.io/otel/metric"
)

// crdSet is the desired state of one Traefik CRD kind. Spec-only kinds fill
// specs; route kinds, whose objects are built whole, fill objects. keep holds
// names whose conversion failed: their existing objects stay in place instead
// of being garbage-collected, so one malformed Pangolin entry does not take
// down a route that currently works.
type crdSet struct {
	kind    string
	gvr     schema.GroupVersionResource
	specs   map[string]json.RawMessage
	objects map[string]map[string]interface{}
	keep    map[string]struct{}
}

func (s *crdSet) names() map[string]json.RawMessage {
	out := make(map[string]json.RawMessage, len(s.specs)+len(s.objects)+len(s.keep))
	for n := range s.specs {
		out[n] = nil
	}
	for n := range s.objects {
		out[n] = nil
	}
	for n := range s.keep {
		out[n] = nil
	}
	return out
}

// desiredState is everything one Pangolin config maps to.
type desiredState struct {
	crds     []*crdSet
	services []*corev1.Service
	slices   []*discoveryv1.EndpointSlice
	secrets  []*corev1.Secret
	failures []error
}

func (c *Controller) applyConfig(ctx context.Context, cfg *traefikconfig.Config) error {
	sanitizedCfg, err := sanitize.SanitizeTraefikConfig(cfg)
	if err != nil {
		return fmt.Errorf("sanitize config: %w", err)
	}
	if sanitizedCfg == nil {
		return nil
	}
	ds := c.buildDesired(sanitizedCfg)
	for _, f := range ds.failures {
		logrus.Warnf("Skipping Pangolin entry that cannot be converted (existing objects are kept): %v", f)
	}

	if err := c.timedPhase(ctx, "backends", func(ctx context.Context) error { return c.applyCoreObjects(ctx, ds) }); err != nil {
		return fmt.Errorf("backends reconcile failed: %w", err)
	}
	if err := c.reconcileCRDs(ctx, ds.crds); err != nil {
		return err
	}
	if len(ds.failures) > 0 {
		logrus.Warnf("Skipping garbage collection of Services, EndpointSlices and Secrets: %d entries failed to convert", len(ds.failures))
		return nil
	}
	if err := c.timedPhase(ctx, "backends-gc", func(ctx context.Context) error { return c.gcCoreObjects(ctx, ds) }); err != nil {
		return fmt.Errorf("backends garbage collection failed: %w", err)
	}
	return nil
}

// buildDesired converts the sanitized config into Kubernetes objects. It does
// not touch the cluster.
func (c *Controller) buildDesired(cfg *traefikconfig.Config) *desiredState {
	ns := c.cfg.Namespace
	ds := &desiredState{}
	newSet := func(kind string, gvr schema.GroupVersionResource) *crdSet {
		s := &crdSet{kind: kind, gvr: gvr, specs: map[string]json.RawMessage{}, objects: map[string]map[string]interface{}{}, keep: map[string]struct{}{}}
		ds.crds = append(ds.crds, s)
		return s
	}
	fail := func(set *crdSet, failed map[string]error) {
		for _, name := range sortedNames(failed) {
			set.keep[name] = struct{}{}
			ds.failures = append(ds.failures, failed[name])
		}
	}

	middlewares := newSet("Middleware", c.gvrMiddleware)
	for n, raw := range cfg.HTTP.Middlewares {
		middlewares.specs[n] = raw
	}
	transports := newSet("ServersTransport", c.gvrServersTransport)
	for n, raw := range cfg.HTTP.ServersTransports {
		transports.specs[n] = raw
	}

	httpArt := protocol.TransformHTTPServices(c.cfg, ns, cfg.HTTP.Services)
	traefikServices := newSet("TraefikService", c.gvrTraefikService)
	traefikServices.specs = httpArt.TraefikServices
	fail(traefikServices, httpArt.Failed)
	ds.services = append(ds.services, httpArt.Services...)
	ds.slices = append(ds.slices, httpArt.Slices...)

	routes := newSet("IngressRoute", c.gvrIngressRoute)
	routeErrs := map[string]error{}
	for name, raw := range cfg.HTTP.Routers {
		u, err := routing.TransformRouterToIngressRoute(name, raw, routing.RouterConfig{
			Namespace:         ns,
			ManagedLabelKey:   c.cfg.ManagedLabelKey,
			ManagedLabelValue: c.cfg.ManagedLabelValue,
			ManagedAnnoKey:    c.cfg.ManagedAnnoKey,
			ManagedAnnoValue:  c.cfg.ManagedAnnoValue,
			IngressClass:      c.cfg.IngressClass,
		})
		if err != nil {
			routeErrs[name] = fmt.Errorf("router %s: %w", name, err)
			continue
		}
		routes.objects[name] = u
	}
	fail(routes, routeErrs)

	tcp := protocol.TransformTCP(cfg, ns)
	tcpTransports := newSet("ServersTransportTCP", c.gvrServersTransportTCP)
	tcpTransports.specs = tcp.Transports
	tcpRoutes := newSet("IngressRouteTCP", c.gvrIngressRouteTCP)
	tcpRoutes.objects = tcp.Routes
	fail(tcpRoutes, tcp.Failed)
	ds.services = append(ds.services, tcp.Services...)
	ds.slices = append(ds.slices, tcp.Slices...)

	udp := protocol.TransformUDP(cfg, ns)
	udpRoutes := newSet("IngressRouteUDP", c.gvrIngressRouteUDP)
	udpRoutes.objects = udp.Routes
	fail(udpRoutes, udp.Failed)
	ds.services = append(ds.services, udp.Services...)
	ds.slices = append(ds.slices, udp.Slices...)

	var tlsOptions map[string]json.RawMessage
	if cfg.TLS != nil {
		tlsOptions = cfg.TLS.Options
	}
	tls := protocol.TransformTLSOptions(ns, tlsOptions)
	tlsSet := newSet("TLSOption", c.gvrTLSOption)
	tlsSet.specs = tls.Options
	fail(tlsSet, tls.Failed)
	ds.secrets = tls.Secrets

	for _, s := range ds.crds {
		if s.specs == nil {
			s.specs = map[string]json.RawMessage{}
		}
		if s.objects == nil {
			s.objects = map[string]map[string]interface{}{}
		}
	}
	return ds
}

func sortedNames(m map[string]error) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (c *Controller) timedPhase(ctx context.Context, name string, fn func(context.Context) error) error {
	start := time.Now()
	err := fn(ctx)
	if c.collector != nil && c.collector.OTel != nil {
		res := "success"
		if err != nil {
			res = "error"
		}
		c.collector.OTel.ReconcilePhaseDuration.Record(ctx, time.Since(start).Seconds(),
			metric.WithAttributes(
				metrics_otel.AttrPhase.String(name),
				metrics_otel.AttrResult.String(res),
			),
		)
	}
	return err
}

// reconcileCRDs applies and garbage-collects every Traefik CRD kind, one
// phase per kind, sequentially or with bounded parallelism.
func (c *Controller) reconcileCRDs(ctx context.Context, sets []*crdSet) error {
	if !c.cfg.ReconcileParallel {
		var errs []error
		for _, s := range sets {
			if err := c.timedPhase(ctx, s.kind, func(ctx context.Context) error { return c.reconcileSet(ctx, s) }); err != nil {
				errs = append(errs, fmt.Errorf("%s reconcile failed: %w", s.kind, err))
			}
		}
		return stderrors.Join(errs...)
	}
	concurrency := c.cfg.ReconcileMax
	if concurrency < 1 {
		concurrency = 1
	}
	// A failing kind must not cancel the others, so errors are collected
	// rather than propagated through a shared context.
	var g errgroup.Group
	g.SetLimit(concurrency)
	errs := make([]error, len(sets))
	for i, s := range sets {
		g.Go(func() error {
			if c.collector != nil && c.collector.OTel != nil {
				c.collector.OTel.ActiveReconcileRoutines.Add(ctx, 1, metric.WithAttributes(metrics_otel.AttrPhase.String(s.kind)))
				defer c.collector.OTel.ActiveReconcileRoutines.Add(ctx, -1, metric.WithAttributes(metrics_otel.AttrPhase.String(s.kind)))
			}
			if err := c.timedPhase(ctx, s.kind, func(ctx context.Context) error { return c.reconcileSet(ctx, s) }); err != nil {
				errs[i] = fmt.Errorf("%s reconcile failed: %w", s.kind, err)
			}
			return nil
		})
	}
	_ = g.Wait()
	return stderrors.Join(errs...)
}

func (c *Controller) reconcileSet(ctx context.Context, s *crdSet) error {
	var errs []error
	if len(s.specs) > 0 {
		errs = append(errs, c.applyDesiredObjects(ctx, c.dyn, s.gvr, s.specs))
	}
	if len(s.objects) > 0 {
		errs = append(errs, c.applyRouteObjects(ctx, s))
	}
	// Objects that failed to apply are still desired, so GC leaves them alone.
	errs = append(errs, c.gcStaleObjects(ctx, c.dyn, s.gvr, s.names(), s.kind))
	return stderrors.Join(errs...)
}

func (c *Controller) metaConfig() apply.MetadataConfig {
	return apply.MetadataConfig{
		ManagedLabelKey:           c.cfg.ManagedLabelKey,
		ManagedLabelValue:         c.cfg.ManagedLabelValue,
		TraefikInstanceLabelKey:   c.cfg.TraefikInstanceLabelKey,
		TraefikInstanceLabelValue: c.cfg.TraefikInstanceLabelValue,
		ManagedAnnoKey:            c.cfg.ManagedAnnoKey,
		ManagedAnnoValue:          c.cfg.ManagedAnnoValue,
		IngressClass:              c.cfg.IngressClass,
	}
}

func (c *Controller) applyRouteObjects(ctx context.Context, s *crdSet) error {
	if c.cfg.ReadOnly {
		return nil
	}
	resIfc := resources.AdaptResource(c.dyn.Resource(s.gvr).Namespace(c.cfg.Namespace))
	ops := &apply.IngressRouteOps{
		ResIfc:                    resIfc,
		Namespace:                 c.cfg.Namespace,
		ManagedLabelKey:           c.cfg.ManagedLabelKey,
		ManagedLabelValue:         c.cfg.ManagedLabelValue,
		ManagedAnnoKey:            c.cfg.ManagedAnnoKey,
		ManagedAnnoValue:          c.cfg.ManagedAnnoValue,
		IngressClass:              c.cfg.IngressClass,
		ReadOnly:                  c.cfg.ReadOnly,
		TraefikInstanceLabelKey:   c.cfg.TraefikInstanceLabelKey,
		TraefikInstanceLabelValue: c.cfg.TraefikInstanceLabelValue,
	}
	names := make([]string, 0, len(s.objects))
	for n := range s.objects {
		names = append(names, n)
	}
	sort.Strings(names)
	// One object the API server rejects (for example a load-balancing strategy
	// an older Traefik CRD does not know) must not block the others.
	var errs []error
	for _, name := range names {
		u := s.objects[name]
		meta, _ := u["metadata"].(map[string]interface{})
		if meta == nil {
			meta = map[string]interface{}{}
			u["metadata"] = meta
		}
		meta["name"] = name
		meta["namespace"] = c.cfg.Namespace
		if err := ops.ApplySingle(ctx, u, s.kind); err != nil {
			errs = append(errs, fmt.Errorf("%s %s: %w", s.kind, name, err))
		}
	}
	return stderrors.Join(errs...)
}

func (c *Controller) applyDesiredObjects(ctx context.Context, dyn dynamic.Interface, gvr schema.GroupVersionResource, objects map[string]json.RawMessage) error {
	if c.cfg.ReadOnly {
		return nil
	}
	ops := &apply.UnstructuredOps{
		Dyn:       dyn,
		GVR:       gvr,
		Namespace: c.cfg.Namespace,
		SSAForce:  c.cfg.SSAForce,
	}
	metaCfg := c.metaConfig()
	names := make([]string, 0, len(objects))
	for n := range objects {
		names = append(names, n)
	}
	sort.Strings(names)
	var errs []error
	for _, name := range names {
		if err := ops.Apply(ctx, name, objects[name], metaCfg); err != nil {
			errs = append(errs, fmt.Errorf("%s %s: %w", apply.KindFor(gvr.Resource), name, err))
		}
	}
	return stderrors.Join(errs...)
}

func (c *Controller) gcStaleObjects(ctx context.Context, dyn dynamic.Interface, gvr schema.GroupVersionResource, desiredObjs map[string]json.RawMessage, kind string) error {
	resIfc := resources.AdaptResource(dyn.Resource(gvr).Namespace(c.cfg.Namespace))
	existing, err := resIfc.List(ctx, metav1.ListOptions{LabelSelector: c.managedSelector()})
	if err != nil {
		return err
	}
	desired := buildDesiredSet(desiredObjs)
	for _, item := range existing.Items {
		name, stale := c.staleManagedName(item, desired)
		if !stale {
			continue
		}
		if c.cfg.ReadOnly {
			continue
		}
		if c.cfg.GCGracePeriod > 0 {
			c.scheduleGraceDeletion(ctx, kind, name)
			continue
		}
		if err := deleteImmediate(ctx, resIfc, name); err != nil {
			return err
		}
		logrus.Infof("Deleted stale %s %s", kind, name)
	}
	return nil
}

func (c *Controller) managedSelector() string {
	return fmt.Sprintf("%s=%s", c.cfg.ManagedLabelKey, c.cfg.ManagedLabelValue)
}

func buildDesiredSet(objects map[string]json.RawMessage) map[string]struct{} {
	desired := make(map[string]struct{}, len(objects))
	for name := range objects {
		desired[name] = struct{}{}
	}
	return desired
}

func (c *Controller) staleManagedName(obj unstructured.Unstructured, desired map[string]struct{}) (string, bool) {
	name := obj.GetName()
	if _, ok := desired[name]; ok {
		return name, false
	}
	if obj.GetAnnotations()[c.cfg.ManagedAnnoKey] != c.cfg.ManagedAnnoValue {
		return name, false
	}
	return name, true
}

func (c *Controller) scheduleGraceDeletion(ctx context.Context, kind, name string) {
	delay := c.cfg.GCGracePeriod
	c.enqueueGraceDeletion(ctx, graceDeleteReq{kind: kind, name: name, delay: delay})
}

func deleteImmediate(ctx context.Context, resIfc resources.ResourceClient, name string) error {
	if err := resIfc.Delete(ctx, name, metav1.DeleteOptions{}); err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		return err
	}
	return nil
}

// ---------------------------------------------------------------------------
// Core objects (Services, EndpointSlices, Secrets)
// ---------------------------------------------------------------------------

func (c *Controller) applyCoreObjects(ctx context.Context, ds *desiredState) error {
	if c.cfg.ReadOnly {
		return nil
	}
	for _, s := range ds.secrets {
		c.markManaged(&s.ObjectMeta)
		if err := c.applyCore(ctx, "Secret", s.Name, s, func(ctx context.Context) (metav1.Object, error) {
			return c.kube.CoreV1().Secrets(c.cfg.Namespace).Get(ctx, s.Name, metav1.GetOptions{})
		}, func(ctx context.Context, data []byte, opts metav1.PatchOptions) error {
			_, err := c.kube.CoreV1().Secrets(c.cfg.Namespace).Patch(ctx, s.Name, types.ApplyPatchType, data, opts)
			return err
		}); err != nil {
			return err
		}
	}
	for _, svc := range ds.services {
		c.markManaged(&svc.ObjectMeta)
		if err := c.applyCore(ctx, "Service", svc.Name, svc, func(ctx context.Context) (metav1.Object, error) {
			return c.kube.CoreV1().Services(c.cfg.Namespace).Get(ctx, svc.Name, metav1.GetOptions{})
		}, func(ctx context.Context, data []byte, opts metav1.PatchOptions) error {
			_, err := c.kube.CoreV1().Services(c.cfg.Namespace).Patch(ctx, svc.Name, types.ApplyPatchType, data, opts)
			return err
		}); err != nil {
			return err
		}
	}
	for _, es := range ds.slices {
		c.markManaged(&es.ObjectMeta)
		if err := c.applyCore(ctx, "EndpointSlice", es.Name, es, func(ctx context.Context) (metav1.Object, error) {
			return c.kube.DiscoveryV1().EndpointSlices(c.cfg.Namespace).Get(ctx, es.Name, metav1.GetOptions{})
		}, func(ctx context.Context, data []byte, opts metav1.PatchOptions) error {
			_, err := c.kube.DiscoveryV1().EndpointSlices(c.cfg.Namespace).Patch(ctx, es.Name, types.ApplyPatchType, data, opts)
			return err
		}); err != nil {
			return err
		}
	}
	return nil
}

func (c *Controller) markManaged(meta *metav1.ObjectMeta) {
	meta.Namespace = c.cfg.Namespace
	if meta.Labels == nil {
		meta.Labels = map[string]string{}
	}
	meta.Labels[c.cfg.ManagedLabelKey] = c.cfg.ManagedLabelValue
	if c.cfg.TraefikInstanceLabelKey != "" && c.cfg.TraefikInstanceLabelValue != "" {
		meta.Labels[c.cfg.TraefikInstanceLabelKey] = c.cfg.TraefikInstanceLabelValue
	}
	if meta.Annotations == nil {
		meta.Annotations = map[string]string{}
	}
	meta.Annotations[c.cfg.ManagedAnnoKey] = c.cfg.ManagedAnnoValue
}

// applyCore server-side applies one core object. An existing object of the
// same name that the controller does not manage is never taken over.
func (c *Controller) applyCore(ctx context.Context, kind, name string, obj interface{}, get func(context.Context) (metav1.Object, error), patch func(context.Context, []byte, metav1.PatchOptions) error) error {
	existing, err := get(ctx)
	switch {
	case err == nil:
		if existing.GetAnnotations()[c.cfg.ManagedAnnoKey] != c.cfg.ManagedAnnoValue {
			return fmt.Errorf("%s %s/%s exists and is not managed by this controller; refusing to overwrite it", kind, c.cfg.Namespace, name)
		}
	case errors.IsNotFound(err):
	default:
		return fmt.Errorf("get %s %s: %w", kind, name, err)
	}
	data, err := json.Marshal(obj)
	if err != nil {
		return err
	}
	force := true
	if err := patch(ctx, data, metav1.PatchOptions{FieldManager: apply.FieldManager, Force: &force}); err != nil {
		return fmt.Errorf("apply %s %s: %w", kind, name, err)
	}
	logrus.Debugf("Applied %s %s via SSA", kind, name)
	return nil
}

func (c *Controller) gcCoreObjects(ctx context.Context, ds *desiredState) error {
	if c.cfg.ReadOnly {
		return nil
	}
	sel := metav1.ListOptions{LabelSelector: c.managedSelector()}
	managed := func(m metav1.Object) bool { return m.GetAnnotations()[c.cfg.ManagedAnnoKey] == c.cfg.ManagedAnnoValue }
	var errs []error

	// Slices first, then Services, so Traefik never sees a Service whose
	// slices still point at removed backends.
	wantSlices := map[string]struct{}{}
	for _, es := range ds.slices {
		wantSlices[es.Name] = struct{}{}
	}
	sliceCli := c.kube.DiscoveryV1().EndpointSlices(c.cfg.Namespace)
	if list, err := sliceCli.List(ctx, sel); err != nil {
		errs = append(errs, err)
	} else {
		for i := range list.Items {
			it := &list.Items[i]
			if _, keep := wantSlices[it.Name]; keep || !managed(it) {
				continue
			}
			if err := sliceCli.Delete(ctx, it.Name, metav1.DeleteOptions{}); err != nil && !errors.IsNotFound(err) {
				errs = append(errs, err)
			}
		}
	}

	wantSvcs := map[string]struct{}{}
	for _, s := range ds.services {
		wantSvcs[s.Name] = struct{}{}
	}
	svcCli := c.kube.CoreV1().Services(c.cfg.Namespace)
	if list, err := svcCli.List(ctx, sel); err != nil {
		errs = append(errs, err)
	} else {
		for i := range list.Items {
			it := &list.Items[i]
			if _, keep := wantSvcs[it.Name]; keep || !managed(it) {
				continue
			}
			if err := svcCli.Delete(ctx, it.Name, metav1.DeleteOptions{}); err != nil && !errors.IsNotFound(err) {
				errs = append(errs, err)
			}
		}
	}

	wantSecrets := map[string]struct{}{}
	for _, s := range ds.secrets {
		wantSecrets[s.Name] = struct{}{}
	}
	secretCli := c.kube.CoreV1().Secrets(c.cfg.Namespace)
	if list, err := secretCli.List(ctx, sel); err != nil {
		errs = append(errs, err)
	} else {
		for i := range list.Items {
			it := &list.Items[i]
			if _, keep := wantSecrets[it.Name]; keep || !managed(it) {
				continue
			}
			if err := secretCli.Delete(ctx, it.Name, metav1.DeleteOptions{}); err != nil && !errors.IsNotFound(err) {
				errs = append(errs, err)
			}
		}
	}
	return stderrors.Join(errs...)
}

// ---------------------------------------------------------------------------
// Grace-period deletion
// ---------------------------------------------------------------------------

func (c *Controller) enqueueGraceDeletion(ctx context.Context, req graceDeleteReq) {
	if c.graceDelQueue == nil {
		c.startGraceDeletionPool(ctx, c.cfg.GCWorkers)
	}
	q := c.graceDelQueue
	select {
	case q <- req:
		return
	case <-ctx.Done():
		return
	case <-time.After(250 * time.Millisecond):
		logrus.Warnf("GC: grace queue full (len=%d); dropping deletion of %s %s", len(q), req.kind, req.name)
	}
}

func (c *Controller) startGraceDeletionPool(ctx context.Context, workers int) {
	if workers <= 0 {
		workers = 1
	}
	c.graceDelOnce.Do(func() {
		qsz := c.cfg.GCGraceQueueSize
		if qsz <= 0 {
			qsz = 256
		}
		c.graceDelQueue = make(chan graceDeleteReq, qsz)
		for i := 0; i < workers; i++ {
			go c.graceDeletionWorker(ctx)
		}
	})
}

func (c *Controller) graceDeletionWorker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case req := <-c.graceDelQueue:
			c.handleGraceDelete(ctx, req)
		}
	}
}

func (c *Controller) handleGraceDelete(ctx context.Context, req graceDeleteReq) {
	if req.delay > 0 {
		t := time.NewTimer(req.delay)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
	}
	c.gcSem <- struct{}{}
	defer func() { <-c.gcSem }()
	gvr := c.gvrForKind(req.kind)
	if gvr.Resource == "" {
		logrus.Warnf("GC: unknown kind %q for grace deletion of %q; dropping", req.kind, req.name)
		return
	}
	resIfc := resources.AdaptResource(c.dyn.Resource(gvr).Namespace(c.cfg.Namespace))
	if err := resIfc.Delete(ctx, req.name, metav1.DeleteOptions{}); err != nil && !errors.IsNotFound(err) {
		logrus.Errorf("GC: grace delete failed for %s %s: %v", req.kind, req.name, err)
	}
}

func (c *Controller) gvrForKind(kind string) schema.GroupVersionResource {
	switch kind {
	case "Middleware":
		return c.gvrMiddleware
	case "IngressRoute":
		return c.gvrIngressRoute
	case "TraefikService":
		return c.gvrTraefikService
	case "ServersTransport":
		return c.gvrServersTransport
	case "IngressRouteTCP":
		return c.gvrIngressRouteTCP
	case "IngressRouteUDP":
		return c.gvrIngressRouteUDP
	case "ServersTransportTCP":
		return c.gvrServersTransportTCP
	case "TLSOption":
		return c.gvrTLSOption
	default:
		return schema.GroupVersionResource{}
	}
}

func (c *Controller) ApplyConfigForTest(ctx context.Context, cfg *traefikconfig.Config) error {
	return c.applyConfig(ctx, cfg)
}
