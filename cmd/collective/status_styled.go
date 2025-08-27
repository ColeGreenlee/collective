package main

import (
	"fmt"
	"strings"
	"time"

	"collective/pkg/protocol"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/lipgloss/table"
)

// Style definitions
var (
	// Color palette - Modern and vibrant
	primaryColor   = lipgloss.Color("#FF79C6") // Pink
	secondaryColor = lipgloss.Color("#8BE9FD") // Cyan
	accentColor    = lipgloss.Color("#50FA7B") // Green
	warningColor   = lipgloss.Color("#FFB86C") // Orange
	dangerColor    = lipgloss.Color("#FF5555") // Red
	mutedColor     = lipgloss.Color("#6272A4") // Comment
	bgColor        = lipgloss.Color("#282A36") // Background
	bgLightColor   = lipgloss.Color("#44475A") // Current Line
	fgColor        = lipgloss.Color("#F8F8F2") // Foreground

	// Base styles
	panelStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(primaryColor).
			Padding(1, 2).
			MarginBottom(1)

	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(primaryColor).
			MarginBottom(1)

	subtitleStyle = lipgloss.NewStyle().
			Foreground(secondaryColor).
			Italic(true)

	mutedStyle = lipgloss.NewStyle().
			Foreground(mutedColor)

	labelStyle = lipgloss.NewStyle().
			Foreground(mutedColor).
			Width(20)

	valueStyle = lipgloss.NewStyle().
			Foreground(fgColor).
			Bold(true)

	accentValueStyle = lipgloss.NewStyle().
				Foreground(accentColor).
				Bold(true)

	warningValueStyle = lipgloss.NewStyle().
				Foreground(warningColor).
				Bold(true)

	dangerValueStyle = lipgloss.NewStyle().
				Foreground(dangerColor).
				Bold(true)

	headerStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(secondaryColor).
			Background(bgLightColor).
			Padding(0, 1)

	rowStyle = lipgloss.NewStyle().
			Padding(0, 1)

	// Icon styles
	iconStyle = lipgloss.NewStyle().
			Foreground(secondaryColor).
			Bold(true).
			MarginRight(1)
)

// Panel creates a styled panel with title and content
func createPanel(title, icon, content string, width int) string {
	// Dynamic panel style based on width
	panel := panelStyle.Copy()
	if width > 0 {
		panel = panel.Width(width)
	}

	// Title with icon
	titleLine := iconStyle.Render(icon) + titleStyle.Render(title)

	// Combine title and content
	fullContent := lipgloss.JoinVertical(lipgloss.Left, titleLine, content)

	return panel.Render(fullContent)
}

// Styled output functions
func styledOutputSummary(status *protocol.GetStatusResponse, flags *StatusFlags) {
	// Calculate metrics
	totalNodes := len(status.LocalNodes) + len(status.RemoteNodes)
	totalPeers := len(status.Peers)
	utilization := float64(status.UsedStorageCapacity) * 100 / float64(max64(status.TotalStorageCapacity, 1))

	// Build content
	var content strings.Builder

	// Create a grid layout
	metrics := []struct {
		label string
		value string
		style lipgloss.Style
	}{
		{"Coordinator ID", status.MemberId, accentValueStyle},
		{"Connected Peers", fmt.Sprintf("%d", totalPeers), valueStyle},
		{"Active Nodes", fmt.Sprintf("%d", totalNodes), getNodeCountStyle(totalNodes)},
		{"Total Capacity", formatBytes(status.TotalStorageCapacity), valueStyle},
		{"Used Storage", formatBytes(status.UsedStorageCapacity), valueStyle},
		{"Available", formatBytes(status.TotalStorageCapacity - status.UsedStorageCapacity), accentValueStyle},
		{"Utilization", fmt.Sprintf("%.1f%%", utilization), getUtilizationStyle(utilization)},
		{"Total Files", fmt.Sprintf("%d", status.TotalFiles), valueStyle},
	}

	for _, m := range metrics {
		line := fmt.Sprintf("%s %s",
			labelStyle.Render(m.label+":"),
			m.style.Render(m.value))
		content.WriteString(line + "\n")
	}

	// Add visual utilization bar
	content.WriteString("\n")
	content.WriteString(labelStyle.Render("Storage Usage:") + "\n")
	content.WriteString(createStyledProgressBar(utilization, 40))

	panel := createPanel("COLLECTIVE OVERVIEW", "🌐", strings.TrimSpace(content.String()), 60)
	fmt.Println(panel)
}

