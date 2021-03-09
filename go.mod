module github.com/rpcxio/rpcx-etcd

go 1.16

require (
	github.com/rcrowley/go-metrics v0.0.0-20200313005456-10cdbea86bc0
	github.com/rpcxio/libkv v0.5.0
	github.com/smallnest/rpcx v0.0.0-20210302003640-3ac62d723635
	github.com/stretchr/testify v1.6.1
	go.etcd.io/etcd/client/v2 v2.305.0-alpha.0
	go.etcd.io/etcd/client/v3 v3.5.0-alpha.0
)

replace (
	github.com/coreos/bbolt => go.etcd.io/bbolt v1.3.3
	github.com/coreos/etcd => go.etcd.io/etcd/v3 v3.5.0-alpha.0
)
