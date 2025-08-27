package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"collective/pkg/config"
	"collective/pkg/protocol"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/lipgloss/table"
	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

// StatusFlags holds all status command flags
type StatusFlags struct {
	Coordinator string
	JSON        bool
	Verbose     bool
	Compact     bool

	// Specific sections
	ShowNodes   bool
	ShowPeers   bool
	ShowStorage bool
	ShowFiles   bool
	ShowTree    bool
	ShowNetwork bool
	ShowSummary bool

	// Filter options
	NodeFilter  string // Filter nodes by member
	HealthyOnly bool   // Only show healthy nodes/peers

	// Output options
	NoColor       bool
	Watch         bool
	WatchInterval int

	// TLS/Auth
	CertPath string
	KeyPath  string
	CAPath   string
	Insecure bool
}

func enhancedStatusCmd() *cobra.Command {
	flags := &StatusFlags{}

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Display collective cluster status with customizable output",
		Long: `Display the status of the collective storage cluster.
		
Examples:
  # Show full status (default)
  collective status
  
  # Show only nodes
  collective status --nodes
  
  # Show only storage information
  collective status --storage
  
  # Compact view with essential info
  collective status --compact
  
  # Watch status updates every 5 seconds
  collective status --watch --interval 5
  
  # Show only healthy nodes from alice
  collective status --nodes --healthy --filter alice
  
  # Get JSON output for specific sections
  collective status --json --nodes --storage`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runEnhancedStatus(flags)
		},
	}

	// Connection flags
	cmd.Flags().StringVar(&flags.Coordinator, "coordinator", "localhost:8001", "Coordinator address")

	// Output format flags
	cmd.Flags().BoolVarP(&flags.JSON, "json", "j", false, "Output in JSON format")
	cmd.Flags().BoolVarP(&flags.Verbose, "verbose", "v", false, "Verbose output with additional details")
	cmd.Flags().BoolVar(&flags.Compact, "compact", false, "Compact output with essential information only")
	cmd.Flags().BoolVar(&flags.NoColor, "no-color", false, "Disable colored output")

	// Section selection flags (if none specified, show all)
	cmd.Flags().BoolVarP(&flags.ShowNodes, "nodes", "n", false, "Show storage nodes information")
	cmd.Flags().BoolVarP(&flags.ShowPeers, "peers", "p", false, "Show coordinator peers information")
	cmd.Flags().BoolVarP(&flags.ShowStorage, "storage", "s", false, "Show storage capacity and usage")
	cmd.Flags().BoolVarP(&flags.ShowFiles, "files", "f", false, "Show file statistics")
	cmd.Flags().BoolVarP(&flags.ShowTree, "tree", "t", false, "Show file tree structure")
	cmd.Flags().BoolVar(&flags.ShowNetwork, "network", false, "Show network information")
	cmd.Flags().BoolVar(&flags.ShowSummary, "summary", false, "Show summary statistics only")

	// Filter flags
	cmd.Flags().StringVar(&flags.NodeFilter, "filter", "", "Filter nodes by member ID")
	cmd.Flags().BoolVar(&flags.HealthyOnly, "healthy", false, "Show only healthy nodes and peers")

	// Watch mode
	cmd.Flags().BoolVarP(&flags.Watch, "watch", "w", false, "Watch status updates continuously")
	cmd.Flags().IntVar(&flags.WatchInterval, "interval", 2, "Watch interval in seconds")

	// TLS/Auth options
	cmd.Flags().StringVar(&flags.CertPath, "cert", "", "Path to client certificate")
	cmd.Flags().StringVar(&flags.KeyPath, "key", "", "Path to client key")
	cmd.Flags().StringVar(&flags.CAPath, "ca", "", "Path to CA certificate")
	cmd.Flags().BoolVar(&flags.Insecure, "insecure", false, "Skip TLS verification")

	return cmd
}

func runEnhancedStatus(flags *StatusFlags) error {
	// If watch mode, run in loop
	if flags.Watch {
		return watchStatus(flags)
	}

	// Single run
	return displayStatus(flags)
}

