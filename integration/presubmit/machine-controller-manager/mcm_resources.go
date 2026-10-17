// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	machinev1alpha1 "github.com/gardener/machine-controller-manager/pkg/apis/machine/v1alpha1"
	vmv1 "github.com/googlecloudplatform/google-distributed-cloud-apis/pkg/apis/public/virtualmachine/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func triggerProvisioning(ctx context.Context, t *testing.T, env *TestEnv) (*machinev1alpha1.MachineDeployment, *machinev1alpha1.MachineClass, error) {
	t.Helper()
	registerMCMResourceCleanup(t, env)
	mcName := fmt.Sprintf("mcm-presubmit-mc-%s", cfg.CommitHash)
	// Pass the secret name here
	mc, err := createMachineClass(ctx, t, env, mcName)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create MachineClass: %w", err)
	}
	md, err := createMachineDeployment(ctx, t, env, mcName)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create MachineDeployment: %w", err)
	}
	return md, mc, nil
}

func registerMCMResourceCleanup(t *testing.T, env *TestEnv) {
	t.Cleanup(func() {
		t.Log("Deleting MachineClasses...")
		ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cancel()
		ns := client.InNamespace(env.Namespace)
		labelSelector := client.MatchingLabels{"test-run-id": cfg.CommitHash}
		mcList := &machinev1alpha1.MachineClassList{}
		if err := env.VucClient.List(ctx, mcList, ns, labelSelector); err == nil {
			for i := range mcList.Items {
				if len(mcList.Items[i].Finalizers) > 0 {
					mcList.Items[i].Finalizers = nil
					_ = env.VucClient.Update(ctx, &mcList.Items[i])
				}
			}
		}
		if err := env.VucClient.DeleteAllOf(ctx, &machinev1alpha1.MachineClass{}, ns, labelSelector); err != nil {
			t.Logf("Error deleting MachineClasses: %v", err)
		}
		t.Log("MCM Resource cleanup completed.")
	})
	t.Cleanup(func() {
		// Delete MachineDeployments
		// This triggers the cascade: MD -> MachineSet -> Machine
		t.Log("Deleting MachineDeployments...")
		ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cancel()
		ns := client.InNamespace(env.Namespace)
		labelSelector := client.MatchingLabels{"test-run-id": cfg.CommitHash}
		// Capture VM Names per zone BEFORE deleting Machines using a bounded timeout
		// so a stalled management API connection cannot starve MachineDeployment deletion.
		zoneVMs := make(map[string][]string)
		for _, z := range env.AvailableZones {
			zClient := env.MgmtClientForZone(z)
			vmList := &vmv1.VirtualMachineList{}
			listCtx, listCancel := context.WithTimeout(ctx, 15*time.Second)
			if err := zClient.List(listCtx, vmList, client.InNamespace(env.Project), labelSelector); err == nil {
				for _, vm := range vmList.Items {
					zoneVMs[z] = append(zoneVMs[z], vm.Name)
				}
			} else {
				t.Logf("Warning: Could not list VMs in zone %s to track deletion: %v", z, err)
			}
			listCancel()
		}
		if err := env.VucClient.DeleteAllOf(ctx, &machinev1alpha1.MachineDeployment{}, ns, labelSelector); err != nil {
			t.Logf("Error deleting MachineDeployments: %v", err)
		}
		// Wait up to gracefulMachineDeleteTimeout for MCM to delete all Machines gracefully.
		// If MCM is still blocked in an in-flight CreateMachine call or backoff, we directly
		// delete the backing VMs across all zones (which immediately aborts CreateMachine)
		// and strip Machine finalizers so the test namespace never hangs or gets orphaned.
		t.Log("Waiting for all Machines to be deleted (this triggers VM deletion)...")
		err := wait.PollUntilContextTimeout(ctx, 5*time.Second, gracefulMachineDeleteTimeout, true, func(ctx context.Context) (bool, error) {
			machineList := &machinev1alpha1.MachineList{}
			if err := env.VucClient.List(ctx, machineList, ns, labelSelector); err != nil {
				t.Logf("Transient error listing machines during cleanup: %v", err)
				return false, nil
			}
			if len(machineList.Items) == 0 {
				return true, nil
			}
			t.Logf("Waiting for %d Machines to terminate...", len(machineList.Items))
			return false, nil
		})
		if err != nil {
			t.Logf("Graceful Machine deletion timed out after %v; forcing VM and Machine finalizer cleanup...", gracefulMachineDeleteTimeout)
			forceCleanupTestResources(ctx, t, env, zoneVMs)
		}
		t.Log("All Machines deleted successfully.")
		// Verify VM deletion in Mgmt API across all zones where VMs were observed.
		for z, vmNames := range zoneVMs {
			for _, vmName := range vmNames {
				if err := waitForVMDeletionInZone(ctx, t, env, z, vmName); err != nil {
					t.Errorf("Error waiting for VM %s deletion in zone %s: %v", vmName, z, err)
				}
			}
		}
		t.Log("All VirtualMachines deleted successfully.")
	})
}

