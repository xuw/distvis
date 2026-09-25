package main

import (
	"fmt"
	"net/rpc"
	"time"
)

type PrepareArgs struct {
	Txn   string `json:"txn"`
	Delta int    `json:"delta"` // change to this participant's balance
}
type PrepareReply struct {
	Vote string `json:"vote"` // yes | no
	Why  string `json:"why,omitempty"`
}
type TxnArgs struct {
	Txn string `json:"txn"`
}
type StatusReply struct {
	State string `json:"state"` // committed | aborted | pending
}
type Empty struct{}

// call is client.Go with a deadline shorter than the 10 s RPC default; a late reply is ignored.
func call(client *rpc.Client, method string, args, reply any, timeout time.Duration) error {
	c := client.Go(method, args, reply, make(chan *rpc.Call, 1))
	select {
	case <-c.Done:
		return c.Error
	case <-time.After(timeout):
		return fmt.Errorf("%s: no reply within %v", method, timeout)
	}
}
