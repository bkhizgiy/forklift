package ovirt

import (
	"fmt"
	"path"
	"strings"

	"github.com/konveyor/forklift-controller/pkg/apis/forklift/v1beta1"
	api "github.com/konveyor/forklift-controller/pkg/apis/forklift/v1beta1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/konveyor/forklift-controller/pkg/apis/forklift/v1beta1/plan"
	"github.com/konveyor/forklift-controller/pkg/apis/forklift/v1beta1/ref"
	planbase "github.com/konveyor/forklift-controller/pkg/controller/plan/adapter/base"
	plancontext "github.com/konveyor/forklift-controller/pkg/controller/plan/context"
	"github.com/konveyor/forklift-controller/pkg/controller/provider/web/base"
	"github.com/konveyor/forklift-controller/pkg/controller/provider/web/ocp"
	model "github.com/konveyor/forklift-controller/pkg/controller/provider/web/ovirt"
	liberr "github.com/konveyor/forklift-controller/pkg/lib/error"
	libitr "github.com/konveyor/forklift-controller/pkg/lib/itinerary"
	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	cnv "kubevirt.io/api/core/v1"
	cdi "kubevirt.io/containerized-data-importer-api/pkg/apis/core/v1beta1"
)

// BIOS types
const (
	ClusterDefault = "cluster_default"
	Q35Ovmf        = "q35_ovmf"
	Q35SecureBoot  = "q35_secure_boot"
)

// Bus types
const (
	VirtioScsi = "virtio_scsi"
	Virtio     = "virtio"
	Sata       = "sata"
	Scsi       = "scsi"
	IDE        = "ide"
)

// Input types
const (
	Tablet = "tablet"
)

// Network types
const (
	Pod    = "pod"
	Multus = "multus"
)

// Template labels
const (
	TemplateOSLabel       = "os.template.kubevirt.io/%s"
	TemplateWorkloadLabel = "workload.template.kubevirt.io/server"
	TemplateFlavorLabel   = "flavor.template.kubevirt.io/medium"
)

// Operating Systems
const (
	DefaultWindows = "win10"
	DefaultLinux   = "rhel8.1"
	Unknown        = "unknown"
)

// Annotations
const (
	// CDI import disk ID annotation on PVC
	AnnImportDiskId = "cdi.kubevirt.io/storage.import.diskId"
)

// Map of ovirt guest ids to osinfo ids.
var osMap = map[string]string{
	"rhel_6_10_plus_ppc64": "rhel6.10",
	"rhel_6_ppc64":         "rhel6.10",
	"rhel_6":               "rhel6.10",
	"rhel_6x64":            "rhel6.10",
	"rhel_6_9_plus_ppc64":  "rhel6.9",
	"rhel_7_ppc64":         "rhel7.7",
	"rhel_7_s390x":         "rhel7.7",
	"rhel_7x64":            "rhel7.7",
	"rhel_8x64":            "rhel8.1",
	"sles_11_ppc64":        "opensuse15.0",
	"sles_11":              "opensuse15.0",
	"sles_12_s390x":        "opensuse15.0",
	"ubuntu_12_04":         "ubuntu18.04",
	"ubuntu_12_10":         "ubuntu18.04",
	"ubuntu_13_04":         "ubuntu18.04",
	"ubuntu_13_10":         "ubuntu18.04",
	"ubuntu_14_04_ppc64":   "ubuntu18.04",
	"ubuntu_14_04":         "ubuntu18.04",
	"ubuntu_16_04_s390x":   "ubuntu18.04",
	"windows_10":           "win10",
	"windows_10x64":        "win10",
	"windows_2003":         "win10",
	"windows_2003x64":      "win10",
	"windows_2008R2x64":    "win2k8",
	"windows_2008":         "win2k8",
	"windows_2008x64":      "win2k8",
	"windows_2012R2x64":    "win2k12r2",
	"windows_2012x64":      "win2k12r2",
	"windows_2016x64":      "win2k16",
	"windows_2019x64":      "win2k19",
	"windows_7":            "win10",
	"windows_7x64":         "win10",
	"windows_8":            "win10",
	"windows_8x64":         "win10",
	"windows_xp":           "win10",
}

