// Copyright 2025 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package spi contains the implementation for session provider interface which handles GDC as a cloud provider
package spi

import (
	"encoding/json"
	"fmt"

	vmv1 "github.com/googlecloudplatform/google-distributed-cloud-apis/pkg/apis/public/virtualmachine/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/gardener/machine-controller-manager-provider-gdc/gdc/pkg/auth"
	gdcclient "github.com/gardener/machine-controller-manager-provider-gdc/gdc/pkg/client"
	api "github.com/gardener/machine-controller-manager-provider-gdc/pkg/provider/apis"
)

const (
	// serviceAccountJSONField is the field in a secret where the service account JSON is stored.
	serviceAccountJSONField = "serviceaccount.json"
)

// SessionProviderInterface provides an interface to deal with cloud provider session
type SessionProviderInterface interface {
	Client(secret *v1.Secret, spec *api.ProviderSpec) (client.WithWatch, error)
}

// PluginSPIImpl is the real implementation of SPI interface that makes the calls to the provider SDK.
type PluginSPIImpl struct {
	scheme *runtime.Scheme
}

func New() (*PluginSPIImpl, error) {
	scheme := runtime.NewScheme()
	if err := vmv1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("add vm types to kubernetes client: %w", err)
	}
	if err := v1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("add 'k8s.io/api/core/v1' types to kubernetes client: %w", err)
	}
	return &PluginSPIImpl{
		scheme: scheme,
	}, nil
}

// Client instantiates a k8s client from the given secret
func (p *PluginSPIImpl) Client(secret *v1.Secret, spec *api.ProviderSpec) (client.WithWatch, error) {
	serviceAccount, err := getServiceAccountFromSecret(secret)
	if err != nil {
		return nil, err
	}
	kubeClient, err := gdcclient.Get(&gdcclient.OrgClusterConfig{
		OrgClusterURL: spec.OrgClusterURL,
		CAData:        spec.CAData,
	}, serviceAccount, p.scheme)
	if err != nil {
		return nil, fmt.Errorf("failed to create kubeClient %w", err)
	}
	return kubeClient, nil
}

// getServiceAccountFromSecret retrieves the ServiceAccount from the secret.
func getServiceAccountFromSecret(secret *v1.Secret) (*auth.ServiceAccount, error) {
	data, ok := secret.Data[serviceAccountJSONField]
	if !ok {
		return nil, fmt.Errorf("secret %s/%s doesn't have a service account json (expected field: %q)", secret.Namespace, secret.Name, serviceAccountJSONField)
	}

	serviceAccount := &auth.ServiceAccount{}
	if err := json.Unmarshal(data, serviceAccount); err != nil {
		return nil, fmt.Errorf("failed to parse service account, %w", err)
	}
	return serviceAccount, nil
}
