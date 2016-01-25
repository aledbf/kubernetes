# kube2dns
==============

A bridge between Kubernetes and a DNS server. This will watch the kubernetes API for
changes in Services and then publish those changes to the selected DNS backend.

## Backends

- [NSD](www.nlnetlabs.nl/projects/nsd)

## Namespaces

Kubernetes namespaces become another level of the DNS hierarchy.  See the
description of `-domain` below.

## Flags

`-domain`: Set the domain under which all DNS names will be hosted.  For
example, if this is set to `kubernetes.io`, then a service named "nifty" in the
"default" namespace would be exposed through DNS as
"nifty.default.svc.kubernetes.io".

`-v`: Set logging level

`-kube_master_url`: URL of kubernetes master. Required if `--kubecfg_file` is not set.

`-kubecfg_file`: Path to kubecfg file that contains the master URL and tokens to authenticate with the master.

`-log_dir`: If non empty, write log files in this directory

`-logtostderr`: Logs to stderr instead of files

[![Analytics](https://kubernetes-site.appspot.com/UA-36037335-10/GitHub/cluster/addons/dns/kube2sky/README.md?pixel)]()
