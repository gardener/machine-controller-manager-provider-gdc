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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
	// Verify Machine and VirtualMachine with automatic multi-zone failover if a baremetal
	// host in the initial zone has a wedged KubeVirt/CSI runtime (leaving the VM stuck in Starting).
	if err = waitForMachineAndVMWithZoneFailover(ctx, t, env, md, mc); err != nil {
		return err
	}
	if err = verifyGDCResources(ctx, t, env); err != nil {
		return fmt.Errorf("failed to verify GDC resources: %w", err)
	}
	t.Log("All Resources Verified Created Successfully.")
	return nil
}

// waitForMachineAndVMWithZoneFailover polls for the Machine to reach Phase=="Pending" and its
// backing VirtualMachine in env.ActiveZone to reach State=="Running". If the VM in the current
// zone remains stuck in "Starting" beyond vmStartupZoneFailoverTimeout (or enters a terminal error
// state) and additional zones are configured in env.AvailableZones, it cleans up the stuck
// resources in the current zone and fails over to the next candidate zone.
func waitForMachineAndVMWithZoneFailover(ctx context.Context, t *testing.T, env *TestEnv, md *machinev1alpha1.MachineDeployment, mc *machinev1alpha1.MachineClass) error {
	t.Helper()
	t.Logf("Verifying Machine and VirtualMachine (active zone: %s, available zones: %v)...", env.ActiveZone, env.AvailableZones)

	zoneIdx := 0
	for i, z := range env.AvailableZones {
		if z == env.ActiveZone {
			zoneIdx = i
			break
		}
	}
	zoneAttemptStart := time.Now()
	ns := client.InNamespace(env.Namespace)
	labelSelector := client.MatchingLabels{"test-run-id": cfg.CommitHash}

	err := wait.PollUntilContextTimeout(ctx, 5*time.Second, provisioningTimeout, true, func(ctx context.Context) (bool, error) {
		var machineList machinev1alpha1.MachineList
		if lErr := env.VucClient.List(ctx, &machineList, ns, labelSelector); lErr != nil {
			t.Logf("Transient error listing Machines: %v", lErr)
			return false, nil
		}

		var vmList vmv1.VirtualMachineList
		_ = env.MgmtClient.List(ctx, &vmList, client.InNamespace(env.Project), labelSelector)

		var (
			machinePending bool
			machineFailed  bool
			failureReason  string
			vmRunning      bool
		)

		for _, m := range machineList.Items {
			phase := m.Status.CurrentStatus.Phase
			if phase == "Pending" || phase == "Running" {
				machinePending = true
			}
			if phase == "CrashLoopBackOff" || phase == "Failed" {
				machineFailed = true
				failureReason = fmt.Sprintf("Machine %s entered %s: %s", m.Name, phase, m.Status.LastOperation.Description)
			}
		}

		for _, vm := range vmList.Items {
			switch vm.Status.State {
			case vmv1.VirtualMachineStateRunning:
				vmRunning = true
			case vmv1.VirtualMachineStateUnschedulable,
				vmv1.VirtualMachineStateErrorConfiguration,
				vmv1.VirtualMachineStateDiskError,
				vmv1.VirtualMachineStateCrashLoopBackoff:
				machineFailed = true
				failureReason = fmt.Sprintf("VirtualMachine %s in zone %s entered %s (%s: %s)", vm.Name, env.ActiveZone, vm.Status.State, vm.Status.Reason, vm.Status.Message)
			case vmv1.VirtualMachineStateStarting, vmv1.VirtualMachineStateStopped, "":
				if !vm.CreationTimestamp.IsZero() && time.Since(vm.CreationTimestamp.Time) >= vmStartupZoneFailoverTimeout {
					machineFailed = true
					failureReason = fmt.Sprintf("VirtualMachine %s in zone %s stuck in state %q for %s", vm.Name, env.ActiveZone, vm.Status.State, time.Since(vm.CreationTimestamp.Time).Round(time.Second))
				}
			}
		}

		if machinePending && vmRunning {
			return true, nil
		}

		if !machineFailed && time.Since(zoneAttemptStart) >= vmStartupZoneFailoverTimeout+30*time.Second && !vmRunning {
			machineFailed = true
			failureReason = fmt.Sprintf("VM in zone %s did not reach Running within %s", env.ActiveZone, time.Since(zoneAttemptStart).Round(time.Second))
		}

		if machineFailed && zoneIdx+1 < len(env.AvailableZones) {
			nextZone := env.AvailableZones[zoneIdx+1]
			t.Logf("WARNING: %s. Failing over from zone %s to %s...", failureReason, env.ActiveZone, nextZone)
			if fErr := failoverProvisioningToZone(ctx, t, env, nextZone, md.Name, mc.Name); fErr != nil {
				t.Logf("Warning: error during zone failover to %s: %v", nextZone, fErr)
			} else {
				zoneIdx++
				zoneAttemptStart = time.Now()
			}
			return false, nil
		}

		return false, nil
	})
	if err != nil {
		t.Log("DEBUG: Failed to verify Machine. Listing Machines...")
		var list machinev1alpha1.MachineList
		if lErr := env.VucClient.List(ctx, &list, ns, labelSelector); lErr != nil {
			t.Logf("Failed to list Machines: %v", lErr)
		} else {
			for _, item := range list.Items {
				t.Logf("Machine %s status: %+v", item.Name, item.Status)
			}
		}
		return fmt.Errorf("failed to verify Machine: %w", err)
	}
	return nil
}

