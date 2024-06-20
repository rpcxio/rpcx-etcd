package etcdv3

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/rpcxio/libkv"
	"github.com/rpcxio/libkv/store"
	estore "github.com/rpcxio/rpcx-etcd/store"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/client/v3/concurrency"
)

func init() {
	libkv.AddStore(estore.ETCDV3, New)
}

const defaultTTL = 30

// EtcdConfigAutoSyncInterval give a choice to those etcd cluster could not auto sync
// such I deploy clusters in docker they will dial tcp: lookup etcd1: Try again, can just set this to zero
var EtcdConfigAutoSyncInterval = time.Minute * 5

func init() {
	libkv.AddStore(estore.ETCDV3, New)
}

// EtcdV3 is the receiver type for the Store interface
type EtcdV3 struct {
	timeout        time.Duration
	client         *clientv3.Client
	leaseID        clientv3.LeaseID
	cfg            clientv3.Config
	done           chan struct{}
	startKeepAlive chan struct{}

	AllowKeyNotFound bool

	mu  sync.RWMutex
	ttl int64
}

// New creates a new Etcd client given a list
// of endpoints and an optional tls config
func New(addrs []string, options *store.Config) (store.Store, error) {
	s := &EtcdV3{
		done:           make(chan struct{}),
		startKeepAlive: make(chan struct{}),
		ttl:            defaultTTL,
	}

	cfg := clientv3.Config{
		Endpoints: addrs,
	}

	if options != nil {
		s.timeout = options.ConnectionTimeout
		cfg.DialTimeout = options.ConnectionTimeout
		cfg.DialKeepAliveTimeout = options.ConnectionTimeout
		cfg.TLS = options.TLS
		cfg.Username = options.Username
		cfg.Password = options.Password

		cfg.AutoSyncInterval = EtcdConfigAutoSyncInterval
	}
	if s.timeout == 0 {
		s.timeout = 10 * time.Second
	}
	s.cfg = cfg
	err := s.init(true)
	if err != nil {
		return nil, err
	}

	go s.keepAlive()

	return s, nil
}

func (s *EtcdV3) keepAlive() {
	var ch <-chan *clientv3.LeaseKeepAliveResponse
	var err error
rekeepalive:
	for {
		if s.leaseID != 0 {
			ch, err = s.client.KeepAlive(context.Background(), s.leaseID)
		}
		if err == nil {
			break
		}
		time.Sleep(time.Second)
	}

	// KeepAlive success
	for {
		select {
		case <-s.done:
			return
		case resp := <-ch: // KeepAlive channel is closed
			if resp == nil { // connection is closed
				s.client.Close()
				for {
					select {
					case <-s.done:
						return
					default:
						err = s.init(false)
						if err != nil {
							time.Sleep(time.Second)
							continue
						}

						s.mu.RLock()
						err = s.grant(s.ttl)
						s.mu.RUnlock()
						if err != nil {
							s.client.Close()
							time.Sleep(time.Second)
							continue
						}
						goto rekeepalive
					}
				}

			}
		}
	}

}

func (s *EtcdV3) init(grant bool) error {
	cli, err := clientv3.New(s.cfg)
	if err != nil {
		return err
	}

	s.client = cli

	if grant {

		s.mu.RLock()
		err = s.grant(s.ttl)
		s.mu.RUnlock()
	}

	return err
}

func (s *EtcdV3) normalize(key string) string {
	key = store.Normalize(key)
	return strings.TrimPrefix(key, "/")
}

// grant a lease.
func (s *EtcdV3) grant(ttl int64) error {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	resp, err := s.client.Grant(ctx, ttl)
	cancel()
	if err == nil {
		s.leaseID = resp.ID
	}
	return err
}

// Put a value at the specified key
func (s *EtcdV3) Put(key string, value []byte, options *store.WriteOptions) error {
	var ttl int64
	if options != nil {
		ttl = int64(options.TTL.Seconds())
	}
	if ttl == 0 {
		ttl = defaultTTL
	}
	s.mu.Lock()
	s.ttl = ttl
	s.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	_, err := s.client.Put(ctx, key, string(value), clientv3.WithLease(s.leaseID))
	cancel()

	return err
}

// Get a value given its key
func (s *EtcdV3) Get(key string) (*store.KVPair, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	resp, err := s.client.Get(ctx, key)
	cancel()
	if err != nil {
		return nil, err
	}
	if len(resp.Kvs) == 0 {
		return nil, store.ErrKeyNotFound
	}

	pair := &store.KVPair{
		Key:       key,
		Value:     resp.Kvs[0].Value,
		LastIndex: uint64(resp.Kvs[0].Version),
	}

	return pair, nil
}

// Delete the value at the specified key
func (s *EtcdV3) Delete(key string) error {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	_, err := s.client.Delete(ctx, key)
	cancel()

	return err
}

// Exists verifies if a Key exists in the store
func (s *EtcdV3) Exists(key string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	resp, err := s.client.Get(ctx, key)
	cancel()
	if err != nil {
		return false, err
	}

	return len(resp.Kvs) != 0, nil
}

