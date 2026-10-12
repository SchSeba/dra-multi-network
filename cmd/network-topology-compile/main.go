// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/yaml"

	api "github.com/SchSeba/dra-multi-network/pkg/api/networking/v1alpha1"
	"github.com/SchSeba/dra-multi-network/pkg/topology"
)

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// This is an offline preview. The controller owns API publication and assigns
// the actual source UID; no kubeconfig or Kubernetes client is used here.
func run(paths []string, stdin io.Reader, stdout io.Writer) error {
	if len(paths) == 0 {
		return fmt.Errorf("usage: network-topology-compile FILE... (use - for stdin; NetworkPlugin and NetworkTopology documents)")
	}
	plugins := topology.PluginRegistry(nil)
	seen := map[string]bool{}
	var topologies []api.NetworkTopology
	read := func(input io.Reader) error {
		decoder := yaml.NewYAMLOrJSONDecoder(input, 4096)
		for {
			var raw json.RawMessage
			if err := decoder.Decode(&raw); err != nil {
				if errors.Is(err, io.EOF) {
					return nil
				}
				return err
			}
			if len(raw) == 0 || string(raw) == "null" {
				continue
			}
			var meta struct {
				metav1.TypeMeta `json:",inline"`
				Metadata        metav1.ObjectMeta `json:"metadata"`
			}
			if err := json.Unmarshal(raw, &meta); err != nil {
				return err
			}
			if meta.APIVersion != api.GroupVersion.String() || meta.Metadata.Name == "" {
				return fmt.Errorf("E_PREVIEW_RESOURCE: require project apiVersion and metadata.name")
			}
			key := meta.Kind + "/" + meta.Metadata.Name
			if seen[key] {
				return fmt.Errorf("E_PREVIEW_DUPLICATE: %s", key)
			}
			seen[key] = true
			switch meta.Kind {
			case "NetworkPlugin":
				var plugin api.NetworkPlugin
				if err := topology.Decode(raw, &plugin); err != nil {
					return err
				}
				if err := topology.ValidatePlugin(plugin); err != nil {
					return fmt.Errorf("%s: %w", key, err)
				}
				plugins[plugin.Name] = plugin
			case "NetworkTopology":
				var network api.NetworkTopology
				if err := topology.Decode(raw, &network); err != nil {
					return err
				}
				topologies = append(topologies, network)
			default:
				return fmt.Errorf("E_PREVIEW_RESOURCE: unsupported %s", meta.Kind)
			}
		}
	}
	for _, path := range paths {
		if path == "-" {
			if err := read(stdin); err != nil {
				return fmt.Errorf("stdin: %w", err)
			}
			continue
		}
		// #nosec G703 -- Local CLI reads paths explicitly selected by its caller, without elevated privileges.
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		err = errors.Join(read(file), file.Close())
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	}
	if len(topologies) == 0 {
		return fmt.Errorf("E_PREVIEW_RESOURCE: at least one NetworkTopology required")
	}
	// Validate every document before writing any preview; a later invalid
	// topology must not leave an apparently successful partial result.
	revisions := make([]api.NetworkTopologyRevision, 0, len(topologies))
	for _, network := range topologies {
		spec, err := topology.Compile(network, plugins)
		if err != nil {
			return fmt.Errorf("NetworkTopology/%s: %w", network.Name, err)
		}
		revisions = append(revisions, api.NetworkTopologyRevision{
			TypeMeta:   metav1.TypeMeta{APIVersion: api.GroupVersion.String(), Kind: "NetworkTopologyRevision"},
			ObjectMeta: metav1.ObjectMeta{GenerateName: network.Name + "-", Annotations: map[string]string{api.Group + "/offline-preview": "true"}},
			Spec:       spec,
		})
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(struct {
		metav1.TypeMeta `json:",inline"`
		Items           []api.NetworkTopologyRevision `json:"items"`
	}{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "List"}, Items: revisions})
}
