// distvis:name 多数投票互斥（完全去中心化）· L06
// distvis:description 每个节点都是持有一票的协调者：请求者向所有节点要票，得到多数票 m=n/2+1 才进入，否则退还选票、随机退避后重试；可观察选票冲突、饥饿与重启遗忘选票。
package main

import lab "distvis/sdk/rpc"

func main() { lab.Main(configure) }

func configure(node *lab.Runtime) error {
	m, err := newMajority(node)
	if err != nil {
		return err
	}
	if err := node.RegisterName("Vote", &Voter{m}); err != nil {
		return err
	}
	if err := node.RegisterName("Application", &Application{m}, lab.Application()); err != nil {
		return err
	}
	node.OnStart(m.run)
	return m.report()
}
