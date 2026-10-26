package home

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestRedisOptionsSnapshotPreservesTLSAndTimeouts(t *testing.T) {
	for _, test := range []struct {
		name       string
		seed       string
		host       string
		address    string
		tls        config.HomeTLSConfig
		serverName string
	}{
		{name: "TLS disabled", seed: "seed.example", host: "current.example", address: "candidate.example:8327"},
		{name: "seed name", seed: " seed.example ", host: "current.example", address: "candidate.example:8327", tls: config.HomeTLSConfig{Enable: true}, serverName: "seed.example"},
		{name: "explicit name", seed: "seed.example", address: "candidate.example:8327", tls: config.HomeTLSConfig{Enable: true, ServerName: " explicit.example ", UseTargetServerName: true, InsecureSkipVerify: true}, serverName: "explicit.example"},
		{name: "target name", seed: "seed.example", address: "candidate.example:8327", tls: config.HomeTLSConfig{Enable: true, UseTargetServerName: true}, serverName: "candidate.example"},
		{name: "IPv6 target", address: "[2001:db8::1]:8327", tls: config.HomeTLSConfig{Enable: true, UseTargetServerName: true}, serverName: "2001:db8::1"},
		{name: "current host fallback", host: " current.example ", address: "candidate.example:8327", tls: config.HomeTLSConfig{Enable: true}, serverName: "current.example"},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := New(config.HomeConfig{Host: test.host, TLS: test.tls})
			client.seedHost = test.seed
			client.cmdOptions = &redis.Options{DialTimeout: time.Second, ReadTimeout: 2 * time.Second, WriteTimeout: 4 * time.Second}
			client.mu.Lock()
			options, tlsSettings := client.redisOptionsSnapshotLocked(test.address)
			client.mu.Unlock()
			if options.TLSConfig != nil || options.Dialer != nil {
				t.Fatal("snapshot loaded certificates or installed a client-bound dialer")
			}
			// Mutating live settings after the snapshot must not affect certificate loading or timeouts.
			client.mu.Lock()
			client.homeCfg.TLS = config.HomeTLSConfig{Enable: true, ClientCert: "missing", ClientKey: "missing"}
			client.cmdOptions.DialTimeout, client.cmdOptions.ReadTimeout, client.cmdOptions.WriteTimeout = 9*time.Second, 9*time.Second, 9*time.Second
			client.mu.Unlock()
			tlsConfig, errTLS := newHomeTLSConfig(tlsSettings, "")
			if errTLS != nil {
				t.Fatal(errTLS)
			}
			if !test.tls.Enable {
				if tlsConfig != nil {
					t.Fatal("TLS disabled snapshot enabled TLS")
				}
			} else if tlsConfig == nil || tlsConfig.ServerName != test.serverName || tlsConfig.MinVersion != tls.VersionTLS12 || tlsConfig.InsecureSkipVerify != test.tls.InsecureSkipVerify {
				t.Fatalf("TLS snapshot changed semantics: %+v", tlsConfig)
			}
			if options.Addr != test.address || options.DialTimeout != time.Second || options.ReadTimeout != 2*time.Second || options.WriteTimeout != 4*time.Second || options.MaxRetries != -1 || options.DialerRetries != 1 || !options.ContextTimeoutEnabled {
				t.Fatalf("connection settings changed: %+v", options)
			}
		})
	}
}

func TestRedisOptionsSnapshotDoesNotLoadCertificateFiles(t *testing.T) {
	cert := newHomeTestCertificate(t)
	keyDER, errKey := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	if errKey != nil {
		t.Fatal(errKey)
	}
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	client := New(config.HomeConfig{Host: "seed.example", TLS: config.HomeTLSConfig{
		Enable: true, ClientCert: certPath, ClientKey: keyPath, CACert: certPath,
	}})
	client.testOperationTimeout = 5 * time.Second
	client.mu.Lock()
	options, tlsSettings := client.redisOptionsSnapshotLocked("candidate.example:8327")
	client.mu.Unlock()
	// The files do not exist at snapshot time; IO must be deferred until after releasing the lock.
	if errWrite := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]}), 0600); errWrite != nil {
		t.Fatal(errWrite)
	}
	if errWrite := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0600); errWrite != nil {
		t.Fatal(errWrite)
	}
	tlsConfig, errTLS := newHomeTLSConfig(tlsSettings, "")
	if errTLS != nil {
		t.Fatal(errTLS)
	}
	if len(tlsConfig.Certificates) != 1 || !bytes.Equal(tlsConfig.Certificates[0].Certificate[0], cert.Certificate[0]) || tlsConfig.RootCAs == nil || tlsConfig.ServerName != "seed.example" {
		t.Fatal("snapshot did not preserve client certificate, CA, or server name")
	}
	if options.DialTimeout != 5*time.Second || options.ReadTimeout != 5*time.Second || options.WriteTimeout != 5*time.Second {
		t.Fatal("test timeout precedence changed")
	}
	if errRemove := os.Remove(certPath); errRemove != nil {
		t.Fatal(errRemove)
	}
	if _, errReload := newHomeTLSConfig(tlsSettings, ""); !errors.Is(errReload, os.ErrNotExist) {
		t.Fatalf("certificate failure was cached or swallowed: %v", errReload)
	}
}

