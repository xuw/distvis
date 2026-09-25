// distvis:name 两阶段提交 (2PC) · L07
// distvis:description node-1 为协调者，其余节点各持有一个账户；转账事务先 Prepare 投票，全部同意才提交，决定写入日志后再通知。可注入否决与崩溃，观察协调者故障时参与者的阻塞。
package main

import lab "distvis/sdk/rpc"

func main() { lab.Main(configure) }

func configure(node *lab.Runtime) error {
	if node.ID == node.Nodes[0] {
		c, err := newCoordinator(node)
		if err != nil {
			return err
		}
		if err := node.RegisterName("TM", c); err != nil {
			return err
		}
		if err := node.RegisterName("Application", &CoordinatorApp{c}, lab.Application()); err != nil {
			return err
		}
		node.OnStart(c.recover)
		return c.report()
	}
	p, err := newParticipant(node)
	if err != nil {
		return err
	}
	if err := node.RegisterName("RM", p); err != nil {
		return err
	}
	if err := node.RegisterName("Application", &ParticipantApp{p}, lab.Application()); err != nil {
		return err
	}
	node.OnStart(p.run)
	return p.report()
}
