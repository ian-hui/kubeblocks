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

package operations

import (
	"fmt"
	"slices"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/utils/pointer"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	appsv1alpha1 "github.com/apecloud/kubeblocks/apis/apps/v1alpha1"
	dpv1alpha1 "github.com/apecloud/kubeblocks/apis/dataprotection/v1alpha1"
	"github.com/apecloud/kubeblocks/pkg/constant"
	intctrlcomp "github.com/apecloud/kubeblocks/pkg/controller/component"
	intctrlutil "github.com/apecloud/kubeblocks/pkg/controllerutil"
)

type horizontalScalingOpsHandler struct{}

var _ OpsHandler = horizontalScalingOpsHandler{}

func init() {
	hsHandler := horizontalScalingOpsHandler{}
	horizontalScalingBehaviour := OpsBehaviour{
		// if cluster is Abnormal or Failed, new opsRequest may repair it.
		FromClusterPhases: appsv1alpha1.GetClusterUpRunningPhases(),
		ToClusterPhase:    appsv1alpha1.UpdatingClusterPhase,
		QueueByCluster:    true,
		OpsHandler:        hsHandler,
		CancelFunc:        hsHandler.Cancel,
	}
	opsMgr := GetOpsManager()
	opsMgr.RegisterOps(appsv1alpha1.HorizontalScalingType, horizontalScalingBehaviour)
}

// ActionStartedCondition the started condition when handling the horizontal scaling request.
func (hs horizontalScalingOpsHandler) ActionStartedCondition(reqCtx intctrlutil.RequestCtx, cli client.Client, opsRes *OpsResource) (*metav1.Condition, error) {
	return appsv1alpha1.NewHorizontalScalingCondition(opsRes.OpsRequest), nil
}

// Action modifies Cluster.spec.components[*].replicas from the opsRequest
func (hs horizontalScalingOpsHandler) Action(reqCtx intctrlutil.RequestCtx, cli client.Client, opsRes *OpsResource) error {
	if slices.Contains([]appsv1alpha1.ClusterPhase{appsv1alpha1.StoppedClusterPhase,
		appsv1alpha1.StoppingClusterPhase}, opsRes.Cluster.Status.Phase) {
		return intctrlutil.NewFatalError("please start the cluster before scaling the cluster horizontally")
	}
	compOpsSet := newComponentOpsHelper(opsRes.OpsRequest.Spec.HorizontalScalingList)
	// abort earlier running horizontal scaling opsRequest.
	if err := abortEarlierOpsRequestWithSameKind(reqCtx, cli, opsRes, []appsv1alpha1.OpsType{appsv1alpha1.HorizontalScalingType, appsv1alpha1.StartType},
		func(earlierOps *appsv1alpha1.OpsRequest) (bool, error) {
			if slices.Contains([]appsv1alpha1.OpsType{appsv1alpha1.StartType, appsv1alpha1.StopType}, earlierOps.Spec.Type) {
				return true, nil
			}
			for _, v := range earlierOps.Spec.HorizontalScalingList {
				compOps, ok := compOpsSet.componentOpsSet[v.ComponentName]
				if !ok {
					return false, nil
				}
				currHorizontalScaling := compOps.(appsv1alpha1.HorizontalScaling)
				// abort the opsRequest for overwrite replicas operation.
				if currHorizontalScaling.Replicas != nil || v.Replicas != nil {
					return true, nil
				}
				// if the earlier opsRequest is pending and not `Overwrite` operator, return false.
				if earlierOps.Status.Phase == appsv1alpha1.OpsPendingPhase {
					return false, nil
				}
				// check if the instance to be taken offline was created by another opsRequest.
				if err := hs.checkIntersectionWithEarlierOps(opsRes, earlierOps, currHorizontalScaling, v); err != nil {
					return false, err
				}
			}
			return false, nil
		}); err != nil {
		return err
	}

	if err := compOpsSet.updateClusterComponentsAndShardings(opsRes.Cluster, func(compSpec *appsv1alpha1.ClusterComponentSpec, obj ComponentOpsInterface) error {
		horizontalScaling := obj.(appsv1alpha1.HorizontalScaling)
		lastCompConfiguration := opsRes.OpsRequest.Status.LastConfiguration.Components[obj.GetComponentName()]

		if err := hs.validateHorizontalScalingWithPolicy(opsRes, lastCompConfiguration, obj); err != nil {
			return err
		}
		replicas, instances, offlineInstances, err := hs.getExpectedCompValues(opsRes, compSpec.DeepCopy(),
			lastCompConfiguration, horizontalScaling)
		if err != nil {
			return err
		}
		var insReplicas int32
		for _, v := range instances {
			insReplicas += v.GetReplicas()
		}
		if insReplicas > replicas {
			errMsg := fmt.Sprintf(`the total number of replicas for the instance template can not greater than the number of replicas for component "%s" after horizontally scaling`,
				horizontalScaling.ComponentName)
			return intctrlutil.NewFatalError(errMsg)
		}
		compSpec.Replicas = replicas
		compSpec.Instances = instances
		compSpec.OfflineInstances = offlineInstances
		return nil
	}); err != nil {
		return err
	}
	return cli.Update(reqCtx.Ctx, opsRes.Cluster)
}

