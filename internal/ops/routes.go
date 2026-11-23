package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"slices"
	"strings"

	rootconfig "mt/internal/config"
	"mt/internal/execx"
	"mt/internal/support"
)

const routeDNSStart = "# mt routes begin"
const routeDNSEnd = "# mt routes end"

type routeDNS struct {
	logger *slog.Logger
	runner execx.Runner
}

type routeCluster struct {
	config rootconfig.ClusterConfig
	ip     string
}

func (r *routeDNS) reconcile(ctx context.Context) error {
	clusters, err := r.runningClusters(ctx)
	if err != nil {
		return err
	}

	var reconcileErrors []error
	for _, cluster := range clusters {
		var blocks []coreDNSBlock
		for _, peer := range clusters {
			if peer.config.Name == cluster.config.Name || peer.config.BridgeName != cluster.config.BridgeName {
				continue
			}
			blocks = append(blocks, routeDNSBlock(peer.config.Name, peer.ip))
		}

		if err := r.update(ctx, cluster, blocks); err != nil {
			reconcileErrors = append(reconcileErrors, fmt.Errorf("cluster %s: %w", cluster.config.Name, err))
		}
	}
	return errors.Join(reconcileErrors...)
}

func (r *routeDNS) runningClusters(ctx context.Context) ([]routeCluster, error) {
	configs, err := runningClusterConfigs(ctx, r.runner)
	if err != nil {
		return nil, err
	}

	clusters := make([]routeCluster, 0, len(configs))
	for _, cfg := range configs {
		ip, err := support.GetContainerIP(ctx, r.runner, cfg.BridgeName, cfg.NodeName)
		if err != nil {
			return nil, fmt.Errorf("get control node IP for cluster %s: %w", cfg.Name, err)
		}
		clusters = append(clusters, routeCluster{config: cfg, ip: ip})
	}
	return clusters, nil
}

func runningClusterConfigs(ctx context.Context, runner execx.Runner) ([]rootconfig.ClusterConfig, error) {
	entries, err := os.ReadDir(rootconfig.ConfigDir())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read mt config directory: %w", err)
	}

	var configs []rootconfig.ClusterConfig
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ",") {
			continue
		}

		cfg, err := rootconfig.ReadClusterConfig(entry.Name())
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read cluster config %s: %w", entry.Name(), err)
		}

		containers, err := support.RunningClusterContainers(ctx, runner, cfg.Name)
		if err != nil {
			return nil, fmt.Errorf("list running containers for cluster %s: %w", cfg.Name, err)
		}
		if !slices.Contains(containers, cfg.NodeName) {
			continue
		}

		configs = append(configs, cfg)
	}
	return configs, nil
}

func (r *routeDNS) update(ctx context.Context, cluster routeCluster, blocks []coreDNSBlock) error {
	result, err := support.RunPodmanPrivileged(ctx, r.runner,
		"exec", cluster.config.NodeName,
		"oc", "--kubeconfig", internalKubeconfig,
		"get", "configmap", "dns-default", "-n", "openshift-dns",
		"-o=jsonpath={.data.Corefile}",
	)
	if err != nil {
		return fmt.Errorf("get CoreDNS Corefile: %w", err)
	}

	corefile, changed, err := reconcileCorefile(result.Stdout, blocks)
	if err != nil {
		return err
	}
	if !changed {
		r.logger.Info("route DNS is current", "cluster", cluster.config.Name, "peers", len(blocks))
		return nil
	}

	patch, err := json.Marshal(map[string]any{"data": map[string]string{"Corefile": corefile}})
	if err != nil {
		return fmt.Errorf("encode CoreDNS patch: %w", err)
	}
	r.logger.Info("updating route DNS", "cluster", cluster.config.Name, "peers", len(blocks))
	if _, err := support.RunPodmanPrivileged(ctx, r.runner,
		"exec", cluster.config.NodeName,
		"oc", "--kubeconfig", internalKubeconfig,
		"patch", "configmap", "dns-default", "-n", "openshift-dns",
		"--type=merge", "-p", string(patch),
	); err != nil {
		return fmt.Errorf("patch CoreDNS Corefile: %w", err)
	}
	return nil
}

type coreDNSBlock struct {
	zone string
	body string
}

func (b coreDNSBlock) String() string {
	return fmt.Sprintf("%s:5353 {\n%s\n}", b.zone, b.body)
}

func routeDNSBlock(clusterName, ip string) coreDNSBlock {
	zone := "apps." + rootconfig.BaseDomain(clusterName)
	return coreDNSBlock{
		zone: zone,
		body: fmt.Sprintf("\ttemplate IN A %s {\n\t\tmatch ^[a-zA-Z0-9-]+\\.%s\\.$\n\t\tanswer \"{{ .Name }} 30 IN A %s\"\n\t}", zone, regexp.QuoteMeta(zone), ip),
	}
}

func reconcileCorefile(corefile string, blocks []coreDNSBlock) (string, bool, error) {
	return reconcileManagedCorefile(corefile, routeDNSStart, routeDNSEnd, blocks)
}

func reconcileManagedCorefile(corefile, startMarker, endMarker string, blocks []coreDNSBlock) (string, bool, error) {
	original := corefile
	start := strings.Index(corefile, startMarker)
	end := strings.Index(corefile, endMarker)
	if (start == -1) != (end == -1) || (start != -1 && end < start) {
		return "", false, fmt.Errorf("malformed mt DNS block in Corefile")
	}

	if start == -1 && len(blocks) == 0 {
		return corefile, false, nil
	}

	managed := ""
	if len(blocks) > 0 {
		content := make([]string, 0, len(blocks))
		for _, block := range blocks {
			content = append(content, block.String())
		}
		managed = startMarker + "\n" + strings.Join(content, "\n\n") + "\n" + endMarker
	}
	if start != -1 {
		end += len(endMarker)
		updated := corefile[:start] + managed + corefile[end:]
		return updated, updated != original, nil
	}

	updated := strings.TrimRight(corefile, "\n") + "\n\n" + managed + "\n"
	return updated, updated != original, nil
}
