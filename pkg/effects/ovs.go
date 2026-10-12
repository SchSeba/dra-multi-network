// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
// Port/Interface/Bridge transactions follow ovs-cni pkg/ovsdb at
// d893f044ce6dc4214086ba9c054203d675180631. The framework owns grants and intent.
//
//nolint:goconst // OVS schema columns and transaction vocabulary remain literal.
package effects

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/ovn-org/libovsdb/client"
	"github.com/ovn-org/libovsdb/model"
	"github.com/ovn-org/libovsdb/ovsdb"
	"github.com/vishvananda/netlink"
	"k8s.io/klog/v2"

	api "github.com/SchSeba/dra-multi-network/pkg/api/networking/v1alpha1"
	"github.com/SchSeba/dra-multi-network/pkg/topology"
)

const (
	ovsName           = "name"
	ovsType           = "type"
	ovsSocket         = "unix:/var/run/openvswitch/db.sock"
	ovsBridge         = "Bridge"
	ovsPort           = "Port"
	ovsInterface      = "Interface"
	ovsBridgeIdentity = "dra-multi-network.schseba.github.io/bridge-id"
	ovsOwner          = "dra-multi-network"
)

var errOVSDataplaneNotReady = errors.New("E_OVS_DATAPLANE_NOT_READY")

type OVSPortState struct {
	Before        LinkState `json:"before"`
	Bridge        ovsdb.Row `json:"bridge"`
	BridgeLink    LinkState `json:"bridgeLink"`
	BridgeIndex   int       `json:"bridgeIndex"`
	DatapathIndex int       `json:"datapathIndex"`
	DatapathLink  LinkState `json:"datapathLink"`
	PortUUID      string    `json:"portUUID,omitempty"`
	InterfaceUUID string    `json:"interfaceUUID,omitempty"`
	Restoring     bool      `json:"restoring,omitempty"`
}

type ovsIdentity struct {
	UUID string `ovsdb:"_uuid"`
}
type ovsDatabase struct{ client.Client }
type ovsSnapshot map[string][]ovsdb.Row

//nolint:unparam // Software checks use an isolated Unix OVSDB server.
func openOVS(endpoint string) (*ovsDatabase, error) {
	dbModel, err := model.NewClientDBModel("Open_vSwitch", map[string]model.Model{ovsBridge: &ovsIdentity{}, ovsPort: &ovsIdentity{}, ovsInterface: &ovsIdentity{}})
	if err != nil {
		return nil, err
	}
	logger := klog.Background()
	c, err := client.NewOVSDBClient(dbModel, client.WithEndpoint(endpoint), client.WithLogger(&logger))
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.Connect(ctx); err != nil {
		c.Close()
		return nil, err
	}
	return &ovsDatabase{c}, nil
}

func (d *ovsDatabase) transact(ops ...ovsdb.Operation) ([]ovsdb.OperationResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := d.Transact(ctx, ops...)
	if err != nil {
		return nil, err
	}
	failures, err := ovsdb.CheckOperationResults(result, ops)
	for _, failure := range failures {
		err = errors.Join(err, failure)
	}
	if err != nil {
		return nil, err
	}
	if len(result) != len(ops) {
		return nil, fmt.Errorf("E_OVS_RESULT_COUNT")
	}
	return result, nil
}

func (d *ovsDatabase) snapshot() (ovsSnapshot, error) {
	tables := []string{ovsBridge, ovsPort, ovsInterface}
	ops := make([]ovsdb.Operation, 0, len(tables))
	for _, table := range tables {
		ops = append(ops, ovsdb.Operation{Op: ovsdb.OperationSelect, Table: table})
	}
	result, err := d.transact(ops...)
	if err != nil {
		return nil, err
	}
	s := ovsSnapshot{}
	for i, table := range tables {
		s[table] = result[i].Rows
		for _, row := range s[table] {
			for field, column := range d.Schema().Tables[table].Columns {
				if row[field] == nil {
					value, err := ovsdb.NativeToOvs(column, reflect.Zero(ovsdb.NativeType(column)).Interface())
					if err != nil {
						return nil, err
					}
					row[field] = value
				}
			}
		}
	}
	return s, nil
}

func ovsWhere(id string) []ovsdb.Condition {
	return []ovsdb.Condition{ovsdb.NewCondition("_uuid", ovsdb.ConditionEqual, ovsdb.UUID{GoUUID: id})}
}