// oVirt builder.
type Builder struct {
	*plancontext.Context
	// MAC addresses already in use on the destination cluster. k=mac, v=vmName
	macConflictsMap map[string]string
}

// Get list of destination VMs with mac addresses that would
// conflict with this VM, if any exist.
func (r *Builder) macConflicts(vm *model.Workload) (conflictingVMs []string, err error) {
	if r.macConflictsMap == nil {
		list := []ocp.VM{}
		err = r.Destination.Inventory.List(&list, base.Param{
			Key:   base.DetailParam,
			Value: "all",
		})
		if err != nil {
			return
		}

		r.macConflictsMap = make(map[string]string)
		for _, kVM := range list {
			for _, iface := range kVM.Object.Spec.Template.Spec.Domain.Devices.Interfaces {
				r.macConflictsMap[iface.MacAddress] = path.Join(kVM.Namespace, kVM.Name)
			}
		}
	}

	for _, nic := range vm.NICs {
		if conflictingVm, found := r.macConflictsMap[nic.MAC]; found {
			for i := range conflictingVMs {
				// ignore duplicates
				if conflictingVMs[i] == conflictingVm {
					continue
				}
			}
			conflictingVMs = append(conflictingVMs, conflictingVm)
		}
	}

	return
}

// Create DataVolume certificate configmap.
func (r *Builder) ConfigMap(_ ref.Ref, in *core.Secret, object *core.ConfigMap) (err error) {
	object.BinaryData["ca.pem"] = in.Data["cacert"]
	return
}

func (r *Builder) PodEnvironment(_ ref.Ref, _ *core.Secret) (env []core.EnvVar, err error) {
	return
}

// Build the DataVolume credential secret.
func (r *Builder) Secret(_ ref.Ref, in, object *core.Secret) (err error) {
	object.StringData = map[string]string{
		"accessKeyId": string(in.Data["user"]),
		"secretKey":   string(in.Data["password"]),
	}
	return
}

// Create DataVolume specs for the VM.
func (r *Builder) DataVolumes(vmRef ref.Ref, secret *core.Secret, configMap *core.ConfigMap, dvTemplate *cdi.DataVolume) (dvs []cdi.DataVolume, err error) {
	vm := &model.Workload{}
	err = r.Source.Inventory.Find(vm, vmRef)
	if err != nil {
		err = liberr.Wrap(
			err,
			"VM lookup failed.",
			"vm",
			vmRef.String())
		return
	}
	url := r.Source.Provider.Spec.URL

	dsMapIn := r.Context.Map.Storage.Spec.Map
	for i := range dsMapIn {
		mapped := &dsMapIn[i]
		ref := mapped.Source
		sd := &model.StorageDomain{}
		fErr := r.Source.Inventory.Find(sd, ref)
		if fErr != nil {
			err = fErr
			return
		}
		for _, da := range vm.DiskAttachments {
			if da.Disk.StorageType == "image" && da.Disk.StorageDomain == sd.ID {
				storageClass := mapped.Destination.StorageClass
				size := da.Disk.ProvisionedSize
				if da.Disk.ActualSize > size {
					size = da.Disk.ActualSize
				}
				dvSpec := cdi.DataVolumeSpec{
					Source: &cdi.DataVolumeSource{
						Imageio: &cdi.DataVolumeSourceImageIO{
							URL:           url,
							DiskID:        da.Disk.ID,
							SecretRef:     secret.Name,
							CertConfigMap: configMap.Name,
						},
					},
					Storage: &cdi.StorageSpec{
						Resources: core.ResourceRequirements{
							Requests: core.ResourceList{
								core.ResourceStorage: *resource.NewQuantity(size, resource.BinarySI),
							},
						},
						StorageClassName: &storageClass,
					},
				}
				// set the access mode and volume mode if they were specified in the storage map.
				// otherwise, let the storage profile decide the default values.
				if mapped.Destination.AccessMode != "" {
					dvSpec.Storage.AccessModes = []core.PersistentVolumeAccessMode{mapped.Destination.AccessMode}
				}
				if mapped.Destination.VolumeMode != "" {
					dvSpec.Storage.VolumeMode = &mapped.Destination.VolumeMode
				}

				dv := dvTemplate.DeepCopy()
				dv.Spec = dvSpec
				if dv.ObjectMeta.Annotations == nil {
					dv.ObjectMeta.Annotations = make(map[string]string)
				}
				dv.ObjectMeta.Annotations[planbase.AnnDiskSource] = da.Disk.ID
				dvs = append(dvs, *dv)
			}
		}
	}

	return
}

