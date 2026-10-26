// Package externalgrpc adapts Cluster Autoscaler requests to the cloud service.
package externalgrpc

import (
	"context"
	"errors"
	"log/slog"
	"runtime"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/miciav/minikube-cloud-lab/internal/cloud"
	protos "github.com/miciav/minikube-cloud-lab/proto"
)

type server struct {
	protos.UnimplementedCloudProviderServer
	svc    *cloud.Service
	logger *slog.Logger
}

// New exposes the same cloud service to Cluster Autoscaler.
func New(svc *cloud.Service, logger *slog.Logger) protos.CloudProviderServer {
	if logger == nil {
		logger = slog.Default()
	}
	return &server{svc: svc, logger: logger}
}

func rpcError(err error) error {
	if err == nil {
		return nil
	}
	code := codes.Internal
	switch {
	case errors.Is(err, cloud.ErrNotFound):
		code = codes.NotFound
	case errors.Is(err, cloud.ErrFailedPrecondition), errors.Is(err, cloud.ErrConflict):
		code = codes.FailedPrecondition
	case errors.Is(err, cloud.ErrInvalid):
		code = codes.InvalidArgument
	}
	return status.Error(code, err.Error())
}

func group(c cloud.Cluster) *protos.NodeGroup {
	return &protos.NodeGroup{Id: c.Name, MinSize: int32(c.MinMachines), MaxSize: int32(c.MaxMachines)}
} // #nosec G115 -- cloud validates both limits within int32.

func (s *server) ready(id string) (cloud.Cluster, error) {
	c, err := s.svc.Cluster(id)
	if err != nil {
		return cloud.Cluster{}, rpcError(err)
	}
	if c.Status != "RUNNING" {
		return c, status.Errorf(codes.FailedPrecondition, "cluster %s is %s", id, c.Status)
	}
	return c, nil
}

func (s *server) NodeGroups(context.Context, *protos.NodeGroupsRequest) (*protos.NodeGroupsResponse, error) {
	response := &protos.NodeGroupsResponse{}
	for _, c := range s.svc.Clusters() {
		if c.Status == "RUNNING" {
			response.NodeGroups = append(response.NodeGroups, group(c))
		}
	}
	return response, nil
}

func matches(n *protos.ExternalGrpcNode, m cloud.Machine) bool {
	return n != nil && (n.Name != "" && n.Name == m.Name || n.ProviderID != "" && (n.ProviderID == m.Name || strings.TrimPrefix(n.ProviderID, "minikube://") == m.Name))
}

func (s *server) NodeGroupForNode(_ context.Context, req *protos.NodeGroupForNodeRequest) (*protos.NodeGroupForNodeResponse, error) {
	response := &protos.NodeGroupForNodeResponse{NodeGroup: &protos.NodeGroup{}}
	for _, c := range s.svc.Clusters() {
		if c.Status != "RUNNING" {
			continue
		}
		machines, err := s.svc.Machines(c.Name)
		if err != nil {
			return nil, rpcError(err)
		}
		for _, m := range machines {
			if matches(req.GetNode(), m) {
				response.NodeGroup = group(c)
				return response, nil
			}
		}
	}
	return response, nil
}

func (s *server) NodeGroupTargetSize(_ context.Context, req *protos.NodeGroupTargetSizeRequest) (*protos.NodeGroupTargetSizeResponse, error) {
	if _, err := s.ready(req.GetId()); err != nil {
		return nil, err
	}
	machines, err := s.svc.Machines(req.GetId())
	if err != nil {
		return nil, rpcError(err)
	}
	var target int32
	for _, m := range machines {
		if m.Status != "DELETING" {
			target++
		}
	}
	return &protos.NodeGroupTargetSizeResponse{TargetSize: target}, nil
}

func (s *server) NodeGroupIncreaseSize(_ context.Context, req *protos.NodeGroupIncreaseSizeRequest) (*protos.NodeGroupIncreaseSizeResponse, error) {
	if _, err := s.ready(req.GetId()); err != nil {
		return nil, err
	}
	ops, err := s.svc.AddMachines(req.GetId(), int(req.GetDelta()), "cluster-autoscaler") //nolint:contextcheck // accepted work must survive RPC cancellation.
	if err != nil {
		return nil, rpcError(err)
	}
	for _, op := range ops {
		s.logger.Info("autoscaler requested machine", "operation", op.ID, "cluster", req.Id)
	}
	return &protos.NodeGroupIncreaseSizeResponse{}, nil
}

