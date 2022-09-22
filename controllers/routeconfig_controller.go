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

package controllers

import (
	"context"
	"strings"

	"github.com/go-logr/logr"
	contour_v1 "github.com/projectcontour/contour/apis/projectcontour/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/source"
	configv1 "tanzu.support.vmware.com/routeconfiger/api/v1"
)

type filterValidProxy struct {
	log logr.Logger
	predicate.Funcs
}

func (f filterValidProxy) Generic(e event.GenericEvent) bool {
	if e.Object == nil {
		f.log.Error(nil, "Generic event has no old metadata", "event", e)
		return false
	}

	return strings.HasSuffix(e.Object.GetNamespace(), "cf-space-")
}

// RouteConfigReconciler reconciles a RouteConfig object
type RouteConfigReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=config.tanzu.support.vmware.com,resources=routeconfigs,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=config.tanzu.support.vmware.com,resources=routeconfigs/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=config.tanzu.support.vmware.com,resources=routeconfigs/finalizers,verbs=update
//+kubebuilder:rbac:groups="projectcontour.io",resources=httpproxies,verbs=get;list;watch;update
//+kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the RouteConfig object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.12.2/pkg/reconcile
func (r *RouteConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	// TODO(user): your logic here
	//

	rc := &configv1.RouteConfig{}
	hp := &contour_v1.HTTPProxy{}
	err := r.Get(ctx, req.NamespacedName, rc)
	if apierrors.IsNotFound(err) { // object is not RouteConfig
		err = r.Get(ctx, req.NamespacedName, hp)
		if apierrors.IsNotFound(err) {
			log.Info("http proxy not found.  might have been deleted", "name", req.Name, "namespace", req.Namespace)
			return ctrl.Result{}, nil
		}
		if err != nil {
			log.Error(err, "could not identify http proxy object to reconcile", "name", req.Name, "namespace", req.Namespace)
			return ctrl.Result{}, err
		}
		// object is http proxy so reconcile it
		err = r.updateProxy(ctx, log, hp)
		if err != nil {
			log.Error(err, "Failed to update http proxy", "name", req.Name, "namespace", req.Namespace)
		}
		return ctrl.Result{}, err // end function here

	} else if err != nil { // something went wrong
		log.Error(err, "could not identify routeConfig object to reconcile", "name", req.Name, "namespace", req.Namespace)
		return ctrl.Result{}, err
	}

	// object is RouteConfig so go and process all routes
	//r.Get(ctx, types.NamespacedName{Name: "test", Namespace: "test"}, &hp)
	nsList := &corev1.NamespaceList{}
	proxyList := &contour_v1.HTTPProxyList{}
	proxyItems := make([]contour_v1.HTTPProxy, 0)
	if err := r.List(ctx, nsList); err != nil {
		return ctrl.Result{}, err
	}

	log.Info("printing namespaces found")
	for _, ns := range nsList.Items {
		log.Info("namespace", "name", ns.Name)

		if strings.HasPrefix("cf-space-", ns.Name) {

			// list only valid http routes
			listOps := &client.ListOptions{
				FieldSelector: fields.OneTermEqualSelector(".status.currentStatus", contour_v1.ValidConditionType),
				Namespace:     ns.Name,
			}
			err := r.List(ctx, proxyList, listOps)
			if err != nil {
				if client.IgnoreNotFound(err) != nil {
					return ctrl.Result{}, err
				}
			}
			if len(proxyList.Items) > 0 {
				proxyItems = append(proxyItems, proxyList.Items...)
			} else {
				log.Info("No routes found in namespace", "name", ns.Name)
			}
		}
	}

	log.Info("processing routes")
	for _, pr := range proxyItems {
		r.updateProxy(ctx, log, &pr)
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *RouteConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {

	// u := &unstructured.Unstructured{}
	// u.SetGroupVersionKind(schema.GroupVersionKind{
	// 	Kind:    "HTTPProxy",
	// 	Group:   "projectcontour.io",
	// 	Version: "v1",
	// })

	c, err := controller.New("containerset-controller", mgr,
		controller.Options{Reconciler: &RouteConfigReconciler{
			Client: mgr.GetClient(),
			Scheme: mgr.GetScheme(),
		}})
	if err != nil {
		return err
	}
	err = c.Watch(&source.Kind{Type: &contour_v1.HTTPProxy{}},
		&handler.EnqueueRequestForObject{},
		filterValidProxy{})
	if err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&configv1.RouteConfig{}).
		//Watches(&source.Kind{Type: &contour_v1.HTTPProxy{}}, &handler.EnqueueRequestForObject{}).
		Complete(r)
}

//err = c.Watch(&source.Kind{Type: u}, &handler.EnqueueRequestForObject{})

func (r *RouteConfigReconciler) updateProxy(ctx context.Context, log logr.Logger, pr *contour_v1.HTTPProxy) error {
	modified := false
	for i, spec := range pr.Spec.Routes {
		if !spec.PermitInsecure {
			pr.Spec.Routes[i].PermitInsecure = true
			log.Info("PermitInsecure updated on proxy", "name", pr.ObjectMeta.GetName())
			modified = true
		}
	}

	if modified {
		err := r.Update(ctx, pr) // set permit insecure
		if err != nil {
			return err
		}
	}
	return nil
}
