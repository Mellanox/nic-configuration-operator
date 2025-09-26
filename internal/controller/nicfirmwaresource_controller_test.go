/*
2025 NVIDIA CORPORATION & AFFILIATES
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
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
	"github.com/Mellanox/nic-configuration-operator/pkg/firmware/mocks"
)

const (
	crName          = "nic-fw-source"
	crNamespace     = "default"
	testBFBFileName = "firmware.bfb"
)

var _ = Describe("NicFirmwareTemplate Controller", func() {
	var (
		mgr                 manager.Manager
		k8sClient           client.Client
		reconciler          *NicFirmwareSourceReconciler
		ctx                 context.Context
		cancel              context.CancelFunc
		firmwareProvisioner mocks.FirmwareProvisioner
	)

	getCR := func(name, namespace string) (*v1alpha1.NicFirmwareSource, error) {
		key := types.NamespacedName{Name: name, Namespace: namespace}
		cr := &v1alpha1.NicFirmwareSource{}
		err := k8sClient.Get(ctx, key, cr)
		return cr, err
	}

	createCR := func(name, namespace string) {
		By("creating NicFirmwareSource CR")
		log.Log.Info("creating NicFirmwareSource CR for test", "test", GinkgoT().Name())
		cr := &v1alpha1.NicFirmwareSource{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: namespace,
			},
			Spec: v1alpha1.NicFirmwareSourceSpec{
				BinUrlSources: []string{"https://firmware.example.com/fwA.zip"},
			},
		}
		Expect(k8sClient.Create(ctx, cr)).To(Succeed())
	}

	ValidateCRStatusAndReason := func(name, namespace, status, reason string) {
		Eventually(func() []string {
			cr, err := getCR(name, namespace)
			if apiErrors.IsNotFound(err) {
				return []string{}
			}
			Expect(err).NotTo(HaveOccurred())
			return []string{cr.Status.State, cr.Status.Reason}
		}, time.Second*10).Should(Equal([]string{status, reason}))
	}

	ValidateCRReportedVersions := func(name, namespace string, versions map[string][]string) {
		Eventually(func() map[string][]string {
			cr, err := getCR(name, namespace)
			Expect(err).NotTo(HaveOccurred())
			return cr.Status.BinaryVersions
		}).Should(Equal(versions))
	}

	Describe("Reconciling NicFirmwareSource", func() {
		BeforeEach(func() {
			ctx, cancel = context.WithCancel(context.Background())

			mgr = createManager()

			k8sClient = mgr.GetClient()

			firmwareProvisioner = mocks.FirmwareProvisioner{}

			firmwareProvisioner.On("DeleteCache", mock.Anything).
				Return(nil)

			reconciler = &NicFirmwareSourceReconciler{
				Client:              mgr.GetClient(),
				Scheme:              mgr.GetScheme(),
				FirmwareProvisioner: &firmwareProvisioner,
			}

			Expect(reconciler.SetupWithManager(mgr)).To(Succeed())

			list := &v1alpha1.NicFirmwareSourceList{}
			_ = k8sClient.List(ctx, list)

			By("start manager")
			wg := sync.WaitGroup{}
			startManager(mgr, ctx, &wg)

			DeferCleanup(func() {
				By("Shut down controller manager")
				cancel()
				wg.Wait()
			})
		})

		AfterEach(func() {
			Expect(k8sClient.DeleteAllOf(ctx, &v1alpha1.NicFirmwareSource{}, client.InNamespace(crNamespace))).To(Succeed())
			Eventually(func() []v1alpha1.NicFirmwareSource {
				list := &v1alpha1.NicFirmwareSourceList{}
				Expect(k8sClient.List(ctx, list)).To(Succeed())
				return list.Items
			}, time.Second*5).Should(BeEmpty())

			cancel()
		})

		It("should set the success status if the firmware provisioner did not return any errors", func() {
			versionsMap := map[string][]string{"1.2.3": {"psid1"}}

			firmwareProvisioner.On("IsFWStorageAvailable").
				Return(nil)
			firmwareProvisioner.On("VerifyCachedBinaries", crName, mock.AnythingOfType("[]string")).
				Return([]string{"http://firmware.example.com/fwA.zip"}, nil)

			firmwareProvisioner.On("DownloadAndUnzipFirmwareArchives", crName, []string{"http://firmware.example.com/fwA.zip"}, true).
				Return(nil)

			firmwareProvisioner.On("AddFirmwareBinariesToCacheByMetadata", crName).
				Return(nil)

			firmwareProvisioner.On("ValidateFirmwareBinariesCache", crName).
				Return(versionsMap, nil)

			createCR(crName, crNamespace)

			ValidateCRStatusAndReason(crName, crNamespace, consts.FirmwareSourceSuccessStatus, "")
			ValidateCRReportedVersions(crName, crNamespace, versionsMap)
		})

		It("should set the CacheVerification status if the firmware provisioner failed to verify the existing cache", func() {
			errMsg := "failed to verify cache"
			firmwareProvisioner.On("IsFWStorageAvailable").
				Return(nil)
			firmwareProvisioner.On("VerifyCachedBinaries", crName, mock.AnythingOfType("[]string")).
				Return([]string(nil), errors.New(errMsg))

			firmwareProvisioner.AssertNotCalled(GinkgoT(), "DownloadAndUnzipFirmwareArchives", crName, []string{}, true)
			firmwareProvisioner.AssertNotCalled(GinkgoT(), "AddFirmwareBinariesToCacheByMetadata", crName)
			firmwareProvisioner.AssertNotCalled(GinkgoT(), "ValidateFirmwareBinariesCache", crName)

			createCR(crName, crNamespace)

			ValidateCRStatusAndReason(crName, crNamespace, consts.FirmwareSourceCacheVerificationFailedStatus, errMsg)
		})

		It("should set the success status if no urls need to be processed after cache verification", func() {
			versionsMap := map[string][]string{"1.2.3": {"psid1"}}

			firmwareProvisioner.On("IsFWStorageAvailable").
				Return(nil)

			firmwareProvisioner.On("VerifyCachedBinaries", crName, mock.AnythingOfType("[]string")).
				Return([]string{}, nil)

			firmwareProvisioner.AssertNotCalled(GinkgoT(), "DownloadAndUnzipFirmwareArchives", crName, []string{}, true)

			firmwareProvisioner.On("AddFirmwareBinariesToCacheByMetadata", crName).
				Return(nil)

			firmwareProvisioner.On("ValidateFirmwareBinariesCache", crName).
				Return(versionsMap, nil)

			createCR(crName, crNamespace)

			ValidateCRStatusAndReason(crName, crNamespace, consts.FirmwareSourceSuccessStatus, "")
			ValidateCRReportedVersions(crName, crNamespace, versionsMap)
		})

		It("should set the Downloading status when the firmware provisioner is downloading the firmware binaries", func() {
			versionsMap := map[string][]string{"1.2.3": {"psid4"}}

			firmwareProvisioner.On("IsFWStorageAvailable").
				Return(nil)

			firmwareProvisioner.On("VerifyCachedBinaries", crName, mock.AnythingOfType("[]string")).
				Return([]string{"http://firmware.example.com/fwA.zip"}, nil)

			// Simulate a slow download so we can observe the intermediate status
			firmwareProvisioner.On("DownloadAndUnzipFirmwareArchives", crName, []string{"http://firmware.example.com/fwA.zip"}, true).
				Run(func(args mock.Arguments) {
					time.Sleep(1 * time.Second)
				}).
				Return(nil)

			firmwareProvisioner.On("AddFirmwareBinariesToCacheByMetadata", crName).
				Return(nil)

			firmwareProvisioner.On("ValidateFirmwareBinariesCache", crName).
				Return(versionsMap, nil)

			createCR(crName, crNamespace)

			// We want to see "Downloading" before it completes
			Eventually(func() string {
				cr, err := getCR(crName, crNamespace)
				if apiErrors.IsNotFound(err) {
					return ""
				}
				Expect(err).NotTo(HaveOccurred())
				return cr.Status.State
			}, 500*time.Millisecond, 100*time.Millisecond).Should(Equal(consts.FirmwareSourceDownloadingStatus))

			// Eventually it should succeed with an empty reason
			ValidateCRStatusAndReason(crName, crNamespace, consts.FirmwareSourceDownloadingStatus, "")
			ValidateCRStatusAndReason(crName, crNamespace, consts.FirmwareSourceSuccessStatus, "")
			ValidateCRReportedVersions(crName, crNamespace, versionsMap)
		})

		It("should set the DownloadFailed status if the firmware provisioner failed to download the binaries", func() {
			errMsg := "failed to download"

			firmwareProvisioner.On("IsFWStorageAvailable").
				Return(nil)

			firmwareProvisioner.On("VerifyCachedBinaries", crName, mock.AnythingOfType("[]string")).
				Return([]string{"http://firmware.example.com/fwA.zip"}, nil)

			firmwareProvisioner.On("DownloadAndUnzipFirmwareArchives", crName, []string{"http://firmware.example.com/fwA.zip"}, true).
				Return(errors.New(errMsg))

			firmwareProvisioner.AssertNotCalled(GinkgoT(), "AddFirmwareBinariesToCacheByMetadata", crName)
			firmwareProvisioner.AssertNotCalled(GinkgoT(), "ValidateFirmwareBinariesCache", crName)

			createCR(crName, crNamespace)

			ValidateCRStatusAndReason(crName, crNamespace, consts.FirmwareSourceDownloadFailedStatus, errMsg)
		})

		It("should set the Processing status if the firmware provisioner is organizing the firmware binaries", func() {
			versionsMap := map[string][]string{"15.5.3": {"psid4"}}

			firmwareProvisioner.On("IsFWStorageAvailable").
				Return(nil)

			firmwareProvisioner.On("VerifyCachedBinaries", crName, mock.AnythingOfType("[]string")).
				Return([]string{}, nil)

			firmwareProvisioner.AssertNotCalled(GinkgoT(), "DownloadAndUnzipFirmwareArchives")

			// Simulate a short delay for organizing
			firmwareProvisioner.On("AddFirmwareBinariesToCacheByMetadata", crName).
				Run(func(args mock.Arguments) {
					time.Sleep(1 * time.Second) // Enough delay to observe "Processing"
				}).
				Return(nil)

			firmwareProvisioner.On("ValidateFirmwareBinariesCache", crName).
				Return(versionsMap, nil)

			createCR(crName, crNamespace)

			// After the download, the code sets it to "Processing"
			Eventually(func() string {
				cr, err := getCR(crName, crNamespace)
				if apiErrors.IsNotFound(err) {
					return ""
				}
				Expect(err).NotTo(HaveOccurred())
				return cr.Status.State
			}, 500*time.Millisecond, 100*time.Millisecond).Should(Equal(consts.FirmwareSourceProcessingStatus))

			// Eventually it should succeed with an empty reason
			ValidateCRStatusAndReason(crName, crNamespace, consts.FirmwareSourceSuccessStatus, "")
			ValidateCRReportedVersions(crName, crNamespace, versionsMap)
		})

		It("should set the ProcessingFailed status if the firmware provisioner failed to organize the binaries", func() {
			errMsg := "failed to organize"

			firmwareProvisioner.On("IsFWStorageAvailable").
				Return(nil)

			firmwareProvisioner.On("VerifyCachedBinaries", crName, mock.AnythingOfType("[]string")).
				Return([]string{"http://firmware.example.com/fwA.zip"}, nil)

			firmwareProvisioner.On("DownloadAndUnzipFirmwareArchives", crName, []string{"http://firmware.example.com/fwA.zip"}, true).
				Return(nil)

			firmwareProvisioner.On("AddFirmwareBinariesToCacheByMetadata", crName).
				Return(errors.New(errMsg))

			firmwareProvisioner.AssertNotCalled(GinkgoT(), "ValidateFirmwareBinariesCache", crName)

			createCR(crName, crNamespace)

			ValidateCRStatusAndReason(crName, crNamespace, consts.FirmwareSourceProcessingFailedStatus, errMsg)
		})

		createBFBOnlyCR := func(name, namespace string) {
			By("creating BFB-only NicFirmwareSource CR")
			log.Log.Info("creating BFB-only NicFirmwareSource CR for test", "test", GinkgoT().Name())
			cr := &v1alpha1.NicFirmwareSource{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: namespace,
				},
				Spec: v1alpha1.NicFirmwareSourceSpec{
					BFBUrlSource: "https://firmware.example.com/firmware.bfb",
				},
			}
			Expect(k8sClient.Create(ctx, cr)).To(Succeed())
		}

		createMixedCR := func(name, namespace string) {
			By("creating Mixed Binary+BFB NicFirmwareSource CR")
			log.Log.Info("creating Mixed NicFirmwareSource CR for test", "test", GinkgoT().Name())
			cr := &v1alpha1.NicFirmwareSource{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: namespace,
				},
				Spec: v1alpha1.NicFirmwareSourceSpec{
					BinUrlSources:       []string{"https://firmware.example.com/fwA.zip"},
					BFBUrlSource:        "https://firmware.example.com/firmware.bfb",
					DocaSpcXCCUrlSource: "https://firmware.example.com/doca-spc-x-pcc.bin",
				},
			}
			Expect(k8sClient.Create(ctx, cr)).To(Succeed())
		}

		ValidateCRBFBVersions := func(name, namespace string, expectedVersions map[string]string) {
			Eventually(func() map[string]string {
				cr, err := getCR(name, namespace)
				Expect(err).NotTo(HaveOccurred())
				return cr.Status.BFBVersions
			}).Should(Equal(expectedVersions))
		}

		It("should successfully process BFB-only firmware source", func() {
			bfbFileName := testBFBFileName
			bfbVersions := map[string]string{
				"bf2": "24.35.1000",
				"bf3": "28.39.1002",
			}

			firmwareProvisioner.On("IsFWStorageAvailable").
				Return(nil)

			// BFB processing
			firmwareProvisioner.On("VerifyCachedBFB", crName, "https://firmware.example.com/firmware.bfb").
				Return(true, nil) // needs download

			firmwareProvisioner.On("DownloadBFB", crName, "https://firmware.example.com/firmware.bfb").
				Return(bfbFileName, nil)

			firmwareProvisioner.On("ValidateBFB", crName).
				Return(bfbVersions, nil)

			createBFBOnlyCR(crName, crNamespace)

			ValidateCRStatusAndReason(crName, crNamespace, consts.FirmwareSourceSuccessStatus, "")
			ValidateCRBFBVersions(crName, crNamespace, bfbVersions)

			// Verify no binary processing was called
			firmwareProvisioner.AssertNotCalled(GinkgoT(), "VerifyCachedBinaries")
			firmwareProvisioner.AssertNotCalled(GinkgoT(), "DownloadAndUnzipFirmwareArchives")
			firmwareProvisioner.AssertNotCalled(GinkgoT(), "AddFirmwareBinariesToCacheByMetadata")
			firmwareProvisioner.AssertNotCalled(GinkgoT(), "ValidateFirmwareBinariesCache")
		})

		It("should skip BFB download when already cached", func() {
			bfbVersions := map[string]string{
				"bf2": "24.35.1000",
				"bf3": "28.39.1002",
			}

			firmwareProvisioner.On("IsFWStorageAvailable").
				Return(nil)

			// BFB already cached
			firmwareProvisioner.On("VerifyCachedBFB", crName, "https://firmware.example.com/firmware.bfb").
				Return(false, nil) // no download needed

			firmwareProvisioner.On("ValidateBFB", crName).
				Return(bfbVersions, nil)

			createBFBOnlyCR(crName, crNamespace)

			ValidateCRStatusAndReason(crName, crNamespace, consts.FirmwareSourceSuccessStatus, "")
			ValidateCRBFBVersions(crName, crNamespace, bfbVersions)

			// Verify download was not called
			firmwareProvisioner.AssertNotCalled(GinkgoT(), "DownloadBFB")
		})

		It("should process both binary and BFB sources independently", func() {
			versionsMap := map[string][]string{"1.2.3": {"psid1"}}
			bfbFileName := "firmware.bfb"
			bfbVersions := map[string]string{
				"bf2": "24.35.1000",
				"bf3": "28.39.1002",
			}

			firmwareProvisioner.On("IsFWStorageAvailable").
				Return(nil)

			// Binary processing
			firmwareProvisioner.On("VerifyCachedBinaries", crName, mock.AnythingOfType("[]string")).
				Return([]string{"https://firmware.example.com/fwA.zip"}, nil)

			firmwareProvisioner.On("DownloadAndUnzipFirmwareArchives", crName, []string{"https://firmware.example.com/fwA.zip"}, true).
				Return(nil)

			firmwareProvisioner.On("AddFirmwareBinariesToCacheByMetadata", crName).
				Return(nil)

			firmwareProvisioner.On("ValidateFirmwareBinariesCache", crName).
				Return(versionsMap, nil)

			// BFB processing
			firmwareProvisioner.On("VerifyCachedBFB", crName, "https://firmware.example.com/firmware.bfb").
				Return(true, nil) // needs download

			firmwareProvisioner.On("DownloadBFB", crName, "https://firmware.example.com/firmware.bfb").
				Return(bfbFileName, nil)

			firmwareProvisioner.On("ValidateBFB", crName).
				Return(bfbVersions, nil)

			// DOCA processing
			firmwareProvisioner.On("VerifyCachedDocaSpcXCC", crName, "https://firmware.example.com/doca-spc-x-pcc.bin").
				Return(true, nil)
			firmwareProvisioner.On("DownloadDocaSpcXCC", crName, "https://firmware.example.com/doca-spc-x-pcc.bin").
				Return("doca-spc-x-pcc.bin", nil)
			firmwareProvisioner.On("ValidateDocaSpcXCC", crName).
				Return("1.2.3", nil)

			createMixedCR(crName, crNamespace)

			ValidateCRStatusAndReason(crName, crNamespace, consts.FirmwareSourceSuccessStatus, "")
			ValidateCRReportedVersions(crName, crNamespace, versionsMap)
			ValidateCRBFBVersions(crName, crNamespace, bfbVersions)
			Eventually(func() string {
				cr, err := getCR(crName, crNamespace)
				Expect(err).NotTo(HaveOccurred())
				return cr.Status.DocaSpcXCCVersion
			}).Should(Equal("1.2.3"))
		})

		It("should handle DOCA SPC-X PCC only CR", func() {
			firmwareProvisioner.On("IsFWStorageAvailable").Return(nil)
			cr := &v1alpha1.NicFirmwareSource{
				ObjectMeta: metav1.ObjectMeta{
					Name:      crName,
					Namespace: crNamespace,
				},
				Spec: v1alpha1.NicFirmwareSourceSpec{
					DocaSpcXCCUrlSource: "https://firmware.example.com/doca-spc-x-pcc.bin",
				},
			}
			firmwareProvisioner.On("VerifyCachedDocaSpcXCC", crName, "https://firmware.example.com/doca-spc-x-pcc.bin").Return(true, nil)
			firmwareProvisioner.On("DownloadDocaSpcXCC", crName, "https://firmware.example.com/doca-spc-x-pcc.bin").Return("doca-spc-x-pcc.bin", nil)
			firmwareProvisioner.On("ValidateDocaSpcXCC", crName).Return("4.5.6", nil)

			Expect(k8sClient.Create(ctx, cr)).To(Succeed())
			ValidateCRStatusAndReason(crName, crNamespace, consts.FirmwareSourceSuccessStatus, "")
			Eventually(func() string {
				cr, err := getCR(crName, crNamespace)
				Expect(err).NotTo(HaveOccurred())
				return cr.Status.DocaSpcXCCVersion
			}).Should(Equal("4.5.6"))
		})

		It("should set CacheVerificationFailed when DOCA cache verification fails", func() {
			firmwareProvisioner.On("IsFWStorageAvailable").Return(nil)
			cr := &v1alpha1.NicFirmwareSource{
				ObjectMeta: metav1.ObjectMeta{
					Name:      crName,
					Namespace: crNamespace,
				},
				Spec: v1alpha1.NicFirmwareSourceSpec{
					DocaSpcXCCUrlSource: "https://firmware.example.com/doca-spc-x-pcc.bin",
				},
			}
			firmwareProvisioner.On("VerifyCachedDocaSpcXCC", crName, "https://firmware.example.com/doca-spc-x-pcc.bin").Return(false, errors.New("verify error"))

			Expect(k8sClient.Create(ctx, cr)).To(Succeed())
			ValidateCRStatusAndReason(crName, crNamespace, consts.FirmwareSourceCacheVerificationFailedStatus, "verify error")
		})

		It("should set DownloadFailed when DOCA download fails", func() {
			firmwareProvisioner.On("IsFWStorageAvailable").Return(nil)
			cr := &v1alpha1.NicFirmwareSource{
				ObjectMeta: metav1.ObjectMeta{
					Name:      crName,
					Namespace: crNamespace,
				},
				Spec: v1alpha1.NicFirmwareSourceSpec{
					DocaSpcXCCUrlSource: "https://firmware.example.com/doca-spc-x-pcc.bin",
				},
			}
			firmwareProvisioner.On("VerifyCachedDocaSpcXCC", crName, "https://firmware.example.com/doca-spc-x-pcc.bin").Return(true, nil)
			firmwareProvisioner.On("DownloadDocaSpcXCC", crName, "https://firmware.example.com/doca-spc-x-pcc.bin").Return("", errors.New("download error"))

			Expect(k8sClient.Create(ctx, cr)).To(Succeed())
			ValidateCRStatusAndReason(crName, crNamespace, consts.FirmwareSourceDownloadFailedStatus, "download error")
		})

		It("should set ProcessingFailed when DOCA validation fails", func() {
			firmwareProvisioner.On("IsFWStorageAvailable").Return(nil)
			cr := &v1alpha1.NicFirmwareSource{
				ObjectMeta: metav1.ObjectMeta{
					Name:      crName,
					Namespace: crNamespace,
				},
				Spec: v1alpha1.NicFirmwareSourceSpec{
					DocaSpcXCCUrlSource: "https://firmware.example.com/doca-spc-x-pcc.bin",
				},
			}
			firmwareProvisioner.On("VerifyCachedDocaSpcXCC", crName, "https://firmware.example.com/doca-spc-x-pcc.bin").Return(false, nil)
			firmwareProvisioner.On("ValidateDocaSpcXCC", crName).Return("", errors.New("validation error"))

			Expect(k8sClient.Create(ctx, cr)).To(Succeed())
			ValidateCRStatusAndReason(crName, crNamespace, consts.FirmwareSourceProcessingFailedStatus, "validation error")
		})

		It("should set CacheVerificationFailed status if BFB cache verification fails", func() {
			errMsg := "failed to verify BFB cache"

			firmwareProvisioner.On("IsFWStorageAvailable").
				Return(nil)

			firmwareProvisioner.On("VerifyCachedBFB", crName, "https://firmware.example.com/firmware.bfb").
				Return(false, errors.New(errMsg))

			firmwareProvisioner.AssertNotCalled(GinkgoT(), "DownloadBFB")

			createBFBOnlyCR(crName, crNamespace)

			ValidateCRStatusAndReason(crName, crNamespace, consts.FirmwareSourceCacheVerificationFailedStatus, errMsg)
			ValidateCRBFBVersions(crName, crNamespace, nil) // BFB versions should be empty on failure
		})

		It("should set DownloadFailed status if BFB download fails", func() {
			errMsg := "failed to download BFB"

			firmwareProvisioner.On("IsFWStorageAvailable").
				Return(nil)

			firmwareProvisioner.On("VerifyCachedBFB", crName, "https://firmware.example.com/firmware.bfb").
				Return(true, nil) // needs download

			firmwareProvisioner.On("DownloadBFB", crName, "https://firmware.example.com/firmware.bfb").
				Return("", errors.New(errMsg))

			createBFBOnlyCR(crName, crNamespace)

			ValidateCRStatusAndReason(crName, crNamespace, consts.FirmwareSourceDownloadFailedStatus, errMsg)
			ValidateCRBFBVersions(crName, crNamespace, nil) // BFB versions should be empty on failure
		})

		It("should show Downloading status during BFB download", func() {
			bfbFileName := testBFBFileName
			bfbVersions := map[string]string{
				"bf2": "24.35.1000",
				"bf3": "28.39.1002",
			}

			firmwareProvisioner.On("IsFWStorageAvailable").
				Return(nil)

			firmwareProvisioner.On("VerifyCachedBFB", crName, "https://firmware.example.com/firmware.bfb").
				Return(true, nil) // needs download

			// Simulate slow BFB download
			firmwareProvisioner.On("DownloadBFB", crName, "https://firmware.example.com/firmware.bfb").
				Run(func(args mock.Arguments) {
					time.Sleep(1 * time.Second)
				}).
				Return(bfbFileName, nil)

			firmwareProvisioner.On("ValidateBFB", crName).
				Return(bfbVersions, nil)

			createBFBOnlyCR(crName, crNamespace)

			// We want to see "Downloading" before it completes
			Eventually(func() string {
				cr, err := getCR(crName, crNamespace)
				if apiErrors.IsNotFound(err) {
					return ""
				}
				Expect(err).NotTo(HaveOccurred())
				return cr.Status.State
			}, 500*time.Millisecond, 100*time.Millisecond).Should(Equal(consts.FirmwareSourceDownloadingStatus))

			ValidateCRStatusAndReason(crName, crNamespace, consts.FirmwareSourceSuccessStatus, "")
			ValidateCRBFBVersions(crName, crNamespace, bfbVersions)
		})

		It("should handle mixed processing with binary success and BFB failure", func() {
			versionsMap := map[string][]string{"1.2.3": {"psid1"}}
			errMsg := "BFB download failed"

			firmwareProvisioner.On("IsFWStorageAvailable").
				Return(nil)

			// Binary processing succeeds
			firmwareProvisioner.On("VerifyCachedBinaries", crName, mock.AnythingOfType("[]string")).
				Return([]string{}, nil) // no download needed

			firmwareProvisioner.On("AddFirmwareBinariesToCacheByMetadata", crName).
				Return(nil)

			firmwareProvisioner.On("ValidateFirmwareBinariesCache", crName).
				Return(versionsMap, nil)

			// BFB processing fails
			firmwareProvisioner.On("VerifyCachedBFB", crName, "https://firmware.example.com/firmware.bfb").
				Return(true, nil) // needs download

			firmwareProvisioner.On("DownloadBFB", crName, "https://firmware.example.com/firmware.bfb").
				Return("", errors.New(errMsg))

			createMixedCR(crName, crNamespace)

			// Should fail overall due to BFB failure
			ValidateCRStatusAndReason(crName, crNamespace, consts.FirmwareSourceDownloadFailedStatus, errMsg)
			ValidateCRBFBVersions(crName, crNamespace, nil) // BFB versions should be empty on failure
		})

		It("should handle mixed processing with binary failure and BFB success", func() {
			errMsg := "binary download failed"

			firmwareProvisioner.On("IsFWStorageAvailable").
				Return(nil)

			// Binary processing fails
			firmwareProvisioner.On("VerifyCachedBinaries", crName, mock.AnythingOfType("[]string")).
				Return([]string{"https://firmware.example.com/fwA.zip"}, nil)

			firmwareProvisioner.On("DownloadAndUnzipFirmwareArchives", crName, []string{"https://firmware.example.com/fwA.zip"}, true).
				Return(errors.New(errMsg))

			// BFB processing should not be called after binary failure
			firmwareProvisioner.AssertNotCalled(GinkgoT(), "VerifyCachedBFB")
			firmwareProvisioner.AssertNotCalled(GinkgoT(), "DownloadBFB")

			createMixedCR(crName, crNamespace)

			// Should fail due to binary failure, before BFB processing
			ValidateCRStatusAndReason(crName, crNamespace, consts.FirmwareSourceDownloadFailedStatus, errMsg)
			ValidateCRBFBVersions(crName, crNamespace, nil) // BFB versions should be empty on failure
		})
	})

	Describe("NicFirmwareSource Finalizer", func() {
		BeforeEach(func() {
			ctx, cancel = context.WithCancel(context.Background())

			mgr = createManager()

			k8sClient = mgr.GetClient()

			firmwareProvisioner = mocks.FirmwareProvisioner{}

			reconciler = &NicFirmwareSourceReconciler{
				Client:              mgr.GetClient(),
				Scheme:              mgr.GetScheme(),
				FirmwareProvisioner: &firmwareProvisioner,
			}

			Expect(reconciler.SetupWithManager(mgr)).To(Succeed())

			list := &v1alpha1.NicFirmwareSourceList{}
			_ = k8sClient.List(ctx, list)

			By("start manager")
			wg := sync.WaitGroup{}
			startManager(mgr, ctx, &wg)

			DeferCleanup(func() {
				By("Shut down controller manager")
				cancel()
				wg.Wait()
			})
		})

		AfterEach(func() {
			cancel()
		})

		It("should delete the cache and remove the finalizer", func() {
			firmwareProvisioner.On("IsFWStorageAvailable").
				Return(nil)
			firmwareProvisioner.On("VerifyCachedBinaries", crName, mock.AnythingOfType("[]string")).
				Return([]string{"http://firmware.example.com/fwA.zip"}, nil)

			firmwareProvisioner.On("DownloadAndUnzipFirmwareArchives", crName, []string{"http://firmware.example.com/fwA.zip"}, true).
				Return(nil)

			firmwareProvisioner.On("AddFirmwareBinariesToCacheByMetadata", crName).
				Return(nil)

			firmwareProvisioner.On("ValidateFirmwareBinariesCache", crName).
				Return(map[string][]string{}, nil)

			cr := &v1alpha1.NicFirmwareSource{
				ObjectMeta: metav1.ObjectMeta{
					Name:       crName,
					Namespace:  crNamespace,
					Finalizers: []string{consts.FirmwareSourceFinalizerName},
				},
				Spec: v1alpha1.NicFirmwareSourceSpec{
					BinUrlSources: []string{"https://firmware.example.com/fwA.zip"},
				},
			}
			Expect(k8sClient.Create(ctx, cr)).To(Succeed())

			ValidateCRStatusAndReason(crName, crNamespace, consts.FirmwareSourceSuccessStatus, "")

			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: crName, Namespace: crNamespace}, cr)).To(Succeed())

			firmwareProvisioner.On("DeleteCache", crName).
				Return(nil)

			Expect(k8sClient.Delete(ctx, cr)).To(Succeed())

			Eventually(func() *v1alpha1.NicFirmwareSource {
				cr, err := getCR(crName, crNamespace)
				if apiErrors.IsNotFound(err) {
					return nil
				}
				Expect(err).NotTo(HaveOccurred())
				return cr
			}, time.Second*10).Should(BeNil())
		})

		It("should not remove the finalizer if failed to delete cache", func() {
			firmwareProvisioner.On("IsFWStorageAvailable").
				Return(nil)
			firmwareProvisioner.On("VerifyCachedBinaries", crName, mock.AnythingOfType("[]string")).
				Return([]string{"http://firmware.example.com/fwA.zip"}, nil)

			firmwareProvisioner.On("DownloadAndUnzipFirmwareArchives", crName, []string{"http://firmware.example.com/fwA.zip"}, true).
				Return(nil)

			firmwareProvisioner.On("AddFirmwareBinariesToCacheByMetadata", crName).
				Return(nil)

			firmwareProvisioner.On("ValidateFirmwareBinariesCache", crName).
				Return(map[string][]string{}, nil)

			cr := &v1alpha1.NicFirmwareSource{
				ObjectMeta: metav1.ObjectMeta{
					Name:       crName,
					Namespace:  crNamespace,
					Finalizers: []string{consts.FirmwareSourceFinalizerName},
				},
				Spec: v1alpha1.NicFirmwareSourceSpec{
					BinUrlSources: []string{"https://firmware.example.com/fwA.zip"},
				},
			}
			Expect(k8sClient.Create(ctx, cr)).To(Succeed())

			ValidateCRStatusAndReason(crName, crNamespace, consts.FirmwareSourceSuccessStatus, "")

			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: crName, Namespace: crNamespace}, cr)).To(Succeed())

			err := errors.New("failed to delete cache")
			firmwareProvisioner.On("DeleteCache", crName).
				Return(err)

			Expect(k8sClient.Delete(ctx, cr)).To(Succeed())

			Consistently(func() *v1alpha1.NicFirmwareSource {
				cr, err := getCR(crName, crNamespace)
				if apiErrors.IsNotFound(err) {
					return nil
				}
				Expect(err).NotTo(HaveOccurred())
				return cr
			}, time.Second).ShouldNot(BeNil())
		})
	})

})
