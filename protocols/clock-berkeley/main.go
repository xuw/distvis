// distvis:name Berkeley 时钟同步 · L05
// distvis:description 没有 UTC 权威：master（时间守护进程）轮询所有节点的时钟差，剔除离群值后求平均，再给每个节点发送各自的相对调整量；客户端默认平滑调整（slew），也可直接跳变（step）。
package main

import lab "distvis/sdk/rpc"

func main() { lab.Main(configure) }

func configure(node *lab.Runtime) error {
	b, err := newBerkeley(node)
	if err != nil {
		return err
	}
	if err := node.RegisterName("Berkeley", b); err != nil {
		return err
	}
	if err := node.RegisterName("Application", &Application{b}, lab.Application()); err != nil {
		return err
	}
	node.OnStart(b.run)
	return b.report()
}
