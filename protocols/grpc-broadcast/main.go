// distvis:name 数字广播 · gRPC
// distvis:description 从 protobuf 自动生成输入；任意节点提交数字，以标准 gRPC 客户端调用广播给其他节点。此示例演示通信，不是共识算法。
package main

import (
	lab "distvis/sdk/rpc"
	"google.golang.org/grpc"
	pb "lesson/broadcast/api"
)

func main() { lab.Main(configureBroadcast) }

func configureBroadcast(node *lab.Runtime) error {
	service, err := newService(node)
	if err != nil {
		return err
	}
	if err := node.GRPC(func(server grpc.ServiceRegistrar) {
		pb.RegisterApplicationServer(server, service)
		pb.RegisterReplicationServer(server, service)
	}, "lesson.Application"); err != nil {
		return err
	}
	return node.Report(map[string]any{"role": "ready"})
}
