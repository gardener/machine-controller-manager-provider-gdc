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

package provider

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gardener/machine-controller-manager/pkg/apis/machine/v1alpha1"
	"github.com/gardener/machine-controller-manager/pkg/util/provider/driver"
	"github.com/gardener/machine-controller-manager/pkg/util/provider/machinecodes/codes"
	"github.com/gardener/machine-controller-manager/pkg/util/provider/machinecodes/status"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	networkingv1 "github.com/googlecloudplatform/google-distributed-cloud-apis/pkg/apis/public/networking/v1"
	vmv1 "github.com/googlecloudplatform/google-distributed-cloud-apis/pkg/apis/public/virtualmachine/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	k8sfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	api "github.com/gardener/machine-controller-manager-provider-gdc/pkg/provider/apis"
	"github.com/gardener/machine-controller-manager-provider-gdc/pkg/spi"
	mock "github.com/gardener/machine-controller-manager-provider-gdc/pkg/spi/fake"
)

func scheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = vmv1.AddToScheme(s)
	_ = corev1.AddToScheme(s)
	return s
}

func TestProvider_CreateMachine(t *testing.T) {
	type args struct {
		ctx context.Context
		req *driver.CreateMachineRequest
	}
	tests := []struct {
		name         string
		initObjs     []client.Object
		spiError     error
		args         args
		want         *driver.CreateMachineResponse
		wantVM       *vmv1.VirtualMachine
		wantBootDisk *vmv1.VirtualMachineDisk
		wantSecret   *corev1.Secret
	}{
		{
			name: "create VM with private registry, a performance boot disks, and a blank disk",
			args: args{
				ctx: nil,
				req: &driver.CreateMachineRequest{
					Machine: &v1alpha1.Machine{
						ObjectMeta: metav1.ObjectMeta{
							Name: "fake-machine-1",
						},
					},
					MachineClass: &v1alpha1.MachineClass{
						ProviderSpec: runtime.RawExtension{
							Raw: func() []byte {
								spec := api.ProviderSpec{
									Project:                 "fake-shoot-project",
									MachineType:             "fake-vm-type",
									VMRunningTimeoutSeconds: ptr.To(0),
									AccessEnabled:           false,
									Disks: []*api.Disk{{
										Image:      "fake-image",
										Project:    "vm-system",
										Boot:       true,
										AutoDelete: true,
										SizeGB:     100,
										Type:       "Performance",
									}, {
										Boot:       false,
										AutoDelete: false,
										SizeGB:     100,
									}},
									RegistryURL:  "10.200.9.2:10443",
									CAData:       "fake-cert",
									EnableEgress: nil,
								}
								raw, _ := json.Marshal(spec)
								return raw
							}(),
						},
					},
					Secret: &corev1.Secret{
						ObjectMeta: metav1.ObjectMeta{
							Name: "mcm-secret",
						},
						Data: map[string][]byte{"userData": []byte(`fake-bootstrap-content`)},
					},
				},
			},
			initObjs: []client.Object{},
			want: &driver.CreateMachineResponse{
				ProviderID:     "fake-machine-1",
				NodeName:       "fake-machine-1",
				LastKnownState: "Created fake-machine-1",
			},
			wantVM: &vmv1.VirtualMachine{
				ObjectMeta: metav1.ObjectMeta{
					Name:            "fake-machine-1",
					Namespace:       "fake-shoot-project",
					ResourceVersion: "1",
					Generation:      0,
					Labels: map[string]string{
						networkingv1.EnableEgressNATLabelKey: "true",
					},
				},
				Spec: vmv1.VirtualMachineSpec{
					Disks: []vmv1.DiskAttachment{{
						Boot:       ptr.To(true),
						AutoDelete: ptr.To(true),
						VirtualMachineDiskRef: corev1.LocalObjectReference{
							Name: "fake-machine-1-boot-disk",
						},
					}, {
						Boot:       ptr.To(false),
						AutoDelete: ptr.To(false),
						VirtualMachineDiskRef: corev1.LocalObjectReference{
							Name: "fake-machine-1-disk",
						},
					}},
					Compute: vmv1.Compute{
						VirtualMachineType: "fake-vm-type",
					},
					StartupScripts: []vmv1.StartupScript{
						{Name: "caInit", ScriptSecretRef: &corev1.LocalObjectReference{Name: "fake-machine-1-cainit-script"}},
						{Name: "userData", ScriptSecretRef: &corev1.LocalObjectReference{Name: "fake-machine-1-init-script"}},
					},
					GuestEnvironment: &vmv1.GuestEnvironment{
						AccessManagement: &vmv1.AccessManagementConfig{},
					},
					Network: &vmv1.NetworkSpec{
						Interfaces: []vmv1.NetworkInterfaceSpec{
							{
								Network: "default",
								Subnet:  "",
							},
						},
					},
				},
				Status: vmv1.VirtualMachineStatus{},
			},
			wantBootDisk: &vmv1.VirtualMachineDisk{
				ObjectMeta: metav1.ObjectMeta{
					Name:            "fake-machine-1-boot-disk",
					Namespace:       "fake-shoot-project",
					ResourceVersion: "2",
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion: "virtualmachine.gdc.goog/v1",
							Kind:       "VirtualMachine",
							Name:       "fake-machine-1",
						},
					},
				},
				Spec: vmv1.VirtualMachineDiskSpec{
					Source: &vmv1.DiskSource{
						Image: &vmv1.ImageDiskSource{
							Name:      "fake-image",
							Namespace: "vm-system",
						},
					},
					Type: "Performance",
					Size: resource.MustParse("100Gi"),
				},
			},
			wantSecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:            "fake-machine-1-init-script",
					Namespace:       "fake-shoot-project",
					ResourceVersion: "2",
					OwnerReferences: []metav1.OwnerReference{
						metav1.OwnerReference{
							APIVersion: vmv1.SchemeGroupVersion.String(),
							Kind:       "VirtualMachine",
							Name:       "fake-machine-1",
						},
					},
				},
				Data: map[string][]byte{
					"script": []byte(`fake-bootstrap-content`),
				},
			},
		},
		{
			name: "create VM with VM type",
			args: args{
				ctx: nil,
				req: &driver.CreateMachineRequest{
					Machine: &v1alpha1.Machine{
						ObjectMeta: metav1.ObjectMeta{
							Name: "fake-machine-1",
						},
					},
					MachineClass: &v1alpha1.MachineClass{
						ProviderSpec: runtime.RawExtension{
							Raw: func() []byte {
								spec := api.ProviderSpec{
									Project:                 "fake-shoot-project",
									MachineType:             "fake-vm-type",
									VMRunningTimeoutSeconds: ptr.To(0),
									AccessEnabled:           false,
									Disks: []*api.Disk{
										{
											Image:      "fake-image",
											Project:    "vm-system",
											Boot:       true,
											AutoDelete: true,
											SizeGB:     100,
										},
									},
								}
								raw, _ := json.Marshal(spec)
								return raw
							}(),
						},
					},
					Secret: &corev1.Secret{
						ObjectMeta: metav1.ObjectMeta{
							Name: "mcm-secret",
						},
						Data: map[string][]byte{"userData": []byte(`fake-bootstrap-content`)},
					},
				},
			},
			initObjs: []client.Object{},
			want: &driver.CreateMachineResponse{
				ProviderID:     "fake-machine-1",
				NodeName:       "fake-machine-1",
				LastKnownState: "Created fake-machine-1",
			},
			wantVM: &vmv1.VirtualMachine{
				ObjectMeta: metav1.ObjectMeta{
					Name:            "fake-machine-1",
					Namespace:       "fake-shoot-project",
					ResourceVersion: "1",
					Labels: map[string]string{
						networkingv1.EnableEgressNATLabelKey: "true",
					},
				},
				Spec: vmv1.VirtualMachineSpec{
					Disks: []vmv1.DiskAttachment{{
						Boot:       ptr.To(true),
						AutoDelete: ptr.To(true),
						VirtualMachineDiskRef: corev1.LocalObjectReference{
							Name: "fake-machine-1-boot-disk",
						},
					}},
					Compute:        vmv1.Compute{VirtualMachineType: "fake-vm-type"},
					StartupScripts: []vmv1.StartupScript{{Name: "userData", ScriptSecretRef: &corev1.LocalObjectReference{Name: "fake-machine-1-init-script"}}},
					GuestEnvironment: &vmv1.GuestEnvironment{
						AccessManagement: &vmv1.AccessManagementConfig{},
					},
					Network: &vmv1.NetworkSpec{
						Interfaces: []vmv1.NetworkInterfaceSpec{
							{
								Network: "default",
								Subnet:  "",
							},
						},
					},
				},
				Status: vmv1.VirtualMachineStatus{},
			},
			wantSecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:            "fake-machine-1-init-script",
					Namespace:       "fake-shoot-project",
					ResourceVersion: "2",
					OwnerReferences: []metav1.OwnerReference{
						metav1.OwnerReference{
							APIVersion: vmv1.SchemeGroupVersion.String(),
							Kind:       "VirtualMachine",
							Name:       "fake-machine-1",
						},
					},
				},
				Data: map[string][]byte{
					"script": []byte(`fake-bootstrap-content`),
				},
			},
		},
		{
			name: "create VM with existing init script",
			initObjs: []client.Object{&corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:            "fake-machine-1-init-script",
					Namespace:       "fake-shoot-project",
					ResourceVersion: "1",
				},
				Data: map[string][]byte{
					"script": []byte(`fake-bootstrap-content`),
				},
			}},
			args: args{
				ctx: nil,
				req: &driver.CreateMachineRequest{
					Machine: &v1alpha1.Machine{
						ObjectMeta: metav1.ObjectMeta{
							Name: "fake-machine-1",
						},
					},
					MachineClass: &v1alpha1.MachineClass{
						ProviderSpec: runtime.RawExtension{
							Raw: func() []byte {
								spec := api.ProviderSpec{
									Project:                 "fake-shoot-project",
									MachineType:             "fake-vm-type",
									VMRunningTimeoutSeconds: ptr.To(0),
									AccessEnabled:           false,
									Disks: []*api.Disk{
										{
											Image:      "fake-image",
											Project:    "vm-system",
											Boot:       true,
											AutoDelete: true,
											SizeGB:     100,
										},
									},
								}
								raw, _ := json.Marshal(spec)
								return raw
							}(),
						},
					},
					Secret: &corev1.Secret{
						ObjectMeta: metav1.ObjectMeta{
							Name: "mcm-secret",
						},
						Data: map[string][]byte{"userData": []byte(`fake-bootstrap-content`)},
					},
				},
			},
			want: &driver.CreateMachineResponse{
				ProviderID:     "fake-machine-1",
				NodeName:       "fake-machine-1",
				LastKnownState: "Created fake-machine-1",
			},
			wantVM: &vmv1.VirtualMachine{
				ObjectMeta: metav1.ObjectMeta{
					Name:            "fake-machine-1",
					Namespace:       "fake-shoot-project",
					ResourceVersion: "1",
					Labels: map[string]string{
						networkingv1.EnableEgressNATLabelKey: "true",
					},
				},
				Spec: vmv1.VirtualMachineSpec{
					Disks: []vmv1.DiskAttachment{{
						Boot:       ptr.To(true),
						AutoDelete: ptr.To(true),
						VirtualMachineDiskRef: corev1.LocalObjectReference{
							Name: "fake-machine-1-boot-disk",
						},
					}},
					Compute:        vmv1.Compute{VirtualMachineType: "fake-vm-type"},
					StartupScripts: []vmv1.StartupScript{{Name: "userData", ScriptSecretRef: &corev1.LocalObjectReference{Name: "fake-machine-1-init-script"}}},
					GuestEnvironment: &vmv1.GuestEnvironment{
						AccessManagement: &vmv1.AccessManagementConfig{},
					},
					Network: &vmv1.NetworkSpec{
						Interfaces: []vmv1.NetworkInterfaceSpec{
							{
								Network: "default",
								Subnet:  "",
							},
						},
					},
				},
				Status: vmv1.VirtualMachineStatus{},
			},
			wantSecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:            "fake-machine-1-init-script",
					Namespace:       "fake-shoot-project",
					ResourceVersion: "2",
					OwnerReferences: []metav1.OwnerReference{
						metav1.OwnerReference{
							APIVersion: vmv1.SchemeGroupVersion.String(),
							Kind:       "VirtualMachine",
							Name:       "fake-machine-1",
						},
					},
				},
				Data: map[string][]byte{
					"script": []byte(`fake-bootstrap-content`),
				},
			},
		},
		{
			name: "create VM with existing disk",
			initObjs: []client.Object{&vmv1.VirtualMachineDisk{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "fake-machine-1-boot-disk",
					Namespace: "fake-shoot-project",
				},
			}},
			args: args{
				ctx: nil,
				req: &driver.CreateMachineRequest{
					Machine: &v1alpha1.Machine{
						ObjectMeta: metav1.ObjectMeta{
							Name: "fake-machine-1",
						},
					},
					MachineClass: &v1alpha1.MachineClass{
						ProviderSpec: runtime.RawExtension{
							Raw: func() []byte {
								spec := api.ProviderSpec{
									Project:                 "fake-shoot-project",
									MachineType:             "fake-vm-type",
									VMRunningTimeoutSeconds: ptr.To(0),
									AccessEnabled:           false,
									Disks: []*api.Disk{
										{
											Image:      "fake-image",
											Project:    "vm-system",
											Boot:       true,
											AutoDelete: true,
											SizeGB:     100,
										},
									},
								}
								raw, _ := json.Marshal(spec)
								return raw
							}(),
						},
					},
					Secret: &corev1.Secret{
						ObjectMeta: metav1.ObjectMeta{
							Name: "mcm-secret",
						},
						Data: map[string][]byte{"userData": []byte(`fake-bootstrap-content`)},
					},
				},
			},
			want: &driver.CreateMachineResponse{
				ProviderID:     "fake-machine-1",
				NodeName:       "fake-machine-1",
				LastKnownState: "Created fake-machine-1",
			},
			wantVM: &vmv1.VirtualMachine{
				ObjectMeta: metav1.ObjectMeta{
					Name:            "fake-machine-1",
					Namespace:       "fake-shoot-project",
					ResourceVersion: "1",
					Labels: map[string]string{
						networkingv1.EnableEgressNATLabelKey: "true",
					},
				},
				Spec: vmv1.VirtualMachineSpec{
					Disks: []vmv1.DiskAttachment{{
						Boot:       ptr.To(true),
						AutoDelete: ptr.To(true),
						VirtualMachineDiskRef: corev1.LocalObjectReference{
							Name: "fake-machine-1-boot-disk",
						},
					}},
					Compute:        vmv1.Compute{VirtualMachineType: "fake-vm-type"},
					StartupScripts: []vmv1.StartupScript{{Name: "userData", ScriptSecretRef: &corev1.LocalObjectReference{Name: "fake-machine-1-init-script"}}},
					GuestEnvironment: &vmv1.GuestEnvironment{
						AccessManagement: &vmv1.AccessManagementConfig{},
					},
					Network: &vmv1.NetworkSpec{
						Interfaces: []vmv1.NetworkInterfaceSpec{
							{
								Network: "default",
								Subnet:  "",
							},
						},
					},
				},
				Status: vmv1.VirtualMachineStatus{},
			},
			wantSecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:            "fake-machine-1-init-script",
					Namespace:       "fake-shoot-project",
					ResourceVersion: "2",
					OwnerReferences: []metav1.OwnerReference{
						metav1.OwnerReference{
							APIVersion: vmv1.SchemeGroupVersion.String(),
							Kind:       "VirtualMachine",
							Name:       "fake-machine-1",
						},
					},
				},
				Data: map[string][]byte{
					"script": []byte(`fake-bootstrap-content`),
				},
			},
		},
		{
			name: "create VM with EnableEgress=true",
			args: args{
				ctx: nil,
				req: &driver.CreateMachineRequest{
					Machine: &v1alpha1.Machine{
						ObjectMeta: metav1.ObjectMeta{
							Name: "fake-machine-1",
						},
					},
					MachineClass: &v1alpha1.MachineClass{
						ProviderSpec: runtime.RawExtension{
							Raw: func() []byte {
								spec := api.ProviderSpec{
									Project:                 "fake-shoot-project",
									MachineType:             "fake-vm-type",
									VMRunningTimeoutSeconds: ptr.To(0),
									AccessEnabled:           false,
									Disks: []*api.Disk{
										{
											Image:      "fake-image",
											Project:    "vm-system",
											Boot:       true,
											AutoDelete: true,
											SizeGB:     100,
										},
									},
									EnableEgress: ptr.To(true),
								}
								raw, _ := json.Marshal(spec)
								return raw
							}(),
						},
					},
					Secret: &corev1.Secret{
						ObjectMeta: metav1.ObjectMeta{
							Name: "mcm-secret",
						},
						Data: map[string][]byte{"userData": []byte(`fake-bootstrap-content`)},
					},
				},
			},
			initObjs: []client.Object{},
			want: &driver.CreateMachineResponse{
				ProviderID:     "fake-machine-1",
				NodeName:       "fake-machine-1",
				LastKnownState: "Created fake-machine-1",
			},
			wantVM: &vmv1.VirtualMachine{
				ObjectMeta: metav1.ObjectMeta{
					Name:            "fake-machine-1",
					Namespace:       "fake-shoot-project",
					ResourceVersion: "1",
					Labels:          nil,
				},
				Spec: vmv1.VirtualMachineSpec{
					Disks: []vmv1.DiskAttachment{{
						Boot:       ptr.To(true),
						AutoDelete: ptr.To(true),
						VirtualMachineDiskRef: corev1.LocalObjectReference{
							Name: "fake-machine-1-boot-disk",
						},
					}},
					Compute:        vmv1.Compute{VirtualMachineType: "fake-vm-type"},
					StartupScripts: []vmv1.StartupScript{{Name: "userData", ScriptSecretRef: &corev1.LocalObjectReference{Name: "fake-machine-1-init-script"}}},
					GuestEnvironment: &vmv1.GuestEnvironment{
						AccessManagement: &vmv1.AccessManagementConfig{},
					},
					Network: &vmv1.NetworkSpec{
						Interfaces: []vmv1.NetworkInterfaceSpec{
							{
								Network: "default",
								Subnet:  "",
							},
						},
					},
				},
				Status: vmv1.VirtualMachineStatus{},
			},
			wantSecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:            "fake-machine-1-init-script",
					Namespace:       "fake-shoot-project",
					ResourceVersion: "2",
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion: vmv1.SchemeGroupVersion.String(),
							Kind:       "VirtualMachine",
							Name:       "fake-machine-1",
						},
					},
				},
				Data: map[string][]byte{
					"script": []byte(`fake-bootstrap-content`),
				},
			},
		},
		{
			name: "create VM with EnableEgress=false",
			args: args{
				ctx: nil,
				req: &driver.CreateMachineRequest{
					Machine: &v1alpha1.Machine{
						ObjectMeta: metav1.ObjectMeta{
							Name: "fake-machine-1",
						},
					},
					MachineClass: &v1alpha1.MachineClass{
						ProviderSpec: runtime.RawExtension{
							Raw: func() []byte {
								spec := api.ProviderSpec{
									Project:                 "fake-shoot-project",
									MachineType:             "fake-vm-type",
									VMRunningTimeoutSeconds: ptr.To(0),
									AccessEnabled:           false,
									Disks: []*api.Disk{
										{
											Image:      "fake-image",
											Project:    "vm-system",
											Boot:       true,
											AutoDelete: true,
											SizeGB:     100,
										},
									},
									EnableEgress: ptr.To(false),
								}
								raw, _ := json.Marshal(spec)
								return raw
							}(),
						},
					},
					Secret: &corev1.Secret{
						ObjectMeta: metav1.ObjectMeta{
							Name: "mcm-secret",
						},
						Data: map[string][]byte{"userData": []byte(`fake-bootstrap-content`)},
					},
				},
			},
			initObjs: []client.Object{},
			want: &driver.CreateMachineResponse{
				ProviderID:     "fake-machine-1",
				NodeName:       "fake-machine-1",
				LastKnownState: "Created fake-machine-1",
			},
			wantVM: &vmv1.VirtualMachine{
				ObjectMeta: metav1.ObjectMeta{
					Name:            "fake-machine-1",
					Namespace:       "fake-shoot-project",
					ResourceVersion: "1",
					Labels:          nil,
				},
				Spec: vmv1.VirtualMachineSpec{
					Disks: []vmv1.DiskAttachment{{
						Boot:       ptr.To(true),
						AutoDelete: ptr.To(true),
						VirtualMachineDiskRef: corev1.LocalObjectReference{
							Name: "fake-machine-1-boot-disk",
						},
					}},
					Compute:        vmv1.Compute{VirtualMachineType: "fake-vm-type"},
					StartupScripts: []vmv1.StartupScript{{Name: "userData", ScriptSecretRef: &corev1.LocalObjectReference{Name: "fake-machine-1-init-script"}}},
					GuestEnvironment: &vmv1.GuestEnvironment{
						AccessManagement: &vmv1.AccessManagementConfig{},
					},
					Network: &vmv1.NetworkSpec{
						Interfaces: []vmv1.NetworkInterfaceSpec{
							{
								Network: "default",
								Subnet:  "",
							},
						},
					},
				},
				Status: vmv1.VirtualMachineStatus{},
			},
			wantSecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:            "fake-machine-1-init-script",
					Namespace:       "fake-shoot-project",
					ResourceVersion: "2",
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion: vmv1.SchemeGroupVersion.String(),
							Kind:       "VirtualMachine",
							Name:       "fake-machine-1",
						},
					},
				},
				Data: map[string][]byte{
					"script": []byte(`fake-bootstrap-content`),
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := scheme()
			tracker := k8stesting.NewObjectTracker(s, serializer.NewCodecFactory(s).UniversalDecoder())
			spi := &mock.FakeSPI{
				KubeClient: k8sfake.NewClientBuilder().
					WithScheme(s).
					WithObjectTracker(tracker).
					WithObjects(tt.initObjs...).
					Build(),

				Err: tt.spiError,
			}

			p := &Provider{SPI: spi}

			got, err := p.CreateMachine(tt.args.ctx, tt.args.req)
			if err != nil {
				t.Fatalf("Provider.CreateMachine() has unexpected error = %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Provider.CreateMachine() = %v, want %v", got, tt.want)
			}
			if tt.wantVM != nil {
				gotVM := &vmv1.VirtualMachine{}
				if err = spi.KubeClient.Get(context.TODO(), types.NamespacedName{
					Namespace: tt.wantVM.Namespace,
					Name:      tt.wantVM.Name,
				}, gotVM); err != nil {
					t.Errorf("failed to get VM error = %v", err)
				}
				if !reflect.DeepEqual(gotVM, tt.wantVM) {
					t.Errorf("VM = %v, want %v", gotVM, tt.wantVM)
				}
			}
			if tt.wantSecret != nil {
				gotSecret := &corev1.Secret{}
				if err = spi.KubeClient.Get(context.TODO(), types.NamespacedName{
					Namespace: tt.wantSecret.Namespace,
					Name:      tt.wantSecret.Name,
				}, gotSecret); err != nil {
					t.Errorf("failed to get Secret error = %v", err)
				}
				if !reflect.DeepEqual(gotSecret, tt.wantSecret) {
					t.Errorf("Secret = %v, want %v", gotSecret, tt.wantSecret)
				}
			}
			if tt.wantBootDisk != nil {
				gotDisk := &vmv1.VirtualMachineDisk{}
				if err = spi.KubeClient.Get(context.TODO(), types.NamespacedName{
					Namespace: tt.wantBootDisk.Namespace,
					Name:      tt.wantBootDisk.Name,
				}, gotDisk); err != nil {
					t.Errorf("failed to get gdisk error = %v", err)
				}
				if diff := cmp.Diff(gotDisk, tt.wantBootDisk, cmpopts.IgnoreUnexported(vmv1.VirtualMachineDiskSpec{})); diff != "" {
					t.Errorf("gdisk(%q) returned unexpected diff (-want +got):\n%s", tt.wantBootDisk.Name, diff)
				}
			}
		})
	}
}