func (s *server) NodeGroupNodes(_ context.Context, req *protos.NodeGroupNodesRequest) (*protos.NodeGroupNodesResponse, error) {
	if _, err := s.ready(req.GetId()); err != nil {
		return nil, err
	}
	machines, err := s.svc.Machines(req.GetId())
	if err != nil {
		return nil, rpcError(err)
	}
	response := &protos.NodeGroupNodesResponse{}
	for _, m := range machines {
		state := protos.InstanceStatus_instanceCreating
		switch m.Status {
		case "RUNNING":
			state = protos.InstanceStatus_instanceRunning
		case "DELETING":
			state = protos.InstanceStatus_instanceDeleting
		}
		response.Instances = append(response.Instances, &protos.Instance{Id: m.Name, Status: &protos.InstanceStatus{InstanceState: state}})
	}
	return response, nil
}

func (s *server) NodeGroupDeleteNodes(_ context.Context, req *protos.NodeGroupDeleteNodesRequest) (*protos.NodeGroupDeleteNodesResponse, error) {
	if _, err := s.ready(req.GetId()); err != nil {
		return nil, err
	}
	if len(req.Nodes) != 1 || req.Nodes[0] == nil {
		return nil, status.Error(codes.InvalidArgument, "exactly one node must be deleted")
	}
	machines, err := s.svc.Machines(req.Id)
	if err != nil {
		return nil, rpcError(err)
	}
	for _, m := range machines {
		if matches(req.Nodes[0], m) {
			op, err := s.svc.DeleteMachine(req.Id, m.Name, "cluster-autoscaler") //nolint:contextcheck // accepted work must survive RPC cancellation.
			if err != nil {
				return nil, rpcError(err)
			}
			s.logger.Info("autoscaler requested deletion", "operation", op.ID, "machine", m.Name)
			return &protos.NodeGroupDeleteNodesResponse{}, nil
		}
	}
	return nil, status.Error(codes.NotFound, "machine is not observed")
}

func (s *server) NodeGroupTemplateNodeInfo(_ context.Context, req *protos.NodeGroupTemplateNodeInfoRequest) (*protos.NodeGroupTemplateNodeInfoResponse, error) {
	c, err := s.ready(req.GetId())
	if err != nil {
		return nil, err
	}
	var shape cloud.MachineType
	for _, kind := range cloud.MachineTypes() {
		if kind.Name == c.MachineType {
			shape = kind
			break
		}
	}
	labels := cloud.NodeLabels(c.MachineType)
	labels["kubernetes.io/os"] = "linux"
	labels["kubernetes.io/arch"] = runtime.GOARCH
	capacity := corev1.ResourceList{corev1.ResourceCPU: *resource.NewQuantity(int64(shape.CPUs), resource.DecimalSI), corev1.ResourceMemory: *resource.NewQuantity(int64(shape.MemoryMiB)*1024*1024, resource.BinarySI), corev1.ResourcePods: resource.MustParse("110")}
	node := corev1.Node{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Status: corev1.NodeStatus{Capacity: capacity, Allocatable: capacity.DeepCopy()}}
	data, err := node.Marshal()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "marshal node template: %v", err)
	}
	return &protos.NodeGroupTemplateNodeInfoResponse{NodeBytes: data}, nil
}

func (s *server) NodeGroupDecreaseTargetSize(context.Context, *protos.NodeGroupDecreaseTargetSizeRequest) (*protos.NodeGroupDecreaseTargetSizeResponse, error) {
	return nil, status.Error(codes.Unimplemented, "pending capacity cancellation is unavailable")
}

func (s *server) Refresh(context.Context, *protos.RefreshRequest) (*protos.RefreshResponse, error) {
	return &protos.RefreshResponse{}, nil
}

func (s *server) Cleanup(context.Context, *protos.CleanupRequest) (*protos.CleanupResponse, error) {
	return &protos.CleanupResponse{}, nil
}

func (s *server) GPULabel(context.Context, *protos.GPULabelRequest) (*protos.GPULabelResponse, error) {
	return &protos.GPULabelResponse{}, nil
}

func (s *server) GetAvailableGPUTypes(context.Context, *protos.GetAvailableGPUTypesRequest) (*protos.GetAvailableGPUTypesResponse, error) {
	return &protos.GetAvailableGPUTypesResponse{}, nil
}
