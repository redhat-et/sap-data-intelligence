package managed_dh

import (
	"context"
	"fmt"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	csroute "github.com/openshift/client-go/route/clientset/versioned"
	routeinformers "github.com/openshift/client-go/route/informers/externalversions"

	sdiv1alpha1 "github.com/redhat-sap/sap-data-intelligence/operator/api/v1alpha1"
)

const (
	defaultSyncTime = time.Minute
	dhSyncTime      = time.Minute * 3
	routeSyncTime   = time.Minute * 10
	coreSyncTime    = time.Minute * 10
)

type managedDhReconciler struct {
	client         client.Client
	scheme         *runtime.Scheme
	namespacedName types.NamespacedName
	// Namespace where the managed DataHub resource lives.
	dhNamespace string
}

// Manages a single DataHub instance. It is controller by the SdiObserver resource. The controller updates its
// status. Is created dynamically by the parent controller.
type DhController interface {
	controller.Controller

	// The controller relies on the parent controller to get notified when the SdiObserver changes.
	ReconcileObs(*sdiv1alpha1.SdiObserver)
	Stop()
}

type dhController struct {
	controller.Controller

	mgr                manager.Manager
	unstartedFactories []informerFactory
	cancels            []context.CancelFunc
	// get notified from the parent controller when SdiObserver changes
	chanReconcileObs chan event.GenericEvent
	isStarted        bool
}

var _ DhController = &dhController{}

var _ reconcile.Reconciler = &managedDhReconciler{}

type informerFactory interface {
	Start(<-chan struct{})
}

// Managed in this context means that the SdiObserver CR is managed by the controller.
// The controller itself is not managed by the manager. It is created dynamically.
// Usually just for a single DH namespace where DataHub instance has been detected.
func NewManagedDhController(
	client client.Client,
	scheme *runtime.Scheme,
	nmName types.NamespacedName,
	dhNamespace string,
	mgr manager.Manager,
	options controller.Options,
) (*dhController, error) {
	r := &managedDhReconciler{
		client:         client,
		scheme:         scheme,
		namespacedName: nmName,
		dhNamespace:    dhNamespace,
	}
	ctrlName := strings.Join([]string{"ManagedObs", nmName.Namespace, nmName.Name}, "-")
	logger := logf.Log.WithName(ctrlName).WithValues(
		"reconciler group", DataHubResourceGroup,
		"reconciler kind", DataHubResourceName,
		"controller name", ctrlName,
		"managed DH namespace", dhNamespace)

	unmanagedCtrl, err := controller.NewUnmanaged(
		ctrlName,
		mgr,
		controller.Options{
			Reconciler: r,
			Log:        logger,
		})
	if err != nil {
		return nil, err
	}

	ctrl := &dhController{
		Controller:       unmanagedCtrl,
		mgr:              mgr,
		chanReconcileObs: make(chan event.GenericEvent),
	}

	obsContext, obsWatchCancel := context.WithCancel(context.Background())
	sc := source.Channel{Source: ctrl.chanReconcileObs}
	sc.InjectStopChannel(obsContext.Done())
	if err := ctrl.Watch(&sc, &handler.EnqueueRequestForObject{}); err != nil {
		obsWatchCancel()
		return nil, err
	}
	ctrl.cancels = append(ctrl.cancels, obsWatchCancel)

	err = ctrl.manageDhNamespace(obsContext, dhNamespace)
	if err != nil {
		obsWatchCancel()
		return nil, err
	}

	return ctrl, nil
}

func (c *dhController) startFactories(chCancel <-chan struct{}) {
	if !c.isStarted {
		// we don't want to miss the intial list of objects produced by each informer once started
		// let's make sure to start the factories once the controller and its queue are prepared
		return
	}
	for _, f := range c.unstartedFactories {
		f.Start(chCancel)
	}
	c.unstartedFactories = nil
}

func (c *dhController) ReconcileObs(obs *sdiv1alpha1.SdiObserver) {
	c.chanReconcileObs <- event.GenericEvent{Object: obs}
}

