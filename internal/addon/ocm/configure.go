package ocm

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	rootconfig "dfmicro/internal/config"
	"dfmicro/internal/execx"
	"dfmicro/internal/support"
)

//go:embed assets/application-manager.yaml
var applicationManagerManifest string

type hubAddonSetup uint8

const (
	hubAddonApplicationManager hubAddonSetup = iota
	hubAddonArgoCD
	hubAddonGovernancePolicyFramework
)

type ocmAddon struct {
	hubSetup   hubAddonSetup
	spokeAddon string
}

// The hub and spoke CLIs use different names for some addons.
var supportedOCMAddons = map[string]ocmAddon{
	"application-manager":         {hubSetup: hubAddonApplicationManager, spokeAddon: "application-manager"},
	"argocd":                      {hubSetup: hubAddonArgoCD},
	"config-policy-controller":    {hubSetup: hubAddonGovernancePolicyFramework, spokeAddon: "config-policy-controller"},
	"governance-policy-framework": {hubSetup: hubAddonGovernancePolicyFramework, spokeAddon: "governance-policy-framework"},
}

var supportedOCMAddonNames = []string{
	"application-manager",
	"argocd",
	"config-policy-controller",
	"governance-policy-framework",
}

func (o *ocm) listAddons() error {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "DFMICRO ADDON\tHUB SETUP\tSPOKE SETUP")
	for _, name := range supportedOCMAddonNames {
		addon := supportedOCMAddons[name]
		hubSetup := ""
		switch addon.hubSetup {
		case hubAddonApplicationManager:
			hubSetup = "embedded bundle"
		case hubAddonArgoCD:
			hubSetup = "Argo CD v" + argocdVersion
		case hubAddonGovernancePolicyFramework:
			hubSetup = "governance-policy-framework"
		}
		spokeSetup := addon.spokeAddon
		if addon.hubSetup == hubAddonArgoCD {
			spokeSetup = "Argo CD cluster Secrets"
		} else if spokeSetup == "" {
			spokeSetup = "hub only"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", name, hubSetup, spokeSetup)
	}
	return w.Flush()
}

func validateOCMSpokes(names []string) error {
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			return fmt.Errorf("spoke name cannot be empty")
		}
		if _, ok := seen[name]; ok {
			return fmt.Errorf("duplicate spoke name: %s", name)
		}
		seen[name] = struct{}{}
	}
	return nil
}

func validateOCMAddons(names []string) error {
	for _, name := range names {
		name = strings.TrimSpace(name)
		if _, ok := supportedOCMAddons[name]; !ok {
			return fmt.Errorf("unsupported OCM addon %q; supported addons: %s", name, strings.Join(supportedOCMAddonNames, ", "))
		}
	}
	return nil
}

