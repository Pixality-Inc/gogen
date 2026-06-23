package main

import (
	"context"

	"github.com/pixality-inc/gogen/internal/config"
	"github.com/pixality-inc/gogen/internal/gen"
	"github.com/pixality-inc/golang-core/logger"
	"github.com/spf13/cobra"
)

func main() {
	ctx := context.Background()
	log := logger.New(logger.NewConfig(
		logger.DebugLevel,
		logger.TextFormat,
		true,
		true,
		true,
		true,
	))

	if err := logger.InitLogSpawner(log); err != nil {
		log.WithError(err).Fatal()
	}

	genConfig := config.LoadConfig()

	generator, err := gen.New(genConfig)
	if err != nil {
		log.WithError(err).Fatal()
	}

	if err := generator.Generate(ctx); err != nil {
		log.WithError(err).Fatal()
	}

	if true {
		return
	}

	// Root

	rootCmd := &cobra.Command{
		Use:   "gogen",
		Short: "Go tool for generating",
		Run: func(cmd *cobra.Command, args []string) {
			if err := cmd.Help(); err != nil {
				log.WithError(err).Fatal()
			}
		},
	}

	// Init

	{
		cmd := &cobra.Command{
			Use:   "init",
			Short: "Initialize a new project",
			Args:  cobra.ExactArgs(0),
			Run: func(cmd *cobra.Command, args []string) { //nolint:contextcheck
				log.Info("Initializing...")
			},
		}

		rootCmd.AddCommand(cmd)
	}

	// Api

	{
		cmd := &cobra.Command{
			Use:   "api",
			Short: "Generate API",
			Args:  cobra.ExactArgs(0),
			Run: func(cmd *cobra.Command, args []string) { //nolint:contextcheck
				log.Info("Generating API")
			},
		}

		rootCmd.AddCommand(cmd)
	}

	// Execute

	if err := rootCmd.ExecuteContext(ctx); err != nil {
		log.WithError(err).Fatal("failed to run command")
	}
}