func styledOutputNodes(status *protocol.GetStatusResponse, flags *StatusFlags) {
	nodes := filterNodes(status, flags)

	if len(nodes.local) == 0 && len(nodes.remote) == 0 {
		return
	}

	// Create table with custom styling
	t := table.New().
		Border(lipgloss.NormalBorder()).
		BorderStyle(lipgloss.NewStyle().Foreground(bgLightColor)).
		StyleFunc(func(row, col int) lipgloss.Style {
			if row == 0 {
				return headerStyle
			}
			if col == 3 { // Status column
				return rowStyle
			}
			return rowStyle.Copy().Foreground(fgColor)
		})

	t.Headers("NODE ID", "MEMBER", "ADDRESS", "STATUS", "CAPACITY", "USED", "USAGE")

	// Process all nodes
	allNodes := append(nodes.local, nodes.remote...)
	for _, node := range allNodes {
		statusIcon := "🟢"
		statusText := "HEALTHY"
		statusColor := accentColor

		if !node.IsHealthy {
			statusIcon = "🔴"
			statusText = "UNHEALTHY"
			statusColor = dangerColor
		}

		usage := float64(node.UsedCapacity) * 100 / float64(max64(node.TotalCapacity, 1))

		t.Row(
			node.NodeId,
			node.MemberId,
			node.Address,
			fmt.Sprintf("%s %s", statusIcon, lipgloss.NewStyle().Foreground(statusColor).Render(statusText)),
			formatBytes(node.TotalCapacity),
			formatBytes(node.UsedCapacity),
			createMiniProgressBar(usage, 15),
		)
	}

	panel := createPanel("STORAGE NODES", "💾", t.Render(), 0)
	fmt.Println(panel)
}

func styledOutputPeers(status *protocol.GetStatusResponse, flags *StatusFlags) {
	peers := filterPeers(status.Peers, flags)

	// Build content with current coordinator info
	var content strings.Builder

	// Add current coordinator information
	content.WriteString(labelStyle.Render("Connected To:") + " " +
		accentValueStyle.Render(flags.Coordinator) + "\n")
	content.WriteString(labelStyle.Render("Coordinator ID:") + " " +
		valueStyle.Render(status.MemberId) + "\n")
	content.WriteString(labelStyle.Render("Peer Count:") + " " +
		valueStyle.Render(fmt.Sprintf("%d", len(peers))) + "\n")

	if len(peers) == 0 {
		content.WriteString("\n" + mutedStyle.Render("No peers connected"))
		panel := createPanel("COORDINATOR PEERS", "📡", strings.TrimSpace(content.String()), 0)
		fmt.Println(panel)
		return
	}

	content.WriteString("\n")

	t := table.New().
		Border(lipgloss.NormalBorder()).
		BorderStyle(lipgloss.NewStyle().Foreground(bgLightColor)).
		StyleFunc(func(row, col int) lipgloss.Style {
			if row == 0 {
				return headerStyle
			}
			return rowStyle.Copy().Foreground(fgColor)
		})

	t.Headers("PEER ID", "ADDRESS", "STATUS", "LAST SEEN")

	for _, peer := range peers {
		statusIcon := "🟢"
		statusText := "CONNECTED"
		statusColor := accentColor

		if !peer.IsHealthy {
			statusIcon = "🔴"
			statusText = "DISCONNECTED"
			statusColor = dangerColor
		}

		// Format last seen time
		lastSeenText := "Never"
		if peer.LastSeen > 0 {
			lastSeenTime := time.Unix(peer.LastSeen, 0)
			elapsed := time.Since(lastSeenTime)
			if elapsed < time.Minute {
				lastSeenText = fmt.Sprintf("%ds ago", int(elapsed.Seconds()))
			} else if elapsed < time.Hour {
				lastSeenText = fmt.Sprintf("%dm ago", int(elapsed.Minutes()))
			} else if elapsed < 24*time.Hour {
				lastSeenText = fmt.Sprintf("%dh ago", int(elapsed.Hours()))
			} else {
				lastSeenText = fmt.Sprintf("%dd ago", int(elapsed.Hours()/24))
			}
		}

		t.Row(
			peer.MemberId,
			peer.Address,
			fmt.Sprintf("%s %s", statusIcon, lipgloss.NewStyle().Foreground(statusColor).Render(statusText)),
			lastSeenText,
		)
	}

	content.WriteString(t.Render())
	panel := createPanel("COORDINATOR PEERS", "📡", strings.TrimSpace(content.String()), 0)
	fmt.Println(panel)
}

