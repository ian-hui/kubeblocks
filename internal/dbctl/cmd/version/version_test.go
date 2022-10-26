/*
Copyright 2022 The KubeBlocks Authors

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package version

import (
	"context"
	"fmt"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/dynamic"
	cmdtesting "k8s.io/kubectl/pkg/cmd/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/apecloud/kubeblocks/internal/dbctl/types"
)

var _ = Describe("version", func() {

	createKubeBlocksDeplyment := func() {
		deployYaml := fmt.Sprintf(`
apiVersion: apps/v1
kind: Deployment
metadata:
  annotations:
    deployment.kubernetes.io/revision: "1"
    meta.helm.sh/release-name: opendbaas-core
    meta.helm.sh/release-namespace: default
  labels:
    app.kubernetes.io/instance: opendbaas-core
    app.kubernetes.io/managed-by: Helm
    app.kubernetes.io/name: %s
    app.kubernetes.io/version: 0.2.0-wyl
    helm.sh/chart: opendbaas-core-0.1.0-wyl
  name: opendbaas-core
  namespace: default
spec:
  progressDeadlineSeconds: 600
  replicas: 1
  revisionHistoryLimit: 10
  selector:
    matchLabels:
      app.kubernetes.io/instance: opendbaas-core
      app.kubernetes.io/name: opendbaas-core
  template:
    metadata:
      creationTimestamp: null
      labels:
        app.kubernetes.io/instance: opendbaas-core
        app.kubernetes.io/name: opendbaas-core
    spec:
      containers:
      - args:
        - --health-probe-bind-address=:8081
        - --metrics-bind-address=:8080
        - --leader-elect
        - -zap-encoder=console
        - -zap-time-encoding=iso8601
        - -zap-devel=true
        env:
        - name: ENABLE_WEBHOOKS
          value: "true"
        image: docker.io/wangyelei/opendbaas-core:0.1.0-wyl
        imagePullPolicy: IfNotPresent
        livenessProbe:
          failureThreshold: 3
          httpGet:
            path: /healthz
            port: health
            scheme: HTTP
          initialDelaySeconds: 15
          periodSeconds: 20
          successThreshold: 1
          timeoutSeconds: 1
        name: manager
        ports:
        - containerPort: 9443
          name: webhook-server
          protocol: TCP
        - containerPort: 8081
          name: health
          protocol: TCP
        - containerPort: 8080
          name: metrics
          protocol: TCP
        readinessProbe:
          failureThreshold: 3
          httpGet:
            path: /readyz
            port: health
            scheme: HTTP
          initialDelaySeconds: 5
          periodSeconds: 10
          successThreshold: 1
          timeoutSeconds: 1
        resources: {}
        securityContext:
          allowPrivilegeEscalation: false
          capabilities:
            drop:
            - ALL
        terminationMessagePath: /dev/termination-log
        terminationMessagePolicy: File
        volumeMounts:
        - mountPath: /tmp/k8s-webhook-server/serving-certs
          name: cert
          readOnly: true
      dnsPolicy: ClusterFirst
      restartPolicy: Always
      schedulerName: default-scheduler
      securityContext:
        runAsNonRoot: true
      serviceAccount: opendbaas-core
      serviceAccountName: opendbaas-core
      terminationGracePeriodSeconds: 10
      volumes:
      - configMap:
          defaultMode: 420
          name: manager-config
        name: manager-config
      - name: cert
        secret:
          defaultMode: 420
          secretName: opendbaas-core.default.svc.tls-pair
`, types.KubeBlocksChartName)
		deployment := &appsv1.Deployment{}
		_ = yaml.Unmarshal([]byte(deployYaml), deployment)
		Expect(k8sClient.Create(context.Background(), deployment)).To(Succeed())
		Eventually(func() bool {
			tmpDeploy := &appsv1.Deployment{}
			err := k8sClient.Get(context.Background(), client.ObjectKey{Name: deployment.Name, Namespace: deployment.Namespace}, tmpDeploy)
			return err == nil
		}).Should(BeTrue())
	}

	Context("version command", func() {
		It("version", func() {
			tf := cmdtesting.NewTestFactory()
			By("create KubeBlocks deployment")
			createKubeBlocksDeplyment()

			By("testing version command")
			cmd := NewVersionCmd(tf)
			_ = cmd.Flags().Set("verbose", "true")
			cmd.Run(cmd, []string{})

			By("testing init KubeBlocks version")
			o := &versionOptions{}
			o.client, _ = dynamic.NewForConfig(cfg)
			o.initKubeBlocksVersion()
			Expect(len(o.kubeBlocksVersions) > 0).To(Equal(true))

			By("testing print version")
			o.k8sServerVersion = "v1.24.0"
			o.Run()
		})
	})
})
