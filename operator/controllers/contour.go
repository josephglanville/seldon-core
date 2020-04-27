package controllers

import (
	contour "github.com/projectcontour/contour/apis/projectcontour/v1"
	v1 "github.com/seldonio/seldon-core/operator/apis/machinelearning.seldon.io/v1"
)

const (
	ENV_CONTOUR_ENABLED = "CONTOUR_ENABLED"
)

func createContourResources(mlDep *v1.SeldonDeployment,
	seldonId string,
	namespace string,
	ports []httpGrpcPorts,
	httpAllowed bool,
	grpcAllowed bool) ([]*contour.HTTPProxy, error) {
	return nil, nil
}

func createExplainerContourResources(pSvcName string, p *v1.PredictorSpec,
	mlDep *v1.SeldonDeployment,
	seldonId string,
	namespace string,
	engine_http_port int,
	engine_grpc_port int) []*contour.HTTPProxy {
	return nil
}