func (o *ocm) configure(ctx context.Context, hubName string, spokeNames, addonNames []string) error {
	for i, name := range spokeNames {
		spokeNames[i] = strings.TrimSpace(name)
	}

	hubKubeconfig, err := rootconfig.Kubeconfig(hubName)
	if err != nil {
		return fmt.Errorf("could not load kubeconfig for hub cluster %q: %w", hubName, err)
	}

	var hubAddons []string
	spokeAddons := make([]string, 0, len(addonNames))
	hubSeen := make(map[string]struct{})
	spokeAddonSeen := make(map[string]struct{})
	installApplicationManager := false
	installArgoCD := false
	installGPFOnSpokes := false
	for _, name := range addonNames {
		name = strings.TrimSpace(name)
		addon := supportedOCMAddons[name]
		switch addon.hubSetup {
		case hubAddonApplicationManager:
			installApplicationManager = true
		case hubAddonArgoCD:
			installArgoCD = true
		case hubAddonGovernancePolicyFramework:
			if _, ok := hubSeen["governance-policy-framework"]; !ok {
				hubSeen["governance-policy-framework"] = struct{}{}
				hubAddons = append(hubAddons, "governance-policy-framework")
			}
		}
		if addon.spokeAddon != "" {
			if _, ok := spokeAddonSeen[addon.spokeAddon]; !ok {
				spokeAddonSeen[addon.spokeAddon] = struct{}{}
				spokeAddons = append(spokeAddons, addon.spokeAddon)
			}
			installGPFOnSpokes = installGPFOnSpokes || addon.spokeAddon == "governance-policy-framework"
		}
	}

	spokeKubeconfigs := make(map[string]string, len(spokeNames))
	for _, spoke := range spokeNames {
		kubeconfig, err := rootconfig.Kubeconfig(spoke)
		if err != nil {
			return fmt.Errorf("could not load kubeconfig for spoke cluster %q: %w", spoke, err)
		}
		spokeKubeconfigs[spoke] = kubeconfig
	}

	clusteradm := support.ClusteradmPath()
	var tempDir string
	var hubPrivateKubeconfig, contextName string
	if len(hubAddons) > 0 || len(spokeNames) > 0 {
		tempDir, err = os.MkdirTemp("", "dfmicro-ocm-")
		if err != nil {
			return fmt.Errorf("create temporary OCM config directory: %w", err)
		}
		defer os.RemoveAll(tempDir)
		hubPrivateKubeconfig = filepath.Join(tempDir, "hub-kubeconfig")
		contextName, err = privateContext(ctx, o.runner, hubKubeconfig, hubPrivateKubeconfig)
		if err != nil {
			return err
		}
	}

	if installApplicationManager {
		o.logger.Info("installing OCM addon on hub", "addon", "application-manager", "cluster", hubName)
		if err := support.ApplyYAML(ctx, o.runner, support.KubeClient, hubKubeconfig, applicationManagerManifest); err != nil {
			return fmt.Errorf("install application-manager on hub: %w", err)
		}
	}
	if installArgoCD {
		o.logger.Info("installing Argo CD on hub", "cluster", hubName, "version", argocdVersion)
		if err := o.installArgoCD(ctx, hubKubeconfig); err != nil {
			return fmt.Errorf("install Argo CD on hub: %w", err)
		}
	}
	if len(hubAddons) > 0 {
		o.logger.Info("installing OCM addons on hub", "addons", hubAddons, "cluster", hubName)
		args := []string{
			"--kubeconfig", hubPrivateKubeconfig, "--context", contextName,
			"install", "hub-addon", "--names=" + strings.Join(hubAddons, ","),
		}
		if _, err := execx.Run(ctx, o.runner, clusteradm, args...); err != nil {
			return fmt.Errorf("install OCM addons on hub: %w", err)
		}
	}

	if len(spokeNames) == 0 {
		return nil
	}

	for i, spoke := range spokeNames {
		managedCluster, err := execx.Run(ctx, o.runner, support.KubeClient,
			"--kubeconfig", hubPrivateKubeconfig, "--context", contextName,
			"get", "managedcluster", spoke, "--ignore-not-found", "-o", "name")
		if err != nil {
			return fmt.Errorf("check OCM spoke %q: %w", spoke, err)
		}
		if strings.TrimSpace(managedCluster.Stdout) != "" {
			o.logger.Info("OCM spoke already joined", "hub", hubName, "spoke", spoke)
			continue
		}

		spokePrivateKubeconfig := filepath.Join(tempDir, fmt.Sprintf("spoke-%d-kubeconfig", i))
		spokeContext, err := privateContext(ctx, o.runner, spokeKubeconfigs[spoke], spokePrivateKubeconfig)
		if err != nil {
			return fmt.Errorf("select private API context for spoke %q: %w", spoke, err)
		}

		tokenResult, err := execx.Run(ctx, o.runner, clusteradm, "--kubeconfig", hubPrivateKubeconfig,
			"--context", contextName, "get", "token", "--output=json")
		if err != nil {
			return fmt.Errorf("get OCM token for spoke %q: %w", spoke, err)
		}
		var token struct {
			HubToken     string `json:"hub-token"`
			HubAPIServer string `json:"hub-apiserver"`
		}
		if err := json.Unmarshal([]byte(tokenResult.Stdout), &token); err != nil {
			return fmt.Errorf("decode OCM token for spoke %q: %w", spoke, err)
		}
		if token.HubToken == "" || token.HubAPIServer == "" {
			return fmt.Errorf("OCM token response for spoke %q is missing hub-token or hub-apiserver", spoke)
		}
		o.logger.Info("joining OCM spoke", "hub", hubName, "spoke", spoke, "hub-api-server", token.HubAPIServer, "hub-context", contextName, "spoke-context", spokeContext)
		args := []string{
			"--kubeconfig", spokePrivateKubeconfig,
			"join",
			"--hub-token", token.HubToken,
			"--hub-apiserver", token.HubAPIServer,
			"--cluster-name", spoke,
			"--singleton",
			"--wait",
		}
		if err := o.runner.RunInteractive(ctx, clusteradm, args...); err != nil {
			return fmt.Errorf("join OCM spoke %q: %w", spoke, err)
		}
		o.logger.Info("OCM spoke joined", "hub", hubName, "spoke", spoke)
	}
	for _, spoke := range spokeNames {
		if _, err := execx.Run(ctx, o.runner, support.KubeClient,
			"--kubeconfig", hubPrivateKubeconfig, "--context", contextName,
			"label", "managedcluster", spoke, "name="+spoke, "--overwrite"); err != nil {
			return fmt.Errorf("label OCM spoke %q: %w", spoke, err)
		}
	}

	if installGPFOnSpokes {
		server, err := execx.Run(ctx, o.runner, support.KubeClient,
			"--kubeconfig", hubPrivateKubeconfig, "--context", contextName,
			"config", "view", "--minify", "-o", "jsonpath={.clusters[0].cluster.server}")
		if err != nil {
			return fmt.Errorf("get private hub API endpoint: %w", err)
		}
		hubAPIServer := strings.TrimSpace(server.Stdout)
		for _, spoke := range spokeNames {
			o.logger.Info("allowing GPF egress to hub API", "hub", hubName, "spoke", spoke, "hub-api-server", hubAPIServer)
			if err := o.allowGPFHubAPI(ctx, hubPrivateKubeconfig, spoke, hubAPIServer); err != nil {
				return fmt.Errorf("allow GPF access to hub API from spoke %q: %w", spoke, err)
			}
		}
	}

	if len(spokeAddons) > 0 {
		o.logger.Info("enabling OCM addons on spokes", "addons", spokeAddons, "spokes", spokeNames)
		args := []string{
			"--kubeconfig", hubPrivateKubeconfig,
			"--context", contextName,
			"addon", "enable",
			"--names=" + strings.Join(spokeAddons, ","),
			"--clusters=" + strings.Join(spokeNames, ","),
		}
		if _, err := execx.Run(ctx, o.runner, clusteradm, args...); err != nil {
			return fmt.Errorf("enable OCM addons on spokes: %w", err)
		}
	}
	if installArgoCD {
		for i, spoke := range spokeNames {
			spokePrivateKubeconfig := filepath.Join(tempDir, fmt.Sprintf("argocd-spoke-%d-kubeconfig", i))
			if _, err := privateContext(ctx, o.runner, spokeKubeconfigs[spoke], spokePrivateKubeconfig); err != nil {
				return fmt.Errorf("select private API context for Argo CD cluster %q: %w", spoke, err)
			}
			o.logger.Info("registering cluster with Argo CD", "hub", hubName, "cluster", spoke)
			if err := o.registerArgoCDCluster(ctx, hubKubeconfig, spoke, spokePrivateKubeconfig); err != nil {
				return fmt.Errorf("register cluster %q with Argo CD: %w", spoke, err)
			}
		}
	}

	return nil
}
