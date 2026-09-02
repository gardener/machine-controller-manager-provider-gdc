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
	"fmt"
	"time"

	"github.com/gardener/machine-controller-manager/pkg/apis/machine/v1alpha1"
	"github.com/gardener/machine-controller-manager/pkg/util/provider/driver"
	"github.com/gardener/machine-controller-manager/pkg/util/provider/machinecodes/codes"
	"github.com/gardener/machine-controller-manager/pkg/util/provider/machinecodes/status"
	networkingv1 "github.com/googlecloudplatform/google-distributed-cloud-apis/pkg/apis/public/networking/v1"
	vmv1 "github.com/googlecloudplatform/google-distributed-cloud-apis/pkg/apis/public/virtualmachine/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	k8swait "k8s.io/apimachinery/pkg/util/wait"
	klog "k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	api "github.com/gardener/machine-controller-manager-provider-gdc/pkg/provider/apis"
)

const GDCNetworkName = "default"
const defaultVMWaitTimeout = 10 * time.Minute
const vmStatusPollInterval = 5 * time.Second

// CreateMachine handles a machine creation request
// REQUIRED METHOD
//
// REQUEST PARAMETERS (driver.CreateMachineRequest)
// Machine               *v1alpha1.Machine        Machine object from whom VM is to be created
// MachineClass          *v1alpha1.MachineClass   MachineClass backing the machine object
// Secret                *corev1.Secret           Kubernetes secret that contains any sensitive data/credentials
//
// RESPONSE PARAMETERS (driver.CreateMachineResponse)
// ProviderID            string                   Unique identification of the VM at the cloud provider. This could be the same/different from req.MachineName.
//
//	ProviderID typically matches with the node.Spec.ProviderID on the node object.
//	Eg: gce://project-name/region/vm-ProviderID
//
// NodeName              string                   Returns the name of the node-object that the VM registers with Kubernetes.
//
//	This could be different from req.MachineName as well
//
// LastKnownState        string                   (Optional) Last known state of VM during the current operation.
//
//	Could be helpful to continue operations in future requests.
//
// OPTIONAL IMPLEMENTATION LOGIC
// It is optionally expected by the safety controller to use an identification mechanism to map the VM Created by a providerSpec.
// These could be done using tag(s)/resource-groups etc.
// This logic is used by safety controller to delete orphan VMs which are not backed by any machine CRD
func (p *Provider) CreateMachine(ctx context.Context, req *driver.CreateMachineRequest) (*driver.CreateMachineResponse, error) {
	// Log messages to track request
	klog.V(2).Infof("Machine creation request has been received for %q", req.Machine.Name)
	defer klog.V(2).Infof("Machine creation request has been processed for %q", req.Machine.Name)

	spec := &api.ProviderSpec{}
	err := json.Unmarshal(req.MachineClass.ProviderSpec.Raw, spec)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "failed to parse MachineClass")
	}

	kubeClient, err := p.SPI.Client(req.Secret, spec)
	if err != nil {
		return nil, wrapError(err)
	}

	var caInitScriptSecret *corev1.Secret
	// if vm uses image from RegistryURL, need to specify cert to authorize
	if spec.RegistryURL != "" && spec.CAData != "" {
		caInitScriptSecret, err = p.createCAInitScriptSecret(ctx, kubeClient, req.Machine, spec)
		if err != nil {
			return nil, wrapError(err)
		}
	}

	script, err := p.createSecret(ctx, kubeClient, req.Machine, spec, req.Secret)
	if err != nil {
		return nil, wrapError(err)
	}

	disks := []vmv1.DiskAttachment{}
	for _, diskSpec := range spec.Disks {
		disk, err := p.createDisk(ctx, kubeClient, req.Machine, spec, diskSpec)
		if err != nil {
			return nil, wrapError(err)
		}
		disks = append(disks, *disk)
	}

	vm, err := p.createMachine(ctx, kubeClient, req.Machine, spec, disks, script.Name, caInitScriptSecret)
	if err != nil {
		return nil, wrapError(err)
	}

	if _, err := controllerutil.CreateOrUpdate(ctx, kubeClient, script, func() error {
		return controllerutil.SetOwnerReference(vm, script, kubeClient.Scheme())
	}); err != nil {
		return nil, status.Error(codes.Internal, fmt.Sprintf("failed to set owner reference on init script: %v", err))
	}

	if caInitScriptSecret != nil {
		if _, err := controllerutil.CreateOrUpdate(ctx, kubeClient, caInitScriptSecret, func() error {
			return controllerutil.SetOwnerReference(vm, caInitScriptSecret, kubeClient.Scheme())
		}); err != nil {
			return nil, status.Error(codes.Internal, fmt.Sprintf("failed to set owner reference on ca init script: %v", err))
		}
	}

	for _, disk := range disks {
		vmd := &vmv1.VirtualMachineDisk{
			ObjectMeta: metav1.ObjectMeta{
				Name:      disk.VirtualMachineDiskRef.Name,
				Namespace: spec.Project,
			},
		}
		if _, err := controllerutil.CreateOrUpdate(ctx, kubeClient, vmd, func() error {
			return controllerutil.SetOwnerReference(vm, vmd, kubeClient.Scheme())
		}); err != nil {
			return nil, status.Error(codes.Internal, fmt.Sprintf("failed to set owner reference on disk %s", vmd.Name))
		}
	}

	timeout := defaultVMWaitTimeout
	if spec.VMRunningTimeoutSeconds != nil {
		timeout = time.Duration(*spec.VMRunningTimeoutSeconds) * time.Second
	}

	if timeout > 0 {
		var (
			lastState   vmv1.VirtualMachineState
			lastReason  vmv1.VirtualMachineStateReason
			lastMessage string
		)
		if err := k8swait.PollUntilContextTimeout(ctx, vmStatusPollInterval, timeout, true, func(ctx context.Context) (bool, error) {
			currentVM := &vmv1.VirtualMachine{}
			if err := kubeClient.Get(ctx, client.ObjectKey{Name: vm.Name, Namespace: vm.Namespace}, currentVM); err != nil {
				if apierrors.IsNotFound(err) {
					return false, nil
				}
				return false, err
			}
			lastState = currentVM.Status.State
			lastReason = currentVM.Status.Reason
			lastMessage = currentVM.Status.Message
			if currentVM.Status.State == vmv1.VirtualMachineStateRunning {
				return true, nil
			}
			if currentVM.Status.State == vmv1.VirtualMachineStateUnschedulable {
				return false, status.Error(codes.ResourceExhausted, fmt.Sprintf("VM %s could not be scheduled because of resource limitations: %s - %s", vm.Name, currentVM.Status.Reason, currentVM.Status.Message))
			}
			switch currentVM.Status.State {
			case vmv1.VirtualMachineStateErrorConfiguration,
				vmv1.VirtualMachineStateDiskError,
				vmv1.VirtualMachineStateCrashLoopBackoff:
				return false, status.Error(codes.Internal, fmt.Sprintf("VM %s is in error state %q: %s - %s", vm.Name, currentVM.Status.State, currentVM.Status.Reason, currentVM.Status.Message))
			}
			return false, nil
		}); err != nil {
			if _, ok := status.FromError(err); ok {
				return nil, err
			}
			if errors.Is(err, context.DeadlineExceeded) {
				return nil, status.Error(codes.DeadlineExceeded, fmt.Sprintf("failed to wait for VM %s to be running: still not running after %s (last state: %q, reason: %q, message: %q)", vm.Name, timeout, lastState, lastReason, lastMessage))
			}
			return nil, status.Error(codes.Internal, fmt.Sprintf("failed to wait for VM %s to be running: %v", vm.Name, err))
		}
	}

	return &driver.CreateMachineResponse{
		ProviderID:     vm.Name,
		NodeName:       req.Machine.Name,
		LastKnownState: fmt.Sprintf("Created %s", vm.Name),
	}, nil
}

