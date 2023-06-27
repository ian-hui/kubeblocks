/*
Copyright (C) 2022-2023 ApeCloud Co., Ltd

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

// Code generated by client-gen. DO NOT EDIT.

package fake

import (
	"context"

	v1alpha1 "github.com/apecloud/kubeblocks/apis/dataprotection/v1alpha1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	labels "k8s.io/apimachinery/pkg/labels"
	schema "k8s.io/apimachinery/pkg/runtime/schema"
	types "k8s.io/apimachinery/pkg/types"
	watch "k8s.io/apimachinery/pkg/watch"
	testing "k8s.io/client-go/testing"
)

// FakeBackupPolicies implements BackupPolicyInterface
type FakeBackupPolicies struct {
	Fake *FakeDataprotectionV1alpha1
	ns   string
}

var backuppoliciesResource = schema.GroupVersionResource{Group: "dataprotection.kubeblocks.io", Version: "v1alpha1", Resource: "backuppolicies"}

var backuppoliciesKind = schema.GroupVersionKind{Group: "dataprotection.kubeblocks.io", Version: "v1alpha1", Kind: "BackupPolicy"}

// Get takes name of the backupPolicy, and returns the corresponding backupPolicy object, and an error if there is any.
func (c *FakeBackupPolicies) Get(ctx context.Context, name string, options v1.GetOptions) (result *v1alpha1.BackupPolicy, err error) {
	obj, err := c.Fake.
		Invokes(testing.NewGetAction(backuppoliciesResource, c.ns, name), &v1alpha1.BackupPolicy{})

	if obj == nil {
		return nil, err
	}
	return obj.(*v1alpha1.BackupPolicy), err
}

// List takes label and field selectors, and returns the list of BackupPolicies that match those selectors.
func (c *FakeBackupPolicies) List(ctx context.Context, opts v1.ListOptions) (result *v1alpha1.BackupPolicyList, err error) {
	obj, err := c.Fake.
		Invokes(testing.NewListAction(backuppoliciesResource, backuppoliciesKind, c.ns, opts), &v1alpha1.BackupPolicyList{})

	if obj == nil {
		return nil, err
	}

	label, _, _ := testing.ExtractFromListOptions(opts)
	if label == nil {
		label = labels.Everything()
	}
	list := &v1alpha1.BackupPolicyList{ListMeta: obj.(*v1alpha1.BackupPolicyList).ListMeta}
	for _, item := range obj.(*v1alpha1.BackupPolicyList).Items {
		if label.Matches(labels.Set(item.Labels)) {
			list.Items = append(list.Items, item)
		}
	}
	return list, err
}

// Watch returns a watch.Interface that watches the requested backupPolicies.
func (c *FakeBackupPolicies) Watch(ctx context.Context, opts v1.ListOptions) (watch.Interface, error) {
	return c.Fake.
		InvokesWatch(testing.NewWatchAction(backuppoliciesResource, c.ns, opts))

}

// Create takes the representation of a backupPolicy and creates it.  Returns the server's representation of the backupPolicy, and an error, if there is any.
func (c *FakeBackupPolicies) Create(ctx context.Context, backupPolicy *v1alpha1.BackupPolicy, opts v1.CreateOptions) (result *v1alpha1.BackupPolicy, err error) {
	obj, err := c.Fake.
		Invokes(testing.NewCreateAction(backuppoliciesResource, c.ns, backupPolicy), &v1alpha1.BackupPolicy{})

	if obj == nil {
		return nil, err
	}
	return obj.(*v1alpha1.BackupPolicy), err
}

// Update takes the representation of a backupPolicy and updates it. Returns the server's representation of the backupPolicy, and an error, if there is any.
func (c *FakeBackupPolicies) Update(ctx context.Context, backupPolicy *v1alpha1.BackupPolicy, opts v1.UpdateOptions) (result *v1alpha1.BackupPolicy, err error) {
	obj, err := c.Fake.
		Invokes(testing.NewUpdateAction(backuppoliciesResource, c.ns, backupPolicy), &v1alpha1.BackupPolicy{})

	if obj == nil {
		return nil, err
	}
	return obj.(*v1alpha1.BackupPolicy), err
}

// UpdateStatus was generated because the type contains a Status member.
// Add a +genclient:noStatus comment above the type to avoid generating UpdateStatus().
func (c *FakeBackupPolicies) UpdateStatus(ctx context.Context, backupPolicy *v1alpha1.BackupPolicy, opts v1.UpdateOptions) (*v1alpha1.BackupPolicy, error) {
	obj, err := c.Fake.
		Invokes(testing.NewUpdateSubresourceAction(backuppoliciesResource, "status", c.ns, backupPolicy), &v1alpha1.BackupPolicy{})

	if obj == nil {
		return nil, err
	}
	return obj.(*v1alpha1.BackupPolicy), err
}

// Delete takes name of the backupPolicy and deletes it. Returns an error if one occurs.
func (c *FakeBackupPolicies) Delete(ctx context.Context, name string, opts v1.DeleteOptions) error {
	_, err := c.Fake.
		Invokes(testing.NewDeleteActionWithOptions(backuppoliciesResource, c.ns, name, opts), &v1alpha1.BackupPolicy{})

	return err
}

// DeleteCollection deletes a collection of objects.
func (c *FakeBackupPolicies) DeleteCollection(ctx context.Context, opts v1.DeleteOptions, listOpts v1.ListOptions) error {
	action := testing.NewDeleteCollectionAction(backuppoliciesResource, c.ns, listOpts)

	_, err := c.Fake.Invokes(action, &v1alpha1.BackupPolicyList{})
	return err
}

// Patch applies the patch and returns the patched backupPolicy.
func (c *FakeBackupPolicies) Patch(ctx context.Context, name string, pt types.PatchType, data []byte, opts v1.PatchOptions, subresources ...string) (result *v1alpha1.BackupPolicy, err error) {
	obj, err := c.Fake.
		Invokes(testing.NewPatchSubresourceAction(backuppoliciesResource, c.ns, name, pt, data, subresources...), &v1alpha1.BackupPolicy{})

	if obj == nil {
		return nil, err
	}
	return obj.(*v1alpha1.BackupPolicy), err
}
