package controllers

import (
	"github.com/gogo/protobuf/types"
	"github.com/seldonio/seldon-core/operator/apis/machinelearning.seldon.io/v1"
	"github.com/seldonio/seldon-core/operator/constants"
	v1alpha32 "istio.io/api/networking/v1alpha3"
	"istio.io/client-go/pkg/apis/networking/v1alpha3"
	v12 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"strconv"
	"strings"
)

const (
	ENV_ISTIO_ENABLED                = "ISTIO_ENABLED"
	ENV_ISTIO_GATEWAY                = "ISTIO_GATEWAY"
	ENV_ISTIO_TLS_MODE               = "ISTIO_TLS_MODE"
	ANNOTATION_ISTIO_GATEWAY         = "seldon.io/istio-gateway"
	ANNOTATION_ISTIO_RETRIES         = "seldon.io/istio-retries"
	ANNOTATION_ISTIO_RETRIES_TIMEOUT = "seldon.io/istio-retries-timeout"
)

// Create istio virtual service and destination rule.
// Creates routes for each predictor with traffic weight split
func createIstioResources(mlDep *v1.SeldonDeployment,
	seldonId string,
	namespace string,
	ports []httpGrpcPorts,
	httpAllowed bool,
	grpcAllowed bool) ([]*v1alpha3.VirtualService, []*v1alpha3.DestinationRule, error) {

	istio_gateway := GetEnv(ENV_ISTIO_GATEWAY, "seldon-gateway")
	istioTLSMode := GetEnv(ENV_ISTIO_TLS_MODE, "")
	istioRetriesAnnotation := getAnnotation(mlDep, ANNOTATION_ISTIO_RETRIES, "")
	istioRetriesTimeoutAnnotation := getAnnotation(mlDep, ANNOTATION_ISTIO_RETRIES_TIMEOUT, "1")
	istioRetries := 0
	istioRetriesTimeout := 1
	var err error

	if istioRetriesAnnotation != "" {
		istioRetries, err = strconv.Atoi(istioRetriesAnnotation)
		if err != nil {
			return nil, nil, err
		}
		istioRetriesTimeout, err = strconv.Atoi(istioRetriesTimeoutAnnotation)
		if err != nil {
			return nil, nil, err
		}
	}
	httpVsvc := &v1alpha3.VirtualService{
		ObjectMeta: v12.ObjectMeta{
			Name:      seldonId + "-http",
			Namespace: namespace,
		},
		Spec: v1alpha32.VirtualService{
			Hosts:    []string{"*"},
			Gateways: []string{getAnnotation(mlDep, ANNOTATION_ISTIO_GATEWAY, istio_gateway)},
			Http: []*v1alpha32.HTTPRoute{
				{
					Match: []*v1alpha32.HTTPMatchRequest{
						{
							Uri: &v1alpha32.StringMatch{MatchType: &v1alpha32.StringMatch_Prefix{Prefix: "/seldon/" + namespace + "/" + mlDep.Name + "/"}},
						},
					},
					Rewrite: &v1alpha32.HTTPRewrite{Uri: "/"},
				},
			},
		},
	}

	grpcVsvc := &v1alpha3.VirtualService{
		ObjectMeta: v12.ObjectMeta{
			Name:      seldonId + "-grpc",
			Namespace: namespace,
		},
		Spec: v1alpha32.VirtualService{
			Hosts:    []string{"*"},
			Gateways: []string{getAnnotation(mlDep, ANNOTATION_ISTIO_GATEWAY, istio_gateway)},
			Http: []*v1alpha32.HTTPRoute{
				{
					Match: []*v1alpha32.HTTPMatchRequest{
						{
							Uri: &v1alpha32.StringMatch{MatchType: &v1alpha32.StringMatch_Regex{Regex: constants.GRPCRegExMatchIstio}},
							Headers: map[string]*v1alpha32.StringMatch{
								"seldon":    &v1alpha32.StringMatch{MatchType: &v1alpha32.StringMatch_Exact{Exact: mlDep.Name}},
								"namespace": &v1alpha32.StringMatch{MatchType: &v1alpha32.StringMatch_Exact{Exact: namespace}},
							},
						},
					},
				},
			},
		},
	}
	// Add retries
	if istioRetries > 0 {
		httpVsvc.Spec.Http[0].Retries = &v1alpha32.HTTPRetry{Attempts: int32(istioRetries), PerTryTimeout: &types.Duration{Seconds: int64(istioRetriesTimeout)}, RetryOn: "gateway-error,connect-failure,refused-stream"}
		grpcVsvc.Spec.Http[0].Retries = &v1alpha32.HTTPRetry{Attempts: int32(istioRetries), PerTryTimeout: &types.Duration{Seconds: int64(istioRetriesTimeout)}, RetryOn: "gateway-error,connect-failure,refused-stream"}
	}

	// shadows don't get destinations in the vs as a shadow is a mirror instead
	var shadows int = 0
	for i := 0; i < len(mlDep.Spec.Predictors); i++ {
		p := mlDep.Spec.Predictors[i]
		if p.Shadow == true {
			shadows += 1
		}
	}

	routesHttp := make([]*v1alpha32.HTTPRouteDestination, len(mlDep.Spec.Predictors)-shadows)
	routesGrpc := make([]*v1alpha32.HTTPRouteDestination, len(mlDep.Spec.Predictors)-shadows)

	// the shdadow/mirror entry does need a DestinationRule though
	drules := make([]*v1alpha3.DestinationRule, len(mlDep.Spec.Predictors))
	routesIdx := 0
	for i := 0; i < len(mlDep.Spec.Predictors); i++ {

		p := mlDep.Spec.Predictors[i]
		pSvcName := v1.GetPredictorKey(mlDep, &p)

		drule := &v1alpha3.DestinationRule{
			ObjectMeta: v12.ObjectMeta{
				Name:      pSvcName,
				Namespace: namespace,
			},
			Spec: v1alpha32.DestinationRule{
				Host: pSvcName,
				Subsets: []*v1alpha32.Subset{
					{
						Name: p.Name,
						Labels: map[string]string{
							"version": p.Labels["version"],
						},
					},
				},
			},
		}

		if istioTLSMode != "" {
			drule.Spec.TrafficPolicy = &v1alpha32.TrafficPolicy{
				Tls: &v1alpha32.TLSSettings{
					Mode: v1alpha32.TLSSettings_TLSmode(v1alpha32.TLSSettings_TLSmode_value[istioTLSMode]),
				},
			}
		}
		drules[i] = drule

		if p.Shadow == true {
			//if there's a shadow then add a mirror section to the VirtualService

			httpVsvc.Spec.Http[0].Mirror = &v1alpha32.Destination{
				Host:   pSvcName,
				Subset: p.Name,
				Port: &v1alpha32.PortSelector{
					Number: uint32(ports[i].httpPort),
				},
			}

			grpcVsvc.Spec.Http[0].Mirror = &v1alpha32.Destination{
				Host:   pSvcName,
				Subset: p.Name,
				Port: &v1alpha32.PortSelector{
					Number: uint32(ports[i].grpcPort),
				},
			}

			continue
		}

		//we split by adding different routes with their own Weights
		//so not by tag - different destinations (like https://istio.io/docs/tasks/traffic-management/traffic-shifting/) distinguished by host
		routesHttp[routesIdx] = &v1alpha32.HTTPRouteDestination{
			Destination: &v1alpha32.Destination{
				Host:   pSvcName,
				Subset: p.Name,
				Port: &v1alpha32.PortSelector{
					Number: uint32(ports[i].httpPort),
				},
			},
			Weight: p.Traffic,
		}
		routesGrpc[routesIdx] = &v1alpha32.HTTPRouteDestination{
			Destination: &v1alpha32.Destination{
				Host:   pSvcName,
				Subset: p.Name,
				Port: &v1alpha32.PortSelector{
					Number: uint32(ports[i].grpcPort),
				},
			},
			Weight: p.Traffic,
		}
		routesIdx += 1

	}
	httpVsvc.Spec.Http[0].Route = routesHttp
	grpcVsvc.Spec.Http[0].Route = routesGrpc

	if httpAllowed && grpcAllowed {
		vscs := make([]*v1alpha3.VirtualService, 2)
		vscs[0] = httpVsvc
		vscs[1] = grpcVsvc
		return vscs, drules, nil
	} else if httpAllowed {
		vscs := make([]*v1alpha3.VirtualService, 1)
		vscs[0] = httpVsvc
		return vscs, drules, nil
	} else {
		vscs := make([]*v1alpha3.VirtualService, 1)
		vscs[0] = grpcVsvc
		return vscs, drules, nil
	}
}