func TestProvider_CreateMachine_WithRegistry(t *testing.T) {
	type args struct {
		ctx context.Context
		req *driver.CreateMachineRequest
	}
	tests := []struct {
		name       string
		initObjs   []client.Object
		spiError   error
		args       args
		want       *driver.CreateMachineResponse
		wantVM     *vmv1.VirtualMachine
		wantSecret *corev1.Secret
	}{
		{
			name: "create VM with private registry, a boot disks, and a blank disk",
			args: args{
				ctx: nil,
				req: &driver.CreateMachineRequest{
					Machine: &v1alpha1.Machine{
						ObjectMeta: metav1.ObjectMeta{
							Name: "fake-machine-1",
						},
					},
					MachineClass: &v1alpha1.MachineClass{
						ProviderSpec: runtime.RawExtension{
							Raw: func() []byte {
								spec := api.ProviderSpec{
									Project:                 "fake-shoot-project",
									MachineType:             "fake-vm-type",
									VMRunningTimeoutSeconds: ptr.To(0),
									AccessEnabled:           false,
									Disks: []*api.Disk{{
										Image:      "fake-image",
										Project:    "vm-system",
										Boot:       true,
										AutoDelete: true,
										SizeGB:     100,
									}, {
										Boot:       false,
										AutoDelete: false,
										SizeGB:     100,
									}},
									OrgClusterURL: "org-cluster-url",
									RegistryURL:   "10.200.9.2:10443",
									CAData:        "fake-cert",
									SubnetName:    "test-subnet",
								}
								raw, _ := json.Marshal(spec)
								return raw
							}(),
						},
					},
					Secret: &corev1.Secret{
						ObjectMeta: metav1.ObjectMeta{
							Name: "mcm-secret",
						},
						Data: map[string][]byte{"userData": []byte(`fake-bootstrap-content`)},
					},
				},
			},
			want: &driver.CreateMachineResponse{
				ProviderID:     "fake-machine-1",
				NodeName:       "fake-machine-1",
				LastKnownState: "Created fake-machine-1",
			},
			wantVM: &vmv1.VirtualMachine{
				ObjectMeta: metav1.ObjectMeta{
					Name:            "fake-machine-1",
					Namespace:       "fake-shoot-project",
					ResourceVersion: "1",
					Generation:      0,
					Labels: map[string]string{
						networkingv1.EnableEgressNATLabelKey: "true",
					},
				},
				Spec: vmv1.VirtualMachineSpec{
					Disks: []vmv1.DiskAttachment{{
						Boot:       ptr.To(true),
						AutoDelete: ptr.To(true),
						VirtualMachineDiskRef: corev1.LocalObjectReference{
							Name: "fake-machine-1-boot-disk",
						},
					}, {
						Boot:       ptr.To(false),
						AutoDelete: ptr.To(false),
						VirtualMachineDiskRef: corev1.LocalObjectReference{
							Name: "fake-machine-1-disk",
						},
					}},
					Compute: vmv1.Compute{
						VirtualMachineType: "fake-vm-type",
					},
					StartupScripts: []vmv1.StartupScript{
						{Name: "caInit", ScriptSecretRef: &corev1.LocalObjectReference{Name: "fake-machine-1-cainit-script"}},
						{Name: "userData", ScriptSecretRef: &corev1.LocalObjectReference{Name: "fake-machine-1-init-script"}},
					},
					GuestEnvironment: &vmv1.GuestEnvironment{
						AccessManagement: &vmv1.AccessManagementConfig{},
					},
					Network: &vmv1.NetworkSpec{
						Interfaces: []vmv1.NetworkInterfaceSpec{
							{
								Network: "default",
								Subnet:  "test-subnet",
							},
						},
					},
				},
				Status: vmv1.VirtualMachineStatus{},
			},
			wantSecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:            "fake-machine-1-init-script",
					Namespace:       "fake-shoot-project",
					ResourceVersion: "2",
					OwnerReferences: []metav1.OwnerReference{
						metav1.OwnerReference{
							APIVersion: vmv1.SchemeGroupVersion.String(),
							Kind:       "VirtualMachine",
							Name:       "fake-machine-1",
						},
					},
				},
				Data: map[string][]byte{
					"script": []byte(`fake-bootstrap-content`),
				},
			},
		},
		{
			name: "create VM with VM type",
			args: args{
				ctx: nil,
				req: &driver.CreateMachineRequest{
					Machine: &v1alpha1.Machine{
						ObjectMeta: metav1.ObjectMeta{
							Name: "fake-machine-1",
						},
					},
					MachineClass: &v1alpha1.MachineClass{
						ProviderSpec: runtime.RawExtension{
							Raw: func() []byte {
								spec := api.ProviderSpec{
									Project:                 "fake-shoot-project",
									MachineType:             "fake-vm-type",
									VMRunningTimeoutSeconds: ptr.To(0),
									AccessEnabled:           false,
									Disks: []*api.Disk{{
										Image:      "fake-image",
										Project:    "vm-system",
										Boot:       true,
										AutoDelete: true,
										SizeGB:     100,
									}},
									SubnetName: "test-subnet",
								}
								raw, _ := json.Marshal(spec)
								return raw
							}(),
						},
					},
					Secret: &corev1.Secret{
						ObjectMeta: metav1.ObjectMeta{
							Name: "mcm-secret",
						},
						Data: map[string][]byte{"userData": []byte(`fake-bootstrap-content`)},
					},
				},
			},
			want: &driver.CreateMachineResponse{
				ProviderID:     "fake-machine-1",
				NodeName:       "fake-machine-1",
				LastKnownState: "Created fake-machine-1",
			},
			wantVM: &vmv1.VirtualMachine{
				ObjectMeta: metav1.ObjectMeta{
					Name:            "fake-machine-1",
					Namespace:       "fake-shoot-project",
					ResourceVersion: "1",
					Labels: map[string]string{
						networkingv1.EnableEgressNATLabelKey: "true",
					},
				},
				Spec: vmv1.VirtualMachineSpec{
					Disks: []vmv1.DiskAttachment{{
						Boot:       ptr.To(true),
						AutoDelete: ptr.To(true),
						VirtualMachineDiskRef: corev1.LocalObjectReference{
							Name: "fake-machine-1-boot-disk",
						},
					}},
					Compute:        vmv1.Compute{VirtualMachineType: "fake-vm-type"},
					StartupScripts: []vmv1.StartupScript{{Name: "userData", ScriptSecretRef: &corev1.LocalObjectReference{Name: "fake-machine-1-init-script"}}},
					GuestEnvironment: &vmv1.GuestEnvironment{
						AccessManagement: &vmv1.AccessManagementConfig{},
					},
					Network: &vmv1.NetworkSpec{
						Interfaces: []vmv1.NetworkInterfaceSpec{
							{
								Network: "default",
								Subnet:  "test-subnet",
							},
						},
					},
				},
				Status: vmv1.VirtualMachineStatus{},
			},
			wantSecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:            "fake-machine-1-init-script",
					Namespace:       "fake-shoot-project",
					ResourceVersion: "2",
					OwnerReferences: []metav1.OwnerReference{
						metav1.OwnerReference{
							APIVersion: vmv1.SchemeGroupVersion.String(),
							Kind:       "VirtualMachine",
							Name:       "fake-machine-1",
						},
					},
				},
				Data: map[string][]byte{
					"script": []byte(`fake-bootstrap-content`),
				},
			},
		},
		{
			name: "create VM with existing init script",
			initObjs: []client.Object{&corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:            "fake-machine-1-init-script",
					Namespace:       "fake-shoot-project",
					ResourceVersion: "1",
				},
				Data: map[string][]byte{
					"script": []byte(`fake-bootstrap-content`),
				},
			}},
			args: args{
				ctx: nil,
				req: &driver.CreateMachineRequest{
					Machine: &v1alpha1.Machine{
						ObjectMeta: metav1.ObjectMeta{
							Name: "fake-machine-1",
						},
					},
					MachineClass: &v1alpha1.MachineClass{
						ProviderSpec: runtime.RawExtension{
							Raw: func() []byte {
								spec := api.ProviderSpec{
									Project:                 "fake-shoot-project",
									MachineType:             "fake-vm-type",
									VMRunningTimeoutSeconds: ptr.To(0),
									AccessEnabled:           false,
									Disks: []*api.Disk{{
										Image:      "fake-image",
										Project:    "vm-system",
										Boot:       true,
										AutoDelete: true,
										SizeGB:     100,
									}},
									SubnetName: "test-subnet",
								}
								raw, _ := json.Marshal(spec)
								return raw
							}(),
						},
					},
					Secret: &corev1.Secret{
						ObjectMeta: metav1.ObjectMeta{
							Name: "mcm-secret",
						},
						Data: map[string][]byte{"userData": []byte(`fake-bootstrap-content`)},
					},
				},
			},
			want: &driver.CreateMachineResponse{
				ProviderID:     "fake-machine-1",
				NodeName:       "fake-machine-1",
				LastKnownState: "Created fake-machine-1",
			},
			wantVM: &vmv1.VirtualMachine{
				ObjectMeta: metav1.ObjectMeta{
					Name:            "fake-machine-1",
					Namespace:       "fake-shoot-project",
					ResourceVersion: "1",
					Labels: map[string]string{
						networkingv1.EnableEgressNATLabelKey: "true",
					},
				},
				Spec: vmv1.VirtualMachineSpec{
					Disks: []vmv1.DiskAttachment{{
						Boot:       ptr.To(true),
						AutoDelete: ptr.To(true),
						VirtualMachineDiskRef: corev1.LocalObjectReference{
							Name: "fake-machine-1-boot-disk",
						},
					}},
					Compute:        vmv1.Compute{VirtualMachineType: "fake-vm-type"},
					StartupScripts: []vmv1.StartupScript{{Name: "userData", ScriptSecretRef: &corev1.LocalObjectReference{Name: "fake-machine-1-init-script"}}},
					GuestEnvironment: &vmv1.GuestEnvironment{
						AccessManagement: &vmv1.AccessManagementConfig{},
					},
					Network: &vmv1.NetworkSpec{
						Interfaces: []vmv1.NetworkInterfaceSpec{
							{
								Network: "default",
								Subnet:  "test-subnet",
							},
						},
					},
				},
				Status: vmv1.VirtualMachineStatus{},
			},
			wantSecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:            "fake-machine-1-init-script",
					Namespace:       "fake-shoot-project",
					ResourceVersion: "2",
					OwnerReferences: []metav1.OwnerReference{
						metav1.OwnerReference{
							APIVersion: vmv1.SchemeGroupVersion.String(),
							Kind:       "VirtualMachine",
							Name:       "fake-machine-1",
						},
					},
				},
				Data: map[string][]byte{
					"script": []byte(`fake-bootstrap-content`),
				},
			},
		},
		{
			name: "create VM with existing disk",
			initObjs: []client.Object{&vmv1.VirtualMachineDisk{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "fake-machine-1-boot-disk",
					Namespace: "fake-shoot-project",
				},
			}},
			args: args{
				ctx: nil,
				req: &driver.CreateMachineRequest{
					Machine: &v1alpha1.Machine{
						ObjectMeta: metav1.ObjectMeta{
							Name: "fake-machine-1",
						},
					},
					MachineClass: &v1alpha1.MachineClass{
						ProviderSpec: runtime.RawExtension{
							Raw: func() []byte {
								spec := api.ProviderSpec{
									Project:                 "fake-shoot-project",
									MachineType:             "fake-vm-type",
									VMRunningTimeoutSeconds: ptr.To(0),
									AccessEnabled:           false,
									Disks: []*api.Disk{{
										Image:      "fake-image",
										Project:    "vm-system",
										Boot:       true,
										AutoDelete: true,
										SizeGB:     100,
									}},
									SubnetName: "test-subnet",
								}
								raw, _ := json.Marshal(spec)
								return raw
							}(),
						},
					},
					Secret: &corev1.Secret{
						ObjectMeta: metav1.ObjectMeta{
							Name: "mcm-secret",
						},
						Data: map[string][]byte{"userData": []byte(`fake-bootstrap-content`)},
					},
				},
			},
			want: &driver.CreateMachineResponse{
				ProviderID:     "fake-machine-1",
				NodeName:       "fake-machine-1",
				LastKnownState: "Created fake-machine-1",
			},
			wantVM: &vmv1.VirtualMachine{
				ObjectMeta: metav1.ObjectMeta{
					Name:            "fake-machine-1",
					Namespace:       "fake-shoot-project",
					ResourceVersion: "1",
					Labels: map[string]string{
						networkingv1.EnableEgressNATLabelKey: "true",
					},
				},
				Spec: vmv1.VirtualMachineSpec{
					Disks: []vmv1.DiskAttachment{{
						Boot:       ptr.To(true),
						AutoDelete: ptr.To(true),
						VirtualMachineDiskRef: corev1.LocalObjectReference{
							Name: "fake-machine-1-boot-disk",
						},
					}},
					Compute:        vmv1.Compute{VirtualMachineType: "fake-vm-type"},
					StartupScripts: []vmv1.StartupScript{{Name: "userData", ScriptSecretRef: &corev1.LocalObjectReference{Name: "fake-machine-1-init-script"}}},
					GuestEnvironment: &vmv1.GuestEnvironment{
						AccessManagement: &vmv1.AccessManagementConfig{},
					},
					Network: &vmv1.NetworkSpec{
						Interfaces: []vmv1.NetworkInterfaceSpec{
							{
								Network: "default",
								Subnet:  "test-subnet",
							},
						},
					},
				},
				Status: vmv1.VirtualMachineStatus{},
			},
			wantSecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:            "fake-machine-1-init-script",
					Namespace:       "fake-shoot-project",
					ResourceVersion: "2",
					OwnerReferences: []metav1.OwnerReference{
						metav1.OwnerReference{
							APIVersion: vmv1.SchemeGroupVersion.String(),
							Kind:       "VirtualMachine",
							Name:       "fake-machine-1",
						},
					},
				},
				Data: map[string][]byte{
					"script": []byte(`fake-bootstrap-content`),
				},
			},
		},
		{
			name: "create VM when it already exists",
			args: args{
				ctx: context.Background(),
				req: &driver.CreateMachineRequest{
					Machine: &v1alpha1.Machine{
						ObjectMeta: metav1.ObjectMeta{
							Name: "fake-machine-already-exists",
						},
					},
					MachineClass: &v1alpha1.MachineClass{
						ProviderSpec: runtime.RawExtension{
							Raw: func() []byte {
								spec := api.ProviderSpec{
									Project:                 "fake-shoot-project",
									MachineType:             "fake-vm-type",
									VMRunningTimeoutSeconds: ptr.To(1),
									AccessEnabled:           false,
									Disks: []*api.Disk{{
										Image:      "fake-image",
										Project:    "vm-system",
										Boot:       true,
										AutoDelete: true,
										SizeGB:     100,
									}},
									SubnetName: "test-subnet",
								}
								raw, _ := json.Marshal(spec)
								return raw
							}(),
						},
					},
					Secret: &corev1.Secret{
						ObjectMeta: metav1.ObjectMeta{
							Name: "mcm-secret",
						},
						Data: map[string][]byte{"userData": []byte(`fake-bootstrap-content`)},
					},
				},
			},
			initObjs: []client.Object{
				&vmv1.VirtualMachine{
					TypeMeta: metav1.TypeMeta{
						APIVersion: "virtualmachine.gke.io/v1",
						Kind:       "VirtualMachine",
					},
					ObjectMeta: metav1.ObjectMeta{
						Name:      "fake-machine-already-exists",
						Namespace: "fake-shoot-project",
						UID:       types.UID("pre-existing-uid-from-api-server"),
					},
					Status: vmv1.VirtualMachineStatus{
						State: vmv1.VirtualMachineStateRunning,
					},
				},
			},
			want: &driver.CreateMachineResponse{
				ProviderID:     "fake-machine-already-exists",
				NodeName:       "fake-machine-already-exists",
				LastKnownState: "Created fake-machine-already-exists",
			},
			wantVM: &vmv1.VirtualMachine{
				ObjectMeta: metav1.ObjectMeta{
					Name:            "fake-machine-already-exists",
					Namespace:       "fake-shoot-project",
					UID:             types.UID("pre-existing-uid-from-api-server"),
					ResourceVersion: "999",
				},
				Status: vmv1.VirtualMachineStatus{
					State: vmv1.VirtualMachineStateRunning,
				},
			},
			wantSecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:            "fake-machine-already-exists-init-script",
					Namespace:       "fake-shoot-project",
					ResourceVersion: "2",
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion: vmv1.SchemeGroupVersion.String(),
							Kind:       "VirtualMachine",
							Name:       "fake-machine-already-exists",
							UID:        types.UID("pre-existing-uid-from-api-server"),
						},
					},
				},
				Data: map[string][]byte{
					"script": []byte(`fake-bootstrap-content`),
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := scheme()
			tracker := k8stesting.NewObjectTracker(s, serializer.NewCodecFactory(s).UniversalDecoder())
			spi := &mock.FakeSPI{
				KubeClient: k8sfake.NewClientBuilder().
					WithScheme(s).
					WithObjectTracker(tracker).
					WithObjects(tt.initObjs...).
					Build(),
				Err: tt.spiError,
			}
			p := &Provider{SPI: spi}
			got, err := p.CreateMachine(tt.args.ctx, tt.args.req)
			if err != nil {
				t.Fatalf("Provider.CreateMachine() has unexpected error = %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Provider.CreateMachine() = %v, want %v", got, tt.want)
			}
			if tt.wantVM != nil {
				gotVM := &vmv1.VirtualMachine{}
				if err = spi.KubeClient.Get(context.TODO(), types.NamespacedName{
					Namespace: tt.wantVM.Namespace,
					Name:      tt.wantVM.Name,
				}, gotVM); err != nil {
					t.Errorf("failed to get VM error = %v", err)
				}
				if !reflect.DeepEqual(gotVM, tt.wantVM) {
					t.Errorf("VM = %v, want %v", gotVM, tt.wantVM)
				}
			}
			if tt.wantSecret != nil {
				gotSecret := &corev1.Secret{}
				if err = spi.KubeClient.Get(context.TODO(), types.NamespacedName{
					Namespace: tt.wantSecret.Namespace,
					Name:      tt.wantSecret.Name,
				}, gotSecret); err != nil {
					t.Errorf("failed to get Secret error = %v", err)
				}
				if !reflect.DeepEqual(gotSecret, tt.wantSecret) {
					t.Errorf("Secret = %v, want %v", gotSecret, tt.wantSecret)
				}
			}
		})
	}
}