// failoverProvisioningToZone tears down the stuck VM and Machine resources in the current zone,
// switches env.ActiveZone to nextZone, and recreates the MachineClass and MachineDeployment.
func failoverProvisioningToZone(ctx context.Context, t *testing.T, env *TestEnv, nextZone, mdName, mcName string) error {
	ns := client.InNamespace(env.Namespace)
	labelSelector := client.MatchingLabels{"test-run-id": cfg.CommitHash}

	// 1. Delete the MachineDeployment and MachineSet on the VUC first so MCM does not
	// spawn another Machine in the unhealthy zone.
	_ = env.VucClient.DeleteAllOf(ctx, &machinev1alpha1.MachineDeployment{}, ns, labelSelector)
	_ = env.VucClient.DeleteAllOf(ctx, &machinev1alpha1.MachineSet{}, ns, labelSelector)

	// 2. Delete the stuck VirtualMachine (and its boot disk/secrets) in the current zone.
	// Deleting the VM causes the in-flight CreateMachine poll loop in MCM to immediately
	// abort with codes.Aborted so the MCM worker goroutine is freed within 5s.
	oldZone := env.ActiveZone
	oldMgmtClient := env.MgmtClientForZone(oldZone)
	var vmList vmv1.VirtualMachineList
	if err := oldMgmtClient.List(ctx, &vmList, client.InNamespace(env.Project), labelSelector); err == nil {
		for i := range vmList.Items {
			vm := &vmList.Items[i]
			t.Logf("Deleting stuck VirtualMachine %s in zone %s...", vm.Name, oldZone)
			_ = oldMgmtClient.Delete(ctx, vm)
			_ = oldMgmtClient.Delete(ctx, &vmv1.VirtualMachineDisk{
				ObjectMeta: metav1.ObjectMeta{Name: vm.Name + "-boot-disk", Namespace: env.Project},
			})
			_ = oldMgmtClient.Delete(ctx, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: vm.Name + "-init-script", Namespace: env.Project},
			})
			_ = oldMgmtClient.Delete(ctx, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: vm.Name + "-cainit-script", Namespace: env.Project},
			})
		}
	}

	// 3. Strip finalizers and delete stuck Machine and MachineClass objects on the VUC.
	var machineList machinev1alpha1.MachineList
	if err := env.VucClient.List(ctx, &machineList, ns, labelSelector); err == nil {
		for i := range machineList.Items {
			m := &machineList.Items[i]
			if len(m.Finalizers) > 0 {
				m.Finalizers = nil
				_ = env.VucClient.Update(ctx, m)
			}
			_ = env.VucClient.Delete(ctx, m)
		}
	}
	var mcList machinev1alpha1.MachineClassList
	if err := env.VucClient.List(ctx, &mcList, ns, labelSelector); err == nil {
		for i := range mcList.Items {
			mcItem := &mcList.Items[i]
			if len(mcItem.Finalizers) > 0 {
				mcItem.Finalizers = nil
				_ = env.VucClient.Update(ctx, mcItem)
			}
			_ = env.VucClient.Delete(ctx, mcItem)
		}
	}

	// Wait briefly for the old MachineClass and MachineDeployment to be gone from the VUC API server.
	_ = wait.PollUntilContextTimeout(ctx, time.Second, 15*time.Second, true, func(ctx context.Context) (bool, error) {
		errMC := env.VucClient.Get(ctx, types.NamespacedName{Namespace: env.Namespace, Name: mcName}, &machinev1alpha1.MachineClass{})
		errMD := env.VucClient.Get(ctx, types.NamespacedName{Namespace: env.Namespace, Name: mdName}, &machinev1alpha1.MachineDeployment{})
		return errors.IsNotFound(errMC) && errors.IsNotFound(errMD), nil
	})

	// 4. Switch active zone to nextZone and recreate MachineClass and MachineDeployment.
	env.SetActiveZone(nextZone)
	if _, err := createMachineClass(ctx, t, env, mcName); err != nil {
		return fmt.Errorf("failed to recreate MachineClass in zone %s: %w", nextZone, err)
	}
	if _, err := createMachineDeployment(ctx, t, env, mcName); err != nil {
		return fmt.Errorf("failed to recreate MachineDeployment in zone %s: %w", nextZone, err)
	}
	t.Logf("Recreated MachineClass %s and MachineDeployment %s targeting zone %s", mcName, mdName, nextZone)
	return nil
}