// Create istio virtual service and destination rule for explainer.
// Explainers need one each with no traffic-splitting
func createExplainerIstioResources(pSvcName string, p *v1.PredictorSpec,
	mlDep *v1.SeldonDeployment,
	seldonId string,
	namespace string,
	engine_http_port int,
	engine_grpc_port int) ([]*v1alpha3.VirtualService, []*v1alpha3.DestinationRule) {

	vsNameHttp := pSvcName + "-http"
	if len(vsNameHttp) > 63 {
		vsNameHttp = vsNameHttp[0:63]
		vsNameHttp = strings.TrimSuffix(vsNameHttp, "-")
	}

	vsNameGrpc := pSvcName + "-grpc"
	if len(vsNameGrpc) > 63 {
		vsNameGrpc = vsNameGrpc[0:63]
		vsNameGrpc = strings.TrimSuffix(vsNameGrpc, "-")
	}

	istio_gateway := GetEnv(ENV_ISTIO_GATEWAY, "seldon-gateway")
	httpVsvc := &v1alpha3.VirtualService{
		ObjectMeta: v12.ObjectMeta{
			Name:      vsNameHttp,
			Namespace: namespace,
		},
		Spec: v1alpha32.VirtualService{
			Hosts:    []string{"*"},
			Gateways: []string{getAnnotation(mlDep, ANNOTATION_ISTIO_GATEWAY, istio_gateway)},
			Http: []*v1alpha32.HTTPRoute{
				{
					Match: []*v1alpha32.HTTPMatchRequest{
						{
							Uri: &v1alpha32.StringMatch{MatchType: &v1alpha32.StringMatch_Prefix{Prefix: "/seldon/" + namespace + "/" + mlDep.GetName() + constants.ExplainerPathSuffix + "/" + p.Name + "/"}},
						},
					},
					Rewrite: &v1alpha32.HTTPRewrite{Uri: "/"},
				},
			},
		},
	}

	grpcVsvc := &v1alpha3.VirtualService{
		ObjectMeta: v12.ObjectMeta{
			Name:      vsNameGrpc,
			Namespace: namespace,
		},
		Spec: v1alpha32.VirtualService{
			Hosts:    []string{"*"},
			Gateways: []string{getAnnotation(mlDep, ANNOTATION_ISTIO_GATEWAY, istio_gateway)},
			Http: []*v1alpha32.HTTPRoute{
				{
					Match: []*v1alpha32.HTTPMatchRequest{
						{
							Uri: &v1alpha32.StringMatch{MatchType: &v1alpha32.StringMatch_Prefix{Prefix: "/seldon.protos.Seldon/"}},
							Headers: map[string]*v1alpha32.StringMatch{
								"seldon":    &v1alpha32.StringMatch{MatchType: &v1alpha32.StringMatch_Exact{Exact: mlDep.GetName()}},
								"namespace": &v1alpha32.StringMatch{MatchType: &v1alpha32.StringMatch_Exact{Exact: namespace}},
							},
						},
					},
				},
			},
		},
	}

	routesHttp := make([]*v1alpha32.HTTPRouteDestination, 1)
	routesGrpc := make([]*v1alpha32.HTTPRouteDestination, 1)
	drules := make([]*v1alpha3.DestinationRule, 1)

	drule := &v1alpha3.DestinationRule{
		ObjectMeta: v12.ObjectMeta{
			Name:      pSvcName,
			Namespace: namespace,
		},
		Spec: v1alpha32.DestinationRule{
			Host: pSvcName,
			Subsets: []*v1alpha32.Subset{
				{
					Name: p.Name,
					Labels: map[string]string{
						"version": p.Labels["version"],
					},
				},
			},
		},
	}

	routesHttp[0] = &v1alpha32.HTTPRouteDestination{
		Destination: &v1alpha32.Destination{
			Host:   pSvcName,
			Subset: p.Name,
			Port: &v1alpha32.PortSelector{
				Number: uint32(engine_http_port),
			},
		},
		Weight: int32(100),
	}
	routesGrpc[0] = &v1alpha32.HTTPRouteDestination{
		Destination: &v1alpha32.Destination{
			Host:   pSvcName,
			Subset: p.Name,
			Port: &v1alpha32.PortSelector{
				Number: uint32(engine_grpc_port),
			},
		},
		Weight: int32(100),
	}
	drules[0] = drule

	httpVsvc.Spec.Http[0].Route = routesHttp
	grpcVsvc.Spec.Http[0].Route = routesGrpc
	vscs := make([]*v1alpha3.VirtualService, 0, 2)
	// explainer may not expose REST and grpc (presumably engine ensures predictors do?)
	if engine_http_port > 0 {
		vscs = append(vscs, httpVsvc)
	}
	if engine_grpc_port > 0 {
		vscs = append(vscs, grpcVsvc)
	}

	return vscs, drules
}
