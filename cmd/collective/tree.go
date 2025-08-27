package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"collective/pkg/protocol"
	"github.com/charmbracelet/lipgloss"
)

// TreeNode represents a node in the file tree
type TreeNode struct {
	Name     string
	IsDir    bool
	Size     int64
	Modified time.Time
	Children []*TreeNode
	Path     string
}

// buildFileTree builds a tree structure from the collective's files
func buildFileTree(client protocol.CoordinatorClient, ctx context.Context) (*TreeNode, error) {
	// Start with root
	root := &TreeNode{
		Name:     "/",
		IsDir:    true,
		Path:     "/",
		Children: []*TreeNode{},
	}

	// Get root directory listing
	if err := populateTreeNode(client, ctx, root); err != nil {
		return nil, err
	}

	return root, nil
}

// populateTreeNode recursively populates a tree node with its children
func populateTreeNode(client protocol.CoordinatorClient, ctx context.Context, node *TreeNode) error {
	if !node.IsDir {
		return nil
	}

	// List directory contents
	resp, err := client.ListDirectory(ctx, &protocol.ListDirectoryRequest{
		Path: node.Path,
	})
	if err != nil {
		return err
	}

	for _, entry := range resp.Entries {
		childPath := node.Path
		if !strings.HasSuffix(childPath, "/") {
			childPath += "/"
		}
		if childPath == "//" {
			childPath = "/"
		}
		childPath += entry.Name

		child := &TreeNode{
			Name:     entry.Name,
			IsDir:    entry.IsDirectory,
			Size:     entry.Size,
			Modified: time.Unix(entry.ModifiedTime, 0),
			Path:     childPath,
			Children: []*TreeNode{},
		}

		// Recursively populate subdirectories (limit depth to avoid too deep recursion)
		if entry.IsDirectory && strings.Count(childPath, "/") < 5 {
			populateTreeNode(client, ctx, child)
		}

		node.Children = append(node.Children, child)
	}

	// Sort children: directories first, then by name
	sort.Slice(node.Children, func(i, j int) bool {
		if node.Children[i].IsDir != node.Children[j].IsDir {
			return node.Children[i].IsDir
		}
		return node.Children[i].Name < node.Children[j].Name
	})

	return nil
}

// renderTree renders a tree node as a string
func renderTree(node *TreeNode, prefix string, isLast bool) string {
	var result strings.Builder

	if node.Name != "/" { // Don't show root marker
		// Tree characters
		if isLast {
			result.WriteString(prefix + "└── ")
		} else {
			result.WriteString(prefix + "├── ")
		}

		// Node icon and name
		icon := "📄"
		if node.IsDir {
			icon = "📁"
		}

		// Style based on type
		nameStyle := lipgloss.NewStyle()
		if node.IsDir {
			nameStyle = nameStyle.Bold(true).Foreground(lipgloss.Color("#7571f9"))
		} else {
			nameStyle = nameStyle.Foreground(lipgloss.Color("#ffffff"))
		}

		result.WriteString(fmt.Sprintf("%s %s", icon, nameStyle.Render(node.Name)))

		// Add size for files
		if !node.IsDir && node.Size > 0 {
			sizeStr := formatSize(node.Size)
			sizeStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#6c757d"))
			result.WriteString(sizeStyle.Render(fmt.Sprintf(" (%s)", sizeStr)))
		}

		result.WriteString("\n")
	}

	// Render children
	childPrefix := prefix
	if node.Name != "/" {
		if isLast {
			childPrefix += "    "
		} else {
			childPrefix += "│   "
		}
	}

	for i, child := range node.Children {
		isLastChild := i == len(node.Children)-1
		result.WriteString(renderTree(child, childPrefix, isLastChild))
	}

	return result.String()
}

// createDataTreePanel creates a panel showing the collective's data in tree form
func createDataTreePanel(client protocol.CoordinatorClient, ctx context.Context) string {
	// Style definitions
	var (
		primaryColor = lipgloss.Color("#7571f9")
		mutedColor   = lipgloss.Color("#6c757d")

		panelStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(primaryColor).
				Padding(1).
				MarginBottom(1)

		headerStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(primaryColor).
				Underline(true).
				MarginBottom(1)

		emptyStyle = lipgloss.NewStyle().
				Foreground(mutedColor).
				Italic(true)
	)

	// Build the tree
	tree, err := buildFileTree(client, ctx)
	if err != nil {
		// If error, still show panel with error message
		errorContent := headerStyle.Render("📂 COLLECTIVE DATA") + "\n\n" +
			emptyStyle.Render("Unable to fetch data tree: "+err.Error())
		return panelStyle.Render(errorContent)
	}

	// Check if tree is empty
	if len(tree.Children) == 0 {
		emptyContent := headerStyle.Render("📂 COLLECTIVE DATA") + "\n\n" +
			emptyStyle.Render("No files or directories in collective")
		return panelStyle.Render(emptyContent)
	}

	// Render the tree
	treeContent := renderTree(tree, "", false)

	// Calculate statistics
	stats := calculateTreeStats(tree)
	statsLine := lipgloss.NewStyle().
		Foreground(mutedColor).
		MarginTop(1).
		Render(fmt.Sprintf("\n%d directories, %d files, %s total",
			stats.Dirs, stats.Files, formatSize(stats.TotalSize)))

	// Create the panel
	content := headerStyle.Render("📂 COLLECTIVE DATA") + "\n\n" +
		treeContent + statsLine

	return panelStyle.Render(content)
}

// TreeStats holds statistics about the tree
type TreeStats struct {
	Files     int
	Dirs      int
	TotalSize int64
}

// calculateTreeStats calculates statistics for a tree
func calculateTreeStats(node *TreeNode) TreeStats {
	stats := TreeStats{}

	if node.IsDir {
		if node.Name != "/" {
			stats.Dirs++
		}
		for _, child := range node.Children {
			childStats := calculateTreeStats(child)
			stats.Files += childStats.Files
			stats.Dirs += childStats.Dirs
			stats.TotalSize += childStats.TotalSize
		}
	} else {
		stats.Files++
		stats.TotalSize += node.Size
	}

	return stats
}

// formatSize formats a size in bytes to human readable format
func formatSize(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}