// ReconcileAction will be performed when action is done and loops till OpsRequest.status.phase is Succeed/Failed.
// the Reconcile function for horizontal scaling opsRequest.
func (hs horizontalScalingOpsHandler) ReconcileAction(reqCtx intctrlutil.RequestCtx, cli client.Client, opsRes *OpsResource) (appsv1alpha1.OpsPhase, time.Duration, error) {
	handleComponentProgress := func(
		reqCtx intctrlutil.RequestCtx,
		cli client.Client,
		opsRes *OpsResource,
		pgRes *progressResource,
		compStatus *appsv1alpha1.OpsRequestComponentStatus) (int32, int32, error) {
		lastCompConfiguration := opsRes.OpsRequest.Status.LastConfiguration.Components[pgRes.compOps.GetComponentName()]
		horizontalScaling := pgRes.compOps.(appsv1alpha1.HorizontalScaling)
		var err error

		// // shoube be monitor all the instances in the horizontal scaling except not exist instances.
		// getMonitorInstanceList := func(opsRes *OpsResource,
		// 	horizontalScaling appsv1alpha1.HorizontalScaling,
		// 	fullCompName string) (map[string]string, map[string]string, error) {
		// 	deletePodSet := map[string]string{}
		// 	createPodSet := map[string]string{}
		// 	clusterName := opsRes.Cluster.Name
		// 	if horizontalScaling.ScaleIn != nil && len(horizontalScaling.ScaleIn.OnlineInstancesToOffline) > 0 {
		// 		for _, v := range horizontalScaling.ScaleIn.OnlineInstancesToOffline {
		// 			deletePodSet[v] = appsv1alpha1.GetInstanceTemplateName(clusterName, fullCompName, v)
		// 		}
		// 	}
		// 	if horizontalScaling.ScaleOut != nil && len(horizontalScaling.ScaleOut.OfflineInstancesToOnline) > 0 {
		// 		for _, v := range horizontalScaling.ScaleOut.OfflineInstancesToOnline {
		// 			createPodSet[v] = appsv1alpha1.GetInstanceTemplateName(clusterName, fullCompName, v)
		// 		}
		// 	}
		// 	if opsRes.OpsRequest.Status.Phase == appsv1alpha1.OpsCancellingPhase {
		// 		// when cancelling this opsRequest, revert the changes.
		// 		return deletePodSet, createPodSet, nil
		// 	}
		// 	return createPodSet, deletePodSet, nil
		// }

		pgRes.createdPodSet, pgRes.deletedPodSet, err = hs.getCreateAndDeletePodSet(opsRes, lastCompConfiguration, *pgRes.clusterComponent, horizontalScaling, pgRes.fullComponentName)
		if err != nil {
			return 0, 0, err
		}
		pgRes.noWaitComponentCompleted = true
		return handleComponentProgressForScalingReplicas(reqCtx, cli, opsRes, pgRes, compStatus)
	}
	compOpsHelper := newComponentOpsHelper(opsRes.OpsRequest.Spec.HorizontalScalingList)
	return compOpsHelper.reconcileActionWithComponentOps(reqCtx, cli, opsRes, "", handleComponentProgress)
}

