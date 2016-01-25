# Makefile for the Docker image gcr.io/google_containers/kube2dns
# If you update this image please bump the tag value before pushing.

.PHONY: all kube2dns container push clean test

TAG = 0.1
PREFIX = gcr.io/google_containers

all: container

kube2dns: kube2dns.go
	GOOS=linux CGO_ENABLED=0 godep go build -a -installsuffix cgo --ldflags '-w' -o kube2dns

container: kube2dns
	docker build -t $(PREFIX)/kube2dns:$(TAG) .

push:
	gcloud docker push $(PREFIX)/kube2dns:$(TAG)

clean:
	rm -f kube2dns

test: clean
	godep go test -v --vmodule=*=4
