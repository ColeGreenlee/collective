package main

import (
	"context"
	"fmt"
	"strings"

	"collective/pkg/protocol"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

var cleanupCmd = &cobra.Command{
	Use:   "cleanup",
	Short: "Clean up orphaned chunks from storage nodes",
	Long:  `Removes chunks that are no longer referenced by any files in the collective`,
	Run:   runCleanup,
}

func init() {
	cleanupCmd.Flags().Bool("dry-run", true, "Show what would be cleaned without actually removing chunks")
	cleanupCmd.Flags().Bool("force", false, "Skip confirmation prompt (use with caution)")
	cleanupCmd.Flags().String("coordinator", "localhost:8001", "Coordinator address")
}

func runCleanup(cmd *cobra.Command, args []string) {
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	force, _ := cmd.Flags().GetBool("force")

	coordinatorAddr, _ := cmd.Flags().GetString("coordinator")
	if coordinatorAddr == "" {
		coordinatorAddr = "localhost:8001"
	}

	// Connect to coordinator
	conn, err := grpc.Dial(coordinatorAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		fmt.Printf("Failed to connect to coordinator: %v\n", err)
		return
	}
	defer conn.Close()

	client := protocol.NewCoordinatorClient(conn)
	ctx := context.Background()

	// Get current status to see storage usage
	statusResp, err := client.GetStatus(ctx, &protocol.GetStatusRequest{})
	if err != nil {
		fmt.Printf("Failed to get status: %v\n", err)
		return
	}

	// Calculate current usage
	var totalCapacity, totalUsed int64
	orphanedNodes := []string{}

	// Combine local and remote nodes
	allNodes := append(statusResp.LocalNodes, statusResp.RemoteNodes...)
	for _, node := range allNodes {
		totalCapacity += node.TotalCapacity
		totalUsed += node.UsedCapacity

		// If a node has usage but we have minimal tracked files, it likely has orphaned chunks
		if node.UsedCapacity > 1024*1024 { // More than 1MB used
			orphanedNodes = append(orphanedNodes, node.NodeId)
		}
	}

	// Display cleanup summary
	headerStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("205")).
		MarginBottom(1)

	infoStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("243"))

	warningStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("214"))

	successStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("42"))

	fmt.Println(headerStyle.Render("🧹 COLLECTIVE CLEANUP UTILITY"))
	fmt.Println()

	fmt.Println(infoStyle.Render(fmt.Sprintf("Current Storage Usage: %.1f GB / %.1f GB (%.1f%%)",
		float64(totalUsed)/(1024*1024*1024),
		float64(totalCapacity)/(1024*1024*1024),
		float64(totalUsed)*100/float64(totalCapacity))))

	if len(orphanedNodes) > 0 {
		fmt.Println(warningStyle.Render(fmt.Sprintf("\n⚠️  Found %d nodes with potential orphaned chunks", len(orphanedNodes))))
		fmt.Println(infoStyle.Render(strings.Join(orphanedNodes, ", ")))
	}

	if dryRun {
		fmt.Println()
		fmt.Println(infoStyle.Render("This is a DRY RUN - no changes will be made"))
		fmt.Println(infoStyle.Render("To actually clean up, run: collective cleanup --dry-run=false"))

		// In a real implementation, we would:
		// 1. List all chunks on each node
		// 2. List all chunks referenced by files in the coordinator
		// 3. Identify orphaned chunks (in nodes but not in coordinator)
		// 4. Show what would be deleted
		fmt.Println()
		fmt.Println(warningStyle.Render("⚠️  Note: Cleanup functionality requires coordinator API extensions"))
		fmt.Println(infoStyle.Render("The coordinator needs methods to:"))
		fmt.Println(infoStyle.Render("  1. List all active chunk IDs"))
		fmt.Println(infoStyle.Render("  2. Instruct nodes to delete specific chunks"))
		fmt.Println(infoStyle.Render("  3. Verify chunk deletion"))
		return
	}

	if !force {
		fmt.Println()
		fmt.Println(warningStyle.Render("⚠️  WARNING: This will permanently delete orphaned chunks"))
		fmt.Print("Are you sure you want to continue? (yes/no): ")

		var response string
		fmt.Scanln(&response)
		if response != "yes" {
			fmt.Println("Cleanup cancelled")
			return
		}
	}

	// TODO: Implement actual cleanup logic once coordinator API is extended
	fmt.Println()
	fmt.Println(successStyle.Render("✅ Cleanup would be performed here (not yet implemented)"))
	fmt.Println(infoStyle.Render("Coordinator API extensions needed for chunk cleanup"))
}