func ovsID(row ovsdb.Row) string {
	id, _ := row["_uuid"].(ovsdb.UUID)
	return id.GoUUID
}

func ovsMap(values map[string]string) ovsdb.OvsMap {
	result := ovsdb.OvsMap{GoMap: map[any]any{}}
	for key, value := range values {
		result.GoMap[key] = value
	}
	return result
}

func ovsSet(values ...any) ovsdb.OvsSet { return ovsdb.OvsSet{GoSet: values} }

func ovsContains(value any, id string) bool {
	if single, ok := value.(ovsdb.UUID); ok {
		return single.GoUUID == id
	}
	set, ok := value.(ovsdb.OvsSet)
	return ok && slices.ContainsFunc(set.GoSet, func(v any) bool { u, ok := v.(ovsdb.UUID); return ok && u.GoUUID == id })
}

func ovsFind(rows []ovsdb.Row, name string) (ovsdb.Row, error) {
	var found ovsdb.Row
	for _, row := range rows {
		if row[ovsName] == name {
			if found != nil {
				return nil, fmt.Errorf("E_OVS_AMBIGUOUS_NAME")
			}
			found = row
		}
	}
	return found, nil
}

func (d *ovsDatabase) nativeValue(table, field string, value any) (any, error) {
	column := d.Schema().Tables[table].Column(field)
	if column == nil {
		return nil, fmt.Errorf("E_OVS_SCHEMA: %s.%s", table, field)
	}
	if value == nil {
		return reflect.Zero(ovsdb.NativeType(column)).Interface(), nil
	}
	v, err := ovsdb.OvsToNative(column, value)
	if err != nil {
		return nil, err
	}
	rv := reflect.ValueOf(v)
	if (rv.Kind() == reflect.Slice || rv.Kind() == reflect.Map) && rv.Len() == 0 {
		return reflect.Zero(rv.Type()).Interface(), nil
	}
	if reflect.TypeOf(v).Kind() == reflect.Slice {
		sort.Slice(v, func(i, j int) bool {
			rv := reflect.ValueOf(v)
			return fmt.Sprint(rv.Index(i).Interface()) < fmt.Sprint(rv.Index(j).Interface())
		})
	}
	return v, nil
}

func (d *ovsDatabase) rowMatches(table string, actual, expected ovsdb.Row) error {
	for key := range d.configRow(table, actual) {
		a, err := d.nativeValue(table, key, actual[key])
		if err != nil {
			return err
		}
		b, err := d.nativeValue(table, key, expected[key])
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(a, b) {
			return fmt.Errorf("E_OVS_FOREIGN_CONFIG: %s.%s", table, key)
		}
	}
	return nil
}

func (d *ovsDatabase) configRow(table string, row ovsdb.Row) ovsdb.Row {
	result := ovsdb.Row{"_uuid": row["_uuid"]}
	// Derived dataplane observations are checked separately for readiness.
	observations := []string{"ifindex", "ofport", "error", "admin_state", "link_state", "link_resets", "link_speed", "duplex", "mtu", "mac_in_use", "statistics", "status", "lacp_current", "bfd_status", "cfm_fault", "cfm_fault_status", "cfm_health", "cfm_remote_mpids", "cfm_remote_opstate", "datapath_id"}
	for key, column := range d.Schema().Tables[table].Columns {
		if !column.Ephemeral() && !slices.Contains(observations, key) {
			result[key] = row[key]
		}
	}
	return result
}

func (d *ovsDatabase) anchor(r Record, s ovsSnapshot) (ovsdb.Row, error) {
	if r.OVS == nil {
		return nil, fmt.Errorf("E_OVS_BEFORE_IMAGE")
	}
	bridge, err := ovsFind(s[ovsBridge], r.Effect.Parameters.Bridge)
	if err != nil || bridge == nil {
		return nil, errors.Join(err, fmt.Errorf("E_OVS_BRIDGE_MISSING"))
	}
	before := maps.Clone(r.OVS.Bridge)
	current := d.configRow(ovsBridge, bridge)
	delete(before, "ports")
	delete(current, "ports")
	for key, value := range before {
		a, err := d.nativeValue(ovsBridge, key, value)
		if err != nil {
			return nil, err
		}
		b, err := d.nativeValue(ovsBridge, key, current[key])
		if err != nil || !reflect.DeepEqual(a, b) {
			return nil, errors.Join(err, fmt.Errorf("E_OVS_BRIDGE_CHANGED: %s", key))
		}
	}
	return bridge, nil
}

