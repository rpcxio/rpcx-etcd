package serverplugin

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/rpcxio/libkv"
	"github.com/rpcxio/libkv/store"
	estore "github.com/rpcxio/rpcx-etcd/store"
	"github.com/rpcxio/rpcx-etcd/store/etcdv3"
	"github.com/smallnest/rpcx/log"
	"github.com/smallnest/rpcx/share"
)

func init() {
	libkv.AddStore(estore.ETCDV3, etcdv3.New)
}

type RegPath struct {
	key     string
	value   []byte
	options *store.WriteOptions
}

// EtcdV3RegisterPlugin implements etcd registry.
type EtcdV3RegisterPlugin struct {
	// service address, for example, tcp@127.0.0.1:8972, quic@127.0.0.1:1234
	ServiceAddress string
	// etcd addresses
	EtcdServers []string
	// base path for rpcx server, for example com/example/rpcx
	BasePath string
	// Registered services
	Services  []string
	metasLock sync.RWMutex
	metas     map[string]string
	TTL       time.Duration

	Options *store.Config
	kv      store.Store

	done chan struct{}

	paths         map[string]*RegPath
	ServerStarted chan struct{}
}

func (p *EtcdV3RegisterPlugin) register() error {
	if share.Trace {
		log.Infof("etcd register start")
	}
	for path, value := range p.paths {
		err := p.kv.Put(path, value.value, value.options)
		if err != nil && !strings.Contains(err.Error(), "Not a file") {
			log.Errorf("cannot create etcd path %s: %v", p.BasePath, err)
			return err
		}
	}
	return nil
}

// verifyCompletionParameters
func (p *EtcdV3RegisterPlugin) verifyCompletionParameters() error {
	if p.TTL == 0 {
		p.TTL = time.Minute
	}

	if p.done == nil {
		p.done = make(chan struct{})
	}

	if p.kv == nil {
		kv, err := libkv.NewStore(estore.ETCDV3, p.EtcdServers, p.Options)
		if err != nil {
			log.Errorf("cannot create etcd registry: %v", err)
			return err
		}
		p.kv = kv
	}
	return nil
}

// Start starts to connect etcd cluster
func (p *EtcdV3RegisterPlugin) Start() error {

	err := p.verifyCompletionParameters()
	if err != nil {
		return err
	}

	// create root path
	err = p.kv.Put(p.BasePath, []byte("rpcx_path"), &store.WriteOptions{IsDir: true, TTL: -1})
	if err != nil && !strings.Contains(err.Error(), "Not a file") {
		log.Errorf("cannot create etcd path %s: %v", p.BasePath, err)
		return err
	}

	go p.register()

	go func() {
		defer p.kv.Close()
		<-p.done
	}()

	return nil
}

// Stop unregister all services.
func (p *EtcdV3RegisterPlugin) Stop() error {

	err := p.verifyCompletionParameters()
	if err != nil {
		return err
	}

	for _, name := range p.Services {
		nodePath := fmt.Sprintf("%s/%s/%s", p.BasePath, name, p.ServiceAddress)
		exist, err := p.kv.Exists(nodePath)
		if err != nil {
			log.Errorf("cannot delete path %s: %v", nodePath, err)
			continue
		}
		if exist {
			p.kv.Delete(nodePath) // delete the registered node
			log.Infof("delete path %s", nodePath)
		}
	}

	close(p.done)
	return nil
}

// Register handles registering event.
// this service is registered at BASE/serviceName/thisIpAddress node
func (p *EtcdV3RegisterPlugin) Register(name string, rcvr interface{}, metadata string) (err error) {
	if strings.TrimSpace(name) == "" {
		err = errors.New("Register service `name` can't be empty")
		return
	}

	if p.kv == nil {
		kv, err := libkv.NewStore(estore.ETCDV3, p.EtcdServers, nil)
		if err != nil {
			log.Errorf("cannot create etcd registry: %v", err)
			return err
		}
		p.kv = kv
	}

	paths := make(map[string]*RegPath)

	// create service path
	nodePath := fmt.Sprintf("%s/%s", p.BasePath, name)
	paths[nodePath] = &RegPath{nodePath, []byte(name), &store.WriteOptions{IsDir: true, TTL: -1}}

	// create node
	nodePath = fmt.Sprintf("%s/%s/%s", p.BasePath, name, p.ServiceAddress)
	paths[nodePath] = &RegPath{nodePath, []byte(metadata), &store.WriteOptions{TTL: p.TTL}}

	services := make(map[string]struct{})
	for _, v := range p.Services {
		services[v] = struct{}{}
	}

	if _, ok := services[name]; !ok {
		p.Services = append(p.Services, name)
	}

	if p.ServerStarted == nil {
		p.register()
	} else {
		go p.register()
	}

	p.metasLock.Lock()
	if p.metas == nil {
		p.metas = make(map[string]string)
	}
	p.paths = paths
	p.metas[name] = metadata
	p.metasLock.Unlock()
	return
}

func (p *EtcdV3RegisterPlugin) RegisterFunction(serviceName, fname string, fn interface{}, metadata string) error {
	return p.Register(serviceName, fn, metadata)
}

func (p *EtcdV3RegisterPlugin) Unregister(name string) (err error) {
	if len(p.Services) == 0 {
		return nil
	}

	if strings.TrimSpace(name) == "" {
		err = errors.New("Register service `name` can't be empty")
		return
	}

	err = p.verifyCompletionParameters()
	if err != nil {
		return err
	}

	nodePath := fmt.Sprintf("%s/%s/%s", p.BasePath, name, p.ServiceAddress)
	err = p.kv.Delete(nodePath) // delete the registered node
	if err != nil {
		log.Errorf("cannot create consul path %s: %v", nodePath, err)
		return err
	}

	if len(p.Services) > 0 {
		var services = make([]string, 0, len(p.Services)-1)
		for _, s := range p.Services {
			if s != name {
				services = append(services, s)
			}
		}
		p.Services = services
	}

	p.metasLock.Lock()
	if p.metas == nil {
		p.metas = make(map[string]string)
	}
	delete(p.metas, name)
	p.metasLock.Unlock()
	return
}