// SaveLastConfiguration records last configuration to the OpsRequest.status.lastConfiguration
func (hs horizontalScalingOpsHandler) SaveLastConfiguration(reqCtx intctrlutil.RequestCtx, cli client.Client, opsRes *OpsResource) error {
	compOpsHelper := newComponentOpsHelper(opsRes.OpsRequest.Spec.HorizontalScalingList)
	getLastComponentInfo := func(compSpec appsv1alpha1.ClusterComponentSpec, comOps ComponentOpsInterface) appsv1alpha1.LastComponentConfiguration {
		lastCompConfiguration := appsv1alpha1.LastComponentConfiguration{
			Replicas:         pointer.Int32(compSpec.Replicas),
			Instances:        compSpec.Instances,
			OfflineInstances: compSpec.OfflineInstances,
		}
		return lastCompConfiguration
	}
	compOpsHelper.saveLastConfigurations(opsRes, getLastComponentInfo)
	return nil
}

// getCreateAndDeletePodSet gets the pod set that are created and deleted in this opsRequest.
func (hs horizontalScalingOpsHandler) getCreateAndDeletePodSet(opsRes *OpsResource,
	lastCompConfiguration appsv1alpha1.LastComponentConfiguration,
	currCompSpec appsv1alpha1.ClusterComponentSpec,
	horizontalScaling appsv1alpha1.HorizontalScaling,
	fullCompName string) (map[string]string, map[string]string, error) {
	clusterName := opsRes.Cluster.Name
	lastPodSet, err := intctrlcomp.GenerateAllPodNamesToSet(*lastCompConfiguration.Replicas,
		lastCompConfiguration.Instances, lastCompConfiguration.OfflineInstances, clusterName, fullCompName)
	if err != nil {
		return nil, nil, err
	}
	expectReplicas, expectInstanceTpls, expectOfflineInstances, err := hs.getExpectedCompValues(opsRes, &currCompSpec, lastCompConfiguration, horizontalScaling)
	if err != nil {
		return nil, nil, err
	}
	currPodSet, err := intctrlcomp.GenerateAllPodNamesToSet(expectReplicas, expectInstanceTpls,
		expectOfflineInstances, clusterName, fullCompName)
	if err != nil {
		return nil, nil, err
	}
	createPodSet := map[string]string{}
	deletePodSet := map[string]string{}
	for k := range currPodSet {
		if _, ok := lastPodSet[k]; !ok {
			createPodSet[k] = appsv1alpha1.GetInstanceTemplateName(clusterName, fullCompName, k)
		}
	}
	for k := range lastPodSet {
		if _, ok := currPodSet[k]; !ok {
			deletePodSet[k] = appsv1alpha1.GetInstanceTemplateName(clusterName, fullCompName, k)
		}
	}
	if horizontalScaling.ScaleIn != nil && len(horizontalScaling.ScaleIn.OnlineInstancesToOffline) > 0 {
		for _, v := range horizontalScaling.ScaleIn.OnlineInstancesToOffline {
			deletePodSet[v] = appsv1alpha1.GetInstanceTemplateName(clusterName, fullCompName, v)
		}
	}
	if horizontalScaling.ScaleOut != nil && len(horizontalScaling.ScaleOut.OfflineInstancesToOnline) > 0 {
		for _, v := range horizontalScaling.ScaleOut.OfflineInstancesToOnline {
			createPodSet[v] = appsv1alpha1.GetInstanceTemplateName(clusterName, fullCompName, v)
		}
	}
	if opsRes.OpsRequest.Status.Phase == appsv1alpha1.OpsCancellingPhase {
		// when cancelling this opsRequest, revert the changes.
		return deletePodSet, createPodSet, nil
	}
	return createPodSet, deletePodSet, nil
}

