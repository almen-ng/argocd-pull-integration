/*
Copyright 2025 Open Cluster Management.

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
	"fmt"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"

	appsv1alpha1 "open-cluster-management.io/argocd-pull-integration/api/v1alpha1"
)

const (
	// ArgoCDAgentPrincipalServiceName is the name of the principal service
	ArgoCDAgentPrincipalServiceName = "argocd-agent-principal"

	// Fallback service name
	OpenshiftGitOpsAgentPrincipalServiceName = "openshift-gitops-agent-principal"
)

// EnsureServerAddressAndPort discovers and populates server address and port if they are empty
// Returns true if the GitOpsCluster spec was updated, false otherwise
func (r *GitOpsClusterReconciler) EnsureServerAddressAndPort(
	ctx context.Context,
	gitOpsCluster *appsv1alpha1.GitOpsCluster,
	argoCDNamespace string) (bool, error) {

	// Check if serverAddress and serverPort are already set in the GitOpsCluster spec
	if gitOpsCluster.Spec.ArgoCDAgentAddon.PrincipalServerAddress != "" &&
		gitOpsCluster.Spec.ArgoCDAgentAddon.PrincipalServerPort != "" {
		klog.V(2).InfoS("Server address and port already set in GitOpsCluster spec, using them",
			"address", gitOpsCluster.Spec.ArgoCDAgentAddon.PrincipalServerAddress,
			"port", gitOpsCluster.Spec.ArgoCDAgentAddon.PrincipalServerPort)
		return false, nil
	}

	// Discover server address and port from the ArgoCD agent principal service
	serverAddress, serverPort, err := r.discoverServerAddressAndPort(ctx, argoCDNamespace)
	if err != nil {
		return false, fmt.Errorf("failed to discover server address and port: %w", err)
	}

	// Update the GitOpsCluster spec with discovered values
	gitOpsCluster.Spec.ArgoCDAgentAddon.PrincipalServerAddress = serverAddress
	gitOpsCluster.Spec.ArgoCDAgentAddon.PrincipalServerPort = serverPort

	klog.InfoS("Auto-discovered and populated server address and port",
		"address", serverAddress, "port", serverPort,
		"namespace", gitOpsCluster.Namespace, "name", gitOpsCluster.Name)

	return true, nil
}

// discoverServerAddressAndPort discovers the external server address and port from the ArgoCD agent principal service.
// It supports both LoadBalancer and NodePort service types.
func (r *GitOpsClusterReconciler) discoverServerAddressAndPort(
	ctx context.Context,
	argoCDNamespace string) (string, string, error) {

	service, err := r.findArgoCDAgentPrincipalService(ctx, argoCDNamespace)
	if err != nil {
		return "", "", fmt.Errorf("failed to find ArgoCD agent principal service: %w", err)
	}

	var serverAddress string
	var serverPort string = "443"

	// Try LoadBalancer ingress first
	for _, ingress := range service.Status.LoadBalancer.Ingress {
		if ingress.Hostname != "" {
			serverAddress = ingress.Hostname
			klog.InfoS("Discovered server address from LoadBalancer hostname", "hostname", serverAddress)
			break
		}
		if ingress.IP != "" {
			serverAddress = ingress.IP
			klog.InfoS("Discovered server address from LoadBalancer IP", "ip", serverAddress)
			break
		}
	}

	if serverAddress != "" {
		for _, port := range service.Spec.Ports {
			if port.Name == "https" || port.Port == 443 || port.Port == 8443 {
				serverPort = strconv.Itoa(int(port.Port))
				break
			}
		}
		klog.InfoS("Discovered ArgoCD agent server endpoint", "address", serverAddress, "port", serverPort)
		return serverAddress, serverPort, nil
	}

	// Fallback to NodePort: use a node's InternalIP + the assigned NodePort
	if service.Spec.Type == corev1.ServiceTypeNodePort {
		nodeIP, err := r.getNodeInternalIP(ctx)
		if err != nil {
			return "", "", fmt.Errorf("NodePort service found but failed to get node IP: %w", err)
		}
		serverAddress = nodeIP

		for _, port := range service.Spec.Ports {
			if port.NodePort != 0 && (port.Name == "https" || port.Port == 443 || port.Port == 8443) {
				serverPort = strconv.Itoa(int(port.NodePort))
				break
			}
		}
		// If no named match, use the first port with a NodePort
		if serverPort == "443" {
			for _, port := range service.Spec.Ports {
				if port.NodePort != 0 {
					serverPort = strconv.Itoa(int(port.NodePort))
					break
				}
			}
		}
		klog.InfoS("Discovered ArgoCD agent server endpoint via NodePort",
			"address", serverAddress, "port", serverPort)
		return serverAddress, serverPort, nil
	}

	return "", "", fmt.Errorf("no external endpoint found for service %s in namespace %s (type=%s, no LoadBalancer ingress)",
		service.Name, argoCDNamespace, service.Spec.Type)
}

// getNodeInternalIP returns the InternalIP of the first node in the cluster
func (r *GitOpsClusterReconciler) getNodeInternalIP(ctx context.Context) (string, error) {
	nodeList := &corev1.NodeList{}
	if err := r.List(ctx, nodeList); err != nil {
		return "", fmt.Errorf("failed to list nodes: %w", err)
	}
	for _, node := range nodeList.Items {
		for _, addr := range node.Status.Addresses {
			if addr.Type == corev1.NodeInternalIP {
				return addr.Address, nil
			}
		}
	}
	return "", fmt.Errorf("no node with InternalIP found")
}