// Create the destination Kubevirt VM.
func (r *Builder) VirtualMachine(vmRef ref.Ref, object *cnv.VirtualMachineSpec, persistentVolumeClaims []core.PersistentVolumeClaim) (err error) {
	vm := &model.Workload{}
	err = r.Source.Inventory.Find(vm, vmRef)
	if err != nil {
		err = liberr.Wrap(
			err,
			"VM lookup failed.",
			"vm",
			vmRef.String())
		return
	}

	var conflicts []string
	conflicts, err = r.macConflicts(vm)
	if err != nil {
		return
	}
	if len(conflicts) > 0 {
		err = liberr.New(
			fmt.Sprintf("Source VM has a mac address conflict with one or more destination VMs: %s", conflicts))
		return
	}

	if object.Template == nil {
		object.Template = &cnv.VirtualMachineInstanceTemplateSpec{}
	}
	r.mapDisks(vm, persistentVolumeClaims, object)
	r.mapFirmware(vm, &vm.Cluster, object)
	r.mapCPU(vm, object)
	r.mapMemory(vm, object)
	r.mapClock(vm, object)
	r.mapInput(object)
	err = r.mapNetworks(vm, object)
	if err != nil {
		return
	}

	return
}

func (r *Builder) mapNetworks(vm *model.Workload, object *cnv.VirtualMachineSpec) (err error) {
	var kNetworks []cnv.Network
	var kInterfaces []cnv.Interface

	numNetworks := 0
	netMapIn := r.Context.Map.Network.Spec.Map
	for i := range netMapIn {
		mapped := &netMapIn[i]
		ref := mapped.Source
		network := &model.Network{}
		fErr := r.Source.Inventory.Find(network, ref)
		if fErr != nil {
			err = fErr
			return
		}
		needed := []model.XNIC{}
		for _, nic := range vm.NICs {
			if nic.Profile.Network == network.ID {
				needed = append(needed, nic)
			}
		}
		if len(needed) == 0 {
			continue
		}
		for _, nic := range needed {
			networkName := fmt.Sprintf("net-%v", numNetworks)
			numNetworks++
			kNetwork := cnv.Network{
				Name: networkName,
			}
			kInterface := cnv.Interface{
				Name:       networkName,
				Model:      nic.Interface,
				MacAddress: nic.MAC,
			}
			switch mapped.Destination.Type {
			case Pod:
				kNetwork.Pod = &cnv.PodNetwork{}
				kInterface.Masquerade = &cnv.InterfaceMasquerade{}
			case Multus:
				kNetwork.Multus = &cnv.MultusNetwork{
					NetworkName: path.Join(mapped.Destination.Namespace, mapped.Destination.Name),
				}
				if nic.Profile.PassThrough {
					kInterface.SRIOV = &cnv.InterfaceSRIOV{}
				} else {
					kInterface.Bridge = &cnv.InterfaceBridge{}
				}
			}
			kNetworks = append(kNetworks, kNetwork)
			kInterfaces = append(kInterfaces, kInterface)
		}
	}
	object.Template.Spec.Networks = kNetworks
	object.Template.Spec.Domain.Devices.Interfaces = kInterfaces
	return
}

func (r *Builder) mapInput(object *cnv.VirtualMachineSpec) {
	tablet := cnv.Input{
		Type: Tablet,
		Name: Tablet,
		Bus:  Virtio,
	}
	object.Template.Spec.Domain.Devices.Inputs = []cnv.Input{tablet}
}

func (r *Builder) mapClock(vm *model.Workload, object *cnv.VirtualMachineSpec) {
	clock := cnv.Clock{
		Timer: &cnv.Timer{},
	}
	timezone := cnv.ClockOffsetTimezone(vm.Timezone)
	clock.Timezone = &timezone
	object.Template.Spec.Domain.Clock = &clock
}