func (c *dhController) manageDhNamespace(ctx context.Context, dhNamespace string) error {
	cfg := c.mgr.GetConfig()
	kubeClient, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return err
	}
	routesClientSet := csroute.NewForConfigOrDie(cfg)
	dhDynClient := dynamic.NewForConfigOrDie(cfg)
	if err != nil {
		return err
	}

	// Create a factory object that can generate informers for resource types
	c.GetLogger().Info("(*dhController).manageDhNamespace: setting up watches for DH instance",
		"DH namespace", dhNamespace)

	// TODO: Watch just metadata
	factory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(
		dhDynClient,
		dhSyncTime,
		dhNamespace,
		nil)
	informer := factory.ForResource(MkDataHubGvr())
	c.unstartedFactories = append(c.unstartedFactories, factory)
	if err := c.Watch(
		&source.Informer{Informer: informer.Informer()},
		&handler.EnqueueRequestForObject{}); err != nil {
		return err
	}

	kubeInformerFactory := informers.NewSharedInformerFactoryWithOptions(
		kubeClient,
		coreSyncTime,
		informers.WithNamespace(dhNamespace))
	c.unstartedFactories = append(c.unstartedFactories, kubeInformerFactory)
	lsPred, err := predicate.LabelSelectorPredicate(metav1.LabelSelector{
		MatchLabels: map[string]string{
			"datahub.sap.com/app-component": "vsystem",
			"datahub.sap.com/app":           "vsystem",
		},
	})
	if err := c.Watch(
		&source.Informer{Informer: kubeInformerFactory.Core().V1().Services().Informer()},
		&handler.EnqueueRequestForObject{},
		lsPred); err != nil {
		return err
	}
	if err := c.Watch(
		&source.Informer{Informer: kubeInformerFactory.Core().V1().Secrets().Informer()},
		&handler.EnqueueRequestForObject{},
		predicate.NewPredicateFuncs(func(object client.Object) bool {
			return object.GetName() == vsystemCaBundleSecretName
		})); err != nil {
		return err
	}

	routeInformerFactory := routeinformers.NewSharedInformerFactoryWithOptions(
		routesClientSet,
		routeSyncTime,
		routeinformers.WithNamespace(dhNamespace))
	c.unstartedFactories = append(c.unstartedFactories, routeInformerFactory)
	if err := c.Watch(
		&source.Informer{Informer: routeInformerFactory.Route().V1().Routes().Informer()},
		&handler.EnqueueRequestForObject{}); err != nil {
		return err
	}

	c.startFactories(ctx.Done())
	return nil
}

func (c *dhController) Start(ctx context.Context) error {
	ctx_, cancel := context.WithCancel(context.Background())
	go func() {
		if err := c.Controller.Start(ctx_); err != nil {
			c.GetLogger().Error(err, "(*dhController).manageDhs: controller terminated")
		}
	}()

	c.isStarted = true
	c.startFactories(ctx_.Done())
	c.cancels = append(c.cancels, cancel)
	return nil
}

func (c *dhController) Stop() {
	close(c.chanReconcileObs)
	for _, c := range c.cancels {
		c()
	}
}

//+kubebuilder:rbac:groups=route.openshift.io;"",resources=routes,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=route.openshift.io;"",resources=routes/custom-host,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=route.openshift.io;"",resources=routes/status,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch
//+kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch
//+kubebuilder:rbac:groups=installers.datahub.sap.com,resources=datahubs,verbs=get;list;watch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.9.2/pkg/reconcile
func (r *managedDhReconciler) Reconcile(ctx context.Context, req reconcile.Request) (rs reconcile.Result, err error) {
	logger := log.FromContext(ctx)
	logger.Info(fmt.Sprintf("(*ManagedObsReconciler).Reconcile: running for %v", req))

	obs := &sdiv1alpha1.SdiObserver{}
	if err = r.client.Get(ctx, r.namespacedName, obs); err != nil && !errors.IsNotFound(err) {
		return
	}
	err = manageVsystemRoute(ctx, r.scheme, r.client, obs, &obs.Spec.VsystemRoute, r.dhNamespace)
	if err != nil {
		logger.Error(err, "failed to reconcile vsystem route")
	}
	return
}