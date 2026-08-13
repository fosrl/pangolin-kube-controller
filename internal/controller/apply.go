package controller

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"fmt"
	"time"

	logrus "github.com/sirupsen/logrus"

	"golang.org/x/sync/errgroup"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
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

func (c *Controller) applyConfig(ctx context.Context, cfg *traefikconfig.Config) error {
	sanitizedCfg, err := sanitize.SanitizeTraefikConfig(cfg)
	if err != nil {
		return fmt.Errorf("sanitize config: %w", err)
	}
	if sanitizedCfg == nil {
		return nil
	}
	if !c.cfg.ReconcileParallel {
		return c.applyConfigSequential(ctx, sanitizedCfg)
	}
	return c.applyConfigParallel(ctx, sanitizedCfg)
}

func (c *Controller) applyConfigSequential(ctx context.Context, sanitizedCfg *traefikconfig.Config) error {
	phase := func(name string, fn func(context.Context) error) error {
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
	if err := phase("middlewares", func(ctx context.Context) error {
		return c.reconcileMiddlewares(ctx, c.dyn, c.gvrMiddleware, sanitizedCfg.HTTP.Middlewares)
	}); err != nil {
		return fmt.Errorf("middleware reconcile failed: %w", err)
	}
	if err := phase("routers", func(ctx context.Context) error {
		return c.reconcileRouters(ctx, c.dyn, c.gvrIngressRoute, sanitizedCfg.HTTP.Routers)
	}); err != nil {
		return fmt.Errorf("routers reconcile failed: %w", err)
	}
	if err := phase("serversTransports", func(ctx context.Context) error {
		return c.reconcileServersTransports(ctx, c.dyn, c.gvrServersTransport, sanitizedCfg.HTTP.ServersTransports)
	}); err != nil {
		return fmt.Errorf("serversTransports reconcile failed: %w", err)
	}
	if err := phase("services", func(ctx context.Context) error {
		return c.reconcileServices(ctx, c.dyn, c.gvrTraefikService, sanitizedCfg.HTTP.Services)
	}); err != nil {
		return fmt.Errorf("services reconcile failed: %w", err)
	}
	if err := phase("tcp", func(ctx context.Context) error { return c.reconcileTCP(ctx, sanitizedCfg) }); err != nil {
		return fmt.Errorf("tcp reconcile failed: %w", err)
	}
	if err := phase("udp", func(ctx context.Context) error { return c.reconcileUDP(ctx, sanitizedCfg) }); err != nil {
		return fmt.Errorf("udp reconcile failed: %w", err)
	}
	return nil
}

func (c *Controller) applyConfigParallel(ctx context.Context, sanitizedCfg *traefikconfig.Config) error {
	concurrency := c.cfg.ReconcileMax
	if concurrency < 1 {
		concurrency = 1
	}
	sem := make(chan struct{}, concurrency)
	g, gctx := errgroup.WithContext(ctx)
	wrapPhase := func(phase string, f func(context.Context) error) func() error {
		return func() error {
			sem <- struct{}{}
			defer func() { <-sem }()
			if c.collector != nil && c.collector.OTel != nil {
				c.collector.OTel.ActiveReconcileRoutines.Add(gctx, 1, metric.WithAttributes(metrics_otel.AttrPhase.String(phase)))
				defer c.collector.OTel.ActiveReconcileRoutines.Add(gctx, -1, metric.WithAttributes(metrics_otel.AttrPhase.String(phase)))
			}
			start := time.Now()
			err := f(gctx)
			if c.collector != nil && c.collector.OTel != nil {
				res := "success"
				if err != nil {
					res = "error"
				}
				c.collector.OTel.ReconcilePhaseDuration.Record(gctx, time.Since(start).Seconds(),
					metric.WithAttributes(
						metrics_otel.AttrPhase.String(phase),
						metrics_otel.AttrResult.String(res),
					),
				)
			}
			return err
		}
	}
	g.Go(wrapPhase("middlewares", func(ctx context.Context) error {
		return c.reconcileMiddlewares(ctx, c.dyn, c.gvrMiddleware, sanitizedCfg.HTTP.Middlewares)
	}))
	g.Go(wrapPhase("routers", func(ctx context.Context) error {
		return c.reconcileRouters(ctx, c.dyn, c.gvrIngressRoute, sanitizedCfg.HTTP.Routers)
	}))
	g.Go(wrapPhase("serversTransports", func(ctx context.Context) error {
		return c.reconcileServersTransports(ctx, c.dyn, c.gvrServersTransport, sanitizedCfg.HTTP.ServersTransports)
	}))
	g.Go(wrapPhase("services", func(ctx context.Context) error {
		return c.reconcileServices(ctx, c.dyn, c.gvrTraefikService, sanitizedCfg.HTTP.Services)
	}))
	g.Go(wrapPhase("tcp", func(ctx context.Context) error { return c.reconcileTCP(ctx, sanitizedCfg) }))
	g.Go(wrapPhase("udp", func(ctx context.Context) error { return c.reconcileUDP(ctx, sanitizedCfg) }))
	if err := g.Wait(); err != nil {
		return err
	}
	return nil
}

func (c *Controller) reconcileMiddlewares(ctx context.Context, dyn dynamic.Interface, gvr schema.GroupVersionResource, middlewares map[string]json.RawMessage) error {
	if err := c.applyDesiredObjects(ctx, dyn, gvr, middlewares); err != nil {
		return err
	}
	return c.gcStaleObjects(ctx, dyn, gvr, middlewares, "Middleware")
}

func (c *Controller) reconcileRouters(ctx context.Context, dyn dynamic.Interface, gvr schema.GroupVersionResource, routers map[string]json.RawMessage) error {
	resIfc := resources.AdaptResource(dyn.Resource(gvr).Namespace(c.cfg.Namespace))
	transformed := make(map[string]map[string]interface{}, len(routers))
	var transformErrors []error
	for name, raw := range routers {
		u, err := routing.TransformRouterToIngressRoute(name, raw, routing.RouterConfig{
			Namespace:         c.cfg.Namespace,
			ManagedLabelKey:   c.cfg.ManagedLabelKey,
			ManagedLabelValue: c.cfg.ManagedLabelValue,
			ManagedAnnoKey:    c.cfg.ManagedAnnoKey,
			ManagedAnnoValue:  c.cfg.ManagedAnnoValue,
			IngressClass:      c.cfg.IngressClass,
		})
		if err != nil {
			transformErrors = append(transformErrors, fmt.Errorf("router %s: %w", name, err))
			continue
		}
		transformed[name] = u
	}
	if len(transformErrors) > 0 {
		return stderrors.Join(transformErrors...)
	}
	for name, u := range transformed {
		if err := c.applyIngressRoute(ctx, resIfc, name, u); err != nil {
			return err
		}
	}
	err := c.gcStaleObjects(ctx, dyn, gvr, routers, "IngressRoute")
	if err != nil {
		return err
	}
	return nil
}

func (c *Controller) applyIngressRoute(ctx context.Context, resIfc resources.ResourceClient, name string, u map[string]interface{}) error {
	if c.cfg.ReadOnly {
		return nil
	}
	ops := &apply.IngressRouteOps{
		ResIfc:            resIfc,
		Namespace:         c.cfg.Namespace,
		ManagedLabelKey:   c.cfg.ManagedLabelKey,
		ManagedLabelValue: c.cfg.ManagedLabelValue,
		ManagedAnnoKey:    c.cfg.ManagedAnnoKey,
		ManagedAnnoValue:  c.cfg.ManagedAnnoValue,
		IngressClass:      c.cfg.IngressClass,
		ReadOnly:          c.cfg.ReadOnly,
	}
	return ops.Apply(ctx, name, u)
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
	metaCfg := apply.MetadataConfig{
		ManagedLabelKey:   c.cfg.ManagedLabelKey,
		ManagedLabelValue: c.cfg.ManagedLabelValue,
		ManagedAnnoKey:    c.cfg.ManagedAnnoKey,
		ManagedAnnoValue:  c.cfg.ManagedAnnoValue,
		IngressClass:      c.cfg.IngressClass,
	}
	for name := range objects {
		if err := ops.Apply(ctx, name, objects[name], metaCfg); err != nil {
			return err
		}
	}
	return nil
}

func (c *Controller) gcStaleObjects(ctx context.Context, dyn dynamic.Interface, gvr schema.GroupVersionResource, desiredObjs map[string]json.RawMessage, kind string) error {
	resIfc := resources.AdaptResource(dyn.Resource(gvr).Namespace(c.cfg.Namespace))
	existing, err := resIfc.List(ctx, metav1.ListOptions{LabelSelector: fmt.Sprintf("%s=%s", c.cfg.ManagedLabelKey, c.cfg.ManagedLabelValue)})
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
	}
	return nil
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

func (c *Controller) reconcileServices(ctx context.Context, dyn dynamic.Interface, gvr schema.GroupVersionResource, services map[string]json.RawMessage) error {
	processed, kubeServices, endpointSlices, err := protocol.ProcessHTTPServices(c.cfg, services, c.cfg.Namespace)
	if err != nil {
		return err
	}
	if err := c.applyProtocolServices(ctx, kubeServices); err != nil {
		return err
	}
	if err := c.applyProtocolSlices(ctx, endpointSlices); err != nil {
		return err
	}
	if err := c.applyDesiredObjects(ctx, dyn, gvr, processed); err != nil {
		return err
	}
	if err := c.gcStaleObjects(ctx, dyn, gvr, processed, "TraefikService"); err != nil {
		return err
	}
	if err := c.gcStaleCoreServices(ctx, desiredServiceNames(kubeServices), "http"); err != nil {
		return err
	}
	return c.gcStaleEndpointSlices(ctx, desiredEndpointSliceNames(endpointSlices), "http")
}

func (c *Controller) reconcileServersTransports(ctx context.Context, dyn dynamic.Interface, gvr schema.GroupVersionResource, transports map[string]json.RawMessage) error {
	if transports == nil {
		return nil
	}
	if err := c.applyDesiredObjects(ctx, dyn, gvr, transports); err != nil {
		return err
	}
	return c.gcStaleObjects(ctx, dyn, gvr, transports, "ServersTransport")
}

func (c *Controller) reconcileTCP(ctx context.Context, cfg *traefikconfig.Config) error {
	if cfg == nil || cfg.TCP == nil {
		return nil
	}
	if err := c.reconcileServersTransportTCP(ctx, cfg.TCP.ServersTransports); err != nil {
		return err
	}
	routes, svcs, slices, err := protocol.TransformTCP(cfg, c.cfg.Namespace)
	if err != nil {
		return err
	}
	if err := c.applyProtocolArtifacts(ctx, routes, svcs, slices, c.gvrIngressRouteTCP, "IngressRouteTCP"); err != nil {
		return err
	}
	desiredSvc := make(map[string]json.RawMessage, len(svcs))
	for _, svc := range svcs {
		desiredSvc[svc.Name] = json.RawMessage("{}")
	}
	desiredSlices := make(map[string]json.RawMessage, len(slices))
	for _, es := range slices {
		desiredSlices[es.Name] = json.RawMessage("{}")
	}
	desiredRoutes := make(map[string]json.RawMessage, len(routes))
	for _, r := range routes {
		meta, _ := r["metadata"].(map[string]interface{})
		if meta == nil {
			continue
		}
		name, _ := meta["name"].(string)
		if name != "" {
			desiredRoutes[name] = json.RawMessage("{}")
		}
	}
	if err := c.gcStaleObjects(ctx, c.dyn, c.gvrIngressRouteTCP, desiredRoutes, "IngressRouteTCP"); err != nil {
		return err
	}
	if err := c.gcStaleCoreServices(ctx, desiredSvc, "tcp"); err != nil {
		return err
	}
	if err := c.gcStaleEndpointSlices(ctx, desiredSlices, "tcp"); err != nil {
		return err
	}
	return nil
}

func (c *Controller) reconcileUDP(ctx context.Context, cfg *traefikconfig.Config) error {
	if cfg == nil || cfg.UDP == nil {
		return nil
	}
	routes, svcs, slices, err := protocol.TransformUDP(cfg, c.cfg.Namespace)
	if err != nil {
		return err
	}
	if err := c.applyProtocolArtifacts(ctx, routes, svcs, slices, c.gvrIngressRouteUDP, "IngressRouteUDP"); err != nil {
		return err
	}
	desiredSvc := make(map[string]json.RawMessage, len(svcs))
	for _, svc := range svcs {
		desiredSvc[svc.Name] = json.RawMessage("{}")
	}
	desiredSlices := make(map[string]json.RawMessage, len(slices))
	for _, es := range slices {
		desiredSlices[es.Name] = json.RawMessage("{}")
	}
	desiredRoutes := make(map[string]json.RawMessage, len(routes))
	for _, r := range routes {
		meta, _ := r["metadata"].(map[string]interface{})
		if meta == nil {
			continue
		}
		name, _ := meta["name"].(string)
		if name != "" {
			desiredRoutes[name] = json.RawMessage("{}")
		}
	}
	if err := c.gcStaleObjects(ctx, c.dyn, c.gvrIngressRouteUDP, desiredRoutes, "IngressRouteUDP"); err != nil {
		return err
	}
	if err := c.gcStaleCoreServices(ctx, desiredSvc, "udp"); err != nil {
		return err
	}
	if err := c.gcStaleEndpointSlices(ctx, desiredSlices, "udp"); err != nil {
		return err
	}
	return nil
}

func (c *Controller) reconcileServersTransportTCP(ctx context.Context, transports map[string]json.RawMessage) error {
	if transports == nil {
		return nil
	}
	st := make(map[string]json.RawMessage, len(transports))
	for name, raw := range transports {
		st[sanitize.SanitizeResourceName(name)] = raw
	}
	if err := c.applyDesiredObjects(ctx, c.dyn, c.gvrServersTransportTCP, st); err != nil {
		return err
	}
	return c.gcStaleObjects(ctx, c.dyn, c.gvrServersTransportTCP, st, "ServersTransportTCP")
}

func (c *Controller) applyProtocolArtifacts(ctx context.Context, routes []map[string]interface{}, svcs []*corev1.Service, slices []*discoveryv1.EndpointSlice, ingressRouteGVR schema.GroupVersionResource, kind string) error {
	if err := c.applyProtocolServices(ctx, svcs); err != nil {
		return err
	}
	if err := c.applyProtocolSlices(ctx, slices); err != nil {
		return err
	}
	return c.applyProtocolIngressRoutes(ctx, routes, ingressRouteGVR, kind)
}

func (c *Controller) applyProtocolServices(ctx context.Context, svcs []*corev1.Service) error {
	for _, svc := range svcs {
		if err := c.applyCoreService(ctx, svc); err != nil {
			return err
		}
	}
	return nil
}

func (c *Controller) applyProtocolSlices(ctx context.Context, slices []*discoveryv1.EndpointSlice) error {
	for _, es := range slices {
		if err := c.applyEndpointSlice(ctx, es); err != nil {
			return err
		}
	}
	return nil
}

func (c *Controller) applyProtocolIngressRoutes(ctx context.Context, routes []map[string]interface{}, ingressRouteGVR schema.GroupVersionResource, kind string) error {
	resIfc := resources.AdaptResource(c.dyn.Resource(ingressRouteGVR).Namespace(c.cfg.Namespace))
	ops := &apply.IngressRouteOps{
		ResIfc:            resIfc,
		Namespace:         c.cfg.Namespace,
		ManagedLabelKey:   c.cfg.ManagedLabelKey,
		ManagedLabelValue: c.cfg.ManagedLabelValue,
		ManagedAnnoKey:    c.cfg.ManagedAnnoKey,
		ManagedAnnoValue:  c.cfg.ManagedAnnoValue,
		IngressClass:      c.cfg.IngressClass,
		ReadOnly:          c.cfg.ReadOnly,
	}
	for _, m := range routes {
		if err := ops.ApplySingle(ctx, m, kind); err != nil {
			return err
		}
	}
	return nil
}

func (c *Controller) applyCoreService(ctx context.Context, svc *corev1.Service) error {
	c.ensureManagedServiceMeta(svc)
	if c.cfg.ReadOnly {
		return nil
	}
	cli := c.kube.CoreV1().Services(c.cfg.Namespace)
	existing, err := cli.Get(ctx, svc.Name, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			_, err = cli.Create(ctx, svc, metav1.CreateOptions{})
			return err
		}
		return err
	}
	existing.Spec = svc.Spec
	existing.Labels = svc.Labels
	existing.Annotations = svc.Annotations
	_, err = cli.Update(ctx, existing, metav1.UpdateOptions{})
	return err
}

