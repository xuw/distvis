// distvis:name Bully 选举 · L06
// distvis:description 编号最大的存活节点当协调者：发现协调者失联的节点向更高编号发 Election，没人回 OK 就自立并广播 Coordinator；恢复的高编号节点会把位置「抢」回来。
package main

import lab "distvis/sdk/rpc"

func main() { lab.Main(configure) }

func configure(node *lab.Runtime) error {
	b, err := newBully(node)
	if err != nil {
		return err
	}
	if err := node.RegisterName("Bully", b); err != nil {
		return err
	}
	if err := node.RegisterName("Application", &Application{b}, lab.Application()); err != nil {
		return err
	}
	node.OnStart(b.run)
	return b.report()
}
