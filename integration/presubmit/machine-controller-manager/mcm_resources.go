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
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func triggerProvisioning(ctx context.Context, t *testing.T, env *TestEnv) (*machinev1alpha1.MachineDeployment, *machinev1alpha1.MachineClass, error) {
	t.Helper()
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
	t.Cleanup(func() {
		// Delete MachineDeployments
		// This triggers the cascade: MD -> MachineSet -> Machine
		t.Log("Deleting MachineDeployments...")
		ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cancel()
		ns := client.InNamespace(env.Namespace)
		labelSelector := client.MatchingLabels{"test-run-id": cfg.CommitHash}
		// Capture VM Names BEFORE deleting Machines
		// Will use these names to verify infrastructure deletion later.
		vmNames := []string{}
		vmList := &vmv1.VirtualMachineList{}
		if err := env.MgmtClient.List(ctx, vmList, client.InNamespace(env.Project), labelSelector); err == nil {
			for _, vm := range vmList.Items {
				vmNames = append(vmNames, vm.Name)
			}
		} else {
			t.Logf("Warning: Could not list VMs to track deletion: %v", err)
		}
		if err := env.VucClient.DeleteAllOf(ctx, &machinev1alpha1.MachineDeployment{}, ns, labelSelector); err != nil {
			t.Logf("Error deleting MachineDeployments: %v", err)
		}
		// Wait for all Machines to be deleted.
		t.Log("Waiting for all Machines to be deleted (this triggers VM deletion)...")
		err := wait.PollUntilContextTimeout(ctx, 5*time.Second, cleanupTimeout, true, func(ctx context.Context) (bool, error) {
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
			t.Errorf("FATAL: Machines failed to delete within timeout! VM might be orphaned. Error: %v", err)
		}
		t.Log("All Machines deleted successfully.")
		// Verify VM deletion in Mgmt API
		for _, vmName := range vmNames {
			if err := waitForVMDeletion(ctx, t, env, vmName); err != nil {
				t.Errorf("Error waiting for VM deletion: %v", err)
			}
		}
		t.Log("All VirtualMachines deleted successfully.")
	})

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
			Zone:         cfg.Zone,
			Capacity: corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse("2"),
				corev1.ResourceMemory: resource.MustParse(
					"7Gi",
				),
				"gpu": resource.MustParse("0"),
			},
		},
	}
	t.Cleanup(func() {
		t.Log("Deleting MachineClasses...")
		ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cancel()
		ns := client.InNamespace(env.Namespace)
		labelSelector := client.MatchingLabels{"test-run-id": cfg.CommitHash}
		if err := env.VucClient.DeleteAllOf(ctx, &machinev1alpha1.MachineClass{}, ns, labelSelector); err != nil {
			t.Logf("Error deleting MachineClasses: %v", err)
		}
		t.Log("MCM Resource cleanup completed.")
	})

	t.Logf("Creating MachineClass: %s", name)
	if err := env.VucClient.Create(ctx, mc); err != nil {
		return nil, err
	}
	return mc, nil
}
