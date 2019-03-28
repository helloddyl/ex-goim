package main

import (
	"context"
	"flag"
	"fmt"
	"math/rand"
	"net"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/tsingson/discovery/naming"
	resolver "github.com/tsingson/discovery/naming/grpc"
	"github.com/tsingson/fastx/utils"
	log "github.com/tsingson/zaplogger"

	"github.com/tsingson/goim/internal/nats/comet"
	"github.com/tsingson/goim/internal/nats/comet/conf"
	"github.com/tsingson/goim/internal/nats/comet/grpc"
	md "github.com/tsingson/goim/internal/nats/model"
	"github.com/tsingson/goim/pkg/ip"
)

const (
	ver   = "2.0.0"
	appid = "goim.comet"
)

var (
	cfg *conf.CometConfig
)

func main() {

	path, _ := utils.GetCurrentExecDir()
	confPath := path + "/comet-config.toml"

	flag.Parse()
	var err error
	cfg, err = conf.Init(confPath)
	if err != nil {
		panic(err)
	}

	cfg.Env = &conf.Env{
		Region:    "test",
		Zone:      "test",
		DeployEnv: "test",
		Host:      "test_server",
	}

	rand.Seed(time.Now().UTC().UnixNano())
	runtime.GOMAXPROCS(runtime.NumCPU())
	println(cfg.Debug)

	log.Infof("goim-comet [version: %s env: %+v] start", ver, cfg.Env)
	// register discovery
	dis := naming.New(cfg.Discovery)
	resolver.Register(dis)
	// new comet server
	srv := comet.NewServer(cfg)
	// if err := comet.InitWhitelist(cfg.Whitelist); err != nil {
	// 	panic(err)
	// }
	if err := comet.InitTCP(srv, cfg.TCP.Bind, runtime.NumCPU()); err != nil {
		panic(err)
	}
	if err := comet.InitWebsocket(srv, cfg.Websocket.Bind, runtime.NumCPU()); err != nil {
		panic(err)
	}
	if cfg.Websocket.TLSOpen {
		if err := comet.InitWebsocketWithTLS(srv, cfg.Websocket.TLSBind, cfg.Websocket.CertFile, cfg.Websocket.PrivateFile, runtime.NumCPU()); err != nil {
			panic(err)
		}
	}
	// new grpc server
	rpcSrv := grpc.New(cfg.RPCServer, srv)
	cancel := register(dis, srv)
	// signal
	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGHUP, syscall.SIGQUIT, syscall.SIGTERM, syscall.SIGINT)
	for {
		s := <-c
		log.Infof("goim-comet get a signal %s", s.String())
		switch s {
		case syscall.SIGQUIT, syscall.SIGTERM, syscall.SIGINT:
			if cancel != nil {
				cancel()
			}
			rpcSrv.GracefulStop()
			srv.Close()
			log.Infof("goim-comet [version: %s] exit", ver)
			// log.Flush()
			return
		case syscall.SIGHUP:
		default:
			return
		}
	}
}

func register(dis *naming.Discovery, srv *comet.Server) context.CancelFunc {
	env := cfg.Env
	addr := ip.InternalIP()
	_, port, _ := net.SplitHostPort(cfg.RPCServer.Addr)
	ins := &naming.Instance{
		Region:   env.Region,
		Zone:     env.Zone,
		Env:      env.DeployEnv,
		Hostname: env.Host,
		AppID:    appid,
		Addrs: []string{
			"grpc://" + addr + ":" + port,
		},
		Metadata: map[string]string{
			md.MetaWeight:  strconv.FormatInt(env.Weight, 10),
			md.MetaOffline: strconv.FormatBool(env.Offline),
			md.MetaAddrs:   strings.Join(env.Addrs, ","),
		},
	}
	cancel, err := dis.Register(ins)
	if err != nil {
		panic(err)
	}
	// renew discovery metadata
	go func() {
		for {
			var (
				err   error
				conns int
				ips   = make(map[string]struct{})
			)
			for _, bucket := range srv.Buckets() {
				for ip := range bucket.IPCount() {
					ips[ip] = struct{}{}
				}
				conns += bucket.ChannelCount()
			}
			ins.Metadata[md.MetaConnCount] = fmt.Sprint(conns)
			ins.Metadata[md.MetaIPCount] = fmt.Sprint(len(ips))
			if err = dis.Set(ins); err != nil {
				log.Errorf("dis.Set(%+v) error(%v)", ins, err)
				time.Sleep(time.Second)
				continue
			}
			time.Sleep(time.Second * 10)
		}
	}()
	return cancel
}