func (r *Builder) mapMemory(vm *model.Workload, object *cnv.VirtualMachineSpec) {
	reservation := resource.NewQuantity(vm.Memory, resource.BinarySI)
	object.Template.Spec.Domain.Resources = cnv.ResourceRequirements{
		Requests: map[core.ResourceName]resource.Quantity{
			core.ResourceMemory: *reservation,
		},
	}
}
func (r *Builder) mapCPU(vm *model.Workload, object *cnv.VirtualMachineSpec) {
	object.Template.Spec.Domain.Machine = &cnv.Machine{Type: "q35"}
	object.Template.Spec.Domain.CPU = &cnv.CPU{
		Sockets: uint32(vm.CpuSockets),
		Cores:   uint32(vm.CpuCores),
		Threads: uint32(vm.CpuThreads),
	}
	if vm.CpuPinningPolicy == model.Dedicated {
		object.Template.Spec.Domain.CPU.DedicatedCPUPlacement = true
	}

}

func (r *Builder) mapFirmware(vm *model.Workload, cluster *model.Cluster, object *cnv.VirtualMachineSpec) {
	biosType := vm.BIOS
	if biosType == ClusterDefault {
		biosType = cluster.BiosType
	}
	serial := vm.SerialNumber
	if serial == "" {
		serial = vm.ID
	}
	features := &cnv.Features{}
	firmware := &cnv.Firmware{
		Serial: serial,
		UUID:   types.UID(vm.ID),
	}
	switch biosType {
	case Q35Ovmf, Q35SecureBoot:
		// We disable secure boot even if it was enabled on the source because the guest OS won't
		// be able to boot without getting the NVRAM data. So we start the VM without secure boot
		// to ease the procedure users need to do in order to make the guest OS to boot.
		secureBootEnabled := false
		firmware.Bootloader = &cnv.Bootloader{
			EFI: &cnv.EFI{
				SecureBoot: &secureBootEnabled,
			}}
	default:
		firmware.Bootloader = &cnv.Bootloader{BIOS: &cnv.BIOS{}}
	}
	object.Template.Spec.Domain.Features = features
	object.Template.Spec.Domain.Firmware = firmware
}

func (r *Builder) mapDisks(vm *model.Workload, persistentVolumeClaims []core.PersistentVolumeClaim, object *cnv.VirtualMachineSpec) {
	var kVolumes []cnv.Volume
	var kDisks []cnv.Disk

	pvcMap := make(map[string]*core.PersistentVolumeClaim)
	for i := range persistentVolumeClaims {
		pvc := &persistentVolumeClaims[i]
		pvcMap[r.ResolvePersistentVolumeClaimIdentifier(pvc)] = pvc
	}

	for _, da := range vm.DiskAttachments {
		claimName := pvcMap[da.Disk.ID].Name
		volumeName := da.Disk.ID
		var bus string
		switch da.Interface {
		case VirtioScsi:
			bus = Scsi
		case Sata, IDE:
			bus = Sata
		default:
			bus = Virtio
		}
		var disk cnv.Disk
		if da.Disk.Disk.StorageType == "lun" {
			claimName = volumeName
			disk = cnv.Disk{
				Name: volumeName,
				DiskDevice: cnv.DiskDevice{
					LUN: &cnv.LunTarget{
						Bus: cnv.DiskBus(bus),
					},
				},
			}
		} else {
			disk = cnv.Disk{
				Name: volumeName,
				DiskDevice: cnv.DiskDevice{
					Disk: &cnv.DiskTarget{
						Bus: cnv.DiskBus(bus),
					},
				},
			}
		}
		volume := cnv.Volume{
			Name: volumeName,
			VolumeSource: cnv.VolumeSource{
				PersistentVolumeClaim: &cnv.PersistentVolumeClaimVolumeSource{
					PersistentVolumeClaimVolumeSource: core.PersistentVolumeClaimVolumeSource{
						ClaimName: claimName,
					},
				},
			},
		}
		if da.DiskAttachment.Bootable {
			var bootOrder uint = 1
			disk.BootOrder = &bootOrder
		}

		kVolumes = append(kVolumes, volume)
		kDisks = append(kDisks, disk)
	}
	object.Template.Spec.Volumes = kVolumes
	object.Template.Spec.Domain.Devices.Disks = kDisks
}

