/*
Copyright (C) 2022-2025 ApeCloud Co., Ltd

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

package controllerutil

import (
	"context"
	"fmt"
	"sync"

	viper "github.com/apecloud/kubeblocks/pkg/viperx"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	additionalClient client.Client
	clientMutex      sync.RWMutex
)

// InitFederalClient initializes the additional kubeconfig client if enabled
func InitFederalClient(ctx context.Context, cli client.Client) error {
	enabled := viper.GetBool("additional_kubeconfig_enabled")
	if !enabled {
		return nil
	}

	secretName := viper.GetString("additional_kubeconfig_secret_name")
	contextName := viper.GetString("additional_kubeconfig_context")
	secretNameSpace := viper.GetString("additional_kubeconfig_secret_namespace")

	if secretName == "" {
		return fmt.Errorf("additional kubeconfig secret name is required when enabled")
	}

	// Get the secret containing the kubeconfig
	secret := &corev1.Secret{}
	secretKey := types.NamespacedName{
		Name:      secretName,
		Namespace: secretNameSpace,
	}

	if err := cli.Get(ctx, secretKey, secret); err != nil {
		return fmt.Errorf("failed to get kubeconfig secret %s: %v", secretName, err)
	}
	kubeconfig, ok := secret.Data["kubeconfig"]
	if !ok {
		return fmt.Errorf("kubeconfig not found in secret %s", secretName)
	}
	fmt.Printf("kubeconfig: %s\n", string(kubeconfig))

	// Parse the kubeconfig
	config, err := clientcmd.NewClientConfigFromBytes(kubeconfig)
	if err != nil {
		return fmt.Errorf("failed to parse kubeconfig: %v", err)
	}

	// Get REST config for the specified context
	var restConfig *rest.Config
	if contextName != "" {
		restConfig, err = getConfigWithContext(string(kubeconfig), contextName)
	} else {
		restConfig, err = config.ClientConfig()
	}
	if err != nil {
		return fmt.Errorf("failed to get REST config: %v", err)
	}

	// Create the client
	newClient, err := client.New(restConfig, client.Options{})
	if err != nil {
		return fmt.Errorf("failed to create client: %v", err)
	}
	clientMutex.Lock()
	additionalClient = newClient
	clientMutex.Unlock()

	return nil
}

// GetFederalClient returns the additional client if available
func GetFederalClient() client.Client {
	clientMutex.RLock()
	defer clientMutex.RUnlock()
	return additionalClient
}

// getConfigWithContext creates a REST config for a specific context
func getConfigWithContext(kubeconfig, context string) (*rest.Config, error) {
	// Parse kubeconfig content directly from bytes
	clientConfig, err := clientcmd.NewClientConfigFromBytes([]byte(kubeconfig))
	if err != nil {
		return nil, err
	}

	// If context is specified, override the current context
	if context != "" {
		rawConfig, err := clientConfig.RawConfig()
		if err != nil {
			return nil, err
		}
		rawConfig.CurrentContext = context
		return clientcmd.NewDefaultClientConfig(rawConfig, &clientcmd.ConfigOverrides{}).ClientConfig()
	}

	// Use default context
	return clientConfig.ClientConfig()
}
