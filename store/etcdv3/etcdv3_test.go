package etcdv3

import (
	"testing"
	"time"

	"github.com/rpcxio/libkv"
	"github.com/rpcxio/libkv/store"
	"github.com/rpcxio/libkv/testutils"
	estore "github.com/rpcxio/rpcx-etcd/store"
	"github.com/stretchr/testify/assert"
)

var client = "localhost:4001"

func makeEtcdClient(t *testing.T) store.Store {
	kv, err := New(
		[]string{client},
		&store.Config{
			ConnectionTimeout: 3 * time.Second,
			Username:          "test",
			Password:          "very-secure",
		},
	)
	if err != nil {
		t.Fatalf("cannot create store: %v", err)
	}

	return kv
}

func TestEtcdV3Register(t *testing.T) {
	Register()

	kv, err := libkv.NewStore(estore.ETCDV3, []string{client}, nil)
	assert.NoError(t, err)
	assert.NotNil(t, kv)

	if _, ok := kv.(*EtcdV3); !ok {
		t.Fatal("Error registering and initializing etcd")
	}
}

func TestEtcdV3Store(t *testing.T) {
	kv := makeEtcdClient(t)
	lockKV := makeEtcdClient(t)
	ttlKV := makeEtcdClient(t)

	defer testutils.RunCleanup(t, kv)

	testutils.RunTestCommon(t, kv)

	testutils.RunTestAtomic(t, kv)
	testutils.RunTestWatch(t, kv)
	testutils.RunTestLock(t, kv)
	testutils.RunTestLockTTL(t, kv, lockKV)
	testutils.RunTestLockWait(t, kv, lockKV)
	testutils.RunTestTTL(t, kv, ttlKV)
}
