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

func watchSourceWhenAvailable(mgr ctrl.Manager, c controller.Controller, gvk schema.GroupVersionKind, h handler.EventHandler) error {
	watch := func() error {
		object := &unstructured.Unstructured{}
		object.SetGroupVersionKind(gvk)
		return c.Watch(source.Kind[client.Object](mgr.GetCache(), object, h))
	}
	available, err := sourceAPIAvailable(mgr.GetRESTMapper(), gvk)
	if err != nil {
		return fmt.Errorf("discover %s API: %w", gvk.Kind, err)
	}
	if available {
		return watch()
	}
	ctrl.Log.Info("Waiting for source API", "kind", gvk.Kind)
	// Register absent APIs after startup so their informers cannot block other controllers.
	return mgr.Add(manager.RunnableFunc(func(ctx context.Context) error {
		err := wait.PollUntilContextCancel(ctx, 10*time.Second, true, func(context.Context) (bool, error) {
			available, err := sourceAPIAvailable(mgr.GetRESTMapper(), gvk)
			if err != nil {
				return false, fmt.Errorf("discover %s API: %w", gvk.Kind, err)
			}
			if !available {
				return false, nil
			}
			return true, watch()
		})
		if ctx.Err() != nil {
			return nil
		}
		return err
	}))
}