func (p *Provider) createMachine(ctx context.Context, c client.Client, machine *v1alpha1.Machine, spec *api.ProviderSpec, disks []vmv1.DiskAttachment, secretName string, caInitScriptSecret *corev1.Secret) (*vmv1.VirtualMachine, error) {
	startupScripts := []vmv1.StartupScript{}
	if caInitScriptSecret != nil {
		startupScripts = append(startupScripts, vmv1.StartupScript{
			Name: "caInit",
			ScriptSecretRef: &corev1.LocalObjectReference{
				Name: caInitScriptSecret.Name,
			},
		})
	}
	startupScripts = append(startupScripts, vmv1.StartupScript{
		Name: "userData",
		ScriptSecretRef: &corev1.LocalObjectReference{
			Name: secretName,
		},
	})

	compute := vmv1.Compute{
		VirtualMachineType: spec.MachineType,
	}
	// Add label to shoot vm, allow egress traffic from vm to outside GDC org
	if spec.Labels == nil {
		spec.Labels = make(map[string]string)
	}
	// EnableEgress is a flag used to enable egress via a Cloud NAT gateway.
	// This logic controls a *legacy* NAT label on the VM, which is the inverse of the new flag.
	//
	// - If EnableEgress is true:
	//   Remove the legacy label, as Cloud NAT is now responsible for egress.
	//
	// - If EnableEgress is false:
	//   Remove the legacy label to disable all egress (both new and legacy).
	//
	// - If EnableEgress is nil (not defined):
	//   Keep the legacy label to enable NAT on the VM, preserving existing behavior.
	if spec.EnableEgress == nil {
		spec.Labels[networkingv1.EnableEgressNATLabelKey] = "true"
	}
	vm := &vmv1.VirtualMachine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      machine.Name,
			Namespace: spec.Project,
			Labels:    spec.Labels,
		},
		Spec: vmv1.VirtualMachineSpec{
			Disks:          disks,
			Compute:        compute,
			StartupScripts: startupScripts,
			GuestEnvironment: &vmv1.GuestEnvironment{
				AccessManagement: &vmv1.AccessManagementConfig{
					Enable: spec.AccessEnabled,
				},
			},
		},
	}

	vm.Spec.Network = &vmv1.NetworkSpec{
		Interfaces: []vmv1.NetworkInterfaceSpec{
			{
				Network: GDCNetworkName,
				Subnet:  spec.SubnetName,
			},
		},
	}

	if err := c.Create(ctx, vm); err != nil && !apierrors.IsAlreadyExists(err) {
		return nil, fmt.Errorf("failed to create VirtualMachine for %q in namespace: %q, %w", machine.Name, spec.Project, err)
	}
	currentVM := &vmv1.VirtualMachine{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: spec.Project, Name: machine.Name}, currentVM); err != nil {
		return nil, fmt.Errorf("failed to get VirtualMachine for %q in namespace: %q, %w", machine.Name, spec.Project, err)
	}
	return currentVM, nil
}

