package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"swarmdash/internal/admin"
	"swarmdash/internal/secretenv"
)

// newRotateClusterSecretCmd rotates the cluster secret without losing
// access to registry passwords, GitOps auth tokens, or the SSO client
// secret - all three are encrypted at rest with a key derived from the
// cluster secret (see internal/admin/crypto.go), which up to now had no
// rotation path at all: changing SWARMDASH_CLUSTER_SECRET and redeploying
// just made every stored credential permanently undecryptable. This
// re-encrypts them under the new secret's key first.
func newRotateClusterSecretCmd() *cobra.Command {
	var (
		oldSecret string
		newSecret string
		sf        storeFlags
	)

	cmd := &cobra.Command{
		Use:   "rotate-cluster-secret",
		Short: "Re-encrypt stored credentials for a new cluster secret",
		Long: `Rotating the cluster secret changes the key registry passwords, GitOps auth
tokens, and the SSO client secret are encrypted with at rest. Run this
against admin's store BEFORE rolling the new secret out to admin/agent, so
those credentials stay readable instead of becoming permanently
undecryptable garbage the moment the old secret is gone everywhere.

Steps:
  1. Stop admin (or accept it briefly serving stale-key errors on anything
     that touches an encrypted field while this runs).
  2. Run this command with --old-secret (the current SWARMDASH_CLUSTER_SECRET)
     and --new-secret (the value you're rotating to), pointed at the same
     store admin uses (--storage-driver/--data-dir or --mongo-*).
  3. Update SWARMDASH_CLUSTER_SECRET to --new-secret on every admin and
     agent instance and redeploy.

Safe to re-run if it fails partway through - see RotateClusterSecret's doc
comment for why.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if oldSecret == "" {
				resolved, err := secretenv.Resolve("SWARMDASH_OLD_CLUSTER_SECRET")
				if err != nil {
					return fmt.Errorf("read old cluster secret: %w", err)
				}
				oldSecret = resolved
			}
			if oldSecret == "" {
				return fmt.Errorf("--old-secret is required (or SWARMDASH_OLD_CLUSTER_SECRET): the cluster secret credentials are currently encrypted with")
			}
			if newSecret == "" {
				resolved, err := secretenv.Resolve("SWARMDASH_NEW_CLUSTER_SECRET")
				if err != nil {
					return fmt.Errorf("read new cluster secret: %w", err)
				}
				newSecret = resolved
			}
			if newSecret == "" {
				return fmt.Errorf("--new-secret is required (or SWARMDASH_NEW_CLUSTER_SECRET): the cluster secret you're rotating to")
			}
			if oldSecret == newSecret {
				return fmt.Errorf("--old-secret and --new-secret are the same value - nothing to rotate")
			}

			st, err := sf.open(cmd.Context())
			if err != nil {
				return fmt.Errorf("open store: %w", err)
			}
			defer st.Close()

			if err := admin.RotateClusterSecret(st, oldSecret, newSecret); err != nil {
				return fmt.Errorf("rotate cluster secret: %w", err)
			}

			fmt.Println("done - now update SWARMDASH_CLUSTER_SECRET to the new value on every admin and agent instance and redeploy")
			return nil
		},
	}

	cmd.Flags().StringVar(&oldSecret, "old-secret", "", "current cluster secret, that stored credentials are encrypted with (env SWARMDASH_OLD_CLUSTER_SECRET)")
	cmd.Flags().StringVar(&newSecret, "new-secret", "", "cluster secret to rotate to (env SWARMDASH_NEW_CLUSTER_SECRET)")
	sf.register(cmd)

	return cmd
}
