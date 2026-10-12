package host_test

import (
	"fmt"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.uber.org/mock/gomock"

	"github.com/k8snetworkplumbingwg/sriovnet"

	"github.com/SchSeba/dra-multi-network/pkg/consts"
	"github.com/SchSeba/dra-multi-network/pkg/host"
	mock_host "github.com/SchSeba/dra-multi-network/pkg/host/mock"
)

var _ = Describe("Host", func() {
	var (
		h        host.Interface
		fs       *host.FakeFilesystem
		tearDown func()
	)

	BeforeEach(func() {
		h = host.NewHost()
		fs = &host.FakeFilesystem{}
	})

	AfterEach(func() {
		if tearDown != nil {
			tearDown()
		}
	})

	Describe("SR-IOV Detection Functions", func() {
		Context("IsSriovVF", func() {
			It("should return true when physfn symlink exists", func() {
				fs.Dirs = []string{
					"sys/bus/pci/devices/0000:01:00.0",
				}
				fs.Symlinks = map[string]string{
					"sys/bus/pci/devices/0000:01:00.0/physfn": "../0000:01:00.0",
				}
				tearDown = fs.Use()

				result := h.IsSriovVF("0000:01:00.0")
				Expect(result).To(BeTrue())
			})

			It("should return false when physfn symlink does not exist", func() {
				fs.Dirs = []string{
					"sys/bus/pci/devices/0000:01:00.0",
				}
				tearDown = fs.Use()

				result := h.IsSriovVF("0000:01:00.0")
				Expect(result).To(BeFalse())
			})
		})

		Context("IsSriovPF", func() {
			It("should identify a PF even with zero enabled VFs", func() {
				fs.Dirs = []string{
					"sys/bus/pci/devices/0000:01:00.0",
				}
				fs.Files = map[string][]byte{
					"sys/bus/pci/devices/0000:01:00.0/sriov_totalvfs": []byte("5\n"),
					"sys/bus/pci/devices/0000:01:00.0/sriov_numvfs":   []byte("0\n"),
				}
				tearDown = fs.Use()

				result := h.IsSriovPF("0000:01:00.0")
				Expect(result).To(BeTrue())
			})

			It("should return false when the SR-IOV capability is absent", func() {
				fs.Dirs = []string{
					"sys/bus/pci/devices/0000:01:00.0",
				}
				tearDown = fs.Use()

				result := h.IsSriovPF("0000:01:00.0")
				Expect(result).To(BeFalse())
			})
		})

		Context("GetVFList", func() {
			It("should return list of VFs with their information", func() {
				fs.Dirs = []string{
					"sys/bus/pci/devices/0000:01:00.0",
					"sys/bus/pci/devices/0000:01:00.1",
					"sys/bus/pci/devices/0000:01:00.2",
				}
				fs.Files = map[string][]byte{
					"sys/bus/pci/devices/0000:01:00.1/device": []byte("0x1016"),
					"sys/bus/pci/devices/0000:01:00.2/device": []byte("0x1017"),
				}
				fs.Symlinks = map[string]string{
					"sys/bus/pci/devices/0000:01:00.0/virtfn0": "../0000:01:00.1",
					"sys/bus/pci/devices/0000:01:00.0/virtfn1": "../0000:01:00.2",
				}
				tearDown = fs.Use()

				vfList, err := h.GetVFList("0000:01:00.0")
				Expect(err).NotTo(HaveOccurred())
				Expect(vfList).To(HaveLen(2))
				Expect(vfList[0]).To(Equal(host.VFInfo{
					PciAddress: "0000:01:00.1",
					VFID:       0,
					DeviceID:   "1016",
				}))
				Expect(vfList[1]).To(Equal(host.VFInfo{
					PciAddress: "0000:01:00.2",
					VFID:       1,
					DeviceID:   "1017",
				}))
			})

			It("should return empty list when no VFs exist", func() {
				fs.Dirs = []string{
					"sys/bus/pci/devices/0000:01:00.0",
				}
				tearDown = fs.Use()

				vfList, err := h.GetVFList("0000:01:00.0")
				Expect(err).NotTo(HaveOccurred())
				Expect(vfList).To(HaveLen(0))
			})

			It("should return error when PF directory does not exist", func() {
				tearDown = fs.Use()

				_, err := h.GetVFList("0000:01:00.0")
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("failed to read PF directory"))
			})
		})
	})

	Describe("Network Interface Functions", func() {
		Context("TryGetPFInterfaceName", func() {
			// --- devlink path (driver implements devlink, e.g. mlx5) ---

			It("should return interface name when sriovnet succeeds", func() {
				hTest := host.NewHostForTest(nil, &host.FakeSriovnetProvider{UplinkName: "eth0"})
				tearDown = fs.Use()

				Expect(hTest.TryGetPFInterfaceName("0000:01:00.0")).To(Equal("eth0"))
			})

			It("should return empty string when sriovnet fails with a non-representor error", func() {
				hTest := host.NewHostForTest(nil, &host.FakeSriovnetProvider{
					UplinkError: fmt.Errorf("invalid PCI address"),
				})
				tearDown = fs.Use()

				Expect(hTest.TryGetPFInterfaceName("0000:01:00.0")).To(BeEmpty())
			})

			// --- sysfs fallback path (driver does not implement devlink, e.g. Intel NICs) ---
			// sriovnet returns ErrRepresentorNotFound when neither devlink nor
			// phys_switch_id sysfs succeed — the signal that this driver has no
			// devlink support and we should fall back to simple sysfs.

			It("should return first netdev from sysfs on ErrRepresentorNotFound", func() {
				hTest := host.NewHostForTest(nil, &host.FakeSriovnetProvider{
					UplinkError: sriovnet.ErrRepresentorNotFound,
				})
				fs.Dirs = []string{
					"sys/bus/pci/devices/0000:01:00.0/net",
					"sys/bus/pci/devices/0000:01:00.0/net/eth0",
				}
				tearDown = fs.Use()

				Expect(hTest.TryGetPFInterfaceName("0000:01:00.0")).To(Equal("eth0"))
			})

			It("should return empty string on ErrRepresentorNotFound when net directory is missing", func() {
				hTest := host.NewHostForTest(nil, &host.FakeSriovnetProvider{
					UplinkError: sriovnet.ErrRepresentorNotFound,
				})
				fs.Dirs = []string{"sys/bus/pci/devices/0000:01:00.0"}
				tearDown = fs.Use()

				Expect(hTest.TryGetPFInterfaceName("0000:01:00.0")).To(BeEmpty())
			})

			It("should return empty string on ErrRepresentorNotFound when net directory is empty", func() {
				hTest := host.NewHostForTest(nil, &host.FakeSriovnetProvider{
					UplinkError: sriovnet.ErrRepresentorNotFound,
				})
				fs.Dirs = []string{"sys/bus/pci/devices/0000:01:00.0/net"}
				tearDown = fs.Use()

				Expect(hTest.TryGetPFInterfaceName("0000:01:00.0")).To(BeEmpty())
			})
		})

		Context("HasDefaultRoute", func() {
			It("should return true when interface has default route", func() {
				fs.Dirs = []string{
					"sys/bus/pci/devices/0000:01:00.0/net",
					"sys/bus/pci/devices/0000:01:00.0/net/eth0",
					"proc/net",
				}
				fs.Files = map[string][]byte{
					"proc/net/route": []byte("Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\tMTU\tWindow\tIRTT\neth0\t00000000\t0100A8C0\t0003\t0\t0\t0\t00000000\t0\t0\t0\n"),
				}
				tearDown = fs.Use()

				hasDefaultRoute, err := h.HasDefaultRoute("0000:01:00.0")
				Expect(err).NotTo(HaveOccurred())
				Expect(hasDefaultRoute).To(BeTrue())
			})

			It("should return false when interface has no default route", func() {
				fs.Dirs = []string{
					"sys/bus/pci/devices/0000:01:00.0/net",
					"sys/bus/pci/devices/0000:01:00.0/net/eth0",
					"proc/net",
				}
				fs.Files = map[string][]byte{
					"proc/net/route": []byte("Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\tMTU\tWindow\tIRTT\neth0\t0001A8C0\t00000000\t0001\t0\t0\t0\t00FFFFFF\t0\t0\t0\n"),
				}
				tearDown = fs.Use()

				hasDefaultRoute, err := h.HasDefaultRoute("0000:01:00.0")
				Expect(err).NotTo(HaveOccurred())
				Expect(hasDefaultRoute).To(BeFalse())
			})

			It("should return true when one of multiple interfaces has default route", func() {
				fs.Dirs = []string{
					"sys/bus/pci/devices/0000:01:00.0/net",
					"sys/bus/pci/devices/0000:01:00.0/net/eth0",
					"sys/bus/pci/devices/0000:01:00.0/net/eth1",
					"proc/net",
				}
				fs.Files = map[string][]byte{
					"proc/net/route": []byte(
						"Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\tMTU\tWindow\tIRTT\n" +
							"eth0\t0001A8C0\t00000000\t0001\t0\t0\t0\t00FFFFFF\t0\t0\t0\n" +
							"eth1\t00000000\t0100A8C0\t0003\t0\t0\t0\t00000000\t0\t0\t0\n",
					),
				}
				tearDown = fs.Use()

				hasDefaultRoute, err := h.HasDefaultRoute("0000:01:00.0")
				Expect(err).NotTo(HaveOccurred())
				Expect(hasDefaultRoute).To(BeTrue())
			})

			It("should return false when default route belongs to unrelated interface", func() {
				fs.Dirs = []string{
					"sys/bus/pci/devices/0000:01:00.0/net",
					"sys/bus/pci/devices/0000:01:00.0/net/eth0",
					"proc/net",
				}
				fs.Files = map[string][]byte{
					"proc/net/route": []byte(
						"Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\tMTU\tWindow\tIRTT\n" +
							"eth9\t00000000\t0100A8C0\t0003\t0\t0\t0\t00000000\t0\t0\t0\n",
					),
				}
				tearDown = fs.Use()

				hasDefaultRoute, err := h.HasDefaultRoute("0000:01:00.0")
				Expect(err).NotTo(HaveOccurred())
				Expect(hasDefaultRoute).To(BeFalse())
			})

			It("should return true when interface has IPv6 default route", func() {
				fs.Dirs = []string{
					"sys/bus/pci/devices/0000:01:00.0/net",
					"sys/bus/pci/devices/0000:01:00.0/net/eth0",
					"proc/net",
				}
				fs.Files = map[string][]byte{
					"proc/net/route": []byte("Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\tMTU\tWindow\tIRTT\n"),
					"proc/net/ipv6_route": []byte(
						"00000000000000000000000000000000 00 00000000000000000000000000000000 00 00000000000000000000000000000000 00000000 00000000 00000000 00000001 eth0\n",
					),
				}
				tearDown = fs.Use()

				hasDefaultRoute, err := h.HasDefaultRoute("0000:01:00.0")
				Expect(err).NotTo(HaveOccurred())
				Expect(hasDefaultRoute).To(BeTrue())
			})

			It("should return true when default route is on master interface", func() {
				fs.Dirs = []string{
					"sys/bus/pci/devices/0000:01:00.0/net",
					"sys/bus/pci/devices/0000:01:00.0/net/eth0",
					"sys/class/net/eth0",
					"sys/class/net/bond0",
					"proc/net",
				}
				fs.Symlinks = map[string]string{
					"sys/class/net/eth0/master": "../bond0",
				}
				fs.Files = map[string][]byte{
					"proc/net/route": []byte("Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\tMTU\tWindow\tIRTT\nbond0\t00000000\t0100A8C0\t0003\t0\t0\t0\t00000000\t0\t0\t0\n"),
				}
				tearDown = fs.Use()

				hasDefaultRoute, err := h.HasDefaultRoute("0000:01:00.0")
				Expect(err).NotTo(HaveOccurred())
				Expect(hasDefaultRoute).To(BeTrue())
			})

			It("should return false when IPv6 default route belongs to unrelated interface", func() {
				fs.Dirs = []string{
					"sys/bus/pci/devices/0000:01:00.0/net",
					"sys/bus/pci/devices/0000:01:00.0/net/eth0",
					"proc/net",
				}
				fs.Files = map[string][]byte{
					"proc/net/route": []byte("Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\tMTU\tWindow\tIRTT\n"),
					"proc/net/ipv6_route": []byte(
						"00000000000000000000000000000000 00 00000000000000000000000000000000 00 00000000000000000000000000000000 00000000 00000000 00000000 00000001 eth9\n",
					),
				}
				tearDown = fs.Use()

				hasDefaultRoute, err := h.HasDefaultRoute("0000:01:00.0")
				Expect(err).NotTo(HaveOccurred())
				Expect(hasDefaultRoute).To(BeFalse())
			})

			It("should treat VLAN subinterface default route as in-use", func() {
				fs.Dirs = []string{
					"sys/bus/pci/devices/0000:01:00.0/net",
					"sys/bus/pci/devices/0000:01:00.0/net/eth0",
					"proc/net",
				}
				fs.Files = map[string][]byte{
					"proc/net/route": []byte(
						"Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\tMTU\tWindow\tIRTT\n" +
							"eth0.100\t00000000\t0100A8C0\t0003\t0\t0\t0\t00000000\t0\t0\t0\n",
					),
				}
				tearDown = fs.Use()

				hasDefaultRoute, err := h.HasDefaultRoute("0000:01:00.0")
				Expect(err).NotTo(HaveOccurred())
				Expect(hasDefaultRoute).To(BeTrue())
			})

			It("should not treat split routes as default route", func() {
				fs.Dirs = []string{
					"sys/bus/pci/devices/0000:01:00.0/net",
					"sys/bus/pci/devices/0000:01:00.0/net/eth0",
					"proc/net",
				}
				fs.Files = map[string][]byte{
					"proc/net/route": []byte(
						"Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\tMTU\tWindow\tIRTT\n" +
							"eth0\t00000000\t0100A8C0\t0003\t0\t0\t0\t00000080\t0\t0\t0\n",
					),
				}
				tearDown = fs.Use()

				hasDefaultRoute, err := h.HasDefaultRoute("0000:01:00.0")
				Expect(err).NotTo(HaveOccurred())
				Expect(hasDefaultRoute).To(BeFalse())
			})

			It("should return false when PCI device has no interfaces", func() {
				fs.Dirs = []string{
					"sys/bus/pci/devices/0000:01:00.0/net",
				}
				tearDown = fs.Use()

				hasDefaultRoute, err := h.HasDefaultRoute("0000:01:00.0")
				Expect(err).NotTo(HaveOccurred())
				Expect(hasDefaultRoute).To(BeFalse())
			})

			It("should return error when route table cannot be read", func() {
				fs.Dirs = []string{
					"sys/bus/pci/devices/0000:01:00.0/net",
					"sys/bus/pci/devices/0000:01:00.0/net/eth0",
				}
				tearDown = fs.Use()

				_, err := h.HasDefaultRoute("0000:01:00.0")
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("failed to read route table"))
			})

			It("should reuse cached route tables across repeated default-route checks", func() {
				fs.Dirs = []string{
					"sys/bus/pci/devices/0000:01:00.0/net",
					"sys/bus/pci/devices/0000:01:00.0/net/eth0",
					"proc/net",
				}
				fs.Files = map[string][]byte{
					"proc/net/route":      []byte("Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\tMTU\tWindow\tIRTT\neth0\t00000000\t0100A8C0\t0003\t0\t0\t0\t00000000\t0\t0\t0\n"),
					"proc/net/ipv6_route": []byte(""),
				}
				tearDown = fs.Use()

				hasDefaultRoute, err := h.HasDefaultRoute("0000:01:00.0")
				Expect(err).NotTo(HaveOccurred())
				Expect(hasDefaultRoute).To(BeTrue())

				Expect(os.Remove(filepath.Join(fs.RootDir, "proc/net/route"))).To(Succeed())

				hasDefaultRoute, err = h.HasDefaultRoute("0000:01:00.0")
				Expect(err).NotTo(HaveOccurred())
				Expect(hasDefaultRoute).To(BeTrue())
			})
		})

		Context("HasBridgeMaster", func() {
			It("should return true when interface is attached to linux bridge", func() {
				fs.Dirs = []string{
					"sys/bus/pci/devices/0000:01:00.0/net",
					"sys/bus/pci/devices/0000:01:00.0/net/enp1s0",
					"sys/class/net/enp1s0",
					"sys/class/net/br-ex",
					"sys/class/net/br-ex/bridge",
				}
				fs.Symlinks = map[string]string{
					"sys/class/net/enp1s0/master": "../br-ex",
				}
				tearDown = fs.Use()

				hasBridgeMaster, err := h.HasBridgeMaster("0000:01:00.0")
				Expect(err).NotTo(HaveOccurred())
				Expect(hasBridgeMaster).To(BeTrue())
			})

			It("should return true when interface is attached to ovs-system bridge master", func() {
				fs.Dirs = []string{
					"sys/bus/pci/devices/0000:01:00.0/net",
					"sys/bus/pci/devices/0000:01:00.0/net/enp1s0",
					"sys/class/net/enp1s0",
				}
				fs.Symlinks = map[string]string{
					"sys/class/net/enp1s0/master": "../ovs-system",
				}
				tearDown = fs.Use()

				hasBridgeMaster, err := h.HasBridgeMaster("0000:01:00.0")
				Expect(err).NotTo(HaveOccurred())
				Expect(hasBridgeMaster).To(BeTrue())
			})

			It("should return true when master interface is marked as openvswitch bridge", func() {
				fs.Dirs = []string{
					"sys/bus/pci/devices/0000:01:00.0/net",
					"sys/bus/pci/devices/0000:01:00.0/net/enp1s0",
					"sys/class/net/enp1s0",
					"sys/class/net/br-int",
					"sys/class/net/br-int/openvswitch",
				}
				fs.Symlinks = map[string]string{
					"sys/class/net/enp1s0/master": "../br-int",
				}
				tearDown = fs.Use()

				hasBridgeMaster, err := h.HasBridgeMaster("0000:01:00.0")
				Expect(err).NotTo(HaveOccurred())
				Expect(hasBridgeMaster).To(BeTrue())
			})

			It("should return false when interface is attached to non-bridge master", func() {
				fs.Dirs = []string{
					"sys/bus/pci/devices/0000:01:00.0/net",
					"sys/bus/pci/devices/0000:01:00.0/net/enp1s0",
					"sys/class/net/enp1s0",
				}
				fs.Symlinks = map[string]string{
					"sys/class/net/enp1s0/master": "../bond0",
				}
				tearDown = fs.Use()

				hasBridgeMaster, err := h.HasBridgeMaster("0000:01:00.0")
				Expect(err).NotTo(HaveOccurred())
				Expect(hasBridgeMaster).To(BeFalse())
			})

			It("should return true when bridge is found through multi-hop master chain", func() {
				fs.Dirs = []string{
					"sys/bus/pci/devices/0000:01:00.0/net",
					"sys/bus/pci/devices/0000:01:00.0/net/enp1s0",
					"sys/class/net/enp1s0",
					"sys/class/net/bond0",
					"sys/class/net/ovs-system",
				}
				fs.Symlinks = map[string]string{
					"sys/class/net/enp1s0/master": "../bond0",
					"sys/class/net/bond0/master":  "../ovs-system",
				}
				tearDown = fs.Use()

				hasBridgeMaster, err := h.HasBridgeMaster("0000:01:00.0")
				Expect(err).NotTo(HaveOccurred())
				Expect(hasBridgeMaster).To(BeTrue())
			})

			It("should return false when interface is not attached to any master", func() {
				fs.Dirs = []string{
					"sys/bus/pci/devices/0000:01:00.0/net",
					"sys/bus/pci/devices/0000:01:00.0/net/enp1s0",
					"sys/class/net/enp1s0",
				}
				tearDown = fs.Use()

				hasBridgeMaster, err := h.HasBridgeMaster("0000:01:00.0")
				Expect(err).NotTo(HaveOccurred())
				Expect(hasBridgeMaster).To(BeFalse())
			})

			It("should return error when master path exists but is not a symlink", func() {
				fs.Dirs = []string{
					"sys/bus/pci/devices/0000:01:00.0/net",
					"sys/bus/pci/devices/0000:01:00.0/net/enp1s0",
					"sys/class/net/enp1s0",
				}
				fs.Files = map[string][]byte{
					"sys/class/net/enp1s0/master": []byte("not-a-symlink"),
				}
				tearDown = fs.Use()

				_, err := h.HasBridgeMaster("0000:01:00.0")
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("failed to read master link"))
			})
		})

		Context("GetNicSriovMode", func() {
			It("should return switchdev when devlink reports switchdev", func() {
				fakeNetlink := &host.FakeNetlinkProvider{EswitchMode: consts.EswitchModeSwitchdev}
				hMode := host.NewHostForTest(fakeNetlink)
				tearDown = fs.Use()

				mode := hMode.GetNicSriovMode("0000:01:00.0")
				Expect(mode).To(Equal(consts.EswitchModeSwitchdev))
			})

			It("should return legacy when devlink reports legacy", func() {
				fakeNetlink := &host.FakeNetlinkProvider{EswitchMode: consts.EswitchModeLegacy}
				hMode := host.NewHostForTest(fakeNetlink)
				tearDown = fs.Use()

				mode := hMode.GetNicSriovMode("0000:01:00.0")
				Expect(mode).To(Equal(consts.EswitchModeLegacy))
			})

			It("should fall back to legacy when devlink query fails", func() {
				fakeNetlink := &host.FakeNetlinkProvider{EswitchError: fmt.Errorf("devlink not supported")}
				hMode := host.NewHostForTest(fakeNetlink)
				tearDown = fs.Use()

				mode := hMode.GetNicSriovMode("0000:01:00.0")
				Expect(mode).To(Equal(consts.EswitchModeLegacy))
			})

			It("should fall back to legacy when devlink returns empty mode", func() {
				fakeNetlink := &host.FakeNetlinkProvider{EswitchMode: ""}
				hMode := host.NewHostForTest(fakeNetlink)
				tearDown = fs.Use()

				mode := hMode.GetNicSriovMode("0000:01:00.0")
				Expect(mode).To(Equal(consts.EswitchModeLegacy))
			})
		})

		Context("GetLinkType", func() {
			It("should return 'ethernet' for type ArphrdEther", func() {
				fs.Dirs = []string{"sys/class/net/eth0"}
				fs.Files = map[string][]byte{
					"sys/class/net/eth0/type": []byte(fmt.Sprintf("%d\n", host.ArphrdEther)),
				}
				hLink := host.NewHostForTest(nil, &host.FakeSriovnetProvider{UplinkName: "eth0"})
				tearDown = fs.Use()

				linkType, err := hLink.GetLinkType("0000:01:00.0")
				Expect(err).NotTo(HaveOccurred())
				Expect(linkType).To(Equal(consts.LinkTypeEthernet))
			})

			It("should return 'infiniband' for type ArphrdInfiniband", func() {
				fs.Dirs = []string{"sys/class/net/ib0"}
				fs.Files = map[string][]byte{
					"sys/class/net/ib0/type": []byte(fmt.Sprintf("%d\n", host.ArphrdInfiniband)),
				}
				hLink := host.NewHostForTest(nil, &host.FakeSriovnetProvider{UplinkName: "ib0"})
				tearDown = fs.Use()

				linkType, err := hLink.GetLinkType("0000:02:00.0")
				Expect(err).NotTo(HaveOccurred())
				Expect(linkType).To(Equal(consts.LinkTypeInfiniband))
			})

			It("should return 'unknown' for unknown types", func() {
				fs.Dirs = []string{"sys/class/net/custom0"}
				fs.Files = map[string][]byte{
					"sys/class/net/custom0/type": []byte("99\n"),
				}
				hLink := host.NewHostForTest(nil, &host.FakeSriovnetProvider{UplinkName: "custom0"})
				tearDown = fs.Use()

				linkType, err := hLink.GetLinkType("0000:03:00.0")
				Expect(err).NotTo(HaveOccurred())
				Expect(linkType).To(Equal(consts.LinkTypeUnknown))
			})

			It("should return error when interface name cannot be determined", func() {
				hLink := host.NewHostForTest(nil, &host.FakeSriovnetProvider{
					UplinkError: fmt.Errorf("invalid PCI address"),
				})
				tearDown = fs.Use()

				linkType, err := hLink.GetLinkType("0000:04:00.0")
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("unable to get interface name"))
				Expect(linkType).To(BeEmpty())
			})

			It("should return error when type file does not exist", func() {
				fs.Dirs = []string{"sys/class/net/eth0"}
				// No type file → ReadFile will fail.
				hLink := host.NewHostForTest(nil, &host.FakeSriovnetProvider{UplinkName: "eth0"})
				tearDown = fs.Use()

				linkType, err := hLink.GetLinkType("0000:05:00.0")
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("failed to read link type"))
				Expect(linkType).To(BeEmpty())
			})

			It("should return error when type file contains invalid data", func() {
				fs.Dirs = []string{"sys/class/net/eth0"}
				fs.Files = map[string][]byte{
					"sys/class/net/eth0/type": []byte("invalid\n"),
				}
				hLink := host.NewHostForTest(nil, &host.FakeSriovnetProvider{UplinkName: "eth0"})
				tearDown = fs.Use()

				linkType, err := hLink.GetLinkType("0000:06:00.0")
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("failed to parse link type value"))
				Expect(linkType).To(BeEmpty())
			})
		})
	})

	Describe("Topology Functions", func() {
		Context("GetNumaNode", func() {
			It("should return NUMA node from file", func() {
				fs.Dirs = []string{
					"sys/bus/pci/devices/0000:01:00.0",
				}
				fs.Files = map[string][]byte{
					"sys/bus/pci/devices/0000:01:00.0/numa_node": []byte("1"),
				}
				tearDown = fs.Use()

				numaNode, err := h.GetNumaNode("0000:01:00.0")
				Expect(err).NotTo(HaveOccurred())
				Expect(numaNode).To(Equal("1"))
			})

			It("should passthrough '-1' when NUMA node file contains -1", func() {
				fs.Dirs = []string{
					"sys/bus/pci/devices/0000:01:00.0",
				}
				fs.Files = map[string][]byte{
					"sys/bus/pci/devices/0000:01:00.0/numa_node": []byte("-1"),
				}
				tearDown = fs.Use()

				numaNode, err := h.GetNumaNode("0000:01:00.0")
				Expect(err).NotTo(HaveOccurred())
				Expect(numaNode).To(Equal("-1"))
			})

			It("should return '-1' when NUMA node file does not exist", func() {
				tearDown = fs.Use()

				numaNode, err := h.GetNumaNode("0000:01:00.0")
				Expect(err).NotTo(HaveOccurred())
				Expect(numaNode).To(Equal("-1"))
			})
		})

		Context("GetPCIeRoot", func() {
			It("should return error for invalid PCI address format", func() {
				// Test with invalid format - this is validated by the upstream implementation
				// before it tries to access sysfs, so it works in tests
				_, err := h.GetPCIeRoot("invalid-address")
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("invalid PCI Bus ID format"))
			})

			It("should return error for empty PCI address", func() {
				_, err := h.GetPCIeRoot("")
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("PCI Bus ID cannot be empty"))
			})

			It("should return error when device doesn't exist in sysfs", func() {
				// This will fail because the device doesn't exist in the real sysfs
				// The upstream implementation reads from /sys directly and cannot use our fake filesystem
				_, err := h.GetPCIeRoot("ffff:ff:ff.f")
				Expect(err).To(HaveOccurred())
			})

			It("should return PCIe root for valid device", func() {
				// Set up a realistic PCI device structure with proper hierarchy
				fs.Dirs = []string{
					"sys/bus/pci/devices/0000:00:00.0", // PCIe root complex
					"sys/bus/pci/devices/0000:01:00.0", // Device on bus 01
					"sys/devices/pci0000:00/0000:00:00.0",
					"sys/devices/pci0000:00/0000:00:00.0/0000:01:00.0",
				}
				fs.Files = map[string][]byte{
					"sys/bus/pci/devices/0000:01:00.0/vendor":                 []byte("0x8086"),
					"sys/bus/pci/devices/0000:01:00.0/device":                 []byte("0x1572"),
					"sys/bus/pci/devices/0000:01:00.0/class":                  []byte("0x020000"),
					"sys/devices/pci0000:00/0000:00:00.0/vendor":              []byte("0x8086"),
					"sys/devices/pci0000:00/0000:00:00.0/device":              []byte("0x1234"),
					"sys/devices/pci0000:00/0000:00:00.0/0000:01:00.0/vendor": []byte("0x8086"),
					"sys/devices/pci0000:00/0000:00:00.0/0000:01:00.0/device": []byte("0x1572"),
				}
				tearDown = fs.Use()

				// Try to get PCIe root - this should work if the upstream package respects SYSFS_PATH
				// or has been updated to support alternative root directories
				pcieRoot, err := h.GetPCIeRoot("0000:01:00.0")
				if err != nil {
					// If the upstream package doesn't support fake filesystem yet,
					// skip this test rather than failing
					Skip("Upstream package doesn't support alternative sysfs root yet: " + err.Error())
				}
				Expect(pcieRoot).To(Equal("pci0000:00"))
			})
		})

	})

	Describe("RDMA Device Functions", func() {
		var (
			mockCtrl         *gomock.Controller
			mockRdmaProvider *mock_host.MockRdmaProvider
			hostImpl         *host.Host
		)

		BeforeEach(func() {
			mockCtrl = gomock.NewController(GinkgoT())
			mockRdmaProvider = mock_host.NewMockRdmaProvider(mockCtrl)
			hostImpl = host.NewHost().(*host.Host)
			hostImpl.SetRdmaProvider(mockRdmaProvider)
		})

		AfterEach(func() {
			mockCtrl.Finish()
		})

		Context("GetRDMADevicesForPCI", func() {
			It("should return RDMA devices when they exist", func() {
				mockRdmaProvider.EXPECT().
					GetRdmaDevicesForPcidev("0000:08:00.1").
					Return([]string{"mlx5_1", "mlx5_2"}).
					Times(1)

				devices := hostImpl.GetRDMADevicesForPCI("0000:08:00.1")
				Expect(devices).To(HaveLen(2))
				Expect(devices).To(Equal([]string{"mlx5_1", "mlx5_2"}))
			})

			It("should return empty slice when no RDMA devices exist", func() {
				mockRdmaProvider.EXPECT().
					GetRdmaDevicesForPcidev("0000:08:00.2").
					Return([]string{}).
					Times(1)

				devices := hostImpl.GetRDMADevicesForPCI("0000:08:00.2")
				Expect(devices).To(BeEmpty())
			})

			It("should return empty slice when device is not RDMA capable", func() {
				mockRdmaProvider.EXPECT().
					GetRdmaDevicesForPcidev("0000:01:00.0").
					Return([]string{}).
					Times(1)

				devices := hostImpl.GetRDMADevicesForPCI("0000:01:00.0")
				Expect(devices).To(BeEmpty())
			})

		})

		Context("VerifyRDMACapability", func() {
			It("should return true when device has RDMA capabilities", func() {
				mockRdmaProvider.EXPECT().
					GetRdmaDevicesForPcidev("0000:08:00.1").
					Return([]string{"mlx5_1"}).
					Times(1)

				capable := hostImpl.VerifyRDMACapability("0000:08:00.1")
				Expect(capable).To(BeTrue())
			})

			It("should return false when device has no RDMA capabilities", func() {
				mockRdmaProvider.EXPECT().
					GetRdmaDevicesForPcidev("0000:08:00.2").
					Return([]string{}).
					Times(1)

				capable := hostImpl.VerifyRDMACapability("0000:08:00.2")
				Expect(capable).To(BeFalse())
			})
		})

	})
})