func (p *Provider) createDisk(ctx context.Context, c client.Client, machine *v1alpha1.Machine, spec *api.ProviderSpec, diskSpec *api.Disk) (*vmv1.DiskAttachment, error) {
	diskName := bootDiskName(machine.Name)
	if !diskSpec.Boot {
		diskName = fmt.Sprintf("%s-disk", machine.Name)
	}
	diskSize, err := resource.ParseQuantity(fmt.Sprintf("%dGi", diskSpec.SizeGB))
	if err != nil {
		return nil, fmt.Errorf("invalid disk size, %w", err)
	}
	disk := &vmv1.VirtualMachineDisk{
		ObjectMeta: metav1.ObjectMeta{
			Name:      diskName,
			Namespace: spec.Project,
			Labels:    diskSpec.Labels,
		},
		Spec: vmv1.VirtualMachineDiskSpec{
			Source: &vmv1.DiskSource{
				Image: &vmv1.ImageDiskSource{
					Name:      diskSpec.Image,
					Namespace: diskSpec.Project,
				},
			},
			Size: diskSize,
			Type: vmv1.DiskType(diskSpec.Type),
		},
	}
	if err := c.Create(ctx, disk); err != nil && !apierrors.IsAlreadyExists(err) {
		return nil, fmt.Errorf("failed to create VirtualMachineDisk for %q in namespace: %q, %w", diskName, spec.Project, err)
	}
	return &vmv1.DiskAttachment{
		Boot:       ptr.To(diskSpec.Boot),
		AutoDelete: ptr.To(diskSpec.AutoDelete),
		VirtualMachineDiskRef: corev1.LocalObjectReference{
			Name: diskName,
		},
	}, nil
}