func verifyGDCResources(ctx context.Context, t *testing.T, env *TestEnv) error {
	t.Logf("verifying resources in Management API (zone: %s)", env.ActiveZone)
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

// waitForVMDeletionInZone waits until the VirtualMachine is fully deleted from the zone's Management API.
func waitForVMDeletionInZone(ctx context.Context, t *testing.T, env *TestEnv, zone, vmName string) error {
	t.Logf("Waiting for VirtualMachine %q in zone %s to be deleted...", vmName, zone)
	zClient := env.MgmtClientForZone(zone)
	vmKey := types.NamespacedName{
		Name:      vmName,
		Namespace: env.Project,
	}
	return wait.PollUntilContextTimeout(ctx, 5*time.Second, cleanupTimeout, true, func(ctx context.Context) (bool, error) {
		vm := &vmv1.VirtualMachine{}
		err := zClient.Get(ctx, vmKey, vm)
		// SUCCESS: The VM is gone.
		if errors.IsNotFound(err) {
			t.Logf("VirtualMachine %q in zone %s successfully deleted.", vmName, zone)
			return true, nil
		}
		// RETRY: Transient API error
		if err != nil {
			t.Logf("Transient error getting VM in zone %s during deletion wait: %v", zone, err)
			return false, nil
		}
		// PENDING: VM still exists
		if vm.DeletionTimestamp != nil {
			t.Logf("VirtualMachine %q in zone %s is terminating... (DeletionTimestamp: %s)", vmName, zone, vm.DeletionTimestamp)
		} else {
			t.Logf("VirtualMachine %q in zone %s exists but DeletionTimestamp is not set yet. Deleting...", vmName, zone)
			_ = zClient.Delete(ctx, vm)
		}
		return false, nil
	})
}