func watchStatus(flags *StatusFlags) error {
	for {
		// Clear screen
		fmt.Print("\033[H\033[2J")

		if err := displayStatus(flags); err != nil {
			fmt.Printf("Error: %v\n", err)
		}

		time.Sleep(time.Duration(flags.WatchInterval) * time.Second)
	}
}

func displayStatus(flags *StatusFlags) error {
	// Try to load client config if no explicit connection params provided
	var connConfig *config.ConnectionConfig
	if flags.CertPath == "" && flags.KeyPath == "" && !flags.Insecure {
		cfg, err := config.LoadClientConfig()
		if err == nil && cfg.CurrentContext != "" {
			// Use context configuration
			connConfig, _ = cfg.GetConnectionConfig("")
			if connConfig != nil {
				// Apply context settings if not overridden
				if flags.Coordinator == "localhost:8001" { // Default value
					flags.Coordinator = connConfig.Coordinator
				}
				if flags.CAPath == "" {
					flags.CAPath = connConfig.CAPath
				}
				if flags.CertPath == "" {
					flags.CertPath = connConfig.CertPath
				}
				if flags.KeyPath == "" {
					flags.KeyPath = connConfig.KeyPath
				}
			}
		}
	}

	// Setup connection options
	var dialOpts []grpc.DialOption

	if flags.CertPath != "" && flags.KeyPath != "" {
		// Use client certificates
		cert, err := tls.LoadX509KeyPair(flags.CertPath, flags.KeyPath)
		if err != nil {
			return fmt.Errorf("failed to load client certificates: %w", err)
		}

		tlsConfig := &tls.Config{
			Certificates: []tls.Certificate{cert},
		}

		if flags.CAPath != "" {
			// Load CA if provided
			caCert, err := os.ReadFile(flags.CAPath)
			if err != nil {
				return fmt.Errorf("failed to read CA certificate: %w", err)
			}

			caPool := x509.NewCertPool()
			if !caPool.AppendCertsFromPEM(caCert) {
				return fmt.Errorf("failed to parse CA certificate")
			}
			tlsConfig.RootCAs = caPool
		}

		if flags.Insecure {
			tlsConfig.InsecureSkipVerify = true
		}

		creds := credentials.NewTLS(tlsConfig)
		dialOpts = append(dialOpts, grpc.WithTransportCredentials(creds))
	} else if flags.Insecure || (flags.CertPath == "" && flags.KeyPath == "") {
		// Default to insecure for backward compatibility when no certs provided
		dialOpts = append(dialOpts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	} else {
		return fmt.Errorf("both --cert and --key must be provided for TLS authentication")
	}

	// Connect to coordinator
	conn, err := grpc.Dial(flags.Coordinator, dialOpts...)
	if err != nil {
		return handleConnectionError(err, flags)
	}
	defer conn.Close()

	client := protocol.NewCoordinatorClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Get status
	statusResp, err := client.GetStatus(ctx, &protocol.GetStatusRequest{})
	if err != nil {
		return handleStatusError(err, flags)
	}

	// If no specific sections requested, show all
	showAll := !flags.ShowNodes && !flags.ShowPeers && !flags.ShowStorage &&
		!flags.ShowFiles && !flags.ShowTree && !flags.ShowNetwork && !flags.ShowSummary

	if flags.JSON {
		return outputJSON(statusResp, flags, showAll)
	}

	// Terminal output
	if flags.Compact {
		return outputCompact(statusResp, flags)
	}

	// Use styled output if colors are enabled
	if !flags.NoColor {
		if flags.ShowSummary || showAll {
			styledOutputSummary(statusResp, flags)
		}

		if flags.ShowPeers || showAll {
			styledOutputPeers(statusResp, flags)
		}

		if flags.ShowNodes || showAll {
			styledOutputNodes(statusResp, flags)
		}

		if flags.ShowStorage || showAll {
			styledOutputStorage(statusResp, flags)
		}

		if flags.ShowFiles || showAll {
			styledOutputFiles(statusResp, flags)
		}

		if flags.ShowTree {
			outputTree(statusResp, flags)
		}

		if flags.ShowNetwork || (showAll && flags.Verbose) {
			styledOutputNetwork(statusResp, flags)
		}
	} else {
		// Plain output without colors
		if flags.ShowSummary || showAll {
			outputSummary(statusResp, flags)
		}

		if flags.ShowPeers || showAll {
			outputPeers(statusResp, flags)
		}

		if flags.ShowNodes || showAll {
			outputNodes(statusResp, flags)
		}

		if flags.ShowStorage || showAll {
			outputStorage(statusResp, flags)
		}

		if flags.ShowFiles || showAll {
			outputFiles(statusResp, flags)
		}

		if flags.ShowTree {
			outputTree(statusResp, flags)
		}

		if flags.ShowNetwork || (showAll && flags.Verbose) {
			outputNetwork(statusResp, flags)
		}
	}

	return nil
}

func outputJSON(status *protocol.GetStatusResponse, flags *StatusFlags, showAll bool) error {
	output := make(map[string]interface{})

	output["timestamp"] = time.Now().Format(time.RFC3339)
	output["coordinator"] = status.MemberId

	if flags.ShowNodes || showAll {
		nodes := filterNodes(status, flags)
		output["nodes"] = map[string]interface{}{
			"local":  nodes.local,
			"remote": nodes.remote,
			"total":  len(nodes.local) + len(nodes.remote),
		}
	}

	if flags.ShowPeers || showAll {
		output["peers"] = filterPeers(status.Peers, flags)
	}

	if flags.ShowStorage || showAll {
		output["storage"] = map[string]interface{}{
			"total_bytes":     status.TotalStorageCapacity,
			"used_bytes":      status.UsedStorageCapacity,
			"available_bytes": status.TotalStorageCapacity - status.UsedStorageCapacity,
			"utilization":     fmt.Sprintf("%.1f%%", float64(status.UsedStorageCapacity)*100/float64(status.TotalStorageCapacity)),
		}
	}

	if flags.ShowFiles || showAll {
		output["files"] = map[string]interface{}{
			"total": status.TotalFiles,
		}
	}

	jsonBytes, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		return err
	}

	fmt.Println(string(jsonBytes))
	return nil
}