func (p *Provider) createSecret(ctx context.Context, c client.Client, machine *v1alpha1.Machine, spec *api.ProviderSpec, secret *corev1.Secret) (*corev1.Secret, error) {
	s := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      userDataSecretName(machine.Name),
			Namespace: spec.Project,
		}, Data: map[string][]byte{
			"script": secret.Data["userData"],
		},
	}
	if err := c.Create(ctx, s); err != nil && !apierrors.IsAlreadyExists(err) {
		return nil, fmt.Errorf("failed to create init script for %q in namespace: %q, %w", machine.Name, spec.Project, err)
	}
	return s, nil
}

func (p *Provider) createCAInitScriptSecret(ctx context.Context, c client.Client, machine *v1alpha1.Machine, spec *api.ProviderSpec) (*corev1.Secret, error) {
	script := `#!/bin/bash
export REGISTRY=%q
export CADATA=%q
mkdir -p /etc/containerd/certs.d/${REGISTRY}
echo ${CADATA} | openssl base64 -A -d > /etc/containerd/certs.d/${REGISTRY}/ca.crt`

	s := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      caInitScriptSecretName(machine.Name),
			Namespace: spec.Project,
		},
	}
	s.Data = map[string][]byte{
		"script": []byte(fmt.Sprintf(script, spec.RegistryURL, spec.CAData)),
	}
	if err := c.Create(ctx, s); err != nil && !apierrors.IsAlreadyExists(err) {
		return nil, fmt.Errorf("failed to create CA setup secret for %q in namespace: %q, %w", machine.Name, spec.Project, err)
	}

	return s, nil
}

// DeleteMachine handles a machine deletion request
//
// REQUEST PARAMETERS (driver.DeleteMachineRequest)
// Machine               *v1alpha1.Machine        Machine object from whom VM is to be deleted
// MachineClass          *v1alpha1.MachineClass   MachineClass backing the machine object
// Secret                *corev1.Secret           Kubernetes secret that contains any sensitive data/credentials
//
// RESPONSE PARAMETERS (driver.DeleteMachineResponse)
// LastKnownState        bytes(blob)              (Optional) Last known state of VM during the current operation.
//
//	Could be helpful to continue operations in future requests.
func (p *Provider) DeleteMachine(ctx context.Context, req *driver.DeleteMachineRequest) (*driver.DeleteMachineResponse, error) {
	// Log messages to track delete request
	klog.V(2).Infof("Machine deletion request has been received for %q", req.Machine.Name)
	defer klog.V(2).Infof("Machine deletion request has been processed for %q", req.Machine.Name)

	spec := &api.ProviderSpec{}
	err := json.Unmarshal(req.MachineClass.ProviderSpec.Raw, spec)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "failed to parse MachineClass")
	}

	kubeClient, err := p.SPI.Client(req.Secret, spec)
	if err != nil {
		return nil, wrapError(err)
	}
	vm := &vmv1.VirtualMachine{}
	err = kubeClient.Get(ctx, types.NamespacedName{
		Namespace: spec.Project,
		Name:      req.Machine.Name,
	}, vm)
	if err != nil {
		// when gvm is not found, mcm will clean up
		// all vm resources.
		if apierrors.IsNotFound(err) {
			if err := kubeClient.Delete(ctx, &vmv1.VirtualMachineDisk{
				ObjectMeta: metav1.ObjectMeta{
					Name:      bootDiskName(req.Machine.Name),
					Namespace: spec.Project,
				},
			}); client.IgnoreNotFound(err) != nil {
				return nil, wrapError(fmt.Errorf("failed to delete boot disk %q in namespace %q, %w", req.Machine.Name, spec.Project, err))
			}

			if err := kubeClient.Delete(ctx, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      caInitScriptSecretName(req.Machine.Name),
					Namespace: spec.Project,
				},
			}); client.IgnoreNotFound(err) != nil {
				return nil, wrapError(fmt.Errorf("failed to delete cainit script %q in namespace %q, %w", req.Machine.Name, spec.Project, err))
			}

			if err := kubeClient.Delete(ctx, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      userDataSecretName(req.Machine.Name),
					Namespace: spec.Project,
				},
			}); client.IgnoreNotFound(err) != nil {
				return nil, wrapError(fmt.Errorf("failed to delete userdata script %q in namespace %q, %w", req.Machine.Name, spec.Project, err))
			}

			return &driver.DeleteMachineResponse{}, nil
		}
		return nil, wrapError(err)
	}

	if err := kubeClient.Delete(ctx, vm); client.IgnoreNotFound(err) != nil {
		return nil, wrapError(fmt.Errorf("failed to delete vm %q in namespace %q, %w", req.Machine.Name, spec.Project, err))
	}

	return &driver.DeleteMachineResponse{}, nil
}

