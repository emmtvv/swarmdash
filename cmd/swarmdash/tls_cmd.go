package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"swarmdash/internal/pki"
)

func newTLSCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "tls",
		Short: "Manage the certificate authority securing admin<->agent traffic",
	}
	root.AddCommand(newTLSInitCmd())
	root.AddCommand(newTLSRenewCmd())
	return root
}

func newTLSInitCmd() *cobra.Command {
	var out string
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Generate a CA plus agent/admin certificates for mTLS",
		Long: `Generates a self-signed CA and issues one shared agent server
certificate and one shared admin client certificate from it, writing them
as PEM files into --out. Distribute ca.crt + agent.{crt,key} to the agent
service and ca.crt + admin.{crt,key} to the admin service (e.g. as Docker
secrets), then pass the resulting paths via --tls-cert/--tls-key/--tls-ca
(agent) or --agent-tls-cert/--agent-tls-key/--agent-tls-ca (admin).

ca.key (the CA's private key) is also written to --out - keep it around
and out of the files you distribute to agent/admin: it's what lets
'swarmdash tls renew' reissue leaf certs later without minting a new CA.
Re-running 'tls init' itself always mints a brand new CA and so
invalidates every previously issued certificate; treat it as a one-time
(per-cluster) setup step and use 'tls renew' for renewals instead.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := os.MkdirAll(out, 0o700); err != nil {
				return fmt.Errorf("create output dir: %w", err)
			}
			bundle, err := pki.Generate()
			if err != nil {
				return fmt.Errorf("generate certificates: %w", err)
			}
			if err := writeBundle(out, bundle); err != nil {
				return err
			}
			fmt.Printf("wrote CA and certificates to %s\n", out)
			return nil
		},
	}
	cmd.Flags().StringVar(&out, "out", "./certs", "output directory for the generated PEM files")
	return cmd
}

func newTLSRenewCmd() *cobra.Command {
	var out string
	cmd := &cobra.Command{
		Use:   "renew",
		Short: "Reissue agent/admin certificates from the existing CA",
		Long: `Reads ca.crt and ca.key from --out and issues fresh agent/admin
certificates from that same CA, overwriting agent.{crt,key} and
admin.{crt,key} in place. ca.crt/ca.key themselves are left untouched, so
already-distributed copies of ca.crt stay valid trust anchors - only the
renewed agent.{crt,key}/admin.{crt,key} need redistributing afterwards.

Use this instead of re-running 'tls init' to replace certificates before
they expire (leaf certs are valid 1y) without invalidating the CA and
every certificate issued from it.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			caCertPEM, err := os.ReadFile(filepath.Join(out, "ca.crt"))
			if err != nil {
				return fmt.Errorf("read ca.crt (run 'tls init' first if this cluster has none yet): %w", err)
			}
			caKeyPEM, err := os.ReadFile(filepath.Join(out, "ca.key"))
			if err != nil {
				return fmt.Errorf("read ca.key: %w", err)
			}
			bundle, err := pki.Reissue(caCertPEM, caKeyPEM)
			if err != nil {
				return fmt.Errorf("reissue certificates: %w", err)
			}
			// ca.crt/ca.key are unchanged (Reissue echoes back what was
			// passed in) - only rewrite the leaf files.
			if err := writeBundleFiles(out, map[string][]byte{
				"agent.crt": bundle.AgentCert,
				"agent.key": bundle.AgentKey,
				"admin.crt": bundle.AdminCert,
				"admin.key": bundle.AdminKey,
			}); err != nil {
				return err
			}
			fmt.Printf("renewed agent/admin certificates in %s (CA unchanged)\n", out)
			return nil
		},
	}
	cmd.Flags().StringVar(&out, "out", "./certs", "directory holding ca.crt/ca.key, and to write the renewed certificates into")
	return cmd
}

func writeBundle(out string, bundle *pki.Bundle) error {
	return writeBundleFiles(out, map[string][]byte{
		"ca.crt":    bundle.CACert,
		"ca.key":    bundle.CAKey,
		"agent.crt": bundle.AgentCert,
		"agent.key": bundle.AgentKey,
		"admin.crt": bundle.AdminCert,
		"admin.key": bundle.AdminKey,
	})
}

func writeBundleFiles(out string, files map[string][]byte) error {
	for name, data := range files {
		perm := os.FileMode(0o644)
		if filepath.Ext(name) == ".key" {
			perm = 0o600
		}
		if err := os.WriteFile(filepath.Join(out, name), data, perm); err != nil {
			return fmt.Errorf("write %s: %w", name, err)
		}
	}
	return nil
}
