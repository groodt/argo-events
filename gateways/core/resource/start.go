/*
Copyright 2018 BlackRock, Inc.

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

package resource

import (
	"fmt"
	"github.com/argoproj/argo-events/gateways"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"strings"
)

// StartEventSource starts an event source
func (ce *ResourceConfigExecutor) StartEventSource(eventSource *gateways.EventSource, eventStream gateways.Eventing_StartEventSourceServer) error {
	ce.GatewayConfig.Log.Info().Str("event-source-name", *eventSource.Name).Msg("operating on event source")
	res, err := parseEventSource(eventSource.Data)
	if err != nil {
		return err
	}

	dataCh := make(chan []byte)
	errorCh := make(chan error)
	doneCh := make(chan struct{}, 1)

	go ce.listenEvents(res, eventSource, dataCh, errorCh, doneCh)

	return gateways.ConsumeEventsFromEventSource(eventSource.Name, eventStream, dataCh, errorCh, doneCh, &ce.Log)
}

func (ce *ResourceConfigExecutor) listenEvents(res *resource, eventSource *gateways.EventSource, dataCh chan []byte, errorCh chan error, doneCh chan struct{}) {
	logger := ce.Log.With().Str("event-source-name", *eventSource.Name).Logger()
	resource, err := ce.discoverResources(res)
	if err != nil {
		errorCh <- err
		return
	}
	options := metav1.ListOptions{Watch: true}
	if res.Filter != nil {
		options.LabelSelector = labels.Set(res.Filter.Labels).AsSelector().String()
	}

	w, err := resource.Watch(options)
	if err != nil {
		errorCh <- err
		return
	}

	for {
		select {
		case item := <-w.ResultChan():
			if item.Object == nil {
				logger.Warn().Msg("object to watch is nil")
				// renew watch due to it being ended with "too old resource version"
				w, err = resource.Watch(options)
				if err != nil {
					errorCh <- err
					return
				}
				continue
			}
			itemObj := item.Object.(*unstructured.Unstructured)
			b, err := itemObj.MarshalJSON()
			if err != nil {
				errorCh <- err
				return
			}
			if item.Type == watch.Error {
				err = errors.FromObject(item.Object)
				errorCh <- err
				return
			}
			if ce.passFilters(itemObj, res.Filter) {
				dataCh <- b
			}
		case <-doneCh:
			return
		}
	}
}

func (ce *ResourceConfigExecutor) discoverResources(obj *resource) (dynamic.ResourceInterface, error) {
	dynClientPool := dynamic.NewDynamicClientPool(ce.GatewayConfig.KubeConfig)
	disco, err := discovery.NewDiscoveryClientForConfig(ce.GatewayConfig.KubeConfig)
	if err != nil {
		return nil, err
	}

	groupVersion := ce.resolveGroupVersion(obj)
	resourceInterfaces, err := disco.ServerResourcesForGroupVersion(groupVersion)
	if err != nil {
		return nil, err
	}
	for i := range resourceInterfaces.APIResources {
		apiResource := resourceInterfaces.APIResources[i]
		gvk := schema.FromAPIVersionAndKind(resourceInterfaces.GroupVersion, apiResource.Kind)
		ce.GatewayConfig.Log.Info().Str("api-resource", gvk.String())
		if apiResource.Kind != obj.Kind || apiResource.Version != obj.Version {
			continue
		}
		canWatch := false
		for _, verb := range apiResource.Verbs {
			if verb == "watch" {
				canWatch = true
				break
			}
		}
		if canWatch {
			client, err := dynClientPool.ClientForGroupVersionKind(gvk)
			if err != nil {
				return nil, err
			}
			return client.Resource(&apiResource, obj.Namespace), nil
		}
	}
	return nil, fmt.Errorf("failed to list resource with group: %s, kined: %s and version: %s with watch capabilities", obj.Group, obj.Kind, obj.Version)
}

func (ce *ResourceConfigExecutor) resolveGroupVersion(obj *resource) string {
	if obj.Version == "v1" {
		return obj.Version
	}
	return obj.Group + "/" + obj.Version
}

// helper method to return a flag indicating if the object passed the client side filters
func (ce *ResourceConfigExecutor) passFilters(obj *unstructured.Unstructured, filter *ResourceFilter) bool {
	// no filters are applied.
	if filter == nil {
		return true
	}
	// check prefix
	if !strings.HasPrefix(obj.GetName(), filter.Prefix) {
		ce.GatewayConfig.Log.Info().Str("resource-name", obj.GetName()).Str("prefix", filter.Prefix).Msg("FILTERED: resource name does not match prefix")
		return false
	}
	// check creation timestamp
	created := obj.GetCreationTimestamp()
	if !filter.CreatedBy.IsZero() && created.UTC().After(filter.CreatedBy.UTC()) {
		ce.GatewayConfig.Log.Info().Str("creation-timestamp", created.UTC().String()).Str("createdBy", filter.CreatedBy.UTC().String()).Msg("FILTERED: resource creation timestamp is after createdBy")
		return false
	}
	// check labels
	if ok := checkMap(filter.Labels, obj.GetLabels()); !ok {
		ce.GatewayConfig.Log.Info().Interface("resource-labels", obj.GetLabels()).Interface("filter-labels", filter.Labels).Msg("FILTERED: labels mismatch")
		return false
	}
	// check annotations
	if ok := checkMap(filter.Annotations, obj.GetAnnotations()); !ok {
		ce.GatewayConfig.Log.Info().Interface("resource-annotations", obj.GetAnnotations()).Interface("filter-annotations", filter.Annotations).Msg("FILTERED: annotations mismatch")
		return false
	}
	return true
}

// utility method to check the actual map matches the expected by values
func checkMap(expected, actual map[string]string) bool {
	if actual != nil {
		for k, v := range expected {
			if actual[k] != v {
				return false
			}
		}
		return true
	}
	if expected != nil {
		return false
	}
	return true
}
