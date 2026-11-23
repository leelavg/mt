package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	rootconfig "mt/internal/config"
	"mt/internal/execx"
	"mt/internal/support"
)

const (
	volsyncDNSStart       = "# mt volsync begin"
	volsyncDNSEnd         = "# mt volsync end"
	volsyncServicePrefix  = "volsync-rsync-tls-dst-"
	serviceExportsCRDName = "serviceexports.multicluster.x-k8s.io"
	clusterSetDNSPoll     = 5 * time.Second
	clusterSetDNSTimeout  = 2 * time.Minute
)

type clusterSetDNS struct {
	logger *slog.Logger
	runner execx.Runner
}

type volsyncServiceExportList struct {
	Items []struct {
		Metadata struct {
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
		} `json:"metadata"`
	} `json:"items"`
}

type clusterSetDNSRecord struct {
	cluster   string
	bridge    string
	hostname  string
	clusterIP string
}

func (c *clusterSetDNS) reconcile(ctx context.Context) error {
	clusters, err := runningClusterConfigs(ctx, c.runner)
	if err != nil {
		return err
	}

	var records []clusterSetDNSRecord
	for _, cluster := range clusters {
		clusterRecords, err := c.serviceRecords(ctx, cluster)
		if err != nil {
			return fmt.Errorf("list VolSync exports in cluster %q: %w", cluster.Name, err)
		}
		records = append(records, clusterRecords...)
	}

	var reconcileErrors []error
	for _, cluster := range clusters {
		clusterRecords, err := clusterSetPeerRecords(cluster, records)
		if err != nil {
			reconcileErrors = append(reconcileErrors, fmt.Errorf("cluster %s: %w", cluster.Name, err))
			continue
		}

		var blocks []coreDNSBlock
		if len(clusterRecords) > 0 {
			blocks = []coreDNSBlock{clusterSetDNSBlock(clusterRecords)}
		}
		if err := c.update(ctx, cluster, blocks, clusterRecords); err != nil {
			reconcileErrors = append(reconcileErrors, fmt.Errorf("cluster %s: %w", cluster.Name, err))
		}
	}
	return errors.Join(reconcileErrors...)
}

func (c *clusterSetDNS) serviceRecords(ctx context.Context, cluster rootconfig.ClusterConfig) ([]clusterSetDNSRecord, error) {
	crd, err := c.run(ctx, cluster,
		"get", "crd", serviceExportsCRDName, "--ignore-not-found", "-o=name",
	)
	if err != nil {
		return nil, fmt.Errorf("check ServiceExport CRD: %w", err)
	}
	if strings.TrimSpace(crd) == "" {
		return nil, nil
	}

	output, err := c.run(ctx, cluster,
		"get", "serviceexports.multicluster.x-k8s.io", "--all-namespaces", "-o", "json",
	)
	if err != nil {
		return nil, fmt.Errorf("get ServiceExports: %w", err)
	}

	var exports volsyncServiceExportList
	if err := json.Unmarshal([]byte(output), &exports); err != nil {
		return nil, fmt.Errorf("decode ServiceExports: %w", err)
	}

	var records []clusterSetDNSRecord
	for _, export := range exports.Items {
		if !strings.HasPrefix(export.Metadata.Name, volsyncServicePrefix) {
			continue
		}

		clusterIP, err := c.run(ctx, cluster,
			"get", "service", export.Metadata.Name, "-n", export.Metadata.Namespace,
			"--ignore-not-found", "-o=jsonpath={.spec.clusterIP}",
		)
		if err != nil {
			return nil, fmt.Errorf("get service %s/%s: %w", export.Metadata.Namespace, export.Metadata.Name, err)
		}
		clusterIP = strings.TrimSpace(clusterIP)
		if clusterIP == "" || clusterIP == "None" {
			c.logger.Warn("skipping VolSync ServiceExport without a service ClusterIP",
				"cluster", cluster.Name, "namespace", export.Metadata.Namespace, "service", export.Metadata.Name)
			continue
		}

		records = append(records, clusterSetDNSRecord{
			cluster:   cluster.Name,
			bridge:    cluster.BridgeName,
			hostname:  fmt.Sprintf("%s.%s.svc.clusterset.local", export.Metadata.Name, export.Metadata.Namespace),
			clusterIP: clusterIP,
		})
	}
	return records, nil
}

func clusterSetPeerRecords(cluster rootconfig.ClusterConfig, records []clusterSetDNSRecord) ([]clusterSetDNSRecord, error) {
	byHostname := make(map[string]clusterSetDNSRecord)
	for _, record := range records {
		if record.cluster == cluster.Name || record.bridge != cluster.BridgeName {
			continue
		}
		if existing, ok := byHostname[record.hostname]; ok {
			if existing.clusterIP != record.clusterIP {
				return nil, fmt.Errorf("VolSync service %s is exported by multiple peer clusters with different ClusterIPs", record.hostname)
			}
			continue
		}
		byHostname[record.hostname] = record
	}

	peerRecords := make([]clusterSetDNSRecord, 0, len(byHostname))
	for _, record := range byHostname {
		peerRecords = append(peerRecords, record)
	}
	sort.Slice(peerRecords, func(i, j int) bool {
		return peerRecords[i].hostname < peerRecords[j].hostname
	})
	return peerRecords, nil
}