func (c *Controller) ensureManagedServiceMeta(svc *corev1.Service) {
	if svc.Labels == nil {
		svc.Labels = map[string]string{}
	}
	svc.Labels[c.cfg.ManagedLabelKey] = c.cfg.ManagedLabelValue
	if c.cfg.TraefikInstanceLabelKey != "" && c.cfg.TraefikInstanceLabelValue != "" {
		svc.Labels[c.cfg.TraefikInstanceLabelKey] = c.cfg.TraefikInstanceLabelValue
	}
	if svc.Annotations == nil {
		svc.Annotations = map[string]string{}
	}
	svc.Annotations[c.cfg.ManagedAnnoKey] = c.cfg.ManagedAnnoValue
}

func (c *Controller) applyEndpointSlice(ctx context.Context, es *discoveryv1.EndpointSlice) error {
	c.ensureManagedEndpointSliceMeta(es)
	if c.cfg.ReadOnly {
		return nil
	}
	cli := c.kube.DiscoveryV1().EndpointSlices(c.cfg.Namespace)
	existing, err := cli.Get(ctx, es.Name, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			_, err = cli.Create(ctx, es, metav1.CreateOptions{})
			return err
		}
		return err
	}
	existing.Endpoints = es.Endpoints
	existing.Ports = es.Ports
	existing.Labels = es.Labels
	existing.Annotations = es.Annotations
	_, err = cli.Update(ctx, existing, metav1.UpdateOptions{})
	return err
}

