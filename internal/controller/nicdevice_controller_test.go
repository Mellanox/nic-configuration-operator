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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sTypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/json"
	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
	hostMocks "github.com/Mellanox/nic-configuration-operator/pkg/host/mocks"
	maintenanceMocks "github.com/Mellanox/nic-configuration-operator/pkg/maintenance/mocks"
	"github.com/Mellanox/nic-configuration-operator/pkg/testutils"
	"github.com/Mellanox/nic-configuration-operator/pkg/types"
)

const specValidationFailed = "spec validation failed"

var _ = Describe("NicDeviceReconciler", func() {
	var (
		mgr                manager.Manager
		reconciler         *NicDeviceReconciler
		hostManager        *hostMocks.HostManager
		maintenanceManager *maintenanceMocks.MaintenanceManager
		nodeName           = "test-node"
		deviceName         = "test-device"
		ctx                context.Context
		cancel             context.CancelFunc
		timeout            = time.Second * 10
		namespaceName      string
		wg                 sync.WaitGroup
		err                error
		startManager       = func() {
			wg = sync.WaitGroup{}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer GinkgoRecover()
				Expect(mgr.Start(ctx)).To(Succeed())
			}()
			//time.Sleep(1 * time.Second)
		}
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.TODO())
		mgr, err = ctrl.NewManager(cfg, ctrl.Options{
			Scheme:     k8sClient.Scheme(),
			Metrics:    metricsserver.Options{BindAddress: "0"},
			Controller: config.Controller{SkipNameValidation: ptr.To(true)},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(mgr).NotTo(BeNil())

		node := &v1.Node{ObjectMeta: metav1.ObjectMeta{Name: nodeName}}
		Expect(k8sClient.Create(ctx, node)).To(Succeed())

		namespaceName = "nic-configuration-operator-" + rand.String(6)
		ns := &v1.Namespace{ObjectMeta: metav1.ObjectMeta{
			Name: namespaceName,
		}}
		Expect(k8sClient.Create(context.Background(), ns)).To(Succeed())

		err = mgr.GetCache().IndexField(context.Background(), &v1alpha1.NicDevice{}, "status.node", func(o client.Object) []string {
			return []string{o.(*v1alpha1.NicDevice).Status.Node}
		})
		Expect(err).NotTo(HaveOccurred())

		deviceDiscoveryReconcileTime = 1 * time.Second
		hostManager = &hostMocks.HostManager{}
		maintenanceManager = &maintenanceMocks.MaintenanceManager{}

		reconciler = &NicDeviceReconciler{
			Client:             mgr.GetClient(),
			Scheme:             mgr.GetScheme(),
			NodeName:           nodeName,
			NamespaceName:      namespaceName,
			HostManager:        hostManager,
			MaintenanceManager: maintenanceManager,
		}
		Expect(reconciler.SetupWithManager(mgr, false)).To(Succeed())
	})

	AfterEach(func() {
		Expect(k8sClient.DeleteAllOf(ctx, &v1alpha1.NicDevice{}, client.InNamespace(namespaceName))).To(Succeed())
		Expect(k8sClient.Delete(ctx, &v1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespaceName}})).To(Succeed())
		Expect(k8sClient.DeleteAllOf(ctx, &v1.Node{})).To(Succeed())
		cancel()
		wg.Wait()
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
			Expect(k8sClient.Get(ctx, k8sTypes.NamespacedName{Name: deviceName, Namespace: namespaceName}, device)).To(Succeed())

			err := reconciler.updateDeviceStatusCondition(ctx, device, "TestReason", metav1.ConditionTrue, "test-message")
			Expect(err).NotTo(HaveOccurred())

			Eventually(func() []metav1.Condition {
				device := &v1alpha1.NicDevice{}
				Expect(k8sClient.Get(ctx, k8sTypes.NamespacedName{Name: deviceName, Namespace: namespaceName}, device)).To(Succeed())
				return device.Status.Conditions
			}, timeout).Should(testutils.MatchCondition(metav1.Condition{
				Type:    consts.ConfigUpdateInProgressCondition,
				Status:  metav1.ConditionTrue,
				Reason:  "TestReason",
				Message: "test-message",
			}))

			Expect(k8sClient.Get(ctx, k8sTypes.NamespacedName{Name: deviceName, Namespace: namespaceName}, device)).To(Succeed())
			err = reconciler.updateDeviceStatusCondition(ctx, device, "AnotherTestReason", metav1.ConditionFalse, "")
			Expect(err).NotTo(HaveOccurred())

			Eventually(func() []metav1.Condition {
				device := &v1alpha1.NicDevice{}
				Expect(k8sClient.Get(ctx, k8sTypes.NamespacedName{Name: deviceName, Namespace: namespaceName}, device)).To(Succeed())
				return device.Status.Conditions
			}, timeout).Should(testutils.MatchCondition(metav1.Condition{
				Type:    consts.ConfigUpdateInProgressCondition,
				Status:  metav1.ConditionFalse,
				Reason:  "AnotherTestReason",
				Message: "",
			}))
		})
	})

	Describe("reconcile a single device", func() {
		var createDevice = func(setLastSpecAnnotation bool) {
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
							RawNvConfig: []v1alpha1.NvConfigParam{{
								Name:  "CUSTOM_PARAM",
								Value: "true",
							}},
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
			Expect(k8sClient.Status().Update(ctx, device)).To(Succeed())
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

			startManager()

			Consistently(func() []metav1.Condition {
				maintenanceManager.AssertNotCalled(GinkgoT(), "ReleaseMaintenance", mock.Anything)
				device := &v1alpha1.NicDevice{}
				Expect(k8sClient.Get(ctx, k8sTypes.NamespacedName{Name: "another-name", Namespace: namespaceName}, device)).To(Succeed())
				return device.Status.Conditions
			}).Should(BeNil())
		})
		It("Should result in SpecValidationFailed status if spec validation failed", func() {
			hostManager.On("ValidateDeviceNvSpec", mock.Anything, mock.Anything).Return(false, false, errors.New(specValidationFailed))

			createDevice(false)
			startManager()

			Eventually(func() []metav1.Condition {
				device := &v1alpha1.NicDevice{}
				Expect(k8sClient.Get(ctx, k8sTypes.NamespacedName{Name: deviceName, Namespace: namespaceName}, device)).To(Succeed())
				return device.Status.Conditions
			}, timeout).Should(testutils.MatchCondition(metav1.Condition{
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

			createDevice(false)
			startManager()

			Eventually(func() []metav1.Condition {
				device := &v1alpha1.NicDevice{}
				Expect(k8sClient.Get(ctx, k8sTypes.NamespacedName{Name: deviceName, Namespace: namespaceName}, device)).To(Succeed())
				return device.Status.Conditions
			}, timeout).Should(testutils.MatchCondition(metav1.Condition{
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

			createDevice(false)
			startManager()

			Eventually(func() []metav1.Condition {
				device := &v1alpha1.NicDevice{}
				Expect(k8sClient.Get(ctx, k8sTypes.NamespacedName{Name: deviceName, Namespace: namespaceName}, device)).To(Succeed())
				return device.Status.Conditions
			}, timeout).Should(testutils.MatchCondition(metav1.Condition{
				Type:   consts.ConfigUpdateInProgressCondition,
				Status: metav1.ConditionFalse,
				Reason: consts.UpdateSuccessfulReason,
			}))

			device := &v1alpha1.NicDevice{}
			Expect(k8sClient.Get(ctx, k8sTypes.NamespacedName{Name: deviceName, Namespace: namespaceName}, device)).To(Succeed())
			spec, err := json.Marshal(device.Spec)
			Expect(err).NotTo(HaveOccurred())

			// Should dump the last applied state to annotations
			Eventually(func() string {
				device := &v1alpha1.NicDevice{}
				Expect(k8sClient.Get(ctx, k8sTypes.NamespacedName{Name: deviceName, Namespace: namespaceName}, device)).To(Succeed())
				val := device.Annotations[consts.LastAppliedStateAnnotation]
				return val
			}).Should(Equal(string(spec)))

			maintenanceManager.AssertCalled(GinkgoT(), "ReleaseMaintenance", mock.Anything)
			maintenanceManager.AssertExpectations(GinkgoT())
		})
		It("Should keep in UpdateStarted status if maintenance fails to schedule", func() {
			errorText := "maintenance request failed"
			hostManager.On("ValidateDeviceNvSpec", mock.Anything, mock.Anything).Return(true, false, nil)
			maintenanceManager.On("ScheduleMaintenance", mock.Anything).Return(errors.New(errorText))

			createDevice(true)
			startManager()

			Eventually(func() []metav1.Condition {
				device := &v1alpha1.NicDevice{}
				Expect(k8sClient.Get(ctx, k8sTypes.NamespacedName{Name: deviceName, Namespace: namespaceName}, device)).To(Succeed())
				return device.Status.Conditions
			}, timeout).Should(testutils.MatchCondition(metav1.Condition{
				Type:    consts.ConfigUpdateInProgressCondition,
				Status:  metav1.ConditionTrue,
				Reason:  consts.UpdateStartedReason,
				Message: "", // Should not copy the error message from the failed maintenance request
			}))
			// Should keep this status consistently
			Consistently(func() []metav1.Condition {
				device := &v1alpha1.NicDevice{}
				Expect(k8sClient.Get(ctx, k8sTypes.NamespacedName{Name: deviceName, Namespace: namespaceName}, device)).To(Succeed())
				return device.Status.Conditions
			}).Should(testutils.MatchCondition(metav1.Condition{
				Type:   consts.ConfigUpdateInProgressCondition,
				Status: metav1.ConditionTrue,
				Reason: consts.UpdateStartedReason,
			}))
			// Should not reset last applied state annotation if maintenance was not scheduled
			Consistently(func() bool {
				device := &v1alpha1.NicDevice{}
				Expect(k8sClient.Get(ctx, k8sTypes.NamespacedName{Name: deviceName, Namespace: namespaceName}, device)).To(Succeed())
				_, found := device.Annotations[consts.LastAppliedStateAnnotation]
				return found
			}).Should(BeTrue())
		})
		It("Should keep in UpdateStarted status if maintenance is not allowed", func() {
			hostManager.On("ValidateDeviceNvSpec", mock.Anything, mock.Anything).Return(true, false, nil)
			maintenanceManager.On("ScheduleMaintenance", mock.Anything).Return(nil)
			maintenanceManager.On("MaintenanceAllowed", mock.Anything).Return(false, nil)

			createDevice(true)
			startManager()

			Eventually(func() []metav1.Condition {
				device := &v1alpha1.NicDevice{}
				Expect(k8sClient.Get(ctx, k8sTypes.NamespacedName{Name: deviceName, Namespace: namespaceName}, device)).To(Succeed())
				return device.Status.Conditions
			}, timeout).Should(testutils.MatchCondition(metav1.Condition{
				Type:   consts.ConfigUpdateInProgressCondition,
				Status: metav1.ConditionTrue,
				Reason: consts.UpdateStartedReason,
			}))
			// Should keep this status consistently
			Consistently(func() []metav1.Condition {
				device := &v1alpha1.NicDevice{}
				Expect(k8sClient.Get(ctx, k8sTypes.NamespacedName{Name: deviceName, Namespace: namespaceName}, device)).To(Succeed())
				return device.Status.Conditions
			}).Should(testutils.MatchCondition(metav1.Condition{
				Type:   consts.ConfigUpdateInProgressCondition,
				Status: metav1.ConditionTrue,
				Reason: consts.UpdateStartedReason,
			}))
			// Should not reset last applied state annotation if maintenance was not allowed
			Consistently(func() bool {
				device := &v1alpha1.NicDevice{}
				Expect(k8sClient.Get(ctx, k8sTypes.NamespacedName{Name: deviceName, Namespace: namespaceName}, device)).To(Succeed())
				_, found := device.Annotations[consts.LastAppliedStateAnnotation]
				return found
			}).Should(BeTrue())
		})
		It("Should result in NonVolatileConfigUpdateFailed status if nv config fails to apply", func() {
			errorText := "maintenance request failed"
			hostManager.On("ValidateDeviceNvSpec", mock.Anything, mock.Anything).Return(true, false, nil)
			maintenanceManager.On("ScheduleMaintenance", mock.Anything).Return(nil)
			maintenanceManager.On("MaintenanceAllowed", mock.Anything).Return(true, nil)
			hostManager.On("ApplyDeviceNvSpec", mock.Anything, mock.Anything).Return(false, errors.New(errorText))

			createDevice(false)
			startManager()

			Eventually(func() []metav1.Condition {
				device := &v1alpha1.NicDevice{}
				Expect(k8sClient.Get(ctx, k8sTypes.NamespacedName{Name: deviceName, Namespace: namespaceName}, device)).To(Succeed())
				return device.Status.Conditions
			}, timeout).Should(testutils.MatchCondition(metav1.Condition{
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

			createDevice(true)
			startManager()

			Eventually(func() []metav1.Condition {
				device := &v1alpha1.NicDevice{}
				Expect(k8sClient.Get(ctx, k8sTypes.NamespacedName{Name: deviceName, Namespace: namespaceName}, device)).To(Succeed())
				return device.Status.Conditions
			}, timeout).Should(testutils.MatchCondition(metav1.Condition{
				Type:   consts.ConfigUpdateInProgressCondition,
				Status: metav1.ConditionTrue,
				Reason: consts.PendingRebootReason,
			}))

			// Should reset last applied state annotation if tried to reboot but failed
			Eventually(func() bool {
				device := &v1alpha1.NicDevice{}
				Expect(k8sClient.Get(ctx, k8sTypes.NamespacedName{Name: deviceName, Namespace: namespaceName}, device)).To(Succeed())
				_, found := device.Annotations[consts.LastAppliedStateAnnotation]
				return found
			}).Should(BeFalse())

			maintenanceManager.AssertNotCalled(GinkgoT(), "ApplyDeviceRuntimeSpec", mock.Anything)
			maintenanceManager.AssertExpectations(GinkgoT())
		})
		It("Should not release maintenance if runtime config failed to apply", func() {
			errorText := "runtime config update failed"
			hostManager.On("ValidateDeviceNvSpec", mock.Anything, mock.Anything).Return(false, false, nil)
			hostManager.On("ApplyDeviceRuntimeSpec", mock.Anything).Return(errors.New(errorText))

			createDevice(false)
			startManager()

			Eventually(func() []metav1.Condition {
				device := &v1alpha1.NicDevice{}
				Expect(k8sClient.Get(ctx, k8sTypes.NamespacedName{Name: deviceName, Namespace: namespaceName}, device)).To(Succeed())
				return device.Status.Conditions
			}, timeout).Should(testutils.MatchCondition(metav1.Condition{
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

			createDevice(true) // lastAppliedSpec will not match the current resulting in need to reboot
			startManager()

			Eventually(func() []metav1.Condition {
				device := &v1alpha1.NicDevice{}
				Expect(k8sClient.Get(ctx, k8sTypes.NamespacedName{Name: deviceName, Namespace: namespaceName}, device)).To(Succeed())
				return device.Status.Conditions
			}, timeout).Should(testutils.MatchCondition(metav1.Condition{
				Type:   consts.ConfigUpdateInProgressCondition,
				Status: metav1.ConditionTrue,
				Reason: consts.PendingRebootReason,
			}))

			hostManager.AssertNotCalled(GinkgoT(), "ApplyDeviceNvSpec", mock.Anything, mock.Anything)
			maintenanceManager.AssertNotCalled(GinkgoT(), "ReleaseMaintenance", mock.Anything)
			hostManager.AssertExpectations(GinkgoT())
			maintenanceManager.AssertExpectations(GinkgoT())
		})
	})
	Describe("reconcile multiple devices", func() {
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
			startManager()

			Eventually(func() []metav1.Condition {
				device := &v1alpha1.NicDevice{}
				Expect(k8sClient.Get(ctx, k8sTypes.NamespacedName{Name: deviceName, Namespace: namespaceName}, device)).To(Succeed())
				return device.Status.Conditions
			}, timeout).Should(testutils.MatchCondition(metav1.Condition{
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
			startManager()

			Eventually(func() []metav1.Condition {
				device := &v1alpha1.NicDevice{}
				Expect(k8sClient.Get(ctx, k8sTypes.NamespacedName{Name: deviceName, Namespace: namespaceName}, device)).To(Succeed())
				return device.Status.Conditions
			}, timeout).Should(testutils.MatchCondition(metav1.Condition{
				Type:    consts.ConfigUpdateInProgressCondition,
				Status:  metav1.ConditionFalse,
				Reason:  consts.SpecValidationFailed,
				Message: specValidationFailed,
			}))

			Consistently(func() []metav1.Condition {
				device := &v1alpha1.NicDevice{}
				Expect(k8sClient.Get(ctx, k8sTypes.NamespacedName{Name: secondDeviceName, Namespace: namespaceName}, device)).To(Succeed())
				return device.Status.Conditions
			}).Should(BeEmpty()) // No operation are performed on this device, no reason to change status

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
			startManager()

			Eventually(func() []metav1.Condition {
				device := &v1alpha1.NicDevice{}
				Expect(k8sClient.Get(ctx, k8sTypes.NamespacedName{Name: deviceName, Namespace: namespaceName}, device)).To(Succeed())
				return device.Status.Conditions
			}, time.Minute*2).Should(testutils.MatchCondition(metav1.Condition{
				Type:   consts.ConfigUpdateInProgressCondition,
				Status: metav1.ConditionTrue,
				Reason: consts.PendingRebootReason,
			}))

			Consistently(func() []metav1.Condition {
				device := &v1alpha1.NicDevice{}
				Expect(k8sClient.Get(ctx, k8sTypes.NamespacedName{Name: secondDeviceName, Namespace: namespaceName}, device)).To(Succeed())
				return device.Status.Conditions
			}).Should(BeEmpty()) // No operation are performed on this device, no reason to change status

			hostManager.AssertNotCalled(GinkgoT(), "ApplyDeviceRuntimeSpec", mock.Anything)
			maintenanceManager.AssertExpectations(GinkgoT())
		})
	})
})
