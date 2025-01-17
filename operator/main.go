/*
Copyright 2022.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"flag"
	"fmt"
	"os"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	routev1 "github.com/openshift/api/route/v1"

	sdiv1alpha1 "github.com/redhat-sap/sap-data-intelligence/operator/api/v1alpha1"
	"github.com/redhat-sap/sap-data-intelligence/operator/controllers/sdiobserver"
	//+kubebuilder:scaffold:imports
)

const (
	namespaceEnvVar     = "NAMESPACE"
	sdiNamespaceEnvVar  = "SDI_NAMESPACE"
	slcbNamespaceEnvVar = "SLCB_NAMESPACE"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(sdiv1alpha1.AddToScheme(scheme))
	utilruntime.Must(routev1.AddToScheme(scheme))
	//+kubebuilder:scaffold:scheme
}

func mkOverride(varName string) string {
	return fmt.Sprintf("Overrides %s environment variable.", varName)
}

func main() {
	var metricsAddr string
	var enableLeaderElection bool
	var probeAddr string
	var namespace, sdiNamespace, slcbNamespace string
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.StringVar(&namespace, "namespace", os.Getenv(namespaceEnvVar),
		"The k8s namespace where the operator runs. "+mkOverride(namespaceEnvVar))
	flag.StringVar(&sdiNamespace, "sdi-namespace", os.Getenv(sdiNamespaceEnvVar),
		"SAP DI namespace to monitor. Unless specified, all namespaces will be watched. "+
			mkOverride(sdiNamespaceEnvVar))
	flag.StringVar(&slcbNamespace, "slcb-namespace", os.Getenv(slcbNamespaceEnvVar),
		"K8s namespace where SAP Software Lifecycle Container Bridge runs."+
			" Unless specified, all namespaces will be watched. "+mkOverride(slcbNamespaceEnvVar))
	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	if len(namespace) == 0 {
		setupLog.Error(fmt.Errorf("missing namespace argument, please set at least the NAMESPACE variable"), "fatal")
		os.Exit(1)
	}

	var mgrCache cache.NewCacheFunc
	if len(sdiNamespace) == 0 || len(slcbNamespace) == 0 {
		mgrCache = cache.MultiNamespacedCacheBuilder([]string{namespace, sdiNamespace, slcbNamespace})
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		MetricsBindAddress:     metricsAddr,
		Port:                   9443,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "225c8f26.sap-cop.redhat.com",
		NewCache:               mgrCache,
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	r := sdiobserver.NewReconciler(mgr.GetClient(), mgr.GetScheme(), mgr)
	if err := r.SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "SDIObserver")
		os.Exit(1)
	}
	//+kubebuilder:scaffold:builder

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