func (c *Controller) ensureManagedEndpointSliceMeta(es *discoveryv1.EndpointSlice) {
	if es.Labels == nil {
		es.Labels = map[string]string{}
	}
	es.Labels[c.cfg.ManagedLabelKey] = c.cfg.ManagedLabelValue
	if c.cfg.TraefikInstanceLabelKey != "" && c.cfg.TraefikInstanceLabelValue != "" {
		es.Labels[c.cfg.TraefikInstanceLabelKey] = c.cfg.TraefikInstanceLabelValue
	}
	if es.Annotations == nil {
		es.Annotations = map[string]string{}
	}
	es.Annotations[c.cfg.ManagedAnnoKey] = c.cfg.ManagedAnnoValue
}

func desiredServiceNames(services []*corev1.Service) map[string]json.RawMessage {
	desired := make(map[string]json.RawMessage, len(services))
	for _, service := range services {
		desired[service.Name] = json.RawMessage("{}")
	}
	return desired
}

func desiredEndpointSliceNames(endpointSlices []*discoveryv1.EndpointSlice) map[string]json.RawMessage {
	desired := make(map[string]json.RawMessage, len(endpointSlices))
	for _, endpointSlice := range endpointSlices {
		desired[endpointSlice.Name] = json.RawMessage("{}")
	}
	return desired
}