func createMachineDeployment(ctx context.Context, t *testing.T, env *TestEnv, className string) (*machinev1alpha1.MachineDeployment, error) {
	replicas := int32(1)
	mdName := fmt.Sprintf("mcm-presubmit-md-%s", cfg.CommitHash)
	md := &machinev1alpha1.MachineDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      mdName,
			Namespace: env.Namespace,
			Labels: map[string]string{
				"test-run-id": cfg.CommitHash,
			},
		},
		Spec: machinev1alpha1.MachineDeploymentSpec{
			Replicas: replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"test-run-id": cfg.CommitHash,
				},
			},
			Template: machinev1alpha1.MachineTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"test-run-id": cfg.CommitHash,
					},
				},
				Spec: machinev1alpha1.MachineSpec{
					Class: machinev1alpha1.ClassSpec{
						Kind: "MachineClass",
						Name: className,
					},
					// ProviderID filled by controller
				},
			},
			Strategy: machinev1alpha1.MachineDeploymentStrategy{
				Type: machinev1alpha1.RollingUpdateMachineDeploymentStrategyType,
				RollingUpdate: &machinev1alpha1.RollingUpdateMachineDeployment{
					UpdateConfiguration: machinev1alpha1.UpdateConfiguration{
						MaxUnavailable: &intstr.IntOrString{IntVal: 1},
						MaxSurge:       &intstr.IntOrString{IntVal: 1},
					},
				},
			},
		},
	}

	t.Logf("Creating MachineDeployment:  %s", mdName)
	if err := env.VucClient.Create(ctx, md); err != nil {
		return nil, err
	}
	return md, nil
}
func createMachineClass(ctx context.Context, t *testing.T, env *TestEnv, name string) (*machinev1alpha1.MachineClass, error) {
	// Construct the provider spec using the struct for type safety.
	spec := GDCProviderSpec{
		Name:          name,
		Project:       cfg.Project,
		MachineType:   cfg.MachineType,
		AccessEnabled: true,
		CAData:        env.GDCConfig.CAData,
		RegistryURL:   cfg.RegistryURL,
		OrgClusterURL: env.GDCConfig.OrgClusterURL,
		SubnetName:    "mcm-presubmit-vm-subnet",
		Disks: []GDCProviderDisk{
			{
				Boot:       true,
				AutoDelete: true,
				SizeGb:     50,
				Type:       "Standard",
				Image:      cfg.MachineImage,
				Project:    cfg.Project,
				Labels: map[string]string{
					"disk-label": "test-boot-disk",
				},
			},
		},
		CredentialsSecretRef: map[string]string{
			"name":      mcmSecretName,
			"namespace": env.Namespace,
		},
		Labels: map[string]string{
			"test-run-id": cfg.CommitHash,
		},
		Annotations: map[string]string{
			"description": "Presubmit test machine class",
		},
		// Bound each CreateMachine wait call to 3 minutes so a wedged baremetal node
		// cannot block the MCM worker goroutine for 10 minutes.
		VMRunningTimeoutSeconds: ptr.To(180),
	}
	providerSpecJSON, err := json.Marshal(spec)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal provider spec: %w", err)
	}
	mc := &machinev1alpha1.MachineClass{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: env.Namespace,
			Labels: map[string]string{
				"test-run-id":            cfg.CommitHash,
				"gardener.cloud/purpose": "machineclass",
			},
		},
		ProviderSpec: runtime.RawExtension{
			Raw: providerSpecJSON,
		},
		Provider: "GDCH",
		// Reference to the UserData Secret (created in createUserDataSecret)
		SecretRef: &corev1.SecretReference{
			Name:      mcmSecretName,
			Namespace: env.Namespace,
		},
		NodeTemplate: &machinev1alpha1.NodeTemplate{
			InstanceType: cfg.MachineType,
			Region:       cfg.Region,
			Zone:         env.ActiveZone,
			Capacity: corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse("2"),
				corev1.ResourceMemory: resource.MustParse(
					"7Gi",
				),
				"gpu": resource.MustParse("0"),
			},
		},
	}

	t.Logf("Creating MachineClass: %s (zone: %s)", name, env.ActiveZone)
	if err := env.VucClient.Create(ctx, mc); err != nil {
		return nil, err
	}
	return mc, nil
}

// forceCleanupTestResources directly deletes any remaining VMs, disks, and init-script
// secrets across all candidate zones and strips finalizers from remaining MachineSet and
// Machine objects on the VUC so cleanup cannot hang.
func forceCleanupTestResources(ctx context.Context, t *testing.T, env *TestEnv, zoneVMs map[string][]string) {
	ns := client.InNamespace(env.Namespace)
	labelSelector := client.MatchingLabels{"test-run-id": cfg.CommitHash}

	for _, z := range env.AvailableZones {
		zClient := env.MgmtClientForZone(z)
		vmList := &vmv1.VirtualMachineList{}
		zCtx, zCancel := context.WithTimeout(ctx, 15*time.Second)
		if err := zClient.List(zCtx, vmList, client.InNamespace(env.Project), labelSelector); err == nil {
			for i := range vmList.Items {
				vm := &vmList.Items[i]
				zoneVMs[z] = appendUniqueString(zoneVMs[z], vm.Name)
				_ = zClient.Delete(zCtx, vm)
				_ = zClient.Delete(zCtx, &vmv1.VirtualMachineDisk{
					ObjectMeta: metav1.ObjectMeta{Name: vm.Name + "-boot-disk", Namespace: env.Project},
				})
				_ = zClient.Delete(zCtx, &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{Name: vm.Name + "-init-script", Namespace: env.Project},
				})
				_ = zClient.Delete(zCtx, &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{Name: vm.Name + "-cainit-script", Namespace: env.Project},
				})
			}
		}
		zCancel()
	}

	_ = env.VucClient.DeleteAllOf(ctx, &machinev1alpha1.MachineSet{}, ns, labelSelector)
	machineList := &machinev1alpha1.MachineList{}
	if err := env.VucClient.List(ctx, machineList, ns, labelSelector); err == nil {
		for i := range machineList.Items {
			m := &machineList.Items[i]
			if len(m.Finalizers) > 0 {
				m.Finalizers = nil
				_ = env.VucClient.Update(ctx, m)
			}
			_ = env.VucClient.Delete(ctx, m)
		}
	}
}

func appendUniqueString(slice []string, val string) []string {
	for _, s := range slice {
		if s == val {
			return slice
		}
	}
	return append(slice, val)
}