func TestClusterLatencyProbeTLSFailureDoesNotDial(t *testing.T) {
	for _, test := range []struct {
		name string
		tls  config.HomeTLSConfig
	}{
		{name: "client certificate", tls: config.HomeTLSConfig{Enable: true, ClientCert: "missing", ClientKey: "missing"}},
		{name: "CA certificate", tls: config.HomeTLSConfig{Enable: true, CACert: "missing"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			client, commands := newRedisCommandTestClient(t, balanceTestRedisReply)
			path := filepath.Join(t.TempDir(), "missing.pem")
			settings := test.tls
			if settings.ClientCert != "" {
				settings.ClientCert, settings.ClientKey = path, path
			} else {
				settings.CACert = path
			}
			client.homeCfg.TLS = settings
			address := clusterNodeAddress(clusterNode{IP: client.homeCfg.Host, Port: client.homeCfg.Port})
			if _, errProbe := client.probeClusterLatency(context.Background(), address); !errors.Is(errProbe, os.ErrNotExist) {
				t.Fatalf("probe certificate error = %v, want missing-file error", errProbe)
			}
			if len(commands.All()) != 0 {
				t.Fatal("probe contacted Home after certificate loading failed")
			}
		})
	}
}

func TestClusterLatencyReportsRandomizeOnlyInitialDelay(t *testing.T) {
	for range 100 {
		if delay := clusterLatencyReportDelay(); delay <= time.Minute || delay > 2*time.Minute {
			t.Fatalf("initial report delay out of bounds: %v", delay)
		}
	}
	for _, rpcDelay := range []time.Duration{0, 2 * time.Second} {
		t.Run(rpcDelay.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				reports := make(chan time.Time, 8)
				redisClient := redis.NewClient(&redis.Options{
					Addr: "127.0.0.1:8327", Protocol: 2, DisableIdentity: true, MaxRetries: -1,
					Dialer: func(context.Context, string, string) (net.Conn, error) {
						local, peer := net.Pipe()
						subscribed := false
						go serveRedisCommandTestConn(peer, &redisCommandLog{}, func(args []string) string {
							switch strings.ToUpper(args[0]) {
							case "SUBSCRIBE":
								subscribed = true
								return "*3\r\n$9\r\nsubscribe\r\n$6\r\nconfig\r\n:1\r\n"
							case "PING":
								if subscribed && len(args) == 2 {
									return fmt.Sprintf("*2\r\n$4\r\npong\r\n$%d\r\n%s\r\n", len(args[1]), args[1])
								}
							case "CLUSTER":
								reports <- time.Now()
								if rpcDelay > 0 {
									timer := time.NewTimer(rpcDelay)
									<-timer.C
								}
								return "$11\r\n{\"ok\":true}\r\n"
							}
							return balanceTestRedisReply(args)
						})
						return local, nil
					},
				})
				client := New(config.HomeConfig{Enabled: true, Host: "127.0.0.1", Port: 8327})
				client.cmd, client.sub = redisClient, redisClient
				enableBalanceTestClient(client)
				client.balance.latencySupported = true
				defer client.Close()
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				pubsub := redisClient.Subscribe(ctx, redisChannelConfig)
				defer func() {
					if errClose := pubsub.Close(); errClose != nil && !errors.Is(errClose, redis.ErrClosed) {
						t.Error(errClose)
					}
				}()
				if errACK := receiveSubscriptionACKs(ctx, pubsub, time.Second, []string{redisChannelConfig}); errACK != nil {
					t.Fatal(errACK)
				}
				stopClose := context.AfterFunc(ctx, func() { _ = pubsub.Close() })
				defer stopClose()
				sampler := &clusterRTTSampler{history: &clusterLatencyHistory{}, node: clusterNode{IP: "127.0.0.1", Port: 8327}}
				receiveDone := make(chan struct{})
				go func() {
					defer close(receiveDone)
					for {
						event, errReceive := pubsub.Receive(ctx)
						if errReceive != nil {
							if ctx.Err() == nil {
								t.Errorf("receive telemetry: %v", errReceive)
							}
							return
						}
						if pong, ok := event.(*redis.Pong); ok {
							sampler.pong(pong.Payload, time.Now())
						}
					}
				}()
				started := time.Now()
				done := make(chan error, 1)
				go func() { done <- client.runClusterLatency(ctx, pubsub, sampler) }()
				var previous time.Time
				for index := range 4 {
					select {
					case reportedAt := <-reports:
						if index == 0 {
							if delay := reportedAt.Sub(started); delay <= time.Minute || delay > 2*time.Minute {
								t.Fatalf("initial report did not use randomized delay: %v", delay)
							}
						} else if interval := reportedAt.Sub(previous); interval != time.Minute {
							t.Fatalf("recurring reports drifted or added jitter: %v", interval)
						}
						previous = reportedAt
					case errRun := <-done:
						t.Fatalf("sampler ended before four reports: %v", errRun)
					}
				}
				// Let the final RPC complete before stopping; fake time avoids wall-clock sleeps.
				timer := time.NewTimer(rpcDelay + time.Millisecond)
				<-timer.C
				synctest.Wait()
				cancel()
				if errRun := <-done; errRun != nil {
					t.Fatal(errRun)
				}
				<-receiveDone
			})
		})
	}
}
