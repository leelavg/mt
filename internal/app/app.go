package app

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"

	"mt/internal/addon"
	"mt/internal/buildinfo"
	"mt/internal/cluster"
	rootconfig "mt/internal/config"
	"mt/internal/docs"
	"mt/internal/execx"
	"mt/internal/network"
	"mt/internal/node"
	"mt/internal/ops"
	"mt/internal/support"

	"github.com/urfave/cli/v3"
)

func docsCommand() *cli.Command {
	return &cli.Command{
		Name:      "docs",
		Usage:     "Print full command reference as markdown",
		UsageText: "mt docs > cli.md",
		MutuallyExclusiveFlags: []cli.MutuallyExclusiveFlags{
			{
				Flags: [][]cli.Flag{
					{&cli.BoolFlag{
						Name:  "examples",
						Usage: "Print command examples",
					}},
					{&cli.BoolFlag{
						Name:  "skill",
						Usage: "Print agent guidance for operating mt",
					}},
					{&cli.BoolFlag{
						Name:   "devlog",
						Usage:  "Print development log",
						Hidden: true,
					}},
				},
			},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			if cmd.Bool("devlog") {
				_, err := os.Stdout.WriteString(docs.Devlog)
				return err
			}
			if cmd.Bool("skill") {
				_, err := os.Stdout.WriteString(docs.Skill)
				return err
			}
			if cmd.Bool("examples") {
				_, err := os.Stdout.WriteString(docs.Examples)
				return err
			}
			_, err := os.Stdout.WriteString(docs.CLI)
			return err
		},
	}
}

func configCommand() *cli.Command {
	return &cli.Command{
		Name:      "config",
		Usage:     "Print the embedded default configuration as JSON",
		UsageText: "Shows the compiled-in defaults for cluster name, image, network subnet, LVM size, and more.\nThese are the values used when flags are omitted on any command.",
		Action: func(ctx context.Context, cmd *cli.Command) error {
			cfg := rootconfig.Load()

			data, err := json.MarshalIndent(cfg, "", "  ")
			if err != nil {
				return err
			}
			data = append(data, '\n')

			_, err = os.Stdout.Write(data)
			return err
		},
	}
}

func Command(logger *slog.Logger, runner execx.Runner) *cli.Command {
	opsCommand, reconcileNetworkDNS := ops.Command(logger, runner)
	cmd := &cli.Command{
		Name:    support.BinaryName,
		Usage:   "Run MicroShift clusters in rootful Podman containers",
		Version: buildinfo.String(),
		Description: `mt creates and manages single- and multi-node MicroShift clusters inside rootful Podman containers.
Each cluster gets its own Podman network and a loop-device backed LVM thin pool for TopoLVM storage.

Verified on: Linux (Fedora / RHEL)
Best-effort support: macOS (requires rootful Podman machine via 'podman machine init --rootful')

Quick start:
  mt ops sudoers create                   # one-time: passwordless sudo for cluster tools
  mt cluster create                       # create cluster with default name
  mt cluster kubeconfig > ~/.kube/config  # overwrites kubeconfig!
  kubectl get nodes
  mt cluster delete                       # tear everything down`,
		EnableShellCompletion: true,
		ConfigureShellCompletionCommand: func(cmd *cli.Command) {
			cmd.Hidden = false
		},
		Action: support.UnknownSubcommand,
		Commands: []*cli.Command{
			addon.Command(logger, runner),
			cluster.Command(logger, runner, reconcileNetworkDNS),
			node.Command(logger, runner),
			configCommand(),
			docsCommand(),
			network.Command(logger, runner, reconcileNetworkDNS),
			opsCommand,
		},
	}

	support.SortCommand(cmd)
	return cmd
}
