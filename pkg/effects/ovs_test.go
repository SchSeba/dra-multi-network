// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
package effects

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/ovn-org/libovsdb/database/inmemory"
	"github.com/ovn-org/libovsdb/model"
	"github.com/ovn-org/libovsdb/ovsdb"
	"github.com/ovn-org/libovsdb/server"
	"github.com/vishvananda/netlink"
	"net"

	"github.com/SchSeba/dra-multi-network/pkg/topology"
)

type ovsTestBridge struct {
	UUID         string            `ovsdb:"_uuid"`
	Name         string            `ovsdb:"name"`
	Ports        []string          `ovsdb:"ports"`
	ExternalIDs  map[string]string `ovsdb:"external_ids"`
	DatapathType string            `ovsdb:"datapath_type"`
}
type ovsTestPort struct {
	UUID        string            `ovsdb:"_uuid"`
	Name        string            `ovsdb:"name"`
	Interfaces  []string          `ovsdb:"interfaces"`
	ExternalIDs map[string]string `ovsdb:"external_ids"`
	Tag         *int              `ovsdb:"tag"`
	Trunks      []int             `ovsdb:"trunks"`
	VLANMode    *string           `ovsdb:"vlan_mode"`
	Protected   bool              `ovsdb:"protected"`
}
type ovsTestInterface struct {
	UUID        string            `ovsdb:"_uuid"`
	Name        string            `ovsdb:"name"`
	Type        string            `ovsdb:"type"`
	ExternalIDs map[string]string `ovsdb:"external_ids"`
	MTURequest  *int              `ovsdb:"mtu_request"`
	Options     map[string]string `ovsdb:"options"`
	IfIndex     *int              `ovsdb:"ifindex"`
	OfPort      *int              `ovsdb:"ofport"`
	Error       *string           `ovsdb:"error"`
}

