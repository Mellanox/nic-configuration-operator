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

package syncdaemon

import (
	"context"
	"fmt"

	"github.com/pkg/errors"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
	"github.com/Mellanox/nic-configuration-operator/pkg/render"
)

type ConfigDaemonParameters struct {
	Image              string
	Namespace          string
	ReleaseVersion     string
	ServiceAccountName string
	NodeSelector       string
	ImagePullSecrets   string
	Resources          string
	LogLevel           string
}

func SyncConfigDaemonObjs(ctx context.Context,
	client client.Client,
	scheme *runtime.Scheme,
	namespace string) error {
	log.Log.V(2).Info("Synchronizing configuration daemon objects")

	cfg := &v1.ConfigMap{}
	err := client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: consts.OperatorConfigMapName}, cfg)
	if err != nil {
		if apierrors.IsNotFound(err) {
			log.Log.Info("daemon config map not found, daemon set should not be created")
		} else {
			log.Log.Error(err, "failed to get daemon config map")
			return err
		}
	}

	data := render.MakeRenderData()
	data.Data["Image"] = cfg.Data["configDaemonImage"]
	data.Data["Namespace"] = namespace
	data.Data["ReleaseVersion"] = cfg.Data["releaseVersion"]
	data.Data["ServiceAccountName"] = cfg.Data["serviceAccountName"]
	data.Data["NodeSelector"] = cfg.Data["nodeSelector"]
	data.Data["ImagePullSecrets"] = cfg.Data["imagePullSecrets"]
	data.Data["Resources"] = cfg.Data["resources"]
	data.Data["LogLevel"] = cfg.Data["logLevel"]

	log.Log.Info("data", "data", data.Data)

	objs, err := render.RenderDir(consts.ConfigDaemonManifestsPath, &data)
	if err != nil {
		log.Log.Error(err, "Fail to render config daemon manifests")
		return err
	}
	// Sync DaemonSets
	for _, obj := range objs {
		if err := controllerutil.SetControllerReference(cfg, obj, scheme, controllerutil.WithBlockOwnerDeletion(false)); err != nil {
			return err
		}

		if err := applyObject(ctx, client, obj); err != nil {
			return fmt.Errorf("failed to apply object %v with err: %v", obj, err)
		}
	}
	return nil
}

// applyObject applies the desired object against the apiserver,
// merging it with any existing objects if already present.
func applyObject(ctx context.Context, client client.Client, obj *unstructured.Unstructured) error {
	name := obj.GetName()
	namespace := obj.GetNamespace()
	if name == "" {
		return errors.Errorf("Object %s has no name", obj.GroupVersionKind().String())
	}
	gvk := obj.GroupVersionKind()
	// used for logging and errors
	objDesc := fmt.Sprintf("(%s) %s/%s", gvk.String(), namespace, name)

	// Get existing
	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(gvk)
	err := client.Get(ctx, types.NamespacedName{Name: obj.GetName(), Namespace: obj.GetNamespace()}, existing)

	if err != nil && apierrors.IsNotFound(err) {
		err := client.Create(ctx, obj)
		if err != nil {
			return errors.Wrapf(err, "could not create %s", objDesc)
		}
		return nil
	}
	if err != nil {
		return errors.Wrapf(err, "could not retrieve existing %s", objDesc)
	}

	if !equality.Semantic.DeepEqual(existing, obj) {
		if err := client.Update(ctx, obj); err != nil {
			return errors.Wrapf(err, "could not update object %s", objDesc)
		}
	}

	return nil
}
