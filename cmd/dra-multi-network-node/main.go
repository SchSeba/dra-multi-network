// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/urfave/cli/v2"
	"k8s.io/dynamic-resource-allocation/kubeletplugin"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/SchSeba/dra-multi-network/pkg/config"
	"github.com/SchSeba/dra-multi-network/pkg/flags"
	"github.com/SchSeba/dra-multi-network/pkg/multinetwork"
)

func main() {
	if err := newApp().Run(os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
func newApp() *cli.App {
	options := &config.Flags{LoggingConfig: flags.NewLoggingConfig()}
	cliFlags := []cli.Flag{
		&cli.StringFlag{Name: "node-name", Usage: "Node hosting this agent.", Required: true, Destination: &options.NodeName, EnvVars: []string{"NODE_NAME"}},
		&cli.StringFlag{Name: "namespace", Usage: "Namespace hosting the controller and agent.", Value: "dra-multi-network", Destination: &options.Namespace, EnvVars: []string{"NAMESPACE"}},
		&cli.StringFlag{Name: "cdi-root", Usage: "Directory for scoped CDI claim files.", Value: "/var/run/cdi", Destination: &options.CdiRoot, EnvVars: []string{"CDI_ROOT"}},
		&cli.StringFlag{Name: "kubelet-registrar-directory-path", Usage: "Kubelet registration directory.", Value: kubeletplugin.KubeletRegistryDir, Destination: &options.KubeletRegistrarDirectoryPath, EnvVars: []string{"KUBELET_REGISTRAR_DIRECTORY_PATH"}},
		&cli.StringFlag{Name: "kubelet-plugins-directory-path", Usage: "Kubelet plugin data directory.", Value: kubeletplugin.KubeletPluginsDir, Destination: &options.KubeletPluginsDirectoryPath, EnvVars: []string{"KUBELET_PLUGINS_DIRECTORY_PATH"}},
	}
	cliFlags = append(cliFlags, options.KubeClientConfig.Flags()...)
	cliFlags = append(cliFlags, options.LoggingConfig.Flags()...)
	return &cli.App{Name: "dra-multi-network-node", Usage: "Secondary networks through DRA and NRI with in-process plugins.", HideHelpCommand: true, Flags: cliFlags,
		Before: func(c *cli.Context) error {
			if c.Args().Len() > 0 {
				return fmt.Errorf("arguments not supported: %v", c.Args().Slice())
			}
			return options.LoggingConfig.Apply()
		},
		Action: func(c *cli.Context) error {
			clients, err := options.KubeClientConfig.NewClientSets()
			if err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(c.Context, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)
			defer stop()
			return RunPlugin(ctx, &config.Config{Flags: options, K8sClient: clients})
		},
	}
}
func RunPlugin(ctx context.Context, c *config.Config) error {
	ctrl.SetLogger(klog.FromContext(ctx))
	if err := os.MkdirAll(c.DriverPluginPath(), 0750); err != nil {
		return err
	}
	if err := os.MkdirAll(c.Flags.CdiRoot, 0750); err != nil {
		return err
	}
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(context.Canceled)
	c.CancelMainCtx = cancel
	return multinetwork.Run(ctx, c)
}
