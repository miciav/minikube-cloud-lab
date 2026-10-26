package externalgrpc_test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"runtime"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	corev1 "k8s.io/api/core/v1"

	"github.com/miciav/minikube-cloud-lab/internal/cloud"
	"github.com/miciav/minikube-cloud-lab/internal/externalgrpc"
	"github.com/miciav/minikube-cloud-lab/internal/infra"
	"github.com/miciav/minikube-cloud-lab/internal/infra/fake"
	protos "github.com/miciav/minikube-cloud-lab/proto"
)

type blockedInfra struct {
	infra.Infra
	add            chan struct{}
	create, remove chan struct{}
}

func (f *blockedInfra) AddMachine(ctx context.Context, name string) (infra.Machine, error) {
	if f.add != nil {
		select {
		case <-f.add:
		case <-ctx.Done():
			return infra.Machine{}, ctx.Err()
		}
	}
	return f.Infra.AddMachine(ctx, name)
}

func (f *blockedInfra) CreateCluster(ctx context.Context, spec infra.ClusterSpec) error {
	if f.create != nil {
		select {
		case <-f.create:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return f.Infra.CreateCluster(ctx, spec)
}

func (f *blockedInfra) DeleteCluster(ctx context.Context, name string) error {
	if f.remove != nil {
		select {
		case <-f.remove:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return f.Infra.DeleteCluster(ctx, name)
}

func setup(t *testing.T, create bool) (protos.CloudProviderClient, *cloud.Service, *blockedInfra) {
	t.Helper()
	backend := &blockedInfra{Infra: fake.New()}
	svc, err := cloud.NewService(backend, t.TempDir(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	protos.RegisterCloudProviderServer(server, externalgrpc.New(svc, slog.Default()))
	go func() { _ = server.Serve(listener) }()
	conn, err := grpc.NewClient("passthrough:///buffer", grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(); server.Stop(); _ = listener.Close() })
	if create {
		op, err := svc.CreateCluster("demo", "lab-small", 1, 3, "student")
		if err != nil {
			t.Fatal(err)
		}
		wait(t, svc, op)
	}
	return protos.NewCloudProviderClient(conn), svc, backend
}

func wait(t *testing.T, s *cloud.Service, op cloud.Operation) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result, err := s.Wait(ctx, op.ID)
	if err != nil || result.Status != "DONE" {
		t.Fatalf("wait: %+v %v", result, err)
	}
}

func wantCode(t *testing.T, err error, want codes.Code) {
	t.Helper()
	if status.Code(err) != want {
		t.Fatalf("want %s, got %v", want, err)
	}
}

func TestReadRPCsAndTemplate(t *testing.T) {
	c, _, _ := setup(t, true)
	ctx := context.Background()
	groups, err := c.NodeGroups(ctx, &protos.NodeGroupsRequest{})
	if err != nil || len(groups.NodeGroups) != 1 || groups.NodeGroups[0].Id != "demo" || groups.NodeGroups[0].MinSize != 1 || groups.NodeGroups[0].MaxSize != 3 {
		t.Fatalf("groups: %v %v", groups, err)
	}
	target, err := c.NodeGroupTargetSize(ctx, &protos.NodeGroupTargetSizeRequest{Id: "demo"})
	if err != nil || target.TargetSize != 1 {
		t.Fatalf("size: %v %v", target, err)
	}
	nodes, err := c.NodeGroupNodes(ctx, &protos.NodeGroupNodesRequest{Id: "demo"})
	if err != nil || len(nodes.Instances) != 1 || nodes.Instances[0].Id != "demo" || nodes.Instances[0].Status.InstanceState != protos.InstanceStatus_instanceRunning {
		t.Fatalf("instances: %v %v", nodes, err)
	}
	for _, node := range []*protos.ExternalGrpcNode{{Name: "demo"}, {ProviderID: "minikube://demo"}} {
		group, err := c.NodeGroupForNode(ctx, &protos.NodeGroupForNodeRequest{Node: node})
		if err != nil || group.NodeGroup.Id != "demo" {
			t.Fatalf("node group: %v %v", group, err)
		}
	}
	empty, err := c.NodeGroupForNode(ctx, &protos.NodeGroupForNodeRequest{Node: &protos.ExternalGrpcNode{Name: "elsewhere"}})
	if err != nil || empty.NodeGroup.Id != "" {
		t.Fatalf("foreign node: %v %v", empty, err)
	}
	template, err := c.NodeGroupTemplateNodeInfo(ctx, &protos.NodeGroupTemplateNodeInfoRequest{Id: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	var node corev1.Node
	if err := node.Unmarshal(template.NodeBytes); err != nil {
		t.Fatal(err)
	}
	if node.Status.Capacity.Cpu().Value() != 2 || node.Status.Allocatable.Memory().Value() != 2147483648 || node.Status.Capacity.Memory().Value() != 2147483648 || node.Status.Capacity.Pods().Value() != 110 {
		t.Fatalf("template: %+v", node.Status)
	}
	for k, v := range map[string]string{"kubernetes.io/os": "linux", "kubernetes.io/arch": runtime.GOARCH, "node.kubernetes.io/instance-type": "lab-small", "topology.kubernetes.io/region": "lab-region-1", "topology.kubernetes.io/zone": "lab-region-1-a"} {
		if node.Labels[k] != v {
			t.Fatalf("template label %s: %s", k, node.Labels[k])
		}
	}
	if _, err := c.Refresh(ctx, &protos.RefreshRequest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Cleanup(ctx, &protos.CleanupRequest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GPULabel(ctx, &protos.GPULabelRequest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetAvailableGPUTypes(ctx, &protos.GetAvailableGPUTypesRequest{}); err != nil {
		t.Fatal(err)
	}
	_, err = c.NodeGroupDecreaseTargetSize(ctx, &protos.NodeGroupDecreaseTargetSizeRequest{Id: "demo", Delta: -1})
	wantCode(t, err, codes.Unimplemented)
}

func TestIncreaseSizeWithoutReadyCluster(t *testing.T) {
	c, s, backend := setup(t, false)
	ctx := context.Background()
	_, err := c.NodeGroupIncreaseSize(ctx, &protos.NodeGroupIncreaseSizeRequest{Id: "demo", Delta: 1})
	wantCode(t, err, codes.NotFound)
	f := backend.Infra.(*fake.Infra)
	backend.create = make(chan struct{})
	op, err := s.CreateCluster("demo", "lab-small", 1, 3, "student")
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.NodeGroupIncreaseSize(ctx, &protos.NodeGroupIncreaseSizeRequest{Id: "demo", Delta: 1})
	wantCode(t, err, codes.FailedPrecondition)
	close(backend.create)
	wait(t, s, op)
	for _, call := range f.Calls() {
		if call.Method == "AddMachine" {
			t.Fatal("created worker without ready cluster")
		}
	}
}

func TestIncreaseSizeIsAsynchronousAndShowsCreating(t *testing.T) {
	c, s, backend := setup(t, true)
	ctx := context.Background()
	release := make(chan struct{})
	backend.add = release
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	rpcCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if _, err := c.NodeGroupIncreaseSize(rpcCtx, &protos.NodeGroupIncreaseSizeRequest{Id: "demo", Delta: 1}); err != nil {
		t.Fatal(err)
	}
	target, err := c.NodeGroupTargetSize(ctx, &protos.NodeGroupTargetSizeRequest{Id: "demo"})
	if err != nil || target.TargetSize != 2 {
		t.Fatalf("reserved target: %v %v", target, err)
	}
	nodes, err := c.NodeGroupNodes(ctx, &protos.NodeGroupNodesRequest{Id: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	creating := 0
	for _, instance := range nodes.Instances {
		if instance.Status.InstanceState == protos.InstanceStatus_instanceCreating {
			creating++
		}
	}
	if creating != 1 {
		t.Fatalf("pending instances: %v", nodes)
	}
	ops := s.Operations()
	if ops[0].Requester != "cluster-autoscaler" || ops[0].Status != "RUNNING" {
		t.Fatalf("request registry: %+v", ops)
	}
	close(release)
	released = true
	wait(t, s, ops[0])
}

func TestOperationsRecordAutoscalerRequester(t *testing.T) {
	c, s, _ := setup(t, true)
	ctx := context.Background()
	if _, err := c.NodeGroupIncreaseSize(ctx, &protos.NodeGroupIncreaseSizeRequest{Id: "demo", Delta: 1}); err != nil {
		t.Fatal(err)
	}
	wait(t, s, s.Operations()[0])
	machines, _ := s.Machines("demo")
	worker := machines[1].Name
	if _, err := c.NodeGroupDeleteNodes(ctx, &protos.NodeGroupDeleteNodesRequest{Id: "demo", Nodes: []*protos.ExternalGrpcNode{{ProviderID: "minikube://" + worker}}}); err != nil {
		t.Fatal(err)
	}
	op := s.Operations()[0]
	wait(t, s, op)
	if op.Requester != "cluster-autoscaler" || op.Type != "DELETE_MACHINE" {
		t.Fatalf("delete registry: %+v", op)
	}
}

func TestRejectedRequests(t *testing.T) {
	c, _, _ := setup(t, true)
	ctx := context.Background()
	for _, delta := range []int32{0, -1} {
		_, err := c.NodeGroupIncreaseSize(ctx, &protos.NodeGroupIncreaseSizeRequest{Id: "demo", Delta: delta})
		wantCode(t, err, codes.InvalidArgument)
	}
	_, err := c.NodeGroupIncreaseSize(ctx, &protos.NodeGroupIncreaseSizeRequest{Id: "demo", Delta: 3})
	wantCode(t, err, codes.FailedPrecondition)
	for _, nodes := range [][]*protos.ExternalGrpcNode{nil, {{Name: "a"}, {Name: "b"}}} {
		_, err := c.NodeGroupDeleteNodes(ctx, &protos.NodeGroupDeleteNodesRequest{Id: "demo", Nodes: nodes})
		wantCode(t, err, codes.InvalidArgument)
	}
	_, err = c.NodeGroupDeleteNodes(ctx, &protos.NodeGroupDeleteNodesRequest{Id: "demo", Nodes: []*protos.ExternalGrpcNode{{Name: "demo"}}})
	wantCode(t, err, codes.FailedPrecondition)
	_, err = c.NodeGroupDeleteNodes(ctx, &protos.NodeGroupDeleteNodesRequest{Id: "demo", Nodes: []*protos.ExternalGrpcNode{{Name: "missing"}}})
	wantCode(t, err, codes.NotFound)
	_, err = c.NodeGroupNodes(ctx, &protos.NodeGroupNodesRequest{Id: "missing"})
	wantCode(t, err, codes.NotFound)
	_, err = c.NodeGroupTemplateNodeInfo(ctx, &protos.NodeGroupTemplateNodeInfoRequest{Id: "missing"})
	wantCode(t, err, codes.NotFound)
}

func TestIncreaseSizeWhileDeleting(t *testing.T) {
	c, s, backend := setup(t, true)
	backend.remove = make(chan struct{})
	op, err := s.DeleteCluster("demo", "student")
	if err != nil {
		t.Fatal(err)
	}
	_, rpcErr := c.NodeGroupIncreaseSize(context.Background(), &protos.NodeGroupIncreaseSizeRequest{Id: "demo", Delta: 1})
	close(backend.remove)
	wait(t, s, op)
	wantCode(t, rpcErr, codes.FailedPrecondition)
}