func ovsDesired(r Record, intfUUID string) (ovsdb.Row, ovsdb.Row) {
	c := r.Effect.Parameters
	externalIDs := ovsMap(map[string]string{"owner": ovsOwner, "operation": r.Output.Alias})
	intf := ovsdb.Row{ovsName: r.Output.Name, ovsType: "system", "external_ids": externalIDs, "mtu_request": ovsSet(c.MTU)}
	mode := "trunk"
	tag := ovsSet()
	if c.VlanID != 0 {
		mode = "access"
		tag = ovsSet(c.VlanID)
		if len(c.Trunk) != 0 {
			mode = "native-untagged"
		}
	}
	trunks := ovsSet()
	for _, id := range c.Trunk {
		trunks.GoSet = append(trunks.GoSet, id)
	}
	port := ovsdb.Row{ovsName: r.Output.Name, "interfaces": ovsSet(ovsdb.UUID{GoUUID: intfUUID}), "external_ids": externalIDs, "vlan_mode": ovsSet(mode), "tag": tag, "trunks": trunks}
	return port, intf
}

func (d *ovsDatabase) ownedRows(r *Record, s ovsSnapshot) (ovsdb.Row, ovsdb.Row, error) {
	bridge, err := d.anchor(*r, s)
	if err != nil {
		return nil, nil, err
	}
	for _, table := range []string{ovsPort, ovsInterface} {
		for _, row := range s[table] {
			ids, ok := row["external_ids"].(ovsdb.OvsMap)
			if ok && ids.GoMap["operation"] == r.Output.Alias && row[ovsName] != r.Output.Name {
				return nil, nil, fmt.Errorf("E_OVS_AMBIGUOUS_OWNER")
			}
		}
	}
	port, err := ovsFind(s[ovsPort], r.Output.Name)
	if err != nil {
		return nil, nil, err
	}
	intf, err := ovsFind(s[ovsInterface], r.Output.Name)
	if err != nil {
		return nil, nil, err
	}
	if port == nil && intf == nil {
		if r.OVS.PortUUID != "" && !r.OVS.Restoring || r.State == Applied && !r.OVS.Restoring {
			return nil, nil, fmt.Errorf("E_OVS_OWNED_ROWS_LOST")
		}
		return nil, nil, nil
	}
	if port == nil || intf == nil || r.OVS.PortUUID != "" && r.OVS.PortUUID != ovsID(port) || r.OVS.InterfaceUUID != "" && r.OVS.InterfaceUUID != ovsID(intf) {
		return nil, nil, fmt.Errorf("E_OVS_ROW_IDENTITY")
	}
	expectedPort, expectedIntf := ovsDesired(*r, ovsID(intf))
	expectedPort["_uuid"], expectedIntf["_uuid"] = port["_uuid"], intf["_uuid"]
	if err := d.rowMatches(ovsPort, port, expectedPort); err != nil {
		return nil, nil, err
	}
	if err := d.rowMatches(ovsInterface, intf, expectedIntf); err != nil {
		return nil, nil, err
	}
	if !ovsContains(bridge["ports"], ovsID(port)) {
		return nil, nil, fmt.Errorf("E_OVS_MEMBERSHIP_CHANGED")
	}
	for _, b := range s[ovsBridge] {
		if ovsID(b) != ovsID(bridge) && ovsContains(b["ports"], ovsID(port)) {
			return nil, nil, fmt.Errorf("E_OVS_FOREIGN_MEMBERSHIP")
		}
	}
	for _, p := range s[ovsPort] {
		if ovsID(p) != ovsID(port) && ovsContains(p["interfaces"], ovsID(intf)) {
			return nil, nil, fmt.Errorf("E_OVS_FOREIGN_INTERFACE_REFERENCE")
		}
	}
	r.OVS.PortUUID, r.OVS.InterfaceUUID = ovsID(port), ovsID(intf)
	return port, intf, nil
}