func styledOutputStorage(status *protocol.GetStatusResponse, flags *StatusFlags) {
	utilization := float64(status.UsedStorageCapacity) * 100 / float64(max64(status.TotalStorageCapacity, 1))

	var content strings.Builder

	// Storage metrics with visual hierarchy
	metrics := []struct {
		label string
		value string
		bar   bool
	}{
		{"Total Capacity", formatBytes(status.TotalStorageCapacity), false},
		{"Used Storage", formatBytes(status.UsedStorageCapacity), false},
		{"Available", formatBytes(status.TotalStorageCapacity - status.UsedStorageCapacity), false},
		{"", "", true}, // Progress bar
		{"Utilization", fmt.Sprintf("%.1f%%", utilization), false},
	}

	for _, m := range metrics {
		if m.bar {
			content.WriteString("\n" + createStyledProgressBar(utilization, 45) + "\n")
		} else if m.label != "" {
			style := valueStyle
			if strings.Contains(m.label, "Available") {
				style = accentValueStyle
			} else if strings.Contains(m.label, "Utilization") {
				style = getUtilizationStyle(utilization)
			}

			line := fmt.Sprintf("%s %s",
				labelStyle.Render(m.label+":"),
				style.Render(m.value))
			content.WriteString(line + "\n")
		}
	}

	// Add storage distribution if verbose
	if flags.Verbose {
		content.WriteString("\n" + subtitleStyle.Render("Storage Distribution") + "\n")

		// Show top nodes by usage
		var topNodes []struct {
			id    string
			usage float64
		}

		for _, node := range status.LocalNodes {
			usage := float64(node.UsedCapacity) * 100 / float64(max64(node.TotalCapacity, 1))
			topNodes = append(topNodes, struct {
				id    string
				usage float64
			}{node.NodeId, usage})
		}

		// Simple visualization
		for _, node := range topNodes[:minInt(5, len(topNodes))] {
			content.WriteString(fmt.Sprintf("  %s %s %.1f%%\n",
				mutedStyle.Render(node.id),
				createMiniProgressBar(node.usage, 10),
				node.usage))
		}
	}

	panel := createPanel("STORAGE METRICS", "📊", strings.TrimSpace(content.String()), 60)
	fmt.Println(panel)
}

func styledOutputFiles(status *protocol.GetStatusResponse, flags *StatusFlags) {
	var content strings.Builder

	fileCount := status.TotalFiles
	avgSize := int64(0)
	if fileCount > 0 {
		avgSize = status.UsedStorageCapacity / int64(fileCount)
	}

	metrics := []struct {
		label string
		value string
	}{
		{"Total Files", fmt.Sprintf("%d", fileCount)},
		{"Average Size", formatBytes(avgSize)},
	}

	for _, m := range metrics {
		line := fmt.Sprintf("%s %s",
			labelStyle.Render(m.label+":"),
			valueStyle.Render(m.value))
		content.WriteString(line + "\n")
	}

	// Add file type distribution if we had that data
	if flags.Verbose {
		content.WriteString("\n" + subtitleStyle.Render("Recent Activity") + "\n")
		content.WriteString(mutedStyle.Render("  No recent file operations") + "\n")
	}

	panel := createPanel("FILE STATISTICS", "📁", strings.TrimSpace(content.String()), 60)
	fmt.Println(panel)
}

func styledOutputNetwork(status *protocol.GetStatusResponse, flags *StatusFlags) {
	var content strings.Builder

	totalNodes := len(status.LocalNodes) + len(status.RemoteNodes)
	healthyNodes := 0
	for _, node := range append(status.LocalNodes, status.RemoteNodes...) {
		if node.IsHealthy {
			healthyNodes++
		}
	}

	healthyPeers := 0
	for _, peer := range status.Peers {
		if peer.IsHealthy {
			healthyPeers++
		}
	}

	// Connection status
	connectionStatus := "CONNECTED"
	connectionStyle := accentValueStyle
	if healthyPeers == 0 && len(status.Peers) == 0 {
		connectionStatus = "STANDALONE"
		connectionStyle = warningValueStyle
	}

	metrics := []struct {
		label string
		value string
		style lipgloss.Style
	}{
		{"Endpoint", flags.Coordinator, valueStyle},
		{"Local Coordinator", status.MemberId, accentValueStyle},
		{"Connection Status", connectionStatus, connectionStyle},
		{"Network Status", getNetworkStatus(healthyPeers, len(status.Peers)), getNetworkStatusStyle(healthyPeers, len(status.Peers))},
		{"Connected Peers", fmt.Sprintf("%d/%d", healthyPeers, len(status.Peers)), getHealthStyle(healthyPeers, len(status.Peers))},
		{"Active Nodes", fmt.Sprintf("%d/%d", healthyNodes, totalNodes), getHealthStyle(healthyNodes, totalNodes)},
		{"Network Health", fmt.Sprintf("%.1f%%", float64(healthyNodes)*100/float64(maxInt(totalNodes, 1))), getHealthPercentStyle(float64(healthyNodes) * 100 / float64(maxInt(totalNodes, 1)))},
	}

	for _, m := range metrics {
		line := fmt.Sprintf("%s %s",
			labelStyle.Render(m.label+":"),
			m.style.Render(m.value))
		content.WriteString(line + "\n")
	}

	// Add network topology visualization if verbose
	if flags.Verbose {
		content.WriteString("\n" + subtitleStyle.Render("Network Topology") + "\n")
		content.WriteString(createNetworkVisualization(status))
	}

	panel := createPanel("NETWORK STATUS", "🌐", strings.TrimSpace(content.String()), 60)
	fmt.Println(panel)
}

