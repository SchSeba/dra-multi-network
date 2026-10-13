package hack_test

import (
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// Rendering must work without kcli/libvirt and must never invoke cleanup.
func TestClusterPlan(t *testing.T) {
	hackDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name      string
		args      []string
		overrides []string
		cluster   string
		network   string
		secondary string
		apiIP     string
		podCIDR   string
		svcCIDR   string
		workers   int
		wantError bool
	}{
		{
			name: "defaults", args: []string{"--render"},
			cluster: "multi-network", network: "multi-net", secondary: "multi-sriov", apiIP: "192.168.130.250",
			podCIDR: "10.246.0.0/16", svcCIDR: "10.112.0.0/16", workers: 2,
		},
		{
			name: "single-node overrides", args: []string{"--single-node", "--render"},
			overrides: []string{"CLUSTER_NAME=custom", "NETWORK_NAME=custom-network", "SRIOV_NETWORK_NAME=custom-sriov", "API_IP=192.168.140.250", "POD_CIDR=10.247.0.0/16", "SERVICE_CIDR=10.113.0.0/16"},
			cluster:   "custom", network: "custom-network", secondary: "custom-sriov", apiIP: "192.168.140.250",
			podCIDR: "10.247.0.0/16", svcCIDR: "10.113.0.0/16", workers: 0,
		},
		{
			name: "reject oversized bridge names", args: []string{"--render"},
			overrides: []string{"NETWORK_NAME=multi-network-network"},
			wantError: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Symlink(hackDir, filepath.Join(dir, "hack")); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("bash", append([]string{filepath.Join(hackDir, "deploy-virtual-k8s-cluster.sh")}, tc.args...)...)
			cmd.Dir = dir
			// Ignore developer overrides while testing the checked-in defaults.
			for _, entry := range os.Environ() {
				key, _, _ := strings.Cut(entry, "=")
				if !slices.Contains([]string{"CLUSTER_NAME", "CLUSTER_VERSION", "NETWORK_NAME", "SRIOV_NETWORK_NAME", "NETWORK_CIDR", "SRIOV_NETWORK_CIDR", "API_IP", "POD_CIDR", "SERVICE_CIDR", "VIRTUAL_ROUTER_ID", "NUM_OF_WORKERS", "MOCK_SMARTNIC_REPO"}, key) {
					cmd.Env = append(cmd.Env, entry)
				}
			}
			cmd.Env = append(cmd.Env, tc.overrides...)
			output, err := cmd.Output()
			if tc.wantError {
				if err == nil {
					t.Fatal("oversized network name was accepted")
				}
				exitErr, ok := err.(*exec.ExitError)
				if !ok || !strings.Contains(string(exitErr.Stderr), "at most 15 characters") {
					t.Fatalf("unexpected validation error: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("render: %v", err)
			}
			var plan struct {
				Version      string   `json:"version"`
				Network      string   `json:"network"`
				APIIP        string   `json:"api_ip"`
				Domain       string   `json:"domain"`
				PodCIDR      string   `json:"cluster_network_ipv4"`
				ServiceCIDR  string   `json:"service_network_ipv4"`
				Workers      int      `json:"workers"`
				FeatureGates []string `json:"feature_gates"`
				VMRules      []map[string]struct {
					Nets []struct {
						Name string `json:"name"`
						MAC  string `json:"mac"`
					} `json:"nets"`
				} `json:"vmrules"`
			}
			if err := yaml.Unmarshal(output, &plan); err != nil {
				t.Fatalf("parse rendered plan: %v\n%s", err, output)
			}
			if plan.Version != "1.37.0" || plan.Network != tc.network || plan.APIIP != tc.apiIP || plan.Domain != tc.cluster+".lab" || plan.Workers != tc.workers {
				t.Fatalf("unexpected cluster defaults: %+v", plan)
			}
			if plan.PodCIDR != tc.podCIDR || plan.ServiceCIDR != tc.svcCIDR {
				t.Fatalf("unexpected subnets: %s / %s", plan.PodCIDR, plan.ServiceCIDR)
			}
			pod := netip.MustParsePrefix(plan.PodCIDR)
			service := netip.MustParsePrefix(plan.ServiceCIDR)
			if pod.Overlaps(service) || service.Overlaps(netip.MustParsePrefix("10.96.0.0/12")) {
				t.Fatal("new CIDRs overlap each other or the old service network")
			}
			for _, gate := range []string{"DynamicResourceAllocation", "DRAPartitionableDevices", "DRAConsumableCapacity", "DRADeviceCompatibilityGroups"} {
				if !slices.Contains(plan.FeatureGates, gate) {
					t.Errorf("required feature gate missing: %s", gate)
				}
			}
			var secondary bool
			workloadNodes := 0
			macs := map[string]bool{}
			for _, rule := range plan.VMRules {
				for _, vm := range rule {
					if len(vm.Nets) > 1 {
						workloadNodes++
						if len(vm.Nets) != 3 || vm.Nets[0].Name != tc.network || vm.Nets[1].Name != tc.secondary || vm.Nets[2].Name != tc.secondary {
							t.Fatalf("missing separate native and mock NICs: %+v", vm.Nets)
						}
						for _, nic := range vm.Nets[1:] {
							if nic.MAC == "" || macs[nic.MAC] {
								t.Fatalf("missing or duplicate test MAC: %+v", nic)
							}
							macs[nic.MAC] = true
						}
					}
					for _, network := range vm.Nets {
						if len(network.Name) > 15 {
							t.Errorf("network name exceeds libvirt bridge limit: %s", network.Name)
						}
						if network.Name == tc.secondary {
							secondary = true
						}
					}
				}
			}
			expectedNodes := tc.workers
			if expectedNodes == 0 {
				expectedNodes = 1
			}
			if workloadNodes != expectedNodes {
				t.Fatalf("want %d workload nodes, got %d", expectedNodes, workloadNodes)
			}
			if !secondary {
				t.Error("secondary network missing from VM plan")
			}
			if _, err := os.Stat(filepath.Join(dir, tc.cluster+"-plan.yaml")); err != nil {
				t.Fatal(err)
			}
		})
	}
}