func (d *ovsDatabase) snapshotGuards(s ovsSnapshot) ([]ovsdb.Operation, error) {
	// ponytail: guard every observed config row; narrow to affected references
	// if measured fabric churn makes unrelated changes expensive to retry.
	var ops []ovsdb.Operation
	zero := 0
	for _, table := range []string{ovsBridge, ovsPort, ovsInterface} {
		if len(s[table]) == 0 {
			return nil, fmt.Errorf("E_OVS_EXISTING_BRIDGE_PORT_REQUIRED")
		}
		for _, row := range s[table] {
			config := d.configRow(table, row)
			ops = append(ops, ovsdb.Operation{Op: ovsdb.OperationWait, Table: table, Where: ovsWhere(ovsID(row)), Columns: slices.Sorted(maps.Keys(config)), Rows: []ovsdb.Row{config}, Until: "==", Timeout: &zero})
		}
	}
	return ops, nil
}

func (d *ovsDatabase) reconcile(r *Record, remove bool) error {
	s, err := d.snapshot()
	if err != nil {
		return err
	}
	port, _, err := d.ownedRows(r, s)
	if err != nil {
		return err
	}
	if remove && port == nil || !remove && port != nil {
		return nil
	}
	guards, err := d.snapshotGuards(s)
	if err != nil {
		return err
	}
	bridge, err := d.anchor(*r, s)
	if err != nil {
		return err
	}
	if remove {
		guards = append(guards, ovsdb.Operation{Op: ovsdb.OperationMutate, Table: ovsBridge, Where: ovsWhere(ovsID(bridge)), Mutations: []ovsdb.Mutation{*ovsdb.NewMutation("ports", ovsdb.MutateOperationDelete, ovsSet(ovsdb.UUID{GoUUID: r.OVS.PortUUID}))}}, ovsdb.Operation{Op: ovsdb.OperationDelete, Table: ovsPort, Where: ovsWhere(r.OVS.PortUUID)}, ovsdb.Operation{Op: ovsdb.OperationDelete, Table: ovsInterface, Where: ovsWhere(r.OVS.InterfaceUUID)})
	} else {
		zero := 0
		for _, table := range []string{ovsPort, ovsInterface} {
			// Projection to name makes every nonempty match the same row,
			// including duplicate names; RFC 7047 wait compares distinct rows.
			guards = append(guards, ovsdb.Operation{Op: ovsdb.OperationWait, Table: table, Where: []ovsdb.Condition{ovsdb.NewCondition(ovsName, ovsdb.ConditionEqual, r.Output.Name)}, Columns: []string{ovsName}, Rows: []ovsdb.Row{{ovsName: r.Output.Name}}, Until: "!=", Timeout: &zero})
		}
		p, i := ovsDesired(*r, "new_interface")
		guards = append(guards, ovsdb.Operation{Op: ovsdb.OperationInsert, Table: ovsInterface, UUIDName: "new_interface", Row: i}, ovsdb.Operation{Op: ovsdb.OperationInsert, Table: ovsPort, UUIDName: "new_port", Row: p}, ovsdb.Operation{Op: ovsdb.OperationMutate, Table: ovsBridge, Where: ovsWhere(ovsID(bridge)), Mutations: []ovsdb.Mutation{*ovsdb.NewMutation("ports", ovsdb.MutateOperationInsert, ovsSet(ovsdb.UUID{GoUUID: "new_port"}))}})
	}
	_, err = d.transact(guards...)
	return err // Lost acknowledgement is resolved by ownedRows on the next call.
}

