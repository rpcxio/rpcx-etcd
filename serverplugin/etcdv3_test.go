package serverplugin

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/rpcxio/rpcx-etcd/store/etcdv3"
	"github.com/smallnest/rpcx/server"
)

func TestEtcdV3Registry(t *testing.T) {
	s := server.NewServer()

	r := &EtcdV3RegisterPlugin{
		ServiceAddress: "tcp@127.0.0.1:8972",
		EtcdServers:    []string{"127.0.0.1:2379"},
		BasePath:       "/rpcx_test",
		ServerStarted:  s.Started,
	}
	err := r.Start()
	if err != nil {
		return
	}
	s.Plugins.Add(r)

	s.RegisterFunction("AAAA", func(ctx context.Context, args *Args, reply *Reply) error {
		reply.C = args.A * args.B
		return nil
	}, "")

	s.RegisterName("Arith", new(Arith), "")
	go s.Serve("tcp", "127.0.0.1:8972")
	defer s.Close()

	if len(r.Services) != 2 {
		t.Fatal("failed to register services in etcd")
	}

	go func() {
		store, _ := etcdv3.New([]string{"127.0.0.1:2379"}, nil)
		stopCh := make(<-chan struct{})
		ch, _ := store.WatchTree("/rpcx_test", stopCh)
		for v := range ch {
			fmt.Println(v)
		}
	}()

	time.Sleep(10 * time.Minute)

	if err := r.Stop(); err != nil {
		t.Fatal(err)
	}
}
