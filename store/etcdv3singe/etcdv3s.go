package etcdv3singe

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/rpcxio/libkv"
	"github.com/rpcxio/libkv/store"
	estore "github.com/rpcxio/rpcx-etcd/store"
	"go.etcd.io/etcd/api/v3/mvccpb"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/client/v3/concurrency"
)

func init() {
	libkv.AddStore(estore.ETCDV3_SINGLE, New)
}

const defaultTTL = 30

// EtcdConfigAutoSyncInterval give a choice to those etcd cluster could not auto sync
// such I deploy clusters in docker they will dial tcp: lookup etcd1: Try again, can just set this to zero
var EtcdConfigAutoSyncInterval = time.Minute * 5

type RegItem struct {
	key     string
	value   []byte
	options *store.WriteOptions
}

// EtcdV3 is the receiver type for the Store interface
type EtcdV3Singe struct {
	timeout        time.Duration
	client         *clientv3.Client
	cfg            clientv3.Config
	regItems       map[string]RegItem
	leaseIDs       map[int64]clientv3.LeaseID
	done           chan struct{}
	startKeepAlive chan struct{}

	AllowKeyNotFound bool

	FaultRecovery bool

	mu  sync.RWMutex
	ttl int64
}

// New creates a new Etcd client given a list
// of endpoints and an optional tls config
func New(addrs []string, options *store.Config) (store.Store, error) {
	s := &EtcdV3Singe{
		done:           make(chan struct{}),
		startKeepAlive: make(chan struct{}),
		regItems:       make(map[string]RegItem),
		leaseIDs:       make(map[int64]clientv3.LeaseID),
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
	cli, err := clientv3.New(s.cfg)
	if err != nil {
		return nil, err
	}
	s.client = cli
	return s, nil
}

func (s *EtcdV3Singe) keepAlive(ttl int64, leaseID clientv3.LeaseID) {
	go func(id clientv3.LeaseID) {
		ch, kaerr := s.client.KeepAlive(context.Background(), id)
		if kaerr != nil {
			log.Printf("Failed to keep alive lease %v: %v", id, kaerr)
			return
		}
		for ka := range ch {
			if ka == nil {
				log.Printf("lease %v has expired", id)
				return
			}
			fmt.Printf("lease %v renewed with TTL: %v\n", id, ka.TTL)
		}
		s.mu.Lock()
		delete(s.leaseIDs, ttl)
		s.mu.Unlock()
		if s.FaultRecovery {
			log.Printf("lease fault recovery %v", id)
			leaseID, err := s.getLeaseID(ttl)
			if err != nil {
				log.Printf("lease %v grant err: %s", id, err)
				return
			}
			s.mu.Lock()
			s.leaseIDs[ttl] = leaseID
			s.mu.Unlock()
			for _, v := range s.regItems {
				if int64(v.options.TTL.Seconds()) == ttl {
					ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
					_, err := s.client.Put(ctx, v.key, string(v.value), clientv3.WithLease(leaseID))
					cancel()
					if err != nil {
						log.Printf("lease %v fault recovery, put %v err:  %s", id, v.key, err)
						return
					}
					log.Printf("lease fault recovery %v, path: %s", id, v.key)
				}
			}
		}
	}(leaseID)
}

// grant a lease.
func (s *EtcdV3Singe) grant(ttl int64) (clientv3.LeaseID, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	resp, err := s.client.Grant(ctx, ttl)
	cancel()
	if err != nil {
		return 0, err
	}
	s.keepAlive(ttl, resp.ID)
	return resp.ID, err
}

func (s *EtcdV3Singe) getLeaseID(ttl int64) (clientv3.LeaseID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	leaseID, ok := s.leaseIDs[ttl]
	if ok {
		return leaseID, nil
	}
	leaseID, err := s.grant(ttl)
	if err != nil {
		return leaseID, err
	}
	s.leaseIDs[ttl] = leaseID
	return leaseID, nil
}

// Put a value at the specified key
func (s *EtcdV3Singe) Put(key string, value []byte, options *store.WriteOptions) error {
	opts := make([]clientv3.OpOption, 0)
	if options != nil && options.TTL != -1 {
		leaseID, err := s.getLeaseID(int64(options.TTL.Seconds()))
		if err != nil {
			return err
		}
		opts = append(opts, clientv3.WithLease(leaseID))
	}
	s.mu.Lock()
	s.regItems[key] = RegItem{key: key, value: value, options: options}
	s.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	_, err := s.client.Put(ctx, key, string(value), opts...)
	cancel()

	return err
}

// Get a value given its key
func (s *EtcdV3Singe) Get(key string) (*store.KVPair, error) {
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
func (s *EtcdV3Singe) Delete(key string) error {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	_, err := s.client.Delete(ctx, key)
	cancel()

	return err
}

// Exists verifies if a Key exists in the store
func (s *EtcdV3Singe) Exists(key string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	resp, err := s.client.Get(ctx, key)
	cancel()
	if err != nil {
		return false, err
	}

	return len(resp.Kvs) != 0, nil
}

// Watch for changes on a key.
func (s *EtcdV3Singe) Watch(key string, stopCh <-chan struct{}) (<-chan *store.KVPair, error) {
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
func (s *EtcdV3Singe) WatchTree(directory string, stopCh <-chan struct{}) (<-chan []*store.KVPair, error) {
	watchCh := make(chan []*store.KVPair)
	list, err := s.List(directory)
	if err != nil {
		if !s.AllowKeyNotFound || err != store.ErrKeyNotFound {
			return watchCh, err
		}
	}
	localKVPair := make(map[string]*store.KVPair)
	for _, v := range list {
		localKVPair[v.Key] = v
	}
	go func() {
		defer close(watchCh)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		watchCh <- list
		rch := s.client.Watch(ctx, directory, clientv3.WithPrefix())
		for {
			select {
			case <-s.done:
				return
			case resp, ok := <-rch:
				if !ok || resp.Canceled { // watch is canceled
					return
				}

				for _, event := range resp.Events {
					switch event.Type {
					case mvccpb.PUT:
						fmt.Printf("PUT %s : %s\n", event.Kv.Key, event.Kv.Value)
						localKVPair[string(event.Kv.Key)] = &store.KVPair{
							Key:       string(event.Kv.Key),
							Value:     event.Kv.Value,
							LastIndex: uint64(event.Kv.Version),
						}
					case mvccpb.DELETE:
						delete(localKVPair, string(event.Kv.Key))
						fmt.Printf("DELETE %s\n", event.Kv.Key)
					}
				}

				list = list[:0]
				for _, v := range localKVPair {
					list = append(list, v)
				}
				watchCh <- list
			}
		}
	}()

	return watchCh, nil
}

type etcdLock3 struct {
	session *concurrency.Session
	mutex   *concurrency.Mutex
}

// NewLock creates a lock for a given key.
// The returned Locker is not held and must be acquired
// with `.Lock`. The Value is optional.
func (s *EtcdV3Singe) NewLock(key string, options *store.LockOptions) (store.Locker, error) {
	return nil, errors.New("not implemented")
}

// List the content of a given prefix
func (s *EtcdV3Singe) List(directory string) ([]*store.KVPair, error) {
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
func (s *EtcdV3Singe) DeleteTree(directory string) error {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	_, err := s.client.Delete(ctx, directory, clientv3.WithPrefix())
	cancel()

	return err
}

// AtomicPut CAS operation on a single value.
// Pass previous = nil to create a new key.
func (s *EtcdV3Singe) AtomicPut(key string, value []byte, previous *store.KVPair, options *store.WriteOptions) (bool, *store.KVPair, error) {
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
func (s *EtcdV3Singe) AtomicDelete(key string, previous *store.KVPair) (bool, error) {
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
func (s *EtcdV3Singe) Close() {
	defer func() {
		if recover() != nil {
			// close of closed channel panic occur
		}
	}()
	close(s.done)
	s.client.Close()
}
