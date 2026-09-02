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

package test

import (
	v1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	api "github.com/gardener/machine-controller-manager-provider-gdc/pkg/provider/apis"
)

// FakeSPI provides a way to inject fake client and error for testing
type FakeSPI struct {
	KubeClient client.WithWatch
	Err        error
}

func (s *FakeSPI) Client(secret *v1.Secret, spec *api.ProviderSpec) (client.WithWatch, error) {
	if s.Err != nil {
		return nil, s.Err
	}
	return s.KubeClient, nil
}