func (c *Controller) gcStaleCoreServices(ctx context.Context, desired map[string]json.RawMessage, protocolName string) error {
	cli := c.kube.CoreV1().Services(c.cfg.Namespace)
	selector := fmt.Sprintf("%s=%s,%s=%s", c.cfg.ManagedLabelKey, c.cfg.ManagedLabelValue, protocol.ArtifactProtocolLabel, protocolName)
	list, err := cli.List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return err
	}
	for i := range list.Items {
		svc := &list.Items[i]
		name := svc.Name
		if _, keep := desired[name]; keep || svc.Annotations[c.cfg.ManagedAnnoKey] != c.cfg.ManagedAnnoValue || c.cfg.ReadOnly {
			continue
		}
		if err := cli.Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !errors.IsNotFound(err) {
			return err
		}
	}
	return nil
}

func (c *Controller) gcStaleEndpointSlices(ctx context.Context, desired map[string]json.RawMessage, protocolName string) error {
	cli := c.kube.DiscoveryV1().EndpointSlices(c.cfg.Namespace)
	selector := fmt.Sprintf("%s=%s,%s=%s", c.cfg.ManagedLabelKey, c.cfg.ManagedLabelValue, protocol.ArtifactProtocolLabel, protocolName)
	list, err := cli.List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return err
	}
	for i := range list.Items {
		es := &list.Items[i]
		name := es.Name
		if _, keep := desired[name]; keep || es.Annotations[c.cfg.ManagedAnnoKey] != c.cfg.ManagedAnnoValue || c.cfg.ReadOnly {
			continue
		}
		if err := cli.Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !errors.IsNotFound(err) {
			return err
		}
	}
	return nil
}

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
	default:
		return schema.GroupVersionResource{}
	}
}

func (c *Controller) ApplyConfigForTest(ctx context.Context, cfg *traefikconfig.Config) error {
	return c.applyConfig(ctx, cfg)
}