// GetMachineStatus handles a machine get status request
// OPTIONAL METHOD
//
// REQUEST PARAMETERS (driver.GetMachineStatusRequest)
// Machine               *v1alpha1.Machine        Machine object from whom VM status needs to be returned
// MachineClass          *v1alpha1.MachineClass   MachineClass backing the machine object
// Secret                *corev1.Secret           Kubernetes secret that contains any sensitive data/credentials
//
// RESPONSE PARAMETERS (driver.GetMachineStatusResponse)
// ProviderID            string                   Unique identification of the VM at the cloud provider. This could be the same/different from req.MachineName.
//
//	ProviderID typically matches with the node.Spec.ProviderID on the node object.
//	Eg: gce://project-name/region/vm-ProviderID
//
// NodeName             string                    Returns the name of the node-object that the VM registers with Kubernetes.
//
//	This could be different from req.MachineName as well
//
// The request should return a NOT_FOUND (5) status error code if the machine is not existing
func (p *Provider) GetMachineStatus(ctx context.Context, req *driver.GetMachineStatusRequest) (*driver.GetMachineStatusResponse, error) {
	// Log messages to track start and end of request
	klog.V(2).Infof("Get request has been received for %q", req.Machine.Name)
	defer klog.V(2).Infof("Machine get request has been processed successfully for %q", req.Machine.Name)

	return &driver.GetMachineStatusResponse{}, status.Error(codes.Unimplemented, "")
}

// ListMachines lists all the machines possibly created by a providerSpec
// Identifying machines created by a given providerSpec depends on the OPTIONAL IMPLEMENTATION LOGIC
// you have used to identify machines created by a providerSpec. It could be tags/resource-groups etc
// OPTIONAL METHOD
//
// REQUEST PARAMETERS (driver.ListMachinesRequest)
// MachineClass          *v1alpha1.MachineClass   MachineClass based on which VMs created have to be listed
// Secret                *corev1.Secret           Kubernetes secret that contains any sensitive data/credentials
//
// RESPONSE PARAMETERS (driver.ListMachinesResponse)
// MachineList           map<string,string>  A map containing the keys as the MachineID and value as the MachineName
//
//	for all machines that were possibly created by this ProviderSpec
func (p *Provider) ListMachines(ctx context.Context, req *driver.ListMachinesRequest) (*driver.ListMachinesResponse, error) {
	// Log messages to track start and end of request
	klog.V(2).Infof("List machines request has been received for %q", req.MachineClass.Name)
	defer klog.V(2).Infof("List machines request has been received for %q", req.MachineClass.Name)

	spec := &api.ProviderSpec{}
	if err := json.Unmarshal(req.MachineClass.ProviderSpec.Raw, spec); err != nil {
		errMsg := "failed to parse MachineClass " + req.MachineClass.Name
		return nil, status.Error(codes.InvalidArgument, errMsg)
	}

	vmList := &vmv1.VirtualMachineList{}
	vmLabels := spec.Labels
	if vmLabels == nil {
		vmLabels = map[string]string{}
	}

	kubeClient, err := p.SPI.Client(req.Secret, spec)
	if err != nil {
		return nil, wrapError(err)
	}

	// List machines filter by namespace and labels
	if err := kubeClient.List(ctx, vmList, client.InNamespace(spec.Project), client.MatchingLabels(vmLabels)); err != nil {
		return nil, wrapError(fmt.Errorf("failed to list VMs in namespace %q: %w", spec.Project, err))
	}
	machineList := make(map[string]string)
	for _, vm := range vmList.Items {
		machineList[vm.Name] = vm.Name // ProviderID: vm.Name, MachineName: vm.Name
	}

	return &driver.ListMachinesResponse{
		MachineList: machineList,
	}, nil
}

