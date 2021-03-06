// Copyright (c) 2017 Tigera, Inc. All rights reserved.

// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	"io/ioutil"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"
	"fmt"

	authz "github.com/envoyproxy/data-plane-api/api/auth"
	"github.com/projectcalico/app-policy/server"

	"github.com/projectcalico/libcalico-go/lib/apiconfig"

	docopt "github.com/docopt/docopt-go"
	log "github.com/sirupsen/logrus"
	spireauth "github.com/spiffe/spire/pkg/agent/auth"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
)

const usage = `Dikastes - the decider.

Usage:
  dikastes server [-t <token>|--kube <kubeconfig>] [options]
  dikastes client <namespace> <account> [--method <method>] [options]

Options:
  <namespace>            Service account namespace.
  <account>              Service account name.
  -h --help              Show this screen.
  -l --listen <port>     Unix domain socket path [default: /var/run/dikastes/dikastes.sock]
  -d --dial <target>     Target to dial. [default: localhost:50051]
  -k --kubernetes <api>  Kubernetes API Endpoint [default: https://kubernetes:443]
  -c --ca <ca>           Kubernetes CA Cert file [default: /var/run/secrets/kubernetes.io/serviceaccount/ca.crt]
  -t --token <token>     Kubernetes API Token file [default: /var/run/secrets/kubernetes.io/serviceaccount/token]
  --kube <kubeconfig>    Path to kubeconfig.
  --debug             Log at Debug level.`
const version = "0.1"
const NODE_NAME_ENV = "K8S_NODENAME"

func main() {
	arguments, err := docopt.Parse(usage, nil, true, version, false)
	if err != nil {
		println(usage)
		return
	}
	if arguments["--debug"].(bool) {
		log.SetLevel(log.DebugLevel)
	}
	if arguments["server"].(bool) {
		runServer(arguments)
	} else if arguments["client"].(bool) {
		runClient(arguments)
	}
	return
}

func runServer(arguments map[string]interface{}) {
	filePath := arguments["--listen"].(string)
	_, err := os.Stat(filePath)
	if !os.IsNotExist(err) {
		// file exists, try to delete it.
		err := os.Remove(filePath)
		if err != nil {
			log.WithFields(log.Fields{
				"listen": filePath,
				"err":    err,
			}).Fatal("File exists and unable to remove.")
		}
	}
	lis, err := net.Listen("unix", filePath)
	if err != nil {
		log.WithFields(log.Fields{
			"listen": filePath,
			"err":    err,
		}).Fatal("Unable to listen.")
	}
	defer lis.Close()
	err = os.Chmod(filePath, 0777) // Anyone on system can connect.
	if err != nil {
		log.Fatal("Unable to set write permission on socket.")
	}
	gs := grpc.NewServer(grpc.Creds(spireauth.NewCredentials()))
	ds, err := server.NewServer(getConfig(arguments), getNodeName())
	if err != nil {
		log.Fatalf("Unable to start server %v", err)
	}
	authz.RegisterAuthorizationServer(gs, ds)
	reflection.Register(gs)

	// Run gRPC server on separate goroutine so we catch any signals and clean up the socket.
	go func() {
		if err := gs.Serve(lis); err != nil {
			log.Fatalf("failed to serve: %v", err)
		}
	}()

	// Use a buffered channel so we don't miss any signals
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, os.Kill, syscall.SIGTERM)

	// Block until a signal is received.
	s := <-c
	log.Infof("Got signal:", s)
}

func getNodeName() string {
	nn, ok := os.LookupEnv(NODE_NAME_ENV)
	if !ok {
		log.Fatalf("Environment variable %v is required.", NODE_NAME_ENV)
	}
	return nn
}

func runClient(arguments map[string]interface{}) {
	opts := []grpc.DialOption{
		grpc.WithInsecure(),
		grpc.WithDialer(getDialer("unix"))}
	conn, err := grpc.Dial(arguments["--dial"].(string), opts...)
	if err != nil {
		log.Fatalf("fail to dial: %v", err)
	}
	defer conn.Close()
	client := authz.NewAuthorizationClient(conn)
	req := authz.CheckRequest{
		Attributes: &authz.AttributeContext{
			Source: &authz.AttributeContext_Peer{
				Principal: fmt.Sprintf("spiffe://cluster.local/ns/%s/sa/%s",
					arguments["<namespace>"].(string), arguments["<account>"].(string)),
			},
		},
	}
	if arguments["--method"].(bool) {
		req.Attributes.Request = &authz.AttributeContext_Request{
			Http: &authz.AttributeContext_HTTPRequest{
				Method: arguments["<method>"].(string),
			},
		}
	}
	resp, err := client.Check(context.Background(), &req)
	if err != nil {
		log.Fatalf("Failed %v", err)
	}
	log.Infof("Check response:\n %v", resp)
	return
}

func getDialer(proto string) func(string, time.Duration) (net.Conn, error) {
	return func(target string, timeout time.Duration) (net.Conn, error) {
		return net.DialTimeout(proto, target, timeout)
	}
}

func getConfig(arguments map[string]interface{}) apiconfig.CalicoAPIConfig {
	cfg := apiconfig.CalicoAPIConfig{
		Spec: apiconfig.CalicoAPIConfigSpec{
			DatastoreType: apiconfig.Kubernetes,
			KubeConfig:    apiconfig.KubeConfig{},
			AlphaFeatures: "serviceaccounts,httprules",
		},
	}
	if arguments["--kube"] != nil {
		cfg.Spec.KubeConfig.Kubeconfig = arguments["--kube"].(string)
	} else {
		token, err := ioutil.ReadFile(arguments["--token"].(string))
		if err != nil {
			log.Fatalf("Could not open token file %v. %v", arguments["--token"], err)
		}
		cfg.Spec.KubeConfig.K8sAPIToken = string(token)
		cfg.Spec.KubeConfig.K8sAPIEndpoint = arguments["--kubernetes"].(string)
		cfg.Spec.KubeConfig.K8sCAFile = arguments["--ca"].(string)
	}
	return cfg
}
