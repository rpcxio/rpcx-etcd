package serverplugin

import (
	"context"
	"fmt"
	"log"
	"testing"
	"time"

	"github.com/rpcxio/rpcx-etcd/store/etcdv3singe"
	"github.com/smallnest/rpcx/server"
)

func TestEtcdV3Registry(t *testing.T) {
	s := server.NewServer()

	r, err := NewEtcdV3SingleRegisterPlugin([]string{"127.0.0.1:2379"}, "/rpcx_test",
		"tcp@127.0.0.1:8972", s.Started,
		nil,
	)
	if err != nil {
		log.Println(err)
		return
	}
	err = r.Start()
	if err != nil {
		log.Println(err)
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
		store, _ := etcdv3singe.New([]string{"127.0.0.1:2379"}, nil)
		stopCh := make(<-chan struct{})
		ch, _ := store.WatchTree("/rpcx_test", stopCh)
		for vs := range ch {
			for _, v := range vs {
				fmt.Println(v.Key)
			}
		}
	}()

	time.Sleep(10 * time.Second)

	if err := r.Stop(); err != nil {
		t.Fatal(err)
	}
}
