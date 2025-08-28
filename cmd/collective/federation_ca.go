package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"collective/pkg/auth"
	"collective/pkg/federation"
	"github.com/spf13/cobra"
)

var federationCmd = &cobra.Command{
	Use:   "federation",
	Short: "Federation management commands",
	Long:  "Commands for managing federation invitations and member coordination",
}

var federationCACmd = &cobra.Command{
	Use:   "ca",
	Short: "Federation CA management",
	Long:  "Commands for managing the federation certificate authority hierarchy",
}

var federationCAInitCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize federation root CA",
	Long:  "Generate a new federation root CA certificate with ED25519 keys",
	RunE:  runFederationCAInit,
}

var federationCASignMemberCmd = &cobra.Command{
	Use:   "sign-member",
	Short: "Sign a member's intermediate CA",
	Long:  "Sign a member's intermediate CA certificate with the federation root CA",
	RunE:  runFederationCASignMember,
}

func AddFederationCommands(rootCmd *cobra.Command) {
	// Federation commands are now distributed across other command groups
	// CA commands remain in auth, invite commands are now top-level
}

func AddFederationCACommands(authCmd *cobra.Command) {
	authCmd.AddCommand(federationCACmd)
	federationCACmd.AddCommand(federationCAInitCmd)
	federationCACmd.AddCommand(federationCASignMemberCmd)

	// Init command flags
	federationCAInitCmd.Flags().String("domain", "collective.local", "Federation domain")
	federationCAInitCmd.Flags().String("output", "./federation-ca", "Output directory for CA files")
	federationCAInitCmd.Flags().Int("validity-years", 5, "Certificate validity in years")

	// Sign-member command flags
	federationCASignMemberCmd.Flags().String("member", "", "Member domain (e.g., alice.collective.local)")
	federationCASignMemberCmd.Flags().String("csr", "", "Path to member's CSR file")
	federationCASignMemberCmd.Flags().String("root-ca", "./federation-ca", "Path to federation root CA")
	federationCASignMemberCmd.Flags().String("output", "", "Output path for signed certificate")
	federationCASignMemberCmd.Flags().Int("validity-years", 3, "Certificate validity in years")
	federationCASignMemberCmd.MarkFlagRequired("member")
}

func runFederationCAInit(cmd *cobra.Command, args []string) error {
	domain, _ := cmd.Flags().GetString("domain")
	outputDir, _ := cmd.Flags().GetString("output")
	validityYears, _ := cmd.Flags().GetInt("validity-years")

	// Validate domain format
	addr := fmt.Sprintf("root@%s", domain)
	fedAddr, err := federation.ParseAddress(addr)
	if err != nil {
		return fmt.Errorf("invalid federation domain: %w", err)
	}

	// Create output directory
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	// Create enhanced cert manager for federation
	certMgr := auth.NewFederationCertManager(outputDir)

	// Generate root CA with federation extensions
	validity := time.Duration(validityYears) * 365 * 24 * time.Hour
	if err := certMgr.GenerateFederationRootCA(fedAddr.Domain, validity); err != nil {
		return fmt.Errorf("failed to generate federation root CA: %w", err)
	}

	fmt.Printf("✓ Federation root CA created successfully\n")
	fmt.Printf("  Domain: %s\n", domain)
	fmt.Printf("  CA Certificate: %s/ca.crt\n", outputDir)
	fmt.Printf("  CA Private Key: %s/ca.key\n", outputDir)
	fmt.Printf("  Validity: %d years\n", validityYears)

	return nil
}

func runFederationCASignMember(cmd *cobra.Command, args []string) error {
	memberDomain, _ := cmd.Flags().GetString("member")
	csrPath, _ := cmd.Flags().GetString("csr")
	rootCAPath, _ := cmd.Flags().GetString("root-ca")
	outputPath, _ := cmd.Flags().GetString("output")
	validityYears, _ := cmd.Flags().GetInt("validity-years")

	// Validate member domain
	addr := fmt.Sprintf("coord@%s", memberDomain)
	fedAddr, err := federation.ParseAddress(addr)
	if err != nil {
		return fmt.Errorf("invalid member domain: %w", err)
	}

	// Set default output path if not specified
	if outputPath == "" {
		outputPath = filepath.Join(".", fmt.Sprintf("%s-ca.crt", fedAddr.LocalPart))
	}

	// Load federation root CA
	certMgr := auth.NewFederationCertManager(rootCAPath)
	if err := certMgr.LoadCA(); err != nil {
		return fmt.Errorf("failed to load root CA: %w", err)
	}

	// If no CSR provided, generate intermediate CA directly
	validity := time.Duration(validityYears) * 365 * 24 * time.Hour
	if csrPath == "" {
		// Generate intermediate CA certificate for member
		if err := certMgr.GenerateMemberCA(memberDomain, validity, outputPath); err != nil {
			return fmt.Errorf("failed to generate member CA: %w", err)
		}
		fmt.Printf("✓ Member intermediate CA generated and signed\n")
	} else {
		// Sign existing CSR
		if err := certMgr.SignMemberCSR(csrPath, memberDomain, validity, outputPath); err != nil {
			return fmt.Errorf("failed to sign member CSR: %w", err)
		}
		fmt.Printf("✓ Member CSR signed successfully\n")
	}

	fmt.Printf("  Member Domain: %s\n", memberDomain)
	fmt.Printf("  Certificate: %s\n", outputPath)
	fmt.Printf("  Validity: %d years\n", validityYears)

	// Verify the certificate chain
	fmt.Printf("\nVerifying certificate chain...\n")
	if err := certMgr.VerifyChain(outputPath); err != nil {
		return fmt.Errorf("certificate chain verification failed: %w", err)
	}
	fmt.Printf("✓ Certificate chain valid\n")

	return nil
}
