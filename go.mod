module github.com/rpcxio/rpcx-etcd

go 1.15

require (
	github.com/rcrowley/go-metrics v0.0.0-20200313005456-10cdbea86bc0
	github.com/rpcxio/libkv v0.5.0
	github.com/smallnest/rpcx v0.0.0-20201229103109-20b35e5375d1
	github.com/stretchr/testify v1.6.1
	go.etcd.io/etcd v3.4.14+incompatible
)

replace (
	github.com/coreos/bbolt => go.etcd.io/bbolt v1.3.3
	google.golang.org/grpc => google.golang.org/grpc v1.29.1
)
