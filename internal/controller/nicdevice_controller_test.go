/*
2024 NVIDIA CORPORATION & AFFILIATES
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"errors"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/stretchr/testify/mock"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sTypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/json"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
	firmwareMocks "github.com/Mellanox/nic-configuration-operator/pkg/firmware/mocks"
	hostMocks "github.com/Mellanox/nic-configuration-operator/pkg/host/mocks"
	maintenanceMocks "github.com/Mellanox/nic-configuration-operator/pkg/maintenance/mocks"
	"github.com/Mellanox/nic-configuration-operator/pkg/testutils"
	"github.com/Mellanox/nic-configuration-operator/pkg/types"
)

const specValidationFailed = "spec validation failed"

var _ = Describe("NicDeviceReconciler", func() {
	var (
		mgr                 manager.Manager
		k8sClient           client.Client
		reconciler          *NicDeviceReconciler
		hostManager         *hostMocks.HostManager
		firmwareManager     *firmwareMocks.FirmwareManager
		maintenanceManager  *maintenanceMocks.MaintenanceManager
		hostUtils           *hostMocks.HostUtils
		deviceName          = "test-device"
		ctx                 context.Context
		cancel              context.CancelFunc
		timeout             = time.Second * 10
		namespaceName       string
		wg                  sync.WaitGroup
		getDeviceConditions = func() []metav1.Condition {
			device := &v1alpha1.NicDevice{}
			Expect(k8sClient.Get(ctx, k8sTypes.NamespacedName{Name: deviceName, Namespace: namespaceName}, device)).To(Succeed())
			return device.Status.Conditions
		}
		getLastAppliedStateAnnotation = func() string {
			device := &v1alpha1.NicDevice{}
			Expect(k8sClient.Get(ctx, k8sTypes.NamespacedName{Name: deviceName, Namespace: namespaceName}, device)).To(Succeed())
			val := device.Annotations[consts.LastAppliedStateAnnotation]
			return val
		}
		lastAppliedStateAnnotationExists = func() bool {
			device := &v1alpha1.NicDevice{}
			Expect(k8sClient.Get(ctx, k8sTypes.NamespacedName{Name: deviceName, Namespace: namespaceName}, device)).To(Succeed())
			_, found := device.Annotations[consts.LastAppliedStateAnnotation]
			return found
		}
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.TODO())
		mgr = createManager()

		deviceDiscoveryReconcileTime = 1 * time.Second
		hostManager = &hostMocks.HostManager{}
		firmwareManager = &firmwareMocks.FirmwareManager{}
		maintenanceManager = &maintenanceMocks.MaintenanceManager{}
		hostUtils = &hostMocks.HostUtils{}

		reconciler = &NicDeviceReconciler{
			Client:             mgr.GetClient(),
			Scheme:             mgr.GetScheme(),
			NodeName:           nodeName,
			NamespaceName:      namespaceName,
			HostManager:        hostManager,
			FirmwareManager:    firmwareManager,
			MaintenanceManager: maintenanceManager,
			HostUtils:          hostUtils,
			EventRecorder:      mgr.GetEventRecorderFor("testReconciler"),
		}
		Expect(reconciler.SetupWithManager(mgr, false)).To(Succeed())

		k8sClient = mgr.GetClient()

		namespaceName = createNodeAndRandomNamespace(ctx, k8sClient)

		startManager(mgr, ctx, &wg)

		DeferCleanup(func() {
			By("Shut down controller manager")
			wg.Wait()
		})
	})

	AfterEach(func() {
		cancel()

		Expect(k8sClient.DeleteAllOf(context.Background(), &v1alpha1.NicDevice{}, client.InNamespace(namespaceName))).To(Succeed())
		Expect(k8sClient.Delete(context.Background(), &v1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespaceName}})).To(Succeed())
		Expect(k8sClient.DeleteAllOf(context.Background(), &v1.Node{})).To(Succeed())
	})

	Describe("nicDeviceConfigurationStatuses tests", func() {
		It("should return correct flag if nvConfigUpdateRequired", func() {
			statuses := nicDeviceConfigurationStatuses{{nvConfigUpdateRequired: true}, {nvConfigUpdateRequired: false}}
			Expect(statuses.nvConfigUpdateRequired()).To(Equal(true))
			statuses = nicDeviceConfigurationStatuses{{nvConfigUpdateRequired: true}, {nvConfigUpdateRequired: true}}
			Expect(statuses.nvConfigUpdateRequired()).To(Equal(true))
			statuses = nicDeviceConfigurationStatuses{{nvConfigUpdateRequired: false}, {nvConfigUpdateRequired: false}}
			Expect(statuses.nvConfigUpdateRequired()).To(Equal(false))
		})

		It("should return correct flag if rebootRequired", func() {
			statuses := nicDeviceConfigurationStatuses{{rebootRequired: true}, {rebootRequired: false}}
			Expect(statuses.rebootRequired()).To(Equal(true))
			statuses = nicDeviceConfigurationStatuses{{rebootRequired: true}, {rebootRequired: true}}
			Expect(statuses.rebootRequired()).To(Equal(true))
			statuses = nicDeviceConfigurationStatuses{{rebootRequired: false}, {rebootRequired: false}}
			Expect(statuses.rebootRequired()).To(Equal(false))
		})

		It("should return correct flag if nvConfigReadyForAll", func() {
			statuses := nicDeviceConfigurationStatuses{
				{rebootRequired: true, nvConfigUpdateRequired: true},
				{rebootRequired: false, nvConfigUpdateRequired: false},
			}
			Expect(statuses.nvConfigReadyForAll()).To(Equal(false))
			statuses = nicDeviceConfigurationStatuses{
				{rebootRequired: true, nvConfigUpdateRequired: false},
				{rebootRequired: true, nvConfigUpdateRequired: false},
			}
			Expect(statuses.nvConfigReadyForAll()).To(Equal(false))
			statuses = nicDeviceConfigurationStatuses{
				{rebootRequired: false, nvConfigUpdateRequired: true},
				{rebootRequired: false, nvConfigUpdateRequired: true},
			}
			Expect(statuses.nvConfigReadyForAll()).To(Equal(false))
			statuses = nicDeviceConfigurationStatuses{
				{rebootRequired: true, nvConfigUpdateRequired: false},
				{rebootRequired: false, nvConfigUpdateRequired: true},
			}
			Expect(statuses.nvConfigReadyForAll()).To(Equal(false))
			statuses = nicDeviceConfigurationStatuses{
				{rebootRequired: false, nvConfigUpdateRequired: false},
				{rebootRequired: false, nvConfigUpdateRequired: false},
			}
			Expect(statuses.nvConfigReadyForAll()).To(Equal(true))
		})

		It("should return correct flag if firmwareUpToDate", func() {
			status := nicDeviceConfigurationStatus{requestedFirmwareVersion: "1", device: &v1alpha1.NicDevice{Status: v1alpha1.NicDeviceStatus{FirmwareVersion: "1"}}}
			Expect(status.firmwareUpToDate()).To(Equal(true))
			status = nicDeviceConfigurationStatus{requestedFirmwareVersion: "1", device: &v1alpha1.NicDevice{Status: v1alpha1.NicDeviceStatus{FirmwareVersion: "2"}}}
			Expect(status.firmwareUpToDate()).To(Equal(false))
		})

		It("should return correct flag if firmwareReady", func() {
			statuses := nicDeviceConfigurationStatuses{
				{firmwareValidationSuccessful: true, requestedFirmwareVersion: "2", device: &v1alpha1.NicDevice{Status: v1alpha1.NicDeviceStatus{FirmwareVersion: "1"}}},
				{firmwareValidationSuccessful: false, requestedFirmwareVersion: "1", device: &v1alpha1.NicDevice{Status: v1alpha1.NicDeviceStatus{FirmwareVersion: "1"}}},
			}
			Expect(statuses.firmwareReady()).To(Equal(false))
			statuses = nicDeviceConfigurationStatuses{
				{firmwareValidationSuccessful: true, requestedFirmwareVersion: "1", device: &v1alpha1.NicDevice{Status: v1alpha1.NicDeviceStatus{FirmwareVersion: "1"}}},
				{firmwareValidationSuccessful: true, requestedFirmwareVersion: "1", device: &v1alpha1.NicDevice{Status: v1alpha1.NicDeviceStatus{FirmwareVersion: "1"}}},
			}
			Expect(statuses.firmwareReady()).To(Equal(true))
			statuses = nicDeviceConfigurationStatuses{
				{firmwareValidationSuccessful: false, requestedFirmwareVersion: "2", device: &v1alpha1.NicDevice{Status: v1alpha1.NicDeviceStatus{FirmwareVersion: "1"}}},
				{firmwareValidationSuccessful: false, requestedFirmwareVersion: "2", device: &v1alpha1.NicDevice{Status: v1alpha1.NicDeviceStatus{FirmwareVersion: "1"}}},
			}
			Expect(statuses.firmwareReady()).To(Equal(false))
			statuses = nicDeviceConfigurationStatuses{
				{firmwareValidationSuccessful: true, requestedFirmwareVersion: "1", device: &v1alpha1.NicDevice{Status: v1alpha1.NicDeviceStatus{FirmwareVersion: "1"}}},
				{firmwareValidationSuccessful: false, requestedFirmwareVersion: "2", device: &v1alpha1.NicDevice{Status: v1alpha1.NicDeviceStatus{FirmwareVersion: "1"}}},
			}
			Expect(statuses.firmwareReady()).To(Equal(false))
			statuses = nicDeviceConfigurationStatuses{
				{firmwareValidationSuccessful: false, requestedFirmwareVersion: "1", device: &v1alpha1.NicDevice{Status: v1alpha1.NicDeviceStatus{FirmwareVersion: "1"}}},
				{firmwareValidationSuccessful: false, requestedFirmwareVersion: "1", device: &v1alpha1.NicDevice{Status: v1alpha1.NicDeviceStatus{FirmwareVersion: "1"}}},
			}
			Expect(statuses.firmwareReady()).To(Equal(false))
		})

		It("should return correct flag if firmwareUpdateRequired", func() {
			// version mismatch but policy is to Validate
			falseStatus1 := nicDeviceConfigurationStatus{
				firmwareValidationSuccessful: true, requestedFirmwareVersion: "2", device: &v1alpha1.NicDevice{
					Spec: v1alpha1.NicDeviceSpec{Firmware: &v1alpha1.FirmwareTemplateSpec{
						NicFirmwareSourceRef: "some-ref",
						UpdatePolicy:         consts.FirmwareUpdatePolicyValidate}},
					Status: v1alpha1.NicDeviceStatus{FirmwareVersion: "1"}},
			}
			Expect(falseStatus1.firmwareUpdateRequired()).To(Equal(false))

			// version mismatch and policy is to update but firmware validation was unsuccessful
			falseStatus2 := nicDeviceConfigurationStatus{
				firmwareValidationSuccessful: false, requestedFirmwareVersion: "", device: &v1alpha1.NicDevice{
					Spec: v1alpha1.NicDeviceSpec{Firmware: &v1alpha1.FirmwareTemplateSpec{
						NicFirmwareSourceRef: "some-ref",
						UpdatePolicy:         consts.FirmwareUpdatePolicyUpdate}},
					Status: v1alpha1.NicDeviceStatus{FirmwareVersion: "1"}},
			}
			Expect(falseStatus2.firmwareUpdateRequired()).To(Equal(false))

			// version mismatch and policy is to Update, validation successful
			trueStatus := nicDeviceConfigurationStatus{
				firmwareValidationSuccessful: true, requestedFirmwareVersion: "2", device: &v1alpha1.NicDevice{
					Spec: v1alpha1.NicDeviceSpec{Firmware: &v1alpha1.FirmwareTemplateSpec{
						NicFirmwareSourceRef: "some-ref",
						UpdatePolicy:         consts.FirmwareUpdatePolicyUpdate}},
					Status: v1alpha1.NicDeviceStatus{FirmwareVersion: "1"}},
			}
			Expect(trueStatus.firmwareUpdateRequired()).To(Equal(true))

			// version matches and policy is to Update
			falseStatus3 := nicDeviceConfigurationStatus{
				firmwareValidationSuccessful: true, requestedFirmwareVersion: "2", device: &v1alpha1.NicDevice{
					Spec: v1alpha1.NicDeviceSpec{Firmware: &v1alpha1.FirmwareTemplateSpec{
						NicFirmwareSourceRef: "some-ref",
						UpdatePolicy:         consts.FirmwareUpdatePolicyUpdate}},
					Status: v1alpha1.NicDeviceStatus{FirmwareVersion: "2"}},
			}
			Expect(falseStatus3.firmwareUpdateRequired()).To(Equal(false))

			// should return true if at least one is true
			statuses := nicDeviceConfigurationStatuses{&falseStatus1, &trueStatus}
			Expect(statuses.firmwareUpdateRequired()).To(Equal(true))

			// should return false if all false
			statuses = nicDeviceConfigurationStatuses{&falseStatus1, &falseStatus2, &falseStatus3}
			Expect(statuses.firmwareUpdateRequired()).To(Equal(false))
		})
	})

	Describe("updateDeviceStatusCondition", func() {
		It("should set the given status condition for device", func() {
			device := &v1alpha1.NicDevice{ObjectMeta: metav1.ObjectMeta{Name: deviceName, Namespace: namespaceName}}
			Expect(k8sClient.Create(ctx, device))
			device.Status = v1alpha1.NicDeviceStatus{
				Ports: []v1alpha1.NicDevicePortSpec{{
					PCI: "0000:3b:00.0",
				}},
			}
			Expect(k8sClient.Status().Update(ctx, device)).To(Succeed())

			time.Sleep(time.Second * 1)

			Expect(k8sClient.Get(ctx, k8sTypes.NamespacedName{Name: deviceName, Namespace: namespaceName}, device)).To(Succeed())
			err := reconciler.updateConfigInProgressStatusCondition(ctx, device, "TestReason", metav1.ConditionTrue, "test-message")
			Expect(err).NotTo(HaveOccurred())

			Eventually(getDeviceConditions, timeout).Should(testutils.MatchCondition(metav1.Condition{
				Type:    consts.ConfigUpdateInProgressCondition,
				Status:  metav1.ConditionTrue,
				Reason:  "TestReason",
				Message: "test-message",
			}))

			Expect(k8sClient.Get(ctx, k8sTypes.NamespacedName{Name: deviceName, Namespace: namespaceName}, device)).To(Succeed())
			err = reconciler.updateConfigInProgressStatusCondition(ctx, device, "AnotherTestReason", metav1.ConditionFalse, "")
			Expect(err).NotTo(HaveOccurred())
			err = reconciler.updateFirmwareUpdateInProgressStatusCondition(ctx, device, "AnotherTestReason", metav1.ConditionTrue, "")
			Expect(err).NotTo(HaveOccurred())

			Eventually(getDeviceConditions, timeout).Should(testutils.MatchCondition(metav1.Condition{
				Type:    consts.ConfigUpdateInProgressCondition,
				Status:  metav1.ConditionFalse,
				Reason:  "AnotherTestReason",
				Message: "",
			}))
			Eventually(func() []metav1.Condition {
				device := &v1alpha1.NicDevice{}
				Expect(k8sClient.Get(ctx, k8sTypes.NamespacedName{Name: deviceName, Namespace: namespaceName}, device)).To(Succeed())
				return device.Status.Conditions
			}, timeout).Should(testutils.MatchCondition(metav1.Condition{
				Type:    consts.FirmwareUpdateInProgressCondition,
				Status:  metav1.ConditionTrue,
				Reason:  "AnotherTestReason",
				Message: "",
			}))
		})
	})

	Describe("reconcile a single device without firmware spec", func() {
		var createDevice = func(setLastSpecAnnotation bool, initialStatusCondition *metav1.Condition) *v1alpha1.NicDevice {
			device := &v1alpha1.NicDevice{
				ObjectMeta: metav1.ObjectMeta{Name: deviceName, Namespace: namespaceName},
				Spec: v1alpha1.NicDeviceSpec{
					Configuration: &v1alpha1.NicDeviceConfigurationSpec{
						ResetToDefault: false,
						Template: &v1alpha1.ConfigurationTemplateSpec{
							NumVfs:   4,
							LinkType: consts.Ethernet,
							PciPerformanceOptimized: &v1alpha1.PciPerformanceOptimizedSpec{
								Enabled:       true,
								MaxAccOutRead: 9999,
							},
						},
					},
				},
			}
			if setLastSpecAnnotation {
				device.SetAnnotations(map[string]string{consts.LastAppliedStateAnnotation: "some-state"})
			}

			Expect(k8sClient.Create(ctx, device))
			device.Status = v1alpha1.NicDeviceStatus{
				Node: nodeName,
				Ports: []v1alpha1.NicDevicePortSpec{{
					PCI: "0000:3b:00.0",
				}},
			}
			if initialStatusCondition != nil {
				meta.SetStatusCondition(&device.Status.Conditions, *initialStatusCondition)
			}
			Expect(k8sClient.Status().Update(ctx, device)).To(Succeed())

			return device
		}

		It("Should not reconcile device not from its node", func() {
			device := &v1alpha1.NicDevice{ObjectMeta: metav1.ObjectMeta{Name: "another-name", Namespace: namespaceName}}
			Expect(k8sClient.Create(ctx, device))
			device.Status = v1alpha1.NicDeviceStatus{
				Node: "some-other-node",
				Ports: []v1alpha1.NicDevicePortSpec{{
					PCI: "0000:3b:00.0",
				}},
			}
			Expect(k8sClient.Status().Update(ctx, device)).To(Succeed())
			Expect(k8sClient.Get(ctx, k8sTypes.NamespacedName{Name: "another-name", Namespace: namespaceName}, device)).To(Succeed())

			Consistently(func() []metav1.Condition {
				maintenanceManager.AssertNotCalled(GinkgoT(), "ReleaseMaintenance", mock.Anything)
				device := &v1alpha1.NicDevice{}
				Expect(k8sClient.Get(ctx, k8sTypes.NamespacedName{Name: "another-name", Namespace: namespaceName}, device)).To(Succeed())
				return device.Status.Conditions
			}).Should(BeNil())
		})
		It("Should result in SpecValidationFailed status if spec validation failed", func() {
			hostManager.On("ValidateDeviceNvSpec", mock.Anything, mock.Anything).Return(false, false, errors.New(specValidationFailed))

			createDevice(false, nil)

			Eventually(getDeviceConditions, timeout).Should(testutils.MatchCondition(metav1.Condition{
				Type:    consts.ConfigUpdateInProgressCondition,
				Status:  metav1.ConditionFalse,
				Reason:  consts.SpecValidationFailed,
				Message: specValidationFailed,
			}))
		})
		It("Should result in IncorrectSpec status if spec is incorrect", func() {
			err := types.IncorrectSpecError("spec error")
			errorText := err.Error()
			hostManager.On("ValidateDeviceNvSpec", mock.Anything, mock.Anything).Return(false, false, err)

			createDevice(false, nil)

			Eventually(getDeviceConditions, timeout).Should(testutils.MatchCondition(metav1.Condition{
				Type:    consts.ConfigUpdateInProgressCondition,
				Status:  metav1.ConditionFalse,
				Reason:  consts.IncorrectSpecReason,
				Message: errorText,
			}))
		})
		It("Should result in UpdateSuccessful status if nv config updates or reboot are not required", func() {
			hostManager.On("ValidateDeviceNvSpec", mock.Anything, mock.Anything).Return(false, false, nil)
			hostManager.On("ApplyDeviceRuntimeSpec", mock.Anything).Return(nil)
			maintenanceManager.On("ReleaseMaintenance", mock.Anything).Return(nil)

			createDevice(false, nil)

			Eventually(getDeviceConditions, timeout).Should(testutils.MatchCondition(metav1.Condition{
				Type:   consts.ConfigUpdateInProgressCondition,
				Status: metav1.ConditionFalse,
				Reason: consts.UpdateSuccessfulReason,
			}))

			device := &v1alpha1.NicDevice{}
			Expect(k8sClient.Get(ctx, k8sTypes.NamespacedName{Name: deviceName, Namespace: namespaceName}, device)).To(Succeed())
			spec, err := json.Marshal(device.Spec)
			Expect(err).NotTo(HaveOccurred())

			// Should dump the last applied state to annotations
			Eventually(getLastAppliedStateAnnotation).Should(Equal(string(spec)))

			maintenanceManager.AssertCalled(GinkgoT(), "ReleaseMaintenance", mock.Anything)
			maintenanceManager.AssertExpectations(GinkgoT())
		})
		It("Should keep in UpdateStarted status if maintenance fails to schedule", func() {
			errorText := "maintenance request failed"
			hostManager.On("ValidateDeviceNvSpec", mock.Anything, mock.Anything).Return(true, false, nil)
			maintenanceManager.On("ScheduleMaintenance", mock.Anything).Return(errors.New(errorText))

			createDevice(true, nil)

			Eventually(getDeviceConditions, timeout).Should(testutils.MatchCondition(metav1.Condition{
				Type:    consts.ConfigUpdateInProgressCondition,
				Status:  metav1.ConditionTrue,
				Reason:  consts.UpdateStartedReason,
				Message: "", // Should not copy the error message from the failed maintenance request
			}))
			// Should keep this status consistently
			Consistently(getDeviceConditions).Should(testutils.MatchCondition(metav1.Condition{
				Type:   consts.ConfigUpdateInProgressCondition,
				Status: metav1.ConditionTrue,
				Reason: consts.UpdateStartedReason,
			}))
			// Should not reset last applied state annotation if maintenance was not scheduled
			Consistently(lastAppliedStateAnnotationExists).Should(BeTrue())
		})
		It("Should keep in UpdateStarted status if maintenance is not allowed", func() {
			hostManager.On("ValidateDeviceNvSpec", mock.Anything, mock.Anything).Return(true, false, nil)
			maintenanceManager.On("ScheduleMaintenance", mock.Anything).Return(nil)
			maintenanceManager.On("MaintenanceAllowed", mock.Anything).Return(false, nil)

			createDevice(true, nil)

			Eventually(getDeviceConditions, timeout).Should(testutils.MatchCondition(metav1.Condition{
				Type:   consts.ConfigUpdateInProgressCondition,
				Status: metav1.ConditionTrue,
				Reason: consts.UpdateStartedReason,
			}))
			// Should keep this status consistently
			Consistently(getDeviceConditions).Should(testutils.MatchCondition(metav1.Condition{
				Type:   consts.ConfigUpdateInProgressCondition,
				Status: metav1.ConditionTrue,
				Reason: consts.UpdateStartedReason,
			}))
			// Should not reset last applied state annotation if maintenance was not allowed
			Consistently(lastAppliedStateAnnotationExists).Should(BeTrue())
		})
		It("Should result in NonVolatileConfigUpdateFailed status if nv config fails to apply", func() {
			errorText := "maintenance request failed"
			hostManager.On("ValidateDeviceNvSpec", mock.Anything, mock.Anything).Return(true, false, nil)
			maintenanceManager.On("ScheduleMaintenance", mock.Anything).Return(nil)
			maintenanceManager.On("MaintenanceAllowed", mock.Anything).Return(true, nil)
			hostManager.On("ApplyDeviceNvSpec", mock.Anything, mock.Anything).Return(false, errors.New(errorText))

			createDevice(false, nil)

			Eventually(getDeviceConditions, timeout).Should(testutils.MatchCondition(metav1.Condition{
				Type:    consts.ConfigUpdateInProgressCondition,
				Status:  metav1.ConditionFalse,
				Reason:  consts.NonVolatileConfigUpdateFailedReason,
				Message: errorText,
			}))
		})
		It("Should result in Pending status and not apply runtime spec if failed to reboot", func() {
			errorText := "reboot request failed"
			hostManager.On("ValidateDeviceNvSpec", mock.Anything, mock.Anything).Return(true, true, nil)
			maintenanceManager.On("ScheduleMaintenance", mock.Anything).Return(nil)
			maintenanceManager.On("MaintenanceAllowed", mock.Anything).Return(true, nil)
			hostManager.On("ApplyDeviceNvSpec", mock.Anything, mock.Anything).Return(true, nil)
			maintenanceManager.On("Reboot").Return(errors.New(errorText))

			createDevice(true, nil)

			Eventually(getDeviceConditions, timeout).Should(testutils.MatchCondition(metav1.Condition{
				Type:   consts.ConfigUpdateInProgressCondition,
				Status: metav1.ConditionTrue,
				Reason: consts.PendingRebootReason,
			}))

			// Should reset last applied state annotation if tried to reboot but failed
			Eventually(lastAppliedStateAnnotationExists).Should(BeFalse())

			maintenanceManager.AssertNotCalled(GinkgoT(), "ApplyDeviceRuntimeSpec", mock.Anything)
			maintenanceManager.AssertExpectations(GinkgoT())
		})
		It("Should not release maintenance if runtime config failed to apply", func() {
			errorText := "runtime config update failed"
			hostManager.On("ValidateDeviceNvSpec", mock.Anything, mock.Anything).Return(false, false, nil)
			hostManager.On("ApplyDeviceRuntimeSpec", mock.Anything).Return(errors.New(errorText))

			createDevice(false, nil)

			Eventually(getDeviceConditions, timeout).Should(testutils.MatchCondition(metav1.Condition{
				Type:    consts.ConfigUpdateInProgressCondition,
				Status:  metav1.ConditionFalse,
				Reason:  consts.RuntimeConfigUpdateFailedReason,
				Message: errorText,
			}))

			maintenanceManager.AssertNotCalled(GinkgoT(), "ScheduleMaintenance", mock.Anything)
			maintenanceManager.AssertNotCalled(GinkgoT(), "MaintenanceAllowed", mock.Anything)
			hostManager.AssertNotCalled(GinkgoT(), "ApplyDeviceNvSpec", mock.Anything, mock.Anything)
			maintenanceManager.AssertNotCalled(GinkgoT(), "Reboot")
			maintenanceManager.AssertNotCalled(GinkgoT(), "ReleaseMaintenance", mock.Anything)
			hostManager.AssertExpectations(GinkgoT())
			maintenanceManager.AssertExpectations(GinkgoT())
		})
		It("Should request maintenance if runtime config needs to be reset", func() {
			hostManager.On("ValidateDeviceNvSpec", mock.Anything, mock.Anything).Return(false, false, nil)
			maintenanceManager.On("ScheduleMaintenance", mock.Anything).Return(nil)
			maintenanceManager.On("MaintenanceAllowed", mock.Anything).Return(false, nil)

			createDevice(true, nil) // lastAppliedSpec will not match the current resulting in need to reboot

			Eventually(getDeviceConditions, timeout).Should(testutils.MatchCondition(metav1.Condition{
				Type:   consts.ConfigUpdateInProgressCondition,
				Status: metav1.ConditionTrue,
				Reason: consts.PendingRebootReason,
			}))

			hostManager.AssertNotCalled(GinkgoT(), "ApplyDeviceNvSpec", mock.Anything, mock.Anything)
			maintenanceManager.AssertNotCalled(GinkgoT(), "ReleaseMaintenance", mock.Anything)
			hostManager.AssertExpectations(GinkgoT())
			maintenanceManager.AssertExpectations(GinkgoT())
		})
		It("Should not request another reboot if nv config failed to apply after the first one", func() {
			hostManager.On("ValidateDeviceNvSpec", mock.Anything, mock.Anything).Return(false, true, nil)
			hostUtils.On("GetHostUptimeSeconds").Return(time.Second*0, nil)

			_ = createDevice(false, &metav1.Condition{
				Type:    consts.ConfigUpdateInProgressCondition,
				Status:  metav1.ConditionTrue,
				Reason:  consts.PendingRebootReason,
				Message: "",
			})

			Eventually(getDeviceConditions, timeout).Should(testutils.MatchCondition(metav1.Condition{
				Type:    consts.ConfigUpdateInProgressCondition,
				Status:  metav1.ConditionFalse,
				Reason:  consts.FirmwareError,
				Message: consts.FwConfigNotAppliedAfterRebootErrorMsg,
			}))

			hostManager.AssertNotCalled(GinkgoT(), "ApplyDeviceRuntimeSpec", mock.Anything)
			maintenanceManager.AssertNotCalled(GinkgoT(), "ScheduleMaintenance", mock.Anything)
			maintenanceManager.AssertNotCalled(GinkgoT(), "MaintenanceAllowed", mock.Anything)
			hostManager.AssertNotCalled(GinkgoT(), "ApplyDeviceNvSpec", mock.Anything, mock.Anything)
			maintenanceManager.AssertNotCalled(GinkgoT(), "Reboot")
			maintenanceManager.AssertNotCalled(GinkgoT(), "ReleaseMaintenance", mock.Anything)
			hostManager.AssertExpectations(GinkgoT())
			maintenanceManager.AssertExpectations(GinkgoT())
		})

		It("Should not fail on an not applied nv config when reboot hasn't happened yet", func() {
			hostManager.On("ValidateDeviceNvSpec", mock.Anything, mock.Anything).Return(false, true, nil)
			hostUtils.On("GetHostUptimeSeconds").Return(time.Second*1000, nil)

			_ = createDevice(false, &metav1.Condition{
				Type:    consts.ConfigUpdateInProgressCondition,
				Status:  metav1.ConditionTrue,
				Reason:  consts.PendingRebootReason,
				Message: "",
			})

			Consistently(getDeviceConditions, timeout).ShouldNot(testutils.MatchCondition(metav1.Condition{
				Type:    consts.ConfigUpdateInProgressCondition,
				Status:  metav1.ConditionFalse,
				Reason:  consts.FirmwareError,
				Message: consts.FwConfigNotAppliedAfterRebootErrorMsg,
			}))

			hostManager.AssertNotCalled(GinkgoT(), "ApplyDeviceRuntimeSpec", mock.Anything)
			hostManager.AssertNotCalled(GinkgoT(), "ApplyDeviceNvSpec", mock.Anything, mock.Anything)
			maintenanceManager.AssertNotCalled(GinkgoT(), "ReleaseMaintenance", mock.Anything)
			hostManager.AssertExpectations(GinkgoT())
			maintenanceManager.AssertExpectations(GinkgoT())
		})
	})
	Describe("reconcile multiple devices without firmware specs", func() {
		var (
			secondDeviceName = "second-test-device"

			createDevices = func() {
				device := &v1alpha1.NicDevice{ObjectMeta: metav1.ObjectMeta{Name: deviceName, Namespace: namespaceName}, Spec: v1alpha1.NicDeviceSpec{Configuration: &v1alpha1.NicDeviceConfigurationSpec{ResetToDefault: true}}}
				Expect(k8sClient.Create(ctx, device))
				device.Status = v1alpha1.NicDeviceStatus{
					Node: nodeName,
					Ports: []v1alpha1.NicDevicePortSpec{{
						PCI: "0000:3b:00.0",
					}},
				}
				Expect(k8sClient.Status().Update(ctx, device)).To(Succeed())

				device = &v1alpha1.NicDevice{ObjectMeta: metav1.ObjectMeta{Name: secondDeviceName, Namespace: namespaceName}, Spec: v1alpha1.NicDeviceSpec{Configuration: &v1alpha1.NicDeviceConfigurationSpec{ResetToDefault: true}}}
				Expect(k8sClient.Create(ctx, device))
				device.Status = v1alpha1.NicDeviceStatus{
					Node: nodeName,
					Ports: []v1alpha1.NicDevicePortSpec{{
						PCI: "0000:d8:00.0",
					}},
				}
				Expect(k8sClient.Status().Update(ctx, device)).To(Succeed())
			}

			matchFirstDevice = mock.MatchedBy(func(input *v1alpha1.NicDevice) bool {
				return input.Name == deviceName
			})

			matchSecondDevice = mock.MatchedBy(func(input *v1alpha1.NicDevice) bool {
				return input.Name == secondDeviceName
			})
		)

		It("Should not begin maintenance and apply spec for device if spec validation failed for other device", func() {
			hostManager.On("ValidateDeviceNvSpec", mock.Anything, matchSecondDevice).Return(true, true, nil)
			hostManager.On("ValidateDeviceNvSpec", mock.Anything, matchFirstDevice).Return(false, false, errors.New(specValidationFailed))

			createDevices()

			Eventually(getDeviceConditions, timeout).Should(testutils.MatchCondition(metav1.Condition{
				Type:    consts.ConfigUpdateInProgressCondition,
				Status:  metav1.ConditionFalse,
				Reason:  consts.SpecValidationFailed,
				Message: specValidationFailed,
			}))

			Eventually(func() []metav1.Condition {
				device := &v1alpha1.NicDevice{}
				Expect(k8sClient.Get(ctx, k8sTypes.NamespacedName{Name: secondDeviceName, Namespace: namespaceName}, device)).To(Succeed())
				return device.Status.Conditions
			}, timeout).Should(testutils.MatchCondition(metav1.Condition{
				Type:   consts.ConfigUpdateInProgressCondition,
				Status: metav1.ConditionTrue,
				Reason: consts.UpdateStartedReason,
			}))

			maintenanceManager.AssertNotCalled(GinkgoT(), "ScheduleMaintenance", mock.Anything)
			maintenanceManager.AssertExpectations(GinkgoT())
		})

		It("Should not apply runtime spec for device if spec validation failed for other device", func() {
			hostManager.On("ValidateDeviceNvSpec", mock.Anything, matchSecondDevice).Return(false, false, nil)
			hostManager.On("ValidateDeviceNvSpec", mock.Anything, matchFirstDevice).Return(false, false, errors.New(specValidationFailed))

			createDevices()

			Eventually(getDeviceConditions, timeout).Should(testutils.MatchCondition(metav1.Condition{
				Type:    consts.ConfigUpdateInProgressCondition,
				Status:  metav1.ConditionFalse,
				Reason:  consts.SpecValidationFailed,
				Message: specValidationFailed,
			}))

			Consistently(func() []metav1.Condition {
				device := &v1alpha1.NicDevice{}
				Expect(k8sClient.Get(ctx, k8sTypes.NamespacedName{Name: secondDeviceName, Namespace: namespaceName}, device)).To(Succeed())
				return device.Status.Conditions
			}).ShouldNot(testutils.MatchCondition(metav1.Condition{Type: consts.ConfigUpdateInProgressCondition})) // No operation are performed on this device, no reason to change status

			hostManager.AssertNotCalled(GinkgoT(), "ApplyDeviceRuntimeSpec", mock.Anything)
			maintenanceManager.AssertExpectations(GinkgoT())
		})

		It("Should not apply runtime spec for device if nv spec apply needed for other device", func() {
			hostManager.On("ValidateDeviceNvSpec", mock.Anything, matchSecondDevice).Return(false, false, nil)
			hostManager.On("ValidateDeviceNvSpec", mock.Anything, matchFirstDevice).Return(true, true, nil)
			maintenanceManager.On("ScheduleMaintenance", mock.Anything).Return(nil)
			maintenanceManager.On("MaintenanceAllowed", mock.Anything).Return(true, nil)
			hostManager.On("ApplyDeviceNvSpec", mock.Anything, matchFirstDevice).Return(true, nil)
			maintenanceManager.On("Reboot").Return(nil)

			createDevices()

			Eventually(getDeviceConditions, time.Minute*2).Should(testutils.MatchCondition(metav1.Condition{
				Type:   consts.ConfigUpdateInProgressCondition,
				Status: metav1.ConditionTrue,
				Reason: consts.PendingRebootReason,
			}))

			Consistently(func() []metav1.Condition {
				device := &v1alpha1.NicDevice{}
				Expect(k8sClient.Get(ctx, k8sTypes.NamespacedName{Name: secondDeviceName, Namespace: namespaceName}, device)).To(Succeed())
				return device.Status.Conditions
			}).ShouldNot(testutils.MatchCondition(metav1.Condition{Type: consts.ConfigUpdateInProgressCondition})) // No operation are performed on this device, no reason to change status

			hostManager.AssertNotCalled(GinkgoT(), "ApplyDeviceRuntimeSpec", mock.Anything)
			maintenanceManager.AssertExpectations(GinkgoT())
		})
	})

	Describe("reconcile a single device with both firmware and configuration spec", func() {
		var oldFwVersion = "22.44.11"
		var newFwVersion = "33.22.11"

		var createDevice = func(fwVersion string, policy string) *v1alpha1.NicDevice {
			device := &v1alpha1.NicDevice{
				ObjectMeta: metav1.ObjectMeta{Name: deviceName, Namespace: namespaceName},
				Spec: v1alpha1.NicDeviceSpec{
					Firmware: &v1alpha1.FirmwareTemplateSpec{
						NicFirmwareSourceRef: "some-fw-source-ref",
						UpdatePolicy:         policy,
					},
					Configuration: &v1alpha1.NicDeviceConfigurationSpec{
						Template: &v1alpha1.ConfigurationTemplateSpec{
							NumVfs:   4,
							LinkType: consts.Ethernet,
						},
					},
				},
			}

			Expect(k8sClient.Create(ctx, device))
			device.Status = v1alpha1.NicDeviceStatus{
				Node: nodeName,
				Ports: []v1alpha1.NicDevicePortSpec{{
					PCI: "0000:3b:00.0",
				}},
				FirmwareVersion: fwVersion,
			}
			Expect(k8sClient.Status().Update(ctx, device)).To(Succeed())

			return device
		}

		It("Should fail validation if firmware source is not ready", func() {
			err := types.FirmwareSourceNotReadyError("source-name", "source not ready!")
			firmwareManager.On("ValidateRequestedFirmwareSource", mock.Anything, mock.Anything).Return("", err)
			maintenanceManager.On("ReleaseMaintenance", mock.Anything).Return(nil)
			maintenanceManager.AssertNotCalled(GinkgoT(), "ScheduleMaintenance", mock.Anything)

			createDevice(oldFwVersion, consts.FirmwareUpdatePolicyUpdate)

			Eventually(getDeviceConditions, time.Second*10).Should(testutils.MatchCondition(metav1.Condition{
				Type:    consts.FirmwareUpdateInProgressCondition,
				Status:  metav1.ConditionFalse,
				Reason:  consts.FirmwareSourceNotReadyReason,
				Message: err.Error(),
			}))

			Consistently(getDeviceConditions, time.Second).ShouldNot(testutils.MatchCondition(metav1.Condition{Type: consts.FirmwareUpdateInProgressCondition, Status: metav1.ConditionTrue}))

			maintenanceManager.AssertExpectations(GinkgoT())
		})

		It("Should fail validation if firmware source failed", func() {
			err := errors.New("source failed")
			firmwareManager.On("ValidateRequestedFirmwareSource", mock.Anything, mock.Anything).Return("", err)
			maintenanceManager.AssertNotCalled(GinkgoT(), "ScheduleMaintenance", mock.Anything)

			createDevice(oldFwVersion, consts.FirmwareUpdatePolicyUpdate)

			Eventually(getDeviceConditions, time.Second*10).Should(testutils.MatchCondition(metav1.Condition{
				Type:    consts.FirmwareUpdateInProgressCondition,
				Status:  metav1.ConditionFalse,
				Reason:  consts.FirmwareSourceFailedReason,
				Message: err.Error(),
			}))

			Consistently(getDeviceConditions, time.Second).ShouldNot(testutils.MatchCondition(metav1.Condition{Type: consts.FirmwareUpdateInProgressCondition, Status: metav1.ConditionTrue}))

			maintenanceManager.AssertExpectations(GinkgoT())
		})

		It("Should set successful status if fw update is not required", func() {
			firmwareManager.On("ValidateRequestedFirmwareSource", mock.Anything, mock.Anything).Return(newFwVersion, nil)
			maintenanceManager.AssertNotCalled(GinkgoT(), "ScheduleMaintenance", mock.Anything)
			maintenanceManager.On("ReleaseMaintenance", mock.Anything).Return(nil)
			hostManager.On("ValidateDeviceNvSpec", mock.Anything, mock.Anything).Return(false, false, nil)
			hostManager.On("ApplyDeviceRuntimeSpec", mock.Anything).Return(nil)

			createDevice(newFwVersion, consts.FirmwareUpdatePolicyUpdate)

			Eventually(getDeviceConditions, time.Second*10).Should(testutils.MatchCondition(metav1.Condition{
				Type:    consts.FirmwareUpdateInProgressCondition,
				Status:  metav1.ConditionFalse,
				Reason:  consts.DeviceFwMatchReason,
				Message: consts.DeviceFwMatchMessage,
			}))

			Consistently(getDeviceConditions, time.Second).ShouldNot(testutils.MatchCondition(metav1.Condition{Type: consts.FirmwareUpdateInProgressCondition, Status: metav1.ConditionTrue}))

			maintenanceManager.AssertExpectations(GinkgoT())
		})

		It("Should not start firmware update if maintenance is not allowed", func() {
			err := errors.New("maintenance not allowed")
			firmwareManager.On("ValidateRequestedFirmwareSource", mock.Anything, mock.Anything).Return(newFwVersion, nil)
			maintenanceManager.On("ScheduleMaintenance", mock.Anything).Return(err)
			maintenanceManager.AssertNotCalled(GinkgoT(), "ReleaseMaintenance", mock.Anything)
			firmwareManager.AssertNotCalled(GinkgoT(), "BurnNicFirmware", mock.Anything, mock.Anything, mock.Anything)

			createDevice(oldFwVersion, consts.FirmwareUpdatePolicyUpdate)

			Eventually(getDeviceConditions, time.Second*10).Should(testutils.MatchCondition(metav1.Condition{
				Type:   consts.FirmwareUpdateInProgressCondition,
				Status: metav1.ConditionTrue,
				Reason: consts.PendingNodeMaintenanceReason,
			}))

			Consistently(getDeviceConditions, time.Second).Should(testutils.MatchCondition(metav1.Condition{
				Type:    consts.ConfigUpdateInProgressCondition,
				Status:  metav1.ConditionFalse,
				Reason:  consts.PendingFirmwareUpdateReason,
				Message: consts.PendingOwnFirmwareUpdateMessage,
			}))

			firmwareManager.AssertExpectations(GinkgoT())
			maintenanceManager.AssertExpectations(GinkgoT())
		})

		It("Should set the version mismatch status if policy is set to validate", func() {
			firmwareManager.On("ValidateRequestedFirmwareSource", mock.Anything, mock.Anything).Return(newFwVersion, nil)
			maintenanceManager.AssertNotCalled(GinkgoT(), "ScheduleMaintenance", mock.Anything)
			maintenanceManager.AssertNotCalled(GinkgoT(), "ReleaseMaintenance", mock.Anything)
			firmwareManager.AssertNotCalled(GinkgoT(), "BurnNicFirmware", mock.Anything, mock.Anything, mock.Anything)

			createDevice(oldFwVersion, consts.FirmwareUpdatePolicyValidate)

			Eventually(getDeviceConditions, time.Second*10).Should(testutils.MatchCondition(metav1.Condition{
				Type:    consts.FirmwareUpdateInProgressCondition,
				Status:  metav1.ConditionFalse,
				Reason:  consts.DeviceFwMismatchReason,
				Message: consts.DeviceFwMismatchMessage,
			}))

			Consistently(getDeviceConditions, time.Second).Should(testutils.MatchCondition(metav1.Condition{
				Type:    consts.ConfigUpdateInProgressCondition,
				Status:  metav1.ConditionFalse,
				Reason:  consts.PendingFirmwareUpdateReason,
				Message: consts.PendingOwnFirmwareUpdateMessage,
			}))

			firmwareManager.AssertExpectations(GinkgoT())
			maintenanceManager.AssertExpectations(GinkgoT())
		})

		It("Should set the error status if fw update has failed", func() {
			err := errors.New("fw update failed")
			firmwareManager.On("ValidateRequestedFirmwareSource", mock.Anything, mock.Anything).Return(newFwVersion, nil)
			maintenanceManager.On("ScheduleMaintenance", mock.Anything).Return(nil)
			maintenanceManager.On("MaintenanceAllowed", mock.Anything).Return(true, nil)
			maintenanceManager.AssertNotCalled(GinkgoT(), "ReleaseMaintenance", mock.Anything)
			firmwareManager.On("BurnNicFirmware", mock.Anything, mock.Anything, mock.Anything).After(1 * time.Second).Return(err)

			createDevice(oldFwVersion, consts.FirmwareUpdatePolicyUpdate)

			Eventually(getDeviceConditions, time.Second*10).Should(testutils.MatchCondition(metav1.Condition{
				Type:   consts.FirmwareUpdateInProgressCondition,
				Status: metav1.ConditionTrue,
				Reason: consts.FirmwareUpdateStartedReason,
			}))

			Eventually(getDeviceConditions, time.Second*10).Should(testutils.MatchCondition(metav1.Condition{
				Type:    consts.FirmwareUpdateInProgressCondition,
				Status:  metav1.ConditionFalse,
				Reason:  consts.FirmwareUpdateFailedReason,
				Message: err.Error(),
			}))

			Consistently(getDeviceConditions, time.Second).Should(testutils.MatchCondition(metav1.Condition{
				Type:    consts.ConfigUpdateInProgressCondition,
				Status:  metav1.ConditionFalse,
				Reason:  consts.PendingFirmwareUpdateReason,
				Message: consts.PendingOwnFirmwareUpdateMessage,
			}))

			firmwareManager.AssertExpectations(GinkgoT())
			maintenanceManager.AssertExpectations(GinkgoT())
		})

		It("Should reboot if required after the fw update", func() {
			firmwareManager.On("ValidateRequestedFirmwareSource", mock.Anything, mock.Anything).After(time.Second).Return(newFwVersion, nil)
			maintenanceManager.On("ScheduleMaintenance", mock.Anything).Return(nil)
			maintenanceManager.On("MaintenanceAllowed", mock.Anything).Return(true, nil)
			maintenanceManager.On("ReleaseMaintenance", mock.Anything)
			firmwareManager.On("BurnNicFirmware", mock.Anything, mock.Anything, mock.Anything).Return(nil)
			hostManager.On("ValidateDeviceNvSpec", mock.Anything, mock.Anything).Return(false, false, nil)
			hostManager.On("ApplyDeviceRuntimeSpec", mock.Anything).Return(nil)
			hostManager.On("ResetNicFirmware", mock.Anything, mock.Anything).Return(true, nil)
			maintenanceManager.On("Reboot", mock.Anything).Return(nil)

			createDevice(oldFwVersion, consts.FirmwareUpdatePolicyUpdate)

			Eventually(getDeviceConditions, time.Second*10).Should(testutils.MatchCondition(metav1.Condition{
				Type:    consts.FirmwareUpdateInProgressCondition,
				Status:  metav1.ConditionTrue,
				Reason:  consts.PendingRebootReason,
				Message: "",
			}))
		})

		It("Should set the success status after fw update", func() {
			firmwareManager.On("ValidateRequestedFirmwareSource", mock.Anything, mock.Anything).Return(newFwVersion, nil)
			maintenanceManager.On("ScheduleMaintenance", mock.Anything).Return(nil)
			maintenanceManager.On("MaintenanceAllowed", mock.Anything).Return(true, nil)
			maintenanceManager.On("ReleaseMaintenance", mock.Anything)
			firmwareManager.On("BurnNicFirmware", mock.Anything, mock.Anything, mock.Anything).Return(nil)
			hostManager.On("ValidateDeviceNvSpec", mock.Anything, mock.Anything).Return(false, false, nil)
			hostManager.On("ApplyDeviceRuntimeSpec", mock.Anything).Return(nil)
			hostManager.On("ResetNicFirmware", mock.Anything, mock.Anything).Return(false, nil)

			createDevice(oldFwVersion, consts.FirmwareUpdatePolicyUpdate)

			Eventually(getDeviceConditions, time.Second*10).Should(testutils.MatchCondition(metav1.Condition{
				Type:    consts.FirmwareUpdateInProgressCondition,
				Status:  metav1.ConditionFalse,
				Reason:  consts.DeviceFwMatchReason,
				Message: consts.DeviceFwMatchMessage,
			}))
		})
	})

	Describe("reconcile two devices with both firmware and configuration spec", func() {
		var (
			oldFwVersion     = "22.44.11"
			newFwVersion     = "33.22.11"
			secondDeviceName = "second-test-device"
			matchFirstDevice = mock.MatchedBy(func(input *v1alpha1.NicDevice) bool {
				return input.Name == deviceName
			})

			matchSecondDevice = mock.MatchedBy(func(input *v1alpha1.NicDevice) bool {
				return input.Name == secondDeviceName
			})
		)

		var createDevices = func(secondPolicy string) {
			device := &v1alpha1.NicDevice{
				ObjectMeta: metav1.ObjectMeta{Name: deviceName, Namespace: namespaceName},
				Spec: v1alpha1.NicDeviceSpec{
					Firmware: &v1alpha1.FirmwareTemplateSpec{
						NicFirmwareSourceRef: "some-fw-source-ref",
						UpdatePolicy:         consts.FirmwareUpdatePolicyUpdate,
					},
					Configuration: &v1alpha1.NicDeviceConfigurationSpec{
						Template: &v1alpha1.ConfigurationTemplateSpec{
							NumVfs:   4,
							LinkType: consts.Ethernet,
						},
					},
				},
			}

			device2 := &v1alpha1.NicDevice{
				ObjectMeta: metav1.ObjectMeta{Name: secondDeviceName, Namespace: namespaceName},
				Spec: v1alpha1.NicDeviceSpec{
					Firmware: &v1alpha1.FirmwareTemplateSpec{
						NicFirmwareSourceRef: "some-fw-source-ref",
						UpdatePolicy:         secondPolicy,
					},
					Configuration: &v1alpha1.NicDeviceConfigurationSpec{
						Template: &v1alpha1.ConfigurationTemplateSpec{
							NumVfs:   4,
							LinkType: consts.Ethernet,
						},
					},
				},
			}

			Expect(k8sClient.Create(ctx, device2))
			Expect(k8sClient.Create(ctx, device))

			device.Status = v1alpha1.NicDeviceStatus{
				Node: nodeName,
				Ports: []v1alpha1.NicDevicePortSpec{{
					PCI: "0000:3b:00.0",
				}},
				FirmwareVersion: oldFwVersion,
			}

			device2.Status = v1alpha1.NicDeviceStatus{
				Node: nodeName,
				Ports: []v1alpha1.NicDevicePortSpec{{
					PCI: "0000:03:00.0",
				}},
				FirmwareVersion: oldFwVersion,
			}

			Expect(k8sClient.Status().Update(ctx, device2)).To(Succeed())
			Expect(k8sClient.Status().Update(ctx, device)).To(Succeed())
		}

		It("Should not stop firmware update on one device if firmware version doesn't match on the second device and policy is to Validate", func() {
			firmwareManager.On("ValidateRequestedFirmwareSource", mock.Anything, mock.Anything).Return(newFwVersion, nil)
			maintenanceManager.On("ScheduleMaintenance", mock.Anything).Return(nil)
			maintenanceManager.On("MaintenanceAllowed", mock.Anything).Return(true, nil)
			maintenanceManager.On("ReleaseMaintenance", mock.Anything).Return(nil)
			firmwareManager.On("BurnNicFirmware", mock.Anything, mock.Anything, mock.Anything).Return(nil)
			hostManager.On("ResetNicFirmware", mock.Anything, mock.Anything).Return(false, nil)
			hostManager.AssertNotCalled(GinkgoT(), "ValidateDeviceNvSpec", mock.Anything, mock.Anything)
			hostManager.AssertNotCalled(GinkgoT(), "ApplyDeviceRuntimeSpec", mock.Anything)

			createDevices(consts.FirmwareUpdatePolicyValidate)

			Eventually(getDeviceConditions, time.Second*10).Should(testutils.MatchCondition(metav1.Condition{
				Type:    consts.FirmwareUpdateInProgressCondition,
				Status:  metav1.ConditionFalse,
				Reason:  consts.DeviceFwMatchReason,
				Message: consts.DeviceFwMatchMessage,
			}))

			Eventually(func() []metav1.Condition {
				device := &v1alpha1.NicDevice{}
				Expect(k8sClient.Get(ctx, k8sTypes.NamespacedName{Name: secondDeviceName, Namespace: namespaceName}, device)).To(Succeed())
				return device.Status.Conditions
			}).Should(testutils.MatchCondition(metav1.Condition{
				Type:    consts.FirmwareUpdateInProgressCondition,
				Status:  metav1.ConditionFalse,
				Reason:  consts.DeviceFwMismatchReason,
				Message: consts.DeviceFwMismatchMessage,
			}))

			Eventually(func() []metav1.Condition {
				device := &v1alpha1.NicDevice{}
				Expect(k8sClient.Get(ctx, k8sTypes.NamespacedName{Name: secondDeviceName, Namespace: namespaceName}, device)).To(Succeed())
				return device.Status.Conditions
			}, time.Second).Should(testutils.MatchCondition(metav1.Condition{
				Type:    consts.ConfigUpdateInProgressCondition,
				Status:  metav1.ConditionFalse,
				Reason:  consts.PendingFirmwareUpdateReason,
				Message: consts.PendingOwnFirmwareUpdateMessage,
			}))

			Consistently(getDeviceConditions, time.Second).Should(testutils.MatchCondition(metav1.Condition{
				Type:    consts.ConfigUpdateInProgressCondition,
				Status:  metav1.ConditionFalse,
				Reason:  consts.PendingFirmwareUpdateReason,
				Message: consts.PendingOtherFirmwareUpdateMessage,
			}))

			hostManager.AssertExpectations(GinkgoT())
		})

		It("Should stop configuration update on one device if firmware update failed for another one", func() {
			err := errors.New("fw update failed")
			firmwareManager.On("ValidateRequestedFirmwareSource", mock.Anything, mock.Anything).Return(newFwVersion, nil)
			maintenanceManager.On("ScheduleMaintenance", mock.Anything).Return(nil)
			maintenanceManager.On("MaintenanceAllowed", mock.Anything).Return(true, nil)
			maintenanceManager.AssertNotCalled(GinkgoT(), "ReleaseMaintenance", mock.Anything)
			firmwareManager.On("BurnNicFirmware", mock.Anything, matchFirstDevice, mock.Anything).Return(nil)
			firmwareManager.On("BurnNicFirmware", mock.Anything, matchSecondDevice, mock.Anything).Return(err)
			hostManager.On("ResetNicFirmware", mock.Anything, mock.Anything).Return(false, nil)
			hostManager.AssertNotCalled(GinkgoT(), "ValidateDeviceNvSpec", mock.Anything, mock.Anything)
			hostManager.AssertNotCalled(GinkgoT(), "ApplyDeviceRuntimeSpec", mock.Anything)

			createDevices(consts.FirmwareUpdatePolicyUpdate)

			Eventually(getDeviceConditions, time.Second*10).Should(testutils.MatchCondition(metav1.Condition{
				Type:    consts.FirmwareUpdateInProgressCondition,
				Status:  metav1.ConditionFalse,
				Reason:  consts.DeviceFwMatchReason,
				Message: consts.DeviceFwMatchMessage,
			}))

			Eventually(func() []metav1.Condition {
				device := &v1alpha1.NicDevice{}
				Expect(k8sClient.Get(ctx, k8sTypes.NamespacedName{Name: secondDeviceName, Namespace: namespaceName}, device)).To(Succeed())
				return device.Status.Conditions
			}, time.Second*10).Should(testutils.MatchCondition(metav1.Condition{
				Type:    consts.FirmwareUpdateInProgressCondition,
				Status:  metav1.ConditionFalse,
				Reason:  consts.FirmwareUpdateFailedReason,
				Message: err.Error(),
			}))

			Eventually(getDeviceConditions, time.Second).Should(testutils.MatchCondition(metav1.Condition{
				Type:    consts.ConfigUpdateInProgressCondition,
				Status:  metav1.ConditionFalse,
				Reason:  consts.PendingFirmwareUpdateReason,
				Message: consts.PendingOtherFirmwareUpdateMessage,
			}))

			Consistently(func() []metav1.Condition {
				device := &v1alpha1.NicDevice{}
				Expect(k8sClient.Get(ctx, k8sTypes.NamespacedName{Name: secondDeviceName, Namespace: namespaceName}, device)).To(Succeed())
				return device.Status.Conditions
			}, time.Second).Should(testutils.MatchCondition(metav1.Condition{
				Type:    consts.ConfigUpdateInProgressCondition,
				Status:  metav1.ConditionFalse,
				Reason:  consts.PendingFirmwareUpdateReason,
				Message: consts.PendingOwnFirmwareUpdateMessage,
			}))

			hostManager.AssertExpectations(GinkgoT())
		})

		It("Should not stop configuration update if all devices have up-to-date firmware", func() {
			firmwareManager.On("ValidateRequestedFirmwareSource", mock.Anything, mock.Anything).Return(newFwVersion, nil)
			maintenanceManager.On("ScheduleMaintenance", mock.Anything).Return(nil)
			maintenanceManager.On("MaintenanceAllowed", mock.Anything).Return(true, nil)
			maintenanceManager.On("ReleaseMaintenance", mock.Anything).Return(nil)
			firmwareManager.On("BurnNicFirmware", mock.Anything, mock.Anything, mock.Anything).Return(nil)
			hostManager.On("ResetNicFirmware", mock.Anything, mock.Anything).Return(false, nil)
			hostManager.On("ValidateDeviceNvSpec", mock.Anything, mock.Anything).Return(false, false, nil)
			hostManager.On("ApplyDeviceRuntimeSpec", mock.Anything).Return(nil)

			createDevices(consts.FirmwareUpdatePolicyUpdate)

			Eventually(getDeviceConditions, time.Second*10).Should(testutils.MatchCondition(metav1.Condition{
				Type:    consts.FirmwareUpdateInProgressCondition,
				Status:  metav1.ConditionFalse,
				Reason:  consts.DeviceFwMatchReason,
				Message: consts.DeviceFwMatchMessage,
			}))

			Eventually(func() []metav1.Condition {
				device := &v1alpha1.NicDevice{}
				Expect(k8sClient.Get(ctx, k8sTypes.NamespacedName{Name: secondDeviceName, Namespace: namespaceName}, device)).To(Succeed())
				return device.Status.Conditions
			}, time.Second*10).Should(testutils.MatchCondition(metav1.Condition{
				Type:    consts.FirmwareUpdateInProgressCondition,
				Status:  metav1.ConditionFalse,
				Reason:  consts.DeviceFwMatchReason,
				Message: consts.DeviceFwMatchMessage,
			}))

			Eventually(getDeviceConditions, time.Second*10).Should(testutils.MatchCondition(metav1.Condition{
				Type:    consts.ConfigUpdateInProgressCondition,
				Status:  metav1.ConditionFalse,
				Reason:  consts.UpdateSuccessfulReason,
				Message: "",
			}))

			Consistently(func() []metav1.Condition {
				device := &v1alpha1.NicDevice{}
				Expect(k8sClient.Get(ctx, k8sTypes.NamespacedName{Name: secondDeviceName, Namespace: namespaceName}, device)).To(Succeed())
				return device.Status.Conditions
			}, time.Second).Should(testutils.MatchCondition(metav1.Condition{
				Type:    consts.ConfigUpdateInProgressCondition,
				Status:  metav1.ConditionFalse,
				Reason:  consts.UpdateSuccessfulReason,
				Message: "",
			}))
		})
	})
})
