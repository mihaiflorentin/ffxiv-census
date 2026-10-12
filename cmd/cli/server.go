package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	httpserver "github.com/mihaiflorentin88/ffxiv-census/cmd/http"
	"github.com/mihaiflorentin88/ffxiv-census/container"
	"github.com/spf13/cobra"
)

var httpCmd = &cobra.Command{
	Use:   "server",
	Short: "Starts the web server",
	RunE: func(cmd *cobra.Command, args []string) error {
		start, _ := cmd.Flags().GetBool("start")
		if !start {
			return nil
		}

		port, _ := cmd.Flags().GetInt("port")
		poolSize, _ := cmd.Flags().GetInt("pool")
		certFile, _ := cmd.Flags().GetString("cert-file")
		keyFile, _ := cmd.Flags().GetString("key-file")
		profile, _ := cmd.Flags().GetBool("profile")
		maxRequests, _ := cmd.Flags().GetUint64("shutdown-max-requests")

		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()

		// Initialize PostgreSQL driver (triggers runtime migrations)
		if driver := container.Load.Database(); driver != nil {
			defer func() { _ = driver.Close() }()
		}

		if err := httpserver.StartServer(ctx, port, poolSize, certFile, keyFile, profile, maxRequests); err != nil {
			return fmt.Errorf("start http server: %w", err)
		}

		return nil
	},
}

func init() {
	rootCmd.AddCommand(httpCmd)
	initHTTPFlags()
}

func initHTTPFlags() {
	httpCmd.PersistentFlags().Bool("start", false, "Starts the web server")
	httpCmd.PersistentFlags().Int("port", 80, "Sets the server port")
	httpCmd.PersistentFlags().Int("pool", 5, "Sets the server pool size")
	httpCmd.PersistentFlags().String("cert-file", "", "Sets the server certificate file")
	httpCmd.PersistentFlags().String("key-file", "", "Sets the server key file")
	httpCmd.PersistentFlags().Bool("profile", false, "Starts the profiling server on port 6060")
	httpCmd.PersistentFlags().Uint64("shutdown-max-requests", 0, "Graceful shuts down the server after serving x requests. Default is set to 0 and it means that it's disabled.")
}
