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

package server

import (
	authz "github.com/envoyproxy/data-plane-api/api/auth"

	"github.com/projectcalico/libcalico-go/lib/clientv3"
	"github.com/projectcalico/libcalico-go/lib/apiconfig"
	"github.com/projectcalico/libcalico-go/lib/backend/k8s/resources"

	log "github.com/sirupsen/logrus"
	"golang.org/x/net/context"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/genproto/googleapis/rpc/code"

)

type (
	auth_server struct {
		NodeName   string
		Query CalicoQuery
	}
)

func NewServer(config apiconfig.CalicoAPIConfig, nodeName string) (*auth_server, error) {
	c, err := clientv3.New(config)
	log.Debug("Created Calico Client.")
	if err != nil {
		return nil, err
	}

	// Temporary hack for direct access to K8s API.
	clientset, err := NewKubeClient(config)
	if err != nil {
		return nil, err
	}
	q := NewCalicoQuery(c, clientset)
	return &auth_server{nodeName, q}, nil
}

func (as *auth_server) Check(ctx context.Context, req *authz.CheckRequest) (*authz.CheckResponse, error) {
	log.Debugf("Check(%v, %v)", ctx, req)
	resp := authz.CheckResponse{Status: &status.Status{Code: code.Code_value["INTERNAL"]}}
	cid, err := getContainerFromContext(ctx)
	if err != nil {
		log.Errorf("Failed to get container ID. %v", err)
		return &resp, nil
	}
	name, namespace, err := as.Query.GetEndpointFromContainer(cid, as.NodeName)
	if err != nil {
		log.Errorf("Failed to get endpoint for container %v. %v", cid, err)
	}
	policies, err := as.Query.GetPolicies(name, namespace)
	if err != nil {
		log.Errorf("Failed to get policies. %v", err)
		return &resp, nil
	}
	st := checkPolicies(policies, req)
	resp.Status = &st
	log.WithFields(log.Fields{
		"Request":  req,
		"Response": resp,
	}).Info("Check complete")
	return &resp, nil
}

// Modified from libcalico-go/lib/backend/k8s/k8s.go to return bare clientset.
func NewKubeClient(calCfg apiconfig.CalicoAPIConfig) (*kubernetes.Clientset, error) {
	kc := &calCfg.Spec.KubeConfig
	// Use the kubernetes client code to load the kubeconfig file and combine it with the overrides.
	log.Debugf("Building client for config: %+v", kc)
	configOverrides := &clientcmd.ConfigOverrides{}
	var overridesMap = []struct {
		variable *string
		value    string
	}{
		{&configOverrides.ClusterInfo.Server, kc.K8sAPIEndpoint},
		{&configOverrides.AuthInfo.ClientCertificate, kc.K8sCertFile},
		{&configOverrides.AuthInfo.ClientKey, kc.K8sKeyFile},
		{&configOverrides.ClusterInfo.CertificateAuthority, kc.K8sCAFile},
		{&configOverrides.AuthInfo.Token, kc.K8sAPIToken},
	}

	// Set an explicit path to the kubeconfig if one
	// was provided.
	loadingRules := clientcmd.ClientConfigLoadingRules{}
	if kc.Kubeconfig != "" {
		loadingRules.ExplicitPath = kc.Kubeconfig
	}

	// Using the override map above, populate any non-empty values.
	for _, override := range overridesMap {
		if override.value != "" {
			*override.variable = override.value
		}
	}
	if kc.K8sInsecureSkipTLSVerify {
		configOverrides.ClusterInfo.InsecureSkipTLSVerify = true
	}
	log.Debugf("Config overrides: %+v", configOverrides)

	// A kubeconfig file was provided.  Use it to load a config, passing through
	// any overrides.
	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&loadingRules, configOverrides).ClientConfig()
	if err != nil {
		return nil, resources.K8sErrorToCalico(err, nil)
	}

	// Create the clientset
	cs, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, resources.K8sErrorToCalico(err, nil)
	}
	log.Debugf("Created k8s clientSet: %+v", cs)
	return cs, nil
}