func testOVSDatabase(t *testing.T) *ovsDatabase {
	t.Helper()
	var schema ovsdb.DatabaseSchema
	if err := json.Unmarshal([]byte(`{"name":"Open_vSwitch","version":"1.0.0","tables":{
"Bridge":{"isRoot":true,"indexes":[["name"]],"columns":{"name":{"type":"string"},"datapath_type":{"type":"string"},"external_ids":{"type":{"key":"string","value":"string","min":0,"max":"unlimited"}},"ports":{"type":{"key":{"type":"uuid","refTable":"Port"},"min":0,"max":"unlimited"}}}},
"Port":{"indexes":[["name"]],"columns":{"name":{"type":"string"},"external_ids":{"type":{"key":"string","value":"string","min":0,"max":"unlimited"}},"interfaces":{"type":{"key":{"type":"uuid","refTable":"Interface"},"min":1,"max":"unlimited"}},"tag":{"type":{"key":"integer","min":0,"max":1}},"trunks":{"type":{"key":"integer","min":0,"max":"unlimited"}},"vlan_mode":{"type":{"key":"string","min":0,"max":1}},"protected":{"type":"boolean"}}},
"Interface":{"indexes":[["name"]],"columns":{"name":{"type":"string"},"type":{"type":"string"},"external_ids":{"type":{"key":"string","value":"string","min":0,"max":"unlimited"}},"mtu_request":{"type":{"key":"integer","min":0,"max":1}},"options":{"type":{"key":"string","value":"string","min":0,"max":"unlimited"}},"ifindex":{"type":{"key":"integer","min":0,"max":1},"ephemeral":true},"ofport":{"type":{"key":"integer","min":0,"max":1},"ephemeral":true},"error":{"type":{"key":"string","min":0,"max":1},"ephemeral":true}}}
}}`), &schema); err != nil {
		t.Fatal(err)
	}
	cm, err := model.NewClientDBModel("Open_vSwitch", map[string]model.Model{ovsBridge: &ovsTestBridge{}, ovsPort: &ovsTestPort{}, ovsInterface: &ovsTestInterface{}})
	if err != nil {
		t.Fatal(err)
	}
	dm, errs := model.NewDatabaseModel(schema, cm)
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	s, err := server.NewOvsdbServer(inmemory.NewDatabase(map[string]model.ClientDBModel{"Open_vSwitch": cm}), dm)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "ovs.sock")
	done := make(chan error, 1)
	go func() { done <- s.Serve("unix", path) }()
	deadline := time.Now().Add(5 * time.Second)
	for !s.Ready() {
		if time.Now().After(deadline) {
			s.Close()
			t.Fatal("OVSDB test server did not start")
		}
		time.Sleep(time.Millisecond)
	}
	t.Cleanup(func() {
		s.Close()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	d, err := openOVS("unix:" + path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	_, err = d.transact(
		ovsdb.Operation{Op: ovsdb.OperationInsert, Table: ovsInterface, UUIDName: "base_interface", Row: ovsdb.Row{"name": "fabric0", "type": "internal"}},
		ovsdb.Operation{Op: ovsdb.OperationInsert, Table: ovsPort, UUIDName: "base_port", Row: ovsdb.Row{"name": "fabric0", "interfaces": ovsSet(ovsdb.UUID{GoUUID: "base_interface"}), "external_ids": ovsMap(map[string]string{"foreign": "before"})}},
		ovsdb.Operation{Op: ovsdb.OperationInsert, Table: ovsBridge, Row: ovsdb.Row{"name": "fabric0", "ports": ovsSet(ovsdb.UUID{GoUUID: "base_port"}), "external_ids": ovsMap(map[string]string{ovsBridgeIdentity: "site/fabric0"})}},
	)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// A real in-process OVSDB server exercises transactions and RFC wire decoding.
// This does not launch containerd/CRI-O or fabricate native representor grants.
func TestOVSOwnedTransactionsReplayAndExactCleanup(t *testing.T) {
	d := testOVSDatabase(t)
	ready := Record{Output: Handle{IfIndex: 42}}
	if err := d.dataplaneReady(ready, ovsdb.Row{}); !errors.Is(err, errOVSDataplaneNotReady) {
		t.Fatal("pending port acknowledged as Ready", err)
	}
	if err := d.dataplaneReady(ready, ovsdb.Row{"ifindex": ovsSet(42), "ofport": ovsSet(7)}); err != nil {
		t.Fatal(err)
	}
	if err := d.dataplaneReady(ready, ovsdb.Row{"ifindex": ovsSet(43), "ofport": ovsSet(7)}); err == nil || errors.Is(err, errOVSDataplaneNotReady) {
		t.Fatal("foreign interface treated as a transient setup delay", err)
	}
	s, err := d.snapshot()
	if err != nil {
		t.Fatal(err)
	}
	r := Record{State: "Applying", Effect: Effect{Kind: topology.OVSConfigure, Parameters: topology.Config{Bridge: "fabric0", MTU: 1500, Up: true, VlanID: 100, Trunk: []int{200, 300}}}, Output: Handle{Name: "rep1", Alias: "dra-mn:execution:fabric"}, OVS: &OVSPortState{Bridge: d.configRow(ovsBridge, s[ovsBridge][0])}}
	intent, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.reconcile(&r, false); err != nil {
		t.Fatal(err)
	}
	// Lose both the transaction acknowledgement and subsequent journal update.
	if err := json.Unmarshal(intent, &r); err != nil {
		t.Fatal(err)
	}
	if err := d.reconcile(&r, false); err != nil {
		t.Fatal(err)
	}
	s, err = d.snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(s[ovsPort]) != 2 || len(s[ovsInterface]) != 2 || r.OVS.PortUUID == "" || r.OVS.InterfaceUUID == "" {
		t.Fatal("lost acknowledgement duplicated or failed to identify owned rows", s, r.OVS)
	}
	// Foreign configuration on an owned row must prevent cleanup.
	_, err = d.transact(ovsdb.Operation{Op: ovsdb.OperationUpdate, Table: ovsPort, Where: ovsWhere(r.OVS.PortUUID), Row: ovsdb.Row{"protected": true}})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.reconcile(&r, true); err == nil {
		t.Fatal("foreign port configuration was deleted")
	}
	_, err = d.transact(ovsdb.Operation{Op: ovsdb.OperationUpdate, Table: ovsPort, Where: ovsWhere(r.OVS.PortUUID), Row: ovsdb.Row{"protected": false}})
	if err != nil {
		t.Fatal(err)
	}
	// Atomic snapshot guards reject changes made after a read.
	s, err = d.snapshot()
	if err != nil {
		t.Fatal(err)
	}
	guards, err := d.snapshotGuards(s)
	if err != nil {
		t.Fatal(err)
	}
	base, err := ovsFind(s[ovsPort], "fabric0")
	if err != nil {
		t.Fatal(err)
	}
	_, err = d.transact(ovsdb.Operation{Op: ovsdb.OperationUpdate, Table: ovsPort, Where: ovsWhere(ovsID(base)), Row: ovsdb.Row{"external_ids": ovsMap(map[string]string{"foreign": "retain"})}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.transact(guards...); err == nil {
		t.Fatal("concurrent foreign change passed transaction guards")
	}
	r.OVS.Restoring = true
	if err := d.reconcile(&r, true); err != nil {
		t.Fatal(err)
	}
	if err := d.reconcile(&r, true); err != nil {
		t.Fatal("lost deletion acknowledgement was not idempotent", err)
	}
	s, err = d.snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(s[ovsPort]) != 1 || len(s[ovsInterface]) != 1 || len(s[ovsBridge]) != 1 {
		t.Fatal("cleanup changed foreign rows", s)
	}
	ids, ok := s[ovsPort][0]["external_ids"].(ovsdb.OvsMap)
	if !ok || ids.GoMap["foreign"] != "retain" {
		t.Fatal("foreign before-image was changed", s)
	}
}

func TestOVSLinkOwnershipRejectsForeignMastersAndChanges(t *testing.T) {
	mac, err := net.ParseMAC("02:42:00:00:00:01")
	if err != nil {
		t.Fatal(err)
	}
	l := &netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Index: 42, Name: "rep1", MTU: 9000, HardwareAddr: mac}}
	r := Record{Effect: Effect{Parameters: topology.Config{MTU: 1500}}, Output: Handle{IfIndex: 42, Alias: "dra-mn:execution:fabric"}, OVS: &OVSPortState{Before: state(l), DatapathIndex: 7}}
	if err := ovsLinkGuard(r, l); err != nil {
		t.Fatal(err)
	}
	l.Alias, l.MTU, l.MasterIndex = r.Output.Alias, 1500, 7
	if err := ovsLinkGuard(r, l); err != nil {
		t.Fatal("owned OVS datapath membership rejected", err)
	}
	l.MasterIndex = 8
	if err := ovsLinkGuard(r, l); err == nil {
		t.Fatal("foreign master accepted")
	}
	l.MasterIndex, l.MTU = 7, 1600
	if err := ovsLinkGuard(r, l); err == nil {
		t.Fatal("foreign MTU accepted")
	}
}

func TestOVSDatapathAcceptsInternalPortWithoutLinuxMaster(t *testing.T) {
	datapath := &netlink.GenericLink{LinkAttrs: netlink.LinkAttrs{Index: 7}, LinkType: "openvswitch"}
	bridge := &netlink.GenericLink{LinkAttrs: netlink.LinkAttrs{Index: 8}, LinkType: "openvswitch"}
	for _, master := range []int{0, 7} {
		bridge.MasterIndex = master
		if err := ovsDatapathLinks(bridge, datapath); err != nil {
			t.Fatalf("valid system datapath master %d rejected: %v", master, err)
		}
	}
	bridge.MasterIndex = 9
	if err := ovsDatapathLinks(bridge, datapath); err == nil {
		t.Fatal("foreign Linux master accepted")
	}
	bridge.MasterIndex, bridge.LinkType = 0, "bridge"
	if err := ovsDatapathLinks(bridge, datapath); err == nil {
		t.Fatal("Linux bridge accepted as OVS internal port")
	}
	bridge.LinkType, datapath.LinkType = "openvswitch", "bridge"
	if err := ovsDatapathLinks(bridge, datapath); err == nil {
		t.Fatal("foreign datapath accepted")
	}
}
