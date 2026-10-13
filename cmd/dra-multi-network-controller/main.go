// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/SchSeba/dra-multi-network/pkg/ipam"

	"github.com/SchSeba/dra-multi-network/pkg/admission"
	api "github.com/SchSeba/dra-multi-network/pkg/api/networking/v1alpha1"
	"github.com/SchSeba/dra-multi-network/pkg/controller"
	"github.com/SchSeba/dra-multi-network/pkg/flags"
	"github.com/SchSeba/dra-multi-network/pkg/topology"
)

type initialization struct{ manager.RunnableFunc }

func (initialization) NeedLeaderElection() bool { return false }

func main() {
	if err := run(); err != nil {
		klog.ErrorS(err, "controller stopped")
		os.Exit(1)
	}
}
func run() error {
	ctx := ctrl.SetupSignalHandler()
	ctrl.SetLogger(klog.NewKlogr())
	cfg, err := ctrl.GetConfig()
	if err != nil {
		return err
	}
	c, err := client.New(cfg, client.Options{Scheme: flags.Scheme})
	if err != nil {
		return err
	}
	namespace := os.Getenv("NAMESPACE")
	if namespace == "" {
		namespace = "dra-multi-network"
	}
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{Scheme: flags.Scheme, LeaderElection: true, LeaderElectionID: "dra-multi-network-controller", LeaderElectionNamespace: namespace, Metrics: metricsserver.Options{BindAddress: "0"}, HealthProbeBindAddress: ":8081"})
	if err != nil {
		return err
	}
	if err := (&controller.TopologyReconciler{Client: c, Namespace: namespace}).SetupWithManager(mgr); err != nil {
		return err
	}
	if err := (&controller.RetentionReconciler{Client: c, Namespace: namespace}).SetupWithManager(mgr); err != nil {
		return err
	}
	if err := (&controller.CapabilityReconciler{Client: mgr.GetClient(), Namespace: namespace}).SetupWithManager(mgr); err != nil {
		return err
	}
	ipamClient, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return err
	}
	if err := mgr.Add(&ipam.Publisher{Provider: ipam.Store{Client: c, Namespace: namespace}, Client: c, KubeClient: ipamClient}); err != nil {
		return err
	}
	ready := make(chan struct{})
	installed := make(chan struct{})
	if err := mgr.Add(&admission.Server{Client: c, Namespace: namespace, Service: "dra-multi-network-webhook", Ready: ready}); err != nil {
		return err
	}
	if err := mgr.Add(initialization{manager.RunnableFunc(func(ctx context.Context) error {
		select {
		case <-ready:
			// The listener can be ready before the Service endpoint reaches the
			// API server. Keep serving during transient bootstrap admission errors.
			if err := wait.PollUntilContextTimeout(ctx, time.Second, 2*time.Minute, true, func(ctx context.Context) (bool, error) {
				err := installBuiltins(ctx, c)
				if apierrors.IsInternalError(err) || apierrors.IsServiceUnavailable(err) || apierrors.IsTimeout(err) || apierrors.IsServerTimeout(err) || apierrors.IsTooManyRequests(err) {
					return false, nil
				}
				return err == nil, err
			}); err != nil {
				return err
			}
			close(installed)
			return nil
		case <-ctx.Done():
			return nil
		}
	})}); err != nil {
		return err
	}
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return err
	}
	if err := mgr.AddReadyzCheck("readyz", func(_ *http.Request) error {
		select {
		case <-installed:
			return nil
		default:
			return fmt.Errorf("webhook and plugin initialization pending")
		}
	}); err != nil {
		return err
	}
	return mgr.Start(ctx)
}
func installBuiltins(ctx context.Context, c client.Client) error {
	for _, p := range topology.Builtins() {
		var existing api.NetworkPlugin
		err := c.Get(ctx, client.ObjectKey{Name: p.Name}, &existing)
		if apierrors.IsNotFound(err) {
			if err := c.Create(ctx, &p); err != nil && !apierrors.IsAlreadyExists(err) {
				return err
			}
			if err := c.Get(ctx, client.ObjectKey{Name: p.Name}, &existing); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		if topology.Digest(existing.Spec) != topology.Digest(p.Spec) {
			return fmt.Errorf("E_PLUGIN_CONTRACT: %s differs from this build", p.Name)
		}
	}
	return nil
}
