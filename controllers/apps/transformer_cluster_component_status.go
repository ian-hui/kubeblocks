/*
Copyright (C) 2022-2024 ApeCloud Co., Ltd

This file is part of KubeBlocks project

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program.  If not, see <http://www.gnu.org/licenses/>.
*/

package apps

import (
	"fmt"

	"golang.org/x/exp/slices"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	appsv1alpha1 "github.com/apecloud/kubeblocks/apis/apps/v1alpha1"
	"github.com/apecloud/kubeblocks/pkg/controller/component"
	"github.com/apecloud/kubeblocks/pkg/controller/graph"
	"github.com/apecloud/kubeblocks/pkg/controller/model"
)

// clusterComponentStatusTransformer transforms all cluster components' status.
type clusterComponentStatusTransformer struct{}

var _ graph.Transformer = &clusterComponentStatusTransformer{}

func (t *clusterComponentStatusTransformer) Transform(ctx graph.TransformContext, dag *graph.DAG) error {
	transCtx, _ := ctx.(*clusterTransformContext)
	if model.IsObjectDeleting(transCtx.OrigCluster) {
		return nil
	}

	// has no components defined
	if len(transCtx.ComponentSpecs) == 0 || !transCtx.OrigCluster.IsStatusUpdating() {
		return nil
	}
	return t.reconcileComponentsStatus(transCtx)
}

func (t *clusterComponentStatusTransformer) reconcileComponentsStatus(transCtx *clusterTransformContext) error {
	cluster := transCtx.Cluster

	// 添加状态初始化日志
	transCtx.Logger.Info("Starting to reconcile components status",
		"clusterName", cluster.Name,
		"componentsCount", len(transCtx.ComponentSpecs))

	if cluster.Status.Components == nil {
		transCtx.Logger.Info("Initializing cluster.Status.Components map")
		cluster.Status.Components = make(map[string]appsv1alpha1.ClusterComponentStatus)
	}

	for _, compSpec := range transCtx.ComponentSpecs {
		compKey := types.NamespacedName{
			Namespace: cluster.Namespace,
			Name:      component.FullName(cluster.Name, compSpec.Name),
		}

		// 添加组件处理日志
		transCtx.Logger.Info("Processing component",
			"componentName", compSpec.Name,
			"fullName", compKey.Name)

		comp := &appsv1alpha1.Component{}
		if err := transCtx.Client.Get(transCtx.Context, compKey, comp); err != nil {
			if apierrors.IsNotFound(err) {
				transCtx.Logger.Info("Component not found, skipping",
					"componentName", compSpec.Name,
					"error", err)
				continue
			}
			transCtx.Logger.Error(err, "Failed to get component",
				"componentName", compSpec.Name)
			return err
		}

		// 记录组件状态更新
		prevStatus := cluster.Status.Components[compSpec.Name]
		cluster.Status.Components[compSpec.Name] = t.buildClusterCompStatus(transCtx, comp, compSpec.Name)

		// 添加状态变化日志
		transCtx.Logger.Info("Component status updated",
			"componentName", compSpec.Name,
			"previousPhase", prevStatus.Phase,
			"currentPhase", cluster.Status.Components[compSpec.Name].Phase)
	}

	// 添加完成日志
	transCtx.Logger.Info("Completed reconciling components status",
		"clusterName", cluster.Name,
		"componentsCount", len(cluster.Status.Components))

	return nil
}

// buildClusterCompStatus builds cluster component status from specified component object.
func (t *clusterComponentStatusTransformer) buildClusterCompStatus(transCtx *clusterTransformContext,
	comp *appsv1alpha1.Component, compName string) appsv1alpha1.ClusterComponentStatus {
	var (
		cluster = transCtx.Cluster
		status  = cluster.Status.Components[compName]
	)

	phase := status.Phase
	t.updateClusterComponentStatus(comp, &status)
	transCtx.Logger.Info(fmt.Sprintf("cluster component status.phase: %s", status.Phase))

	if phase != status.Phase {
		phaseTransitionMsg := clusterComponentPhaseTransitionMsg(status.Phase)
		if transCtx.GetRecorder() != nil && phaseTransitionMsg != "" {
			transCtx.GetRecorder().Eventf(transCtx.Cluster, corev1.EventTypeNormal, componentPhaseTransition, phaseTransitionMsg)
		}
		transCtx.GetLogger().Info(fmt.Sprintf("cluster component phase transition: %s -> %s (%s)",
			phase, status.Phase, phaseTransitionMsg))
	}

	return status
}

// updateClusterComponentStatus sets the cluster component phase and messages conditionally.
func (t *clusterComponentStatusTransformer) updateClusterComponentStatus(comp *appsv1alpha1.Component,
	status *appsv1alpha1.ClusterComponentStatus) {
	if status.Phase != comp.Status.Phase {
		status.Phase = comp.Status.Phase
		if status.Message == nil {
			status.Message = comp.Status.Message
		} else {
			for k, v := range comp.Status.Message {
				status.Message[k] = v
			}
		}
	}
	// if ready flag not changed, don't update the ready time
	ready := t.isClusterComponentPodsReady(comp.Status.Phase)
	if status.PodsReady == nil || *status.PodsReady != ready {
		status.PodsReady = &ready
		if ready {
			now := metav1.Now()
			status.PodsReadyTime = &now
		}
	}
}

func (t *clusterComponentStatusTransformer) isClusterComponentPodsReady(phase appsv1alpha1.ClusterComponentPhase) bool {
	podsReadyPhases := []appsv1alpha1.ClusterComponentPhase{
		appsv1alpha1.RunningClusterCompPhase,
		appsv1alpha1.StoppingClusterCompPhase,
		appsv1alpha1.StoppedClusterCompPhase,
	}
	return slices.Contains(podsReadyPhases, phase)
}

func clusterComponentPhaseTransitionMsg(phase appsv1alpha1.ClusterComponentPhase) string {
	if len(phase) == 0 {
		return ""
	}
	return fmt.Sprintf("component is %s", phase)
}
