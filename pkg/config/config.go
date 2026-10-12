package config

import (
	"path/filepath"

	"github.com/SchSeba/dra-multi-network/pkg/consts"
	"github.com/SchSeba/dra-multi-network/pkg/flags"
)

type Flags struct {
	KubeClientConfig flags.KubeClientConfig
	LoggingConfig    *flags.LoggingConfig

	NodeName                      string
	Namespace                     string
	CdiRoot                       string
	KubeletRegistrarDirectoryPath string
	KubeletPluginsDirectoryPath   string
}

type Config struct {
	CancelMainCtx func(error)
	Flags         *Flags
	K8sClient     flags.ClientSets
}

func (c Config) DriverPluginPath() string {
	return filepath.Join(c.Flags.KubeletPluginsDirectoryPath, consts.DriverName)
}
