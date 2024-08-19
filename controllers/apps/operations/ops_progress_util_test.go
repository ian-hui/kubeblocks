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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/pointer"
	"sigs.k8s.io/controller-runtime/pkg/client"

	appsv1alpha1 "github.com/apecloud/kubeblocks/apis/apps/v1alpha1"
	opsutil "github.com/apecloud/kubeblocks/controllers/apps/operations/util"
	"github.com/apecloud/kubeblocks/pkg/constant"
	intctrlutil "github.com/apecloud/kubeblocks/pkg/controllerutil"
	"github.com/apecloud/kubeblocks/pkg/generics"
	testapps "github.com/apecloud/kubeblocks/pkg/testutil/apps"
	testk8s "github.com/apecloud/kubeblocks/pkg/testutil/k8s"
)

var _ = Describe("Ops ProgressDetails", func() {

	var (
		randomStr             = testCtx.GetRandomStr()
		clusterDefinitionName = "cluster-definition-for-ops-" + randomStr
		clusterVersionName    = "clusterversion-for-ops-" + randomStr
		clusterName           = "cluster-for-ops-" + randomStr
	)

	cleanEnv := func() {
		// must wait till resources deleted and no longer existed before the testcases start,
		// otherwise if later it needs to create some new resource objects with the same name,
		// in race conditions, it will find the existence of old objects, resulting failure to
		// create the new objects.
		By("clean resources")

		// delete cluster(and all dependent sub-resources), clusterversion and clusterdef
		testapps.ClearClusterResources(&testCtx)

		// delete rest resources
		inNS := client.InNamespace(testCtx.DefaultNamespace)
		ml := client.HasLabels{testCtx.TestObjLabelKey}
		// namespaced
		testapps.ClearResources(&testCtx, generics.OpsRequestSignature, inNS, ml)
		testapps.ClearResourcesWithRemoveFinalizerOption(&testCtx, generics.InstanceSetSignature, true, inNS, ml)
		// default GracePeriod is 30s
		testapps.ClearResources(&testCtx, generics.PodSignature, inNS, ml, client.GracePeriodSeconds(0))
	}

	BeforeEach(cleanEnv)

	AfterEach(cleanEnv)

	initClusterForOps := func(opsRes *OpsResource) {
		Expect(opsutil.UpdateClusterOpsAnnotations(ctx, k8sClient, opsRes.Cluster, nil)).Should(Succeed())
		opsRes.Cluster.Status.Phase = appsv1alpha1.RunningClusterPhase
	}

	testProgressDetailsWithStatefulPodUpdating := func(reqCtx intctrlutil.RequestCtx, opsRes *OpsResource, consensusPodList []corev1.Pod) {
		By("mock pod of InstanceSet updating by deleting the pod")
		pod := consensusPodList[0]
		testk8s.MockPodIsTerminating(ctx, testCtx, &pod)
		_, _ = GetOpsManager().Reconcile(reqCtx, k8sClient, opsRes)
		Expect(getProgressDetailStatus(opsRes, consensusComp, &pod)).Should(Equal(appsv1alpha1.ProcessingProgressStatus))

		By("mock one pod of InstanceSet to update successfully")
		testk8s.RemovePodFinalizer(ctx, testCtx, &pod)
		testapps.MockInstanceSetPod(&testCtx, nil, clusterName, consensusComp,
			pod.Name, "leader", "ReadWrite")

		_, _ = GetOpsManager().Reconcile(reqCtx, k8sClient, opsRes)
		Expect(getProgressDetailStatus(opsRes, consensusComp, &pod)).Should(Equal(appsv1alpha1.SucceedProgressStatus))
		Expect(opsRes.OpsRequest.Status.Progress).Should(Equal("1/4"))
	}

	Context("Test Ops ProgressDetails", func() {
		It("Test Ops ProgressDetails for rolling update", func() {
			By("init operations resources ")
			reqCtx := intctrlutil.RequestCtx{Ctx: testCtx.Ctx}
			opsRes, _, _ := initOperationsResources(clusterDefinitionName, clusterVersionName, clusterName)

			By("create restart ops and pods of consensus component")
			opsRes.OpsRequest = createRestartOpsObj(clusterName, "restart-"+randomStr)
			mockComponentIsOperating(opsRes.Cluster, appsv1alpha1.UpdatingClusterCompPhase, consensusComp, statelessComp)
			podList := initInstanceSetPods(ctx, k8sClient, opsRes)

			By("mock restart OpsRequest is Running")
			_, err := GetOpsManager().Do(reqCtx, k8sClient, opsRes)
			Expect(err).ShouldNot(HaveOccurred())
			Eventually(testapps.GetOpsRequestPhase(&testCtx, client.ObjectKeyFromObject(opsRes.OpsRequest))).Should(Equal(appsv1alpha1.OpsCreatingPhase))

			By("test the progressDetails when stateful pod updates during restart operation")
			testProgressDetailsWithStatefulPodUpdating(reqCtx, opsRes, podList)
		})

		It("Test Ops ProgressDetails with horizontally scaling replicas", func() {
			By("init operations resources ")
			reqCtx := intctrlutil.RequestCtx{Ctx: testCtx.Ctx}
			opsRes, _, _ := initOperationsResources(clusterDefinitionName, clusterVersionName, clusterName)
			its := testapps.MockInstanceSetComponent(&testCtx, clusterName, consensusComp)
			podList := testapps.MockInstanceSetPods(&testCtx, its, opsRes.Cluster, consensusComp)

			By("create horizontalScaling operation to test the progressDetails when scaling in the replicas")
			opsRes.OpsRequest = createHorizontalScaling(clusterName, appsv1alpha1.HorizontalScaling{
				ComponentOps: appsv1alpha1.ComponentOps{ComponentName: consensusComp},
				Replicas:     pointer.Int32(1),
			}, constant.HscaleValidatePolicyStrict)
			mockComponentIsOperating(opsRes.Cluster, appsv1alpha1.UpdatingClusterCompPhase, consensusComp) // appsv1alpha1.HorizontalScalingPhase
			initClusterForOps(opsRes)

			By("mock HorizontalScaling OpsRequest phase is running")
			_, err := GetOpsManager().Do(reqCtx, k8sClient, opsRes)
			Expect(err).ShouldNot(HaveOccurred())
			Eventually(testapps.GetOpsRequestPhase(&testCtx, client.ObjectKeyFromObject(opsRes.OpsRequest))).Should(Equal(appsv1alpha1.OpsCreatingPhase))
			// do h-scale action
			_, err = GetOpsManager().Do(reqCtx, k8sClient, opsRes)
			Expect(err).ShouldNot(HaveOccurred())

			By("mock the pod is terminating, pod[1] is target pod to delete. and mock pod[2] is failed and deleted by stateful controller")
			for i := 1; i < 3; i++ {
				pod := podList[i]
				testk8s.MockPodIsTerminating(ctx, testCtx, pod)
				testapps.MockInstanceSetStatus(testCtx, opsRes.Cluster, consensusComp)
				_, _ = GetOpsManager().Reconcile(reqCtx, k8sClient, opsRes)
				Expect(getProgressDetailStatus(opsRes, consensusComp, pod)).Should(Equal(appsv1alpha1.ProcessingProgressStatus))

			}
			By("mock the target pod is deleted and progressDetail status should be succeed")
			targetPod := podList[1]
			testk8s.RemovePodFinalizer(ctx, testCtx, targetPod)
			testapps.MockInstanceSetStatus(testCtx, opsRes.Cluster, consensusComp)
			_, _ = GetOpsManager().Reconcile(reqCtx, k8sClient, opsRes)
			Expect(getProgressDetailStatus(opsRes, consensusComp, targetPod)).Should(Equal(appsv1alpha1.SucceedProgressStatus))
			Expect(opsRes.OpsRequest.Status.Progress).Should(Equal("1/2"))

			By("delete the pod[2]")
			pod := podList[2]
			testk8s.RemovePodFinalizer(ctx, testCtx, pod)
			// expect the progress is 2/2
			testapps.MockInstanceSetStatus(testCtx, opsRes.Cluster, consensusComp)
			_, _ = GetOpsManager().Reconcile(reqCtx, k8sClient, opsRes)
			Expect(getProgressDetailStatus(opsRes, consensusComp, targetPod)).Should(Equal(appsv1alpha1.SucceedProgressStatus))
			Expect(opsRes.OpsRequest.Status.Progress).Should(Equal("2/2"))

			By("create horizontalScaling operation to test the progressDetails when scaling out the replicas ")
			initClusterForOps(opsRes)
			expectClusterComponentReplicas := int32(2)
			Expect(testapps.ChangeObj(&testCtx, opsRes.Cluster, func(lcluster *appsv1alpha1.Cluster) {
				lcluster.Spec.ComponentSpecs[1].Replicas = expectClusterComponentReplicas
			})).ShouldNot(HaveOccurred())
			// ops will use the startTimestamp to make decision, start time should not equal the pod createTime during testing.
			time.Sleep(time.Second)
			opsRes.OpsRequest = createHorizontalScaling(clusterName, appsv1alpha1.HorizontalScaling{
				ComponentOps: appsv1alpha1.ComponentOps{ComponentName: consensusComp},
				Replicas:     pointer.Int32(3),
			}, constant.HscaleValidatePolicyStrict)
			mockComponentIsOperating(opsRes.Cluster, appsv1alpha1.UpdatingClusterCompPhase, consensusComp, statelessComp)
			// update ops phase to Running first
			_, err = GetOpsManager().Do(reqCtx, k8sClient, opsRes)
			Expect(err).ShouldNot(HaveOccurred())
			Eventually(testapps.GetOpsRequestPhase(&testCtx, client.ObjectKeyFromObject(opsRes.OpsRequest))).Should(Equal(appsv1alpha1.OpsCreatingPhase))
			// do h-scale cluster
			_, err = GetOpsManager().Do(reqCtx, k8sClient, opsRes)
			Expect(err).ShouldNot(HaveOccurred())

			By("test the progressDetails when scaling out replicas")
			targetPod = podList[2]
			testapps.MockInstanceSetPod(&testCtx, nil, clusterName, consensusComp,
				targetPod.Name, "follower", "Readonly")
			Expect(k8sClient.Get(ctx, client.ObjectKey{Name: targetPod.Name, Namespace: testCtx.DefaultNamespace}, targetPod)).Should(Succeed())
			testapps.MockInstanceSetStatus(testCtx, opsRes.Cluster, consensusComp)
			_, _ = GetOpsManager().Reconcile(reqCtx, k8sClient, opsRes)
			Expect(getProgressDetailStatus(opsRes, consensusComp, targetPod)).Should(Equal(appsv1alpha1.SucceedProgressStatus))
			Expect(opsRes.OpsRequest.Status.Progress).Should(Equal("1/1"))
		})
	})
})

func getProgressDetailStatus(opsRes *OpsResource, componentName string, pod *corev1.Pod) appsv1alpha1.ProgressStatus {
	objectKey := getProgressObjectKey(constant.PodKind, pod.Name)
	progressDetails := opsRes.OpsRequest.Status.Components[componentName].ProgressDetails
	progressDetail := findStatusProgressDetail(progressDetails, objectKey)
	var status appsv1alpha1.ProgressStatus
	if progressDetail != nil {
		status = progressDetail.Status
	}
	return status
}