func outputCompact(status *protocol.GetStatusResponse, flags *StatusFlags) error {
	style := lipgloss.NewStyle().Foreground(lipgloss.Color("205"))

	fmt.Printf("%s | Nodes: %d | Storage: %.1f/%.1f GB (%.1f%%) | Files: %d\n",
		style.Render(status.MemberId),
		len(status.LocalNodes)+len(status.RemoteNodes),
		float64(status.UsedStorageCapacity)/(1024*1024*1024),
		float64(status.TotalStorageCapacity)/(1024*1024*1024),
		float64(status.UsedStorageCapacity)*100/float64(status.TotalStorageCapacity),
		status.TotalFiles,
	)

	return nil
}

func outputSummary(status *protocol.GetStatusResponse, flags *StatusFlags) {
	if flags.NoColor {
		fmt.Println("COLLECTIVE SUMMARY")
		fmt.Println("==================")
	} else {
		headerStyle := lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("205")).
			MarginBottom(1)

		fmt.Println(headerStyle.Render("📊 COLLECTIVE SUMMARY"))
	}

	// Create summary table
	t := table.New().
		Border(lipgloss.NormalBorder()).
		BorderStyle(lipgloss.NewStyle().Foreground(lipgloss.Color("240")))

	// Calculate metrics
	totalNodes := len(status.LocalNodes) + len(status.RemoteNodes)
	totalPeers := len(status.Peers)
	utilization := float64(status.UsedStorageCapacity) * 100 / float64(status.TotalStorageCapacity)

	t.Row("Coordinator", status.MemberId)
	t.Row("Total Peers", fmt.Sprintf("%d", totalPeers))
	t.Row("Total Nodes", fmt.Sprintf("%d", totalNodes))
	t.Row("Total Capacity", formatBytes(status.TotalStorageCapacity))
	t.Row("Used Storage", formatBytes(status.UsedStorageCapacity))
	t.Row("Utilization", fmt.Sprintf("%.1f%%", utilization))
	t.Row("Total Files", fmt.Sprintf("%d", status.TotalFiles))

	fmt.Println(t)
	fmt.Println()
}

