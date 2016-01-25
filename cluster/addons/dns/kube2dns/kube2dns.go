/*
Copyright 2016 The Kubernetes Authors All rights reserved.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"strings"
	"sync"
	"time"

	"github.com/golang/glog"
	skymsg "github.com/skynetservices/skydns/msg"
	kapi "k8s.io/kubernetes/pkg/api"
	kcache "k8s.io/kubernetes/pkg/client/cache"
	kclient "k8s.io/kubernetes/pkg/client/unversioned"
	kframework "k8s.io/kubernetes/pkg/controller/framework"
	kselector "k8s.io/kubernetes/pkg/fields"
	"k8s.io/kubernetes/pkg/util"
)

const (
	// Resync period for the kube controller loop.
	resyncPeriod = 30 * time.Minute
	// A subdomain added to the user specified domain for all services.
	serviceSubdomain = "svc"
	// A subdomain added to the user specified dmoain for all pods.
	podSubdomain = "pod"
)

type dnsRecord struct {
	host string
	ip   string
	ttl  uint64
}

type dnsBackend interface {
	// Add adds a new A record.
	Add(record dnsRecord) error
	// Remove removes a record using the host name and IP address
	Remove(host, ip string) error
	// Get returns the record
	Get(host string) (dnsRecord, error)
}

type nameNamespace struct {
	name      string
	namespace string
}

type kube2dns struct {
	backend dnsBackend
	// DNS domain name.
	domain string
	// A cache that contains all the endpoints in the system.
	endpointsStore kcache.Store
	// A cache that contains all the services in the system.
	servicesStore kcache.Store
	// A cache that contains all the pods in the system.
	podsStore kcache.Store
	// Lock for controlling access to headless services.
	mlock sync.Mutex
}

// Removes 'subdomain' from etcd.
func (ks *kube2dns) removeDNS(subdomain string) error {
	glog.V(2).Infof("Removing %s from DNS", subdomain)
	dnsRecord, err := ks.backend.Get(skymsg.Path(subdomain))
	if err != nil {
		return err
	}

	err = ks.backend.Remove(dnsRecord.host, dnsRecord.ip)
	return err
}

func (ks *kube2dns) writeRecord(subdomain string, data string) error {
	// Set with no TTL, and hope that kubernetes events are accurate.
	err := ks.backend.Add(dnsRecord{skymsg.Path(subdomain), data, uint64(0)})
	return err
}

// Generates skydns records for a headless service.
func (ks *kube2dns) newHeadlessService(subdomain string, service *kapi.Service) error {
	// Create an A record for every pod in the service.
	// This record must be periodically updated.
	// Format is as follows:
	// For a service x, with pods a and b create DNS records,
	// a.x.ns.domain. and, b.x.ns.domain.
	ks.mlock.Lock()
	defer ks.mlock.Unlock()
	key, err := kcache.MetaNamespaceKeyFunc(service)
	if err != nil {
		return err
	}
	e, exists, err := ks.endpointsStore.GetByKey(key)
	if err != nil {
		return fmt.Errorf("failed to get endpoints object from endpoints store - %v", err)
	}
	if !exists {
		glog.V(1).Infof("Could not find endpoints for service %q in namespace %q. DNS records will be created once endpoints show up.", service.Name, service.Namespace)
		return nil
	}
	if e, ok := e.(*kapi.Endpoints); ok {
		return ks.generateRecordsForHeadlessService(subdomain, e, service)
	}
	return nil
}

func getSkyMsg(ip string, port int) *skymsg.Service {
	return &skymsg.Service{
		Host:     ip,
		Port:     port,
		Priority: 10,
		Weight:   10,
		Ttl:      30,
	}
}

func (ks *kube2dns) generateRecordsForHeadlessService(subdomain string, e *kapi.Endpoints, svc *kapi.Service) error {
	for idx := range e.Subsets {
		for subIdx := range e.Subsets[idx].Addresses {
			b, err := json.Marshal(getSkyMsg(e.Subsets[idx].Addresses[subIdx].IP, 0))
			if err != nil {
				return err
			}
			recordValue := string(b)
			recordLabel := getHash(recordValue)
			recordKey := buildDNSNameString(subdomain, recordLabel)

			glog.V(2).Infof("Setting DNS record: %v -> %q\n", recordKey, recordValue)
			if err := ks.writeRecord(recordKey, recordValue); err != nil {
				return err
			}
			for portIdx := range e.Subsets[idx].Ports {
				endpointPort := &e.Subsets[idx].Ports[portIdx]
				portSegment := buildPortSegmentString(endpointPort.Name, endpointPort.Protocol)
				if portSegment != "" {
					err := ks.generateSRVRecord(subdomain, portSegment, recordLabel, recordKey, endpointPort.Port)
					if err != nil {
						return err
					}
				}
			}
		}
	}

	return nil
}

func (ks *kube2dns) getServiceFromEndpoints(e *kapi.Endpoints) (*kapi.Service, error) {
	key, err := kcache.MetaNamespaceKeyFunc(e)
	if err != nil {
		return nil, err
	}
	obj, exists, err := ks.servicesStore.GetByKey(key)
	if err != nil {
		return nil, fmt.Errorf("failed to get service object from services store - %v", err)
	}
	if !exists {
		glog.V(1).Infof("could not find service for endpoint %q in namespace %q", e.Name, e.Namespace)
		return nil, nil
	}
	if svc, ok := obj.(*kapi.Service); ok {
		return svc, nil
	}
	return nil, fmt.Errorf("got a non service object in services store %v", obj)
}

func (ks *kube2dns) addDNSUsingEndpoints(subdomain string, e *kapi.Endpoints) error {
	ks.mlock.Lock()
	defer ks.mlock.Unlock()
	svc, err := ks.getServiceFromEndpoints(e)
	if err != nil {
		return err
	}
	if svc == nil || kapi.IsServiceIPSet(svc) {
		// No headless service found corresponding to endpoints object.
		return nil
	}
	// Remove existing DNS entry.
	if err := ks.removeDNS(subdomain); err != nil {
		return err
	}
	return ks.generateRecordsForHeadlessService(subdomain, e, svc)
}

func (ks *kube2dns) handleEndpointAdd(obj interface{}) {
	if e, ok := obj.(*kapi.Endpoints); ok {
		buildDNSNameString(ks.domain, serviceSubdomain, e.Namespace, e.Name)
	}
}

func (ks *kube2dns) handlePodCreate(obj interface{}) {
	if e, ok := obj.(*kapi.Pod); ok {
		// If the pod ip is not yet available, do not attempt to create.
		if e.Status.PodIP != "" {
			buildDNSNameString(ks.domain, podSubdomain, e.Namespace, santizeIP(e.Status.PodIP))
		}
	}
}

func (ks *kube2dns) handlePodUpdate(old interface{}, new interface{}) {
	oldPod, okOld := old.(*kapi.Pod)
	newPod, okNew := new.(*kapi.Pod)

	// Validate that the objects are good
	if okOld && okNew {
		if oldPod.Status.PodIP != newPod.Status.PodIP {
			ks.handlePodDelete(oldPod)
			ks.handlePodCreate(newPod)
		}
	} else if okNew {
		ks.handlePodCreate(newPod)
	} else if okOld {
		ks.handlePodDelete(oldPod)
	}
}

func (ks *kube2dns) handlePodDelete(obj interface{}) {
	if e, ok := obj.(*kapi.Pod); ok {
		if e.Status.PodIP != "" {
			buildDNSNameString(ks.domain, podSubdomain, e.Namespace, santizeIP(e.Status.PodIP))
		}
	}
}

func (ks *kube2dns) generateRecordsForPod(subdomain string, service *kapi.Pod) error {
	b, err := json.Marshal(getSkyMsg(service.Status.PodIP, 0))
	if err != nil {
		return err
	}
	recordValue := string(b)
	recordLabel := getHash(recordValue)
	recordKey := buildDNSNameString(subdomain, recordLabel)

	glog.V(2).Infof("Setting DNS record: %v -> %q, with recordKey: %v\n", subdomain, recordValue, recordKey)
	if err := ks.writeRecord(recordKey, recordValue); err != nil {
		return err
	}

	return nil
}

func (ks *kube2dns) generateRecordsForPortalService(subdomain string, service *kapi.Service) error {
	b, err := json.Marshal(getSkyMsg(service.Spec.ClusterIP, 0))
	if err != nil {
		return err
	}
	recordValue := string(b)
	recordLabel := getHash(recordValue)
	recordKey := buildDNSNameString(subdomain, recordLabel)

	glog.V(2).Infof("Setting DNS record: %v -> %q, with recordKey: %v\n", subdomain, recordValue, recordKey)
	if err := ks.writeRecord(recordKey, recordValue); err != nil {
		return err
	}
	// Generate SRV Records
	for i := range service.Spec.Ports {
		port := &service.Spec.Ports[i]
		portSegment := buildPortSegmentString(port.Name, port.Protocol)
		if portSegment != "" {
			err = ks.generateSRVRecord(subdomain, portSegment, recordLabel, subdomain, port.Port)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func santizeIP(ip string) string {
	return strings.Replace(ip, ".", "-", -1)
}

func buildPortSegmentString(portName string, portProtocol kapi.Protocol) string {
	if portName == "" {
		// we don't create a random name
		return ""
	}

	if portProtocol == "" {
		glog.Errorf("Port Protocol not set. port segment string cannot be created.")
		return ""
	}

	return fmt.Sprintf("_%s._%s", portName, strings.ToLower(string(portProtocol)))
}

func (ks *kube2dns) generateSRVRecord(subdomain, portSegment, recordName, cName string, portNumber int) error {
	recordKey := buildDNSNameString(subdomain, portSegment, recordName)
	srv_rec, err := json.Marshal(getSkyMsg(cName, portNumber))
	if err != nil {
		return err
	}
	if err := ks.writeRecord(recordKey, string(srv_rec)); err != nil {
		return err
	}
	return nil
}

func (ks *kube2dns) addDNS(subdomain string, service *kapi.Service) error {
	if len(service.Spec.Ports) == 0 {
		glog.Fatalf("Unexpected service with no ports: %v", service)
	}
	// if ClusterIP is not set, a DNS entry should not be created
	if !kapi.IsServiceIPSet(service) {
		return ks.newHeadlessService(subdomain, service)
	}
	return ks.generateRecordsForPortalService(subdomain, service)
}

func buildDNSNameString(labels ...string) string {
	var res string
	for _, label := range labels {
		if res == "" {
			res = label
		} else {
			res = fmt.Sprintf("%s.%s", label, res)
		}
	}
	return res
}

// Returns a cache.ListWatch that gets all changes to services.
func createServiceLW(kubeClient *kclient.Client) *kcache.ListWatch {
	return kcache.NewListWatchFromClient(kubeClient, "services", kapi.NamespaceAll, kselector.Everything())
}

// Returns a cache.ListWatch that gets all changes to endpoints.
func createEndpointsLW(kubeClient *kclient.Client) *kcache.ListWatch {
	return kcache.NewListWatchFromClient(kubeClient, "endpoints", kapi.NamespaceAll, kselector.Everything())
}

// Returns a cache.ListWatch that gets all changes to pods.
func createEndpointsPodLW(kubeClient *kclient.Client) *kcache.ListWatch {
	return kcache.NewListWatchFromClient(kubeClient, "pods", kapi.NamespaceAll, kselector.Everything())
}

func (ks *kube2dns) newService(obj interface{}) {
	if s, ok := obj.(*kapi.Service); ok {
		buildDNSNameString(ks.domain, serviceSubdomain, s.Namespace, s.Name)
	}
}

func (ks *kube2dns) removeService(obj interface{}) {
	if s, ok := obj.(*kapi.Service); ok {
		buildDNSNameString(ks.domain, serviceSubdomain, s.Namespace, s.Name)
	}
}

func (ks *kube2dns) updateService(oldObj, newObj interface{}) {
	// TODO: Avoid unwanted updates.
	ks.removeService(oldObj)
	ks.newService(newObj)
}

func watchForServices(kubeClient *kclient.Client, ks *kube2dns) kcache.Store {
	serviceStore, serviceController := kframework.NewInformer(
		createServiceLW(kubeClient),
		&kapi.Service{},
		resyncPeriod,
		kframework.ResourceEventHandlerFuncs{
			AddFunc:    ks.newService,
			DeleteFunc: ks.removeService,
			UpdateFunc: ks.updateService,
		},
	)
	go serviceController.Run(util.NeverStop)
	return serviceStore
}

func watchEndpoints(kubeClient *kclient.Client, ks *kube2dns) kcache.Store {
	eStore, eController := kframework.NewInformer(
		createEndpointsLW(kubeClient),
		&kapi.Endpoints{},
		resyncPeriod,
		kframework.ResourceEventHandlerFuncs{
			AddFunc: ks.handleEndpointAdd,
			UpdateFunc: func(oldObj, newObj interface{}) {
				// TODO: Avoid unwanted updates.
				ks.handleEndpointAdd(newObj)
			},
		},
	)

	go eController.Run(util.NeverStop)
	return eStore
}

func watchPods(kubeClient *kclient.Client, ks *kube2dns) kcache.Store {
	eStore, eController := kframework.NewInformer(
		createEndpointsPodLW(kubeClient),
		&kapi.Pod{},
		resyncPeriod,
		kframework.ResourceEventHandlerFuncs{
			AddFunc: ks.handlePodCreate,
			UpdateFunc: func(oldObj, newObj interface{}) {
				ks.handlePodUpdate(oldObj, newObj)
			},
			DeleteFunc: ks.handlePodDelete,
		},
	)

	go eController.Run(util.NeverStop)
	return eStore
}

func getHash(text string) string {
	h := fnv.New32a()
	h.Write([]byte(text))
	return fmt.Sprintf("%x", h.Sum32())
}
