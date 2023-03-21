/*
Copyright ApeCloud, Inc.

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

package lifecycle

import (
	"k8s.io/apimachinery/pkg/util/sets"
	"reflect"

	"sigs.k8s.io/controller-runtime/pkg/client"

	appsv1alpha1 "github.com/apecloud/kubeblocks/apis/apps/v1alpha1"
	"github.com/apecloud/kubeblocks/internal/controller/component"
	"github.com/apecloud/kubeblocks/internal/controller/graph"
	intctrlutil "github.com/apecloud/kubeblocks/internal/controllerutil"
)

// componentTransformer transforms all components to a K8s objects DAG
// TODO: remove cli and ctx, we should read all objects needed, and then do pure objects computation
// TODO: only replication set left
type componentTransformer struct {
	cc  clusterRefResources
	cli client.Client
	ctx intctrlutil.RequestCtx
}

func (c *componentTransformer) Transform(dag *graph.DAG) error {
	rootVertex, err := findRootVertex(dag)
	if err != nil {
		return err
	}
	origCluster, _ := rootVertex.oriObj.(*appsv1alpha1.Cluster)
	cluster, _ := rootVertex.obj.(*appsv1alpha1.Cluster)

	// return fast when cluster is deleting
	if isClusterDeleting(*origCluster) {
		return nil
	}

	compSpecMap := make(map[string]appsv1alpha1.ClusterComponentSpec)
	for _, spec := range cluster.Spec.ComponentSpecs {
		compSpecMap[spec.Name] = spec
	}
	compProto := sets.KeySet(compSpecMap)
	// TODO: should review that whether it is reasonable and correct to use component status
	compStatus := sets.KeySet(cluster.Status.Components)

	createSet := compProto.Difference(compStatus)
	updateSet := compProto.Intersection(compStatus)
	deleteSet := compStatus.Difference(compProto)

	resources := make([]client.Object, 0)
	for compName := range createSet {
		comp := component.NewComponent(c.cc.cd, c.cc.cv, *cluster, compSpecMap[compName], dag)
		if err := comp.Create(c.ctx, c.cli); err != nil {
			return err
		}
	}

	for compName := range deleteSet {
		comp := component.NewComponent(c.cc.cd, c.cc.cv, *cluster, compSpecMap[compName], dag)
		if err := comp.Delete(c.ctx, c.cli); err != nil {
			return err
		}
	}

	for compName := range updateSet {
		comp := component.NewComponent(c.cc.cd, c.cc.cv, *cluster, compSpecMap[compName], dag)
		if err := comp.Update(c.ctx, c.cli); err != nil {
			return err
		}
	}

	// replication set will create duplicate env configmap and headless service
	// TODO: fix it within replication set
	for _, object := range dedupResources(resources) {
		vertex := &lifecycleVertex{obj: object}
		dag.AddVertex(vertex)
		dag.Connect(rootVertex, vertex)
	}
	return nil
}

func dedupResources(resources []client.Object) []client.Object {
	objects := make([]client.Object, 0)
	for _, resource := range resources {
		contains := false
		for _, object := range objects {
			if reflect.DeepEqual(resource, object) {
				contains = true
				break
			}
		}
		if !contains {
			objects = append(objects, resource)
		}
	}
	return objects
}