func (n *Native) captureOVS(input Handle, c topology.Config) (*OVSPortState, error) {
	l, err := n.representor(input)
	if err != nil {
		return nil, err
	}
	if err := n.preflightLinkUsers(l, false, 0); err != nil {
		return nil, err
	}
	if strings.HasPrefix(l.Attrs().Alias, "dra-mn:") {
		return nil, fmt.Errorf("E_REPRESENTOR_ALREADY_OWNED")
	}
	d, err := openOVS(ovsSocket)
	if err != nil {
		return nil, err
	}
	defer d.Close()
	if err := topology.ValidateOVSConfig(c); err != nil {
		return nil, err
	}
	if err := topology.ValidateOVSBridgeGrants(input.AllowedOVSBridges); err != nil {
		return nil, err
	}
	var grant *api.OVSBridgeGrant
	for i := range input.AllowedOVSBridges {
		if input.AllowedOVSBridges[i].Name == c.Bridge {
			grant = &input.AllowedOVSBridges[i]
		}
	}
	if grant == nil {
		return nil, fmt.Errorf("E_OVS_BRIDGE_GRANT")
	}
	for _, vlan := range append(slices.Clone(c.Trunk), c.VlanID) {
		if vlan != 0 && !slices.Contains(grant.VLANs, vlan) {
			return nil, fmt.Errorf("E_OVS_VLAN_GRANT")
		}
	}
	s, err := d.snapshot()
	if err != nil {
		return nil, err
	}
	b, err := ovsFind(s[ovsBridge], c.Bridge)
	if err != nil || b == nil || ovsID(b) != grant.UUID {
		return nil, errors.Join(err, fmt.Errorf("E_OVS_BRIDGE_IDENTITY"))
	}
	ids, ok := b["external_ids"].(ovsdb.OvsMap)
	if !ok || ids.GoMap[ovsBridgeIdentity] != grant.Identity || b["datapath_type"] != "" && b["datapath_type"] != "system" {
		return nil, fmt.Errorf("E_OVS_BRIDGE_IDENTITY_OR_DATAPATH")
	}
	for _, table := range []string{ovsPort, ovsInterface} {
		row, err := ovsFind(s[table], input.Name)
		if err != nil || row != nil {
			return nil, errors.Join(err, fmt.Errorf("E_OVS_FOREIGN_PORT"))
		}
	}
	if err := n.protectOVSBridge(b, s); err != nil {
		return nil, err
	}
	bridgeLink, err := n.host.LinkByName(c.Bridge)
	if err != nil {
		return nil, err
	}
	datapath, err := n.host.LinkByName("ovs-system")
	if err != nil {
		return nil, err
	}
	if err := ovsDatapathLinks(bridgeLink, datapath); err != nil {
		return nil, err
	}
	config, ok := b["other_config"].(ovsdb.OvsMap)
	fixed, _ := config.GoMap["hwaddr"].(string)
	mac, macErr := net.ParseMAC(fixed)
	if !ok || macErr != nil || len(mac) != 6 || mac[0]&1 != 0 || fixed == "00:00:00:00:00:00" || bridgeLink.Attrs().HardwareAddr.String() != mac.String() || bridgeLink.Attrs().MTU != c.MTU || bridgeLink.Attrs().Flags&net.FlagUp == 0 {
		return nil, fmt.Errorf("E_OVS_FIXED_BRIDGE_MAC_OR_CONFIG_REQUIRED")
	}
	return &OVSPortState{Before: state(l), Bridge: d.configRow(ovsBridge, b), BridgeLink: state(bridgeLink), BridgeIndex: bridgeLink.Attrs().Index, DatapathIndex: datapath.Attrs().Index, DatapathLink: state(datapath)}, nil
}

// Internal OVS bridge ports may have no Linux master even with a system datapath.
func ovsDatapathLinks(bridge, datapath netlink.Link) error {
	master := bridge.Attrs().MasterIndex
	if datapath.Type() != "openvswitch" || bridge.Type() != "openvswitch" || master != 0 && master != datapath.Attrs().Index {
		return fmt.Errorf("E_OVS_SYSTEM_DATAPATH_IDENTITY")
	}
	return nil
}

func (n *Native) ovsHostAnchor(r Record) error {
	if r.OVS == nil {
		return fmt.Errorf("E_OVS_BEFORE_IMAGE")
	}
	l, err := n.host.LinkByIndex(r.OVS.BridgeIndex)
	if err != nil {
		return err
	}
	if state(l) != r.OVS.BridgeLink {
		return fmt.Errorf("E_OVS_HOST_BRIDGE_INCARNATION_OR_STATE")
	}
	d, err := n.host.LinkByIndex(r.OVS.DatapathIndex)
	if err != nil {
		return err
	}
	if d.Type() != "openvswitch" || state(d) != r.OVS.DatapathLink {
		return fmt.Errorf("E_OVS_SYSTEM_DATAPATH_IDENTITY")
	}
	return nil
}

