// Copyright 2026 NVIDIA CORPORATION & AFFILIATES
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

package controller

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/stretchr/testify/mock"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/json"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	configurationMocks "github.com/Mellanox/nic-configuration-operator/pkg/configuration/mocks"
	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
	firmwareMocks "github.com/Mellanox/nic-configuration-operator/pkg/firmware/mocks"
	maintenanceMocks "github.com/Mellanox/nic-configuration-operator/pkg/maintenance/mocks"
	"github.com/Mellanox/nic-configuration-operator/pkg/spectrumx"
	spectrumxMocks "github.com/Mellanox/nic-configuration-operator/pkg/spectrumx/mocks"
	"github.com/Mellanox/nic-configuration-operator/pkg/types"
	udevMocks "github.com/Mellanox/nic-configuration-operator/pkg/udev/mocks"
)

var _ = Describe("NicDevice runtime completion", func() {
	var (
		ctx                  context.Context
		apiClient            client.WithWatch
		reconciler           *NicDeviceReconciler
		configurationManager *configurationMocks.ConfigurationManager
		maintenanceManager   *maintenanceMocks.MaintenanceManager
		spectrumXManager     *spectrumxMocks.SpectrumXManager
		device               *v1alpha1.NicDevice
		namespace            string
		appliedSpec          string
		bootID               string
	)

	readDevice := func() *v1alpha1.NicDevice {
		current := &v1alpha1.NicDevice{}
		Expect(apiClient.Get(ctx, client.ObjectKeyFromObject(device), current)).To(Succeed())
		return current
	}
	changeDevice := func(change func(*v1alpha1.NicDevice)) {
		current := readDevice()
		change(current)
		Expect(apiClient.Update(ctx, current)).To(Succeed())
	}
	changeDiscovery := func(change func(*v1alpha1.NicDeviceStatus)) {
		current := readDevice()
		change(&current.Status)
		Expect(apiClient.Status().Update(ctx, current)).To(Succeed())
	}
	patchRecordsApplied := func(object client.Object, patch client.Patch) (bool, error) {
		data, err := patch.Data(object)
		if err != nil {
			return false, err
		}
		var update struct {
			Metadata struct {
				Annotations map[string]*string `json:"annotations"`
			} `json:"metadata"`
		}
		if err := json.Unmarshal(data, &update); err != nil {
			return false, err
		}
		value := update.Metadata.Annotations[consts.LastAppliedStateAnnotation]
		return value != nil, nil
	}
	pendingState := func(spec v1alpha1.NicDeviceSpec, id string) string {
		encodedSpec, err := json.Marshal(spec)
		Expect(err).NotTo(HaveOccurred())
		encoded, err := json.Marshal(struct {
			SpecDigest string `json:"specDigest"`
			BootID     string `json:"bootID"`
		}{SpecDigest: fmt.Sprintf("%x", sha256.Sum256(encodedSpec)), BootID: id})
		Expect(err).NotTo(HaveOccurred())
		return string(encoded)
	}
	seedPending := func(spec v1alpha1.NicDeviceSpec) {
		changeDevice(func(current *v1alpha1.NicDevice) {
			if current.Annotations == nil {
				current.Annotations = make(map[string]string)
			}
			current.Annotations[pendingRuntimeStateAnnotation] = pendingState(spec, bootID)
		})
	}
	expectPendingReboot := func() {
		current := readDevice()
		Expect(current.Annotations).To(HaveKey(pendingRuntimeStateAnnotation))
		var attempt struct {
			BootID         string `json:"bootID"`
			RebootRequired bool   `json:"rebootRequired"`
		}
		Expect(json.Unmarshal([]byte(current.Annotations[pendingRuntimeStateAnnotation]), &attempt)).To(Succeed())
		Expect(attempt.BootID).To(Equal(bootID))
		Expect(attempt.RebootRequired).To(BeTrue())
	}
	request := ctrl.Request{NamespacedName: client.ObjectKey{Name: nicDeviceSyncEventName}}
	type outcome struct {
		result ctrl.Result
		err    error
	}
	// A real API write happens while a hardware callback is blocked, making the
	// resource-version and target changes deterministic rather than timing based.
	reconcileDuring := func(phase string, change func()) outcome {
		entered := make(chan struct{})
		resume := make(chan struct{})
		var release sync.Once
		defer release.Do(func() { close(resume) })
		block := func(mock.Arguments) {
			close(entered)
			select {
			case <-resume:
			case <-ctx.Done():
			}
		}
		validation := configurationManager.On("ValidateDeviceNvSpec", mock.Anything, mock.Anything).
			Return(false, false, nil, nil)
		runtime := configurationManager.On("ApplyRuntimeConfiguration", mock.Anything, mock.Anything).
			Return(&types.RuntimeConfigurationApplyResult{Status: types.ApplyStatusSuccess}, nil)
		if phase == "validation" {
			validation.Run(block)
		} else {
			runtime.Run(block)
		}
		done := make(chan outcome, 1)
		go func() {
			defer GinkgoRecover()
			result, err := reconciler.Reconcile(ctx, request)
			done <- outcome{result: result, err: err}
		}()
		Eventually(entered, 10*time.Second).Should(BeClosed())
		change()
		release.Do(func() { close(resume) })
		var result outcome
		Eventually(done, 10*time.Second).Should(Receive(&result))
		return result
	}
	expectDeferred := func(result outcome) {
		Expect(result.err != nil || result.result.RequeueAfter > 0).To(BeTrue(), "stale inputs must requeue")
		condition := meta.FindStatusCondition(readDevice().Status.Conditions, consts.ConfigUpdateInProgressCondition)
		Expect(condition).NotTo(BeNil())
		Expect(condition.Reason).NotTo(Equal(consts.UpdateSuccessfulReason))
		maintenanceManager.AssertNotCalled(GinkgoT(), "ReleaseMaintenance", mock.Anything)
		maintenanceManager.AssertNotCalled(GinkgoT(), "Reboot")
	}

	BeforeEach(func() {
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(context.Background())
		DeferCleanup(cancel)
		var err error
		apiClient, err = client.NewWithWatch(cfg, client.Options{Scheme: scheme.Scheme})
		Expect(err).NotTo(HaveOccurred())
		ns := &v1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "completion-"}}
		Expect(apiClient.Create(ctx, ns)).To(Succeed())
		namespace = ns.Name
		DeferCleanup(func() {
			Expect(apiClient.DeleteAllOf(context.Background(), &v1alpha1.NicDevice{}, client.InNamespace(namespace))).To(Succeed())
			Expect(apiClient.Delete(context.Background(), ns)).To(Succeed())
		})
		configurationManager = &configurationMocks.ConfigurationManager{}
		bootID = "boot-a"
		maintenanceManager = &maintenanceMocks.MaintenanceManager{}
		maintenanceManager.On("ReleaseMaintenance", mock.Anything).Return(nil)
		spectrumXManager = &spectrumxMocks.SpectrumXManager{}
		udevManager := &udevMocks.UdevManager{}
		udevManager.On("ApplyUdevRules", mock.Anything, mock.Anything).Return(nil, false, nil)
		reconciler = &NicDeviceReconciler{
			Client: apiClient, APIReader: apiClient, Scheme: scheme.Scheme,
			NodeName: namespace, NamespaceName: namespace,
			ConfigurationManager: configurationManager,
			FirmwareManager:      &firmwareMocks.FirmwareManager{},
			MaintenanceManager:   maintenanceManager,
			SpectrumXManager:     spectrumXManager,
			UdevManager:          udevManager,
			EventRecorder:        record.NewFakeRecorder(10),
			readBootID:           func() (string, error) { return bootID, nil },
		}
		device = &v1alpha1.NicDevice{
			ObjectMeta: metav1.ObjectMeta{Name: "completion-device", Namespace: namespace},
			Spec: v1alpha1.NicDeviceSpec{Configuration: &v1alpha1.NicDeviceConfigurationSpec{
				Template: &v1alpha1.ConfigurationTemplateSpec{NumVfs: 1, LinkType: consts.Ethernet},
			}},
		}
		Expect(apiClient.Create(ctx, device)).To(Succeed())
		device.Status = v1alpha1.NicDeviceStatus{
			Node: namespace, Type: "1023", FirmwareVersion: "40.48.1000",
			Ports: []v1alpha1.NicDevicePortSpec{{PCI: "0000:3b:00.0", NetworkInterface: "eth0", RdmaInterface: "mlx5_0"}},
			Conditions: []metav1.Condition{
				{Type: consts.FirmwareUpdateInProgressCondition, Status: metav1.ConditionFalse,
					Reason: consts.DeviceFirmwareSpecEmptyReason, LastTransitionTime: metav1.Now()},
				{Type: consts.ConfigUpdateInProgressCondition, Status: metav1.ConditionTrue,
					Reason: consts.UpdateStartedReason, LastTransitionTime: metav1.Now()},
			},
		}
		Expect(apiClient.Status().Update(ctx, device)).To(Succeed())
		encoded, err := json.Marshal(device.Spec)
		Expect(err).NotTo(HaveOccurred())
		appliedSpec = string(encoded)
	})

	It("retries completion after condition and metadata churn without repeating hardware work", func() {
		result := reconcileDuring("runtime", func() {
			changeDevice(func(current *v1alpha1.NicDevice) {
				if current.Annotations == nil {
					current.Annotations = make(map[string]string)
				}
				current.Annotations["other-writer"] = "preserve"
				current.Labels = map[string]string{"other-writer": "preserve"}
			})
			changeDiscovery(func(status *v1alpha1.NicDeviceStatus) {
				meta.SetStatusCondition(&status.Conditions, metav1.Condition{
					Type: "OtherWriterReady", Status: metav1.ConditionTrue, Reason: "Ready",
				})
			})
		})
		Expect(result.err).NotTo(HaveOccurred())
		current := readDevice()
		Expect(current.Annotations).To(HaveKeyWithValue("other-writer", "preserve"))
		Expect(current.Labels).To(HaveKeyWithValue("other-writer", "preserve"))
		Expect(current.Annotations).To(HaveKeyWithValue(consts.LastAppliedStateAnnotation, appliedSpec))
		Expect(current.Annotations).NotTo(HaveKey(pendingRuntimeStateAnnotation))
		Expect(meta.IsStatusConditionTrue(current.Status.Conditions, "OtherWriterReady")).To(BeTrue())
		condition := meta.FindStatusCondition(current.Status.Conditions, consts.ConfigUpdateInProgressCondition)
		Expect(condition.Reason).To(Equal(consts.UpdateSuccessfulReason))
		Expect(condition.ObservedGeneration).To(Equal(device.Generation))
		configurationManager.AssertNumberOfCalls(GinkgoT(), "ValidateDeviceNvSpec", 1)
		configurationManager.AssertNumberOfCalls(GinkgoT(), "ApplyRuntimeConfiguration", 1)
		maintenanceManager.AssertNumberOfCalls(GinkgoT(), "ReleaseMaintenance", 1)
	})

	DescribeTable("does not apply runtime against changed validation inputs",
		func(change func(*v1alpha1.NicDeviceStatus)) {
			result := reconcileDuring("validation", func() { changeDiscovery(change) })
			expectDeferred(result)
			configurationManager.AssertNotCalled(GinkgoT(), "ApplyRuntimeConfiguration", mock.Anything, mock.Anything)
			Expect(readDevice().Annotations).NotTo(HaveKey(consts.LastAppliedStateAnnotation))
		},
		Entry("network name", func(status *v1alpha1.NicDeviceStatus) { status.Ports[0].NetworkInterface = "eth1" }),
		Entry("RDMA name", func(status *v1alpha1.NicDeviceStatus) { status.Ports[0].RdmaInterface = "mlx5_1" }),
		Entry("PCI topology", func(status *v1alpha1.NicDeviceStatus) { status.Ports[0].PCI = "0000:4b:00.0" }),
		Entry("firmware", func(status *v1alpha1.NicDeviceStatus) { status.FirmwareVersion = "40.48.2000" }),
		Entry("Network Bay identity", func(status *v1alpha1.NicDeviceStatus) {
			status.NetworkBay = &v1alpha1.NicDeviceNetworkBayStatus{Asic: 1, PeerPCI: "0000:4b:00.0"}
		}),
	)

	DescribeTable("keeps applied history but withholds success for runtime target changes",
		func(change func(*v1alpha1.NicDeviceStatus)) {
			result := reconcileDuring("runtime", func() { changeDiscovery(change) })
			expectDeferred(result)
			Expect(readDevice().Annotations).To(HaveKeyWithValue(consts.LastAppliedStateAnnotation, appliedSpec))
			configurationManager.AssertNumberOfCalls(GinkgoT(), "ApplyRuntimeConfiguration", 1)
		},
		Entry("network name", func(status *v1alpha1.NicDeviceStatus) { status.Ports[0].NetworkInterface = "eth1" }),
		Entry("RDMA name", func(status *v1alpha1.NicDeviceStatus) { status.Ports[0].RdmaInterface = "mlx5_1" }),
		Entry("PCI topology", func(status *v1alpha1.NicDeviceStatus) { status.Ports[0].PCI = "0000:4b:00.0" }),
		Entry("firmware", func(status *v1alpha1.NicDeviceStatus) { status.FirmwareVersion = "40.48.2000" }),
		Entry("Network Bay identity", func(status *v1alpha1.NicDeviceStatus) {
			status.NetworkBay = &v1alpha1.NicDeviceNetworkBayStatus{Asic: 1, PeerPCI: "0000:4b:00.0"}
		}),
	)

	It("does not invent applied history when desired spec changes before runtime", func() {
		result := reconcileDuring("validation", func() {
			changeDevice(func(current *v1alpha1.NicDevice) { current.Spec.Configuration.Template.NumVfs = 2 })
		})
		expectDeferred(result)
		Expect(readDevice().Spec.Configuration.Template.NumVfs).To(Equal(2))
		Expect(readDevice().Annotations).NotTo(HaveKey(consts.LastAppliedStateAnnotation))
		configurationManager.AssertNotCalled(GinkgoT(), "ApplyRuntimeConfiguration", mock.Anything, mock.Anything)
	})

	It("preserves first-applied A across desired B and retains the next-pass reboot baseline", func() {
		result := reconcileDuring("runtime", func() {
			changeDevice(func(current *v1alpha1.NicDevice) { current.Spec.Configuration.Template.NumVfs = 2 })
		})
		expectDeferred(result)
		current := readDevice()
		Expect(current.Spec.Configuration.Template.NumVfs).To(Equal(2))
		Expect(current.Annotations).To(HaveKeyWithValue(consts.LastAppliedStateAnnotation, appliedSpec))
		condition := meta.FindStatusCondition(current.Status.Conditions, consts.ConfigUpdateInProgressCondition)
		Expect(condition.ObservedGeneration).NotTo(Equal(current.Generation))

		// B was never applied. A remains the baseline that makes existing full-spec
		// reboot semantics work on the next reconciliation, including first apply.
		configurationManager.ExpectedCalls = nil
		configurationManager.On("ValidateDeviceNvSpec", mock.Anything, mock.Anything).Return(false, false, nil, nil)
		maintenanceManager.On("ScheduleMaintenance", mock.Anything).Return(nil)
		maintenanceManager.On("MaintenanceAllowed", mock.Anything).Return(true, nil)
		maintenanceManager.On("Reboot").Return(nil)
		_, err := reconciler.Reconcile(ctx, request)
		Expect(err).NotTo(HaveOccurred())
		maintenanceManager.AssertNumberOfCalls(GinkgoT(), "Reboot", 1)
		configurationManager.AssertNumberOfCalls(GinkgoT(), "ApplyRuntimeConfiguration", 1)
		Expect(readDevice().Annotations).NotTo(HaveKey(consts.LastAppliedStateAnnotation))
		expectPendingReboot()
	})

	It("retains a reboot baseline if first apply A succeeds but its history cannot be persisted after desired B arrives", func() {
		reconciler.Client = interceptor.NewClient(apiClient, interceptor.Funcs{
			Patch: func(ctx context.Context, underlying client.WithWatch, object client.Object, patch client.Patch, options ...client.PatchOption) error {
				recordsApplied, err := patchRecordsApplied(object, patch)
				if err != nil {
					return err
				}
				if recordsApplied {
					return apierrors.NewInternalError(errors.New("completion write unavailable"))
				}
				return underlying.Patch(ctx, object, patch, options...)
			},
		})
		result := reconcileDuring("runtime", func() {
			changeDevice(func(current *v1alpha1.NicDevice) { current.Spec.Configuration.Template.NumVfs = 2 })
		})
		expectDeferred(result)
		Expect(result.err).To(HaveOccurred())
		Expect(readDevice().Annotations).NotTo(HaveKey(consts.LastAppliedStateAnnotation))
		Expect(readDevice().Spec.Configuration.Template.NumVfs).To(Equal(2))

		// The incomplete A operation remains durable even though its successful
		// completion could not be recorded. B must not bypass reboot/reset.
		reconciler.Client = apiClient
		configurationManager.ExpectedCalls = nil
		configurationManager.On("ValidateDeviceNvSpec", mock.Anything, mock.Anything).Return(false, false, nil, nil)
		maintenanceManager.On("ScheduleMaintenance", mock.Anything).Return(nil)
		maintenanceManager.On("MaintenanceAllowed", mock.Anything).Return(true, nil)
		maintenanceManager.On("Reboot").Return(nil)
		_, err := reconciler.Reconcile(ctx, request)
		Expect(err).NotTo(HaveOccurred())
		maintenanceManager.AssertNumberOfCalls(GinkgoT(), "Reboot", 1)
		configurationManager.AssertNumberOfCalls(GinkgoT(), "ApplyRuntimeConfiguration", 1)
		Expect(readDevice().Annotations).NotTo(HaveKey(consts.LastAppliedStateAnnotation))
		expectPendingReboot()
	})

	It("does not start runtime if its durable pending state cannot be persisted", func() {
		reconciler.Client = interceptor.NewClient(apiClient, interceptor.Funcs{
			Patch: func(context.Context, client.WithWatch, client.Object, client.Patch, ...client.PatchOption) error {
				return apierrors.NewInternalError(errors.New("pending-state write unavailable"))
			},
		})
		result := reconcileDuring("validation", func() {})
		expectDeferred(result)
		Expect(result.err).To(HaveOccurred())
		Expect(readDevice().Annotations).NotTo(HaveKey(consts.LastAppliedStateAnnotation))
		configurationManager.AssertNotCalled(GinkgoT(), "ApplyRuntimeConfiguration", mock.Anything, mock.Anything)
	})

	It("retains pending evidence and reports runtime failure after the intent write changed resource version", func() {
		configurationManager.On("ValidateDeviceNvSpec", mock.Anything, mock.Anything).Return(false, false, nil, nil)
		configurationManager.On("ApplyRuntimeConfiguration", mock.Anything, mock.Anything).
			Return((*types.RuntimeConfigurationApplyResult)(nil), errors.New("runtime command failed"))
		_, err := reconciler.Reconcile(ctx, request)
		Expect(err).To(MatchError(ContainSubstring("runtime command failed")))
		current := readDevice()
		Expect(current.Annotations).NotTo(HaveKey(consts.LastAppliedStateAnnotation))
		Expect(current.Annotations).To(HaveKeyWithValue(pendingRuntimeStateAnnotation, pendingState(device.Spec, bootID)))
		condition := meta.FindStatusCondition(current.Status.Conditions, consts.ConfigUpdateInProgressCondition)
		Expect(condition.Reason).To(Equal(consts.RuntimeConfigUpdateFailedReason))
		Expect(condition.ObservedGeneration).To(Equal(device.Generation))
		maintenanceManager.AssertNotCalled(GinkgoT(), "ReleaseMaintenance", mock.Anything)
		maintenanceManager.AssertNotCalled(GinkgoT(), "Reboot")
	})

	DescribeTable("retries the same desired spec after runtime failure on the same boot",
		func(explicitEmptyIndices bool) {
			if explicitEmptyIndices {
				changeDevice(func(current *v1alpha1.NicDevice) {
					current.Spec.InterfaceNameTemplate = &v1alpha1.NicDeviceInterfaceNameSpec{
						NetDevicePrefix: "net", PlaneIndices: []int{},
					}
				})
			}
			current := readDevice()
			encoded, err := json.Marshal(current.Spec)
			Expect(err).NotTo(HaveOccurred())
			expectedApplied := string(encoded)
			configurationManager.On("ValidateDeviceNvSpec", mock.Anything, mock.Anything).Return(false, false, nil, nil)
			configurationManager.On("ApplyRuntimeConfiguration", mock.Anything, mock.Anything).
				Return((*types.RuntimeConfigurationApplyResult)(nil), errors.New("transient runtime error")).Once()
			configurationManager.On("ApplyRuntimeConfiguration", mock.Anything, mock.Anything).
				Return(&types.RuntimeConfigurationApplyResult{Status: types.ApplyStatusSuccess}, nil).Once()
			_, err = reconciler.Reconcile(ctx, request)
			Expect(err).To(MatchError(ContainSubstring("transient runtime error")))
			Expect(readDevice().Annotations).NotTo(HaveKey(consts.LastAppliedStateAnnotation))
			Expect(readDevice().Annotations[pendingRuntimeStateAnnotation]).To(MatchJSON(pendingState(current.Spec, bootID)))
			_, err = reconciler.Reconcile(ctx, request)
			Expect(err).NotTo(HaveOccurred())
			current = readDevice()
			Expect(current.Annotations).To(HaveKeyWithValue(consts.LastAppliedStateAnnotation, expectedApplied))
			Expect(current.Annotations).NotTo(HaveKey(pendingRuntimeStateAnnotation))
			configurationManager.AssertNumberOfCalls(GinkgoT(), "ApplyRuntimeConfiguration", 2)
			maintenanceManager.AssertNumberOfCalls(GinkgoT(), "ReleaseMaintenance", 1)
			maintenanceManager.AssertNotCalled(GinkgoT(), "Reboot")
		},
		Entry("ordinary spec", false),
		Entry("explicit empty plane indices", true),
	)

	It("keeps same-boot pending evidence after a failed reboot and across a controller restart", func() {
		seedPending(device.Spec)
		changeDevice(func(current *v1alpha1.NicDevice) { current.Spec.Configuration.Template.NumVfs = 2 })
		configurationManager.On("ValidateDeviceNvSpec", mock.Anything, mock.Anything).Return(false, false, nil, nil)
		maintenanceManager.On("ScheduleMaintenance", mock.Anything).Return(nil)
		maintenanceManager.On("MaintenanceAllowed", mock.Anything).Return(true, nil)
		maintenanceManager.On("Reboot").Return(errors.New("reboot command failed")).Once()
		maintenanceManager.On("Reboot").Return(nil).Once()
		_, err := reconciler.Reconcile(ctx, request)
		Expect(err).To(MatchError(ContainSubstring("reboot command failed")))
		expectPendingReboot()

		// Reconstruct the controller without retaining any reconcile-local evidence.
		reconciler = &NicDeviceReconciler{
			Client: apiClient, APIReader: apiClient, Scheme: scheme.Scheme,
			NodeName: namespace, NamespaceName: namespace,
			ConfigurationManager: configurationManager,
			FirmwareManager:      reconciler.FirmwareManager,
			MaintenanceManager:   maintenanceManager,
			SpectrumXManager:     spectrumXManager,
			UdevManager:          reconciler.UdevManager,
			EventRecorder:        record.NewFakeRecorder(10),
			readBootID:           func() (string, error) { return bootID, nil },
		}
		_, err = reconciler.Reconcile(ctx, request)
		Expect(err).NotTo(HaveOccurred())
		maintenanceManager.AssertNumberOfCalls(GinkgoT(), "Reboot", 2)
		configurationManager.AssertNotCalled(GinkgoT(), "ApplyRuntimeConfiguration", mock.Anything, mock.Anything)
		expectPendingReboot()
	})

	It("retires prior-boot pending evidence and applies the current desired spec", func() {
		seedPending(device.Spec)
		changeDevice(func(current *v1alpha1.NicDevice) { current.Spec.Configuration.Template.NumVfs = 2 })
		bootID = "boot-b"
		configurationManager.On("ValidateDeviceNvSpec", mock.Anything, mock.Anything).Return(false, false, nil, nil)
		configurationManager.On("ApplyRuntimeConfiguration", mock.Anything, mock.Anything).
			Return(&types.RuntimeConfigurationApplyResult{Status: types.ApplyStatusSuccess}, nil)
		_, err := reconciler.Reconcile(ctx, request)
		Expect(err).NotTo(HaveOccurred())
		current := readDevice()
		desired, err := json.Marshal(current.Spec)
		Expect(err).NotTo(HaveOccurred())
		Expect(current.Annotations).To(HaveKeyWithValue(consts.LastAppliedStateAnnotation, string(desired)))
		Expect(current.Annotations).NotTo(HaveKey(pendingRuntimeStateAnnotation))
		configurationManager.AssertNumberOfCalls(GinkgoT(), "ApplyRuntimeConfiguration", 1)
		maintenanceManager.AssertNotCalled(GinkgoT(), "Reboot")
		maintenanceManager.AssertNumberOfCalls(GinkgoT(), "ReleaseMaintenance", 1)
	})

	It("cleans prior-boot history and releases maintenance when the full desired spec was removed", func() {
		seedPending(device.Spec)
		changeDevice(func(current *v1alpha1.NicDevice) {
			current.Annotations[consts.LastAppliedStateAnnotation] = appliedSpec
			current.Annotations["other-writer"] = "preserve"
			current.Spec = v1alpha1.NicDeviceSpec{}
		})
		bootID = "boot-b"
		result, err := reconciler.Reconcile(ctx, request)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.IsZero()).To(BeTrue())
		current := readDevice()
		Expect(current.Spec).To(Equal(v1alpha1.NicDeviceSpec{}))
		Expect(current.Annotations).NotTo(HaveKey(pendingRuntimeStateAnnotation))
		Expect(current.Annotations).NotTo(HaveKey(consts.LastAppliedStateAnnotation))
		Expect(current.Annotations).To(HaveKeyWithValue("other-writer", "preserve"))
		maintenanceManager.AssertNumberOfCalls(GinkgoT(), "ReleaseMaintenance", 1)
		maintenanceManager.AssertNotCalled(GinkgoT(), "Reboot")
		configurationManager.AssertNotCalled(GinkgoT(), "ApplyRuntimeConfiguration", mock.Anything, mock.Anything)
	})

	It("retains a reboot boundary for both a completed device and a peer requiring reboot, then converges after boot changes", func() {
		changeDevice(func(current *v1alpha1.NicDevice) {
			current.Annotations = map[string]string{"other-writer": "main"}
			current.Labels = map[string]string{"other-writer": "main"}
		})
		changeDiscovery(func(status *v1alpha1.NicDeviceStatus) {
			meta.SetStatusCondition(&status.Conditions, metav1.Condition{
				Type: "OtherWriterReady", Status: metav1.ConditionTrue, Reason: "Ready",
			})
		})
		oldPeerSpec := device.Spec.DeepCopy()
		oldPeerSpec.Configuration.Template.NumVfs = 2
		oldPeerJSON, err := json.Marshal(oldPeerSpec)
		Expect(err).NotTo(HaveOccurred())
		peer := &v1alpha1.NicDevice{
			ObjectMeta: metav1.ObjectMeta{
				Name: "reboot-peer", Namespace: namespace,
				Annotations: map[string]string{consts.LastAppliedStateAnnotation: string(oldPeerJSON), "other-writer": "peer"},
				Labels:      map[string]string{"other-writer": "peer"},
			},
			Spec: *device.Spec.DeepCopy(),
		}
		Expect(apiClient.Create(ctx, peer)).To(Succeed())
		peer.Status = *readDevice().Status.DeepCopy()
		peer.Status.Ports[0].PCI = "0000:4b:00.0"
		Expect(apiClient.Status().Update(ctx, peer)).To(Succeed())
		configurationManager.On("ValidateDeviceNvSpec", mock.Anything, mock.Anything).Return(false, false, nil, nil)
		configurationManager.On("ApplyRuntimeConfiguration", mock.Anything, mock.Anything).
			Return(&types.RuntimeConfigurationApplyResult{Status: types.ApplyStatusSuccess}, nil)
		maintenanceManager.On("ScheduleMaintenance", mock.Anything).Return(nil)
		maintenanceManager.On("MaintenanceAllowed", mock.Anything).Return(true, nil)
		maintenanceManager.On("Reboot").Return(nil)
		_, err = reconciler.Reconcile(ctx, request)
		Expect(err).NotTo(HaveOccurred())
		configurationManager.AssertNumberOfCalls(GinkgoT(), "ApplyRuntimeConfiguration", 1)
		maintenanceManager.AssertNumberOfCalls(GinkgoT(), "Reboot", 1)
		maintenanceManager.AssertNotCalled(GinkgoT(), "ReleaseMaintenance", mock.Anything)
		for name, writer := range map[string]string{device.Name: "main", peer.Name: "peer"} {
			current := &v1alpha1.NicDevice{}
			Expect(apiClient.Get(ctx, client.ObjectKey{Name: name, Namespace: namespace}, current)).To(Succeed())
			Expect(current.Annotations).NotTo(HaveKey(consts.LastAppliedStateAnnotation))
			var attempt struct {
				BootID         string `json:"bootID"`
				RebootRequired bool   `json:"rebootRequired"`
			}
			Expect(json.Unmarshal([]byte(current.Annotations[pendingRuntimeStateAnnotation]), &attempt)).To(Succeed())
			Expect(attempt.BootID).To(Equal(bootID))
			Expect(attempt.RebootRequired).To(BeTrue())
			Expect(current.Annotations).To(HaveKeyWithValue("other-writer", writer))
			Expect(current.Labels).To(HaveKeyWithValue("other-writer", writer))
			Expect(meta.IsStatusConditionTrue(current.Status.Conditions, "OtherWriterReady")).To(BeTrue())
		}

		bootID = "boot-b"
		_, err = reconciler.Reconcile(ctx, request)
		Expect(err).NotTo(HaveOccurred())
		configurationManager.AssertNumberOfCalls(GinkgoT(), "ApplyRuntimeConfiguration", 3)
		maintenanceManager.AssertNumberOfCalls(GinkgoT(), "Reboot", 1)
		maintenanceManager.AssertNumberOfCalls(GinkgoT(), "ReleaseMaintenance", 1)
		for _, name := range []string{device.Name, peer.Name} {
			current := &v1alpha1.NicDevice{}
			Expect(apiClient.Get(ctx, client.ObjectKey{Name: name, Namespace: namespace}, current)).To(Succeed())
			Expect(current.Annotations).NotTo(HaveKey(pendingRuntimeStateAnnotation))
			Expect(current.Annotations).To(HaveKeyWithValue(consts.LastAppliedStateAnnotation, appliedSpec))
			condition := meta.FindStatusCondition(current.Status.Conditions, consts.ConfigUpdateInProgressCondition)
			Expect(condition.Reason).To(Equal(consts.UpdateSuccessfulReason))
			Expect(condition.ObservedGeneration).To(Equal(current.Generation))
			Expect(meta.IsStatusConditionTrue(current.Status.Conditions, "OtherWriterReady")).To(BeTrue())
		}
	})

	It("requires reboot when configuration is removed while an attempt is pending on this boot", func() {
		seedPending(device.Spec)
		changeDevice(func(current *v1alpha1.NicDevice) { current.Spec.Configuration = nil })
		maintenanceManager.On("ScheduleMaintenance", mock.Anything).Return(nil)
		maintenanceManager.On("MaintenanceAllowed", mock.Anything).Return(true, nil)
		maintenanceManager.On("Reboot").Return(nil)
		_, err := reconciler.Reconcile(ctx, request)
		Expect(err).NotTo(HaveOccurred())
		maintenanceManager.AssertNumberOfCalls(GinkgoT(), "Reboot", 1)
		maintenanceManager.AssertNotCalled(GinkgoT(), "ReleaseMaintenance", mock.Anything)
		configurationManager.AssertNotCalled(GinkgoT(), "ApplyRuntimeConfiguration", mock.Anything, mock.Anything)
		Expect(readDevice().Annotations).To(HaveKey(pendingRuntimeStateAnnotation))
	})

	It("requires reboot when desired reverts to last-applied A after a pending B attempt", func() {
		attemptB := device.Spec.DeepCopy()
		attemptB.Configuration.Template.NumVfs = 2
		seedPending(*attemptB)
		changeDevice(func(current *v1alpha1.NicDevice) {
			current.Annotations[consts.LastAppliedStateAnnotation] = appliedSpec
		})
		configurationManager.On("ValidateDeviceNvSpec", mock.Anything, mock.Anything).Return(false, false, nil, nil)
		maintenanceManager.On("ScheduleMaintenance", mock.Anything).Return(nil)
		maintenanceManager.On("MaintenanceAllowed", mock.Anything).Return(true, nil)
		maintenanceManager.On("Reboot").Return(nil)
		_, err := reconciler.Reconcile(ctx, request)
		Expect(err).NotTo(HaveOccurred())
		maintenanceManager.AssertNumberOfCalls(GinkgoT(), "Reboot", 1)
		configurationManager.AssertNotCalled(GinkgoT(), "ApplyRuntimeConfiguration", mock.Anything, mock.Anything)
		Expect(readDevice().Annotations).To(HaveKey(pendingRuntimeStateAnnotation))
	})

	It("does not attempt runtime when the current boot cannot be identified", func() {
		reconciler.readBootID = func() (string, error) { return "", errors.New("boot identity unavailable") }
		configurationManager.On("ValidateDeviceNvSpec", mock.Anything, mock.Anything).Return(false, false, nil, nil)
		_, err := reconciler.Reconcile(ctx, request)
		Expect(err).To(MatchError(ContainSubstring("boot identity unavailable")))
		configurationManager.AssertNotCalled(GinkgoT(), "ApplyRuntimeConfiguration", mock.Anything, mock.Anything)
		maintenanceManager.AssertNotCalled(GinkgoT(), "Reboot")
		maintenanceManager.AssertNotCalled(GinkgoT(), "ReleaseMaintenance", mock.Anything)
		Expect(readDevice().Annotations).NotTo(HaveKey(pendingRuntimeStateAnnotation))
	})

	It("retains maintenance for a pending attempt while a requested firmware source is not ready", func() {
		seedPending(device.Spec)
		changeDevice(func(current *v1alpha1.NicDevice) {
			current.Spec.Firmware = &v1alpha1.FirmwareTemplateSpec{
				NicFirmwareSourceRef: "pending-source", UpdatePolicy: consts.FirmwareUpdatePolicyValidate,
			}
		})
		firmwareManager := reconciler.FirmwareManager.(*firmwareMocks.FirmwareManager)
		firmwareManager.On("ValidateRequestedFirmwareSource", mock.Anything, mock.Anything).
			Return("", types.FirmwareSourceNotReadyError("pending-source", "download incomplete"))
		result, err := reconciler.Reconcile(ctx, request)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.RequeueAfter).To(BeNumerically(">", 0))
		maintenanceManager.AssertNotCalled(GinkgoT(), "ReleaseMaintenance", mock.Anything)
		maintenanceManager.AssertNotCalled(GinkgoT(), "Reboot")
		configurationManager.AssertNotCalled(GinkgoT(), "ApplyRuntimeConfiguration", mock.Anything, mock.Anything)
		Expect(readDevice().Annotations).To(HaveKey(pendingRuntimeStateAnnotation))
	})

	It("uses the selector fallback across namespaces when installed CRDs lack status.node selection", func() {
		peerNamespace := &v1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "completion-peer-"}}
		Expect(apiClient.Create(ctx, peerNamespace)).To(Succeed())
		DeferCleanup(func() {
			Expect(apiClient.DeleteAllOf(context.Background(), &v1alpha1.NicDevice{}, client.InNamespace(peerNamespace.Name))).To(Succeed())
			Expect(apiClient.Delete(context.Background(), peerNamespace)).To(Succeed())
		})
		peer := &v1alpha1.NicDevice{
			ObjectMeta: metav1.ObjectMeta{Name: "cross-namespace-peer", Namespace: peerNamespace.Name},
			Spec:       *device.Spec.DeepCopy(),
		}
		Expect(apiClient.Create(ctx, peer)).To(Succeed())
		peer.Status = *device.Status.DeepCopy()
		peer.Status.Ports[0].PCI = "0000:4b:00.0"
		Expect(apiClient.Status().Update(ctx, peer)).To(Succeed())
		var counts sync.Mutex
		rejected, fallback := 0, 0
		reconciler.APIReader = interceptor.NewClient(apiClient, interceptor.Funcs{
			List: func(ctx context.Context, underlying client.WithWatch, list client.ObjectList, options ...client.ListOption) error {
				query := &client.ListOptions{}
				for _, option := range options {
					option.ApplyToList(query)
				}
				counts.Lock()
				if query.FieldSelector != nil {
					rejected++
					counts.Unlock()
					return apierrors.NewBadRequest("field label not supported: status.node")
				}
				fallback++
				counts.Unlock()
				return underlying.List(ctx, list, options...)
			},
		})
		configurationManager.On("ValidateDeviceNvSpec", mock.Anything, mock.Anything).Return(false, false, nil, nil)
		configurationManager.On("ApplyRuntimeConfiguration", mock.Anything, mock.Anything).
			Return(&types.RuntimeConfigurationApplyResult{Status: types.ApplyStatusSuccess}, nil)
		_, err := reconciler.Reconcile(ctx, request)
		Expect(err).NotTo(HaveOccurred())
		Expect(rejected).To(BeNumerically(">", 0))
		Expect(fallback).To(Equal(rejected))
		configurationManager.AssertNumberOfCalls(GinkgoT(), "ApplyRuntimeConfiguration", 2)
		Expect(apiClient.Get(ctx, client.ObjectKeyFromObject(peer), peer)).To(Succeed())
		Expect(peer.Annotations).To(HaveKeyWithValue(consts.LastAppliedStateAnnotation, appliedSpec))
		maintenanceManager.AssertNumberOfCalls(GinkgoT(), "ReleaseMaintenance", 1)
	})

	It("withholds maintenance release if a pending reboot marker reappears after success persistence", func() {
		injected := false
		reconciler.Client = interceptor.NewClient(apiClient, interceptor.Funcs{
			SubResourceUpdate: func(ctx context.Context, underlying client.Client, name string, object client.Object, options ...client.SubResourceUpdateOption) error {
				defer GinkgoRecover()
				if err := underlying.SubResource(name).Update(ctx, object, options...); err != nil {
					return err
				}
				if name == "status" && !injected {
					injected = true
					value, err := json.Marshal(struct {
						SpecDigest     string `json:"specDigest"`
						BootID         string `json:"bootID"`
						RebootRequired bool   `json:"rebootRequired"`
					}{SpecDigest: fmt.Sprintf("%x", sha256.Sum256([]byte(appliedSpec))), BootID: bootID, RebootRequired: true})
					Expect(err).NotTo(HaveOccurred())
					changeDevice(func(current *v1alpha1.NicDevice) {
						current.Annotations[pendingRuntimeStateAnnotation] = string(value)
					})
				}
				return nil
			},
		})
		result := reconcileDuring("runtime", func() {})
		Expect(result.err != nil || result.result.RequeueAfter > 0).To(BeTrue())
		Expect(injected).To(BeTrue())
		maintenanceManager.AssertNotCalled(GinkgoT(), "ReleaseMaintenance", mock.Anything)
		configurationManager.AssertNumberOfCalls(GinkgoT(), "ApplyRuntimeConfiguration", 1)
		expectPendingReboot()
	})

	It("never writes completion to a replacement object with the same name", func() {
		originalUID := device.UID
		result := reconcileDuring("runtime", func() {
			Expect(apiClient.Delete(ctx, readDevice())).To(Succeed())
			replacement := &v1alpha1.NicDevice{ObjectMeta: metav1.ObjectMeta{Name: device.Name, Namespace: namespace}, Spec: *device.Spec.DeepCopy()}
			Expect(apiClient.Create(ctx, replacement)).To(Succeed())
			replacement.Status = *device.Status.DeepCopy()
			Expect(apiClient.Status().Update(ctx, replacement)).To(Succeed())
		})
		expectDeferred(result)
		Expect(readDevice().UID).NotTo(Equal(originalUID))
		Expect(readDevice().Annotations).NotTo(HaveKey(consts.LastAppliedStateAnnotation))
	})

	It("does not overwrite a concurrent applied-history writer", func() {
		result := reconcileDuring("runtime", func() {
			changeDevice(func(current *v1alpha1.NicDevice) {
				current.Annotations[consts.LastAppliedStateAnnotation] = "concurrent-history"
			})
		})
		expectDeferred(result)
		Expect(readDevice().Annotations).To(HaveKeyWithValue(consts.LastAppliedStateAnnotation, "concurrent-history"))
	})

	It("does not complete an object whose deletion has started", func() {
		result := reconcileDuring("runtime", func() {
			changeDevice(func(current *v1alpha1.NicDevice) { current.Finalizers = []string{"test.nvidia.com/hold"} })
			Expect(apiClient.Delete(ctx, readDevice())).To(Succeed())
		})
		expectDeferred(result)
		current := readDevice()
		Expect(current.DeletionTimestamp.IsZero()).To(BeFalse())
		Expect(current.Annotations).NotTo(HaveKey(consts.LastAppliedStateAnnotation))
		current.Finalizers = nil
		Expect(apiClient.Update(ctx, current)).To(Succeed())
	})

	It("detects an added node peer before completion of a node-scoped plan", func() {
		result := reconcileDuring("validation", func() {
			peer := device.DeepCopy()
			peer.Name = "new-peer"
			peer.ResourceVersion, peer.UID = "", ""
			peer.CreationTimestamp = metav1.Time{}
			Expect(apiClient.Create(ctx, peer)).To(Succeed())
			peer.Status = *device.Status.DeepCopy()
			peer.Status.Ports[0].PCI = "0000:4b:00.0"
			Expect(apiClient.Status().Update(ctx, peer)).To(Succeed())
		})
		expectDeferred(result)
		configurationManager.AssertNotCalled(GinkgoT(), "ApplyRuntimeConfiguration", mock.Anything, mock.Anything)
	})

	DescribeTable("refuses invalidated or changed prepared plans",
		func(during string, invalidated bool) {
			changeDevice(func(current *v1alpha1.NicDevice) {
				current.Spec.Configuration.Template.SpectrumXOptimized = &v1alpha1.SpectrumXOptimizedSpec{
					Enabled: true, PlatformType: "gb300",
				}
			})
			var lock sync.Mutex
			changed := false
			spectrumXManager.On("PreparePlan", mock.Anything, mock.Anything).Return(nil)
			spectrumXManager.On("GetPreparedPlan", mock.Anything).
				Return(func(*v1alpha1.NicDevice) (*spectrumx.Plan, error) {
					lock.Lock()
					defer lock.Unlock()
					if changed && invalidated {
						return nil, errors.New("plan invalidated by data replacement")
					}
					plan := &spectrumx.Plan{}
					if changed {
						plan.RuntimeConfig = []spectrumx.OperationGroup{{Name: "new-runtime"}}
					}
					return plan, nil
				})
			result := reconcileDuring(during, func() {
				lock.Lock()
				changed = true
				lock.Unlock()
			})
			expectDeferred(result)
			if during == "validation" {
				configurationManager.AssertNotCalled(GinkgoT(), "ApplyRuntimeConfiguration", mock.Anything, mock.Anything)
			}
		},
		Entry("invalidated during validation", "validation", true),
		Entry("changed during validation", "validation", false),
		Entry("invalidated during runtime", "runtime", true),
		Entry("changed during runtime", "runtime", false),
	)

	It("retries conflicts between annotation and status persistence without rerunning runtime", func() {
		statusWrites := 0
		reconciler.Client = interceptor.NewClient(apiClient, interceptor.Funcs{
			SubResourceUpdate: func(ctx context.Context, underlying client.Client, name string, object client.Object, options ...client.SubResourceUpdateOption) error {
				if name == "status" {
					statusWrites++
					if statusWrites == 1 {
						changeDiscovery(func(status *v1alpha1.NicDeviceStatus) {
							meta.SetStatusCondition(&status.Conditions, metav1.Condition{
								Type: "OtherWriterReady", Status: metav1.ConditionTrue, Reason: "Ready",
							})
						})
					}
				}
				return underlying.SubResource(name).Update(ctx, object, options...)
			},
		})
		result := reconcileDuring("runtime", func() {})
		Expect(result.err).NotTo(HaveOccurred())
		Expect(statusWrites).To(BeNumerically(">", 1))
		Expect(meta.IsStatusConditionTrue(readDevice().Status.Conditions, "OtherWriterReady")).To(BeTrue())
		configurationManager.AssertNumberOfCalls(GinkgoT(), "ApplyRuntimeConfiguration", 1)
		maintenanceManager.AssertNumberOfCalls(GinkgoT(), "ReleaseMaintenance", 1)
	})

	DescribeTable("withholds success when material inputs change between applied history and the success condition",
		func(specChanged bool) {
			statusWrites := 0
			reconciler.Client = interceptor.NewClient(apiClient, interceptor.Funcs{
				SubResourceUpdate: func(ctx context.Context, underlying client.Client, name string, object client.Object, options ...client.SubResourceUpdateOption) error {
					if name == "status" {
						statusWrites++
						if statusWrites == 1 {
							if specChanged {
								changeDevice(func(current *v1alpha1.NicDevice) { current.Spec.Configuration.Template.NumVfs = 2 })
							} else {
								changeDiscovery(func(status *v1alpha1.NicDeviceStatus) { status.Ports[0].NetworkInterface = "eth1" })
							}
						}
					}
					return underlying.SubResource(name).Update(ctx, object, options...)
				},
			})
			result := reconcileDuring("runtime", func() {})
			expectDeferred(result)
			Expect(statusWrites).To(Equal(1), "the conflict must revalidate before another success write")
			Expect(readDevice().Annotations).To(HaveKeyWithValue(consts.LastAppliedStateAnnotation, appliedSpec))
			configurationManager.AssertNumberOfCalls(GinkgoT(), "ApplyRuntimeConfiguration", 1)
		},
		Entry("desired spec changes", true),
		Entry("network target changes", false),
	)

	It("retries an annotation conflict while preserving the competing writer's metadata", func() {
		attempts := 0
		reconciler.Client = interceptor.NewClient(apiClient, interceptor.Funcs{
			Patch: func(ctx context.Context, underlying client.WithWatch, object client.Object, patch client.Patch, options ...client.PatchOption) error {
				recordsApplied, err := patchRecordsApplied(object, patch)
				if err != nil {
					return err
				}
				if !recordsApplied {
					return underlying.Patch(ctx, object, patch, options...)
				}
				attempts++
				if attempts == 1 {
					changeDevice(func(current *v1alpha1.NicDevice) {
						current.Annotations["competing-writer"] = "preserve"
					})
				}
				return underlying.Patch(ctx, object, patch, options...)
			},
		})
		result := reconcileDuring("runtime", func() {})
		Expect(result.err).NotTo(HaveOccurred())
		Expect(attempts).To(Equal(2))
		Expect(readDevice().Annotations).To(HaveKeyWithValue("competing-writer", "preserve"))
		Expect(readDevice().Annotations).To(HaveKeyWithValue(consts.LastAppliedStateAnnotation, appliedSpec))
		configurationManager.AssertNumberOfCalls(GinkgoT(), "ApplyRuntimeConfiguration", 1)
		maintenanceManager.AssertNumberOfCalls(GinkgoT(), "ReleaseMaintenance", 1)
	})

	DescribeTable("retains applied history but withholds release when success persistence fails",
		func(conflict bool) {
			attempts := 0
			reconciler.Client = interceptor.NewClient(apiClient, interceptor.Funcs{
				SubResourceUpdate: func(context.Context, client.Client, string, client.Object, ...client.SubResourceUpdateOption) error {
					attempts++
					if conflict {
						return apierrors.NewConflict(schema.GroupResource{Group: v1alpha1.GroupVersion.Group, Resource: "nicdevices"}, device.Name, errors.New("competing status writer"))
					}
					return apierrors.NewInternalError(errors.New("API unavailable"))
				},
			})
			result := reconcileDuring("runtime", func() {})
			expectDeferred(result)
			Expect(result.err).To(HaveOccurred())
			Expect(attempts).To(BeNumerically(">=", 1))
			if conflict {
				Expect(attempts).To(BeNumerically(">", 1))
			}
			Expect(attempts).To(BeNumerically("<=", 10))
			Expect(readDevice().Annotations).To(HaveKeyWithValue(consts.LastAppliedStateAnnotation, appliedSpec))
			configurationManager.AssertNumberOfCalls(GinkgoT(), "ApplyRuntimeConfiguration", 1)
		},
		Entry("API failure", false),
		Entry("exhausted optimistic conflicts", true),
	)

	DescribeTable("fails closed when applied-history persistence fails",
		func(conflict bool) {
			attempts := 0
			reconciler.Client = interceptor.NewClient(apiClient, interceptor.Funcs{
				Patch: func(ctx context.Context, underlying client.WithWatch, object client.Object, patch client.Patch, options ...client.PatchOption) error {
					recordsApplied, err := patchRecordsApplied(object, patch)
					if err != nil {
						return err
					}
					if !recordsApplied {
						return underlying.Patch(ctx, object, patch, options...)
					}
					attempts++
					if conflict {
						return apierrors.NewConflict(schema.GroupResource{Group: v1alpha1.GroupVersion.Group, Resource: "nicdevices"}, device.Name, errors.New("competing writer"))
					}
					return apierrors.NewInternalError(errors.New("API unavailable"))
				},
			})
			result := reconcileDuring("runtime", func() {})
			expectDeferred(result)
			Expect(result.err).To(HaveOccurred())
			Expect(attempts).To(BeNumerically(">=", 1))
			if conflict {
				Expect(attempts).To(BeNumerically(">", 1))
			}
			Expect(attempts).To(BeNumerically("<=", 10))
			Expect(readDevice().Annotations).NotTo(HaveKey(consts.LastAppliedStateAnnotation))
			configurationManager.AssertNumberOfCalls(GinkgoT(), "ApplyRuntimeConfiguration", 1)
		},
		Entry("API failure", false),
		Entry("exhausted optimistic conflicts", true),
	)
})
