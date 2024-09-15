package store

import "github.com/rpcxio/libkv/store"

const (
	// ETCD backend
	ETCD store.Backend = "etcd"
	// ETCDV3 backend
	ETCDV3 store.Backend = "etcdv3"
	// ETCDV3 Single backend
	ETCDV3_SINGLE store.Backend = "etcdv3_single"
)