func (n *Native) protectOVSBridge(b ovsdb.Row, s ovsSnapshot) error {
	links, err := n.host.LinkList()
	if err != nil {
		return err
	}
	routes, err := n.host.RouteList(nil, netlink.FAMILY_ALL)
	if err != nil {
		return err
	}
	protected := ProtectedLinks(links, routes)
	for _, p := range s[ovsPort] {
		if !ovsContains(b["ports"], ovsID(p)) {
			continue
		}
		for _, i := range s[ovsInterface] {
			if !ovsContains(p["interfaces"], ovsID(i)) {
				continue
			}
			name, ok := i[ovsName].(string)
			if !ok {
				return fmt.Errorf("E_OVS_INTERFACE_IDENTITY")
			}
			link, err := n.host.LinkByName(name)
			if err != nil {
				return err
			}
			if protected[link.Attrs().Index] {
				return fmt.Errorf("E_PROTECTED_OVS_BRIDGE")
			}
		}
	}
	return nil
}

func ovsLinkGuard(r Record, l netlink.Link) error {
	if r.OVS == nil {
		return fmt.Errorf("E_OVS_BEFORE_IMAGE")
	}
	s, b, c := state(l), r.OVS.Before, r.Effect.Parameters
	if l.Attrs().Index != r.Output.IfIndex || s.Name != b.Name || s.MAC != b.MAC || s.MasterIndex != 0 && s.MasterIndex != r.OVS.DatapathIndex || b.MasterIndex != 0 {
		return fmt.Errorf("E_OVS_REPRESENTOR_INCARNATION")
	}
	if s.Alias == b.Alias && s == b {
		return nil
	}
	if s.Alias != r.Output.Alias || s.MTU != b.MTU && s.MTU != c.MTU {
		return fmt.Errorf("E_OVS_FOREIGN_LINK_STATE")
	}
	return nil
}