// Cancel this function defines the cancel horizontalScaling action.
func (hs horizontalScalingOpsHandler) Cancel(reqCtx intctrlutil.RequestCtx, cli client.Client, opsRes *OpsResource) error {
	compOpsHelper := newComponentOpsHelper(opsRes.OpsRequest.Spec.HorizontalScalingList)
	if err := compOpsHelper.cancelComponentOps(reqCtx.Ctx, cli, opsRes, func(lastConfig *appsv1alpha1.LastComponentConfiguration, comp *appsv1alpha1.ClusterComponentSpec) {
		comp.Replicas = *lastConfig.Replicas
		comp.Instances = lastConfig.Instances
		comp.OfflineInstances = lastConfig.OfflineInstances
	}); err != nil {
		return err
	}
	// delete the running restore resource to release PVC of the pod which will be deleted after cancelling the ops.
	restoreList := &dpv1alpha1.RestoreList{}
	if err := cli.List(reqCtx.Ctx, restoreList, client.InNamespace(opsRes.OpsRequest.Namespace),
		client.MatchingLabels{constant.AppInstanceLabelKey: opsRes.Cluster.Name}); err != nil {
		return err
	}
	for i := range restoreList.Items {
		restore := &restoreList.Items[i]
		if restore.Status.Phase != dpv1alpha1.RestorePhaseRunning {
			continue
		}
		compName := restore.Labels[constant.KBAppComponentLabelKey]
		if _, ok := compOpsHelper.componentOpsSet[compName]; !ok {
			continue
		}
		workloadName := constant.GenerateWorkloadNamePattern(opsRes.Cluster.Name, compName)
		if restore.Spec.Backup.Name != constant.GenerateResourceNameWithScalingSuffix(workloadName) {
			continue
		}
		if err := intctrlutil.BackgroundDeleteObject(cli, reqCtx.Ctx, restore); err != nil {
			return err
		}
		// remove component finalizer
		patch := client.MergeFrom(restore.DeepCopy())
		controllerutil.RemoveFinalizer(restore, constant.DBComponentFinalizerName)
		if err := cli.Patch(reqCtx.Ctx, restore, patch); err != nil {
			return err
		}
	}
	return nil
}

// checkIntersectionWithEarlierOps checks if the pod deleted by the current ops is a pod created by another ops
func (hs horizontalScalingOpsHandler) checkIntersectionWithEarlierOps(opsRes *OpsResource, earlierOps *appsv1alpha1.OpsRequest,
	currOpsHScaling, earlierOpsHScaling appsv1alpha1.HorizontalScaling) error {
	getCreatedOrDeletedPodSet := func(ops *appsv1alpha1.OpsRequest, hScaling appsv1alpha1.HorizontalScaling) (map[string]string, map[string]string, error) {
		lastCompSnapshot := ops.Status.LastConfiguration.Components[earlierOpsHScaling.ComponentName]
		compSpec := getComponentSpecOrShardingTemplate(opsRes.Cluster, earlierOpsHScaling.ComponentName).DeepCopy()
		var err error
		compSpec.Replicas, compSpec.Instances, compSpec.OfflineInstances, err = hs.getExpectedCompValues(opsRes, compSpec, lastCompSnapshot, hScaling)
		if err != nil {
			return nil, nil, err
		}
		return hs.getCreateAndDeletePodSet(opsRes, lastCompSnapshot, *compSpec, hScaling, hScaling.ComponentName)
	}
	createdPodSetForEarlier, _, err := getCreatedOrDeletedPodSet(earlierOps, earlierOpsHScaling)
	if err != nil {
		return err
	}
	_, deletedPodSetForCurrent, err := getCreatedOrDeletedPodSet(opsRes.OpsRequest, currOpsHScaling)
	if err != nil {
		return err
	}
	for deletedPod := range deletedPodSetForCurrent {
		if _, ok := createdPodSetForEarlier[deletedPod]; ok {
			errMsg := fmt.Sprintf(`instance "%s" cannot be taken offline as it has been created by another running opsRequest "%s"`,
				deletedPod, earlierOps.Name)
			return intctrlutil.NewFatalError(errMsg)
		}
	}
	return nil
}