func TestProvider_CreateMachineErrors(t *testing.T) {
	type args struct {
		ctx context.Context
		req *driver.CreateMachineRequest
	}
	tests := []struct {
		name     string
		initObjs []client.Object
		spiError error
		args     args
		wantErr  string
	}{
		{
			name: "failed to parse provider spec",
			args: args{
				req: &driver.CreateMachineRequest{
					Machine:      &v1alpha1.Machine{},
					MachineClass: &v1alpha1.MachineClass{},
				},
			},
			wantErr: "failed to parse MachineClass",
		},
		{
			name:     "failed to create k8s client",
			spiError: errors.New("unable to create client"),
			args: args{
				req: &driver.CreateMachineRequest{
					Machine: &v1alpha1.Machine{},
					MachineClass: &v1alpha1.MachineClass{
						ProviderSpec: runtime.RawExtension{
							Raw: []byte(`{}`),
						},
					},
				},
			},
			wantErr: "unable to create client",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := scheme()
			tracker := k8stesting.NewObjectTracker(s, serializer.NewCodecFactory(s).UniversalDecoder())
			c := k8sfake.NewClientBuilder().
				WithScheme(s).
				WithObjectTracker(tracker).
				WithObjects(tt.initObjs...).
				Build()
			spi := &mock.FakeSPI{
				KubeClient: c,
				Err:        tt.spiError,
			}
			p := &Provider{SPI: spi}
			_, err := p.CreateMachine(tt.args.ctx, tt.args.req)
			if (err == nil) || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Provider.CreateMachine() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
		})
	}
}

