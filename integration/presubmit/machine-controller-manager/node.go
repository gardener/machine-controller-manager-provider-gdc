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
	"fmt"
	"testing"
	"time"

	machinev1alpha1 "github.com/gardener/machine-controller-manager/pkg/apis/machine/v1alpha1"
	vmv1 "github.com/googlecloudplatform/google-distributed-cloud-apis/pkg/apis/public/virtualmachine/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/watch"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/gardener/machine-controller-manager-provider-gdc/integration/pkg/kubernetes"
)

func verifyProvisioning(ctx context.Context, t *testing.T, env *TestEnv, md *machinev1alpha1.MachineDeployment, mc *machinev1alpha1.MachineClass) error {
	t.Helper()
	t.Log("Verifying Resources...")
	// Verify MachineClass
	t.Log("Verifying MachineClass...")
	err := kubernetes.WaitForCondition(ctx, provisioningTimeout, func() (watch.Interface, error) {
		return env.VucClient.Watch(ctx, &machinev1alpha1.MachineClassList{}, client.InNamespace(env.Namespace), client.MatchingLabels{"test-run-id": cfg.CommitHash})
	}, func(obj *machinev1alpha1.MachineClass) bool {
		return obj.Name == mc.Name
	})
	if err != nil {
		t.Logf("DEBUG: Failed to verify MachineClass %s. Listing MachineClasses...", mc.Name)
		var list machinev1alpha1.MachineClassList
		if lErr := env.VucClient.List(ctx, &list, client.InNamespace(env.Namespace), client.MatchingLabels{"test-run-id": cfg.CommitHash}); lErr != nil {
			t.Logf("Failed to list MachineClasses: %v", lErr)
		} else {
			for _, item := range list.Items {
				t.Logf("Found MachineClass: %s", item.Name)
			}
		}
		return fmt.Errorf("failed to verify MachineClass: %w", err)
	}
	// Verify MachineDeployment
	t.Log("Verifying MachineDeployment...")
	err = kubernetes.WaitForCondition(ctx, provisioningTimeout, func() (watch.Interface, error) {
		return env.VucClient.Watch(ctx, &machinev1alpha1.MachineDeploymentList{}, client.InNamespace(env.Namespace), client.MatchingLabels{"test-run-id": cfg.CommitHash})
	}, func(obj *machinev1alpha1.MachineDeployment) bool {
		return obj.Name == md.Name && obj.Status.Replicas == 1 && len(obj.Status.FailedMachines) == 0
	})
	if err != nil {
		t.Logf("DEBUG: Failed to verify MachineDeployment %s. Listing MachineDeployments...", md.Name)
		var list machinev1alpha1.MachineDeploymentList
		if lErr := env.VucClient.List(ctx, &list, client.InNamespace(env.Namespace), client.MatchingLabels{"test-run-id": cfg.CommitHash}); lErr != nil {
			t.Logf("Failed to list MachineDeployments: %v", lErr)
		} else {
			for _, item := range list.Items {
				t.Logf("MachineDeployment %s status: %+v", item.Name, item.Status)
			}
		}
		return fmt.Errorf("failed to verify MachineDeployment: %w", err)
	}
	// Verify MachineSet
	t.Log("Verifying MachineSet...")
	err = kubernetes.WaitForCondition(ctx, provisioningTimeout, func() (watch.Interface, error) {
		return env.VucClient.Watch(ctx, &machinev1alpha1.MachineSetList{}, client.InNamespace(env.Namespace), client.MatchingLabels{"test-run-id": cfg.CommitHash})
	}, func(obj *machinev1alpha1.MachineSet) bool {
		return obj.Status.Replicas == 1 && (obj.Status.FailedMachines == nil || len(*obj.Status.FailedMachines) == 0)
	})
	if err != nil {
		t.Logf("DEBUG: Failed to verify MachineSet. Listing MachineSets...")
		var list machinev1alpha1.MachineSetList
		if lErr := env.VucClient.List(ctx, &list, client.InNamespace(env.Namespace), client.MatchingLabels{"test-run-id": cfg.CommitHash}); lErr != nil {
			t.Logf("Failed to list MachineSets: %v", lErr)
		} else {
			for _, item := range list.Items {
				t.Logf("MachineSet %s status: %+v", item.Name, item.Status)
			}
		}
		return fmt.Errorf("failed to verify MachineSet: %w", err)
	}
	// Verify Machine
	t.Log("Verifying Machine...")
	err = kubernetes.WaitForCondition(ctx, provisioningTimeout, func() (watch.Interface, error) {
		return env.VucClient.Watch(ctx, &machinev1alpha1.MachineList{}, client.InNamespace(env.Namespace), client.MatchingLabels{"test-run-id": cfg.CommitHash})
	}, func(obj *machinev1alpha1.Machine) bool {
		// Machine will be Pending because we used a dummy init script
		// As long as Machine doesn't fail and the following check for VM success
		// It indicates the success of this check
		return obj.Status.CurrentStatus.Phase == "Pending"
	})
	if err != nil {
		t.Logf("DEBUG: Failed to verify Machine. Listing Machines...")
		var list machinev1alpha1.MachineList
		if lErr := env.VucClient.List(ctx, &list, client.InNamespace(env.Namespace), client.MatchingLabels{"test-run-id": cfg.CommitHash}); lErr != nil {
			t.Logf("Failed to list Machines: %v", lErr)
		} else {
			for _, item := range list.Items {
				t.Logf("Machine %s status: %+v", item.Name, item.Status)
			}
		}
		return fmt.Errorf("failed to verify Machine: %w", err)
	}
	if err = verifyGDCResources(ctx, t, env); err != nil {
		return fmt.Errorf("failed to verify GDC resources: %w", err)
	}
	t.Log("All Resources Verified Created Successfully.")
	return nil
}
func verifyGDCResources(ctx context.Context, t *testing.T, env *TestEnv) error {
	t.Log("verifying resources in Management API")
	t.Log("Verifying VirtualMachine...")
	var vmName string
	// Wait for VM to reach 'Running' state
	err := kubernetes.WaitForCondition(ctx, provisioningTimeout, func() (watch.Interface, error) {
		return env.MgmtClient.Watch(ctx, &vmv1.VirtualMachineList{}, client.InNamespace(env.Project), client.MatchingLabels{"test-run-id": cfg.CommitHash})
	}, func(obj *vmv1.VirtualMachine) bool {
		if obj.Status.State == vmv1.VirtualMachineStateRunning {
			vmName = obj.Name
			return true
		}
		return false
	})
	if err != nil {
		t.Logf("DEBUG: Failed to verify VirtualMachine. Listing VirtualMachines...")
		var list vmv1.VirtualMachineList
		if lErr := env.MgmtClient.List(ctx, &list, client.InNamespace(env.Project), client.MatchingLabels{"test-run-id": cfg.CommitHash}); lErr != nil {
			t.Logf("Failed to list VirtualMachines: %v", lErr)
		} else {
			for _, item := range list.Items {
				t.Logf("VirtualMachine %s status: %+v", item.Name, item.Status)
			}
		}
		return fmt.Errorf("failed to verify virtualmachine: %w", err)
	}
	t.Log("Verifying that userData(vm init script) has been successfully passed to the VM")
	// MCM creates the UserData as a Secret named <machine-name>-init-script
	vmSecretName := fmt.Sprintf("%s-init-script", vmName)
	secretKey := types.NamespacedName{
		Name:      vmSecretName,
		Namespace: env.Project,
	}
	vmSecret := &corev1.Secret{}
	err = env.MgmtClient.Get(ctx, secretKey, vmSecret)
	if err != nil {
		return fmt.Errorf("failed to get user data secret %q: %w", vmSecretName, err)
	}
	userData, ok := vmSecret.Data["script"]
	if !ok {
		return fmt.Errorf("cannot find 'script' key in secret %s", vmSecretName)
	}
	userDataString := string(userData)
	// Validate the content matches what we injected
	if userDataString != dummyUserData {
		return fmt.Errorf("UserData mismatch!\nExpected: %q\nActual:   %q", dummyUserData, userDataString)
	}
	t.Log("VirtualMachine is Running and UserData verification passed.")
	return nil
}