// getExpectedCompValues gets the expected replicas, instances, offlineInstances.
func (hs horizontalScalingOpsHandler) getExpectedCompValues(
	opsRes *OpsResource,
	compSpec *appsv1alpha1.ClusterComponentSpec,
	lastCompConfiguration appsv1alpha1.LastComponentConfiguration,
	horizontalScaling appsv1alpha1.HorizontalScaling) (int32, []appsv1alpha1.InstanceTemplate, []string, error) {
	compReplicas := compSpec.Replicas
	compInstanceTpls := compSpec.Instances
	compOfflineInstances := compSpec.OfflineInstances
	if horizontalScaling.Replicas == nil {
		compReplicas = *lastCompConfiguration.Replicas
		compInstanceTpls = slices.Clone(lastCompConfiguration.Instances)
		compOfflineInstances = lastCompConfiguration.OfflineInstances
	}
	filteredHorizontal, err := filterHorizontalScalingSpec(opsRes, compReplicas, compInstanceTpls, compOfflineInstances, horizontalScaling.DeepCopy())
	if err != nil {
		return 0, nil, nil, err
	}
	expectOfflineInstances := hs.getCompExpectedOfflineInstances(compOfflineInstances, *filteredHorizontal)
	err = hs.autoSyncReplicaChanges(opsRes, *filteredHorizontal, compReplicas, compInstanceTpls, expectOfflineInstances)
	if err != nil {
		return 0, nil, nil, err
	}
	return hs.getCompExpectReplicas(*filteredHorizontal, compReplicas),
		hs.getCompExpectedInstances(compInstanceTpls, *filteredHorizontal),
		expectOfflineInstances, nil
}

// only offlined instances could be taken online.
// and only onlined instances could be taken offline.
func filterHorizontalScalingSpec(
	opsRes *OpsResource,
	compReplicas int32,
	compInstanceTpls []appsv1alpha1.InstanceTemplate,
	compOfflineInstances []string,
	horizontalScaling *appsv1alpha1.HorizontalScaling) (*appsv1alpha1.HorizontalScaling, error) {
	offlineInstances := sets.New(compOfflineInstances...)
	podSet, err := intctrlcomp.GenerateAllPodNamesToSet(compReplicas, compInstanceTpls, compOfflineInstances,
		opsRes.Cluster.Name, horizontalScaling.ComponentName)
	if err != nil {
		return nil, err
	}
	if horizontalScaling.ScaleIn != nil && len(horizontalScaling.ScaleIn.OnlineInstancesToOffline) > 0 {
		onlinedInstanceFromOps := sets.Set[string]{}
		for _, insName := range horizontalScaling.ScaleIn.OnlineInstancesToOffline {
			if _, ok := podSet[insName]; ok {
				onlinedInstanceFromOps.Insert(insName)
			}
		}
		horizontalScaling.ScaleIn.OnlineInstancesToOffline = onlinedInstanceFromOps.UnsortedList()
	}
	if horizontalScaling.ScaleOut != nil && len(horizontalScaling.ScaleOut.OfflineInstancesToOnline) > 0 {
		offlinedInstanceFromOps := sets.Set[string]{}
		for _, insName := range horizontalScaling.ScaleOut.OfflineInstancesToOnline {
			if _, ok := offlineInstances[insName]; ok {
				offlinedInstanceFromOps.Insert(insName)
			}
		}
		horizontalScaling.ScaleOut.OfflineInstancesToOnline = offlinedInstanceFromOps.UnsortedList()
	}
	return horizontalScaling, nil

}