// Build tasks.
func (r *Builder) Tasks(vmRef ref.Ref) (list []*plan.Task, err error) {
	vm := &model.Workload{}
	err = r.Source.Inventory.Find(vm, vmRef)
	if err != nil {
		err = liberr.Wrap(
			err,
			"VM lookup failed.",
			"vm",
			vmRef.String())
	}
	for _, da := range vm.DiskAttachments {
		// We don't add a task for LUNs because we don't copy their content but rather assume we can connect to
		// the LUNs that are used in the source environment also from the target environment.
		if da.Disk.StorageType != "lun" {
			mB := da.Disk.ProvisionedSize / 0x100000
			list = append(
				list,
				&plan.Task{
					Name: da.Disk.ID,
					Progress: libitr.Progress{
						Total: mB,
					},
					Annotations: map[string]string{
						"unit": "MB",
					},
				})
		}
	}

	return
}

func (r *Builder) TemplateLabels(vmRef ref.Ref) (labels map[string]string, err error) {
	vm := &model.Workload{}
	err = r.Source.Inventory.Find(vm, vmRef)
	if err != nil {
		err = liberr.Wrap(
			err,
			"VM lookup failed.",
			"vm",
			vmRef.String())
		return
	}

	os, ok := osMap[vm.OSType]
	if !ok {
		if strings.Contains(vm.OSType, "linux") || strings.Contains(vm.OSType, "rhel") {
			os = DefaultLinux
		} else if strings.Contains(vm.OSType, "win") {
			os = DefaultWindows
		} else {
			os = Unknown
		}
	}

	labels = make(map[string]string)
	labels[fmt.Sprintf(TemplateOSLabel, os)] = "true"
	labels[TemplateWorkloadLabel] = "true"
	labels[TemplateFlavorLabel] = "true"

	return
}

// Return a stable identifier for a DataVolume.
func (r *Builder) ResolveDataVolumeIdentifier(dv *cdi.DataVolume) string {
	return dv.ObjectMeta.Annotations[planbase.AnnDiskSource]
}

// Return a stable identifier for a PersistentDataVolume.
func (r *Builder) ResolvePersistentVolumeClaimIdentifier(pvc *core.PersistentVolumeClaim) string {
	return pvc.Annotations[AnnImportDiskId]
}

// Build a PersistentVolumeClaim with DataSourceRef for VolumePopulator
func (r *Builder) PersistentVolumeClaimWithSourceRef(da interface{}, storageName *string, populatorName string,
	accessModes []core.PersistentVolumeAccessMode, volumeMode *core.PersistentVolumeMode) *core.PersistentVolumeClaim {
	diskAttachment := da.(model.XDiskAttachment)

	// We add 10% overhead because of the fsOverhead in CDI, around 5% to ext4 and 5% for root partition.
	diskSize := diskAttachment.Disk.ProvisionedSize
	// Accounting for fsOverhead is only required for `volumeMode: Filesystem`, as we may not have enough space
	// after creating a filesystem on an underlying block device
	if *volumeMode == core.PersistentVolumeFilesystem {
		diskSize = int64(float64(diskSize) * 1.1)
	}

	return &core.PersistentVolumeClaim{
		ObjectMeta: meta.ObjectMeta{
			Name:      diskAttachment.DiskAttachment.ID,
			Namespace: r.Plan.Spec.TargetNamespace,
			Annotations: map[string]string{
				AnnImportDiskId: diskAttachment.Disk.ID,
			},
			Labels: map[string]string{"migration": r.Migration.Name},
		},
		Spec: core.PersistentVolumeClaimSpec{
			AccessModes: accessModes,
			Resources: core.ResourceRequirements{
				Requests: map[core.ResourceName]resource.Quantity{
					core.ResourceStorage: *resource.NewQuantity(diskSize, resource.BinarySI)},
			},
			StorageClassName: storageName,
			VolumeMode:       volumeMode,
			DataSourceRef: &core.TypedLocalObjectReference{
				APIGroup: &api.SchemeGroupVersion.Group,
				Kind:     v1beta1.OvirtVolumePopulatorKind,
				Name:     populatorName,
			},
		},
	}
}

