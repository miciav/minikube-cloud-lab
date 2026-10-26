package labcmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/dynamic"

	"github.com/miciav/minikube-cloud-lab/lab/manifests"
)

func readObjects(paths []string) ([]*unstructured.Unstructured, error) {
	var objects []*unstructured.Unstructured
	for _, path := range paths {
		data, err := manifests.Files.ReadFile(path)
		if err != nil {
			return nil, err
		}
		decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)
		for {
			obj := &unstructured.Unstructured{}
			err := decoder.Decode(obj)
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return nil, fmt.Errorf("decode %s: %w", path, err)
			}
			if len(obj.Object) > 0 {
				objects = append(objects, obj)
			}
		}
	}
	return objects, nil
}

func applyFiles(ctx context.Context, client dynamic.Interface, mapper meta.RESTMapper, paths []string, remove bool, replicas *int32) error {
	objects, err := readObjects(paths)
	if err != nil {
		return err
	}
	if remove {
		slices.Reverse(objects)
	}
	for _, obj := range objects {
		gvk := obj.GroupVersionKind()
		mapping, err := mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
		if err != nil {
			return err
		}
		var resource dynamic.ResourceInterface = client.Resource(mapping.Resource)
		if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
			namespace := obj.GetNamespace()
			if namespace == "" {
				namespace = "default"
			}
			resource = client.Resource(mapping.Resource).Namespace(namespace)
		}
		if remove {
			if err := resource.Delete(ctx, obj.GetName(), metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
				return err
			}
			continue
		}
		if replicas != nil {
			if err := unstructured.SetNestedField(obj.Object, int64(*replicas), "spec", "replicas"); err != nil {
				return err
			}
		}
		data, err := json.Marshal(obj.Object)
		if err != nil {
			return err
		}
		if _, err := resource.Patch(ctx, obj.GetName(), types.ApplyPatchType, data, metav1.PatchOptions{FieldManager: "lab"}); err != nil {
			return err
		}
	}
	return nil
}
