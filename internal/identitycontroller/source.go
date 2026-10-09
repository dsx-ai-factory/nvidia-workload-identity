// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package identitycontroller

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

// watchSourceWhenAvailable attaches installed APIs during setup and retries
// discovery for absent APIs after the manager starts.
func watchSourceWhenAvailable(mgr ctrl.Manager, c controller.Controller, gvk schema.GroupVersionKind, h handler.EventHandler, pollInterval time.Duration) error {
	object := &unstructured.Unstructured{}
	object.SetGroupVersionKind(gvk)
	available, err := sourceAPIAvailable(mgr.GetRESTMapper(), gvk)
	if err != nil {
		return fmt.Errorf("discover %s API: %w", gvk.Kind, err)
	}
	if available {
		// Installed APIs retain the controller's startup cache synchronization checks.
		return c.Watch(source.Kind[client.Object](mgr.GetCache(), object, h))
	}
	ctrl.Log.Info("Waiting for source API", "kind", gvk.Kind)
	// An absent API must not block startup. The manager owns the retry loop
	// and cancels it on shutdown or loss of leadership.
	return mgr.Add(manager.RunnableFunc(func(ctx context.Context) error {
		// Stop discovery retries after attaching the watch. Informer events drive reconciliation.
		err := wait.PollUntilContextCancel(ctx, pollInterval, true, func(ctx context.Context) (bool, error) {
			available, err := sourceAPIAvailable(mgr.GetRESTMapper(), gvk)
			if err != nil {
				return false, fmt.Errorf("discover %s API: %w", gvk.Kind, err)
			}
			if !available {
				return false, nil
			}
			timeout := mgr.GetControllerOptions().CacheSyncTimeout
			if timeout == 0 {
				// Match controller-runtime's default cache synchronization timeout.
				timeout = 2 * time.Minute
			}
			// GetInformer waits for the initial list. Bound that wait so list failures
			// reach the manager instead of leaving the source unwatched.
			syncCtx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			informer, err := mgr.GetCache().GetInformer(syncCtx, object)
			if err != nil {
				return false, fmt.Errorf("sync %s informer: %w", gvk.Kind, err)
			}
			// The informer replays cached objects to the new handler, including
			// sources created before this watch was attached.
			return true, c.Watch(&source.Informer{Informer: informer, Handler: h})
		})
		// Shutdown cancellation must not fail the manager.
		if ctx.Err() != nil {
			return nil
		}
		return err
	}))
}
