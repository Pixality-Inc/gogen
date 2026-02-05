package main

import (
	"context"
	"github.com/pixality-inc/golang-core/logger"
	"github.com/spf13/cobra"
)

func main() {
	ctx := context.Background()
	log := logger.NewDefault()

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

	// Execute

	if err := rootCmd.ExecuteContext(ctx); err != nil {
		log.WithError(err).Fatal("failed to run command")
	}
}
