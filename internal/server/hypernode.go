/*
Copyright 2026 The HAMi Authors.

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

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"text/template"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"

	"github.com/Project-HAMi/HAMi/pkg/util/client"
	"github.com/Project-HAMi/ascend-device-plugin/internal"
	"github.com/Project-HAMi/ascend-device-plugin/internal/monitor"
)

const (
	// DefaultHyperNodeLabelKey is the node label key carrying the supernode
	// identity, matching the key HAMi's hypernode-aware scheduler reads.
	DefaultHyperNodeLabelKey = "hami.io/hypernode"
	// DefaultHyperNodeValueTemplate renders the label value from the
	// super-pod id reported by the driver.
	DefaultHyperNodeValueTemplate = "supernode-{{ .SuperPodID }}"
)

// hyperNodeCfg is the hypernode section of the device config, wired from
// main via SetHyperNodeConfig (package-level to keep NewPluginServer's
// signature stable for existing callers and tests).
var hyperNodeCfg internal.HyperNodeConfig

// superPodIDFunc resolves the supernode identity; a variable so tests can
// stub the DCMI-backed monitor call.
var superPodIDFunc = monitor.GetSuperPodID

// SetHyperNodeConfig wires the hypernode section of the device config. Call
// it before NewPluginServer; an absent/zero section keeps labelling off.
func SetHyperNodeConfig(cfg internal.HyperNodeConfig) {
	hyperNodeCfg = cfg
}

// resolveHyperNodeLabels renders the supernode identity label once. Returns
// nil — meaning "never patch" — when the feature is disabled, the label key
// is blank, the template is invalid, or the hardware/driver exposes no
// supernode identity (-1 unknown / -2 unsupported), so nodes without
// supernode semantics simply stay unlabelled and scheduling degrades to the
// plain device policy.
func resolveHyperNodeLabels() map[string]string {
	if !hyperNodeCfg.Enabled || hyperNodeCfg.LabelKey == "" {
		return nil
	}

	id, err := superPodIDFunc()
	if err != nil || id < 0 {
		klog.Warningf("hypernode labelling disabled: no usable super-pod id (id=%d, err=%v)", id, err)
		return nil
	}

	valueTemplate := hyperNodeCfg.ValueTemplate
	if valueTemplate == "" {
		valueTemplate = DefaultHyperNodeValueTemplate
	}
	tpl, err := template.New("hypernode").Parse(valueTemplate)
	if err != nil {
		klog.Errorf("parse hypernode value template %q failed: %v", valueTemplate, err)
		return nil
	}
	var sb strings.Builder
	if err := tpl.Execute(&sb, struct{ SuperPodID int32 }{SuperPodID: id}); err != nil {
		klog.Errorf("render hypernode label value failed: %v", err)
		return nil
	}
	labels := map[string]string{hyperNodeCfg.LabelKey: sb.String()}
	klog.Infof("hypernode label resolved: %v", labels)
	return labels
}

// hyperNodeLabelsNeedUpdate reports whether any target label differs from the
// node's current labels, so the periodic registration skips no-op patches.
func hyperNodeLabelsNeedUpdate(node *v1.Node, labels map[string]string) bool {
	if node == nil {
		return false
	}
	for key, want := range labels {
		if node.Labels[key] != want {
			return true
		}
	}
	return false
}

// patchNodeLabels merge-patches the node's metadata.labels, mirroring HAMi's
// PatchNodeAnnotations helper (same client, same patch shape).
func patchNodeLabels(node *v1.Node, labels map[string]string) error {
	if node == nil {
		return fmt.Errorf("node is nil")
	}
	if client.KubeClient == nil {
		return fmt.Errorf("kubernetes client is not initialized")
	}
	type patchMetadata struct {
		Labels map[string]string `json:"labels,omitempty"`
	}
	type patchNode struct {
		Metadata patchMetadata `json:"metadata"`
	}
	p := patchNode{}
	p.Metadata.Labels = labels
	bytes, err := json.Marshal(p)
	if err != nil {
		return err
	}
	_, err = client.KubeClient.CoreV1().Nodes().
		Patch(context.Background(), node.Name, k8stypes.MergePatchType, bytes, metav1.PatchOptions{})
	if err != nil {
		klog.Infof("labels=%v", labels)
		klog.Infof("patch node %v failed, %v", node.Name, err)
	}
	return err
}
