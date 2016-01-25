package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/golang/glog"
	flag "github.com/spf13/pflag"
	"k8s.io/kubernetes/pkg/client/unversioned"
	kubectl_util "k8s.io/kubernetes/pkg/kubectl/cmd/util"
)

var (
	flags = flag.NewFlagSet("", flag.ContinueOnError)

	argDomain = flag.String("domain", "cluster.local", "domain under which to create names")

	cluster = flags.Bool("use-kubernetes-cluster-service", true, `If true, use the
    built in kubernetes cluster for creating the client`)

	backend = flags.String("backend", "nsd", "DNS backend to use")
)

func main() {
	clientConfig := kubectl_util.DefaultClientConfig(flags)
	flags.Parse(os.Args)

	var kubeClient *unversioned.Client
	var err error

	if *cluster {
		if kubeClient, err = unversioned.NewInCluster(); err != nil {
			glog.Fatalf("Failed to create client: %v", err)
		}
	} else {
		config, err := clientConfig.ClientConfig()
		if err != nil {
			glog.Fatalf("error connecting to the client: %v", err)
		}
		kubeClient, err = unversioned.New(config)
	}

	domain := *argDomain
	if !strings.HasSuffix(domain, ".") {
		domain = fmt.Sprintf("%s.", domain)
	}

	nsd := newNsdInstance()

	kd := kube2dns{
		domain:  domain,
		backend: nsd,
	}

	kd.endpointsStore = watchEndpoints(kubeClient, &kd)
	kd.servicesStore = watchForServices(kubeClient, &kd)
	kd.podsStore = watchPods(kubeClient, &kd)

	select {}
}
