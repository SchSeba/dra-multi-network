package host

import (
	"github.com/vishvananda/netlink"
)

// NetlinkProvider wraps netlink library calls to allow mocking in unit tests.
type NetlinkProvider interface {
	// GetDevLinkDeviceEswitchMode returns the eswitch mode ("legacy" or
	// "switchdev") for the given PF PCI address via devlink.
	GetDevLinkDeviceEswitchMode(pciAddr string) (string, error)
}

type defaultNetlinkProvider struct{}

var _ NetlinkProvider = &defaultNetlinkProvider{}

func (defaultNetlinkProvider) GetDevLinkDeviceEswitchMode(pciAddr string) (string, error) {
	dev, err := netlink.DevLinkGetDeviceByName("pci", pciAddr)
	if err != nil {
		return "", err
	}
	return dev.Attrs.Eswitch.Mode, nil
}