func (n *Native) applyOVS(r *Record, save func() error) error {
	if r.OVS == nil || r.OVS.Restoring {
		return fmt.Errorf("E_OVS_RESTORING_OR_MISSING_INTENT")
	}
	if r.State == Applied {
		return n.observeOVS(*r)
	}
	l, err := n.representor(r.Output)
	if err != nil {
		return err
	}
	if err := ovsLinkGuard(*r, l); err != nil {
		return err
	}
	if err := n.ovsHostAnchor(*r); err != nil {
		return err
	}
	if err := n.preflightLinkUsers(l, false, r.OVS.DatapathIndex); err != nil {
		return err
	}
	d, err := openOVS(ovsSocket)
	if err != nil {
		return err
	}
	defer d.Close()
	s, err := d.snapshot()
	if err != nil {
		return err
	}
	if _, _, err := d.ownedRows(r, s); err != nil {
		return err
	}
	if err := n.protectOVSBridge(r.OVS.Bridge, s); err != nil {
		return err
	}
	if err := n.host.LinkSetAlias(l, r.Output.Alias); err != nil {
		return err
	}
	if err := n.host.LinkSetDown(l); err != nil {
		return err
	}
	if err := n.host.LinkSetMTU(l, r.Effect.Parameters.MTU); err != nil {
		return err
	}
	if err := d.reconcile(r, false); err != nil {
		return err
	}
	s, err = d.snapshot()
	if err != nil {
		return err
	}
	if _, _, err := d.ownedRows(r, s); err != nil {
		return err
	}
	if err := save(); err != nil {
		return err
	}
	if r.Effect.Parameters.Up {
		if err := n.host.LinkSetUp(l); err != nil {
			return err
		}
	}
	// OVSDB commit does not acknowledge asynchronous ovs-vswitchd port setup.
	deadline := time.Now().Add(5 * time.Second)
	for {
		err := n.observeOVS(*r)
		if !errors.Is(err, errOVSDataplaneNotReady) || time.Now().After(deadline) {
			return err
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (n *Native) observeOVS(r Record) error {
	if err := n.ovsHostAnchor(r); err != nil {
		return err
	}
	l, err := n.link(r.Output)
	if err != nil {
		return err
	}
	if err := ovsLinkGuard(r, l); err != nil {
		return err
	}
	if l.Attrs().MTU != r.Effect.Parameters.MTU || (l.Attrs().Flags&net.FlagUp != 0) != r.Effect.Parameters.Up {
		return fmt.Errorf("E_OVS_LINK_POSTCONDITION")
	}
	d, err := openOVS(ovsSocket)
	if err != nil {
		return err
	}
	defer d.Close()
	s, err := d.snapshot()
	if err != nil {
		return err
	}
	port, intf, err := d.ownedRows(&r, s)
	if err != nil {
		return err
	}
	if port == nil {
		return fmt.Errorf("E_OVS_PORT_MISSING")
	}
	if err := d.dataplaneReady(r, intf); err != nil {
		return err
	}
	return n.protectOVSBridge(r.OVS.Bridge, s)
}

func (d *ovsDatabase) dataplaneReady(r Record, intf ovsdb.Row) error {
	index, err := d.nativeValue(ovsInterface, "ifindex", intf["ifindex"])
	if err != nil {
		return err
	}
	ofport, err := d.nativeValue(ovsInterface, "ofport", intf["ofport"])
	if err != nil {
		return err
	}
	failure, err := d.nativeValue(ovsInterface, "error", intf["error"])
	if err != nil {
		return err
	}
	i, iok := index.(*int)
	o, ook := ofport.(*int)
	e, eok := failure.(*string)
	if !iok || !ook || !eok {
		return fmt.Errorf("E_OVS_DATAPLANE_SCHEMA")
	}
	if i != nil && *i > 0 && *i != r.Output.IfIndex {
		return fmt.Errorf("E_OVS_DATAPLANE_IDENTITY")
	}
	if e != nil && *e != "" || o != nil && *o < 0 {
		return fmt.Errorf("E_OVS_DATAPLANE_ERROR")
	}
	if i == nil || *i <= 0 || o == nil || *o == 0 {
		return errOVSDataplaneNotReady
	}
	return nil
}

func (n *Native) restoreOVS(r *Record, save func() error) error {
	if err := n.ovsHostAnchor(*r); err != nil {
		return err
	}
	l, err := n.representor(r.Output)
	if err != nil {
		return err
	}
	if err := ovsLinkGuard(*r, l); err != nil {
		return err
	}
	if err := n.preflightLinkUsers(l, false, r.OVS.DatapathIndex); err != nil {
		return err
	}
	d, err := openOVS(ovsSocket)
	if err != nil {
		return err
	}
	defer d.Close()
	s, err := d.snapshot()
	if err != nil {
		return err
	}
	port, _, err := d.ownedRows(r, s)
	if err != nil {
		return err
	}
	if err := n.protectOVSBridge(r.OVS.Bridge, s); err != nil {
		return err
	}
	if !r.OVS.Restoring {
		if r.State == Applied && (l.Attrs().Alias != r.Output.Alias || l.Attrs().MTU != r.Effect.Parameters.MTU || (l.Attrs().Flags&net.FlagUp != 0) != r.Effect.Parameters.Up) {
			return fmt.Errorf("E_OVS_FOREIGN_APPLIED_STATE")
		}
		r.OVS.Restoring = true
		if err := save(); err != nil {
			return err
		}
	}
	if port != nil {
		if err := n.host.LinkSetDown(l); err != nil {
			return err
		}
		if err := d.reconcile(r, true); err != nil {
			return err
		}
	}
	l, err = n.host.LinkByIndex(r.Output.IfIndex)
	if err != nil {
		return err
	}
	if l.Attrs().MasterIndex != 0 {
		return fmt.Errorf("E_OVS_DATAPLANE_REMOVE_PENDING")
	}
	if err := n.host.LinkSetMTU(l, r.OVS.Before.MTU); err != nil {
		return err
	}
	if r.OVS.Before.Up {
		err = n.host.LinkSetUp(l)
	} else {
		err = n.host.LinkSetDown(l)
	}
	if err != nil {
		return err
	}
	actual, err := n.host.LinkByIndex(r.Output.IfIndex)
	if err != nil {
		return err
	}
	expected := r.OVS.Before
	expected.Alias = actual.Attrs().Alias
	if state(actual) != expected {
		return fmt.Errorf("E_OVS_LINK_RESTORE_POSTCONDITION")
	}
	if err := n.host.LinkSetAlias(l, r.OVS.Before.Alias); err != nil {
		return err
	}
	actual, err = n.host.LinkByIndex(r.Output.IfIndex)
	if err != nil {
		return err
	}
	if state(actual) != r.OVS.Before {
		return fmt.Errorf("E_OVS_LINK_RESTORE_POSTCONDITION")
	}
	return nil
}
