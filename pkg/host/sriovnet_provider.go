package host

import "github.com/k8snetworkplumbingwg/sriovnet"

// SriovnetProvider wraps sriovnet library calls to allow mocking in unit tests.
type SriovnetProvider interface {
	// GetUplinkRepresentor returns the PF uplink netdev name for a given PCI
	// address (PF or VF).
	GetUplinkRepresentor(pciAddr string) (string, error)
}

type defaultSriovnetProvider struct{}

var _ SriovnetProvider = &defaultSriovnetProvider{}

func (defaultSriovnetProvider) GetUplinkRepresentor(pciAddr string) (string, error) {
	return sriovnet.GetUplinkRepresentor(pciAddr)
}
