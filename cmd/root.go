package cmd

import (
	"fmt"
	"os"

	"github.com/barff/frank/internal/config"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var (
	cfgFile string
	cfg     *config.Config
)

var rootCmd = &cobra.Command{
	Use:   "frank",
	Short: "Launch isolated Claude Code development environments",
	Long: `Frank is a CLI tool for launching isolated Claude Code development
environments inside Docker containers.

It provides:
  - AWS SSO credential injection
  - Web-based terminal access via ttyd
  - Desktop notifications for Claude prompts
  - Git worktree management for parallel development
  - Support for Docker, Podman, and OrbStack`,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		return initConfig()
	},
}

// Execute runs the root command
func Execute() error {
	return rootCmd.Execute()
}

func init() {
	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default is $HOME/.config/frank/config.yaml)")
	rootCmd.PersistentFlags().String("runtime", "", "container runtime: docker, podman, orbstack (default: auto-detect)")
	rootCmd.PersistentFlags().BoolP("verbose", "v", false, "verbose output")

	viper.BindPFlag("runtime.preferred", rootCmd.PersistentFlags().Lookup("runtime"))
	viper.BindPFlag("logging.verbose", rootCmd.PersistentFlags().Lookup("verbose"))
}

func initConfig() error {
	var err error
	cfg, err = config.Load(cfgFile)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	return nil
}

// GetConfig returns the loaded configuration
func GetConfig() *config.Config {
	return cfg
}

// GetVerbose returns whether verbose mode is enabled
func GetVerbose() bool {
	return viper.GetBool("logging.verbose")
}

// PrintVerbose prints a message if verbose mode is enabled
func PrintVerbose(format string, args ...interface{}) {
	if GetVerbose() {
		fmt.Printf(format+"\n", args...)
	}
}

// PrintError prints an error message to stderr
func PrintError(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "Error: "+format+"\n", args...)
}
