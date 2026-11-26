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
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
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
	if cfg.RegistryURL != "" {
		if err := verifyCAInitScript(ctx, t, env, vmName); err != nil {
			return fmt.Errorf("failed to verify the registry CA init script: %w", err)
		}
		t.Log("Registry CA init script verification passed.")
	}
	return nil
}

// caInitScriptSecretName returns the name of the secret that holds the registry CA init
// script of a VM.
func caInitScriptSecretName(vmName string) string {
	return vmName + "-cainit-script"
}

// verifyCAInitScript checks that the VM runs the registry CA init script before the user
// data, that the script secret is owned by the VM, and that the stored script installs
// exactly the CA bundle of the MachineClass for the configured registry.
func verifyCAInitScript(ctx context.Context, t *testing.T, env *TestEnv, vmName string) error {
	t.Helper()
	t.Log("Verifying the registry CA init script of the VM...")
	vm := &vmv1.VirtualMachine{}
	if err := env.MgmtClient.Get(ctx, types.NamespacedName{Name: vmName, Namespace: env.Project}, vm); err != nil {
		return fmt.Errorf("failed to get VirtualMachine %q: %w", vmName, err)
	}

	secretName := caInitScriptSecretName(vmName)
	wantScripts := []string{"caInit=" + secretName, "userData=" + vmName + "-init-script"}
	gotScripts := make([]string, 0, len(vm.Spec.StartupScripts))
	for _, script := range vm.Spec.StartupScripts {
		ref := ""
		if script.ScriptSecretRef != nil {
			ref = script.ScriptSecretRef.Name
		}
		gotScripts = append(gotScripts, script.Name+"="+ref)
	}
	if !slices.Equal(gotScripts, wantScripts) {
		return fmt.Errorf("startup scripts of VirtualMachine %q are %v, want %v", vmName, gotScripts, wantScripts)
	}

	// The driver sets the owner reference right after it created the VM, so that the
	// script is removed together with the VM.
	secret := &corev1.Secret{}
	secretKey := types.NamespacedName{Name: secretName, Namespace: env.Project}
	if err := wait.PollUntilContextTimeout(ctx, 5*time.Second, time.Minute, true, func(ctx context.Context) (bool, error) {
		if err := env.MgmtClient.Get(ctx, secretKey, secret); err != nil {
			t.Logf("Waiting for CA init script secret %q: %v", secretName, err)
			return false, nil
		}
		for _, owner := range secret.OwnerReferences {
			if owner.UID == vm.UID {
				return true, nil
			}
		}
		t.Logf("Waiting for CA init script secret %q to be owned by VirtualMachine %q", secretName, vmName)
		return false, nil
	}); err != nil {
		return fmt.Errorf("CA init script secret %q does not exist or is not owned by VirtualMachine %q: %w", secretName, vmName, err)
	}

	script, ok := secret.Data["script"]
	if !ok {
		return fmt.Errorf("cannot find 'script' key in secret %s", secretName)
	}
	caBundle, err := base64.StdEncoding.DecodeString(env.GDCConfig.CAData)
	if err != nil {
		return fmt.Errorf("failed to decode the CA data of the MachineClass: %w", err)
	}
	return runCAInitScript(t, string(script), cfg.RegistryURL, caBundle)
}

// runCAInitScript executes the CA init script the way the VM does, except that it writes
// below a scratch directory instead of /etc/containerd/certs.d, and compares the CA file
// for the registry with the expected bundle.
func runCAInitScript(t *testing.T, script, registry string, wantCA []byte) error {
	t.Helper()
	const certsDir = "/etc/containerd/certs.d"
	if !strings.Contains(script, certsDir) {
		return fmt.Errorf("CA init script does not write below %s:\n%s", certsDir, script)
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		return fmt.Errorf("bash is required to run the CA init script: %w", err)
	}
	if _, err := exec.LookPath("openssl"); err != nil {
		return fmt.Errorf("openssl is required to run the CA init script: %w", err)
	}
	scratchDir := filepath.Join(t.TempDir(), "certs.d")
	// #nosec G204 -- runs the script the driver rendered from this test's own MachineClass against a scratch directory
	cmd := exec.Command(bash, "-c", strings.ReplaceAll(script, certsDir, scratchDir))
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("CA init script failed: %w\n%s", err, out)
	}
	caFile := filepath.Join(scratchDir, registry, "ca.crt")
	gotCA, err := os.ReadFile(caFile) // #nosec G304 -- path below the test's own temporary directory
	if err != nil {
		return fmt.Errorf("CA init script did not write the CA file for registry %q: %w", registry, err)
	}
	if !bytes.Equal(gotCA, wantCA) {
		return fmt.Errorf("CA file written for registry %q has %d bytes and differs from the CA bundle of the MachineClass (%d bytes)", registry, len(gotCA), len(wantCA))
	}
	return nil
}

// waitForCAInitScriptDeletion waits until the CA init script secret of a deleted VM is gone.
// If it is still there after the timeout it is deleted, so that test runs do not pile up
// secrets in the project, and an error is returned.
func waitForCAInitScriptDeletion(ctx context.Context, t *testing.T, env *TestEnv, vmName string) error {
	t.Helper()
	secretKey := types.NamespacedName{Name: caInitScriptSecretName(vmName), Namespace: env.Project}
	secret := &corev1.Secret{}
	pollErr := wait.PollUntilContextTimeout(ctx, 5*time.Second, 2*time.Minute, true, func(ctx context.Context) (bool, error) {
		err := env.MgmtClient.Get(ctx, secretKey, secret)
		if errors.IsNotFound(err) {
			return true, nil
		}
		if err != nil {
			t.Logf("Transient error getting CA init script secret %q: %v", secretKey.Name, err)
		}
		return false, nil
	})
	if pollErr == nil {
		t.Logf("CA init script secret %q was removed with the VirtualMachine.", secretKey.Name)
		return nil
	}
	secret.Name, secret.Namespace = secretKey.Name, secretKey.Namespace
	if err := env.MgmtClient.Delete(ctx, secret); err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("CA init script secret %q was not removed with VirtualMachine %q and could not be deleted: %w", secretKey.Name, vmName, err)
	}
	return fmt.Errorf("CA init script secret %q was not removed with VirtualMachine %q; deleted it", secretKey.Name, vmName)
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