// Helper functions for styling

func createStyledProgressBar(percentage float64, width int) string {
	filled := int(percentage * float64(width) / 100)
	if filled > width {
		filled = width
	}
	if filled < 0 {
		filled = 0
	}

	// Create gradient effect with different characters
	var bar strings.Builder

	// Different fill characters for visual interest
	fillChars := []string{"█", "▓", "▒", "░"}
	emptyChar := "·"

	for i := 0; i < width; i++ {
		if i < filled {
			// Choose fill character based on position for gradient effect
			charIndex := 0
			if i > filled-3 && filled < width {
				charIndex = i - (filled - 3)
				if charIndex >= len(fillChars) {
					charIndex = 0
				}
			}

			color := getProgressBarColor(percentage)
			bar.WriteString(lipgloss.NewStyle().Foreground(color).Render(fillChars[charIndex]))
		} else {
			bar.WriteString(mutedStyle.Render(emptyChar))
		}
	}

	return bar.String()
}

func createMiniProgressBar(percentage float64, width int) string {
	filled := int(percentage * float64(width) / 100)
	if filled > width {
		filled = width
	}
	if filled < 0 {
		filled = 0
	}

	bar := strings.Repeat("▪", filled) + strings.Repeat("·", width-filled)

	color := getProgressBarColor(percentage)
	filledPart := lipgloss.NewStyle().Foreground(color).Render(bar[:filled])
	emptyPart := mutedStyle.Render(bar[filled:])

	return fmt.Sprintf("%s%s %.1f%%", filledPart, emptyPart, percentage)
}

func getProgressBarColor(percentage float64) lipgloss.Color {
	if percentage > 80 {
		return dangerColor
	} else if percentage > 60 {
		return warningColor
	}
	return accentColor
}

func getUtilizationStyle(utilization float64) lipgloss.Style {
	if utilization > 80 {
		return dangerValueStyle
	} else if utilization > 60 {
		return warningValueStyle
	}
	return accentValueStyle
}

func getNodeCountStyle(count int) lipgloss.Style {
	if count == 0 {
		return dangerValueStyle
	} else if count < 5 {
		return warningValueStyle
	}
	return accentValueStyle
}

func getHealthStyle(healthy, total int) lipgloss.Style {
	if healthy == total {
		return accentValueStyle
	} else if healthy > total/2 {
		return warningValueStyle
	}
	return dangerValueStyle
}

func getHealthPercentStyle(percentage float64) lipgloss.Style {
	if percentage >= 90 {
		return accentValueStyle
	} else if percentage >= 70 {
		return warningValueStyle
	}
	return dangerValueStyle
}

func getNetworkStatus(healthyPeers, totalPeers int) string {
	if healthyPeers == totalPeers && totalPeers > 0 {
		return "FULLY CONNECTED"
	} else if healthyPeers > totalPeers/2 {
		return "PARTIALLY CONNECTED"
	} else if healthyPeers > 0 {
		return "LIMITED CONNECTIVITY"
	}
	return "DISCONNECTED"
}

func getNetworkStatusStyle(healthyPeers, totalPeers int) lipgloss.Style {
	if healthyPeers == totalPeers && totalPeers > 0 {
		return accentValueStyle
	} else if healthyPeers > totalPeers/2 {
		return warningValueStyle
	}
	return dangerValueStyle
}

func createNetworkVisualization(status *protocol.GetStatusResponse) string {
	var viz strings.Builder

	// Simple ASCII network diagram
	viz.WriteString(accentValueStyle.Render("  ┌─[") + status.MemberId + accentValueStyle.Render("]─┐") + "\n")

	// Show peers
	for i, peer := range status.Peers {
		if i == len(status.Peers)-1 {
			viz.WriteString(accentValueStyle.Render("  └──") + getPeerSymbol(peer) + " " + peer.MemberId + "\n")
		} else {
			viz.WriteString(accentValueStyle.Render("  ├──") + getPeerSymbol(peer) + " " + peer.MemberId + "\n")
		}
	}

	return viz.String()
}

func getPeerSymbol(peer *protocol.PeerInfo) string {
	if peer.IsHealthy {
		return accentValueStyle.Render("●")
	}
	return dangerValueStyle.Render("○")
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