func clusterSetDNSBlock(records []clusterSetDNSRecord) coreDNSBlock {
	var hosts []string
	for _, record := range records {
		hosts = append(hosts, fmt.Sprintf("\t\t%s %s", record.clusterIP, record.hostname))
	}
	return coreDNSBlock{
		zone: "clusterset.local",
		body: fmt.Sprintf("\thosts {\n\t\tttl 30\n%s\n\t\tfallthrough\n\t}", strings.Join(hosts, "\n")),
	}
}

func (c *clusterSetDNS) update(ctx context.Context, cluster rootconfig.ClusterConfig, blocks []coreDNSBlock, records []clusterSetDNSRecord) error {
	corefile, err := c.run(ctx, cluster,
		"get", "configmap", "dns-default", "-n", "openshift-dns", "-o=jsonpath={.data.Corefile}",
	)
	if err != nil {
		return fmt.Errorf("get CoreDNS Corefile: %w", err)
	}

	updated, changed, err := reconcileManagedCorefile(corefile, volsyncDNSStart, volsyncDNSEnd, blocks)
	if err != nil {
		return err
	}
	if !changed {
		c.logger.Info("ClusterSet DNS is current", "cluster", cluster.Name, "records", recordCount(blocks))
		return nil
	}

	patch, err := json.Marshal(map[string]any{"data": map[string]string{"Corefile": updated}})
	if err != nil {
		return fmt.Errorf("encode ClusterSet DNS patch: %w", err)
	}
	c.logger.Info("updating ClusterSet DNS", "cluster", cluster.Name, "records", recordCount(blocks))
	if _, err := c.run(ctx, cluster,
		"patch", "configmap", "dns-default", "-n", "openshift-dns", "--type=merge", "-p", string(patch),
	); err != nil {
		return fmt.Errorf("patch CoreDNS Corefile: %w", err)
	}
	c.logger.Info("waiting for ClusterSet DNS answers", "cluster", cluster.Name)
	if err := c.waitForDNS(ctx, cluster, records); err != nil {
		return fmt.Errorf("wait for ClusterSet DNS answers: %w", err)
	}
	c.logger.Info("ClusterSet DNS answers are current", "cluster", cluster.Name)
	return nil
}

func (c *clusterSetDNS) waitForDNS(ctx context.Context, cluster rootconfig.ClusterConfig, records []clusterSetDNSRecord) error {
	if len(records) == 0 {
		return nil
	}

	pod, err := c.run(ctx, cluster,
		"get", "pods", "-n", "openshift-dns", "-l", "dns.operator.openshift.io/daemonset-dns=default",
		"-o=jsonpath={.items[?(@.status.phase==\"Running\")].metadata.name}",
	)
	if err != nil {
		return fmt.Errorf("get CoreDNS pod: %w", err)
	}
	pods := strings.Fields(pod)
	if len(pods) == 0 {
		return fmt.Errorf("no CoreDNS pod is available")
	}
	pod = pods[0]

	dnsIP, err := c.run(ctx, cluster,
		"get", "service", "dns-default", "-n", "openshift-dns", "-o=jsonpath={.spec.clusterIP}",
	)
	if err != nil {
		return fmt.Errorf("get CoreDNS service IP: %w", err)
	}
	dnsIP = strings.TrimSpace(dnsIP)
	if dnsIP == "" || dnsIP == "None" {
		return fmt.Errorf("CoreDNS service has no ClusterIP")
	}

	return support.Poll(ctx, clusterSetDNSPoll, clusterSetDNSTimeout, func(ctx context.Context) (bool, error) {
		for _, record := range records {
			answer, err := c.run(ctx, cluster,
				"exec", "-n", "openshift-dns", pod, "-c", "dns", "--",
				"dig", "+short", "+time=2", "+tries=1", "@"+dnsIP, record.hostname+".", "A",
			)
			if err != nil {
				return false, fmt.Errorf("query %s from dns-default pod %q: %w", record.hostname, pod, err)
			}
			answers := strings.Fields(answer)
			if len(answers) != 1 || answers[0] != record.clusterIP {
				return false, nil
			}
		}
		return true, nil
	})
}

func (c *clusterSetDNS) run(ctx context.Context, cluster rootconfig.ClusterConfig, args ...string) (string, error) {
	command := append([]string{"exec", cluster.NodeName, "oc", "--kubeconfig", internalKubeconfig}, args...)
	result, err := support.RunPodmanPrivileged(ctx, c.runner, command...)
	if err != nil {
		return "", err
	}
	return result.Stdout, nil
}

func recordCount(blocks []coreDNSBlock) int {
	if len(blocks) == 0 {
		return 0
	}
	return strings.Count(blocks[0].body, ".svc.clusterset.local")
}
