// Package main is the entrypoint for the squirrel image-rewrite
// operator. It is a thin shim that parses flags, configures the
// controller-runtime logger, and hands off to internal/manager.Run
// with a signal-handled context. All testable logic lives in
// internal/manager.
package main

import (
	"fmt"
	"os"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/RemkoMolier/squirrel/internal/manager"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "squirrel-manager: %v\n", err)
		os.Exit(1)
	}
}

// run is the testable shape of main. It parses flags, installs the
// controller-runtime logger (zap dev-mode encoder; production
// deployments should switch via a --zap-* flag once the integration
// is wired), and calls manager.Run with the signal-handled context.
func run() error {
	opts, err := manager.ParseOptions(os.Args[1:])
	if err != nil {
		return fmt.Errorf("parse options: %w", err)
	}
	ctrl.SetLogger(zap.New(zap.UseDevMode(false)))
	return manager.Run(ctrl.SetupSignalHandler(), opts)
}