// GetVolumeIDs returns a list of Volume IDs for all PV Specs for whom a provider volume was found
//
// REQUEST PARAMETERS (driver.GetVolumeIDsRequest)
// PVSpecList            []*corev1.PersistentVolumeSpec       PVSpecsList is a list PV specs for whom volume-IDs are required.
//
// RESPONSE PARAMETERS (driver.GetVolumeIDsResponse)
// VolumeIDs             []string                             VolumeIDs is a repeated list of VolumeIDs.
func (p *Provider) GetVolumeIDs(ctx context.Context, req *driver.GetVolumeIDsRequest) (*driver.GetVolumeIDsResponse, error) {
	// Log messages to track start and end of request
	klog.V(2).Infof("GetVolumeIDs request has been received for %q", req.PVSpecs)
	defer klog.V(2).Infof("GetVolumeIDs request has been processed successfully for %q", req.PVSpecs)

	return &driver.GetVolumeIDsResponse{}, status.Error(codes.Unimplemented, "")
}

// GenerateMachineClassForMigration helps in migration of one kind of machineClass CR to another kind.
// For instance a machineClass custom resource of `AWSMachineClass` to `MachineClass`.
// Implement this functionality only if something like this is desired in your setup.
// If you don't require this functionality leave it as is. (return Unimplemented)
//
// The following are the tasks typically expected out of this method
// 1. Validate if the incoming classSpec is valid one for migration (e.g. has the right kind).
// 2. Migrate/Copy over all the fields/spec from req.ProviderSpecificMachineClass to req.MachineClass
// For an example refer
//
//	https://github.com/prashanth26/machine-controller-manager-provider-gcp/blob/migration/pkg/gcp/machine_controller.go#L222-L233
//
// REQUEST PARAMETERS (driver.GenerateMachineClassForMigration)
// ProviderSpecificMachineClass    interface{}                             ProviderSpecificMachineClass is provider specific machine class object (E.g. AWSMachineClass). Typecasting is required here.
// MachineClass 				   *v1alpha1.MachineClass                  MachineClass is the machine class object that is to be filled up by this method.
// ClassSpec                       *v1alpha1.ClassSpec                     Some more classSpec details useful while migration.
//
// RESPONSE PARAMETERS (driver.GenerateMachineClassForMigration)
// NONE
func (p *Provider) GenerateMachineClassForMigration(ctx context.Context, req *driver.GenerateMachineClassForMigrationRequest) (*driver.GenerateMachineClassForMigrationResponse, error) {
	// Log messages to track start and end of request
	klog.V(2).Infof("MigrateMachineClass request has been received for %q", req.ClassSpec)
	defer klog.V(2).Infof("MigrateMachineClass request has been processed successfully for %q", req.ClassSpec)

	return &driver.GenerateMachineClassForMigrationResponse{}, status.Error(codes.Unimplemented, "")
}

// wrapError translates k8s errors to machine error codes that can be understood by machine-controller-manager
func wrapError(err error) error {
	switch {
	case apierrors.IsInvalid(err):
		return status.Error(codes.InvalidArgument, err.Error())
	case apierrors.IsAlreadyExists(err):
		return status.Error(codes.AlreadyExists, err.Error())
	case apierrors.IsNotFound(err):
		return status.Error(codes.NotFound, err.Error())
	case apierrors.IsForbidden(err):
		return status.Error(codes.PermissionDenied, err.Error())
	case apierrors.IsUnauthorized(err):
		return status.Error(codes.Unauthenticated, err.Error())
	default:
		return status.Error(codes.Internal, err.Error())
	}
}

func bootDiskName(machineName string) string {
	return machineName + "-boot-disk"
}

func caInitScriptSecretName(machineName string) string {
	return machineName + "-cainit-script"
}

func userDataSecretName(machineName string) string {
	return machineName + "-init-script"
}