// autoSyncReplicaChanges auto-sync the replicaChanges of the component and instance templates.
func (hs horizontalScalingOpsHandler) autoSyncReplicaChanges(
	opsRes *OpsResource,
	horizontalScaling appsv1alpha1.HorizontalScaling,
	compReplicas int32,
	compInstanceTpls []appsv1alpha1.InstanceTemplate,
	compExpectOfflineInstances []string) error {
	// sync the replicaChanges for component and instance template.
	getSyncedInstancesAndReplicaChanges := func(offlineOrOnlineInsCountMap map[string]int32,
		replicaChanger appsv1alpha1.ReplicaChanger,
		newInstances []appsv1alpha1.InstanceTemplate) ([]appsv1alpha1.InstanceReplicasTemplate, *int32) {
		allReplicaChanges := int32(0)
		insTplMap := map[string]sets.Empty{}
		for _, v := range replicaChanger.Instances {
			insTplMap[v.Name] = sets.Empty{}
			allReplicaChanges += v.ReplicaChanges
		}
		for k, v := range offlineOrOnlineInsCountMap {
			if k == "" {
				allReplicaChanges += v
				continue
			}
			if _, ok := insTplMap[k]; !ok {
				replicaChanger.Instances = append(replicaChanger.Instances, appsv1alpha1.InstanceReplicasTemplate{Name: k, ReplicaChanges: v})
				allReplicaChanges += v
			}
		}
		for _, v := range newInstances {
			allReplicaChanges += v.GetReplicas()
		}
		if replicaChanger.ReplicaChanges != nil {
			allReplicaChanges = *replicaChanger.ReplicaChanges
		}
		return replicaChanger.Instances, &allReplicaChanges
	}

	// auto sync the replicaChanges.
	scaleIn := horizontalScaling.ScaleIn
	if scaleIn != nil {
		offlineInsCountMap := opsRes.OpsRequest.CountOfflineOrOnlineInstances(opsRes.Cluster.Name, horizontalScaling.ComponentName, scaleIn.OnlineInstancesToOffline)
		scaleIn.Instances, scaleIn.ReplicaChanges = getSyncedInstancesAndReplicaChanges(offlineInsCountMap, scaleIn.ReplicaChanger, nil)
	}
	scaleOut := horizontalScaling.ScaleOut
	if scaleOut != nil {
		onlineInsCountMap := opsRes.OpsRequest.CountOfflineOrOnlineInstances(opsRes.Cluster.Name, horizontalScaling.ComponentName, scaleOut.OfflineInstancesToOnline)
		scaleOut.Instances, scaleOut.ReplicaChanges = getSyncedInstancesAndReplicaChanges(onlineInsCountMap, scaleOut.ReplicaChanger, scaleOut.NewInstances)
	}
	return nil
}

// getCompExpectReplicas gets the expected replicas for the component.
func (hs horizontalScalingOpsHandler) getCompExpectReplicas(horizontalScaling appsv1alpha1.HorizontalScaling,
	compReplicas int32) int32 {
	if horizontalScaling.Replicas != nil {
		return *horizontalScaling.Replicas
	}
	if horizontalScaling.ScaleOut != nil && horizontalScaling.ScaleOut.ReplicaChanges != nil {
		compReplicas += *horizontalScaling.ScaleOut.ReplicaChanges
	}
	if horizontalScaling.ScaleIn != nil && horizontalScaling.ScaleIn.ReplicaChanges != nil {
		compReplicas -= *horizontalScaling.ScaleIn.ReplicaChanges
	}
	return compReplicas
}

// getCompExpectedOfflineInstances gets the expected instance templates of the component.
func (hs horizontalScalingOpsHandler) getCompExpectedInstances(
	compInstanceTpls []appsv1alpha1.InstanceTemplate,
	horizontalScaling appsv1alpha1.HorizontalScaling,
) []appsv1alpha1.InstanceTemplate {
	if horizontalScaling.Replicas != nil {
		return compInstanceTpls
	}
	compInsTplSet := map[string]int{}
	for i := range compInstanceTpls {
		compInsTplSet[compInstanceTpls[i].Name] = i
	}
	handleInstanceTplReplicaChanges := func(instances []appsv1alpha1.InstanceReplicasTemplate, isScaleIn bool) {
		for _, v := range instances {
			compInsIndex, ok := compInsTplSet[v.Name]
			if !ok {
				continue
			}
			if isScaleIn {
				compInstanceTpls[compInsIndex].Replicas = pointer.Int32(compInstanceTpls[compInsIndex].GetReplicas() - v.ReplicaChanges)
			} else {
				compInstanceTpls[compInsIndex].Replicas = pointer.Int32(compInstanceTpls[compInsIndex].GetReplicas() + v.ReplicaChanges)
			}
		}
	}
	if horizontalScaling.ScaleOut != nil {
		compInstanceTpls = append(compInstanceTpls, horizontalScaling.ScaleOut.NewInstances...)
		handleInstanceTplReplicaChanges(horizontalScaling.ScaleOut.Instances, false)
	}
	if horizontalScaling.ScaleIn != nil {
		handleInstanceTplReplicaChanges(horizontalScaling.ScaleIn.Instances, true)
	}
	return compInstanceTpls
}

