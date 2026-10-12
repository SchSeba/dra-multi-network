// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
package effects

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
)

func rdmaFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write := func(path, value string) {
		t.Helper()
		path = filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(value+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	link := func(from, to string) {
		t.Helper()
		to = filepath.Join(root, to)
		if err := os.MkdirAll(filepath.Dir(to), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(root, from), to); err != nil {
			t.Fatal(err)
		}
	}
	write("devices/pci0000:00/0000:29:00.0/auxiliary/mlx5_core.sf.4/uevent", "")
	write("devices/pci0000:00/0000:30:00.0/uevent", "")
	link("devices/pci0000:00/0000:29:00.0", "bus/pci/devices/0000:29:00.0")
	link("devices/pci0000:00/0000:30:00.0", "bus/pci/devices/0000:30:00.0")
	link("devices/pci0000:00/0000:29:00.0/auxiliary/mlx5_core.sf.4", "class/infiniband/mlx5_0/device")
	write("class/infiniband/mlx5_0/node_guid", "1122:3344:5566:7788")
	write("class/infiniband/mlx5_0/sys_image_guid", "AABB:CCDD:EEFF:0011")
	for _, port := range []string{"1", "2"} {
		base := "class/infiniband/mlx5_0/ports/" + port
		write(base+"/link_layer", "Ethernet")
		write(base+"/state", "4: ACTIVE")
		write(base+"/pkeys/0", "0xffff")
		write(base+"/gid_attrs/ndevs/0", "")
	}
	write("class/infiniband/mlx5_0/ports/1/gid_attrs/ndevs/1", "eth0")
	write("class/infiniband/mlx5_0/ports/1/gid_attrs/ndevs/2", "eth0")
	write("class/infiniband/mlx5_0/ports/2/gid_attrs/ndevs/1", "eth1")
	link("devices/pci0000:00/0000:29:00.0", "class/net/eth0/device")
	link("devices/pci0000:00/0000:30:00.0", "class/net/eth1/device")
	write("class/net/lo/type", "772")
	write("class/infiniband_verbs/abi_version", "6")
	write("class/infiniband_verbs/uverbs0/ibdev", "mlx5_0")
	link("devices/pci0000:00/0000:29:00.0/auxiliary/mlx5_core.sf.4", "class/infiniband_verbs/uverbs0/device")
	write("class/infiniband_verbs/uverbs0/dev", "231:192")
	link("class/infiniband_verbs/uverbs0", "dev/char/231:192")
	write("class/infiniband_verbs/uverbs1/ibdev", "another_hca")
	write("class/infiniband_mad/umad0/ibdev", "mlx5_0")
	link("devices/pci0000:00/0000:29:00.0/auxiliary/mlx5_core.sf.4", "class/infiniband_mad/umad0/device")
	write("class/infiniband_mad/umad0/dev", "231:0")
	write("class/infiniband_mad/umad0/port", "1")
	link("class/infiniband_mad/umad0", "dev/char/231:0")
	write("class/infiniband_cm/rdma_cm/dev", "10:59")
	return root
}

func TestRDMANamespaceIdentityAndForeignCharacterOwner(t *testing.T) {
	root := rdmaFixture(t)
	devices, err := discoverRDMA(root)
	if err != nil {
		t.Fatal(err)
	}
	links := []*netlink.RdmaLink{{Attrs: netlink.RdmaLinkAttrs{Index: 7, Name: "mlx5_0", NodeGuid: "88:77:66:55:44:33:22:11", SysImageGuid: "11:00:ff:ee:dd:cc:bb:aa", NumPorts: 2}}}
	ns := NamespaceIdentity{Device: 4, Inode: 5}
	if err := matchRDMALinks(devices, links, ns); err != nil || devices[0].Index != 7 || *devices[0].Namespace != ns {
		t.Fatalf("namespace identity: %+v %v", devices, err)
	}
	links[0].Attrs.NodeGuid = "11:22:33:44:55:66:77:88"
	if err := matchRDMALinks(devices, links, ns); err == nil {
		t.Fatal("accepted incorrect GUID byte order")
	}
	if err := matchRDMALinks(devices, nil, ns); err == nil {
		t.Fatal("accepted an HCA absent from the target namespace")
	}
	if err := matchRDMALinks(nil, append(links, links[0]), ns); err == nil {
		t.Fatal("accepted ambiguous RDMA identity")
	}
	for _, parent := range []string{"devices/pci0000:00/0000:30:00.0", "devices/pci0000:00/0000:29:00.0"} {
		path := filepath.Join(root, "class/infiniband_verbs/uverbs0/device")
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(root, parent), path); err != nil {
			t.Fatal(err)
		}
		if _, err := discoverRDMA(root); err == nil || !strings.Contains(err.Error(), "E_RDMA_CHAR_OWNER") {
			t.Fatalf("accepted name-matching character node from another physical parent: %v", err)
		}
	}
}

