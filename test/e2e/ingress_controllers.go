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

package e2e

import (
	"fmt"
	"io/ioutil"
	"net/http"
	"path/filepath"

	"k8s.io/kubernetes/pkg/api"
	"k8s.io/kubernetes/pkg/apis/extensions"
	client "k8s.io/kubernetes/pkg/client/unversioned"
	"k8s.io/kubernetes/pkg/labels"
	"k8s.io/kubernetes/pkg/runtime"
	"k8s.io/kubernetes/pkg/util/wait"
	utilyaml "k8s.io/kubernetes/pkg/util/yaml"
	"k8s.io/kubernetes/test/e2e/framework"

	"github.com/ghodss/yaml"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

// IngressControllerTester is an interface used to test ingress controllers.
type IngressControllerTester interface {
	// start starts the ingress controller in the given namespace
	start(namespace string) error
	// lookup returns the address (ip/hostname) associated with ingressKey
	lookup(ingressKey string) string
	// stop stops the ingress controller
	stop() error
	// name returns the name of the ingress controller
	getName() string
}

type httpResponse struct {
	StatusCode int         `yaml:"statusCode"`
	Body       string      `yaml:"body"`
	Headers    http.Header `yaml:"headers"`
}

type ingressTest struct {
	Name   string `yaml:"name"`
	Checks []struct {
		Request struct {
			Headers map[string]string `yaml:"headers"`
			Method  string            `yaml:"method"`
			URL     string            `yaml:"url"`
		} `yaml:"request"`
		Response httpResponse `yaml:"response"`
	} `yaml:"checks"`
	Requirements struct {
		Services []struct {
			Filename string `yaml:"file"`
			Name     string `yaml:"name"`
		} `yaml:"services"`
		Ingress []struct {
			Filename string `yaml:"file"`
			Name     string `yaml:"name"`
		} `yaml:"ingress"`
	} `yaml:"requirements,omitempty"`
}

// nginxControllerTester implements LBCTester for bare metal haproxy LBs.
type nginxControllerTester struct {
	client      *client.Client
	cfg         string
	rcName      string
	rcNamespace string
	name        string
	address     []string
}

// getLoadBalancerControllers returns a list of LBCtesters.
func getIngressControllers(repoRoot string, client *client.Client) []*nginxControllerTester {
	return []*nginxControllerTester{
		&nginxControllerTester{
			name:   "nginx",
			cfg:    filepath.Join(repoRoot, "test", "e2e", "testing-manifests", "ingress", "nginx-ingress-rc.yaml"),
			client: client,
		},
	}
}

// getTestCases returns a list of tests.
func getTestCases(repoRoot string) ([]ingressTest, error) {
	filename := filepath.Join(repoRoot, "test", "e2e", "testing-manifests", "ingress", "tests.yaml")
	b, err := ioutil.ReadFile(filename)
	if err != nil {
		return []ingressTest{}, err
	}

	var tests []ingressTest
	err = yaml.Unmarshal(b, &tests)
	if err != nil {
		return []ingressTest{}, err
	}

	return tests, nil
}

func (h *nginxControllerTester) getName() string {
	return h.name
}

func (h *nginxControllerTester) start(namespace string) (err error) {

	// Create a replication controller with the given configuration.
	rc := rcFromManifest(h.cfg)
	rc.Namespace = namespace
	rc.Spec.Template.Labels["name"] = rc.Name

	// Add the --namespace arg.
	// TODO: Remove this when we have proper namespace support.
	for i, c := range rc.Spec.Template.Spec.Containers {
		rc.Spec.Template.Spec.Containers[i].Args = append(
			c.Args, fmt.Sprintf("--namespace=%v", namespace))
		framework.Logf("Container args %+v", rc.Spec.Template.Spec.Containers[i].Args)
	}

	rc, err = h.client.ReplicationControllers(rc.Namespace).Create(rc)
	if err != nil {
		return
	}
	if err = framework.WaitForRCPodsRunning(h.client, namespace, rc.Name); err != nil {
		return
	}
	h.rcName = rc.Name
	h.rcNamespace = rc.Namespace

	// Find the pods of the rc we just created.
	labelSelector := labels.SelectorFromSet(
		labels.Set(map[string]string{"name": h.rcName}))
	options := api.ListOptions{LabelSelector: labelSelector}
	pods, err := h.client.Pods(h.rcNamespace).List(options)
	if err != nil {
		return err
	}

	// Find the external addresses of the nodes the pods are running on.
	for _, p := range pods.Items {
		wait.Poll(pollInterval, framework.ServiceRespondingTimeout, func() (bool, error) {
			address, err := framework.GetHostExternalAddress(h.client, &p)
			if err != nil {
				framework.Logf("%v", err)
				return false, nil
			}
			h.address = append(h.address, address)
			return true, nil
		})
	}
	if len(h.address) == 0 {
		return fmt.Errorf("No external ips found for loadbalancer %v", h.getName())
	}
	return nil
}

func (h *nginxControllerTester) stop() error {
	return h.client.ReplicationControllers(h.rcNamespace).Delete(h.rcName)
}

func (h *nginxControllerTester) lookup(ingressKey string) string {
	// The address of a service is the address of the lb/servicename, currently.
	return fmt.Sprintf("http://%v/%v", h.address[0], ingressKey)
}

var _ = framework.KubeDescribe("IngressController [Feature:IngressController]", func() {
	// These variables are initialized after framework's beforeEach.
	var ns string
	var repoRoot string
	var client *client.Client

	f := framework.NewDefaultFramework("ingress-controller")

	BeforeEach(func() {
		client = f.Client
		ns = f.Namespace.Name
		repoRoot = framework.TestContext.RepoRoot
	})

	It("should PASS Ingress suite", func() {
		for _, t := range getIngressControllers(repoRoot, client) {
			By(fmt.Sprintf("Starting loadbalancer controller %v in namespace %v", t.getName(), ns))
			Expect(t.start(ns)).NotTo(HaveOccurred())

			tests, err := getTestCases(repoRoot)
			Expect(err).NotTo(HaveOccurred())
			for _, test := range tests {
				By(fmt.Sprintf("Starting test %v in namespace %v", test.Name, ns))

				By(fmt.Sprintf("creating required resources for the test case"))
				for _, svc := range test.Requirements.Services {
					By(fmt.Sprintf("creating service %v required for test", svc.Name))
					svcFile := filepath.Join(repoRoot, "test", "e2e", "testing-manifests", "ingress", svc.Filename)
					svcFromManifest(svcFile)
				}

				for _, ing := range test.Requirements.Ingress {
					By(fmt.Sprintf("creating ingress rule %v required for test", ing.Name))
					ingFile := filepath.Join(repoRoot, "test", "e2e", "testing-manifests", "ingress", ing.Filename)
					ingressFromManifest(ingFile)
				}

				By(fmt.Sprintf("creating required resources for the test case"))
				for _, check := range test.Checks {
					httpClient := &http.Client{}
					res, err := simpleRequest(httpClient, check.Request.URL, "", check.Request.Method, check.Request.Headers)
					Expect(err).NotTo(HaveOccurred())
					Expect(res.StatusCode).To(Equal(check.Response.StatusCode))
					Expect(res.Body).Should(ContainSubstring(check.Response.Body))
					Expect(res.Headers).Should(BeEquivalentTo(check.Response.Headers))
				}
			}

			Expect(t.stop()).NotTo(HaveOccurred())
		}
	})
})

// simpleRequest executes a request on the given url
func simpleRequest(c *http.Client, url, host, method string, headers map[string]string) (*httpResponse, error) {
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		return nil, err
	}
	req.Host = host
	for hk, hv := range headers {
		req.Header.Add(hk, hv)
	}

	res, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	rawBody, err := ioutil.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	body := string(rawBody)

	return &httpResponse{
		StatusCode: res.StatusCode,
		Body:       body,
		Headers:    res.Header,
	}, err
}

// ingressFromManifest reads a .json/yaml file and returns the Ingress in it.
func ingressFromManifest(fileName string) *extensions.Ingress {
	var ing extensions.Ingress
	framework.Logf("Parsing ingress from %v", fileName)
	data, err := ioutil.ReadFile(fileName)
	Expect(err).NotTo(HaveOccurred())

	json, err := utilyaml.ToJSON(data)
	Expect(err).NotTo(HaveOccurred())

	Expect(runtime.DecodeInto(api.Codecs.UniversalDecoder(), json, &ing)).NotTo(HaveOccurred())
	return &ing
}
