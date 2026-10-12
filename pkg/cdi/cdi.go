package cdi

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"

	cdiapi "tags.cncf.io/container-device-interface/pkg/cdi"
	cdiparser "tags.cncf.io/container-device-interface/pkg/parser"
	cdispec "tags.cncf.io/container-device-interface/specs-go"

	"github.com/SchSeba/dra-multi-network/pkg/consts"
)

const (
	cdiVendor = consts.DriverName
	cdiClass  = "vf"
	cdiKind   = cdiVendor + "/" + cdiClass
)

type Handler struct {
	cache *cdiapi.Cache
}

func NewHandler(cdiRootPath string) (*Handler, error) {
	cache, err := cdiapi.NewCache(
		cdiapi.WithSpecDirs(cdiRootPath),
		cdiapi.WithAutoRefresh(false),
	)
	if err != nil {
		return nil, fmt.Errorf("unable to create a new CDI cache: %w", err)
	}
	handler := &Handler{
		cache: cache,
	}

	return handler, nil
}

// CreateScopedSpecFile publishes only the already verified per-root edits.
func (cdi *Handler) CreateScopedSpecFile(uid string, edits map[string]cdispec.ContainerEdits) error {
	spec := &cdispec.Spec{Kind: cdiKind}
	for _, root := range slices.Sorted(maps.Keys(edits)) {
		spec.Devices = append(spec.Devices, cdispec.Device{Name: uid + "-" + root, ContainerEdits: edits[root]})
	}
	version, err := cdiapi.MinimumRequiredVersion(spec)
	if err != nil {
		return err
	}
	spec.Version = version
	if err := cdi.cache.WriteSpec(spec, cdiapi.GenerateTransientSpecName(cdiVendor, cdiClass, uid)); err != nil {
		return err
	}
	if err := cdi.CheckScopedSpecFile(uid, edits); err != nil {
		return err
	}
	var path string
	for root := range edits {
		device := cdi.cache.GetDevice(cdi.GetClaimDevices(uid, root))
		if device == nil {
			return fmt.Errorf("E_CDI_IDENTITY")
		}
		path = device.GetSpec().GetPath()
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		err = f.Sync()
		closeErr := f.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	if path != "" {
		dir, err := os.Open(filepath.Dir(path))
		if err != nil {
			return err
		}
		err = dir.Sync()
		closeErr := dir.Close()
		if err != nil {
			return err
		}
		return closeErr
	}
	return nil
}

func (cdi *Handler) CheckScopedSpecFile(uid string, edits map[string]cdispec.ContainerEdits) error {
	if err := cdi.cache.Refresh(); err != nil {
		return err
	}
	for root, expected := range edits {
		device := cdi.cache.GetDevice(cdi.GetClaimDevices(uid, root))
		if device == nil || !reflect.DeepEqual(device.ContainerEdits, expected) || !reflect.DeepEqual(device.GetSpec().ContainerEdits, cdispec.ContainerEdits{}) || len(device.GetSpec().Devices) != len(edits) {
			return fmt.Errorf("E_CDI_IDENTITY: root %s", root)
		}
	}
	return nil
}

func (cdi *Handler) DeleteSpecFile(uid string) error {
	specName := cdiapi.GenerateTransientSpecName(cdiVendor, cdiClass, uid)
	return cdi.cache.RemoveSpec(specName)
}

func (cdi *Handler) GetClaimDevices(claimUID string, device string) string {
	return cdiparser.QualifiedName(cdiVendor, cdiClass, fmt.Sprintf("%s-%s", claimUID, device))
}
