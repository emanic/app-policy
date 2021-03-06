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
	"fmt"
	"regexp"

	authz "github.com/envoyproxy/data-plane-api/api/auth"

	api "github.com/projectcalico/libcalico-go/lib/apis/v3"
	"github.com/projectcalico/libcalico-go/lib/selector"

	log "github.com/sirupsen/logrus"
)

const SPIFFE_ID_PATTERN = "^spiffe://[^/]+/ns/([^/]+)/sa/([^/]+)$"

var spiffeIdRegExp *regexp.Regexp

// match checks if the Rule matches the request.  It returns true if the Rule matches, false otherwise.
func match(rule api.Rule, req *authz.CheckRequest) bool {
	log.Debugf("Checking rule %v on request %v", rule, req)
	attr := req.GetAttributes()
	return matchPeer(rule.Source, attr.GetSource()) && matchRequest(rule, attr.GetRequest())
}

func matchPeer(er api.EntityRule, peer *authz.AttributeContext_Peer) bool {
	return matchServiceAccounts(er.ServiceAccounts, peer)
}

func matchRequest(rule api.Rule, req *authz.AttributeContext_Request) bool {
	log.WithFields(log.Fields{
		"request": req,
	}).Debug("Matching request.")
	return matchHTTP(rule.HTTP, req.GetHttp())
}

func matchServiceAccounts(saMatch *api.ServiceAccountMatch, peer *authz.AttributeContext_Peer) bool {
	principle := peer.GetPrincipal()
	labels := peer.GetLabels()
	log.WithFields(log.Fields{
		"peer":   principle,
		"labels": labels,
		"rule":   saMatch},
	).Debug("Matching service account.")
	accountName, namespace, err := parseSpiffeId(principle)
	if err != nil {
		log.WithFields(log.Fields{
			"principle": principle,
			"msg":       err,
		}).Warn("Unable to parse authenticated principle as SPIFFE ID.")
		return false
	}
	log.WithFields(log.Fields{
		"name":      accountName,
		"namespace": namespace,
	}).Debug("Parsed SPIFFE ID.")
	if saMatch == nil {
		log.Debug("nil ServiceAccountMatch.  Return true.")
		return true
	}
	return matchServiceAccountName(saMatch.Names, accountName) &&
		matchServiceAccountLabels(saMatch.Selector, labels)
}

// Parse an Istio SPIFFE ID and extract the service account name and namespace.
func parseSpiffeId(id string) (name, namespace string, err error) {
	// Init the regexp the first time this is called, and store it in the package namespace.
	if spiffeIdRegExp == nil {
		// We drop the returned error here, since we are compiling
		spiffeIdRegExp, _ = regexp.Compile(SPIFFE_ID_PATTERN)
	}
	match := spiffeIdRegExp.FindStringSubmatch(id)
	if match == nil {
		err = fmt.Errorf("expected match %s, got %s", SPIFFE_ID_PATTERN, id)
	} else {
		name = match[2]
		namespace = match[1]
	}
	return
}

func matchServiceAccountName(names []string, name string) bool {
	log.WithFields(log.Fields{
		"names": names,
		"name":  name,
	}).Debug("Matching service account name")
	if len(names) == 0 {
		log.Debug("No service account names on rule.")
		return true
	}
	for _, name2 := range names {
		if name2 == name {
			return true
		}
	}
	return false
}

func matchServiceAccountLabels(selectorStr string, labels map[string]string) bool {
	log.WithFields(log.Fields{
		"selector": selectorStr,
		"labels":   labels,
	}).Debug("Matching service account labels.")
	sel, err := selector.Parse(selectorStr)
	if err != nil {
		log.Warnf("Could not parse policy selector %v, %v", selectorStr, err)
		return false
	}
	log.Debugf("Parsed selector.", sel)
	return sel.Evaluate(labels)

}

func matchHTTP(rule *api.HTTPRule, req *authz.AttributeContext_HTTPRequest) bool {
	log.WithFields(log.Fields{
		"rule": rule,
	}).Debug("Matching HTTP.")
	if rule == nil {
		log.Debug("nil HTTPRule.  Return true")
		return true
	}
	return matchHTTPMethods(rule.Methods, req.GetMethod())
}

func matchHTTPMethods(methods []string, reqMethod string) bool {
	log.WithFields(log.Fields{
		"methods":   methods,
		"reqMethod": reqMethod,
	}).Debug("Matching HTTP Methods")
	if len(methods) == 0 {
		log.Debug("Rule has 0 HTTP Methods, matched.")
		return true
	}
	for _, method := range methods {
		if method == "*" {
			log.Debug("Rule matches all methods with wildcard *")
			return true
		}
		if method == reqMethod {
			log.Debug("HTTP Method matched.")
			return true
		}
	}
	log.Debug("HTTP Method not matched.")
	return false
}
