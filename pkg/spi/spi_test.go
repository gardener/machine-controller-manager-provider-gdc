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

package spi

import (
	"strings"
	"testing"

	v1 "k8s.io/api/core/v1"

	api "github.com/gardener/machine-controller-manager-provider-gdc/pkg/provider/apis"
)

func TestPluginSPIImpl_Client(t *testing.T) {
	type args struct {
		secret *v1.Secret
		spec   *api.ProviderSpec
	}
	tests := []struct {
		name         string
		p            *PluginSPIImpl
		args         args
		wantErr      bool
		errorMessage string
	}{
		{
			name: "failed to create client without service account",
			p:    &PluginSPIImpl{},
			args: args{
				secret: &v1.Secret{},
			},
			wantErr:      true,
			errorMessage: "doesn't have a service account json",
		},
		{
			name: "failed to create client malformed service account",
			p:    &PluginSPIImpl{},
			args: args{
				secret: &v1.Secret{
					Data: map[string][]byte{
						"serviceaccount.json": []byte(`fake-data`),
					},
				},
			},
			wantErr:      true,
			errorMessage: "failed to parse service account",
		},
		{
			name: "successfully create client",
			p:    &PluginSPIImpl{},
			args: args{
				secret: &v1.Secret{
					Data: map[string][]byte{
						"serviceaccount.json": []byte(`{}`),
					},
				},
				spec: &api.ProviderSpec{
					OrgClusterURL: "global.com",
				},
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := New()
			if err != nil {
				t.Fatalf("unexpected error creating SPI %v", err)
			}
			_, err = p.Client(tt.args.secret, tt.args.spec)
			if (err != nil) != tt.wantErr || (err != nil && !strings.Contains(err.Error(), tt.errorMessage)) {
				t.Errorf("PluginSPIImpl.Client() error = %v, wantErr %v with message %q", err, tt.wantErr, tt.errorMessage)
				return
			}
		})
	}
}
