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

package api

// ProviderSpec contains the specifications for the new Virtual Machine
type ProviderSpec struct {
	// Name is the identification of the Virtual Machine
	Name string `json:"name"`

	// Project is the namespace which the Virtual Machine belongs to
	Project string `json:"project"`

	// Labels to apply to the VM
	Labels map[string]string `json:"labels,omitempty"`

	// Annotations to apply to the VM
	Annotations map[string]string `json:"annotations,omitempty"`

	// MachineType is the type of the virtual machine
	MachineType string `json:"machineType"`

	// AccessEnabled specifies whether to enable the AccessManagement feature in the VM's guest environment.
	// https://cloud.google.com/distributed-cloud/hosted/docs/latest/gdch/apis/service/virtualmachine/v1/virtualmachine-v1#guestenvironment
	AccessEnabled bool `json:"accessEnabled"`

	// Disks specifies the disk attachments to the VM.
	Disks []*Disk `json:"disks"`

	// SubnetName is the subnet assigned to the created VMs.
	SubnetName string `json:"subnetName"`

	// RegistryURL is harbor registry endpoint that stores images
	RegistryURL string `json:"registryURL"`

	// CAData is Base64 encoded certificateAuthorityData of org admin
	CAData string `json:"caData"`

	// OrgClusterURL is the API Server URL for GDC.
	OrgClusterURL string `json:"orgClusterURL"`

	// EnableEgress controls whether to enable egress for shoot VMs using CloudNAT.
	EnableEgress *bool `json:"enableEgress,omitempty"`

	// VMRunningTimeoutSeconds specifies the wait timeout for the VM to be running in seconds.
	// If not defined, it defaults to 600 seconds. If set to 0, no wait.
	VMRunningTimeoutSeconds *int `json:"vmRunningTimeoutSeconds,omitempty"`
}

// Disks contains the specification for a new disk
type Disk struct {
	// Image specifies the image that VM should use to boot.
	// Empty image indicates a blank disk.
	Image string `json:"image,omitempty"`

	// Project specifies the namespace which the disk image comes from.
	// This is only required when `image` field is not empty.
	Project string `json:"project"`

	// Labels to apply to the disk
	Labels map[string]string `json:"labels"`

	// Boot specifies whether this is a bootable disk
	Boot bool `json:"boot"`

	// SizeGB specifies the amount of disk size in GB for the disk
	SizeGB int `json:"sizeGb"`

	// AutoDelete to apply to the disk
	AutoDelete bool `json:"autoDelete"`

	// Type specifies the disk type for VM.
	Type string `json:"type,omitempty"`
}