func outputNodes(status *protocol.GetStatusResponse, flags *StatusFlags) {
	nodes := filterNodes(status, flags)

	if len(nodes.local) == 0 && len(nodes.remote) == 0 {
		return
	}

	if !flags.NoColor {
		fmt.Println(lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("206")).Render("💾 STORAGE NODES"))
	} else {
		fmt.Println("STORAGE NODES")
	}

	t := table.New().
		Border(lipgloss.NormalBorder()).
		BorderStyle(lipgloss.NewStyle().Foreground(lipgloss.Color("240"))).
		Headers("NODE ID", "MEMBER", "ADDRESS", "STATUS", "CAPACITY", "USED", "USAGE")

	// Add local nodes
	for _, node := range nodes.local {
		status := "🟢 HEALTHY"
		if !node.IsHealthy {
			status = "🔴 UNHEALTHY"
		}

		usage := float64(node.UsedCapacity) * 100 / float64(node.TotalCapacity)
		usageBar := createUsageBar(usage, 10)

		t.Row(
			node.NodeId,
			node.MemberId,
			node.Address,
			status,
			formatBytes(node.TotalCapacity),
			formatBytes(node.UsedCapacity),
			fmt.Sprintf("%s %.1f%%", usageBar, usage),
		)
	}

	// Add remote nodes
	for _, node := range nodes.remote {
		status := "🟢 HEALTHY"
		if !node.IsHealthy {
			status = "🔴 UNHEALTHY"
		}

		usage := float64(node.UsedCapacity) * 100 / float64(node.TotalCapacity)
		usageBar := createUsageBar(usage, 10)

		t.Row(
			node.NodeId,
			node.MemberId,
			node.Address,
			status,
			formatBytes(node.TotalCapacity),
			formatBytes(node.UsedCapacity),
			fmt.Sprintf("%s %.1f%%", usageBar, usage),
		)
	}

	fmt.Println(t)
	fmt.Println()
}

func outputPeers(status *protocol.GetStatusResponse, flags *StatusFlags) {
	peers := filterPeers(status.Peers, flags)

	if len(peers) == 0 {
		return
	}

	if !flags.NoColor {
		fmt.Println(lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("207")).Render("📡 COORDINATOR PEERS"))
	} else {
		fmt.Println("COORDINATOR PEERS")
	}

	t := table.New().
		Border(lipgloss.NormalBorder()).
		BorderStyle(lipgloss.NewStyle().Foreground(lipgloss.Color("240"))).
		Headers("MEMBER ID", "ADDRESS", "STATUS", "NODES", "CAPACITY", "USED")

	for _, peer := range peers {
		status := "🟢 CONNECTED"
		if !peer.IsHealthy {
			status = "🔴 DISCONNECTED"
		}

		t.Row(
			peer.MemberId,
			peer.Address,
			status,
			"-", // Node count not available in PeerInfo
			"-", // Capacity not available in PeerInfo
			"-", // Used capacity not available in PeerInfo
		)
	}

	fmt.Println(t)
	fmt.Println()
}

func outputStorage(status *protocol.GetStatusResponse, flags *StatusFlags) {
	if !flags.NoColor {
		fmt.Println(lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("208")).Render("💾 STORAGE OVERVIEW"))
	} else {
		fmt.Println("STORAGE OVERVIEW")
	}

	utilization := float64(status.UsedStorageCapacity) * 100 / float64(status.TotalStorageCapacity)
	usageBar := createUsageBar(utilization, 30)

	fmt.Printf("Total Capacity: %s\n", formatBytes(status.TotalStorageCapacity))
	fmt.Printf("Used Storage:   %s\n", formatBytes(status.UsedStorageCapacity))
	fmt.Printf("Available:      %s\n", formatBytes(status.TotalStorageCapacity-status.UsedStorageCapacity))
	fmt.Printf("Utilization:    %s %.1f%%\n", usageBar, utilization)
	fmt.Println()
}