func TestProvider_DeleteMachine(t *testing.T) {
	type fields struct {
		SPI spi.SessionProviderInterface
	}
	type args struct {
		ctx context.Context
		req *driver.DeleteMachineRequest
	}
	tests := []struct {
		name   string
		fields fields
		args   args
		want   *driver.DeleteMachineResponse
	}{
		{
			name: "delete VM",
			fields: fields{
				SPI: &mock.FakeSPI{
					KubeClient: k8sfake.NewClientBuilder().
						WithScheme(scheme()).
						WithObjects(&vmv1.VirtualMachine{
							ObjectMeta: metav1.ObjectMeta{
								Name:      "fake-machine-1",
								Namespace: "fake-shoot-project",
							},
						}).
						Build(),
				},
			},
			args: args{
				req: &driver.DeleteMachineRequest{
					Machine: &v1alpha1.Machine{
						ObjectMeta: metav1.ObjectMeta{
							Name: "fake-machine-1",
						},
					},
					MachineClass: &v1alpha1.MachineClass{
						ProviderSpec: runtime.RawExtension{
							Raw: func() []byte {
								spec := api.ProviderSpec{
									Project: "fake-shoot-project",
								}
								raw, _ := json.Marshal(spec)
								return raw
							}(),
						},
					},
					Secret: &corev1.Secret{},
				},
			},
			want: &driver.DeleteMachineResponse{},
		},
		{
			name: "delete non-existent VM",
			fields: fields{
				SPI: &mock.FakeSPI{
					KubeClient: k8sfake.NewClientBuilder().
						WithScheme(scheme()).
						Build(),
				},
			},
			args: args{
				req: &driver.DeleteMachineRequest{
					Machine: &v1alpha1.Machine{
						ObjectMeta: metav1.ObjectMeta{
							Name: "fake-machine-1",
						},
					},
					MachineClass: &v1alpha1.MachineClass{
						ProviderSpec: runtime.RawExtension{
							Raw: func() []byte {
								spec := api.ProviderSpec{
									Project: "fake-shoot-project",
								}
								raw, _ := json.Marshal(spec)
								return raw
							}(),
						},
					},
					Secret: &corev1.Secret{},
				},
			},
			want: &driver.DeleteMachineResponse{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &Provider{
				SPI: tt.fields.SPI,
			}
			got, err := p.DeleteMachine(tt.args.ctx, tt.args.req)
			if err != nil {
				t.Errorf("Provider.DeleteMachine() error = %v", err)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Provider.DeleteMachine() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestProvider_DeleteMachineErrors(t *testing.T) {
	type fields struct {
		SPI spi.SessionProviderInterface
	}
	type args struct {
		ctx context.Context
		req *driver.DeleteMachineRequest
	}
	tests := []struct {
		name      string
		fields    fields
		args      args
		wantError string
	}{
		{
			name: "failed to parse provider spec",
			args: args{
				req: &driver.DeleteMachineRequest{
					Machine:      &v1alpha1.Machine{},
					MachineClass: &v1alpha1.MachineClass{},
				},
			},
			wantError: "failed to parse MachineClass",
		},
		{
			name: "failed to create k8s client",
			fields: fields{
				SPI: &mock.FakeSPI{
					Err: errors.New("unable to create client"),
				},
			},
			args: args{
				req: &driver.DeleteMachineRequest{
					Machine: &v1alpha1.Machine{},
					MachineClass: &v1alpha1.MachineClass{
						ProviderSpec: runtime.RawExtension{
							Raw: []byte(`{}`),
						},
					},
				},
			},
			wantError: "unable to create client",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &Provider{
				SPI: tt.fields.SPI,
			}
			_, err := p.DeleteMachine(tt.args.ctx, tt.args.req)
			if (err == nil) || !strings.Contains(err.Error(), tt.wantError) {
				t.Errorf("Provider.DeleteMachine() error = %v, wantErr %v", err, tt.wantError)
				return
			}
		})
	}
}

func TestProvider_TestListMachines(t *testing.T) {
	type args struct {
		ctx context.Context
		req *driver.ListMachinesRequest
	}
	// make argument used to pass to ListMachine() based on namespace and labels
	make_args := func(namespace string, labels map[string]string) args {
		return args{
			ctx: nil,
			req: &driver.ListMachinesRequest{
				MachineClass: &v1alpha1.MachineClass{
					ProviderSpec: runtime.RawExtension{
						Raw: func() []byte {
							spec := api.ProviderSpec{
								Project: namespace,
								Labels:  labels,
							}
							raw, _ := json.Marshal(spec)
							return raw
						}(),
					},
				},
				Secret: &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name: "mcm-secret",
					},
					Data: map[string][]byte{"userData": []byte(`fake-bootstrap-content`)},
				},
			},
		}
	}
	// define testing template for labels and fake VMs
	label1Only := map[string]string{
		"fake-label1": "fake-label-val1",
	}
	label1And2 := map[string]string{
		"fake-label1": "fake-label-val1",
		"fake-label2": "fake-label-val2",
	}
	fakeMachine1 := &vmv1.VirtualMachine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "fake-machine-1",
			Namespace: "fake-ns-1",
			Labels: map[string]string{
				"fake-label1": "fake-label-val1",
				"fake-label2": "fake-label-val2",
			},
		},
	}
	fakeMachine2 := &vmv1.VirtualMachine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "fake-machine-2",
			Namespace: "fake-ns-2",
			Labels: map[string]string{
				"fake-label1": "fake-label-val1",
				"fake-label2": "fake-label-val2",
			},
		},
	}
	fakeMachine3 := &vmv1.VirtualMachine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "fake-machine-3",
			Namespace: "fake-ns-1",
			Labels: map[string]string{
				"fake-label1": "fake-label-val1",
			},
		},
	}
	wantVMs := []*vmv1.VirtualMachine{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "fake-machine-1",
				Namespace: "fake-ns-1",
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "fake-machine-2",
				Namespace: "fake-ns-2",
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "fake-machine-3",
				Namespace: "fake-ns-1",
			},
		},
	}

	// main test suites
	tests := []struct {
		name     string
		initObjs []client.Object
		args     args
		want     *driver.ListMachinesResponse
		wantVMs  []*vmv1.VirtualMachine
	}{
		{
			name: "successfully list machines in Namespace 1 with label 1",
			initObjs: []client.Object{
				fakeMachine1,
				fakeMachine2,
				fakeMachine3,
			},
			args: make_args("fake-ns-1", label1Only),
			want: &driver.ListMachinesResponse{
				MachineList: map[string]string{
					"fake-machine-1": "fake-machine-1",
					"fake-machine-3": "fake-machine-3",
				},
			},
			wantVMs: wantVMs,
		},
		{
			name: "successfully list machines in Namespace 1 with both label 1 and label 2",
			initObjs: []client.Object{
				fakeMachine1,
				fakeMachine2,
				fakeMachine3,
			},
			args: make_args("fake-ns-1", label1And2),
			want: &driver.ListMachinesResponse{
				MachineList: map[string]string{
					"fake-machine-1": "fake-machine-1",
				},
			},
			wantVMs: wantVMs,
		},
		{
			name: "successfully list machines in Namespace 2 with label 1",
			initObjs: []client.Object{
				fakeMachine1,
				fakeMachine2,
				fakeMachine3,
			},
			args: make_args("fake-ns-2", label1Only),
			want: &driver.ListMachinesResponse{
				MachineList: map[string]string{
					"fake-machine-2": "fake-machine-2",
				},
			},
			wantVMs: wantVMs,
		},
		{
			name: "successfully list machines in Namespace 1 with no labels",
			initObjs: []client.Object{
				fakeMachine1,
				fakeMachine2,
				fakeMachine3,
			},
			args: make_args("fake-ns-1", nil),
			want: &driver.ListMachinesResponse{
				MachineList: map[string]string{
					"fake-machine-1": "fake-machine-1",
					"fake-machine-3": "fake-machine-3",
				},
			},
			wantVMs: wantVMs,
		},
		{
			name: "successfully list empty machinelist with combination of namespace and labels where no matching vm is found",
			initObjs: []client.Object{
				fakeMachine1,
				fakeMachine2,
				fakeMachine3,
			},
			args: make_args("fake-ns-3", nil),
			want: &driver.ListMachinesResponse{
				MachineList: map[string]string{},
			},
			wantVMs: wantVMs,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spi := &mock.FakeSPI{
				KubeClient: k8sfake.NewClientBuilder().
					WithScheme(scheme()).
					WithObjects(tt.initObjs...).
					Build(),
			}
			// Verify the vm existence before ListMachines to ensure the VMs exist
			if tt.wantVMs != nil {
				for _, wantVM := range tt.wantVMs {
					gotVM := &vmv1.VirtualMachine{}
					if err := spi.KubeClient.Get(context.TODO(), types.NamespacedName{
						Namespace: wantVM.Namespace,
						Name:      wantVM.Name,
					}, gotVM); err != nil {
						t.Fatalf("failed to get VM: %v", err)
					}
					// Compare relevant fields of gotVM and wantVM
					if gotVM.Name != wantVM.Name || gotVM.Namespace != wantVM.Namespace {
						t.Errorf("Provider.ListMachines() gotVM = %v, wantVM %v", gotVM, wantVM)
					}
				}
			}
			p := &Provider{SPI: spi}
			got, err := p.ListMachines(tt.args.ctx, tt.args.req)
			if err != nil {
				t.Fatalf("Provider.ListMachines() error = %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Provider.ListMachines() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestProvider_ListMachinesErrors(t *testing.T) {
	type args struct {
		ctx context.Context
		req *driver.ListMachinesRequest
	}
	tests := []struct {
		name     string
		spiError error
		args     args
		wantErr  string
	}{
		{
			name: "failed to parse provider spec",
			args: args{
				req: &driver.ListMachinesRequest{
					MachineClass: &v1alpha1.MachineClass{},
				},
			},
			wantErr: "failed to parse MachineClass",
		},
		{
			name:     "failed to create k8s client",
			spiError: errors.New("unable to create client"),
			args: args{
				req: &driver.ListMachinesRequest{
					MachineClass: &v1alpha1.MachineClass{
						ProviderSpec: runtime.RawExtension{
							Raw: []byte(`{}`),
						},
					},
				},
			},
			wantErr: "unable to create client",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := k8sfake.NewClientBuilder().
				WithScheme(scheme()).
				Build()
			spi := &mock.FakeSPI{
				KubeClient: c,
				Err:        tt.spiError,
			}
			p := &Provider{SPI: spi}
			_, err := p.ListMachines(tt.args.ctx, tt.args.req)
			if (err == nil) || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Provider.ListMachines() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
		})
	}
}

func TestProvider_CreateMachine_VMRunningTimeoutSeconds_TimeoutError(t *testing.T) {
	s := scheme()

	initObjs := []client.Object{}

	spi := &mock.FakeSPI{
		KubeClient: k8sfake.NewClientBuilder().
			WithScheme(s).
			WithObjects(initObjs...).
			Build(),
	}

	p := &Provider{SPI: spi}

	req := &driver.CreateMachineRequest{
		Machine: &v1alpha1.Machine{ObjectMeta: metav1.ObjectMeta{Name: "fake-machine-2"}},
		MachineClass: &v1alpha1.MachineClass{
			ProviderSpec: runtime.RawExtension{
				Raw: func() []byte {
					spec := api.ProviderSpec{
						Project:     "fake-shoot-project",
						MachineType: "fake-vm-type",

						SubnetName:              "test-subnet",
						VMRunningTimeoutSeconds: ptr.To(5),
						Disks:                   []*api.Disk{},
					}
					raw, _ := json.Marshal(spec)
					return raw
				}(),
			},
		},
		Secret: &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "mcm-secret"}, Data: map[string][]byte{"userData": []byte(`fake-bootstrap-content`)}},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
	defer cancel()
	_, err := p.CreateMachine(ctx, req)
	if err == nil {
		t.Fatalf("Expected timeout error when VM state stays empty, got nil")
	}
	machineErr, ok := status.FromError(err)
	if !ok {
		t.Fatalf("Expected status error, got: %v", err)
	}
	if machineErr.Code() != codes.DeadlineExceeded {
		t.Errorf("Expected error code DeadlineExceeded, got %s", machineErr.Code())
	}
	expectedSubstr := "failed to wait for VM fake-machine-2 to be running: still not running after 5s (last state: \"\", reason: \"\", message: \"\")"
	if !strings.Contains(machineErr.Message(), expectedSubstr) {
		t.Errorf("Expected error message to contain %q, got %q", expectedSubstr, machineErr.Message())
	}
}

func TestProvider_CreateMachine_VMRunningTimeoutSeconds_Success(t *testing.T) {
	s := scheme()

	initObjs := []client.Object{}

	c := k8sfake.NewClientBuilder().
		WithScheme(s).
		WithObjects(initObjs...).
		Build()

	spi := &mock.FakeSPI{
		KubeClient: c,
	}

	p := &Provider{SPI: spi}

	req := &driver.CreateMachineRequest{
		Machine: &v1alpha1.Machine{ObjectMeta: metav1.ObjectMeta{Name: "fake-machine-3"}},
		MachineClass: &v1alpha1.MachineClass{
			ProviderSpec: runtime.RawExtension{
				Raw: func() []byte {
					spec := api.ProviderSpec{
						Project:                 "fake-shoot-project",
						MachineType:             "fake-vm-type",
						SubnetName:              "test-subnet",
						VMRunningTimeoutSeconds: ptr.To(15),
						Disks:                   []*api.Disk{},
					}
					raw, _ := json.Marshal(spec)
					return raw
				}(),
			},
		},
		Secret: &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "mcm-secret"}, Data: map[string][]byte{"userData": []byte(`fake-bootstrap-content`)}},
	}

	go func() {
		// Actively poll until the VM exists to avoid fixed sleeps and prevent flakiness
		wait.PollUntilContextTimeout(context.Background(), 10*time.Millisecond, 5*time.Second, true, func(ctx context.Context) (bool, error) {
			vm := &vmv1.VirtualMachine{}
			err := c.Get(ctx, client.ObjectKey{Namespace: "fake-shoot-project", Name: "fake-machine-3"}, vm)
			if err == nil {
				vm.Status.State = vmv1.VirtualMachineStateRunning
				if err := c.Update(ctx, vm); err == nil {
					return true, nil
				}
				return false, nil
			}
			return false, nil
		})
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, err := p.CreateMachine(ctx, req)
	if err != nil {
		t.Fatalf("Expected successful wait, but got error: %v", err)
	}
	if resp.ProviderID != "fake-machine-3" {
		t.Errorf("Expected ProviderID fake-machine-3, got %s", resp.ProviderID)
	}
}

