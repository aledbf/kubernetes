package main

type nsd struct {
	data []dnsRecord
}

func newNsdInstance() dnsBackend {
	return new(nsd)
}

func (dns *nsd) Add(record dnsRecord) error {
	return nil
}

func (dns *nsd) Remove(host, ip string) error {
	return nil
}

func (nsd *nsd) Get(host string) (dnsRecord, error) {
	return dnsRecord{}, nil
}

func (nsd *nsd) reload() error {
	return nil
}