func TestRDMANamespaceSysfsLifecycle(t *testing.T) {
	if os.Getenv("DRA_NATIVE_TEST") != "1" {
		t.Skip("requires root and namespace/mount capabilities; DRA_NATIVE_TEST=1")
	}
	original, err := netns.Get()
	if err != nil {
		t.Fatal(err)
	}
	defer original.Close()
	if _, err := DiscoverRDMAInNamespace(original); err != nil {
		t.Fatalf("current namespace discovery: %v", err)
	}
	path := filepath.Join(t.TempDir(), "rdma-sandbox")
	if err := CreateStagingNamespace(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := Unpin(path); err != nil {
			t.Error(err)
		}
	})
	ns, err := netns.GetFromPath(path)
	if err != nil {
		t.Fatal(err)
	}
	defer ns.Close()
	want, err := IdentifyNamespace(path)
	if err != nil {
		t.Fatal(err)
	}
	var mounted string
	if err := withRDMASysfs(ns, func(root string) error {
		mounted = root
		var stat unix.Stat_t
		if err := unix.Stat(fmt.Sprintf("/proc/self/task/%d/ns/net", unix.Gettid()), &stat); err != nil {
			return err
		}
		if want != (NamespaceIdentity{Device: stat.Dev, Inode: stat.Ino}) {
			return fmt.Errorf("wrong reader namespace")
		}
		var fs unix.Statfs_t
		if err := unix.Statfs(root, &fs); err != nil {
			return err
		}
		if fs.Type != unix.SYSFS_MAGIC || fs.Flags&unix.ST_RDONLY == 0 {
			return fmt.Errorf("sysfs is not read-only")
		}
		links, err := os.ReadDir(filepath.Join(root, "class/net"))
		if err != nil {
			return err
		}
		var names []string
		for _, link := range links {
			if link.Type()&os.ModeSymlink != 0 || link.IsDir() {
				names = append(names, link.Name())
			}
		}
		if len(names) != 1 || names[0] != "lo" {
			return fmt.Errorf("sysfs belongs to another namespace: %v", names)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(mounted); !os.IsNotExist(err) {
		t.Fatalf("temporary sysfs mount retained: %v", err)
	}
	wantErr := errors.New("interrupted RDMA observation")
	if err := withRDMASysfs(ns, func(root string) error { mounted = root; return wantErr }); !errors.Is(err, wantErr) {
		t.Fatalf("lost observation error: %v", err)
	}
	if _, err := os.Stat(mounted); !os.IsNotExist(err) {
		t.Fatalf("failed observation retained its mount: %v", err)
	}
	after, err := netns.Get()
	if err != nil {
		t.Fatal(err)
	}
	defer after.Close()
	if !after.Equal(original) {
		t.Fatal("reader changed caller namespace")
	}
}

func TestRDMADiscoveryMapsEveryPortAndPrivateNode(t *testing.T) {
	root := rdmaFixture(t)
	devices, err := discoverRDMA(root)
	if err != nil || len(devices) != 1 {
		t.Fatalf("discovery: %+v %v", devices, err)
	}
	d := devices[0]
	for _, pci := range []string{"0000:29:00.0", "0000:30:00.0"} {
		if err := pciRDMAScope(pci, devices); err == nil {
			t.Fatalf("binding permitted for HCA-associated function %s", pci)
		}
	}
	if err := pciRDMAScope("0000:31:00.0", devices); err != nil {
		t.Fatal(err)
	}
	if d.PCI != "0000:29:00.0" || d.NodeGUID != "1122334455667788" || d.SystemImageGUID != "aabbccddeeff0011" || !reflect.DeepEqual(d.RelatedPCI, []string{"0000:29:00.0", "0000:30:00.0"}) || !reflect.DeepEqual(d.NetDevices, []string{"eth0", "eth1"}) {
		t.Fatalf("incorrect identity/whole device mapping: %+v", d)
	}
	if len(d.Ports) != 2 || !reflect.DeepEqual(d.Ports[0].NetDevices, []string{"eth0"}) || !reflect.DeepEqual(d.Ports[1].NetDevices, []string{"eth1"}) || d.Ports[0].PKeys[0] != 0xffff {
		t.Fatalf("lost a port or duplicated GID mapping: %+v", d.Ports)
	}
	want := []RDMAChar{{Name: "uverbs0", Class: "infiniband_verbs", Major: 231, Minor: 192}, {Name: "umad0", Class: "infiniband_mad", Major: 231, Minor: 0, Port: 1}}
	if !reflect.DeepEqual(d.CharDevices, want) {
		t.Fatalf("incorrect scoped char nodes: %+v", d.CharDevices)
	}
	// IB has no RoCE GID-to-netdevice mapping; retain the PCI join without
	// pretending this proves which port owns the IPoIB interface.
	if err := os.WriteFile(filepath.Join(root, "class/infiniband/mlx5_0/ports/1/link_layer"), []byte("InfiniBand\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(root, "class/infiniband/mlx5_0/ports/1/gid_attrs")); err != nil {
		t.Fatal(err)
	}
	devices, err = discoverRDMA(root)
	if err != nil || devices[0].Ports[0].LinkLayer != "infiniband" || len(devices[0].Ports[0].NetDevices) != 0 || !reflect.DeepEqual(devices[0].NetDevices, []string{"eth0", "eth1"}) {
		t.Fatalf("IB PCI fallback: %+v %v", devices, err)
	}
}

func TestRDMADiscoveryDoesNotConvertUnknownToFree(t *testing.T) {
	for _, tc := range []struct{ name, path, value, want string }{
		{"malformed GUID", "class/infiniband/mlx5_0/node_guid", "1234", "E_RDMA_GUID"},
		{"invalid PKey", "class/infiniband/mlx5_0/ports/1/pkeys/0", "0x10000", "E_RDMA_PKEY"},
		{"unknown link type", "class/infiniband/mlx5_0/ports/1/link_layer", "Unknown", "E_RDMA_LINK_LAYER"},
		{"foreign netdevice", "class/infiniband/mlx5_0/ports/1/gid_attrs/ndevs/1", "missing0", "E_RDMA_NETDEVICE_OWNER"},
		{"netdevice traversal", "class/infiniband/mlx5_0/ports/1/gid_attrs/ndevs/1", "../eth0", "E_RDMA_NETDEVICE"},
		{"recycled device number", "class/infiniband_verbs/uverbs0/dev", "231:0", "E_RDMA_CHAR_IDENTITY"},
		{"malformed device number", "class/infiniband_verbs/uverbs0/dev", "231:0:1", "E_RDMA_CHAR_IDENTITY"},
		{"foreign MAD port", "class/infiniband_mad/umad0/port", "3", "E_RDMA_CHAR_PORT"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := rdmaFixture(t)
			if err := os.WriteFile(filepath.Join(root, tc.path), []byte(tc.value+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := discoverRDMA(root); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("accepted unknown/foreign identity: %v", err)
			}
		})
	}
	if devices, err := discoverRDMA(t.TempDir()); err != nil || devices != nil {
		t.Fatalf("no RDMA hardware: %+v %v", devices, err)
	}
	root := rdmaFixture(t)
	if err := os.Remove(filepath.Join(root, "class/infiniband/mlx5_0/device")); err != nil {
		t.Fatal(err)
	}
	if _, err := discoverRDMA(root); err == nil {
		t.Fatal("missing physical identity treated as software RDMA")
	}
}