// getCompExpectedOfflineInstances gets the expected offlineInstances of the component.
func (hs horizontalScalingOpsHandler) getCompExpectedOfflineInstances(
	compOfflineInstances []string,
	horizontalScaling appsv1alpha1.HorizontalScaling,
) []string {
	handleOfflineInstances := func(baseInstanceNames, comparedInstanceNames, newOfflineInstances []string) []string {
		instanceNameSet := sets.New(comparedInstanceNames...)
		for _, instanceName := range baseInstanceNames {
			if _, ok := instanceNameSet[instanceName]; !ok {
				newOfflineInstances = append(newOfflineInstances, instanceName)
			}
		}
		return newOfflineInstances
	}
	if horizontalScaling.ScaleIn != nil && len(horizontalScaling.ScaleIn.OnlineInstancesToOffline) > 0 {
		compOfflineInstances = handleOfflineInstances(horizontalScaling.ScaleIn.OnlineInstancesToOffline, compOfflineInstances, compOfflineInstances)
	}
	if horizontalScaling.ScaleOut != nil && len(horizontalScaling.ScaleOut.OfflineInstancesToOnline) > 0 {
		compOfflineInstances = handleOfflineInstances(compOfflineInstances, horizontalScaling.ScaleOut.OfflineInstancesToOnline, make([]string, 0))
	}
	return compOfflineInstances
}

// validate if there is any instance specified in the request that is not exist, return error.
// if HscaleValidatePolicy is StrictScalePolicy or empty, it would validate the instances if they are already offlined or onlined.
func (hs horizontalScalingOpsHandler) validateHorizontalScalingWithPolicy(
	opsRes *OpsResource,
	lastCompConfiguration appsv1alpha1.LastComponentConfiguration,
	obj ComponentOpsInterface,
) error {
	horizontalScaling := obj.(appsv1alpha1.HorizontalScaling)
	currPodSet, err := intctrlcomp.GenerateAllPodNamesToSet(*lastCompConfiguration.Replicas, lastCompConfiguration.Instances, lastCompConfiguration.OfflineInstances,
		opsRes.Cluster.Name, obj.GetComponentName())
	if err != nil {
		return err
	}
	offlineInstances := sets.New(lastCompConfiguration.OfflineInstances...)
	onlinedInstanceFromScaleInOps, offlinedInstanceFromScaleOutOps, notExistInstanceFromOps := hs.collecteAllTypeOfInstancesFromOps(horizontalScaling, currPodSet, offlineInstances)
	if notExistInstanceFromOps.Len() > 0 {
		return intctrlutil.NewFatalError(fmt.Sprintf(`instances "%s" specified in the request is not exist`, strings.Join(notExistInstanceFromOps.UnsortedList(), ", ")))
	}

	if policy, exist := opsRes.OpsRequest.Annotations[constant.HscaleValidatePolicyKey]; exist && policy != constant.HscaleValidatePolicyStrict {
		return nil
	}

	if err := hs.strictPolicyValidation(horizontalScaling, onlinedInstanceFromScaleInOps, offlinedInstanceFromScaleOutOps); err != nil {
		return err
	}

	return nil
}

