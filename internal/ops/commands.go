package ops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"mt/internal/execx"

	"github.com/urfave/cli/v3"
)

// Command returns the DNS reconciler for cluster and peer lifecycle hooks.
func Command(logger *slog.Logger, runner execx.Runner) (*cli.Command, func(context.Context) error) {
	routes := &routeDNS{logger: logger, runner: runner}
	clusterSet := &clusterSetDNS{logger: logger, runner: runner}
	network := &cli.Command{
		Name:  "network",
		Usage: "Reconcile DNS records for running clusters",
		Flags: []cli.Flag{
			&cli.BoolFlag{Name: "sync-routes", Usage: "Sync wildcard route DNS entries"},
			&cli.BoolFlag{Name: "sync-clusterset", Usage: "Sync exported VolSync ClusterSet DNS entries"},
		},
		Before: func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
			if !cmd.Bool("sync-routes") && !cmd.Bool("sync-clusterset") {
				return ctx, fmt.Errorf("select --sync-routes, --sync-clusterset, or both")
			}
			return ctx, nil
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			var errs []error
			if cmd.Bool("sync-routes") {
				errs = append(errs, routes.reconcile(ctx))
			}
			if cmd.Bool("sync-clusterset") {
				errs = append(errs, clusterSet.reconcile(ctx))
			}
			return errors.Join(errs...)
		},
	}
	command := &cli.Command{
		Name:  "ops",
		Usage: "Operational utilities for running clusters",
		Action: func(ctx context.Context, cmd *cli.Command) error {
			return cli.ShowSubcommandHelp(cmd)
		},
		Commands: []*cli.Command{
			resourcesCommand(logger, runner),
			network,
			sudoersCommand(logger, runner),
			storageCommand(runner),
		},
	}
	return command, func(ctx context.Context) error {
		return errors.Join(routes.reconcile(ctx), clusterSet.reconcile(ctx))
	}
}
