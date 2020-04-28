package controllers

import (
	"context"
	"fmt"
	"github.com/go-logr/logr"
	contour "github.com/projectcontour/contour/apis/projectcontour/v1"
	v1 "github.com/seldonio/seldon-core/operator/apis/machinelearning.seldon.io/v1"
	"github.com/seldonio/seldon-core/operator/constants"
	v13 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	v12 "k8s.io/apimachinery/pkg/apis/meta/v1"
	types2 "k8s.io/apimachinery/pkg/types"
	"knative.dev/pkg/kmp"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	ANNOTATION_CONTOUR_PROTOCOL_H2 = "projectcontour.io/upstream-protocol.h2"

	ENV_CONTOUR_ENABLED = "CONTOUR_ENABLED"
	ENV_CONTOUR_BASE_HOST = "CONTOUR_BASE_HOST"
)

func createContourResources(mlDep *v1.SeldonDeployment,
	seldonId string,
	namespace string,
	ports []httpGrpcPorts,
	httpAllowed bool,
	grpcAllowed bool) ([]*contour.HTTPProxy, error) {


	contourBaseHost := GetEnv(ENV_CONTOUR_BASE_HOST, ".")

	httpProxy := &contour.HTTPProxy{
		ObjectMeta: v12.ObjectMeta{
			Name:      seldonId + "-http",
			Namespace: namespace,
		},
		Spec: contour.HTTPProxySpec{
			VirtualHost: &contour.VirtualHost{
				Fqdn: fmt.Sprintf("%s.%s", mlDep.Name, contourBaseHost), // TODO(jpg) is this good enough?
				TLS: nil, // TODO(jpg) look into TLS stuffs
			},
			// TODO(jpg): Need to build this up from the list of predictors
			Routes: []contour.Route{
				{

				},
			},
		},
	}

	httpProxies := []*contour.HTTPProxy{httpProxy}

	return httpProxies, nil
}

// TODO(jpg): Look at entire thing to understand wtf to do here
// Can most likely create a shared function called by this and the predictor version
func createExplainerContourResources(pSvcName string, p *v1.PredictorSpec,
	mlDep *v1.SeldonDeployment,
	seldonId string,
	namespace string,
	engineHttpPort int,
	engineGrpcPort int) []*contour.HTTPProxy {

	contourBaseHost := GetEnv(ENV_CONTOUR_BASE_HOST, ".")

	httpProxy := &contour.HTTPProxy{
		ObjectMeta: v12.ObjectMeta{
			Name:      seldonId + "-http",
			Namespace: namespace,
		},
		Spec: contour.HTTPProxySpec{
			VirtualHost: &contour.VirtualHost{
				Fqdn: fmt.Sprintf("%s.%s", mlDep.Name, contourBaseHost), // TODO(jpg) is this good enough?
				TLS: nil, // TODO(jpg) look into TLS stuffs
			},
			// TODO(jpg): Need to build this up from the list of predictors
			Routes: []contour.Route{

			},
		},
	}

	httpProxies := []*contour.HTTPProxy{httpProxy}

	return httpProxies
}

func (r *SeldonDeploymentReconciler) createContourServices(components *components, instance *v1.SeldonDeployment, log logr.Logger) (bool, error) {
	ready := true
	for _, httpProxy := range components.httpProxies {
		if err := controllerutil.SetControllerReference(instance, httpProxy, r.Scheme); err != nil {
			return ready, err
		}
		found := &contour.HTTPProxy{}
		err := r.Get(context.TODO(), types2.NamespacedName{Name: httpProxy.Name, Namespace: httpProxy.Namespace}, found)
		if err != nil && errors.IsNotFound(err) {
			ready = false
			log.Info("Creating HTTPProxy", "namespace", httpProxy.Namespace, "name", httpProxy.Name)
			err = r.Create(context.TODO(), httpProxy)
			if err != nil {
				return ready, err
			}
			r.Recorder.Eventf(instance, v13.EventTypeNormal, constants.EventsCreateHTTPProxy, "Created HTTPProxy %q", httpProxy.GetName())
		} else if err != nil {
			return ready, err
		} else {
			// Update the found object and write the result back if there are any changes
			if !equality.Semantic.DeepEqual(httpProxy.Spec, found.Spec) {
				desiredSvc := found.DeepCopy()
				found.Spec = httpProxy.Spec
				log.Info("Updating HTTPProxy", "namespace", httpProxy.Namespace, "name", httpProxy.Name)
				err = r.Update(context.TODO(), found)
				if err != nil {
					return ready, err
				}

				// Check if what came back from server modulo the defaults applied by k8s is the same or not
				if !equality.Semantic.DeepEqual(desiredSvc.Spec, found.Spec) {
					ready = false
					r.Recorder.Eventf(instance, v13.EventTypeNormal, constants.EventsUpdateHTTPProxy, "Updated HTTPProxy %q", httpProxy.GetName())
					//For debugging we will show the difference
					diff, err := kmp.SafeDiff(desiredSvc.Spec, found.Spec)
					if err != nil {
						log.Error(err, "Failed to diff")
					} else {
						log.Info(fmt.Sprintf("Difference in HTTPProxy: %v", diff))
					}
				} else {
					log.Info("The HTTPProxy objects are the same - API server defaults ignored")
				}
			} else {
				log.Info("Found identical HTTPProxy", "namespace", found.Namespace, "name", found.Name)
			}
		}
	}

	// Cleanup unused HTTPProxy objects.
	// This should usually only happen on Operator upgrades where there is a breaking change to the names of the HTTPProxies created
	if len(components.httpProxies) > 0 && ready {
		cleaner := ResourceCleaner{instance: instance, client: r, httpProxies: components.httpProxies, logger: r.Log}
		deleted, err := cleaner.cleanUnusedHTTPProxies()
		if err != nil {
			return ready, err
		}
		for _, httpProxy := range deleted {
			r.Recorder.Eventf(instance, v13.EventTypeNormal, constants.EventsDeleteHTTPProxy, "Delete HTTPProxy %q", httpProxy.GetName())
		}
	}

	return true, nil
}