// waitForVMDeletion waits until the VirtualMachine is fully deleted from the API.
func waitForVMDeletion(ctx context.Context, t *testing.T, env *TestEnv, vmName string) error {
	t.Logf("Waiting for VirtualMachine %q to be deleted...", vmName)
	vmKey := types.NamespacedName{
		Name:      vmName,
		Namespace: env.Project,
	}
	// NOTE: Removed cleanupTimeout from args as it was causing a syntax error.
	return wait.PollUntilContextTimeout(ctx, 5*time.Second, cleanupTimeout, true, func(ctx context.Context) (bool, error) {
		vm := &vmv1.VirtualMachine{}
		err := env.MgmtClient.Get(ctx, vmKey, vm)
		// SUCCESS: The VM is gone.
		if errors.IsNotFound(err) {
			t.Logf("VirtualMachine %q successfully deleted.", vmName)
			return true, nil
		}
		// RETRY: Transient API error
		if err != nil {
			t.Logf("Transient error getting VM during deletion wait: %v", err)
			return false, nil
		}
		// PENDING: VM still exists
		if vm.DeletionTimestamp != nil {
			t.Logf("VirtualMachine %q is terminating... (DeletionTimestamp: %s)", vmName, vm.DeletionTimestamp)
		} else {
			t.Logf("VirtualMachine %q exists but DeletionTimestamp is not set yet. Waiting...", vmName)
		}
		return false, nil
	})
}
