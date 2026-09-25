// distvis:name NTP 时钟同步 · L05
// distvis:description 一轮交换得到 t0..t3：offset = ((t1−t0)+(t2−t3))/2，delay = (t3−t0)−(t2−t1)，服务器处理时间被扣除；每次 8 个样本取 delay 最小者；node-1 为 stratum 1，逐级向下同步。
package main

import lab "distvis/sdk/rpc"

func main() { lab.Main(configure) }

func configure(node *lab.Runtime) error {
	n, err := newNTP(node)
	if err != nil {
		return err
	}
	if err := node.RegisterName("NTP", n); err != nil {
		return err
	}
	if err := node.RegisterName("Application", &Application{n}, lab.Application()); err != nil {
		return err
	}
	node.OnStart(n.run)
	return n.report()
}