// collecteAllTypeOfInstancesFromOps collects the online and offline instances specified in the request.
func (hs horizontalScalingOpsHandler) collecteAllTypeOfInstancesFromOps(
	horizontalScaling appsv1alpha1.HorizontalScaling,
	currPodSet map[string]string,
	offlineInstances sets.Set[string]) (onlinedInstanceFromScaleInOps, offlinedInstanceFromScaleOutOps, notExistInstanceFromOps sets.Set[string]) {
	if horizontalScaling.ScaleIn != nil && len(horizontalScaling.ScaleIn.OnlineInstancesToOffline) > 0 {
		notExistInstanceFromScaleIn := sets.Set[string]{}
		onlinedInstanceFromScaleInOps, _, notExistInstanceFromScaleIn = hs.collectOnlineAndOfflineAndNotExistInstances(
			horizontalScaling.ScaleIn.OnlineInstancesToOffline,
			offlineInstances,
			currPodSet)
		if notExistInstanceFromScaleIn.Len() > 0 {
			notExistInstanceFromOps = notExistInstanceFromOps.Union(notExistInstanceFromScaleIn)
		}
	}
	if horizontalScaling.ScaleOut != nil && len(horizontalScaling.ScaleOut.OfflineInstancesToOnline) > 0 {
		notExistInstanceFromScaleOut := sets.Set[string]{}
		_, offlinedInstanceFromScaleOutOps, notExistInstanceFromScaleOut = hs.collectOnlineAndOfflineAndNotExistInstances(
			horizontalScaling.ScaleOut.OfflineInstancesToOnline,
			offlineInstances,
			currPodSet)
		if notExistInstanceFromScaleOut.Len() > 0 {
			notExistInstanceFromOps = notExistInstanceFromOps.Union(notExistInstanceFromScaleOut)
		}
	}
	return
}

// collect the online and offline instances specified in the request.
func (hs horizontalScalingOpsHandler) collectOnlineAndOfflineAndNotExistInstances(
	instance []string,
	offlineInstances sets.Set[string],
	currPodSet map[string]string) (sets.Set[string], sets.Set[string], sets.Set[string]) {

	offlinedInstanceFromOps := sets.Set[string]{}
	onlinedInstanceFromOps := sets.Set[string]{}
	notExistInstanceFromOps := sets.Set[string]{}
	for _, insName := range instance {
		if _, ok := offlineInstances[insName]; ok {
			offlinedInstanceFromOps.Insert(insName)
			continue
		}
		if _, ok := currPodSet[insName]; ok {
			onlinedInstanceFromOps.Insert(insName)
			continue
		}
		notExistInstanceFromOps.Insert(insName)
	}
	return onlinedInstanceFromOps, offlinedInstanceFromOps, notExistInstanceFromOps
}

// check when setting strict validate policy
// if the instances specified in the request are not offline, return error.
// if the instances duplicate in the request, return error.
func (hs horizontalScalingOpsHandler) strictPolicyValidation(
	horizontalScaling appsv1alpha1.HorizontalScaling,
	onlinedInstanceFromScaleInOps, offlinedInstanceFromScaleOutOps sets.Set[string]) error {

	if horizontalScaling.ScaleIn != nil && len(horizontalScaling.ScaleIn.OnlineInstancesToOffline) > 0 {
		if onlinedInstanceFromScaleInOps.Len() != len(horizontalScaling.ScaleIn.OnlineInstancesToOffline) {
			unscalablePods := getMissingElementsInSetFromList(onlinedInstanceFromScaleInOps, horizontalScaling.ScaleIn.OnlineInstancesToOffline)
			if unscalablePods == nil {
				return intctrlutil.NewFatalError("instances specified in onlineInstancesToOffline has duplicates")
			}
			return intctrlutil.NewFatalError(fmt.Sprintf(`instances "%s" specified in onlineInstancesToOffline is not online or not exist`, strings.Join(unscalablePods, ", ")))
		}
	}
	if horizontalScaling.ScaleOut != nil && len(horizontalScaling.ScaleOut.OfflineInstancesToOnline) > 0 {
		if offlinedInstanceFromScaleOutOps.Len() != len(horizontalScaling.ScaleOut.OfflineInstancesToOnline) {
			unscalablePods := getMissingElementsInSetFromList(offlinedInstanceFromScaleOutOps, horizontalScaling.ScaleOut.OfflineInstancesToOnline)
			if unscalablePods == nil {
				return intctrlutil.NewFatalError("instances specified in onlineInstancesToOffline has duplicates")
			}
			return intctrlutil.NewFatalError(fmt.Sprintf(`instances "%s" specified in offlineInstancesToOnline is not offline or not exist`, strings.Join(unscalablePods, ", ")))
		}
	}
	return nil
}