func outputFiles(status *protocol.GetStatusResponse, flags *StatusFlags) {
	if !flags.NoColor {
		fmt.Println(lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("209")).Render("📁 FILE STATISTICS"))
	} else {
		fmt.Println("FILE STATISTICS")
	}

	fmt.Printf("Total Files: %d\n", status.TotalFiles)

	if flags.Verbose {
		// Could add more file statistics here if available
		fmt.Printf("Average File Size: %s\n", formatBytes(status.UsedStorageCapacity/int64(max(status.TotalFiles, 1))))
	}
	fmt.Println()
}

func outputTree(status *protocol.GetStatusResponse, flags *StatusFlags) {
	// This would call the existing tree display functionality
	fmt.Println("File tree display would go here (requires additional RPC call)")
	fmt.Println()
}

func outputNetwork(status *protocol.GetStatusResponse, flags *StatusFlags) {
	if !flags.NoColor {
		fmt.Println(lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("210")).Render("🌐 NETWORK INFORMATION"))
	} else {
		fmt.Println("NETWORK INFORMATION")
	}

	fmt.Printf("Coordinator: %s\n", flags.Coordinator)
	fmt.Printf("Connected Peers: %d\n", len(status.Peers))
	fmt.Printf("Total Nodes: %d (Local: %d, Remote: %d)\n",
		len(status.LocalNodes)+len(status.RemoteNodes),
		len(status.LocalNodes),
		len(status.RemoteNodes))
	fmt.Println()
}

// Helper functions

type filteredNodes struct {
	local  []*protocol.NodeInfo
	remote []*protocol.NodeInfo
}

func filterNodes(status *protocol.GetStatusResponse, flags *StatusFlags) filteredNodes {
	result := filteredNodes{
		local:  make([]*protocol.NodeInfo, 0),
		remote: make([]*protocol.NodeInfo, 0),
	}

	// Filter local nodes
	for _, node := range status.LocalNodes {
		if flags.HealthyOnly && !node.IsHealthy {
			continue
		}
		if flags.NodeFilter != "" && !strings.Contains(node.MemberId, flags.NodeFilter) {
			continue
		}
		result.local = append(result.local, node)
	}

	// Filter remote nodes
	for _, node := range status.RemoteNodes {
		if flags.HealthyOnly && !node.IsHealthy {
			continue
		}
		if flags.NodeFilter != "" && !strings.Contains(node.MemberId, flags.NodeFilter) {
			continue
		}
		result.remote = append(result.remote, node)
	}

	return result
}

func filterPeers(peers []*protocol.PeerInfo, flags *StatusFlags) []*protocol.PeerInfo {
	if !flags.HealthyOnly {
		return peers
	}

	filtered := make([]*protocol.PeerInfo, 0)
	for _, peer := range peers {
		if peer.IsHealthy {
			filtered = append(filtered, peer)
		}
	}
	return filtered
}

func createUsageBar(percentage float64, width int) string {
	filled := int(percentage * float64(width) / 100)
	if filled > width {
		filled = width
	}

	bar := strings.Repeat("▪", filled) + strings.Repeat("·", width-filled)

	// Color based on usage
	if percentage > 80 {
		return lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Render(bar) // Red
	} else if percentage > 60 {
		return lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Render(bar) // Orange
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color("42")).Render(bar) // Green
}

func handleConnectionError(err error, flags *StatusFlags) error {
	if flags.JSON {
		output := map[string]interface{}{
			"error":     true,
			"message":   "Connection failed",
			"details":   err.Error(),
			"timestamp": time.Now().Format(time.RFC3339),
		}
		jsonBytes, _ := json.MarshalIndent(output, "", "  ")
		fmt.Println(string(jsonBytes))
		return nil
	}

	fmt.Printf("❌ Connection failed: %v\n", err)
	return err
}

func handleStatusError(err error, flags *StatusFlags) error {
	if flags.JSON {
		output := map[string]interface{}{
			"error":     true,
			"message":   "Failed to get status",
			"details":   err.Error(),
			"timestamp": time.Now().Format(time.RFC3339),
		}
		jsonBytes, _ := json.MarshalIndent(output, "", "  ")
		fmt.Println(string(jsonBytes))
		return nil
	}

	fmt.Printf("❌ Failed to get status: %v\n", err)
	return err
}

func max(a, b int32) int32 {
	if a > b {
		return a
	}
	return b
}