func TestProvider_CreateMachine_VMRunningTimeoutSeconds_Errors(t *testing.T) {
	tests := []struct {
		name         string
		machineName  string
		vmState      vmv1.VirtualMachineState
		vmReason     vmv1.VirtualMachineStateReason
		vmMessage    string
		expectedCode codes.Code
	}{
		{
			name:         "Unschedulable",
			machineName:  "fake-machine-4",
			vmState:      vmv1.VirtualMachineStateUnschedulable,
			vmReason:     "InsufficientMemory",
			vmMessage:    "Not enough memory resources on node",
			expectedCode: codes.ResourceExhausted,
		},
		{
			name:         "ErrorConfiguration",
			machineName:  "fake-machine-5",
			vmState:      vmv1.VirtualMachineStateErrorConfiguration,
			vmReason:     "NetworkNotFound",
			vmMessage:    "Default network not found",
			expectedCode: codes.Internal,
		},
		{
			name:         "DiskError",
			machineName:  "fake-machine-6",
			vmState:      vmv1.VirtualMachineStateDiskError,
			vmReason:     "VirtualMachineDiskNotFound",
			vmMessage:    "Disk boot-disk not found",
			expectedCode: codes.Internal,
		},
		{
			name:         "CrashLoopBackoff",
			machineName:  "fake-machine-7",
			vmState:      vmv1.VirtualMachineStateCrashLoopBackoff,
			vmReason:     "VirtualMachineProvisioningFailed",
			vmMessage:    "guest exited unexpectedly",
			expectedCode: codes.Internal,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := scheme()
			initObjs := []client.Object{}
			c := k8sfake.NewClientBuilder().
				WithScheme(s).
				WithObjects(initObjs...).
				Build()

			spi := &mock.FakeSPI{
				KubeClient: c,
			}
			p := &Provider{SPI: spi}

			req := &driver.CreateMachineRequest{
				Machine: &v1alpha1.Machine{ObjectMeta: metav1.ObjectMeta{Name: tt.machineName}},
				MachineClass: &v1alpha1.MachineClass{
					ProviderSpec: runtime.RawExtension{
						Raw: func() []byte {
							spec := api.ProviderSpec{
								Project:                 "fake-shoot-project",
								MachineType:             "fake-vm-type",
								SubnetName:              "test-subnet",
								VMRunningTimeoutSeconds: ptr.To(15),
								Disks:                   []*api.Disk{},
							}
							raw, _ := json.Marshal(spec)
							return raw
						}(),
					},
				},
				Secret: &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "mcm-secret"}, Data: map[string][]byte{"userData": []byte(`fake-bootstrap-content`)}},
			}

			go func() {
				// Actively poll until the VM exists to avoid fixed sleeps and prevent flakiness
				wait.PollUntilContextTimeout(t.Context(), 10*time.Millisecond, 5*time.Second, true, func(ctx context.Context) (bool, error) {
					vm := &vmv1.VirtualMachine{}
					err := c.Get(ctx, client.ObjectKey{Namespace: "fake-shoot-project", Name: tt.machineName}, vm)
					if err == nil {
						vm.Status.State = tt.vmState
						vm.Status.Reason = tt.vmReason
						vm.Status.Message = tt.vmMessage
						if err := c.Update(ctx, vm); err == nil {
							return true, nil
						}
						return false, nil
					}
					return false, nil
				})
			}()

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			_, err := p.CreateMachine(ctx, req)
			if err == nil {
				t.Fatalf("Expected error, but got nil")
			}

			machineErr, ok := status.FromError(err)
			if !ok {
				t.Fatalf("Expected status error, got: %v", err)
			}
			if machineErr.Code() != tt.expectedCode {
				t.Errorf("Expected error code %s, got %s", tt.expectedCode, machineErr.Code())
			}
			if !strings.Contains(machineErr.Message(), tt.machineName) {
				t.Errorf("Expected error message to contain machine name %q, got %q", tt.machineName, machineErr.Message())
			}
			if !strings.Contains(machineErr.Message(), string(tt.vmReason)) {
				t.Errorf("Expected error message to contain VM status reason %q, got %q", tt.vmReason, machineErr.Message())
			}
			if !strings.Contains(machineErr.Message(), tt.vmMessage) {
				t.Errorf("Expected error message to contain VM status message %q, got %q", tt.vmMessage, machineErr.Message())
			}
			if tt.vmState != vmv1.VirtualMachineStateUnschedulable {
				if !strings.Contains(machineErr.Message(), string(tt.vmState)) {
					t.Errorf("Expected error message to contain VM state %q, got %q", tt.vmState, machineErr.Message())
				}
			}
		})
	}
}
