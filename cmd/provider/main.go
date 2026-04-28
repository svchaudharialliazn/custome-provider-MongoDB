/*
Copyright 2020 The Crossplane Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package main
package main

import (
	"net/http"
	"os"
	"path/filepath"
	"time"

	"go.uber.org/zap/zapcore"
	"gopkg.in/alecthomas/kingpin.v2"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	crzap "sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/crossplane/crossplane-runtime/v2/pkg/controller"
	"github.com/crossplane/crossplane-runtime/v2/pkg/feature"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"
	"github.com/crossplane/crossplane-runtime/v2/pkg/ratelimiter"

	"github.com/svchaudharialliazn/swapnil-provider-mongodb/apis"
	gateway "github.com/svchaudharialliazn/swapnil-provider-mongodb/internal/controller"
)

// init sets the controller-runtime root logger as early as possible so no
// internal components start before a logger is configured.
func init() {
	// Default to dev logging when DEBUG or LOG_DEV environment variables are set.
	dev := os.Getenv("DEBUG") == "true" || os.Getenv("LOG_DEV") == "1"

	zopts := crzap.Options{
		Development: dev,
		TimeEncoder: zapcore.ISO8601TimeEncoder,
	}
	zl := crzap.New(crzap.UseFlagOptions(&zopts))

	// Set controller-runtime global logger.
	ctrl.SetLogger(zl)

	// Forward klog (client-go) to the same logr sink so all client-go messages show in pod logs.
	klog.SetLogger(zl)
}

// UseISO8601 sets the logger to use ISO8601 timestamp format when building via flags.
func UseISO8601() crzap.Opts {
	return func(o *crzap.Options) {
		o.TimeEncoder = zapcore.ISO8601TimeEncoder
	}
}

func main() {
	var (
		app            = kingpin.New(filepath.Base(os.Args[0]), "App Gateway support for Crossplane.").DefaultEnvars()
		debug          = app.Flag("debug", "Run with debug logging.").Short('d').Bool()
		leaderElection = app.Flag("leader-election", "Use leader election for the controller manager.").Short('l').Default("false").OverrideDefaultFromEnvar("LEADER_ELECTION").Bool()

		syncInterval     = app.Flag("sync", "How often all resources will be double-checked for drift from the desired state.").Short('s').Default("1h").Duration()
		pollInterval     = app.Flag("poll", "How often individual resources will be checked for drift from the desired state").Default("1m").Duration()
		maxReconcileRate = app.Flag("max-reconcile-rate", "The global maximum rate per second at which resources may checked for drift from the desired state.").Default("10").Int()
	)
	kingpin.MustParse(app.Parse(os.Args[1:]))

	// Build a Crossplane logger backed by controller-runtime logr.
	// Note: ctrl.SetLogger was already called in init(); we just reuse it here.
	zl := crzap.New(crzap.UseDevMode(*debug), UseISO8601())
	log := logging.NewLogrLogger(zl.WithName("provider-mongodb"))

	cfg, err := ctrl.GetConfig()
	kingpin.FatalIfError(err, "Cannot get API server rest config")

	mgr, err := ctrl.NewManager(ratelimiter.LimitRESTConfig(cfg, *maxReconcileRate), ctrl.Options{
		Cache: cache.Options{SyncPeriod: syncInterval},

		// Use Leases only with longer durations to avoid leader loss under load.
		LeaderElection:             *leaderElection,
		LeaderElectionID:           "crossplane-leader-election-provider-mongodb",
		LeaderElectionResourceLock: resourcelock.LeasesResourceLock,
		LeaseDuration:              func() *time.Duration { d := 60 * time.Second; return &d }(),
		RenewDeadline:              func() *time.Duration { d := 50 * time.Second; return &d }(),
		HealthProbeBindAddress:     ":8081",
	})
	kingpin.FatalIfError(err, "Cannot create controller manager")
	kingpin.FatalIfError(apis.AddToScheme(mgr.GetScheme()), "Cannot add App Gateway APIs to scheme")

	o := controller.Options{
		Logger:                  log,
		MaxConcurrentReconciles: *maxReconcileRate,
		PollInterval:            *pollInterval,
		GlobalRateLimiter:       ratelimiter.NewGlobal(*maxReconcileRate),
		Features:                &feature.Flags{},
	}

	kingpin.FatalIfError(gateway.Setup(mgr, o), "Cannot setup App Gateway controllers")

	// Optional health/ready probes to integrate with k8s health endpoints.
	_ = mgr.AddHealthzCheck("healthz", func(_ *http.Request) error { return nil })
	_ = mgr.AddReadyzCheck("readyz", func(_ *http.Request) error { return nil })

	log.Info("Starting controller manager")
	kingpin.FatalIfError(mgr.Start(ctrl.SetupSignalHandler()), "Cannot start controller manager")
}