func (r *Builder) PreTransferActions(c planbase.Client, vmRef ref.Ref) (ready bool, err error) {
	return true, nil
}

// Create PVs specs for the VM LUNs.
func (r *Builder) LunPersistentVolumes(vmRef ref.Ref) (pvs []core.PersistentVolume, err error) {
	vm := &model.Workload{}
	err = r.Source.Inventory.Find(vm, vmRef)
	if err != nil {
		err = liberr.Wrap(
			err,
			"VM lookup failed.",
			"vm",
			vmRef.String())
		return
	}
	for _, da := range vm.DiskAttachments {
		if da.Disk.StorageType == "lun" {
			volMode := core.PersistentVolumeBlock
			logicalUnit := da.Disk.Lun.LogicalUnits.LogicalUnit[0]

			pvSpec := core.PersistentVolume{
				ObjectMeta: meta.ObjectMeta{
					Name:      da.Disk.ID,
					Namespace: r.Plan.Spec.TargetNamespace,
					Annotations: map[string]string{
						AnnImportDiskId: da.Disk.ID,
						"vmID":          vm.ID,
						"plan":          string(r.Plan.UID),
						"lun":           "true",
					},
					Labels: map[string]string{
						"volume": fmt.Sprintf("%v-%v", vm.Name, da.ID),
					},
				},
				Spec: core.PersistentVolumeSpec{
					PersistentVolumeSource: core.PersistentVolumeSource{
						ISCSI: &core.ISCSIPersistentVolumeSource{
							TargetPortal: logicalUnit.Address + ":" + logicalUnit.Port,
							IQN:          logicalUnit.Target,
							Lun:          logicalUnit.LunMapping,
							ReadOnly:     false,
						},
					},
					Capacity: core.ResourceList{
						core.ResourceStorage: *resource.NewQuantity(logicalUnit.Size, resource.BinarySI),
					},
					AccessModes: []core.PersistentVolumeAccessMode{
						core.ReadWriteMany,
					},
					VolumeMode: &volMode,
				},
			}
			pvs = append(pvs, pvSpec)
		}
	}
	return
}

// Create PVCs specs for the VM LUNs.
func (r *Builder) LunPersistentVolumeClaims(vmRef ref.Ref) (pvcs []core.PersistentVolumeClaim, err error) {
	vm := &model.Workload{}
	err = r.Source.Inventory.Find(vm, vmRef)
	if err != nil {
		err = liberr.Wrap(
			err,
			"VM lookup failed.",
			"vm",
			vmRef.String())
		return
	}
	for _, da := range vm.DiskAttachments {
		if da.Disk.StorageType == "lun" {
			sc := ""
			volMode := core.PersistentVolumeBlock
			pvcSpec := core.PersistentVolumeClaim{
				ObjectMeta: meta.ObjectMeta{
					Name:      da.Disk.ID,
					Namespace: r.Plan.Spec.TargetNamespace,
					Annotations: map[string]string{
						AnnImportDiskId: da.Disk.ID,
						"vmID":          vm.ID,
						"plan":          string(r.Plan.UID),
						"lun":           "true",
					},
					Labels: map[string]string{"migration": r.Migration.Name},
				},
				Spec: core.PersistentVolumeClaimSpec{
					AccessModes: []core.PersistentVolumeAccessMode{
						core.ReadWriteMany,
					},
					Selector: &meta.LabelSelector{
						MatchLabels: map[string]string{
							"volume": fmt.Sprintf("%v-%v", vm.Name, da.ID),
						},
					},
					StorageClassName: &sc,
					VolumeMode:       &volMode,
					Resources: core.ResourceRequirements{
						Requests: core.ResourceList{
							core.ResourceStorage: *resource.NewQuantity(da.Disk.Lun.LogicalUnits.LogicalUnit[0].Size, resource.BinarySI),
						},
					},
				},
			}
			pvcs = append(pvcs, pvcSpec)
		}
	}
	return
}