// Watch for changes on a key.
func (s *EtcdV3) Watch(key string, stopCh <-chan struct{}) (<-chan *store.KVPair, error) {
	watchCh := make(chan *store.KVPair)

	go func() {
		defer close(watchCh)

		// put the current value into returned channel before watch
		pair, err := s.Get(key)
		if err != nil {
			return
		}
		watchCh <- pair

		rch := s.client.Watch(context.Background(), key)
		for {
			select {
			case <-s.done:
				return
			case wresp, ok := <-rch:
				if !ok || wresp.Canceled { // watch is canceled
					return
				}
				for _, event := range wresp.Events {
					watchCh <- &store.KVPair{
						Key:       string(event.Kv.Key),
						Value:     event.Kv.Value,
						LastIndex: uint64(event.Kv.Version),
					}
				}
			}
		}
	}()

	return watchCh, nil
}

// WatchTree watches for changes on child nodes under a given directory
func (s *EtcdV3) WatchTree(directory string, stopCh <-chan struct{}) (<-chan []*store.KVPair, error) {
	watchCh := make(chan []*store.KVPair)
	list, err := s.List(directory)
	if err != nil {
		if !s.AllowKeyNotFound || err != store.ErrKeyNotFound {
			return watchCh, err
		}
	}
	go func() {
		defer close(watchCh)

		watchCh <- list

		rch := s.client.Watch(context.Background(), directory, clientv3.WithPrefix())
		for {
			select {
			case <-s.done:
				return
			case resp := <-rch:
				if resp.Canceled { // watch is canceled
					return
				}

				list, err := s.List(directory)
				if err != nil {
					if !s.AllowKeyNotFound || err != store.ErrKeyNotFound {
						continue
					}
				}
				watchCh <- list
			}
		}
	}()

	return watchCh, nil
}

type etcdLock struct {
	session *concurrency.Session
	mutex   *concurrency.Mutex
}

// NewLock creates a lock for a given key.
// The returned Locker is not held and must be acquired
// with `.Lock`. The Value is optional.
func (s *EtcdV3) NewLock(key string, options *store.LockOptions) (store.Locker, error) {
	return nil, errors.New("not implemented")
}

// List the content of a given prefix
func (s *EtcdV3) List(directory string) ([]*store.KVPair, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()

	resp, err := s.client.Get(ctx, directory, clientv3.WithPrefix())
	if err != nil {
		return nil, err
	}

	kvpairs := make([]*store.KVPair, 0, len(resp.Kvs))

	if len(resp.Kvs) == 0 {
		return nil, store.ErrKeyNotFound
	}

	for _, kv := range resp.Kvs {
		pair := &store.KVPair{
			Key:       string(kv.Key),
			Value:     kv.Value,
			LastIndex: uint64(kv.Version),
		}
		kvpairs = append(kvpairs, pair)
	}

	return kvpairs, nil
}

// DeleteTree deletes a range of keys under a given directory
func (s *EtcdV3) DeleteTree(directory string) error {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	_, err := s.client.Delete(ctx, directory, clientv3.WithPrefix())
	cancel()

	return err
}

// AtomicPut CAS operation on a single value.
// Pass previous = nil to create a new key.
func (s *EtcdV3) AtomicPut(key string, value []byte, previous *store.KVPair, options *store.WriteOptions) (bool, *store.KVPair, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()

	var revision int64
	var presp *clientv3.PutResponse
	var txresp *clientv3.TxnResponse
	var err error
	if previous == nil {
		if exist, err := s.Exists(key); err != nil { // not atomicput
			return false, nil, err
		} else if !exist {
			presp, err = s.client.Put(ctx, key, string(value))
			if err != nil {
				return false, nil, err
			}
			if presp != nil {
				revision = presp.Header.GetRevision()
			}
		} else {
			return false, nil, store.ErrKeyExists
		}
	} else {

		cmps := []clientv3.Cmp{
			clientv3.Compare(clientv3.Value(key), "=", string(previous.Value)),
			clientv3.Compare(clientv3.Version(key), "=", int64(previous.LastIndex)),
		}
		txresp, err = s.client.Txn(ctx).If(cmps...).
			Then(clientv3.OpPut(key, string(value))).
			Commit()
		if txresp != nil {
			if txresp.Succeeded {
				revision = txresp.Header.GetRevision()
			} else {
				err = errors.New("key's version not matched!")
			}
		}
	}

	if err != nil {
		return false, nil, err
	}

	pair := &store.KVPair{
		Key:       key,
		Value:     value,
		LastIndex: uint64(revision),
	}

	return true, pair, nil
}

// AtomicDelete cas deletes a single value
func (s *EtcdV3) AtomicDelete(key string, previous *store.KVPair) (bool, error) {
	deleted := false
	var err error
	var txresp *clientv3.TxnResponse
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()

	if previous == nil {
		return false, errors.New("key's version info is needed!")
	} else {
		cmps := []clientv3.Cmp{
			clientv3.Compare(clientv3.Value(key), "=", string(previous.Value)),
			clientv3.Compare(clientv3.Version(key), "=", int64(previous.LastIndex)),
		}
		txresp, err = s.client.Txn(ctx).If(cmps...).
			Then(clientv3.OpDelete(key)).
			Commit()

		deleted = txresp.Succeeded
		if !deleted {
			err = errors.New("conflicts!")
		}
	}

	if err != nil {
		return false, err
	}

	return deleted, nil
}

// Close closes the client connection
func (s *EtcdV3) Close() {
	defer func() {
		if recover() != nil {
			// close of closed channel panic occur
		}
	}()
	close(s.done)
	s.client.Close()
}
